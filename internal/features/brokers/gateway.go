package brokers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
)

// gatewayClient is IBKRBrokerGatewayClient: the Client Portal gateway (or the
// OAuth Web API when AccessToken is set).
type gatewayClient struct {
	BaseURL         string
	DefaultCurrency string
	AccessToken     *string
	HTTP            *http.Client
	// Sleep waits between retries; nil is time.Sleep honoring ctx.
	Sleep func(ctx context.Context, d time.Duration) error
}

type ibkrAccount struct {
	ExternalID  string
	DisplayName *string
}

type ibkrPosition struct {
	Symbol      string
	Quantity    float64
	AverageCost float64
	Currency    string
	Conid       *string
	AssetClass  *string
}

type ibkrTransaction struct {
	ExternalID string
	Symbol     string
	Type       string
	Quantity   float64
	Amount     float64
	Price      *float64
	Fees       float64
	Currency   string
	Date       time.Time
	SettleDate *time.Time
	Conid      *string
	AssetClass *string
}

type ibkrCashBalance struct {
	Currency string
	Amount   float64
}

type ibkrDividend struct {
	ExternalID *string
	Symbol     string
	Amount     float64
	Currency   string
	Date       time.Time
}

// withholdingRow is IBKRWithholdingRow.
type withholdingRow struct {
	Symbol   string
	PayDate  time.Time
	Amount   float64
	Currency string
}

// unsupportedAssetClasses is IBKRAssetClass.unsupported: IBKR reports these
// under the underlying's ticker; storing one would overwrite that stock.
var unsupportedAssetClasses = map[string]bool{"OPT": true, "FOP": true, "FUT": true, "WAR": true, "IOPT": true}

func isUnsupportedAssetClass(assetClass *string) bool {
	if assetClass == nil {
		return false
	}
	return unsupportedAssetClasses[strings.ToUpper(portfolios.TrimSwift(*assetClass))]
}

// clientResponse is a buffered upstream reply.
type clientResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

func (g *gatewayClient) sleep(ctx context.Context, d time.Duration) error {
	if g.Sleep != nil {
		return g.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (g *gatewayClient) httpClient() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return http.DefaultClient
}

func (g *gatewayClient) makeURL(path string) string {
	base := strings.TrimSuffix(g.BaseURL, "/")
	return base + "/" + strings.TrimPrefix(path, "/")
}

func (g *gatewayClient) do(ctx context.Context, method, path string) (clientResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.makeURL(path), nil)
	if err != nil {
		return clientResponse{}, err
	}
	if g.AccessToken != nil && portfolios.TrimSwift(*g.AccessToken) != "" {
		req.Header.Set("Authorization", "Bearer "+*g.AccessToken)
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return clientResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return clientResponse{}, err
	}
	return clientResponse{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}

// withRetry is withRetry(maxRetries: 3): transport failures retry after 1s
// then 2s; on a gateway session (no access token) each retry first checks
// the auth status and reauthenticates.
func (g *gatewayClient) withRetry(ctx context.Context, op func() (clientResponse, error)) (clientResponse, error) {
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		resp, err := func() (clientResponse, error) {
			if attempt > 0 && g.AccessToken == nil {
				ok, err := g.checkAuthStatus(ctx)
				if err != nil {
					return clientResponse{}, err
				}
				if !ok {
					if err := g.reauthenticate(ctx); err != nil {
						return clientResponse{}, err
					}
				}
			}
			return op()
		}()
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt < maxRetries-1 {
			if serr := g.sleep(ctx, time.Duration(1<<attempt)*time.Second); serr != nil {
				return clientResponse{}, serr
			}
		}
	}
	return clientResponse{}, lastErr
}

func (g *gatewayClient) checkAuthStatus(ctx context.Context) (bool, error) {
	resp, err := g.do(ctx, http.MethodGet, "/iserver/auth/status")
	if err != nil {
		return false, err
	}
	if resp.Status != http.StatusOK {
		return false, nil
	}
	obj, ok := decodeObject(resp)
	if !ok {
		return false, nil
	}
	var authenticated *bool
	if raw, ok := obj["authenticated"]; ok && !isJSONNull(raw) {
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return false, nil
		}
		authenticated = &b
	}
	return authenticated != nil && *authenticated, nil
}

