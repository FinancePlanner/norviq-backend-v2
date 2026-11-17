package cadence

import "testing"

func fp(v float64) *float64 { return &v }

func TestFundamentalsGuard(t *testing.T) {
	cases := []struct {
		name        string
		f           Fundamentals
		wantReason  string
		wantChecked bool
	}{
		{"ETF is never checked", Fundamentals{IsETF: true, RevenueGrowth: fp(-0.02), FreeCashFlowGrowth: fp(-0.10)}, "", true},
		{"ETF with nil inputs", Fundamentals{IsETF: true}, "", true},
		{"revenue and FCF both negative", Fundamentals{RevenueGrowth: fp(-0.02), FreeCashFlowGrowth: fp(-0.10)}, GuardFundamentalsWeakening, true},
		{"revenue negative, FCF positive", Fundamentals{RevenueGrowth: fp(-0.02), FreeCashFlowGrowth: fp(0.10)}, "", true},
		{"revenue positive, FCF negative", Fundamentals{RevenueGrowth: fp(0.02), FreeCashFlowGrowth: fp(-0.10)}, "", true},
		{"growth exactly 0 on both is not negative", Fundamentals{RevenueGrowth: fp(0), FreeCashFlowGrowth: fp(0)}, "", true},
		{"revenue nil on a stock", Fundamentals{FreeCashFlowGrowth: fp(-0.10)}, "", false},
		{"FCF nil on a stock", Fundamentals{RevenueGrowth: fp(-0.02)}, "", false},
		{"both nil on a stock", Fundamentals{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, checked := FundamentalsGuard(tc.f)
			if reason != tc.wantReason || checked != tc.wantChecked {
				t.Fatalf("FundamentalsGuard() = (%q, %v), want (%q, %v)", reason, checked, tc.wantReason, tc.wantChecked)
			}
		})
	}
}

func TestBullRangeGuard(t *testing.T) {
	cases := []struct {
		name     string
		price    float64
		bullHigh *float64
		want     string
	}{
		{"price above bull high", 210, fp(200), GuardAboveBullRange},
		{"price equal to bull high", 200, fp(200), ""},
		{"nil bull high", 210, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BullRangeGuard(tc.price, tc.bullHigh); got != tc.want {
				t.Fatalf("BullRangeGuard(%v) = %q, want %q", tc.price, got, tc.want)
			}
		})
	}
}

func TestCapped(t *testing.T) {
	cases := []struct {
		name    string
		z       Zone
		reasons []string
		want    Zone
	}{
		{"x4 with one reason is capped to steady", ZoneX4, []string{GuardFundamentalsWeakening}, ZoneSteady},
		{"x3 with a reason is capped to steady", ZoneX3, []string{GuardAboveBullRange}, ZoneSteady},
		{"x2 with empty reasons is unchanged", ZoneX2, []string{"", ""}, ZoneX2},
		{"x2 with no reasons is unchanged", ZoneX2, nil, ZoneX2},
		{"steady is unchanged with a reason", ZoneSteady, []string{GuardFundamentalsWeakening}, ZoneSteady},
		{"extended is unchanged with a reason", ZoneExtended, []string{GuardFundamentalsWeakening}, ZoneExtended},
		{"very_extended is unchanged with a reason", ZoneVeryExtended, []string{GuardAboveBullRange}, ZoneVeryExtended},
		{"insufficient is unchanged with a reason", ZoneInsufficient, []string{GuardFundamentalsWeakening}, ZoneInsufficient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Capped(tc.z, tc.reasons...); got != tc.want {
				t.Fatalf("Capped(%q, %q) = %q, want %q", tc.z, tc.reasons, got, tc.want)
			}
		})
	}
}
