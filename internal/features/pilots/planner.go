package pilots

import (
	"math"
	"sort"
)

// Order is PilotOrder.
type Order struct {
	Symbol   string
	Side     string // SideBuy or SideSell
	Quantity float64
	Price    float64
}

// Plan is PilotRebalancePlan. Unpriced holds held or target symbols with no
// price; they are not traded.
type Plan struct {
	Orders   []Order
	Unpriced []string
}

// PlanRebalance is PilotRebalancePlanner.plan. Pure.
//
// V = cash + priced holdings. Target shares = weight x V / price, with
// fractional shares allowed. Sells come first so their proceeds fund the
// buys, and buys are capped at the cash on hand. Moves smaller than
// max($5, 0.25% of V) are skipped.
func PlanRebalance(weights, holdings map[string]float64, cash float64, prices map[string]float64) Plan {
	set := map[string]bool{}
	for s := range weights {
		set[s] = true
	}
	for s := range holdings {
		set[s] = true
	}
	unpriced := []string{}
	priced := []string{}
	for s := range set {
		if prices[s] <= 0 {
			unpriced = append(unpriced, s)
		} else {
			priced = append(priced, s)
		}
	}
	sort.Strings(unpriced)
	sort.Strings(priced)

	value := cash
	for _, s := range priced {
		value += holdings[s] * prices[s]
	}
	if !(value > 0) {
		return Plan{Orders: []Order{}, Unpriced: unpriced}
	}
	threshold := max(5, value*0.0025)

	var sells, buys []Order
	for _, s := range priced {
		price := prices[s]
		current := holdings[s]
		target := weights[s] * value / price
		delta := target - current
		// A dropped symbol (target zero) is always sold out, whatever its size.
		soldOut := target == 0 && current > 0
		if !soldOut && !(math.Abs(delta)*price >= threshold) {
			continue
		}
		if delta < 0 {
			// Sell-to-zero passes the held quantity unrounded so the ledger
			// closes the position exactly; partial sells never exceed it.
			quantity := current
			if !soldOut {
				quantity = min(round6(-delta), current)
			}
			sells = append(sells, Order{s, SideSell, quantity, price})
		} else {
			buys = append(buys, Order{s, SideBuy, delta, price})
		}
	}

	available := cash
	for _, o := range sells {
		available += o.Quantity * o.Price
	}
	orders := append([]Order{}, sells...)
	for _, b := range buys {
		affordable := min(b.Quantity, floor6(available/b.Price))
		if !(affordable*b.Price >= threshold) {
			continue
		}
		orders = append(orders, Order{b.Symbol, SideBuy, round6(affordable), b.Price})
		available -= round6(affordable) * b.Price
	}
	return Plan{Orders: orders, Unpriced: unpriced}
}

// round6 is Swift's (x * 1e6).rounded() / 1e6: half away from zero.
func round6(x float64) float64 { return math.Round(x*1_000_000) / 1_000_000 }

func floor6(x float64) float64 { return math.Floor(x*1_000_000) / 1_000_000 }

func round2(x float64) float64 { return math.Round(x*100) / 100 }
