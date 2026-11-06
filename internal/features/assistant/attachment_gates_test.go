package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"norviq-api/internal/platform/ai"
	"norviq-api/internal/testutil"
)

// encryptedPDF sniffs as a PDF and is refused by the PDF check with the
// password reason. A turn that answers anything else for it was refused before
// the PDF check ran: it is the probe for gate order.
var encryptedPDF = minimalPDF(" /Encrypt 5 0 R")

// undecodablePNG sniffs as a PNG and fails the decoder.
var undecodablePNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{'i'}, 64)...)

func wantCoded(t *testing.T, what string, r *testutil.Response, status int, code, reason string) {
	t.Helper()
	body := string(r.Body)
	if r.Status != status || !strings.Contains(body, `"`+code+`"`) || !strings.Contains(body, reason) {
		t.Fatalf("%s = %d %s, want %d %s %q", what, r.Status, body, status, code, reason)
	}
	if strings.Contains(body, pdfPasswordReason) {
		t.Fatalf("%s ran the PDF check: %s", what, body)
	}
}

// R29: with documents off, a PDF is the documents-off 404 whatever its content.
func TestDocumentsOffEncryptedPDFIs404(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("enc-off@example.com")
	r := th.DoFiles(chatPath(conv), th.Token(user), "x", testFile{"s.pdf", encryptedPDF})
	wantCoded(t, "encrypted pdf", r, 404, codeAttachmentRejected, documentsOffMessage)
	// Two PDFs are not counted with documents off: still the 404.
	r = th.DoFiles(chatPath(conv), th.Token(user), "x", testFile{"a.pdf", encryptedPDF}, testFile{"b.pdf", encryptedPDF})
	wantCoded(t, "two pdfs", r, 404, codeAttachmentRejected, documentsOffMessage)
	th.noTurnSpent(user)
}

// R29: the scoped-token 403 comes before any file is validated.
func TestScopedTokenWithPDFRefusedBeforeValidation(t *testing.T) {
	th := newTurnHarness(t, docsOn)
	user, conv := th.proConversation("enc-scoped@example.com")
	pat := th.pat(user, "assistant:write", "assistant:read")
	r := th.DoFiles(chatPath(conv), pat, "x", testFile{"s.pdf", encryptedPDF})
	wantCoded(t, "scoped pdf", r, 403, codeAttachmentRejected, "This endpoint requires a first-party session.")
	th.noTurnSpent(user)
}

// R29: the Premium gate comes before any file is validated.
func TestNonPremiumWithPDFRefusedBeforeValidation(t *testing.T) {
	th := newTurnHarness(t, docsOn)
	user := th.CreateUser("enc-free@example.com")
	conv := th.conversation(user)
	r := th.DoFiles(chatPath(conv), th.Token(user), "x", testFile{"s.pdf", encryptedPDF})
	wantCoded(t, "free pdf", r, 403, "upgrade_required", `"feature":"screenshot_import"`)
	th.noTurnSpent(user)
}

// R29: an image is decoded only for a user who passed the gates.
func TestImageDecodeRunsAfterPremiumGate(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user := th.CreateUser("png-free@example.com")
	conv := th.conversation(user)
	r := th.DoMultipart(chatPath(conv), th.Token(user), "x", undecodablePNG)
	wantCoded(t, "free image", r, 403, "upgrade_required", `"feature":"screenshot_import"`)

	pro, proConv := th.proConversation("png-scoped@example.com")
	r = th.DoMultipart(chatPath(proConv), th.pat(pro, "assistant:write", "assistant:read"), "x", undecodablePNG)
	wantCoded(t, "scoped image", r, 403, codeImageRejected, "This endpoint requires a first-party session.")

	// Past the gates the decoder does run and refuses it.
	r = th.DoMultipart(chatPath(proConv), th.Token(pro), "x", undecodablePNG)
	if r.Status != 400 || !strings.Contains(string(r.Body), `"image_rejected"`) {
		t.Fatalf("pro image = %d %s", r.Status, r.Body)
	}
	th.noTurnSpent(user)
}

