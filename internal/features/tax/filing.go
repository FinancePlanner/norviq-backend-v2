package tax

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/httpx"
	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

// The annual filing pack (Tax/Filing/*.swift): the year's lot disposals and
// dividends converted at ECB rates (FilingLedgerBuilder, FXRateResolver,
// ECBDailyRateProvider), mapped to the national form by a country mapper.

// filingDisposal is FilingDisposal.
type filingDisposal struct {
	symbol           string
	isin             *string
	instrumentType   string
	quantity         dec.Decimal
	acquisitionDate  time.Time
	acquisitionValue dec.Decimal
	realizationDate  time.Time
	realizationValue dec.Decimal
	expenses         dec.Decimal
	gain             dec.Decimal
	holdingPeriod    string
	sourceCountry    *string
	fx               []fxConversion
	lotDisposalID    uuid.UUID
}

// filingDividend is FilingDividend.
type filingDividend struct {
	symbol        string
	payDate       time.Time
	sourceCountry *string
	gross         dec.Decimal
	withholding   dec.Decimal
	net           dec.Decimal
	fx            fxConversion
	dividendID    uuid.UUID
}

// filingUnsupportedRow is FilingUnsupportedRow.
type filingUnsupportedRow struct {
	reason    string
	reference string
}

// germanyFilingAdvance is GermanyFilingAdvanceLumpSum.
type germanyFilingAdvance struct {
	symbol         string
	classification string
	gross          dec.Decimal
}

// germanyFilingSupplement is GermanyFilingSupplement.
type germanyFilingSupplement struct {
	fundClassifications map[string]string
	advanceLumpSums     []germanyFilingAdvance
	priorStockLoss      *dec.Decimal
	priorGeneralLoss    *dec.Decimal
}

// filingLedger is FilingLedger.
type filingLedger struct {
	taxYear           int
	reportingCurrency string
	jurisdiction      Jurisdiction
	disposals         []filingDisposal
	dividends         []filingDividend
	unsupported       []filingUnsupportedRow
	germany           *germanyFilingSupplement
}

func (l filingLedger) totalGain() dec.Decimal {
	total := zero
	for idx := range l.disposals {
		d := &l.disposals[idx]
		total = total.Add(d.gain)
	}
	return total
}

func (l filingLedger) totalDividendsGross() dec.Decimal {
	total := zero
	for idx := range l.dividends {
		d := &l.dividends[idx]
		total = total.Add(d.gross)
	}
	return total
}

func (l filingLedger) totalWithholding() dec.Decimal {
	total := zero
	for idx := range l.dividends {
		d := &l.dividends[idx]
		total = total.Add(d.withholding)
	}
	return total
}

// filingFormatMoney is FilingFormat.money: bankers' rounding to cents,
// NSDecimalNumber.stringValue, padded to two decimals.
func filingFormatMoney(v dec.Decimal) string {
	s := roundedForFiling(v).String()
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return s + ".00"
	}
	if decimals := len(s) - dot - 1; decimals < 2 {
		return s + strings.Repeat("0", 2-decimals)
	}
	return s
}

// roundedForFiling is Decimal.roundedForFiling(scale: 2): bankers' rounding.
func roundedForFiling(v dec.Decimal) dec.Decimal { return v.Round(2, dec.RoundBankers) }