func (g *gatewayClient) reauthenticate(ctx context.Context) error {
	resp, err := g.do(ctx, http.MethodPost, "/iserver/reauthenticate")
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return httpx.Abort(http.StatusBadGateway, fmt.Sprintf("IBKR reauthentication failed with status %d.", resp.Status))
	}
	return nil
}

// requirePrimaryAccount is the first account the gateway lists.
func (g *gatewayClient) requirePrimaryAccount(ctx context.Context) (ibkrAccount, error) {
	resp, err := g.withRetry(ctx, func() (clientResponse, error) { return g.do(ctx, http.MethodGet, "/portfolio/accounts") })
	if err != nil {
		return ibkrAccount{}, err
	}
	if resp.Status != http.StatusOK {
		return ibkrAccount{}, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("IBKR accounts request failed with status %d.", resp.Status))
	}
	accounts, ok := decodeList(resp, "accounts", parseAccount)
	if !ok {
		return ibkrAccount{}, httpx.Abort(http.StatusBadGateway, "IBKR accounts response format was not recognized.")
	}
	if len(accounts) == 0 {
		return ibkrAccount{}, httpx.Abort(http.StatusBadGateway, "IBKR did not return any accounts.")
	}
	return accounts[0], nil
}

func (g *gatewayClient) fetchPositions(ctx context.Context, accountID string) ([]ibkrPosition, error) {
	resp, err := g.withRetry(ctx, func() (clientResponse, error) {
		return g.do(ctx, http.MethodGet, "/portfolio/"+accountID+"/positions/0")
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("IBKR positions request failed with status %d.", resp.Status))
	}
	positions, ok := decodeList(resp, "data", func(o map[string]json.RawMessage) (*ibkrPosition, error) {
		return parsePosition(o, g.DefaultCurrency)
	})
	if !ok {
		return nil, httpx.Abort(http.StatusBadGateway, "IBKR positions response format was not recognized.")
	}
	return positions, nil
}

// fetchListingExchange is fetchContractInfo(conid:).listingExchange: nil on
// any non-200 or undecodable reply.
func (g *gatewayClient) fetchListingExchange(ctx context.Context, conid string) (*string, bool, error) {
	resp, err := g.withRetry(ctx, func() (clientResponse, error) {
		return g.do(ctx, http.MethodGet, "/iserver/contract/"+conid+"/info")
	})
	if err != nil {
		return nil, false, err
	}
	if resp.Status != http.StatusOK {
		return nil, false, nil
	}
	obj, ok := decodeObject(resp)
	if !ok {
		return nil, false, nil
	}
	v, err := optString(obj, "listingExchange")
	if err != nil {
		return nil, false, nil
	}
	return v, true, nil
}

func (g *gatewayClient) fetchTransactions(ctx context.Context, accountID string, from, to time.Time) ([]ibkrTransaction, error) {
	path := "/pa/transactions?accountId=" + accountID + "&from=" + from.UTC().Format("2006-01-02") + "&to=" + to.UTC().Format("2006-01-02")
	resp, err := g.withRetry(ctx, func() (clientResponse, error) { return g.do(ctx, http.MethodGet, path) })
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("IBKR transactions request failed with status %d.", resp.Status))
	}
	txs, ok := decodeList(resp, "transactions", parseTransaction)
	if !ok {
		return []ibkrTransaction{}, nil
	}
	return txs, nil
}

