package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
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
	contentType, body := th.multipartForm(content, images...)
	return th.doRaw(path, token, contentType, body)
}

func (th *turnHarness) multipartForm(content string, images ...[]byte) (contentType string, form []byte) {
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
	return w.FormDataContentType(), body.Bytes()
}

func (th *turnHarness) doRaw(path, token, contentType string, body []byte, userAgent ...string) *testutil.Response {
	th.T.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, th.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		th.T.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	if len(userAgent) > 0 {
		req.Header.Set("User-Agent", userAgent[0])
	}
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

// pngCore is a real 1x1 PNG, already in the form the attachment gate writes
// back, so what the model receives equals it byte for byte.
var pngCore = func() []byte {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.Set(0, 0, color.NRGBA{R: 200, G: 30, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		panic(err)
	}
	// The gate decodes and re-encodes: make sure that is a fixed point.
	img, err := png.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		panic(err)
	}
	var again bytes.Buffer
	if err := png.Encode(&again, img); err != nil {
		panic(err)
	}
	return again.Bytes()
}()

// fakePNG is pngCore padded with trailing bytes to exactly size, so size
// limits can be tested at their edges. The decoder stops at IEND, and the gate
// drops the padding.
func fakePNG(size int) []byte {
	if size <= len(pngCore) {
		return append([]byte(nil), pngCore...)
	}
	return append(append([]byte(nil), pngCore...), bytes.Repeat([]byte{'i'}, size-len(pngCore))...)
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
	if last.Images[0].MediaType != "image/png" || !bytes.Equal(last.Images[0].Data, pngCore) {
		t.Fatalf("image = %s, %d bytes", last.Images[0].MediaType, len(last.Images[0].Data))
	}
	for i := range seen[:len(seen)-1] {
		if len(seen[i].Images) != 0 {
			t.Fatalf("message %d carries images", i)
		}
	}
	if !strings.HasSuffix(*seen[0].Content, "\n\n"+attachmentTurnPrompt) {
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
	// The gate decodes what it is sent, so each is a real 1x1 image. WebP and
	// GIF come back as JPEG and PNG: it re-encodes to those two.
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	var jpg, gifBuf bytes.Buffer
	if err := jpeg.Encode(&jpg, src, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBuf, src, nil); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		want string
		body []byte
	}{
		{"image/png", fakePNG(256)},
		{"image/jpeg", jpg.Bytes()},
		{"image/png", gifBuf.Bytes()},
		{"image/jpeg", webp},
	} {
		want, body := tc.want, tc.body
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
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "   ", fakePNG(256))
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
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "look", fakePNG(256))
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
	tools := attachmentTurnTools()
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
	th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))

	names := toolNames(th.client.tools[0])
	if names["record_trades"] != 1 || names["get_quote"] != 1 || names["add_goal"] != 1 {
		t.Fatalf("tools = %v", names)
	}
	for _, banned := range []string{"record_trade", "add_position", "sell_position", "lookup_share_facts"} {
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
	for _, banned := range []string{"record_trade", "add_position", "sell_position", "lookup_share_facts"} {
		before := th.client.calls()
		th.client.replies = []ai.Message{callTool("c1", banned, `{"symbol":"AAPL"}`), say("I can't do that.")}
		r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
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
		r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), content, fakePNG(256))
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
	free, router := &countingClient{}, &countingClient{}
	p.Default = &ai.FallbackClient{Logger: silent, Rungs: []ai.Rung{
		{Tier: ai.Tier{Label: "free", Model: "vendor/model:free"}, Client: free},
		{Tier: ai.Tier{Label: "router", Model: "openrouter/free"}, Client: router},
		{Tier: ai.Tier{Label: "paid", Model: "vendor/model"}, Client: th.client},
	}}
	th.client.replies = []ai.Message{say("paid rung answered")}
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
	if r.Status != 200 || !strings.Contains(string(r.Body), "paid rung answered") {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	if free.hits.Load() != 0 || router.hits.Load() != 0 {
		t.Fatalf("free rungs hit %d and %d times", free.hits.Load(), router.hits.Load())
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
	th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "what is this", fakePNG(256))
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
	if !strings.Contains(system, "Images and files from earlier messages are no longer available. If the user asks about one, ask them to send it again rather than guessing.") {
		t.Fatalf("system = %q", system)
	}
	if strings.Contains(system, attachmentTurnPrompt) {
		t.Fatal("image prompt on a text turn")
	}
}

