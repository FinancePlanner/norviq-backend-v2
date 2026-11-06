package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"norviq-api/internal/features/brokers"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/store/assistantdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

const (
	holdingsCSVBody  = "Symbol,Shares,Average Cost\nAAPL,10,150\nMSFT,5,300\n"
	holdingsNoBody   = "Review these holdings on the web or update the app."
	holdingsNothing  = "Nothing was selected to save."
	collisionReason  = "These holdings already exist: AAPL. Turn on “Replace existing” to update them."
	holdingsModelRow = `{"symbol":"AAPL","shares":999,"avg_price":1}`
)

type holdingsHarness struct{ *tradesHarness }

func newHoldingsHarness(t *testing.T) *holdingsHarness {
	t.Helper()
	th := newTurnHarness(t, docsOn)
	user, conv := th.proConversation("holdings@example.com")
	h := &tradesHarness{turnHarness: th, user: user, conv: conv, token: th.Token(user)}
	for _, symbol := range []string{"AAPL", "MSFT", "NVDA"} {
		if _, err := stocks.ResolveImportInstrument(context.Background(), th.Deps.DB, nil, symbol); err != nil {
			t.Fatal(err)
		}
	}
	return &holdingsHarness{h}
}

type clientHoldings struct {
	PortfolioListID string `json:"portfolioListId"`
	FromCSV         bool   `json:"fromCsv"`
	Rows            []struct {
		Symbol    string   `json:"symbol"`
		Shares    *float64 `json:"shares"`
		AvgPrice  *float64 `json:"avgPrice"`
		BuyDate   *string  `json:"buyDate"`
		Existing  string   `json:"existing"`
		NeedsCost bool     `json:"needsCost"`
	} `json:"rows"`
}

type holdingsRefusalBody struct {
	Reason  string   `json:"reason"`
	Code    string   `json:"code"`
	Symbols []string `json:"symbols"`
	Rows    []struct {
		Index   int    `json:"index"`
		Message string `json:"message"`
	} `json:"rows"`
}