func (g *gatewayClient) fetchCashBalances(ctx context.Context, accountID string) ([]ibkrCashBalance, error) {
	resp, err := g.withRetry(ctx, func() (clientResponse, error) {
		return g.do(ctx, http.MethodGet, "/portfolio/"+accountID+"/ledger")
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("IBKR cash balances request failed with status %d.", resp.Status))
	}
	obj, ok := decodeObject(resp)
	if !ok {
		return []ibkrCashBalance{}, nil
	}
	ledgers := map[string]map[string]*lossy{}
	for _, cur := range []string{"BASE", "USD", "EUR", "GBP", "JPY", "CHF", "CAD", "AUD"} {
		raw, ok := obj[cur]
		if !ok || isJSONNull(raw) {
			continue
		}
		inner, ok := objectOf(raw)
		if !ok {
			return []ibkrCashBalance{}, nil
		}
		m := map[string]*lossy{}
		for k, v := range inner {
			if isJSONNull(v) {
				// [String: BrokerLossyValue] has no optional slot for null.
				return []ibkrCashBalance{}, nil
			}
			l, err := parseLossy(v)
			if err != nil {
				return []ibkrCashBalance{}, nil
			}
			m[k] = l
		}
		ledgers[cur] = m
	}
	balance := func(m map[string]*lossy) (float64, bool) {
		if v := m["cashbalance"]; v != nil {
			if d, ok := v.double(); ok {
				return d, true
			}
		}
		if v := m["totalcashvalue"]; v != nil {
			if d, ok := v.double(); ok {
				return d, true
			}
		}
		return 0, false
	}
	out := []ibkrCashBalance{}
	for _, cur := range []string{"USD", "EUR", "GBP", "JPY", "CHF", "CAD", "AUD"} {
		if m, ok := ledgers[cur]; ok {
			if amount, ok := balance(m); ok {
				out = append(out, ibkrCashBalance{Currency: cur, Amount: amount})
			}
		}
	}
	if m, ok := ledgers["BASE"]; ok {
		if amount, ok := balance(m); ok {
			out = append(out, ibkrCashBalance{Currency: "BASE", Amount: amount})
		}
	}
	return out, nil
}

// fetchDividendsAndWithholdings reads one statement for both: dividend lines
// and withholding-tax lines.
func (g *gatewayClient) fetchDividendsAndWithholdings(ctx context.Context, accountID string, from, to time.Time) ([]ibkrDividend, []withholdingRow, error) {
	txs, err := g.fetchTransactions(ctx, accountID, from, to)
	if err != nil {
		return nil, nil, err
	}
	dividends := []ibkrDividend{}
	withholdings := []withholdingRow{}
	for _, t := range txs {
		switch {
		case t.Type == "DIV" || t.Type == "DIVIDEND":
			id := t.ExternalID
			dividends = append(dividends, ibkrDividend{ExternalID: &id, Symbol: t.Symbol, Amount: t.Amount, Currency: t.Currency, Date: t.Date})
		case isWithholdingType(t.Type):
			withholdings = append(withholdings, withholdingRow{Symbol: t.Symbol, PayDate: t.Date, Amount: t.Amount, Currency: t.Currency})
		}
	}
	return dividends, withholdings, nil
}

// isWithholdingType is DividendWithholdingReconciler.isWithholdingType.
func isWithholdingType(t string) bool {
	n := strings.ReplaceAll(strings.ToUpper(t), "_", " ")
	return n == "WHTAX" || strings.Contains(n, "WITHHOLDING")
}

// MARK: - Payload decoding (Vapor's content.decode with JSONDecoder.backendAPI)

// isJSONContent is whether Vapor's content.decode would pick its JSON
// decoder for this reply.
func isJSONContent(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// objectOf decodes a JSON object with backendAPI's key rewrite: every key
// holding "_" becomes its camelCase spelling. Keys then match exactly, as
// Swift's CodingKeys did (Go's case-insensitive matching would not).
func objectOf(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	out := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		if strings.Contains(k, "_") {
			k = apijson.NormalizeSnakeCaseKey(k)
		}
		out[k] = v
	}
	return out, true
}

func decodeObject(resp clientResponse) (map[string]json.RawMessage, bool) {
	if !isJSONContent(resp.ContentType) {
		return nil, false
	}
	return objectOf(resp.Body)
}