// R29: production's flags give the images-only copy for a wrong type.
func TestUnknownTypeWithDocumentsOffIsImageRejected(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("unknown-off@example.com")
	r := th.DoFiles(chatPath(conv), th.Token(user), "x", testFile{"a.bmp", append([]byte("BM"), make([]byte, 64)...)})
	wantCoded(t, "bmp", r, 400, codeImageRejected, "Only JPEG, PNG, WebP and GIF images are supported.")
	r = th.DoFiles(chatPath(conv), th.Token(user), "x", testFile{"a.txt", []byte("<html><body>x</body></html>")})
	wantCoded(t, "html", r, 400, codeImageRejected, "Only JPEG, PNG, WebP and GIF images are supported.")
	th.noTurnSpent(user)

	both := newTurnHarness(t, docsOn)
	user, conv = both.proConversation("unknown-on@example.com")
	r = both.DoFiles(chatPath(conv), both.Token(user), "x", testFile{"a.txt", []byte("<html><body>x</body></html>")})
	wantCoded(t, "html, documents on", r, 400, codeImageRejected, "Only images, PDFs and CSV files are supported.")
}

// R30: on production's flags an image turn is the holdings path from before
// update_holdings: no such tool, and the import link in the prompt.
func TestImagesOnDocumentsOffOffersNoUpdateHoldings(t *testing.T) {
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("holdings-off@example.com")
	th.client.replies = []ai.Message{callTool("c1", "update_holdings", `{"rows":[{"symbol":"AAPL","shares":1}]}`), say("ok")}
	r := th.DoMultipart(chatPath(conv), th.Token(user), "my positions", fakePNG(256))
	if r.Status != 200 {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	var turn turnResponse
	r.JSON(t, &turn)
	if turn.Kind != "message" || turn.PendingAction != nil {
		t.Fatalf("a card from a tool never offered: %+v", turn)
	}
	th.client.mu.Lock()
	tools, seen := th.client.tools[0], th.client.seen
	th.client.mu.Unlock()
	if n := toolNames(tools)[updateHoldingsToolName]; n != 0 {
		t.Fatalf("update_holdings offered %d times", n)
	}
	system := *seen[0][0].Content
	const old = "If it shows a holdings or positions list, do not call record_trades: describe it and tell the user they can import holdings at /portfolio/import/screenshot."
	if !strings.Contains(system, old) || strings.Contains(system, "update_holdings") {
		t.Fatalf("system = %q", system)
	}
	if n := th.count(`SELECT count(*) FROM ai_pending_actions WHERE user_id = $1`, user); n != 0 {
		t.Fatalf("pending actions = %d", n)
	}
}

func TestDocumentsOnOffersUpdateHoldings(t *testing.T) {
	th := newTurnHarness(t, docsOn)
	user, conv := th.proConversation("holdings-on@example.com")
	th.client.replies = []ai.Message{say("ok")}
	if r := th.DoMultipart(chatPath(conv), th.Token(user), "my positions", fakePNG(256)); r.Status != 200 {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	th.client.mu.Lock()
	tools, system := th.client.tools[0], *th.client.seen[0][0].Content
	th.client.mu.Unlock()
	if n := toolNames(tools)[updateHoldingsToolName]; n != 1 {
		t.Fatalf("update_holdings offered %d times", n)
	}
	if !strings.Contains(system, "call update_holdings once") || strings.Contains(system, "/portfolio/import/screenshot") {
		t.Fatalf("system = %q", system)
	}
}

// pendingArguments is every stored card's arguments and summary, decrypted.
func (h *holdingsHarness) pendingArguments() []string {
	h.T.Helper()
	rows, err := h.Deps.DB.Query(context.Background(),
		`SELECT arguments_encrypted, summary_encrypted FROM ai_pending_actions WHERE user_id = $1`, h.user)
	if err != nil {
		h.T.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var args, summary []byte
		if err := rows.Scan(&args, &summary); err != nil {
			h.T.Fatal(err)
		}
		for _, enc := range [][]byte{args, summary} {
			plain, err := h.h.pii.DecryptString(enc)
			if err != nil {
				h.T.Fatal(err)
			}
			out = append(out, plain)
		}
	}
	if err := rows.Err(); err != nil {
		h.T.Fatal(err)
	}
	return out
}

// lastToolResult is what the model was told for its last tool call.
func (h *holdingsHarness) lastToolResult() string {
	h.T.Helper()
	h.client.mu.Lock()
	defer h.client.mu.Unlock()
	msgs := h.client.seen[len(h.client.seen)-1]
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" && msgs[i].Content != nil {
			return *msgs[i].Content
		}
	}
	h.T.Fatal("no tool result")
	return ""
}

