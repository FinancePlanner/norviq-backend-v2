package cadence

import (
	"math"
	"sort"
	"time"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/rebalancing"
)

// Line note codes.
const (
	NoteNoRecurringBuy      = "no_recurring_buy"
	NoteNoZoneYet           = "no_zone_yet"
	NoteInsufficientHistory = "insufficient_history"
	NoteQuoteUnavailable    = "quote_unavailable"
	NoteQuoteStale          = "quote_stale"
	NoteFxUnavailable       = "fx_unavailable"
	NoteBelowMinimumTrade   = "below_minimum_trade"
	NoteZoneOutdated        = "zone_outdated"
)

// Plan note codes.
const (
	NoteNoBudget             = "no_budget"
	NoteSurplusFxUnavailable = "surplus_fx_unavailable"
	NoteScaledToSurplus      = "scaled_to_surplus"
)

const (
	quoteStaleAfter = 72 * time.Hour
	// floatEpsilon keeps float error from flooring an exact value down.
	floatEpsilon = 1e-9
)

// PlanQuote is the last price of a symbol, in its own currency.
type PlanQuote struct {
	Price    float64 // in Currency
	Currency string
	AsOf     time.Time
}

// PlanSymbolInput is one symbol of the plan.
type PlanSymbolInput struct {
	Symbol      string
	Held        bool
	Baseline    *float64   // monthly, plan currency; nil = no active autobuy
	Zone        *ZoneDTO   // newest confirmed M reading after both guards; nil = none yet
	Quote       *PlanQuote // nil = unavailable
	QuoteToPlan *float64   // plan-currency units per quote-currency unit; nil = no rate
}

// PlanInput is everything BuildPlan needs; it does no I/O.
type PlanInput struct {
	Now                   time.Time
	Currency              string
	Surplus               *float64 // plan currency; nil = unknown
	SurplusNote           string   // "", NoteNoBudget or NoteSurplusFxUnavailable
	QuantityIncrement     float64  // 0 = fractional (floored to 4 decimals)
	MinimumTrade          float64  // plan currency; 0 = none
	OtherRecurringMonthly float64
	Symbols               []PlanSymbolInput
}

// PlanLineDTO is one symbol of the monthly plan.
type PlanLineDTO struct {
	Symbol        string   `json:"symbol"`
	Held          bool     `json:"held"`
	ZoneStatus    string   `json:"zoneStatus"` // "ready" | "pending" | "insufficient" | "outdated"
	Zone          *ZoneDTO `json:"zone"`
	Baseline      *float64 `json:"baseline"`
	Multiplier    float64  `json:"multiplier"`
	Wanted        *float64 `json:"wanted"`
	Amount        *float64 `json:"amount"`
	Shares        *float64 `json:"shares"`
	Price         *float64 `json:"price"`
	PriceCurrency *string  `json:"priceCurrency"`
	QuoteAsOf     *string  `json:"quoteAsOf"` // RFC 3339 UTC
	Notes         []string `json:"notes"`     // never null
}

// PlanTotalsDTO sums the lines that have a baseline.
type PlanTotalsDTO struct {
	Baseline              float64  `json:"baseline"`
	Wanted                float64  `json:"wanted"`
	Planned               float64  `json:"planned"`
	Surplus               *float64 `json:"surplus"`
	Unallocated           *float64 `json:"unallocated"`
	OtherRecurringMonthly float64  `json:"otherRecurringMonthly"`
}

// PlanDTO is the monthly contribution plan.
type PlanDTO struct {
	Month       string        `json:"month"` // "2006-01", UTC
	Currency    string        `json:"currency"`
	Lines       []PlanLineDTO `json:"lines"`
	Totals      PlanTotalsDTO `json:"totals"`
	Scaled      bool          `json:"scaled"`
	Scale       *float64      `json:"scale"`
	Notes       []string      `json:"notes"` // never null
	RuleVersion int           `json:"ruleVersion"`
	Disclaimer  string        `json:"disclaimer"`
}