// decodeList tries `[Payload]`, then `{envelopeKey: [Payload]}`. Any element
// that fails to decode fails the whole attempt; an element whose parse
// returns nil is dropped (compactMap).
func decodeList[T any](resp clientResponse, envelopeKey string, parse func(map[string]json.RawMessage) (*T, error)) ([]T, bool) {
	if !isJSONContent(resp.ContentType) {
		return nil, false
	}
	parseArray := func(raw json.RawMessage) ([]T, bool) {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return nil, false
		}
		out := []T{}
		for _, item := range items {
			obj, ok := objectOf(item)
			if !ok {
				return nil, false
			}
			v, err := parse(obj)
			if err != nil {
				return nil, false
			}
			if v != nil {
				out = append(out, *v)
			}
		}
		return out, true
	}
	if out, ok := parseArray(resp.Body); ok {
		return out, true
	}
	obj, ok := objectOf(resp.Body)
	if !ok {
		return nil, false
	}
	raw, ok := obj[envelopeKey]
	if !ok {
		return nil, false
	}
	return parseArray(raw)
}

var errTypeMismatch = errors.New("type mismatch")

func optString(obj map[string]json.RawMessage, key string) (*string, error) {
	raw, ok := obj[key]
	if !ok || isJSONNull(raw) {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errTypeMismatch
	}
	return &s, nil
}

// lossy is BrokerLossyValue: a string (trimmed), integer, double or bool,
// kept as its string form.
type lossy struct{ s string }

func (l *lossy) double() (float64, bool) {
	return swiftDouble(strings.ReplaceAll(l.s, ",", ""))
}

func parseLossy(raw json.RawMessage) (*lossy, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return &lossy{s: portfolios.TrimSwift(s)}, nil
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&n) == nil {
		if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
			return &lossy{s: strconv.FormatInt(i, 10)}, nil
		}
		f, err := n.Float64()
		if err != nil {
			return nil, errTypeMismatch
		}
		if f == float64(int64(f)) && f >= -9.2e18 && f <= 9.2e18 {
			// Foundation decodes an integral double like 10.0 as Int.
			return &lossy{s: strconv.FormatInt(int64(f), 10)}, nil
		}
		return &lossy{s: swiftDoubleDescription(f)}, nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if b {
			return &lossy{s: "true"}, nil
		}
		return &lossy{s: "false"}, nil
	}
	return nil, errTypeMismatch
}

func optLossy(obj map[string]json.RawMessage, key string) (*lossy, error) {
	raw, ok := obj[key]
	if !ok || isJSONNull(raw) {
		return nil, nil
	}
	return parseLossy(raw)
}

func lossyString(l *lossy) *string {
	if l == nil {
		return nil
	}
	s := l.s
	return &s
}

func lossyDouble(l *lossy) (float64, bool) {
	if l == nil {
		return 0, false
	}
	return l.double()
}