func TestImageTurnFlagOff(t *testing.T) {
	th := newTurnHarness(t, nil)
	user, conv := th.proConversation("off@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
	if r.Status != 404 {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	// With the flag off the routes keep the app-wide 10 MB cap, so nobody can
	// have 16 MB buffered for a feature that isn't on.
	img := fakePNG(4 << 20) // valid on its own, so only the cap can refuse three
	for _, route := range []string{"chat", "stream"} {
		r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/"+route, th.Token(user), "x", img, img, img)
		if r.Status != 413 {
			t.Fatalf("12 MB %s = %d %s", route, r.Status, r.Body)
		}
	}
	th.noTurnSpent(user)
}

// R11: image turns are first-party only, as the screenshot import is.
func TestImageTurnRefusesScopedToken(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("scoped-image@example.com")
	pat := th.pat(user, "assistant:write", "assistant:read")
	path := "/v1/ai/assistant/conversations/" + conv + "/chat"
	r := th.DoMultipart(path, pat, "x", fakePNG(256))
	if r.Status != 403 || !strings.Contains(string(r.Body), "This endpoint requires a first-party session.") {
		t.Fatalf("image = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
	th.client.replies = []ai.Message{say("Hi.")}
	if r := th.Do("POST", path, pat, map[string]any{"content": "hello"}); r.Status != 200 {
		t.Fatalf("scoped text turn = %d %s", r.Status, r.Body)
	}
}

// R12: the legacy-iOS grace predates images and never covers them.
func TestImageTurnIgnoresLegacyConsentGrace(t *testing.T) {
	th := newTurnHarness(t, map[string]string{
		"ASSISTANT_IMAGES_ENABLED": "true", "AI_CONSENT_ENFORCED": "true", "AI_CONSENT_LEGACY_IOS_GRACE": "true",
	})
	user, conv := th.proConversation("grace@example.com")
	const legacyIOS = "Norviq/77 CFNetwork/1498 Darwin/24.0.0"
	path := "/v1/ai/assistant/conversations/" + conv + "/chat"
	contentType, form := th.multipartForm("x", fakePNG(256))
	r := th.doRaw(path, th.Token(user), contentType, form, legacyIOS)
	if r.Status != 403 || !strings.Contains(string(r.Body), ai.ConsentRequiredReason) {
		t.Fatalf("image = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
	// The grace still lets the same client's text turn through.
	th.client.replies = []ai.Message{say("Hi.")}
	if r := th.doRaw(path, th.Token(user), "application/json", []byte(`{"content":"hello"}`), legacyIOS); r.Status != 200 {
		t.Fatalf("text turn = %d %s", r.Status, r.Body)
	}
}

// fakeRedis is what the real Limiter needs of Redis: its one script, counted
// in memory. Anything else panics on the nil embedded client.
type fakeRedis struct {
	redis.UniversalClient
	mu     sync.Mutex
	counts map[string]int64
}

func (f *fakeRedis) EvalSha(_ context.Context, _ string, keys []string, _ ...any) *redis.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[keys[0]]++
	return redis.NewCmdResult(f.counts[keys[0]], nil)
}

func (f *fakeRedis) count(key string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[key]
}

func TestImageTurnRateLimit(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	limits := &fakeRedis{counts: map[string]int64{}}
	th.Deps.Limiter.Redis = limits
	user, conv := th.proConversation("limit-images@example.com")
	path := "/v1/ai/assistant/conversations/" + conv + "/chat"
	img := fakePNG(256)
	for i := range 10 {
		th.client.replies = []ai.Message{say("ok")}
		if r := th.DoMultipart(path, th.Token(user), "x", img); r.Status != 200 {
			t.Fatalf("image turn %d = %d %s", i+1, r.Status, r.Body)
		}
	}
	calls := th.client.calls()
	r := th.DoMultipart(path, th.Token(user), "x", img)
	if r.Status != 429 {
		t.Fatalf("11th image turn = %d %s", r.Status, r.Body)
	}
	if th.client.calls() != calls {
		t.Fatal("the refused turn reached the model")
	}
	// /stream shares the bucket and refuses before any frame.
	if r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), "x", img); r.Status != 429 {
		t.Fatalf("stream = %d %s", r.Status, r.Body)
	}

	key := "ratelimit:assistant-images:user:" + apijson.FormatUUID(user)
	spent := limits.count(key)
	th.client.replies = []ai.Message{say("Hi.")}
	r = th.Do("POST", path, th.Token(user), map[string]any{"content": "hello"})
	if r.Status != 200 {
		t.Fatalf("text turn = %d %s", r.Status, r.Body)
	}
	if limits.count(key) != spent {
		t.Fatal("a text turn spent the image budget")
	}
	// The bucket is per user.
	other, otherConv := th.proConversation("limit-other@example.com")
	th.client.replies = []ai.Message{say("ok")}
	if r := th.DoMultipart("/v1/ai/assistant/conversations/"+otherConv+"/chat", th.Token(other), "x", img); r.Status != 200 {
		t.Fatalf("other user = %d %s", r.Status, r.Body)
	}
}

// Every provider call of the turn carries the image exactly once, on the
// turn's own user message, however many messages the loop adds after it,
// including the later user-role nudge and final-answer asks.
func TestImageStaysOnOneMessageThroughEveryRound(t *testing.T) {
	overview := func(id string) ai.Message { return callTool(id, "get_financial_overview", `{}`) }
	for name, tc := range map[string]struct {
		replies []ai.Message
		calls   int
	}{
		"nudge then tool rounds": {[]ai.Message{
			say("Let me check your portfolio."), overview("c1"), callTool("c2", "get_current_inflation", `{"country":"US"}`), say("Done."),
		}, 4},
		"tool rounds run out": {[]ai.Message{
			overview("c1"), overview("c2"), overview("c3"), overview("c4"), overview("c5"), overview("c6"), say("Done."),
		}, 7},
	} {
		t.Run(name, func(t *testing.T) {
			th := newTurnHarness(t, imagesOn)
			user, conv := th.proConversation("rounds@example.com")
			img := fakePNG(256)
			th.client.replies = tc.replies
			r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", img)
			if r.Status != 200 || th.client.calls() != tc.calls {
				t.Fatalf("status %d, %d calls: %s", r.Status, th.client.calls(), r.Body)
			}
			index := -1
			for call, seen := range th.client.seen {
				carrying := -1
				for i := range seen {
					if len(seen[i].Images) == 0 {
						continue
					}
					if carrying != -1 {
						t.Fatalf("call %d: messages %d and %d both carry images", call, carrying, i)
					}
					carrying = i
				}
				if carrying == -1 || seen[carrying].Role != "user" || !bytes.Equal(seen[carrying].Images[0].Data, pngCore) {
					t.Fatalf("call %d: images on %d of %d messages", call, carrying, len(seen))
				}
				if index != -1 && carrying != index {
					t.Fatalf("call %d: image moved from message %d to %d", call, index, carrying)
				}
				index = carrying
			}
		})
	}
}

// RequirePremium answers every premium gate with a 403 upgrade_required body.
func TestImageTurnNonPremium(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user := th.CreateUser("nonpro@example.com")
	conv := th.conversation(user)
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
	body := string(r.Body)
	if r.Status != 403 || !strings.Contains(body, `"code":"upgrade_required"`) || !strings.Contains(body, `"feature":"screenshot_import"`) {
		t.Fatalf("status = %d %s", r.Status, body)
	}
	th.noTurnSpent(user)
	r = th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), "x", fakePNG(256))
	frames := parseFrames(t, r.Body)
	if events(frames) != "started,error,done" || !strings.Contains(frames[1].data, `"code":"upgrade_required"`) {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestImageTurnConsentRequired(t *testing.T) {
	th := newTurnHarness(t, map[string]string{"ASSISTANT_IMAGES_ENABLED": "true", "AI_CONSENT_ENFORCED": "true"})
	user, conv := th.proConversation("consent@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
	if r.Status != 403 || !strings.Contains(string(r.Body), ai.ConsentRequiredReason) {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
}

func TestImageTurnRejectsNonImage(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("pdf@example.com")
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", []byte("<html>not an image</html>"))
	if r.Status != 400 || !strings.Contains(string(r.Body), "Only images, PDFs and CSV files are supported.") {
		t.Fatalf("status = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
}

func TestImageTurnRejectsFourImages(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("four@example.com")
	img := fakePNG(256)
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", img, img, img, img)
	if r.Status != 400 || !strings.Contains(string(r.Body), "Attach up to 3 files.") {
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
	img := fakePNG(256)
	path := "/v1/ai/assistant/conversations/" + conv + "/stream"
	for reason, images := range map[string][][]byte{
		"Only images, PDFs and CSV files are supported.": {[]byte("<html>not an image</html>")},
		"Attach up to 3 files.":                          {img, img, img, img},
		"Each image must be 5 MB or smaller.":            {fakePNG(5<<20 + 1)},
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

// Only the two turn routes get the larger cap.
func TestBodyLimitStaysOnOtherRoutes(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("otherroutes@example.com")
	body := bytes.Repeat([]byte{' '}, 11<<20)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/ai/assistant/conversations/" + conv + "/messages"},
		{"POST", "/v1/ai/chat"},
		{"PUT", "/v1/ai/assistant/preferences"},
	} {
		if r := th.Do(tc.method, tc.path, th.Token(user), body); r.Status != 413 {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, r.Status, r.Body)
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
	if strings.Contains(*first[0].Content, attachmentTurnPrompt) {
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

// byokStub routes a user's own key to a stub provider answering status.
type byokStub struct {
	status atomic.Int32
	hits   atomic.Int32
	p      *ai.Platform
}

func newByokStub(t *testing.T, th *turnHarness) *byokStub {
	t.Helper()
	b := &byokStub{}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b.hits.Add(1)
		http.Error(w, `{"error":{"message":"stub provider refused"}}`, int(b.status.Load()))
	}))
	t.Cleanup(stub.Close)
	target, err := url.Parse(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	if b.p, err = aiplatform.Platform(th.Deps); err != nil {
		t.Fatal(err)
	}
	b.p.HTTP = &http.Client{Transport: rewriteTransport{target: target}}
	return b
}

func (b *byokStub) addKey(th *turnHarness, user uuid.UUID) {
	th.T.Helper()
	key, err := b.p.Cipher.Encrypt("sk-fake-test-key", ai.CredentialContextAIProvider)
	if err != nil {
		th.T.Fatal(err)
	}
	th.exec(`INSERT INTO user_ai_provider_credentials (id, user_id, provider, label, api_key_encrypted, key_hint, key_fingerprint, status, created_at, updated_at)
		VALUES ($1, $2, 'openai', 'mine', $3, 'hint', 'fp', 'active', now(), now())`, uuid.New(), user, key)
}

var byokFallbackOn = map[string]string{"ASSISTANT_IMAGES_ENABLED": "true", "AI_USER_CREDENTIAL_FALLBACK": "true"}

func TestByokImageRejectionMessage(t *testing.T) {
	th := newTurnHarness(t, byokFallbackOn)
	user, conv := th.proConversation("byok@example.com")
	stub := newByokStub(t, th)
	stub.status.Store(http.StatusBadRequest)
	stub.addKey(th, user)

	const message = "Your openai model can't read images. Try without the image, or change the model in Settings → AI providers."
	r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), "x", fakePNG(256))
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

	r = th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
	if r.Status != 424 || !strings.Contains(string(r.Body), message) {
		t.Fatalf("chat = %d %s", r.Status, r.Body)
	}
	if stub.hits.Load() != 2 {
		t.Fatalf("stub hits = %d", stub.hits.Load())
	}
	if th.client.calls() != 0 {
		t.Fatal("the image fell back to Norviq's provider")
	}
	// The key is fine; the model just can't read images.
	if n := th.count(`SELECT count(*) FROM user_ai_provider_credentials WHERE user_id = $1 AND status = 'active' AND last_error_code IS NULL`, user); n != 1 {
		t.Fatal("an image rejection marked the credential as failing")
	}
}

// With fallback on, no provider failure sends a user's image to Norviq's
// chain. The credential is blamed only when the status says the key is at fault.
func TestByokImageNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		status          int
		message         string
		credentialState string
	}{
		{http.StatusNotFound, "Your openai model didn't accept this image. It may not read images, or its name or base URL may be wrong. Try without the image, or check Settings → AI providers.", "status = 'active' AND last_error_code IS NULL"},
		{http.StatusUnauthorized, "Your openai key was rejected. Update it in Settings → AI providers.", "status = 'invalid' AND last_error_code IS NOT NULL"},
		{http.StatusTooManyRequests, "Your openai account is rate limiting requests. Try again shortly.", "status = 'active' AND last_error_code IS NOT NULL"},
		{http.StatusInternalServerError, "Could not reach openai with your key. Try again shortly.", "status = 'active' AND last_error_code IS NOT NULL"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			th := newTurnHarness(t, byokFallbackOn)
			user, conv := th.proConversation("byok-status@example.com")
			stub := newByokStub(t, th)
			stub.status.Store(int32(tc.status))
			stub.addKey(th, user)
			r := th.DoMultipart("/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), "x", fakePNG(256))
			if r.Status != 424 || !strings.Contains(string(r.Body), tc.message) {
				t.Fatalf("chat = %d %s", r.Status, r.Body)
			}
			if stub.hits.Load() != 1 {
				t.Fatalf("stub hits = %d", stub.hits.Load())
			}
			if th.client.calls() != 0 {
				t.Fatal("the image reached Norviq's chain")
			}
			if n := th.count(`SELECT count(*) FROM user_ai_provider_credentials WHERE user_id = $1 AND `+tc.credentialState, user); n != 1 {
				t.Fatalf("credential is not %s", tc.credentialState)
			}
		})
	}
}

// The fallback is only closed for images: a text turn still takes it.
func TestByokTextTurnStillFallsBack(t *testing.T) {
	th := newTurnHarness(t, byokFallbackOn)
	user, conv := th.proConversation("byok-text@example.com")
	stub := newByokStub(t, th)
	stub.status.Store(http.StatusUnauthorized)
	stub.addKey(th, user)
	th.client.replies = []ai.Message{say("From Norviq's chain.")}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "hello"})
	if r.Status != 200 || !strings.Contains(string(r.Body), "From Norviq's chain.") || th.client.calls() != 1 {
		t.Fatalf("text turn = %d %s", r.Status, r.Body)
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
