package cadence

import (
	"time"

	"norviq-api/internal/store/cadencedb"
)

const dateLayout = "2006-01-02"

// ZoneDTO is one stored reading as the API returns it. Zone is the engine's
// zone, CappedZone the zone after the guards, and Multiplier the multiplier
// of CappedZone.
type ZoneDTO struct {
	Zone         string   `json:"zone"`
	CappedZone   string   `json:"cappedZone"`
	Multiplier   float64  `json:"multiplier"`
	GuardReason  *string  `json:"guardReason"`
	GuardChecked bool     `json:"guardChecked"`
	PeriodEnd    string   `json:"periodEnd"`
	AsOf         string   `json:"asOf"`
	Close        float64  `json:"close"`
	Trend        *float64 `json:"trend"`
	Distance     *float64 `json:"distance"`
	Percentile   *float64 `json:"percentile"`
	SampleCount  int32    `json:"sampleCount"`
	TouchZone    *string  `json:"touchZone"`
	RuleVersion  int32    `json:"ruleVersion"`
}

// ListItemDTO is one symbol of the zone list: its monthly confirmed and
// forming readings.
type ListItemDTO struct {
	Symbol    string   `json:"symbol"`
	Status    string   `json:"status"`
	Confirmed *ZoneDTO `json:"confirmed"`
	Forming   *ZoneDTO `json:"forming"`
}

// HistoryPointDTO is one point of a timeframe's chart history.
type HistoryPointDTO struct {
	PeriodEnd string   `json:"periodEnd"`
	Close     float64  `json:"close"`
	Trend     *float64 `json:"trend"`
	Zone      string   `json:"zone"`
	TouchZone *string  `json:"touchZone"`
}

// TimeframeDTO is an unlocked timeframe of the detail response.
type TimeframeDTO struct {
	Locked    bool              `json:"locked"`
	Confirmed *ZoneDTO          `json:"confirmed"`
	Forming   *ZoneDTO          `json:"forming"`
	History   []HistoryPointDTO `json:"history"`
}

// LockedTimeframeDTO is a timeframe the user's plan does not include.
type LockedTimeframeDTO struct {
	Locked bool `json:"locked"`
}

// RulesDTO is the rule set the zones were computed with. It must keep the
// field order and types of RuleSet (converted directly).
type RulesDTO struct {
	Version      int     `json:"version"`
	X4           float64 `json:"x4"`
	X3           float64 `json:"x3"`
	X2           float64 `json:"x2"`
	Extended     float64 `json:"extended"`
	VeryExtended float64 `json:"veryExtended"`
}

// DetailDTO is GET /v1/cadence/zones/:symbol.
type DetailDTO struct {
	Symbol     string         `json:"symbol"`
	Status     string         `json:"status"`
	Timeframes map[string]any `json:"timeframes"`
	Rules      RulesDTO       `json:"rules"`
}

func rulesDTO(r RuleSet) RulesDTO {
	return RulesDTO(r)
}

// zoneDTO renders a stored row. The bull range guard runs here, per user, on
// the newest readings only (the caller passes a bull high for those rows). A
// fundamentals reason stored on the row wins over the bull range reason.
func zoneDTO(row cadencedb.CadenceZone, bullHigh *float64) *ZoneDTO {
	reason := ""
	if row.GuardReason != nil {
		reason = *row.GuardReason
	}
	bull := BullRangeGuard(row.Close, bullHigh)
	capped := Capped(Zone(row.CappedZone), bull)
	var guard *string
	switch {
	case reason != "":
		guard = &reason
	case bull != "":
		guard = &bull
	}
	return &ZoneDTO{
		Zone:         row.Zone,
		CappedZone:   string(capped),
		Multiplier:   capped.Multiplier(),
		GuardReason:  guard,
		GuardChecked: row.GuardChecked,
		PeriodEnd:    row.PeriodEnd.Format(dateLayout),
		AsOf:         row.AsOf.Format(dateLayout),
		Close:        row.Close,
		Trend:        row.Trend,
		Distance:     row.Distance,
		Percentile:   row.Percentile,
		SampleCount:  row.SampleCount,
		TouchZone:    row.TouchZone,
		RuleVersion:  row.RuleVersion,
	}
}

// newestPair picks, from a LatestZonesForSymbols result for one symbol, the
// newest confirmed reading and the forming one. A forming row is dropped
// unless it is newer than the confirmed row.
func newestPair(rows []cadencedb.CadenceZone) (confirmed, forming *cadencedb.CadenceZone) {
	for i := range rows {
		if rows[i].Confirmed {
			confirmed = &rows[i]
		} else {
			forming = &rows[i]
		}
	}
	if confirmed != nil && forming != nil && !forming.PeriodEnd.After(confirmed.PeriodEnd) {
		forming = nil
	}
	return confirmed, forming
}

func historyPoints(rows []cadencedb.CadenceZone) []HistoryPointDTO {
	out := make([]HistoryPointDTO, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		out = append(out, HistoryPointDTO{
			PeriodEnd: r.PeriodEnd.Format(dateLayout), Close: r.Close, Trend: r.Trend, Zone: r.Zone, TouchZone: r.TouchZone,
		})
	}
	return out
}

// historyFrom is the first period_end the detail history includes.
func historyFrom(now time.Time, years int) time.Time {
	n := now.UTC()
	return time.Date(n.Year()-years, n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}
