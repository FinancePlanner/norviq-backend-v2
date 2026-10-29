package digest

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/store/digestdb"

	"github.com/google/uuid"
)

// PriceBar is DigestPriceBar.
type PriceBar struct {
	// Date is yyyy-MM-dd.
	Date  string
	Close float64
}

// PersonalInflation is the part of PersonalInflationResponse the digest reads.
type PersonalInflation struct {
	Country      string
	AsOf         string
	PersonalRate *float64
	OfficialRate float64
}

// Sources is DigestDataSources plus the budget dashboard: everything the
// digest reads from outside its own tables. Injected so tests never reach a
// provider.
type Sources interface {
	DailyCloses(ctx context.Context, symbol, from, to string) ([]PriceBar, error)
	Earnings(ctx context.Context, from, to string) ([]earnings.ItemResponse, error)
	PersonalInflation(ctx context.Context, userID uuid.UUID, country string) (PersonalInflation, error)
	BudgetDashboard(ctx context.Context, userID, snapshotID uuid.UUID) (budgeting.BudgetDriftDashboard, error)
}

// errInflationUnavailable is what the live sources answer while the macro
// domain (MacroService.personalInflation) is not wired into v2.
var errInflationUnavailable = errors.New("personal inflation source unavailable")

// Position is DigestPosition: a symbol in one portfolio currency, shares
// summed across lists.
type Position struct {
	Symbol   string
	Currency string
	Shares   float64
}

const (
	lookbackDays    = 10
	moverCount      = 3
	budgetLineCount = 2
)

// Collector is DigestFactsCollector. Each section fails on its own: an
// outage drops that section and is logged, it never blocks the email.
type Collector struct {
	DB      digestdb.DBTX
	Sources Sources
	Log     *slog.Logger
}

// Collect is DigestFactsCollector.collect.
func (c Collector) Collect(ctx context.Context, userID uuid.UUID, period Period, pref *digestdb.DigestPreference, previous *Payload) (Payload, error) {
	payload := NewPayload(period)
	positions, err := Positions(ctx, c.DB, userID)
	if err != nil {
		return payload, err
	}
	symbols := map[string]struct{}{}
	for _, p := range positions {
		symbols[p.Symbol] = struct{}{}
	}

	closes := c.closesBySymbol(ctx, symbols, period)
	h := Holdings(positions, closes, period)
	payload.Holdings = h.Moves
	payload.Movers = h.Movers
	payload.PricedPositions = h.Priced
	payload.TotalPositions = len(positions)

	if b, err := c.budget(ctx, userID, period); err != nil {
		c.Log.Warn("digest.section_failed", "section", "budget", "user", userID, "error", err)
	} else {
		payload.Budget = b
	}

	if pref != nil && pref.Country != nil {
		if country, ok := macroCountry(*pref.Country); ok {
			if resp, err := c.Sources.PersonalInflation(ctx, userID, country); err != nil {
				c.Log.Warn("digest.section_failed", "section", "inflation", "user", userID, "error", err)
			} else {
				asOf := resp.AsOf
				payload.InflationAsOfSeen = &asOf
				var seen *string
				if previous != nil {
					seen = previous.InflationAsOfSeen
				}
				payload.Inflation = inflationSection(resp, seen)
			}
		}
	}

	if len(positions) > 0 {
		from, ok1 := Shift(period.End, 1)
		to, ok2 := Shift(period.End, 7)
		if ok1 && ok2 {
			if items, err := c.Sources.Earnings(ctx, from, to); err != nil {
				c.Log.Warn("digest.section_failed", "section", "earnings", "user", userID, "error", err)
			} else {
				payload.Earnings = earningsSection(items, symbols)
			}
		}
	}
	return payload, nil
}

// IsEligible is DigestFactsCollector.isEligible: the user holds shares or has
// an active bank connection.
func IsEligible(ctx context.Context, db digestdb.DBTX, userID uuid.UUID) (bool, error) {
	q := digestdb.New(db)
	holds, err := q.DigestHoldsShares(ctx, userID)
	if err != nil || holds {
		return holds, err
	}
	return q.DigestHasActiveBank(ctx, userID)
}