// BuildPlan turns the month's recurring amounts and confirmed zones into
// planned amounts and share equivalents, scaled down to the surplus when the
// zone-adjusted total exceeds it.
func BuildPlan(in PlanInput) PlanDTO {
	plan := PlanDTO{
		Month:       in.Now.UTC().Format("2006-01"),
		Currency:    in.Currency,
		Lines:       make([]PlanLineDTO, 0, len(in.Symbols)),
		Notes:       []string{},
		RuleVersion: DefaultRules.Version,
		Disclaimer:  budgeting.SpendDisclaimer,
	}
	if in.SurplusNote != "" {
		plan.Notes = append(plan.Notes, in.SurplusNote)
	}

	syms := append([]PlanSymbolInput(nil), in.Symbols...)
	sort.SliceStable(syms, func(a, b int) bool { return syms[a].Symbol < syms[b].Symbol })

	var baselineSum, wantedSum float64
	for _, s := range syms {
		l := PlanLineDTO{Symbol: s.Symbol, Held: s.Held, Zone: s.Zone, Baseline: s.Baseline, Multiplier: 1, Notes: []string{}}
		switch {
		case s.Zone == nil:
			l.ZoneStatus = "pending"
			l.Notes = append(l.Notes, NoteNoZoneYet)
		case s.Zone.Zone == string(ZoneInsufficient):
			l.ZoneStatus = "insufficient"
			l.Notes = append(l.Notes, NoteInsufficientHistory)
		case !zoneCurrent(s.Zone.PeriodEnd, in.Now):
			l.ZoneStatus = "outdated"
			l.Notes = append(l.Notes, NoteZoneOutdated)
		default:
			l.ZoneStatus = "ready"
			l.Multiplier = s.Zone.Multiplier
		}
		if s.Baseline == nil {
			l.Notes = append(l.Notes, NoteNoRecurringBuy)
		} else {
			w := rebalancing.RoundedMoney(*s.Baseline * l.Multiplier)
			l.Wanted = &w
			baselineSum += *s.Baseline
			wantedSum += w
		}
		plan.Lines = append(plan.Lines, l)
	}

	scale := 1.0
	if in.Surplus != nil && wantedSum > *in.Surplus+0.005 {
		scale = *in.Surplus / wantedSum
		plan.Scaled = true
		plan.Scale = &scale
		plan.Notes = append(plan.Notes, NoteScaledToSurplus)
	}

	var planned float64
	for i := range plan.Lines {
		l := &plan.Lines[i]
		applyQuote(l, syms[i], in)
		if l.Wanted == nil {
			continue
		}
		amount := *l.Wanted
		if plan.Scaled {
			amount = math.Floor(*l.Wanted*scale*100+floatEpsilon) / 100
		}
		l.Amount = &amount
		planned += amount
		l.Shares = sharesFor(l, syms[i], in, amount)
	}

	plan.Totals = PlanTotalsDTO{
		Baseline:              rebalancing.RoundedMoney(baselineSum),
		Wanted:                rebalancing.RoundedMoney(wantedSum),
		Planned:               rebalancing.RoundedMoney(planned),
		Surplus:               in.Surplus,
		OtherRecurringMonthly: in.OtherRecurringMonthly,
	}
	if in.Surplus != nil {
		u := max(rebalancing.RoundedMoney(*in.Surplus-planned), 0) + 0 // +0 turns -0 into 0
		plan.Totals.Unallocated = &u
	}
	return plan
}

// zoneCurrent reports whether a zone's period end is on or after the last day
// of the month two months before now's (UTC): the job may not have run yet at
// a month start, and one missing month is tolerated. An unparsable date is not
// current.
func zoneCurrent(periodEnd string, now time.Time) bool {
	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return false
	}
	n := now.UTC()
	cutoff := time.Date(n.Year(), n.Month()-1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	return !end.Before(cutoff)
}

// applyQuote copies the quote onto the line and flags a missing or stale one.
func applyQuote(l *PlanLineDTO, s PlanSymbolInput, in PlanInput) {
	if s.Quote == nil {
		l.Notes = append(l.Notes, NoteQuoteUnavailable)
		return
	}
	price, currency, asOf := s.Quote.Price, s.Quote.Currency, s.Quote.AsOf.UTC().Format(time.RFC3339)
	l.Price, l.PriceCurrency, l.QuoteAsOf = &price, &currency, &asOf
	if in.Now.Sub(s.Quote.AsOf) > quoteStaleAfter {
		l.Notes = append(l.Notes, NoteQuoteStale)
	}
	if s.QuoteToPlan == nil {
		l.Notes = append(l.Notes, NoteFxUnavailable)
	}
}

// sharesFor is the share equivalent of amount at the last price, or nil when
// there is no usable quote or rate (applyQuote already noted why).
func sharesFor(l *PlanLineDTO, s PlanSymbolInput, in PlanInput, amount float64) *float64 {
	if s.Quote == nil || s.QuoteToPlan == nil {
		return nil
	}
	var shares float64
	planPrice := s.Quote.Price * *s.QuoteToPlan
	switch {
	case in.MinimumTrade > 0 && amount < in.MinimumTrade:
		l.Notes = append(l.Notes, NoteBelowMinimumTrade)
	case !(planPrice > 0):
		l.Notes = append(l.Notes, NoteQuoteUnavailable)
		return nil
	case in.QuantityIncrement > 0:
		shares = math.Round(rebalancing.RoundedQuantity(amount/planPrice+floatEpsilon, in.QuantityIncrement)*1e8) / 1e8
	default:
		shares = math.Floor(amount/planPrice*1e4+floatEpsilon) / 1e4
	}
	return &shares
}