func filingYear(t time.Time) string  { return strconv.Itoa(t.UTC().Year()) }
func filingMonth(t time.Time) string { return fmt.Sprintf("%02d", int(t.UTC().Month())) }
func filingISODate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// lastFixingDay is the latest weekday on or before t. The ECB publishes no
// fixing on weekends, so a window ending on a Saturday or Sunday is fully
// cached once it holds Friday's rate; checking t itself would send every
// weekend-dated conversion to the provider.
func lastFixingDay(t time.Time) time.Time {
	d := utcDay(t)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// fxDailyRate is FXDailyRate: 1 EUR = rate units of quote on date.
type fxDailyRate struct {
	date  time.Time
	base  string
	quote string
	rate  dec.Decimal
}

// fxRateProvider is FXDailyRateProviding.
type fxRateProvider interface {
	rates(ctx context.Context, quote string, from, to time.Time) ([]fxDailyRate, error)
}

// fxConversion is FXConversion.
type fxConversion struct {
	amount         dec.Decimal
	rate           dec.Decimal
	fixingDate     time.Time
	sourceCurrency string
}

// fxResolverError is FXRateResolverError; Vapor answered it as a 500.
type fxResolverError struct {
	msg string
}

func (e fxResolverError) Error() string { return e.msg }

// fxResolver is FXRateResolver.
type fxResolver struct {
	provider fxRateProvider
	q        *taxdb.Queries
}

// maxLookbackDays covers the longest TARGET closure run.
const maxLookbackDays = 10

func (r fxResolver) convert(ctx context.Context, amount dec.Decimal, currency, reportingCurrency string, date time.Time) (fxConversion, error) {
	from := strings.ToUpper(currency)
	to := strings.ToUpper(reportingCurrency)
	if from == to {
		return fxConversion{amount: amount, rate: dec.FromInt(1), fixingDate: date, sourceCurrency: from}, nil
	}
	if to != "EUR" {
		return fxConversion{}, fxResolverError{fmt.Sprintf("unsupportedReportingCurrency(%q)", to)}
	}
	day := utcDay(date)
	start := day.AddDate(0, 0, -maxLookbackDays)
	rows, err := r.cachedRates(ctx, from, start, day)
	if err != nil {
		return fxConversion{}, err
	}
	var fixing *fxDailyRate
	for i := range rows {
		if rows[i].date.After(day) {
			continue
		}
		if fixing == nil || fixing.date.Before(rows[i].date) {
			fixing = &rows[i]
		}
	}
	if fixing == nil {
		return fxConversion{}, fxResolverError{fmt.Sprintf("noFixing(currency: %q, on: %s)", from, day.Format("2006-01-02 15:04:05 +0000"))}
	}
	converted := roundedForFiling(amount.Div(fixing.rate))
	return fxConversion{amount: converted, rate: fixing.rate, fixingDate: fixing.date, sourceCurrency: from}, nil
}

// cachedRates answers from the cache when it already holds the fixing for
// the last day of the window, and otherwise asks the provider, caches what
// it returns, and merges it with the cached rows. Deliberate fix: Vapor
// returned any cached rows in the window, even a partial set, so a cache
// filled earlier in the window hid the newer fixing and the conversion used
// a stale rate. When the provider fails, the cached rows are still used.
func (r fxResolver) cachedRates(ctx context.Context, quote string, from, to time.Time) ([]fxDailyRate, error) {
	var cached []fxDailyRate
	if r.q != nil {
		rows, err := r.q.CachedFXRates(ctx, taxdb.CachedFXRatesParams{Quote: quote, StartAt: from, EndAt: to})
		if err != nil {
			return nil, err
		}
		cached = make([]fxDailyRate, 0, len(rows))
		last := time.Time{}
		for _, c := range rows {
			rate, _ := dec.Parse(c.Rate)
			row := fxDailyRate{date: utcDay(c.Date), base: c.Base, quote: c.Quote, rate: rate}
			cached = append(cached, row)
			if row.date.After(last) {
				last = row.date
			}
		}
		if len(cached) > 0 && !last.Before(lastFixingDay(to)) {
			return cached, nil
		}
	}
	fresh, err := r.provider.rates(ctx, quote, from, to)
	if err != nil {
		if len(cached) > 0 {
			return cached, nil
		}
		return nil, err
	}
	if r.q != nil {
		for _, row := range fresh {
			// The unique index makes a concurrent re-insert fail; that is fine.
			_ = r.q.InsertFXRate(ctx, taxdb.InsertFXRateParams{
				ID: uuid.New(), Date: row.date, Base: row.base, Quote: row.quote, Rate: row.rate.String(),
			})
		}
	}
	seen := make(map[time.Time]bool, len(fresh))
	for _, row := range fresh {
		seen[utcDay(row.date)] = true
	}
	for _, row := range cached {
		if !seen[row.date] {
			fresh = append(fresh, row)
		}
	}
	return fresh, nil
}

// ecbBaseURL is ECBDailyRateProvider.baseURL; tests point it at a stub.
var ecbBaseURL = "https://data-api.ecb.europa.eu/service/data"

// ecbProvider is ECBDailyRateProvider: ECB Data Portal flow EXR,
// key D.<QUOTE>.EUR.SP00.A, as CSV.
type ecbProvider struct {
	client  *http.Client
	baseURL string
}

func newECBProvider() ecbProvider {
	return ecbProvider{client: &http.Client{Timeout: 15 * time.Second}, baseURL: ecbBaseURL}
}

func (p ecbProvider) rates(ctx context.Context, quote string, from, to time.Time) ([]fxDailyRate, error) {
	normalized := strings.ToUpper(quote)
	key := "D." + normalized + ".EUR.SP00.A"
	url := p.baseURL + "/EXR/" + key + "?startPeriod=" + filingISODate(from) + "&endPeriod=" + filingISODate(to) + "&format=csvdata"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/csv")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("ECB EXR %s returned %d", normalized, resp.StatusCode))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseECBCSV(string(body), normalized), nil
}

// parseECBCSV is ECBDailyRateProvider.parseCSV: TIME_PERIOD and OBS_VALUE
// located by header name; rows that do not parse, or a rate <= 0, are
// skipped.
func parseECBCSV(body, quote string) []fxDailyRate {
	lines := strings.FieldsFunc(body, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) == 0 {
		return []fxDailyRate{}
	}
	columns := strings.Split(lines[0], ",")
	timeIndex, valueIndex := -1, -1
	for i, c := range columns {
		c = strings.TrimSpace(c)
		if c == "TIME_PERIOD" && timeIndex < 0 {
			timeIndex = i
		}
		if c == "OBS_VALUE" && valueIndex < 0 {
			valueIndex = i
		}
	}
	if timeIndex < 0 || valueIndex < 0 {
		return []fxDailyRate{}
	}
	out := []fxDailyRate{}
	for _, line := range lines[1:] {
		parts := strings.Split(line, ",")
		if len(parts) <= max(timeIndex, valueIndex) {
			continue
		}
		date, err := time.Parse("2006-01-02", strings.TrimSpace(parts[timeIndex]))
		if err != nil {
			continue
		}
		rate, ok := dec.Parse(strings.TrimSpace(parts[valueIndex]))
		if !ok || rate.Sign() <= 0 {
			continue
		}
		out = append(out, fxDailyRate{date: date, base: "EUR", quote: quote, rate: rate})
	}
	return out
}

// filingIsFund is FilingLedgerBuilder.isFund.
func filingIsFund(instrumentType *string) bool {
	return oneOf(lower(instrumentType), []string{"etf", "fund", "mutual_fund", "mutualfund", "investment_fund"})
}

// filingSourceCountry is FilingLedgerBuilder.sourceCountry: the ISIN's
// two-letter prefix, or nil.
func filingSourceCountry(isin *string) *string {
	if isin == nil {
		return nil
	}
	s := strings.ToUpper(strings.Trim(*isin, " \t"))
	if len([]rune(s)) < 2 {
		return nil
	}
	prefix := string([]rune(s)[:2])
	for _, r := range prefix {
		if !unicode.IsLetter(r) {
			return nil
		}
	}
	return &prefix
}

// filingLedgerBuilder is FilingLedgerBuilder.
type filingLedgerBuilder struct {
	q  *taxdb.Queries
	fx fxResolver
}

func emptyFilingLedger(userTaxYear int, currency string, j Jurisdiction) filingLedger {
	return filingLedger{
		taxYear: userTaxYear, reportingCurrency: currency, jurisdiction: j,
		disposals: []filingDisposal{}, dividends: []filingDividend{}, unsupported: []filingUnsupportedRow{},
	}
}

func (b filingLedgerBuilder) build(ctx context.Context, userID uuid.UUID, taxYear int, j Jurisdiction, reportingCurrency string) (filingLedger, error) {
	yearStart := time.Date(taxYear, 1, 1, 0, 0, 0, 0, time.UTC)
	yearEnd := time.Date(taxYear+1, 1, 1, 0, 0, 0, 0, time.UTC)

	accounts, err := b.q.AccountsForUser(ctx, userID)
	if err != nil {
		return filingLedger{}, err
	}
	if len(accounts) == 0 {
		return emptyFilingLedger(taxYear, reportingCurrency, j), nil
	}
	accountIDs := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		accountIDs[i] = a.ID
	}

	sells, err := b.q.SellTransactionsInRange(ctx, taxdb.SellTransactionsInRangeParams{AccountIds: accountIDs, Start: yearStart, EndAt: yearEnd})
	if err != nil {
		return filingLedger{}, err
	}
	sellIDs := make([]uuid.UUID, len(sells))
	for i := range sells {
		s := &sells[i]
		sellIDs[i] = s.ID
	}
	disposals := []taxdb.LotDisposal{}
	if len(sellIDs) > 0 {
		if disposals, err = b.q.DisposalsByTransactionIDs(ctx, sellIDs); err != nil {
			return filingLedger{}, err
		}
	}
	lotIDSet := map[uuid.UUID]bool{}
	lotIDs := []uuid.UUID{}
	for _, d := range disposals {
		if !lotIDSet[d.LotID] {
			lotIDSet[d.LotID] = true
			lotIDs = append(lotIDs, d.LotID)
		}
	}
	lots := []taxdb.Lot{}
	if len(lotIDs) > 0 {
		if lots, err = b.q.LotsByIDs(ctx, lotIDs); err != nil {
			return filingLedger{}, err
		}
	}
	lotByID := map[uuid.UUID]taxdb.Lot{}
	buyIDs := []uuid.UUID{}
	for idx := range lots {
		l := &lots[idx]
		lotByID[l.ID] = *l
		if l.OpenTransactionID != nil {
			buyIDs = append(buyIDs, *l.OpenTransactionID)
		}
	}
	buys := []taxdb.Transaction{}
	if len(buyIDs) > 0 {
		if buys, err = b.q.TransactionsByIDs(ctx, buyIDs); err != nil {
			return filingLedger{}, err
		}
	}
	txByID := map[uuid.UUID]taxdb.Transaction{}
	allTx := append(append([]taxdb.Transaction{}, sells...), buys...)
	for idx := range allTx {
		txByID[allTx[idx].ID] = allTx[idx]
	}

	dividends, err := b.q.DividendsPaidInRange(ctx, taxdb.DividendsPaidInRangeParams{AccountIds: accountIDs, Start: yearStart, EndAt: yearEnd})
	if err != nil {
		return filingLedger{}, err
	}

	instrumentIDSet := map[uuid.UUID]bool{}
	instrumentIDs := []uuid.UUID{}
	addInstrument := func(id uuid.UUID) {
		if !instrumentIDSet[id] {
			instrumentIDSet[id] = true
			instrumentIDs = append(instrumentIDs, id)
		}
	}
	for idx := range sells {
		s := &sells[idx]
		addInstrument(s.InstrumentID)
	}
	for idx := range dividends {
		d := &dividends[idx]
		addInstrument(d.InstrumentID)
	}
	instruments := []taxdb.Instrument{}
	if len(instrumentIDs) > 0 {
		if instruments, err = b.q.InstrumentsByIDs(ctx, instrumentIDs); err != nil {
			return filingLedger{}, err
		}
	}
	instrumentByID := map[uuid.UUID]taxdb.Instrument{}
	for idx := range instruments {
		in := &instruments[idx]
		instrumentByID[in.ID] = *in
	}

	rows := []filingDisposal{}
	unsupported := []filingUnsupportedRow{}
	for _, disposal := range disposals {
		sell, okSell := txByID[disposal.TransactionID]
		lot, okLot := lotByID[disposal.LotID]
		var instrument taxdb.Instrument
		okInstrument := false
		if okSell {
			instrument, okInstrument = instrumentByID[sell.InstrumentID]
		}
		if !okSell || !okLot || !okInstrument {
			unsupported = append(unsupported, filingUnsupportedRow{
				reason: "Disposal is missing its lot, sell, or instrument", reference: uuidString(disposal.ID),
			})
			continue
		}
		acquisitionDate := lot.OpenDate
		if lot.OpenTransactionID != nil {
			if buy, ok := txByID[*lot.OpenTransactionID]; ok {
				acquisitionDate = buy.TradeDate
			}
		}
		quantity := fd(disposal.Quantity)
		sellQuantity := fd(disposal.Quantity)
		if sell.Quantity != nil {
			sellQuantity = fd(abs64(*sell.Quantity))
		}
		share := dec.FromInt(1)
		if sellQuantity.Sign() > 0 {
			share = quantity.Div(sellQuantity)
		}
		sellFees := fd(f64(sell.Fees)).Mul(share)

		acquisition, err := b.fx.convert(ctx, fd(disposal.CostBasis), lot.Currency, reportingCurrency, acquisitionDate)
		if err != nil {
			return filingLedger{}, err
		}
		realization, err := b.fx.convert(ctx, fd(disposal.Proceeds), sell.Currency, reportingCurrency, sell.TradeDate)
		if err != nil {
			return filingLedger{}, err
		}
		expenses, err := b.fx.convert(ctx, roundedForFiling(sellFees), sell.Currency, reportingCurrency, sell.TradeDate)
		if err != nil {
			return filingLedger{}, err
		}
		instrumentType := "stock"
		if instrument.InstrumentType != nil {
			instrumentType = *instrument.InstrumentType
		}
		rows = append(rows, filingDisposal{
			symbol:           instrument.Symbol,
			isin:             instrument.Isin,
			instrumentType:   strings.ToLower(instrumentType),
			quantity:         quantity,
			acquisitionDate:  acquisitionDate,
			acquisitionValue: acquisition.amount,
			realizationDate:  sell.TradeDate,
			realizationValue: realization.amount,
			expenses:         expenses.amount,
			gain:             realization.amount.Sub(acquisition.amount),
			holdingPeriod:    disposal.HoldingPeriod,
			sourceCountry:    filingSourceCountry(instrument.Isin),
			fx:               []fxConversion{acquisition, realization},
			lotDisposalID:    disposal.ID,
		})
	}

	dividendRows := []filingDividend{}
	for idx := range dividends {
		dividend := &dividends[idx]
		instrument, okInstrument := instrumentByID[dividend.InstrumentID]
		grossNative := fd(dividend.Amount)
		if dividend.GrossAmount != nil {
			grossNative = fd(*dividend.GrossAmount)
		}
		withholdingNative := fd(f64(dividend.WithholdingTax))
		gross, err := b.fx.convert(ctx, grossNative, dividend.Currency, reportingCurrency, dividend.PayDate)
		if err != nil {
			return filingLedger{}, err
		}
		withholding, err := b.fx.convert(ctx, withholdingNative, dividend.Currency, reportingCurrency, dividend.PayDate)
		if err != nil {
			return filingLedger{}, err
		}
		symbol := "?"
		var isin *string
		if okInstrument {
			symbol = instrument.Symbol
			isin = instrument.Isin
		}
		source := dividend.SourceCountry
		if source == nil {
			source = filingSourceCountry(isin)
		}
		dividendRows = append(dividendRows, filingDividend{
			symbol: symbol, payDate: dividend.PayDate, sourceCountry: source,
			gross: gross.amount, withholding: withholding.amount, net: gross.amount.Sub(withholding.amount),
			fx: gross, dividendID: dividend.ID,
		})
	}

	// Total orders on content, as Vapor's tuple sorts.
	sort.SliceStable(rows, func(i, j int) bool {
		l, r := rows[i], rows[j]
		if !l.realizationDate.Equal(r.realizationDate) {
			return l.realizationDate.Before(r.realizationDate)
		}
		if l.symbol != r.symbol {
			return l.symbol < r.symbol
		}
		if !l.acquisitionDate.Equal(r.acquisitionDate) {
			return l.acquisitionDate.Before(r.acquisitionDate)
		}
		if c := l.realizationValue.Cmp(r.realizationValue); c != 0 {
			return c < 0
		}
		if c := l.acquisitionValue.Cmp(r.acquisitionValue); c != 0 {
			return c < 0
		}
		return l.quantity.Cmp(r.quantity) < 0
	})
	sort.SliceStable(dividendRows, func(i, j int) bool {
		l, r := dividendRows[i], dividendRows[j]
		if !l.payDate.Equal(r.payDate) {
			return l.payDate.Before(r.payDate)
		}
		if l.symbol != r.symbol {
			return l.symbol < r.symbol
		}
		if c := l.gross.Cmp(r.gross); c != 0 {
			return c < 0
		}
		if c := l.withholding.Cmp(r.withholding); c != 0 {
			return c < 0
		}
		return l.net.Cmp(r.net) < 0
	})

	ledger := filingLedger{
		taxYear:           taxYear,
		reportingCurrency: strings.ToUpper(reportingCurrency),
		jurisdiction:      j,
		disposals:         rows,
		dividends:         dividendRows,
		unsupported:       unsupported,
	}
	if j == DE {
		supplement, err := b.germanySupplement(ctx, userID, taxYear, yearEnd, reportingCurrency, instruments)
		if err != nil {
			return filingLedger{}, err
		}
		ledger.germany = &supplement
	}
	return ledger, nil
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func validFundClassification(raw string) bool { return oneOf(raw, fundClasses) }

func (b filingLedgerBuilder) germanySupplement(ctx context.Context, userID uuid.UUID, taxYear int, yearEnd time.Time, reportingCurrency string, instruments []taxdb.Instrument) (germanyFilingSupplement, error) {
	classifications := map[string]string{}
	for idx := range instruments {
		in := &instruments[idx]
		if !filingIsFund(in.InstrumentType) {
			continue
		}
		c := "unknown"
		if in.FundClassification != nil && validFundClassification(*in.FundClassification) {
			c = *in.FundClassification
		}
		classifications[in.Symbol] = c
	}

	holdings, err := b.q.FundAnnualHoldingsForYear(ctx, taxdb.FundAnnualHoldingsForYearParams{UserID: userID, CalculationYear: int64(taxYear)})
	if err != nil {
		return germanyFilingSupplement{}, err
	}
	idSet := map[uuid.UUID]bool{}
	ids := []uuid.UUID{}
	for idx := range holdings {
		h := &holdings[idx]
		if !idSet[h.InstrumentID] {
			idSet[h.InstrumentID] = true
			ids = append(ids, h.InstrumentID)
		}
	}
	symbolByID := map[uuid.UUID]string{}
	if len(ids) > 0 {
		hi, err := b.q.InstrumentsByIDs(ctx, ids)
		if err != nil {
			return germanyFilingSupplement{}, err
		}
		for idx := range hi {
			in := &hi[idx]
			symbolByID[in.ID] = in.Symbol
		}
	}
	advances := []germanyFilingAdvance{}
	for idx := range holdings {
		h := &holdings[idx]
		if !validFundClassification(h.FundClassification) || h.FundClassification == "unknown" {
			continue
		}
		gross, err := b.fx.convert(ctx, fd(h.GrossAdvanceLumpSum), h.Currency, reportingCurrency, yearEnd)
		if err != nil {
			return germanyFilingSupplement{}, err
		}
		symbol, ok := symbolByID[h.InstrumentID]
		if !ok {
			symbol = "?"
		}
		advances = append(advances, germanyFilingAdvance{symbol: symbol, classification: h.FundClassification, gross: gross.amount})
	}
	sort.SliceStable(advances, func(i, j int) bool { return advances[i].symbol < advances[j].symbol })

	out := germanyFilingSupplement{fundClassifications: classifications, advanceLumpSums: advances}
	stock, err := b.q.StockLossYearFor(ctx, taxdb.StockLossYearForParams{UserID: userID, TaxYear: int64(taxYear - 1)})
	switch {
	case err == nil:
		v := fd(stock.EndingLossCarryforward)
		out.priorStockLoss = &v
	case !isNoRows(err):
		return germanyFilingSupplement{}, err
	}
	general, err := b.q.GeneralLossYearFor(ctx, taxdb.GeneralLossYearForParams{UserID: userID, TaxYear: int64(taxYear - 1)})
	switch {
	case err == nil:
		v := fd(general.EndingLossCarryforward)
		out.priorGeneralLoss = &v
	case !isNoRows(err):
		return germanyFilingSupplement{}, err
	}
	return out, nil
}

// filingPackError is FilingPackService.Failure.
func filingProfileIncomplete(taxYear int) error {
	return httpx.Conflict(fmt.Sprintf("profile_incomplete: complete your tax profile for %d first.", taxYear))
}

func filingJurisdictionUnsupported(j Jurisdiction) error {
	return httpx.Unprocessable("Filing pack is not available for " + string(j) + " yet.")
}

// buildFilingPack is FilingPackService.buildPack.
func (s *service) buildFilingPack(ctx context.Context, userID uuid.UUID, taxYear int, provider fxRateProvider) (filingPack, error) {
	profile, err := s.q.TaxProfileForYear(ctx, taxdb.TaxProfileForYearParams{UserID: userID, TaxYear: int64(taxYear)})
	if err != nil && !isNoRows(err) {
		return filingPack{}, err
	}
	j := Jurisdiction(profile.Jurisdiction)
	if err != nil || !profile.IsComplete || !j.valid() {
		return filingPack{}, filingProfileIncomplete(taxYear)
	}
	mapper := s.filingMapper(j)
	if mapper == nil {
		return filingPack{}, filingJurisdictionUnsupported(j)
	}
	builder := filingLedgerBuilder{q: s.q, fx: fxResolver{provider: provider, q: s.q}}
	ledger, err := builder.build(ctx, userID, taxYear, j, profile.ReportingCurrency)
	if err != nil {
		return filingPack{}, err
	}
	return mapper(ledger), nil
}