// Positions is DigestFactsCollector.positions: current positions grouped by
// (symbol, portfolio currency), pilot-managed lists excluded.
func Positions(ctx context.Context, db digestdb.DBTX, userID uuid.UUID) ([]Position, error) {
	q := digestdb.New(db)
	lists, err := q.DigestPilotLists(ctx, userID)
	if err != nil {
		return nil, err
	}
	pilot := make([]uuid.UUID, 0, len(lists))
	for _, l := range lists {
		if l != nil {
			pilot = append(pilot, *l)
		}
	}
	rows, err := q.DigestPositions(ctx, digestdb.DigestPositionsParams{UserID: userID, PilotLists: pilot})
	if err != nil {
		return nil, err
	}
	type key struct{ symbol, currency string }
	shares := map[key]float64{}
	for _, r := range rows {
		shares[key{strings.ToUpper(r.Symbol), strings.ToUpper(r.BaseCurrency)}] += r.Shares
	}
	out := make([]Position, 0, len(shares))
	for k, v := range shares {
		out = append(out, Position{Symbol: k.symbol, Currency: k.currency, Shares: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].Currency < out[j].Currency
	})
	return out, nil
}

func (c Collector) closesBySymbol(ctx context.Context, symbols map[string]struct{}, period Period) map[string][]PriceBar {
	from, ok := Shift(period.Start, -lookbackDays)
	if !ok {
		return map[string][]PriceBar{}
	}
	sorted := make([]string, 0, len(symbols))
	for s := range symbols {
		sorted = append(sorted, s)
	}
	sort.Strings(sorted)
	out := map[string][]PriceBar{}
	for _, s := range sorted {
		bars, err := c.Sources.DailyCloses(ctx, s, from, period.End)
		if err != nil {
			c.Log.Info("digest.price_missing", "symbol", s, "error", err)
			continue
		}
		out[s] = bars
	}
	return out
}

// HoldingsResult is DigestFactsCollector.HoldingsResult.
type HoldingsResult struct {
	Moves  []CurrencyMove
	Movers []Mover
	Priced int
}

// Holdings is DigestFactsCollector.holdings: the week's move per currency
// and the biggest movers. A position counts only with a close before the
// week began and a close within it.
func Holdings(positions []Position, closes map[string][]PriceBar, period Period) HoldingsResult {
	movers := []Mover{}
	for _, p := range positions {
		bars := append([]PriceBar(nil), closes[p.Symbol]...)
		sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
		var start, end *PriceBar
		for i := range bars {
			if bars[i].Date < period.Start {
				start = &bars[i]
			}
			if bars[i].Date <= period.End {
				end = &bars[i]
			}
		}
		if start == nil || end == nil || start.Close <= 0 {
			continue
		}
		movers = append(movers, Mover{
			Symbol:        p.Symbol,
			Currency:      p.Currency,
			Shares:        p.Shares,
			StartPrice:    start.Close,
			EndPrice:      end.Close,
			Contribution:  roundCents(p.Shares * (end.Close - start.Close)),
			ChangePercent: roundCents((end.Close - start.Close) / start.Close * 100),
		})
	}

	type total struct{ start, end float64 }
	totals := map[string]total{}
	for _, m := range movers {
		t := totals[m.Currency]
		totals[m.Currency] = total{t.start + m.Shares*m.StartPrice, t.end + m.Shares*m.EndPrice}
	}
	moves := make([]CurrencyMove, 0, len(totals))
	for currency, v := range totals {
		move := CurrencyMove{
			Currency:   currency,
			StartValue: roundCents(v.start),
			EndValue:   roundCents(v.end),
			Change:     roundCents(v.end - v.start),
		}
		if v.start > 0 {
			pct := roundCents((v.end - v.start) / v.start * 100)
			move.ChangePercent = &pct
		}
		moves = append(moves, move)
	}
	// Largest book first: that is the headline.
	sort.Slice(moves, func(i, j int) bool {
		if moves[i].StartValue != moves[j].StartValue {
			return moves[i].StartValue > moves[j].StartValue
		}
		return moves[i].Currency < moves[j].Currency
	})

	top := make([]Mover, 0, len(movers))
	for _, m := range movers {
		if m.Contribution != 0 {
			top = append(top, m)
		}
	}
	sort.SliceStable(top, func(i, j int) bool {
		a, b := math.Abs(top[i].Contribution), math.Abs(top[j].Contribution)
		if a != b {
			return a > b
		}
		return top[i].Symbol < top[j].Symbol
	})
	if len(top) > moverCount {
		top = top[:moverCount]
	}
	return HoldingsResult{Moves: moves, Movers: top, Priced: len(movers)}
}

