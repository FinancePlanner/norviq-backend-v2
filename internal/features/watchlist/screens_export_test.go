package watchlist

// SetScreenMarket swaps the market data screen evaluations read (tests).
func SetScreenMarket(m ScreenMarket) func() {
	prev := screenMarketOverride
	screenMarketOverride = m
	return func() { screenMarketOverride = prev }
}