// swiftDouble is Double(String): decimal or exponent forms, "inf"/"nan"
// spellings, no surrounding whitespace, no hex.
func swiftDouble(s string) (float64, bool) {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, "_xX") {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// swiftDoubleDescription is String(Double) for non-integral values.
func swiftDoubleDescription(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func parseAccount(o map[string]json.RawMessage) (*ibkrAccount, error) {
	id, err := optLossy(o, "id")
	if err != nil {
		return nil, err
	}
	accountID, err := optLossy(o, "accountId")
	if err != nil {
		return nil, err
	}
	accountIDUpper, err := optLossy(o, "accountID")
	if err != nil {
		return nil, err
	}
	title, err := optString(o, "accountTitle")
	if err != nil {
		return nil, err
	}
	display, err := optString(o, "displayName")
	if err != nil {
		return nil, err
	}
	alias, err := optString(o, "alias")
	if err != nil {
		return nil, err
	}
	var external *string
	for _, l := range []*lossy{id, accountID, accountIDUpper} {
		if l != nil {
			external = lossyString(l)
			break
		}
	}
	if external == nil || *external == "" {
		return nil, nil
	}
	var name *string
	for _, c := range []*string{display, title, alias} {
		if c != nil {
			if t := portfolios.TrimSwift(*c); t != "" {
				name = &t
				break
			}
		}
	}
	return &ibkrAccount{ExternalID: *external, DisplayName: name}, nil
}

func parsePosition(o map[string]json.RawMessage, defaultCurrency string) (*ibkrPosition, error) {
	var strs [5]*string
	for i, k := range []string{"ticker", "symbol", "contractDesc", "currency", "assetClass"} {
		v, err := optString(o, k)
		if err != nil {
			return nil, err
		}
		strs[i] = v
	}
	var nums [6]*lossy
	for i, k := range []string{"position", "qty", "avgPrice", "averagePrice", "avgCost", "conid"} {
		v, err := optLossy(o, k)
		if err != nil {
			return nil, err
		}
		nums[i] = v
	}
	var symbol string
	for _, c := range strs[:3] {
		if c != nil {
			if t := strings.ToUpper(portfolios.TrimSwift(*c)); t != "" {
				symbol = t
				break
			}
		}
	}
	if symbol == "" {
		return nil, nil
	}
	quantity := firstDouble(nums[0], nums[1])
	averageCost := firstDouble(nums[2], nums[3], nums[4])
	currency := defaultCurrency
	if strs[3] != nil {
		if c := strings.ToUpper(portfolios.TrimSwift(*strs[3])); c != "" {
			currency = c
		}
	}
	return &ibkrPosition{
		Symbol: symbol, Quantity: quantity, AverageCost: averageCost, Currency: currency,
		Conid: lossyString(nums[5]), AssetClass: strs[4],
	}, nil
}

// firstDouble is `a?.doubleValue ?? b?.doubleValue ?? ... ?? 0`.
func firstDouble(values ...*lossy) float64 {
	for _, v := range values {
		if d, ok := lossyDouble(v); ok {
			return d
		}
	}
	return 0
}

func parseTransaction(o map[string]json.RawMessage) (*ibkrTransaction, error) {
	lossies := map[string]*lossy{}
	for _, k := range []string{"transactionID", "id", "quantity", "amount", "price", "tradePrice", "commission", "fee", "conid"} {
		v, err := optLossy(o, k)
		if err != nil {
			return nil, err
		}
		lossies[k] = v
	}
	strs := map[string]*string{}
	for _, k := range []string{"symbol", "type", "currency", "date", "tradeDate", "settleDate", "assetClass"} {
		v, err := optString(o, k)
		if err != nil {
			return nil, err
		}
		strs[k] = v
	}
	var external string
	if l := lossies["transactionID"]; l != nil {
		external = l.s
	} else if l := lossies["id"]; l != nil {
		external = l.s
	}
	if external == "" {
		return nil, nil
	}
	if strs["symbol"] == nil || *strs["symbol"] == "" {
		return nil, nil
	}
	if strs["type"] == nil || *strs["type"] == "" {
		return nil, nil
	}
	dateRaw := strs["date"]
	if dateRaw == nil {
		dateRaw = strs["tradeDate"]
	}
	date, ok := parseGatewayDate(dateRaw)
	if !ok {
		return nil, nil
	}
	var price *float64
	if d, ok := lossyDouble(lossies["tradePrice"]); ok {
		price = &d
	} else if d, ok := lossyDouble(lossies["price"]); ok {
		price = &d
	}
	currency := "USD"
	if strs["currency"] != nil {
		currency = strings.ToUpper(portfolios.TrimSwift(*strs["currency"]))
	}
	t := &ibkrTransaction{
		ExternalID: external,
		Symbol:     strings.ToUpper(portfolios.TrimSwift(*strs["symbol"])),
		Type:       strings.ToUpper(*strs["type"]),
		Quantity:   firstDouble(lossies["quantity"]),
		Amount:     firstDouble(lossies["amount"]),
		Price:      price,
		Fees:       abs(firstDouble(lossies["commission"], lossies["fee"])),
		Currency:   currency,
		Date:       date,
		Conid:      lossyString(lossies["conid"]),
		AssetClass: strs["assetClass"],
	}
	if settle, ok := parseGatewayDate(strs["settleDate"]); ok {
		t.SettleDate = &settle
	}
	return t, nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// parseGatewayDate is IBKRBrokerTransactionPayload.parseDate: ISO 8601
// (internet date-time), then yyyy-MM-dd, then yyyyMMdd, all UTC.
func parseGatewayDate(v *string) (time.Time, bool) {
	if v == nil || *v == "" {
		return time.Time{}, false
	}
	s := *v
	if t, err := time.Parse(time.RFC3339, s); err == nil && !strings.Contains(s, ".") {
		return t.UTC(), true
	}
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
