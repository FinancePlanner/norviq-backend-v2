package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var imagesOn = map[string]string{"ASSISTANT_IMAGES_ENABLED": "true"}

func (th *turnHarness) makePro(user uuid.UUID) {
	th.T.Helper()
	th.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
}

// proConversation is a Pro user with an empty conversation.
func (th *turnHarness) proConversation(email string) (user uuid.UUID, conv string) {
	th.T.Helper()
	user = th.CreateUser(email)
	th.makePro(user)
	return user, th.conversation(user)
}

// DoMultipart posts a `content` field and one `image` file part per image,
// each declared as x.png so nothing can rely on the declared type.
func (th *turnHarness) DoMultipart(path, token, content string, images ...[]byte) *testutil.Response {
	th.T.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("content", content); err != nil {
		th.T.Fatal(err)
	}
	for _, img := range images {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="image"; filename="x.png"`)
		h.Set("Content-Type", "image/png")
		part, err := w.CreatePart(h)
		if err != nil {
			th.T.Fatal(err)
		}
		if _, err := part.Write(img); err != nil {
			th.T.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		th.T.Fatal(err)
	}
	return th.doRaw(path, token, w.FormDataContentType(), body.Bytes())
}

func (th *turnHarness) doRaw(path, token, contentType string, body []byte) *testutil.Response {
	th.T.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, th.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		th.T.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := th.Server.Client().Do(req)
	if err != nil {
		th.T.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		th.T.Fatal(err)
	}
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

// storedMessages decrypts a conversation's ai_messages, oldest first.
func (th *turnHarness) storedMessages(conv string) []string {
	th.T.Helper()
	rows, err := th.Deps.DB.Query(context.Background(),
		`SELECT content_encrypted FROM ai_messages WHERE conversation_id = $1 ORDER BY created_at`, uuid.MustParse(conv))
	if err != nil {
		th.T.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var enc []byte
		if err := rows.Scan(&enc); err != nil {
			th.T.Fatal(err)
		}
		plain, err := th.h.pii.DecryptString(enc)
		if err != nil {
			th.T.Fatal(err)
		}
		out = append(out, plain)
	}
	if err := rows.Err(); err != nil {
		th.T.Fatal(err)
	}
	return out
}

// noTurnSpent: nothing reached the model or was stored for the user.
func (th *turnHarness) noTurnSpent(user uuid.UUID) {
	th.T.Helper()
	if n := th.client.calls(); n != 0 {
		th.T.Fatalf("model called %d times", n)
	}
	if n := th.count(`SELECT count(*) FROM ai_usage_monthly WHERE user_id = $1`, user); n != 0 {
		th.T.Fatalf("usage rows = %d", n)
	}
	if n := th.count(`SELECT count(*) FROM ai_messages WHERE user_id = $1`, user); n != 0 {
		th.T.Fatalf("messages = %d", n)
	}
}

func fakePNG(size int) []byte {
	magic := []byte("\x89PNG\r\n\x1a\n")
	return append(magic, bytes.Repeat([]byte{'i'}, size-len(magic))...)
}

func toolNames(tools []ai.Tool) map[string]int {
	out := map[string]int{}
	for i := range tools {
		out[tools[i].Function.Name]++
	}
	return out
}

func lastMessage(messages []ai.Message) *ai.Message { return &messages[len(messages)-1] }

func TestImageTurnPassesImageToModelOnly(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("image@example.com")
	img := fakePNG(4096)
	th.client.replies = []ai.Message{say("A chart.")}

	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "  what is this  ", img)
	if r.Status != 200 {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	seen := th.client.seen[0]
	last := lastMessage(seen)
	if last.Role != "user" || *last.Content != "what is this [image attached]" || len(last.Images) != 1 {
		t.Fatalf("last message = %+v", last)
	}
	if last.Images[0].MediaType != "image/png" || !bytes.Equal(last.Images[0].Data, img) {
		t.Fatalf("image = %s, %d bytes", last.Images[0].MediaType, len(last.Images[0].Data))
	}
	for i := range seen[:len(seen)-1] {
		if len(seen[i].Images) != 0 {
			t.Fatalf("message %d carries images", i)
		}
	}
	if !strings.HasSuffix(*seen[0].Content, "\n\n"+imageTurnPrompt) {
		t.Fatalf("system = %q", *seen[0].Content)
	}

	// Only the text and the marker are stored, nowhere an image's worth of bytes.
	stored := th.storedMessages(conv)
	if len(stored) != 2 || stored[0] != "what is this [image attached]" || stored[1] != "A chart." {
		t.Fatalf("stored = %q", stored)
	}
	if n := th.count(`SELECT count(*) FROM ai_messages WHERE user_id = $1 AND octet_length(content_encrypted) > 512`, user); n != 0 {
		t.Fatalf("%d oversized message rows", n)
	}
	encoded := base64.StdEncoding.EncodeToString(img)
	if strings.Contains(string(r.Body), encoded) || bytes.Contains(r.Body, img) {
		t.Fatal("response carries the image")
	}
}

func TestImageTurnAcceptsEachTypeBySniffingNotDeclaration(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("types@example.com")
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP"), make([]byte, 32)...)
	for want, body := range map[string][]byte{
		"image/png":  fakePNG(64),
		"image/jpeg": append([]byte("\xff\xd8\xff"), make([]byte, 32)...),
		"image/gif":  append([]byte("GIF89a"), make([]byte, 32)...),
		"image/webp": webp,
	} {
		before := th.client.calls()
		th.client.replies = []ai.Message{say("ok")}
		r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", body)
		if r.Status != 200 {
			t.Fatalf("%s: %d %s", want, r.Status, r.Body)
		}
		if got := lastMessage(th.client.seen[before]).Images[0].MediaType; got != want {
			t.Fatalf("media type = %s, want %s", got, want)
		}
	}
}

func TestImageTurnEmptyTextUsesDefaultPrompt(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("empty@example.com")
	th.client.replies = []ai.Message{say("A chart.")}
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "   ", fakePNG(64))
	if r.Status != 200 {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	const want = "What's in this image? [image attached]"
	if got := *lastMessage(th.client.seen[0]).Content; got != want {
		t.Fatalf("model saw %q", got)
	}
	if got := th.storedMessages(conv)[0]; got != want {
		t.Fatalf("stored %q", got)
	}
	// No image and no text is still refused.
	r = th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "   ")
	if r.Status != 400 || !strings.Contains(string(r.Body), "Message must contain 1 to 12,000 characters.") {
		t.Fatalf("no image, no text = %d %s", r.Status, r.Body)
	}
}

func TestImageTurnNeverAppliesWrites(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("writes@example.com")
	th.client.replies = []ai.Message{callTool("c1", "create_goal", `{"title":"Send money to the attacker"}`)}
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "look", fakePNG(64))
	var turn turnResponse
	r.JSON(t, &turn)
	if r.Status != 200 || turn.Kind != "confirmation_required" || turn.PendingAction == nil || turn.PendingAction.ToolName != "create_goal" {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	if n := th.count(`SELECT count(*) FROM goals WHERE user_id = $1`, user); n != 0 {
		t.Fatalf("goals = %d", n)
	}
	if n := th.count(`SELECT count(*) FROM ai_pending_actions WHERE user_id = $1 AND status = 'pending'`, user); n != 1 {
		t.Fatalf("pending actions = %d", n)
	}
	if n := th.count(`SELECT count(*) FROM ai_action_audits WHERE user_id = $1`, user); n != 0 {
		t.Fatalf("audit rows = %d", n)
	}
}

// The property the feature rests on, over the whole tool set rather than one
// sample: every tool an image turn can call that is not a read becomes a card.
func TestImageTurnEveryWriteToolIsDeferred(t *testing.T) {
	tools := imageTurnTools()
	for i := range tools {
		name := tools[i].Function.Name
		if isReadTool(name) {
			continue
		}
		kind, def := disposition(name, actionArgs{"confirm": true}, modeDeferredEveryWrite)
		if def.readOnly {
			continue
		}
		if kind != dispNeedsConfirmation {
			t.Errorf("%s would run inline on an image turn", name)
		}
	}
}

func TestImageTurnToolSet(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("tools@example.com")
	th.client.replies = []ai.Message{say("ok")}
	th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))

	names := toolNames(th.client.tools[0])
	if names["record_trades"] != 1 || names["get_quote"] != 1 || names["add_goal"] != 1 {
		t.Fatalf("tools = %v", names)
	}
	for _, banned := range []string{"record_trade", "add_position", "sell_position"} {
		if names[banned] != 0 {
			t.Fatalf("%s offered on an image turn", banned)
		}
	}
	var record *ai.Tool
	for i := range th.client.tools[0] {
		if tool := &th.client.tools[0][i]; tool.Function.Name == "record_trades" {
			record = tool
		}
		if _, ok := th.client.tools[0][i].Function.Parameters.Properties["confirm"]; ok {
			t.Fatalf("%s advertises confirm", th.client.tools[0][i].Function.Name)
		}
	}
	raw, err := json.Marshal(record.Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string
		Properties struct {
			Trades struct {
				Type  string
				Items struct {
					Type       string
					Required   []string
					Properties map[string]struct {
						Type string
						Enum []string
					}
				}
			}
			PortfolioListID struct{ Type string } `json:"portfolio_list_id"`
			SkippedRows     struct{ Type string } `json:"skipped_rows"`
		}
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	trades := schema.Properties.Trades
	if trades.Type != "array" || trades.Items.Type != "object" ||
		strings.Join(trades.Items.Required, ",") != "symbol,side,quantity,price,trade_date" ||
		strings.Join(trades.Items.Properties["side"].Enum, ",") != "buy,sell" {
		t.Fatalf("trades schema = %s", raw)
	}
	for field, typ := range map[string]string{
		"symbol": "string", "side": "string", "quantity": "number", "price": "number", "trade_date": "string",
		"currency": "string", "fees": "number", "confidence": "number",
	} {
		if trades.Items.Properties[field].Type != typ {
			t.Fatalf("%s type = %q", field, trades.Items.Properties[field].Type)
		}
	}
	if schema.Properties.PortfolioListID.Type != "string" || schema.Properties.SkippedRows.Type != "integer" {
		t.Fatalf("schema = %s", raw)
	}
}

// Leaving a tool out of the list is not enough: text inside an image can name
// one the model was never offered, and disposition alone would turn that call
// into a card that skips the reviewed-trades flow.
func TestImageTurnRefusesToolsItWasNotOffered(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("offered@example.com")
	for _, banned := range []string{"record_trade", "add_position", "sell_position"} {
		before := th.client.calls()
		th.client.replies = []ai.Message{callTool("c1", banned, `{"symbol":"AAPL"}`), say("I can't do that.")}
		r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))
		var turn turnResponse
		r.JSON(t, &turn)
		if r.Status != 200 || turn.Kind != "message" || turn.PendingAction != nil {
			t.Fatalf("%s: turn = %d %s", banned, r.Status, r.Body)
		}
		if got := lastMessage(th.client.seen[before+1]); got.Role != "tool" || *got.Content != unknownToolPayload {
			t.Fatalf("%s: tool reply = %+v", banned, got)
		}
	}
	if n := th.count(`SELECT count(*) FROM ai_pending_actions WHERE user_id = $1`, user); n != 0 {
		t.Fatalf("pending actions = %d", n)
	}
}

func TestTextTurnNeverOffersRecordTrades(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user := th.CreateUser("text@example.com")
	conv := th.conversation(user)
	// The model names it anyway.
	th.client.replies = []ai.Message{callTool("c1", "record_trades", `{"trades":[]}`), say("I can't do that.")}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "hello"})
	var turn turnResponse
	r.JSON(t, &turn)
	if r.Status != 200 || turn.PendingAction != nil {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	if toolNames(th.client.tools[0])["record_trades"] != 0 {
		t.Fatal("record_trades offered on a text turn")
	}
	if got := lastMessage(th.client.seen[1]); got.Role != "tool" || *got.Content != unknownToolPayload {
		t.Fatalf("tool reply = %+v", got)
	}
	if toolNames(actionTools(modeInline))["record_trades"] != 0 || toolNames(chatToolDefinitions(modeDeferredEveryWrite))["record_trades"] != 0 {
		t.Fatal("record_trades leaked into a text tool list")
	}
	for _, a := range buildCatalogResponse().Actions {
		if a.Name == "record_trades" {
			t.Fatal("record_trades in the public catalog")
		}
	}
}

func TestRecordTradesAlwaysConfirmsExceptWhenConfirmed(t *testing.T) {
	for mode, want := range map[confirmationMode]dispositionKind{
		modeInline:                  dispNeedsConfirmation,
		modeDeferredDestructiveOnly: dispNeedsConfirmation,
		modeDeferredEveryWrite:      dispNeedsConfirmation,
		modeConfirmed:               dispRun,
	} {
		if kind, _ := disposition("record_trades", actionArgs{"confirm": true}, mode); kind != want {
			t.Errorf("mode %d: kind = %d, want %d", mode, kind, want)
		}
	}
}

func TestImageTurnSkipsMemoAndWatchClassification(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("classify@example.com")
	for _, content := range []string{"/memo AAPL", "Watch NVDA and tell me when it drops below 100"} {
		before := th.client.calls()
		th.client.replies = []ai.Message{say("ok")}
		r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), content, fakePNG(64))
		var turn turnResponse
		r.JSON(t, &turn)
		if r.Status != 200 || turn.Memo != nil || turn.WatchProposal != nil || turn.PendingAction != nil {
			t.Fatalf("%q: %d %s", content, r.Status, r.Body)
		}
		if got := *lastMessage(th.client.seen[before]).Content; got != content+imageMarker {
			t.Fatalf("model saw %q", got)
		}
	}
}

type countingClient struct{ hits atomic.Int32 }

func (c *countingClient) Chat(context.Context, []ai.Message, []ai.Tool, string) (ai.Message, error) {
	c.hits.Add(1)
	return say("free rung answered"), nil
}

func TestImageTurnFreeRungNotCalled(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("free@example.com")
	p, err := aiplatform.Platform(th.Deps)
	if err != nil {
		t.Fatal(err)
	}
	free := &countingClient{}
	p.Default = &ai.FallbackClient{Logger: silent, Rungs: []ai.Rung{
		{Tier: ai.Tier{Label: "free", Model: "vendor/model:free"}, Client: free},
		{Tier: ai.Tier{Label: "paid", Model: "vendor/model"}, Client: th.client},
	}}
	th.client.replies = []ai.Message{say("paid rung answered")}
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))
	if r.Status != 200 || !strings.Contains(string(r.Body), "paid rung answered") {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	if free.hits.Load() != 0 {
		t.Fatalf("free rung hit %d times", free.hits.Load())
	}
	// The chain itself is untouched: a text turn still leads with the free rung.
	r = th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "hello"})
	if r.Status != 200 || free.hits.Load() != 1 {
		t.Fatalf("text turn = %d %s, hits %d", r.Status, r.Body, free.hits.Load())
	}
}

func TestLaterTurnHasNoImage(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("later@example.com")
	th.client.replies = []ai.Message{say("A chart."), say("You'd need to send it again.")}
	th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "what is this", fakePNG(64))
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "what color was it"})
	if r.Status != 200 {
		t.Fatalf("second = %d %s", r.Status, r.Body)
	}
	second := th.client.seen[1]
	for i := range second {
		if len(second[i].Images) != 0 {
			t.Fatalf("message %d still carries images", i)
		}
	}
	if got := *second[1].Content; got != "what is this [image attached]" {
		t.Fatalf("history = %q", got)
	}
	system := *second[0].Content
	if !strings.Contains(system, "Images from earlier messages are no longer available. If the user asks about one, ask them to send it again rather than guessing.") {
		t.Fatalf("system = %q", system)
	}
	if strings.Contains(system, imageTurnPrompt) {
		t.Fatal("image prompt on a text turn")
	}
}

func TestImageTurnFlagOff(t *testing.T) {
	th := newTurnHarness(t, nil)
	user, conv := th.proConversation("off@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))
	if r.Status != 404 {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
}

// RequirePremium answers every premium gate with a 403 upgrade_required body.
func TestImageTurnNonPremium(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user := th.CreateUser("nonpro@example.com")
	conv := th.conversation(user)
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))
	body := string(r.Body)
	if r.Status != 403 || !strings.Contains(body, `"code":"upgrade_required"`) || !strings.Contains(body, `"feature":"screenshot_import"`) {
		t.Fatalf("status = %d %s", r.Status, body)
	}
	th.noTurnSpent(user)
	r = th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), "x", fakePNG(64))
	frames := parseFrames(t, r.Body)
	if events(frames) != "started,error,done" || !strings.Contains(frames[1].data, `"code":"upgrade_required"`) {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestImageTurnConsentRequired(t *testing.T) {
	th := newTurnHarness(t, map[string]string{"ASSISTANT_IMAGES_ENABLED": "true", "AI_CONSENT_ENFORCED": "true"})
	user, conv := th.proConversation("consent@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))
	if r.Status != 403 || !strings.Contains(string(r.Body), ai.ConsentRequiredReason) {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
}

func TestImageTurnRejectsNonImage(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("pdf@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", []byte("%PDF-1.7 not an image"))
	if r.Status != 400 || !strings.Contains(string(r.Body), "Only JPEG, PNG, WebP and GIF images are supported.") {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
}

func TestImageTurnRejectsFourImages(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("four@example.com")
	img := fakePNG(64)
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", img, img, img, img)
	if r.Status != 400 || !strings.Contains(string(r.Body), "Attach up to 3 images.") {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
	th.client.replies = []ai.Message{say("ok")}
	if r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", img, img, img); r.Status != 200 {
		t.Fatalf("three images = %d %s", r.Status, r.Body)
	}
}

func TestImageTurnRejectsOversizeImage(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("big@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(5<<20+1))
	if r.Status != 413 || !strings.Contains(string(r.Body), "Each image must be 5 MB or smaller.") {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
	th.client.replies = []ai.Message{say("ok")}
	if r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(5<<20)); r.Status != 200 {
		t.Fatalf("exactly 5 MB = %d %s", r.Status, r.Body)
	}
}

// Web only sees frames, so the real reason has to travel in the frame.
func TestImageRejectionStreamFrame(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("frames@example.com")
	img := fakePNG(64)
	path := "/v1/ai/assistant/conversations/" + conv + "/stream"
	for reason, images := range map[string][][]byte{
		"Only JPEG, PNG, WebP and GIF images are supported.": {[]byte("%PDF-1.7 not an image")},
		"Attach up to 3 images.":                             {img, img, img, img},
		"Each image must be 5 MB or smaller.":                {fakePNG(5<<20 + 1)},
	} {
		r := th.DoMultipart(path, th.Token(user), "x", images...)
		frames := parseFrames(t, r.Body)
		if r.Status != 200 || events(frames) != "started,error,done" {
			t.Fatalf("%s: %d %+v", reason, r.Status, frames)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(frames[1].data), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["message"] != reason || payload["code"] != "image_rejected" {
			t.Fatalf("frame = %s, want %q", frames[1].data, reason)
		}
	}
	th.noTurnSpent(user)
}

func TestImageTurnUnreadableForm(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("form@example.com")
	path := "/v1/ai/assistant/conversations/" + conv + "/chat"
	truncated := []byte("--b\r\nContent-Disposition: form-data; name=\"image\"; filename=\"x.png\"\r\n\r\nno closing boundary")
	for contentType, body := range map[string][]byte{
		"multipart/form-data; boundary=b": truncated,
		"multipart/form-data":             truncated,
	} {
		r := th.doRaw(path, th.Token(user), contentType, body)
		if r.Status != 400 || !strings.Contains(string(r.Body), `"code":"image_rejected"`) {
			t.Fatalf("%q: %d %s", contentType, r.Status, r.Body)
		}
	}
	th.noTurnSpent(user)
}

func TestBodyLimitAppliesToParamRoute(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("limit@example.com")
	stream := "/v1/ai/assistant/conversations/" + conv + "/stream"
	chat := "/v1/ai/assistant/conversations/" + conv + "/chat"
	img := fakePNG(4 << 20) // three of these outrun the app-wide 10 MB cap

	th.client.replies = []ai.Message{say("ok"), say("ok")}
	r := th.DoMultipart(stream, th.Token(user), "x", img, img, img)
	if frames := parseFrames(t, r.Body); r.Status != 200 || events(frames) != "started,turn,done" {
		t.Fatalf("12 MB stream = %d %+v", r.Status, frames)
	}
	if got := len(lastMessage(th.client.seen[0]).Images); got != 3 {
		t.Fatalf("images = %d", got)
	}
	if r := th.DoMultipart(chat, th.Token(user), "x", img, img, img); r.Status != 200 {
		t.Fatalf("12 MB chat = %d %s", r.Status, r.Body)
	}

	big := fakePNG(17 << 20)
	for _, path := range []string{stream, chat} {
		if r := th.DoMultipart(path, th.Token(user), "x", big); r.Status != 413 {
			t.Fatalf("17 MB %s = %d %s", path, r.Status, r.Body)
		}
	}
}

func TestJSONTurnsStillWork(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user := th.CreateUser("json@example.com") // not Pro: JSON turns have no image gate
	conv := th.conversation(user)
	th.client.replies = []ai.Message{callTool("c1", "add_goal", `{"title":"Bike"}`), say("Added.")}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "  Hey Q, add a bike goal "})
	var turn turnResponse
	r.JSON(t, &turn)
	if r.Status != 200 || turn.Kind != "message" {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	first := th.client.seen[0]
	if last := lastMessage(first); *last.Content != "add a bike goal" || len(last.Images) != 0 {
		t.Fatalf("last = %+v", last)
	}
	if strings.Contains(*first[0].Content, imageTurnPrompt) {
		t.Fatal("image prompt on a JSON turn")
	}
	// The harmless write still applies inline; only image turns defer it.
	if n := th.count(`SELECT count(*) FROM goals WHERE user_id = $1`, user); n != 1 {
		t.Fatalf("goals = %d", n)
	}
	if got := th.storedMessages(conv)[0]; got != "add a bike goal" {
		t.Fatalf("stored %q", got)
	}
	// A malformed JSON body is still the decoder's error, not an image rejection.
	r = th.doRaw("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "application/json", []byte("{"))
	if r.Status == 200 || strings.Contains(string(r.Body), "image_rejected") {
		t.Fatalf("bad JSON = %d %s", r.Status, r.Body)
	}
}

type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestByokImageRejectionMessage(t *testing.T) {
	// Fallback on: a model that cannot read images must still say so, not
	// quietly hand the user's picture to Norviq's own provider.
	th := newTurnHarness(t, map[string]string{"ASSISTANT_IMAGES_ENABLED": "true", "AI_USER_CREDENTIAL_FALLBACK": "true"})
	user, conv := th.proConversation("byok@example.com")
	var hits atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"message":"this model does not support image input"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(stub.Close)
	target, err := url.Parse(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := aiplatform.Platform(th.Deps)
	if err != nil {
		t.Fatal(err)
	}
	p.HTTP = &http.Client{Transport: rewriteTransport{target: target}}
	key, err := p.Cipher.Encrypt("sk-fake-test-key", ai.CredentialContextAIProvider)
	if err != nil {
		t.Fatal(err)
	}
	th.exec(`INSERT INTO user_ai_provider_credentials (id, user_id, provider, label, api_key_encrypted, key_hint, key_fingerprint, status, created_at, updated_at)
		VALUES ($1, $2, 'openai', 'mine', $3, 'hint', 'fp', 'active', now(), now())`, uuid.New(), user, key)

	const message = "Your openai model can't read images. Try without the image, or change the model in Settings → AI providers."
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), "x", fakePNG(64))
	frames := parseFrames(t, r.Body)
	if r.Status != 200 || events(frames) != "started,error,done" {
		t.Fatalf("stream = %d %+v", r.Status, frames)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(frames[1].data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "user_credential_image_unsupported" || payload["message"] != message {
		t.Fatalf("frame = %s", frames[1].data)
	}

	r = th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(64))
	if r.Status != 424 || !strings.Contains(string(r.Body), message) {
		t.Fatalf("chat = %d %s", r.Status, r.Body)
	}
	if hits.Load() != 2 {
		t.Fatalf("stub hits = %d", hits.Load())
	}
	if th.client.calls() != 0 {
		t.Fatal("the image fell back to Norviq's provider")
	}
	// The key is fine; the model just can't read images.
	if n := th.count(`SELECT count(*) FROM user_ai_provider_credentials WHERE user_id = $1 AND status = 'active' AND last_error_code IS NULL`, user); n != 1 {
		t.Fatal("an image rejection marked the credential as failing")
	}
}

func TestPreferencesReportImagesEnabled(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{{imagesOn, `"imagesEnabled":true`}, {nil, `"imagesEnabled":false`}} {
		th := newTurnHarness(t, tc.env)
		user := th.CreateUser("prefs@example.com")
		r := th.Do("GET", "/v1/ai/assistant/preferences", th.Token(user), nil)
		if r.Status != 200 || !strings.Contains(string(r.Body), tc.want) {
			t.Fatalf("GET = %d %s", r.Status, r.Body)
		}
		r = th.Do("PUT", "/v1/ai/assistant/preferences", th.Token(user),
			map[string]any{"proactiveTipsEnabled": false, "pushEnabled": false, "timezone": "UTC"})
		if r.Status != 200 || !strings.Contains(string(r.Body), tc.want) {
			t.Fatalf("PUT = %d %s", r.Status, r.Body)
		}
	}
}
