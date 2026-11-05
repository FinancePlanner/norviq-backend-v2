package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

const (
	defaultListName = "Main Portfolio"
	noBodyRefusal   = "Review these trades on the web or update the app."
	noneSelected    = "No trades selected."
)

// clientTrades is the stored arguments as web and iOS parse them.
type clientTrades struct {
	PortfolioListID string `json:"portfolio_list_id"`
	SkippedRows     int    `json:"skipped_rows"`
	BaseCurrency    string `json:"base_currency"`
	Synced          bool   `json:"synced"`
	Trades          []struct {
		Symbol     string   `json:"symbol"`
		Side       string   `json:"side"`
		Quantity   float64  `json:"quantity"`
		Price      float64  `json:"price"`
		TradeDate  string   `json:"trade_date"`
		Currency   *string  `json:"currency"`
		Fees       *float64 `json:"fees"`
		Confidence *float64 `json:"confidence"`
		Issues     []struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			Blocking bool   `json:"blocking"`
		} `json:"issues"`
	} `json:"trades"`
}

type tradesRefusalBody struct {
	Reason    string `json:"reason"`
	RowErrors []struct {
		Index   int    `json:"index"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"rowErrors"`
}

// tradesHarness is a Pro user on an image-enabled server. Every symbol is
// seeded as an instrument, so neither a preview nor a save reaches the market
// search and no test touches a provider.
type tradesHarness struct {
	*turnHarness
	user  uuid.UUID
	conv  string
	token string
}

func newTradesHarness(t *testing.T) *tradesHarness {
	t.Helper()
	th := newTurnHarness(t, imagesOn)
	user, conv := th.proConversation("trades@example.com")
	h := &tradesHarness{turnHarness: th, user: user, conv: conv, token: th.Token(user)}
	for _, symbol := range []string{"AAPL", "MSFT", "NVDA", "BRK.B"} {
		if _, err := stocks.ResolveImportInstrument(context.Background(), th.Deps.DB, nil, symbol); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// imageTurn runs one image turn whose model calls record_trades with args,
// then says "ok" if asked again.
func (h *tradesHarness) imageTurn(args string) turnResponse {
	h.T.Helper()
	h.client.replies = []ai.Message{callTool("c1", "record_trades", args), say("ok")}
	r := h.DoMultipart("/v1/ai/assistant/conversations/"+h.conv+"/chat", h.token, "record these", fakePNG(64))
	if r.Status != 200 {
		h.T.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	var turn turnResponse
	r.JSON(h.T, &turn)
	return turn
}

func (h *tradesHarness) card(args string) *pendingActionResponse {
	h.T.Helper()
	turn := h.imageTurn(args)
	if turn.Kind != "confirmation_required" || turn.PendingAction == nil || turn.PendingAction.ToolName != "record_trades" {
		h.T.Fatalf("no card: %+v", turn)
	}
	return turn.PendingAction
}

func (h *tradesHarness) confirm(action *pendingActionResponse, body any) *testutil.Response {
	h.T.Helper()
	return h.Do("POST", "/v1/ai/assistant/actions/"+action.ID+"/confirm", h.token, body)
}

func (h *tradesHarness) refused422(r *testutil.Response) tradesRefusalBody {
	h.T.Helper()
	if r.Status != 422 {
		h.T.Fatalf("status = %d: %s", r.Status, r.Body)
	}
	var body tradesRefusalBody
	r.JSON(h.T, &body)
	return body
}

func (h *tradesHarness) refused(r *testutil.Response, reason string) tradesRefusalBody {
	h.T.Helper()
	body := h.refused422(r)
	if body.Reason != reason {
		h.T.Fatalf("reason = %q, want %q", body.Reason, reason)
	}
	return body
}

func (h *tradesHarness) actionStatus(action *pendingActionResponse) string {
	h.T.Helper()
	var status string
	if err := h.Deps.DB.QueryRow(context.Background(),
		`SELECT status FROM ai_pending_actions WHERE id = $1`, uuid.MustParse(action.ID)).Scan(&status); err != nil {
		h.T.Fatal(err)
	}
	return status
}

// stillPending: the card can be retried, no audit row, nothing booked since.
func (h *tradesHarness) stillPending(action *pendingActionResponse, transactions int) {
	h.T.Helper()
	if got := h.actionStatus(action); got != statusPending {
		h.T.Fatalf("status = %s, want pending", got)
	}
	if n := h.count(`SELECT count(*) FROM ai_action_audits WHERE pending_action_id = $1`, uuid.MustParse(action.ID)); n != 0 {
		h.T.Fatalf("audit rows = %d", n)
	}
	if n := h.transactions(); n != transactions {
		h.T.Fatalf("transactions = %d, want %d", n, transactions)
	}
}

func (h *tradesHarness) settledAs(action *pendingActionResponse, status string) {
	h.T.Helper()
	if got := h.actionStatus(action); got != status {
		h.T.Fatalf("status = %s, want %s", got, status)
	}
	if n := h.count(`SELECT count(*) FROM ai_action_audits WHERE pending_action_id = $1 AND status = $2`, uuid.MustParse(action.ID), status); n != 1 {
		h.T.Fatalf("%s audit rows = %d", status, n)
	}
}

func (h *tradesHarness) transactions() int {
	h.T.Helper()
	return h.count(`SELECT count(*) FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE a.user_id = $1`, h.user)
}

func (h *tradesHarness) noCards() {
	h.T.Helper()
	if n := h.count(`SELECT count(*) FROM ai_pending_actions WHERE user_id = $1`, h.user); n != 0 {
		h.T.Fatalf("pending actions = %d", n)
	}
}

func (h *tradesHarness) newList(name string) uuid.UUID {
	h.T.Helper()
	list, err := portfoliocoredb.New(h.Deps.DB).InsertPortfolioList(context.Background(), portfolios.NewPortfolioListParams(h.user, name, false))
	if err != nil {
		h.T.Fatal(err)
	}
	return list.ID
}

func (h *tradesHarness) defaultList() uuid.UUID {
	h.T.Helper()
	id, err := portfolios.EnsureDefaultPortfolioListID(context.Background(), h.Deps.DB, h.user)
	if err != nil {
		h.T.Fatal(err)
	}
	return id
}

// hold books a buy directly, as an earlier save would have.
func (h *tradesHarness) hold(symbol string, quantity float64) {
	h.T.Helper()
	_, err := h.h.actions.stocks.RecordTrades(context.Background(), h.user, nil, []stocks.TradeInput{
		{Symbol: symbol, Side: "buy", Quantity: quantity, Price: 100, TradeDate: "2024-01-02"},
	})
	if err != nil {
		h.T.Fatal(err)
	}
}

func (h *tradesHarness) shares(list uuid.UUID, symbol string) float64 {
	h.T.Helper()
	var shares float64
	err := h.Deps.DB.QueryRow(context.Background(),
		`SELECT coalesce(sum(shares), 0) FROM stocks WHERE portfolio_list_id = $1 AND symbol = $2`, list, symbol).Scan(&shares)
	if err != nil {
		h.T.Fatal(err)
	}
	return shares
}

func modelRow(symbol, side string, quantity, price float64) string {
	return fmt.Sprintf(`{"symbol":%q,"side":%q,"quantity":%v,"price":%v,"trade_date":"2024-03-10"}`, symbol, side, quantity, price)
}

func modelArgs(rows ...string) string {
	return `{"trades":[` + strings.Join(rows, ",") + `]}`
}

func reviewed(symbol, side string, quantity, price float64, date string) map[string]any {
	return map[string]any{"symbol": symbol, "side": side, "quantity": quantity, "price": price, "tradeDate": date}
}

func reviewedBody(rows ...map[string]any) map[string]any {
	return map[string]any{"trades": rows}
}

func TestProposalStoresPreviewAndSummary(t *testing.T) {
	h := newTradesHarness(t)
	h.hold("AAPL", 10)
	h.exec(`UPDATE accounts SET base_currency = 'EUR' WHERE user_id = $1`, h.user)
	list := h.defaultList()

	action := h.card(`{"skipped_rows":2,"trades":[
		{"symbol":"msft","side":"buy","quantity":3,"price":400.5,"trade_date":"2024-03-10"},
		{"symbol":"NVDA","side":"buy","quantity":10,"price":200.5,"trade_date":"2024-03-11","currency":"usd","fees":1.5,"confidence":0.97},
		{"symbol":"BRK B","side":"BUY","quantity":1,"price":350,"trade_date":"2024-03-12"},
		{"symbol":"AAPL","side":"sell","quantity":50,"price":190,"trade_date":"2024-03-13"}
	]}`)

	if want := "Record 3 buys and 1 sell in " + defaultListName + "."; action.Summary != want {
		t.Fatalf("summary = %q, want %q", action.Summary, want)
	}
	var got clientTrades
	if err := json.Unmarshal([]byte(action.Arguments), &got); err != nil {
		t.Fatalf("arguments %s: %v", action.Arguments, err)
	}
	if !strings.EqualFold(got.PortfolioListID, list.String()) || got.SkippedRows != 2 || got.BaseCurrency != "EUR" || got.Synced || len(got.Trades) != 4 {
		t.Fatalf("arguments = %s", action.Arguments)
	}
	nvda := got.Trades[1]
	if nvda.Symbol != "NVDA" || nvda.Side != "buy" || nvda.Quantity != 10 || nvda.Price != 200.5 || nvda.TradeDate != "2024-03-11" ||
		nvda.Currency == nil || *nvda.Currency != "USD" || nvda.Fees == nil || *nvda.Fees != 1.5 || nvda.Confidence == nil || *nvda.Confidence != 0.97 {
		t.Fatalf("nvda = %+v", nvda)
	}
	if len(nvda.Issues) != 1 || nvda.Issues[0].Code != stocks.TradeIssueCurrencyMismatch || nvda.Issues[0].Blocking || nvda.Issues[0].Message == "" {
		t.Fatalf("nvda issues = %+v", nvda.Issues)
	}
	if got.Trades[0].Symbol != "MSFT" || got.Trades[2].Symbol != "BRK.B" || got.Trades[2].Side != "buy" {
		t.Fatalf("symbols not normalized: %s", action.Arguments)
	}
	if issues := got.Trades[0].Issues; issues == nil || len(issues) != 0 {
		t.Fatalf("clean row issues = %v", issues)
	}
	sell := got.Trades[3]
	if len(sell.Issues) != 1 || sell.Issues[0].Code != stocks.TradeIssueExceedsHolding || !sell.Issues[0].Blocking {
		t.Fatalf("sell issues = %+v", sell.Issues)
	}

	if n := h.transactions(); n != 1 {
		t.Fatalf("transactions = %d", n)
	}
	if n := h.count(`SELECT count(*) FROM stocks WHERE user_id = $1`, h.user); n != 1 || h.shares(list, "AAPL") != 10 {
		t.Fatalf("holdings changed: rows %d, AAPL %v", n, h.shares(list, "AAPL"))
	}
	if h.actionStatus(action) != statusPending {
		t.Fatal("card not pending")
	}
}

func TestProposalWithNoAccountReportsTheCurrencyItWouldOpen(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	var got clientTrades
	if err := json.Unmarshal([]byte(action.Arguments), &got); err != nil {
		t.Fatal(err)
	}
	if got.BaseCurrency != portfolios.ManualAccountBaseCurrency || got.SkippedRows != 0 {
		t.Fatalf("arguments = %s", action.Arguments)
	}
	if n := h.count(`SELECT count(*) FROM accounts WHERE user_id = $1`, h.user); n != 0 {
		t.Fatalf("accounts = %d", n)
	}
}

// Web and iOS parse this exact shape; a row's unread optional fields are absent.
func TestProposalArgumentsShape(t *testing.T) {
	h := newTradesHarness(t)
	list := h.defaultList()
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100.5)))
	want := `{"portfolio_list_id":"` + apijson.FormatUUID(list) + `","skipped_rows":0,"base_currency":"USD","synced":false,` +
		`"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":100.5,"trade_date":"2024-03-10","issues":[]}]}`
	if action.Arguments != want {
		t.Fatalf("arguments = %s\nwant %s", action.Arguments, want)
	}
}

func TestProposalSummaryCounts(t *testing.T) {
	h := newTradesHarness(t)
	h.defaultList()
	growth := h.newList("Growth")
	buy, sell := modelRow("AAPL", "buy", 1, 100), modelRow("AAPL", "sell", 1, 100)
	for _, tc := range []struct {
		args, want string
	}{
		{modelArgs(buy), "Record 1 buy in " + defaultListName + "."},
		{modelArgs(sell), "Record 1 sell in " + defaultListName + "."},
		{modelArgs(buy, buy), "Record 2 buys in " + defaultListName + "."},
		{modelArgs(sell, sell), "Record 2 sells in " + defaultListName + "."},
		{modelArgs(buy, sell), "Record 1 buy and 1 sell in " + defaultListName + "."},
		{modelArgs(sell, buy, buy), "Record 2 buys and 1 sell in " + defaultListName + "."},
		{`{"portfolio_list_id":"` + growth.String() + `","trades":[` + buy + `]}`, "Record 1 buy in Growth."},
	} {
		if got := h.card(tc.args).Summary; got != tc.want {
			t.Errorf("summary = %q, want %q", got, tc.want)
		}
	}
}

// The arguments come from text in an image, and every client reads what is
// stored. Only the fields the card shows survive.
func TestProposalStoresOnlyParsedFields(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(`{"confirm":true,"note":"PLANTED","trades":[
		{"symbol":"AAPL","side":"buy","quantity":1,"price":100,"trade_date":"2024-03-10","url":"PLANTED"}
	]}`)
	if strings.Contains(action.Arguments, "PLANTED") || strings.Contains(action.Summary, "PLANTED") || strings.Contains(action.Arguments, "confirm") {
		t.Fatalf("stored model text: %s / %s", action.Arguments, action.Summary)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(action.Arguments), &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"portfolio_list_id", "skipped_rows", "base_currency", "synced", "trades"} {
		if _, ok := top[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if len(top) != 5 {
		t.Errorf("keys = %d: %s", len(top), action.Arguments)
	}
	r := h.Do("GET", "/v1/ai/assistant/actions", h.token, nil)
	if r.Status != 200 || strings.Contains(string(r.Body), "PLANTED") {
		t.Fatalf("listed = %d %s", r.Status, r.Body)
	}
}

func TestProposalOverRowLimitGoesBackToModel(t *testing.T) {
	h := newTradesHarness(t)
	rows := make([]string, stocks.MaxTradeRows+1)
	for i := range rows {
		rows[i] = modelRow("AAPL", "buy", 1, 100)
	}
	turn := h.imageTurn(modelArgs(rows...))
	if turn.Kind != "message" || turn.Message.Content != "ok" || turn.PendingAction != nil {
		t.Fatalf("turn = %+v", turn)
	}
	h.noCards()
	if h.client.calls() != 2 {
		t.Fatalf("model calls = %d", h.client.calls())
	}
	last := lastMessage(h.client.seen[1])
	if last.Role != "tool" || !strings.Contains(*last.Content, `"error"`) || !strings.Contains(*last.Content, "25") {
		t.Fatalf("tool result = %+v", last)
	}
}

func TestProposalMalformedTradesGoesBackToModel(t *testing.T) {
	h := newTradesHarness(t)
	for name, args := range map[string]string{
		"not json":          `{"trades":`,
		"no trades":         `{}`,
		"trades not array":  `{"trades":"AAPL"}`,
		"empty trades":      `{"trades":[]}`,
		"row not object":    `{"trades":["AAPL"]}`,
		"missing symbol":    `{"trades":[{"side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10"}]}`,
		"blank symbol":      `{"trades":[{"symbol":"  ","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10"}]}`,
		"long symbol":       `{"trades":[{"symbol":"` + strings.Repeat("A", 200) + `","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10"}]}`,
		"unknown side":      `{"trades":[{"symbol":"AAPL","side":"dividend","quantity":1,"price":1,"trade_date":"2024-03-10"}]}`,
		"missing quantity":  `{"trades":[{"symbol":"AAPL","side":"buy","price":1,"trade_date":"2024-03-10"}]}`,
		"word quantity":     `{"trades":[{"symbol":"AAPL","side":"buy","quantity":"ten","price":1,"trade_date":"2024-03-10"}]}`,
		"NaN price":         `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":"NaN","trade_date":"2024-03-10"}]}`,
		"infinite price":    `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":"1e999","trade_date":"2024-03-10"}]}`,
		"missing date":      `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1}]}`,
		"long date":         `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1,"trade_date":"` + strings.Repeat("9", 200) + `"}]}`,
		"number currency":   `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10","currency":5}]}`,
		"word fees":         `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10","fees":"some"}]}`,
		"percent certainty": `{"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10","confidence":97}]}`,
		"word skipped":      `{"skipped_rows":"two","trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10"}]}`,
		"negative skipped":  `{"skipped_rows":-1,"trades":[{"symbol":"AAPL","side":"buy","quantity":1,"price":1,"trade_date":"2024-03-10"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			seen := h.client.calls()
			turn := h.imageTurn(args)
			if turn.Kind != "message" || turn.Message.Content != "ok" || turn.PendingAction != nil {
				t.Fatalf("turn = %+v", turn)
			}
			h.noCards()
			last := lastMessage(h.client.seen[seen+1])
			if last.Role != "tool" || !strings.Contains(*last.Content, `"error"`) {
				t.Fatalf("tool result = %+v", last)
			}
			if strings.Contains(*last.Content, "dividend") || strings.Contains(*last.Content, "ten") {
				t.Fatalf("model text echoed: %s", *last.Content)
			}
		})
	}
}

// A row that is well formed but wrong goes on the card, where the user can fix
// it, instead of back to the model.
func TestProposalInvalidValuesStayOnTheCard(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(`{"trades":[{"symbol":"AAPL","side":"buy","quantity":-1,"price":100,"trade_date":"10/28/2024"}]}`)
	var got clientTrades
	if err := json.Unmarshal([]byte(action.Arguments), &got); err != nil {
		t.Fatal(err)
	}
	row := got.Trades[0]
	if row.TradeDate != "10/28/2024" || len(row.Issues) != 1 || row.Issues[0].Code != stocks.TradeIssueInvalid || !row.Issues[0].Blocking {
		t.Fatalf("row = %+v", row)
	}
}

func TestProposalUnresolvedPortfolioGoesBackToModel(t *testing.T) {
	h := newTradesHarness(t)
	otherUser := h.CreateUser("other@example.com")
	other, err := portfoliocoredb.New(h.Deps.DB).InsertPortfolioList(context.Background(), portfolios.NewPortfolioListParams(otherUser, "Their Secret List", false))
	if err != nil {
		t.Fatal(err)
	}
	row := modelRow("AAPL", "buy", 1, 100)
	for name, list := range map[string]string{
		"another user's": `"` + other.ID.String() + `"`,
		"unknown":        `"` + uuid.NewString() + `"`,
		"not a uuid":     `"Growth"`,
		"not a string":   `7`,
	} {
		t.Run(name, func(t *testing.T) {
			seen := h.client.calls()
			turn := h.imageTurn(`{"portfolio_list_id":` + list + `,"trades":[` + row + `]}`)
			if turn.Kind != "message" || turn.PendingAction != nil {
				t.Fatalf("turn = %+v", turn)
			}
			h.noCards()
			last := lastMessage(h.client.seen[seen+1])
			if last.Role != "tool" || !strings.Contains(*last.Content, `"error"`) || strings.Contains(*last.Content, "Their Secret List") {
				t.Fatalf("tool result = %+v", *last.Content)
			}
		})
	}
}

func TestConfirmUsesReviewedRows(t *testing.T) {
	h := newTradesHarness(t)
	list := h.defaultList()
	action := h.card(modelArgs(
		modelRow("AAPL", "buy", 1, 100),
		modelRow("MSFT", "buy", 2, 200),
		modelRow("NVDA", "buy", 3, 300),
	))
	r := h.confirm(action, map[string]any{"trades": []map[string]any{
		{"symbol": "MSFT", "side": "buy", "quantity": 5, "price": 123.45, "tradeDate": "2024-03-09", "currency": "USD", "fees": 1.25},
	}})
	if r.Status != 200 {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	var out map[string]any
	r.JSON(t, &out)
	if out["actionId"] != action.ID || out["status"] != statusCompleted || out["message"] != "Trades recorded." || len(out) != 3 {
		t.Fatalf("response = %s", r.Body)
	}
	if h.shares(list, "MSFT") != 5 || h.shares(list, "AAPL") != 0 || h.shares(list, "NVDA") != 0 {
		t.Fatalf("holdings: MSFT %v AAPL %v NVDA %v", h.shares(list, "MSFT"), h.shares(list, "AAPL"), h.shares(list, "NVDA"))
	}
	var price, fees float64
	var externalID, date string
	if err := h.Deps.DB.QueryRow(context.Background(),
		`SELECT t.price, t.fees, t.external_id, t.trade_date::text FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE a.user_id = $1`, h.user).
		Scan(&price, &fees, &externalID, &date); err != nil {
		t.Fatal(err)
	}
	if math.Abs(price-123.45) > 1e-9 || fees != 1.25 || !strings.HasPrefix(externalID, stocks.ManualExternalIDPrefix) || !strings.HasPrefix(date, "2024-03-09") {
		t.Fatalf("transaction: price %v fees %v external %s date %s", price, fees, externalID, date)
	}
	if n := h.count(`SELECT count(*) FROM cash_balances b JOIN accounts a ON a.id = b.account_id WHERE a.user_id = $1`, h.user); n != 0 {
		t.Fatalf("cash balances = %d", n)
	}
	h.settledAs(action, statusCompleted)
}

func TestConfirmNoBodyRefusedCardStaysPending(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	for name, body := range map[string]any{
		"none":          nil,
		"blank":         []byte("  \n"),
		"object":        []byte(`{}`),
		"null trades":   []byte(`{"trades":null}`),
		"only the list": []byte(`{"portfolioListId":"` + uuid.NewString() + `"}`),
	} {
		if got := h.refused422(h.confirm(action, body)); got.Reason != noBodyRefusal {
			t.Errorf("%s: reason = %q", name, got.Reason)
		}
		h.stillPending(action, 0)
	}
}

func TestConfirmEmptyTradesRefused(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	r := h.confirm(action, []byte(`{"trades":[]}`))
	if r.Status != 422 || string(r.Body) != `{"reason":"`+noneSelected+`","rowErrors":[]}` {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	h.stillPending(action, 0)
}

func TestConfirmBadRowKeepsCardPending(t *testing.T) {
	h := newTradesHarness(t)
	h.hold("AAPL", 10)
	list := h.defaultList()
	action := h.card(modelArgs(modelRow("AAPL", "sell", 1, 150)))

	// The sell is earlier, so RecordTrades books it first. Its index is still
	// its place in the submitted array.
	bad := reviewedBody(
		reviewed("MSFT", "buy", 1, 100, "2024-03-12"),
		reviewed("AAPL", "sell", 500, 150, "2024-03-10"),
	)
	r := h.confirm(action, bad)
	body := h.refused422(r)
	if !strings.HasPrefix(string(r.Body), `{"reason":"`) || !strings.Contains(string(r.Body), `,"rowErrors":[{"index":1,"code":"exceeds_holding","message":"`) {
		t.Fatalf("body = %s", r.Body)
	}
	if len(body.RowErrors) != 1 || body.RowErrors[0].Index != 1 || body.RowErrors[0].Code != stocks.TradeIssueExceedsHolding || body.RowErrors[0].Message == "" {
		t.Fatalf("row errors = %+v", body.RowErrors)
	}
	if body.Reason != body.RowErrors[0].Message {
		t.Fatalf("reason = %q", body.Reason)
	}
	h.stillPending(action, 1)
	if h.shares(list, "AAPL") != 10 || h.shares(list, "MSFT") != 0 {
		t.Fatal("holdings changed on a refused batch")
	}

	r = h.confirm(action, reviewedBody(
		reviewed("MSFT", "buy", 1, 100, "2024-03-12"),
		reviewed("AAPL", "sell", 4, 150, "2024-03-10"),
	))
	if r.Status != 200 {
		t.Fatalf("retry = %d %s", r.Status, r.Body)
	}
	if h.shares(list, "AAPL") != 6 || h.shares(list, "MSFT") != 1 {
		t.Fatalf("after retry: AAPL %v MSFT %v", h.shares(list, "AAPL"), h.shares(list, "MSFT"))
	}
	h.settledAs(action, statusCompleted)
}

// A warning is the user's call, and they made it by saving.
func TestConfirmSavesRowsWithWarnings(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	row := reviewed("AAPL", "buy", 1, 100, "2024-03-10")
	row["currency"] = "EUR"
	if r := h.confirm(action, reviewedBody(row)); r.Status != 200 {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
}

// A row a client sends with a field it could not parse is that row's error,
// not a decode failure for the whole batch.
func TestConfirmRowMissingFieldIsARowError(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	body := h.refused422(h.confirm(action, []byte(`{"trades":[
		{"symbol":"AAPL","side":"buy","quantity":1,"price":100,"tradeDate":"2024-03-10"},
		{"symbol":"MSFT","side":"buy","quantity":1,"tradeDate":"2024-03-10"}
	]}`)))
	if len(body.RowErrors) != 1 || body.RowErrors[0].Index != 1 || body.RowErrors[0].Code != stocks.TradeIssueInvalid {
		t.Fatalf("row errors = %+v", body.RowErrors)
	}
	h.stillPending(action, 0)
}

func TestConfirmUnreadableBodyKeepsCardPending(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	for _, body := range []string{`{"trades":`, `{"trades":[{"symbol":"AAPL","quantity":"ten"}]}`, `[]`} {
		if r := h.confirm(action, []byte(body)); r.Status != 400 {
			t.Fatalf("%s = %d %s", body, r.Status, r.Body)
		}
		h.stillPending(action, 0)
	}
}

func TestConfirmOverRowLimitRefused(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	rows := make([]map[string]any, stocks.MaxTradeRows+1)
	for i := range rows {
		rows[i] = reviewed("AAPL", "buy", 1, 100, "2024-03-10")
	}
	body := h.refused422(h.confirm(action, reviewedBody(rows...)))
	if !strings.Contains(body.Reason, "25") {
		t.Fatalf("reason = %q", body.Reason)
	}
	h.stillPending(action, 0)
}

func TestConfirmExpiredIs409(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	h.exec(`UPDATE ai_pending_actions SET expires_at = now() - interval '1 minute' WHERE id = $1`, uuid.MustParse(action.ID))
	// Past its time a card answers 409 whatever is sent, so a stale tab learns
	// to send the screenshot again instead of fixing rows that can't save.
	valid := reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10"))
	invalid := reviewedBody(reviewed("AAPL", "sell", 500, 100, "2024-03-10"))
	for _, body := range []any{valid, invalid, nil, []byte(`{"trades":[]}`)} {
		if r := h.confirm(action, body); r.Status != 409 {
			t.Fatalf("confirm = %d %s", r.Status, r.Body)
		}
	}
	h.stillPending(action, 0)
}

func TestDoubleConfirmIs409(t *testing.T) {
	h := newTradesHarness(t)
	list := h.defaultList()
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	body := reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10"))
	if r := h.confirm(action, body); r.Status != 200 {
		t.Fatalf("first = %d %s", r.Status, r.Body)
	}
	if r := h.confirm(action, body); r.Status != 409 {
		t.Fatalf("second = %d %s", r.Status, r.Body)
	}
	if h.shares(list, "AAPL") != 1 || h.transactions() != 1 {
		t.Fatalf("booked twice: shares %v transactions %d", h.shares(list, "AAPL"), h.transactions())
	}
	h.settledAs(action, statusCompleted)
}

func TestConfirmUnknownActionIs404(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	stranger := h.CreateUser("stranger@example.com")
	body := reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10"))
	if r := h.Do("POST", "/v1/ai/assistant/actions/"+action.ID+"/confirm", h.Token(stranger), body); r.Status != 404 {
		t.Fatalf("stranger = %d %s", r.Status, r.Body)
	}
	h.stillPending(action, 0)
}

func TestConfirmOtherToolsUnchanged(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("goal@example.com")
	conv := th.conversation(user)
	created := th.h.actions.execute(context.Background(), "add_goal", parseArgs(`{"title":"Trip"}`), user, modeConfirmed)
	var goal goalResponse
	if err := json.Unmarshal([]byte(created), &goal); err != nil {
		t.Fatal(err)
	}
	th.client.replies = []ai.Message{callTool("c1", "delete_goal", `{"id":"`+goal.ID+`"}`)}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "delete it"})
	var turn turnResponse
	r.JSON(t, &turn)
	// Free plan, no body: neither the entitlement nor the body rule applies.
	r = th.Do("POST", "/v1/ai/assistant/actions/"+turn.PendingAction.ID+"/confirm", th.Token(user), nil)
	if r.Status != 200 {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	var out map[string]any
	r.JSON(t, &out)
	if out["status"] != statusCompleted || out["message"] != "Goal deleted." || out["actionId"] != turn.PendingAction.ID {
		t.Fatalf("response = %s", r.Body)
	}
	if n := th.count(`SELECT count(*) FROM goals WHERE user_id = $1`, user); n != 0 {
		t.Fatalf("goals = %d", n)
	}
}

func TestConfirmSyncedPortfolioRefused(t *testing.T) {
	h := newTradesHarness(t)
	list := h.defaultList()
	synced := h.newList("Synced")
	h.exec(`INSERT INTO broker_connections (id, user_id, provider, status, portfolio_list_id, created_at)
		VALUES ($1, $2, 'ibkr', 'connected', $3, now())`, uuid.New(), h.user, synced)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	row := reviewed("AAPL", "buy", 1, 100, "2024-03-10")

	body := h.refused(h.confirm(action, map[string]any{"portfolioListId": synced.String(), "trades": []any{row}}), stocks.IBKRSyncedRefusal)
	if body.RowErrors == nil || len(body.RowErrors) != 0 {
		t.Fatalf("rowErrors = %v", body.RowErrors)
	}
	h.stillPending(action, 0)

	r := h.confirm(action, map[string]any{"portfolioListId": list.String(), "trades": []any{row}})
	if r.Status != 200 {
		t.Fatalf("other list = %d %s", r.Status, r.Body)
	}
	if h.shares(list, "AAPL") != 1 || h.shares(synced, "AAPL") != 0 {
		t.Fatal("booked on the wrong list")
	}
	h.settledAs(action, statusCompleted)
}

func TestProposalOnSyncedPortfolioSaysSo(t *testing.T) {
	h := newTradesHarness(t)
	synced := h.newList("Synced")
	h.exec(`INSERT INTO broker_connections (id, user_id, provider, status, portfolio_list_id, created_at)
		VALUES ($1, $2, 'ibkr', 'connected', $3, now())`, uuid.New(), h.user, synced)
	action := h.card(`{"portfolio_list_id":"` + synced.String() + `","trades":[` + modelRow("AAPL", "buy", 1, 100) + `]}`)
	var got clientTrades
	if err := json.Unmarshal([]byte(action.Arguments), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Synced {
		t.Fatalf("arguments = %s", action.Arguments)
	}
}

// PreviewTrades does not report a follow and RecordTrades answers it with 409,
// which reads as a dead card. Checked before the claim, it is a 422 the user
// can act on.
func TestConfirmFollowedPortfolioKeepsCardPending(t *testing.T) {
	h := newTradesHarness(t)
	list := h.defaultList()
	followed := h.newList("Followed")
	pilot := uuid.New()
	h.exec(`INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'fund', $2, 'P')`, pilot, "p-"+pilot.String())
	h.exec(`INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id) VALUES ($1, $2, 'portfolio', $3)`, h.user, pilot, followed)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	row := reviewed("AAPL", "buy", 1, 100, "2024-03-10")

	h.refused(h.confirm(action, map[string]any{"portfolioListId": followed.String(), "trades": []any{row}}), portfolios.ErrPilotFollowed().Reason)
	h.stillPending(action, 0)

	if r := h.confirm(action, map[string]any{"portfolioListId": list.String(), "trades": []any{row}}); r.Status != 200 {
		t.Fatalf("other list = %d %s", r.Status, r.Body)
	}
}

// The turn's gate says nothing about a downgrade between the card and the tap.
func TestConfirmAfterDowngradeKeepsCardPending(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	h.exec(`DELETE FROM entitlements WHERE user_id = $1`, h.user)

	r := h.confirm(action, reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10")))
	if r.Status != 403 {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	var out entitlements.UpgradeRequiredResponse
	r.JSON(t, &out)
	if out.Code != "upgrade_required" || out.Feature != string(entitlements.FeatureScreenshotImport) {
		t.Fatalf("body = %s", r.Body)
	}
	h.stillPending(action, 0)

	h.makePro(h.user)
	if r := h.confirm(action, reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10"))); r.Status != 200 {
		t.Fatalf("after upgrade = %d %s", r.Status, r.Body)
	}
}

func TestConfirmAnotherUsersListRefused(t *testing.T) {
	h := newTradesHarness(t)
	otherUser := h.CreateUser("other@example.com")
	other, err := portfoliocoredb.New(h.Deps.DB).InsertPortfolioList(context.Background(), portfolios.NewPortfolioListParams(otherUser, "Their Secret List", false))
	if err != nil {
		t.Fatal(err)
	}
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	row := reviewed("AAPL", "buy", 1, 100, "2024-03-10")

	r := h.confirm(action, map[string]any{"portfolioListId": other.ID.String(), "trades": []any{row}})
	if r.Status != 404 || strings.Contains(string(r.Body), "Their Secret List") {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	h.stillPending(action, 0)
	if n := h.count(`SELECT count(*) FROM stocks WHERE portfolio_list_id = $1`, other.ID); n != 0 {
		t.Fatalf("wrote into another user's list: %d rows", n)
	}

	if r := h.confirm(action, map[string]any{"portfolioListId": "not-a-uuid", "trades": []any{row}}); r.Status != 400 {
		t.Fatalf("bad id = %d %s", r.Status, r.Body)
	}
	h.stillPending(action, 0)
}

// With no list in the body the save lands where the card said it would, never
// on a default the user did not see.
func TestConfirmWithoutListUsesTheCardsList(t *testing.T) {
	h := newTradesHarness(t)
	list := h.defaultList()
	growth := h.newList("Growth")
	action := h.card(`{"portfolio_list_id":"` + growth.String() + `","trades":[` + modelRow("AAPL", "buy", 1, 100) + `]}`)
	if r := h.confirm(action, reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10"))); r.Status != 200 {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	if h.shares(growth, "AAPL") != 1 || h.shares(list, "AAPL") != 0 {
		t.Fatalf("growth %v default %v", h.shares(growth, "AAPL"), h.shares(list, "AAPL"))
	}
}

// What the preview cleared can stop being true before the save: these triggers
// change the world at the moment of the claim, as another save would.
func TestConfirmRaceFailureSettlesFailed(t *testing.T) {
	t.Run("rows no longer save", func(t *testing.T) {
		h := newTradesHarness(t)
		h.hold("AAPL", 10)
		action := h.card(modelArgs(modelRow("AAPL", "sell", 5, 150)))
		h.exec(`CREATE FUNCTION lose_holding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN DELETE FROM stocks; RETURN NEW; END $$`)
		h.exec(`CREATE TRIGGER lose_holding AFTER INSERT ON ai_action_audits FOR EACH ROW EXECUTE FUNCTION lose_holding()`)

		body := h.refused422(h.confirm(action, reviewedBody(reviewed("AAPL", "sell", 5, 150, "2024-03-10"))))
		if len(body.RowErrors) != 1 || body.RowErrors[0].Index != 0 || body.RowErrors[0].Code != stocks.TradeIssueNoHolding {
			t.Fatalf("row errors = %+v", body.RowErrors)
		}
		h.settledAs(action, statusFailed)
		if h.transactions() != 1 {
			t.Fatalf("transactions = %d", h.transactions())
		}
	})
	t.Run("list followed since the check", func(t *testing.T) {
		h := newTradesHarness(t)
		action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
		pilot := uuid.New()
		h.exec(`INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'fund', $2, 'P')`, pilot, "p-"+pilot.String())
		h.exec(fmt.Sprintf(`CREATE FUNCTION start_follow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id) VALUES ('%s', '%s', 'portfolio', '%s'); RETURN NEW; END $$`, h.user, pilot, h.defaultList()))
		h.exec(`CREATE TRIGGER start_follow AFTER INSERT ON ai_action_audits FOR EACH ROW EXECUTE FUNCTION start_follow()`)

		h.refused(h.confirm(action, reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10"))), portfolios.ErrPilotFollowed().Reason)
		h.settledAs(action, statusFailed)
	})
	t.Run("unexpected failure says nothing of the cause", func(t *testing.T) {
		h := newTradesHarness(t)
		action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
		h.exec(`CREATE FUNCTION refuse_row() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'internal detail'; END $$`)
		h.exec(`CREATE TRIGGER refuse_row BEFORE INSERT ON transactions FOR EACH ROW EXECUTE FUNCTION refuse_row()`)

		r := h.confirm(action, reviewedBody(reviewed("AAPL", "buy", 1, 100, "2024-03-10")))
		h.refused(r, "That action could not be completed.")
		if strings.Contains(string(r.Body), "internal detail") {
			t.Fatalf("leaked: %s", r.Body)
		}
		h.settledAs(action, statusFailed)
		if n := h.count(`SELECT count(*) FROM stocks WHERE user_id = $1`, h.user); n != 0 {
			t.Fatalf("stocks = %d", n)
		}
	})
}

// Telegram's "yes" reaches h.confirm with no rows to save. Nothing books.
func TestExecutorRefusesRecordTrades(t *testing.T) {
	h := newTradesHarness(t)
	action := h.card(modelArgs(modelRow("AAPL", "buy", 1, 100)))
	if _, err := h.h.confirm(context.Background(), uuid.MustParse(action.ID), h.user); err == nil {
		t.Fatal("executor ran record_trades")
	}
	if h.transactions() != 0 || h.count(`SELECT count(*) FROM stocks WHERE user_id = $1`, h.user) != 0 {
		t.Fatal("executor booked trades")
	}
}

func TestActionCatalogOmitsRecordTrades(t *testing.T) {
	h := newTradesHarness(t)
	r := h.Do("GET", "/v1/actions/catalog", h.token, nil)
	if r.Status != 200 || strings.Contains(string(r.Body), "record_trades") {
		t.Fatalf("catalog = %d %s", r.Status, r.Body)
	}
}