func (h *holdingsHarness) turn(args string, file *testFile) turnResponse {
	h.T.Helper()
	h.client.replies = []ai.Message{callTool("c1", "update_holdings", args), say("ok")}
	var r *testutil.Response
	if file != nil {
		r = h.DoFiles(chatPath(h.conv), h.token, "update my holdings", *file)
	} else {
		r = h.DoMultipart(chatPath(h.conv), h.token, "update my holdings", fakePNG(64))
	}
	if r.Status != 200 {
		h.T.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	var turn turnResponse
	r.JSON(h.T, &turn)
	return turn
}

func (h *holdingsHarness) card(args string, file *testFile) (*pendingActionResponse, clientHoldings) {
	h.T.Helper()
	turn := h.turn(args, file)
	if turn.Kind != "confirmation_required" || turn.PendingAction == nil || turn.PendingAction.ToolName != "update_holdings" {
		h.T.Fatalf("no card: %+v", turn)
	}
	var got clientHoldings
	if err := json.Unmarshal([]byte(turn.PendingAction.Arguments), &got); err != nil {
		h.T.Fatalf("arguments %s: %v", turn.PendingAction.Arguments, err)
	}
	return turn.PendingAction, got
}

func (h *holdingsHarness) pending(action *pendingActionResponse) {
	h.T.Helper()
	if got := h.actionStatus(action); got != statusPending {
		h.T.Fatalf("status = %s, want pending", got)
	}
	if n := h.count(`SELECT count(*) FROM ai_action_audits WHERE pending_action_id = $1`, uuid.MustParse(action.ID)); n != 0 {
		h.T.Fatalf("audit rows = %d", n)
	}
}

func (h *holdingsHarness) stocksCount() int {
	return h.count(`SELECT count(*) FROM stocks WHERE user_id = $1`, h.user)
}

func (h *holdingsHarness) refusal(r *testutil.Response) holdingsRefusalBody {
	h.T.Helper()
	if r.Status != 422 {
		h.T.Fatalf("status = %d: %s", r.Status, r.Body)
	}
	var body holdingsRefusalBody
	r.JSON(h.T, &body)
	return body
}

func rev(symbol string, shares, cost float64) map[string]any {
	return map[string]any{"symbol": symbol, "shares": shares, "avgPrice": cost}
}

func revBody(rows ...map[string]any) map[string]any {
	if rows == nil {
		rows = []map[string]any{}
	}
	return map[string]any{"rows": rows}
}

func TestUpdateHoldingsProposalStoresCard(t *testing.T) {
	h := newHoldingsHarness(t)
	action, card := h.card(`{"rows":[{"symbol":"aapl","shares":10,"avg_price":150,"buy_date":"2024-01-02"},{"symbol":"MSFT","shares":5}]}`, nil)
	if len(card.Rows) != 2 || card.FromCSV || card.PortfolioListID == "" {
		t.Fatalf("card = %s", action.Arguments)
	}
	if r := card.Rows[0]; r.Symbol != "AAPL" || *r.Shares != 10 || *r.AvgPrice != 150 || *r.BuyDate != "2024-01-02" || r.NeedsCost || r.Existing != "none" {
		t.Fatalf("row 0 = %+v", r)
	}
	if r := card.Rows[1]; r.Symbol != "MSFT" || r.AvgPrice != nil || !r.NeedsCost {
		t.Fatalf("row 1 = %+v", r)
	}
	if h.stocksCount() != 0 {
		t.Fatal("a proposal saved something")
	}
	h.pending(action)
}

func TestUpdateHoldingsCSVIgnoresModelRows(t *testing.T) {
	h := newHoldingsHarness(t)
	_, card := h.card(`{"rows":[`+holdingsModelRow+`]}`, &testFile{"h.csv", []byte(holdingsCSVBody)})
	if !card.FromCSV || len(card.Rows) != 2 || card.Rows[0].Symbol != "AAPL" || *card.Rows[0].Shares != 10 || *card.Rows[0].AvgPrice != 150 {
		t.Fatalf("card = %+v", card)
	}
	sent := *lastMessage(h.client.seen[0]).Content
	const summary = "The attached CSV is a holdings export with 2 rows: AAPL, MSFT. Call update_holdings to let the user review it; its rows are filled in by Norviq."
	if !strings.Contains(sent, summary) || strings.Contains(sent, "Average Cost") || strings.Contains(sent, "<attachment") {
		t.Fatalf("model saw %q", sent)
	}
}

func TestUpdateHoldingsNonHoldingsCSVStaysText(t *testing.T) {
	h := newHoldingsHarness(t)
	h.client.replies = []ai.Message{say("ok")}
	if r := h.DoFiles(chatPath(h.conv), h.token, "x", testFile{"t.csv", []byte("a,b\n1,2\n")}); r.Status != 200 {
		t.Fatalf("turn = %d", r.Status)
	}
	if got := *lastMessage(h.client.seen[0]).Content; !strings.Contains(got, `<attachment name="t.csv"`) {
		t.Fatalf("model saw %q", got)
	}
}

func TestUpdateHoldingsConfirmSaves(t *testing.T) {
	h := newHoldingsHarness(t)
	action, _ := h.card(`{"rows":[{"symbol":"AAPL","shares":1,"avg_price":1}]}`, nil)
	r := h.confirm(action, revBody(rev("AAPL", 10, 150), rev("MSFT", 5, 300)))
	if r.Status != 200 {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	var out confirmedActionResponse
	r.JSON(t, &out)
	if out.Message != "Saved AAPL, MSFT." || out.Status != statusCompleted {
		t.Fatalf("response = %s", r.Body)
	}
	if got := h.shares(h.defaultList(), "AAPL"); got != 10 {
		t.Fatalf("AAPL shares = %v", got)
	}
	h.settledAs(action, statusCompleted)
}

func TestUpdateHoldingsConfirmMissingCost422(t *testing.T) {
	h := newHoldingsHarness(t)
	action, _ := h.card(`{"rows":[{"symbol":"AAPL","shares":1}]}`, nil)
	for name, row := range map[string]map[string]any{
		"no cost":     {"symbol": "MSFT", "shares": 5},
		"zero cost":   rev("MSFT", 5, 0),
		"no shares":   {"symbol": "MSFT", "avgPrice": 5},
		"zero shares": rev("MSFT", 0, 5),
	} {
		got := h.refusal(h.confirm(action, revBody(rev("AAPL", 10, 150), row)))
		if len(got.Rows) != 1 || got.Rows[0].Index != 1 || got.Rows[0].Message == "" || got.Reason == "" {
			t.Fatalf("%s: body = %+v", name, got)
		}
		h.pending(action)
		if h.stocksCount() != 0 {
			t.Fatalf("%s: saved a row", name)
		}
	}
}

func TestUpdateHoldingsConfirmCollision422Code(t *testing.T) {
	h := newHoldingsHarness(t)
	h.hold("AAPL", 3)
	action, _ := h.card(`{"rows":[{"symbol":"AAPL","shares":1,"avg_price":1}]}`, nil)
	got := h.refusal(h.confirm(action, revBody(rev("AAPL", 10, 150), rev("MSFT", 5, 300))))
	if got.Code != "holdings_collision" || got.Reason != collisionReason || len(got.Symbols) != 1 || got.Symbols[0] != "AAPL" || len(got.Rows) != 0 {
		t.Fatalf("body = %+v", got)
	}
	h.pending(action)

	// Row errors ride along in the same body.
	got = h.refusal(h.confirm(action, revBody(rev("AAPL", 10, 150), rev("MSFT", 5, 0))))
	if got.Code != "holdings_collision" || len(got.Rows) != 1 || got.Rows[0].Index != 1 {
		t.Fatalf("with row errors = %+v", got)
	}

	body := revBody(rev("AAPL", 10, 150), rev("MSFT", 5, 300))
	body["replaceExisting"] = true
	if r := h.confirm(action, body); r.Status != 200 {
		t.Fatalf("replace = %d %s", r.Status, r.Body)
	}
	if got := h.shares(h.defaultList(), "AAPL"); got != 10 {
		t.Fatalf("AAPL shares = %v", got)
	}
	h.settledAs(action, statusCompleted)
}

func TestUpdateHoldingsConfirmEmpty422(t *testing.T) {
	h := newHoldingsHarness(t)
	action, _ := h.card(`{"rows":[{"symbol":"AAPL","shares":1,"avg_price":1}]}`, nil)
	for _, body := range []any{revBody(), []byte(`{"rows":[]}`)} {
		if got := h.refusal(h.confirm(action, body)); got.Reason != holdingsNothing {
			t.Fatalf("reason = %q", got.Reason)
		}
		h.pending(action)
	}
}

func TestUpdateHoldingsExpired409(t *testing.T) {
	h := newHoldingsHarness(t)
	action, _ := h.card(`{"rows":[{"symbol":"AAPL","shares":1,"avg_price":1}]}`, nil)
	h.exec(`UPDATE ai_pending_actions SET expires_at = now() - interval '1 minute' WHERE id = $1`, uuid.MustParse(action.ID))
	for _, body := range []any{revBody(rev("AAPL", 1, 1)), revBody(rev("AAPL", 1, 0)), nil, []byte(`{"rows":[]}`)} {
		if r := h.confirm(action, body); r.Status != 409 {
			t.Fatalf("confirm = %d %s", r.Status, r.Body)
		}
	}
	if h.stocksCount() != 0 {
		t.Fatal("saved after expiry")
	}
}

func TestUpdateHoldingsNoBodyConfirmRefused(t *testing.T) {
	h := newHoldingsHarness(t)
	action, _ := h.card(`{"rows":[{"symbol":"AAPL","shares":1,"avg_price":1}]}`, nil)
	for name, body := range map[string]any{"none": nil, "null": []byte("null"), "object": []byte(`{}`)} {
		if got := h.refusal(h.confirm(action, body)); got.Reason != holdingsNoBody {
			t.Errorf("%s: reason = %q", name, got.Reason)
		}
		h.pending(action)
	}
	_, err := h.h.confirm(context.Background(), uuid.MustParse(action.ID), h.user)
	if reason, ok := abortReason(err); !ok || reason != holdingsNoBody {
		t.Fatalf("confirm = %v", err)
	}
	h.pending(action)

	conv := uuid.MustParse(h.conv)
	link := &assistantdb.MessagingLink{UserID: h.user, ConversationID: &conv}
	m := newMessenger(h.h)
	h.client.replies = []ai.Message{say("A normal answer.")}
	if out, err := m.assistantTurn(context.Background(), telegram.InboundMessage{Text: "yes"}, link, nil); err != nil || out.Text != "A normal answer." {
		t.Fatalf("yes = %q, %v", out.Text, err)
	}
	out, err := m.assistantTurn(context.Background(), telegram.InboundMessage{Text: messagingConfirmPrefix + action.ID}, link, nil)
	if err != nil || out.Text != holdingsNoBody {
		t.Fatalf("explicit confirm = %q, %v", out.Text, err)
	}
	h.pending(action)
}

func TestUpdateHoldingsSplitCallsRefused(t *testing.T) {
	h := newHoldingsHarness(t)
	one := `{"rows":[{"symbol":"AAPL","shares":1,"avg_price":1}]}`
	h.client.replies = []ai.Message{
		{Role: "assistant", ToolCalls: []ai.ToolCall{callTool("a", "update_holdings", one).ToolCalls[0], callTool("b", "update_holdings", one).ToolCalls[0]}},
		say("ok"),
	}
	r := h.DoMultipart(chatPath(h.conv), h.token, "update", fakePNG(64))
	if r.Status != 200 {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	var turn turnResponse
	r.JSON(t, &turn)
	if turn.PendingAction != nil {
		t.Fatalf("split calls made a card: %+v", turn)
	}
	h.noCards()
}

func TestUpdateHoldingsMalformedRowsGoBackToModel(t *testing.T) {
	h := newHoldingsHarness(t)
	for _, args := range []string{`{}`, `{"rows":[]}`, `{"rows":[{"shares":1}]}`, `{"rows":[{"symbol":"A","shares":"x"}]}`} {
		turn := h.turn(args, nil)
		if turn.PendingAction != nil {
			t.Fatalf("%s made a card", args)
		}
	}
	h.noCards()
}

func TestUpdateHoldingsCompletionMessageNamesFailures(t *testing.T) {
	rows := []brokers.HoldingRow{{Symbol: "aapl"}, {Symbol: "XYZ"}, {Symbol: "MSFT"}}
	res := brokers.HoldingsCommitResult{
		Saved: []string{"AAPL", "MSFT"}, Failed: []string{"XYZ"},
		Errors: []brokers.PreviewError{{Line: 1, Message: "No such symbol."}},
	}
	if got, want := holdingsCompletionMessage(rows, res), "Saved AAPL, MSFT. Couldn't save XYZ: No such symbol."; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if got := holdingsCompletionMessage(rows, brokers.HoldingsCommitResult{Saved: []string{"AAPL"}}); got != "Saved AAPL." {
		t.Fatalf("message = %q", got)
	}
}
