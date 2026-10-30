package digest

import "norviq-api/internal/apijson"

// CurrentSchemaVersion is DigestPayload.currentSchemaVersion.
const CurrentSchemaVersion = 1

// Payload is DigestPayload: everything a digest says, computed before any
// model sees it. Stored as JSON on weekly_digests.payload. Every non-optional
// field is always written, because Swift's synthesized Decodable requires it.
type Payload struct {
	SchemaVersion int    `json:"schemaVersion"`
	PeriodStart   string `json:"periodStart"`
	PeriodEnd     string `json:"periodEnd"`
	// One entry per portfolio base currency; amounts are never converted.
	Holdings        []CurrencyMove `json:"holdings"`
	PricedPositions int            `json:"pricedPositions"`
	TotalPositions  int            `json:"totalPositions"`
	Movers          []Mover        `json:"movers"`
	Budget          *Budget        `json:"budget,omitempty"`
	Inflation       *Inflation     `json:"inflation,omitempty"`
	// Latest personal-inflation asOf seen, shown or not.
	InflationAsOfSeen *string   `json:"inflationAsOfSeen,omitempty"`
	Earnings          []Earning `json:"earnings"`
}

// NewPayload is DigestPayload(periodStart:periodEnd:).
func NewPayload(p Period) Payload {
	return Payload{
		SchemaVersion: CurrentSchemaVersion,
		PeriodStart:   p.Start,
		PeriodEnd:     p.End,
		Holdings:      []CurrencyMove{},
		Movers:        []Mover{},
		Earnings:      []Earning{},
	}
}

// IsEmpty is DigestPayload.isEmpty.
func (p *Payload) IsEmpty() bool {
	return len(p.Holdings) == 0 && p.Budget == nil && p.Inflation == nil && len(p.Earnings) == 0
}

// normalize turns decoded nil arrays into empty ones (never null on the wire).
func (p *Payload) normalize() {
	if p.Holdings == nil {
		p.Holdings = []CurrencyMove{}
	}
	if p.Movers == nil {
		p.Movers = []Mover{}
	}
	if p.Earnings == nil {
		p.Earnings = []Earning{}
	}
	if p.Budget != nil && p.Budget.Lines == nil {
		p.Budget.Lines = []BudgetLine{}
	}
}

// CurrencyMove is DigestCurrencyMove.
type CurrencyMove struct {
	Currency   string  `json:"currency"`
	StartValue float64 `json:"startValue"`
	EndValue   float64 `json:"endValue"`
	Change     float64 `json:"change"`
	// Nil when the start value is zero.
	ChangePercent *float64 `json:"changePercent,omitempty"`
}

// Mover is DigestMover.
type Mover struct {
	Symbol     string  `json:"symbol"`
	Currency   string  `json:"currency"`
	Shares     float64 `json:"shares"`
	StartPrice float64 `json:"startPrice"`
	EndPrice   float64 `json:"endPrice"`
	// shares × (endPrice − startPrice).
	Contribution  float64 `json:"contribution"`
	ChangePercent float64 `json:"changePercent"`
	// The model's "why it moved" line (slice 2). Nil in numbers-only digests.
	Why *string `json:"why,omitempty"`
}

// Budget is DigestBudget.
type Budget struct {
	MonthStart string       `json:"monthStart"`
	Currency   string       `json:"currency"`
	Lines      []BudgetLine `json:"lines"`
}

// BudgetLine is DigestBudgetLine.
type BudgetLine struct {
	Title  string  `json:"title"`
	Target float64 `json:"target"`
	Actual float64 `json:"actual"`
	// actual − target: positive is over budget.
	Drift        float64  `json:"drift"`
	DriftPercent *float64 `json:"driftPercent,omitempty"`
}

// Inflation is DigestInflation.
type Inflation struct {
	Country      string  `json:"country"`
	AsOf         string  `json:"asOf"`
	PersonalRate float64 `json:"personalRate"`
	OfficialRate float64 `json:"officialRate"`
}

// Earning is DigestEarning.
type Earning struct {
	Symbol string `json:"symbol"`
	Date   string `json:"date"`
	// Finnhub's bmo / amc / dmh, when known.
	Hour *string `json:"hour,omitempty"`
}

// ---- API --------------------------------------------------------------------

// PreferencesResponse is DigestPreferencesResponse.
type PreferencesResponse struct {
	// False until the user has answered the opt-in card once.
	Decided                bool    `json:"decided"`
	EmailEnabled           bool    `json:"emailEnabled"`
	PushEnabled            bool    `json:"pushEnabled"`
	TelegramEnabled        bool    `json:"telegramEnabled"`
	SentimentAlertsEnabled bool    `json:"sentimentAlertsEnabled"`
	Weekday                int     `json:"weekday"`
	LocalHour              int     `json:"localHour"`
	Timezone               string  `json:"timezone"`
	Country                *string `json:"country,omitempty"`
}

// Summary is DigestSummary.
type Summary struct {
	ID          apijson.UUID `json:"id"`
	PeriodStart string       `json:"periodStart"`
	PeriodEnd   string       `json:"periodEnd"`
	Status      string       `json:"status"`
	Headline    string       `json:"headline"`
}

// ListResponse is DigestListResponse.
type ListResponse struct {
	Items []Summary `json:"items"`
}

// Response is DigestResponse.
type Response struct {
	ID          apijson.UUID  `json:"id"`
	PeriodStart string        `json:"periodStart"`
	PeriodEnd   string        `json:"periodEnd"`
	Status      string        `json:"status"`
	Headline    string        `json:"headline"`
	Payload     Payload       `json:"payload"`
	Narrative   *string       `json:"narrative,omitempty"`
	CreatedAt   *apijson.Time `json:"createdAt,omitempty"`
}

// UnsubscribeResponse is DigestUnsubscribeResponse.
type UnsubscribeResponse struct {
	Unsubscribed bool `json:"unsubscribed"`
}

// WeeklyDigestStatus raw values.
const (
	StatusPending  = "pending"
	StatusComposed = "composed"
	StatusSent     = "sent"
	StatusFailed   = "failed"
)

// DigestChannel and DigestDeliveryStatus raw values.
const (
	ChannelEmail     = "email"
	DeliverySending  = "sending"
	DeliverySent     = "sent"
	DeliveryFailed   = "failed"
	defaultWeekday   = 7
	defaultLocalHour = 18
)
