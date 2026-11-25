package portfolio

import (
	"os"

	"norviq-api/internal/config"
	"norviq-api/internal/platform/fx"
	"norviq-api/internal/store/fxdb"
)

// FxValuationEnabled reports whether valuation converts holdings into the
// list's base currency (FX_VALUATION_ENABLED, default off).
func FxValuationEnabled() bool { return config.Bool(os.Getenv("FX_VALUATION_ENABLED"), false) }

// RatesFor is the Rates valuation uses: the stored ECB rates when the flag is
// on, otherwise Passthrough, which makes valuation behave exactly as before.
func RatesFor(db fxdb.DBTX) fx.Rates {
	if FxValuationEnabled() {
		return fx.NewStore(db)
	}
	return fx.Passthrough{}
}