// budget is the two budgeted categories furthest from target in the
// snapshot for the month containing period.End.
func (c Collector) budget(ctx context.Context, userID uuid.UUID, period Period) (*Budget, error) {
	if len(period.End) < 7 {
		return nil, nil
	}
	monthStart, ok := ParseDay(period.End[:7] + "-01")
	if !ok {
		return nil, nil
	}
	snaps, err := digestdb.New(c.DB).DigestBudgetSnapshots(ctx, digestdb.DigestBudgetSnapshotsParams{
		UserID: userID, MonthStart: monthStart.Add(-24 * time.Hour), MonthStart_2: monthStart.Add(24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, nil
	}
	best := snaps[0]
	for _, s := range snaps[1:] {
		if absDuration(s.MonthStart.Sub(monthStart)) < absDuration(best.MonthStart.Sub(monthStart)) {
			best = s
		}
	}
	dashboard, err := c.Sources.BudgetDashboard(ctx, userID, best.ID)
	if err != nil {
		return nil, err
	}
	return budgetSection(&dashboard), nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// budgetSection is DigestFactsCollector.budget(_:).
func budgetSection(d *budgeting.BudgetDriftDashboard) *Budget {
	cats := make([]budgeting.BudgetCategoryDrift, 0, len(d.Categories))
	for i := range d.Categories {
		if c := &d.Categories[i]; c.TargetAmount > 0 && c.DriftAmount != 0 {
			cats = append(cats, *c)
		}
	}
	sort.SliceStable(cats, func(i, j int) bool {
		a, b := math.Abs(cats[i].DriftAmount), math.Abs(cats[j].DriftAmount)
		if a != b {
			return a > b
		}
		return cats[i].Title < cats[j].Title
	})
	if len(cats) > budgetLineCount {
		cats = cats[:budgetLineCount]
	}
	if len(cats) == 0 {
		return nil
	}
	lines := make([]BudgetLine, 0, len(cats))
	for i := range cats {
		c := &cats[i]
		line := BudgetLine{
			Title:  c.Title,
			Target: roundCents(c.TargetAmount),
			Actual: roundCents(c.ActualAmount),
			Drift:  roundCents(c.DriftAmount),
		}
		if c.DriftPercent != nil {
			p := roundCents(*c.DriftPercent)
			line.DriftPercent = &p
		}
		lines = append(lines, line)
	}
	return &Budget{MonthStart: d.MonthStart, Currency: d.CurrencyCode, Lines: lines}
}

// inflationSection is shown once per new reading: nil when asOf was seen.
func inflationSection(r PersonalInflation, previouslySeen *string) *Inflation {
	if r.PersonalRate == nil || (previouslySeen != nil && *previouslySeen == r.AsOf) {
		return nil
	}
	return &Inflation{
		Country:      r.Country,
		AsOf:         r.AsOf,
		PersonalRate: roundCents(*r.PersonalRate),
		OfficialRate: roundCents(r.OfficialRate),
	}
}

// earningsSection is DigestFactsCollector.earnings: held symbols only,
// sorted by (date, symbol), first report per symbol.
func earningsSection(items []earnings.ItemResponse, held map[string]struct{}) []Earning {
	out := []Earning{}
	for _, it := range items {
		if it.Symbol == nil || it.Date == nil {
			continue
		}
		symbol := strings.ToUpper(*it.Symbol)
		if _, ok := held[symbol]; !ok {
			continue
		}
		date := *it.Date
		if len(date) > 10 {
			date = string([]rune(date)[:min(10, len([]rune(date)))])
		}
		out = append(out, Earning{Symbol: symbol, Date: date, Hour: it.Hour})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Symbol < out[j].Symbol
	})
	seen := map[string]bool{}
	dedup := out[:0]
	for _, e := range out {
		if !seen[e.Symbol] {
			seen[e.Symbol] = true
			dedup = append(dedup, e)
		}
	}
	return dedup
}

// roundCents is DigestFactsCollector.money/percent: (v × 100).rounded() / 100.
func roundCents(v float64) float64 { return math.Round(v*100) / 100 }

// macroCountry is MacroCountry(query:)?.rawValue.
func macroCountry(raw string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "US":
		return "US", true
	case "BR":
		return "BR", true
	case "PT":
		return "PT", true
	case "ES":
		return "ES", true
	case "DE":
		return "DE", true
	case "FR":
		return "FR", true
	case "IT":
		return "IT", true
	case "EA", "EURO", "EZ":
		return "EA", true
	}
	return "", false
}
