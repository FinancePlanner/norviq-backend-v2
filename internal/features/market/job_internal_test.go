package market

import "testing"

func TestHistoryDiverged(t *testing.T) {
	stored := map[string]float64{"2024-06-05": 1224.40, "2024-06-06": 1209.98, "2024-06-07": 1208.88}
	same := map[string]float64{"2024-06-05": 1224.40, "2024-06-06": 1210.00, "2024-06-07": 1208.88}
	split := map[string]float64{"2024-06-05": 122.44, "2024-06-06": 121.00, "2024-06-07": 120.89}
	if HistoryDiverged(stored, same) {
		t.Fatal("0.002% drift is not divergence")
	}
	if !HistoryDiverged(stored, split) {
		t.Fatal("10:1 split must diverge")
	}
	if HistoryDiverged(stored, map[string]float64{"2024-06-10": 121.79}) {
		t.Fatal("no shared dates: not diverged")
	}
	if HistoryDiverged(map[string]float64{}, split) {
		t.Fatal("nothing stored: not diverged")
	}
}