// R31: a cell that is no ticker never reaches the card or the database.
func TestHoldingsCSVLongCellNeverStored(t *testing.T) {
	h := newHoldingsHarness(t)
	long := strings.Repeat("Z", 10<<10)
	csv := "Symbol,Shares,Average Cost,Buy Date\nAAPL,10,150,2024-01-02\n" + long + ",5,300,2024-01-02\nMSFT,5,300," + long + "\n"
	_, card := h.card(`{}`, &testFile{"h.csv", []byte(csv)})
	if len(card.Rows) != 2 || card.Rows[0].Symbol != "AAPL" || card.Rows[1].Symbol != "MSFT" {
		t.Fatalf("card = %+v", card)
	}
	if card.Rows[1].BuyDate != nil {
		t.Fatalf("buyDate = %q", *card.Rows[1].BuyDate)
	}
	stored := h.pendingArguments()
	if len(stored) == 0 {
		t.Fatal("no card stored")
	}
	for _, s := range stored {
		if strings.Contains(s, "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ") {
			t.Fatalf("long cell stored: %.120q", s)
		}
	}
	if sent := *lastMessage(h.client.seen[0]).Content; strings.Contains(sent, "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ") {
		t.Fatalf("long cell reached the model: %.200q", sent)
	}
}

// R31: a date the CSV import can't read is no date at all, from either source.
func TestHoldingsCSVEuropeanDateBecomesNil(t *testing.T) {
	h := newHoldingsHarness(t)
	_, card := h.card(`{}`, &testFile{"h.csv", []byte("Symbol,Shares,Average Cost,Buy Date\nAAPL,10,150,15.03.2024\nMSFT,5,300,03/15/2024\n")})
	if len(card.Rows) != 2 || card.Rows[0].BuyDate != nil {
		t.Fatalf("card = %+v", card)
	}
	if d := card.Rows[1].BuyDate; d == nil || *d != "2024-03-15" {
		t.Fatalf("US date not normalized: %v", d)
	}

	_, card = h.card(`{"rows":[{"symbol":"AAPL","shares":1,"buy_date":"15.03.2024"},{"symbol":"MSFT","shares":1,"buy_date":"2024-03-15"}]}`, nil)
	if len(card.Rows) != 2 || card.Rows[0].BuyDate != nil || card.Rows[1].BuyDate == nil || *card.Rows[1].BuyDate != "2024-03-15" {
		t.Fatalf("model card = %+v", card)
	}
}

// R31: the summary counts the rows it names, the ones the card will hold.
func TestHoldingsSummaryCountMatchesSymbols(t *testing.T) {
	h := newHoldingsHarness(t)
	_, card := h.card(`{}`, &testFile{"h.csv", []byte("Symbol,Shares,Average Cost\nAAPL,10,150\nBUY NOW,1,1\nMSFT,5,300\n")})
	sent := *lastMessage(h.client.seen[0]).Content
	const summary = "The attached CSV is a holdings export with 2 rows: AAPL, MSFT. Call update_holdings to let the user review it; its rows are filled in by Norviq."
	if !strings.Contains(sent, summary) || strings.Contains(sent, "BUY NOW") {
		t.Fatalf("model saw %q", sent)
	}
	if len(card.Rows) != 2 {
		t.Fatalf("card = %+v", card)
	}
}

// R31: with nothing left after sanitizing, the model is told and no card is made.
func TestHoldingsNoReadableRowsIsAProblem(t *testing.T) {
	h := newHoldingsHarness(t)
	for name, file := range map[string]*testFile{
		"model rows": nil,
		"csv rows":   {"h.csv", []byte("Symbol,Shares,Average Cost\nBUY NOW,10,150\n$$$,5,300\n")},
	} {
		turn := h.turn(`{"rows":[{"symbol":"NOT A TICKER","shares":1}]}`, file)
		if turn.PendingAction != nil {
			t.Fatalf("%s made a card", name)
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(h.lastToolResult()), &got); err != nil || got["error"] != "No readable holdings in the attachment." {
			t.Fatalf("%s: tool result %q", name, h.lastToolResult())
		}
	}
	h.noCards()
}
