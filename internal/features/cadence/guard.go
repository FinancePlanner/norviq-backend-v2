package cadence

// Reasons reported by the guards. They are fixed strings that appear in
// responses, so they must not name an action.
const (
	GuardFundamentalsWeakening = "fundamentals_weakening"
	GuardAboveBullRange        = "above_bull_range"
)

// Fundamentals holds the trailing-year growth figures used by the fundamentals
// guard. 0.05 means +5%. A nil field means the figure is unavailable.
type Fundamentals struct {
	IsETF              bool
	RevenueGrowth      *float64
	FreeCashFlowGrowth *float64
}

// FundamentalsGuard reports GuardFundamentalsWeakening when both revenue and
// free cash flow growth are strictly negative. checked is false for a stock
// missing either figure, in which case nothing could be judged. ETFs are never
// checked and always return ("", true).
func FundamentalsGuard(f Fundamentals) (reason string, checked bool) {
	if f.IsETF {
		return "", true
	}
	if f.RevenueGrowth == nil || f.FreeCashFlowGrowth == nil {
		return "", false
	}
	if *f.RevenueGrowth < 0 && *f.FreeCashFlowGrowth < 0 {
		return GuardFundamentalsWeakening, true
	}
	return "", true
}

// BullRangeGuard reports GuardAboveBullRange when price is strictly above the
// bull range high. It returns "" when the high is unknown or the price is not
// above it.
func BullRangeGuard(price float64, bullHigh *float64) string {
	if bullHigh == nil {
		return ""
	}
	if price > *bullHigh {
		return GuardAboveBullRange
	}
	return ""
}

// Capped lowers a multiplier zone to ZoneSteady when any reason is non-empty
// and the zone is above steady (x4, x3 or x2). The guard can only lower a zone,
// never raise one. Zones at or beyond steady, and any zone with no non-empty
// reason, are returned unchanged.
func Capped(z Zone, reasons ...string) Zone {
	if !anyReason(reasons) {
		return z
	}
	switch z {
	case ZoneX4, ZoneX3, ZoneX2:
		return ZoneSteady
	}
	return z
}

func anyReason(reasons []string) bool {
	for _, r := range reasons {
		if r != "" {
			return true
		}
	}
	return false
}
