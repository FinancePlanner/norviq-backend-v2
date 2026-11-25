// Package fxrates keeps the fx_daily_rates table fresh from the ECB.
package fxrates

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"norviq-api/internal/platform/fx"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/fxdb"
)

// JobName is both the Job name (V2_JOBS) and the advisory-lock name.
const JobName = "fx_ecb_daily"

const (
	tickInterval = 6 * time.Hour
	lookbackDays = 10
)

// ECBJob pulls ECB daily reference rates for every currency in use. ECB
// publishes once per TARGET day, so the 6h tick is a retry; inserts are
// idempotent.
type ECBJob struct {
	// BaseURL defaults to fx.ECBBaseURL.
	BaseURL string
	// InitialDelay defaults to 60s.
	InitialDelay time.Duration
}

// Name implements features.Job.
func (ECBJob) Name() string { return JobName }

// Run implements features.Job.
func (j ECBJob) Run(ctx context.Context, d *server.Deps) {
	base := j.BaseURL
	if base == "" {
		base = fx.ECBBaseURL
	}
	delay := j.InitialDelay
	if delay == 0 {
		delay = 60 * time.Second
	}
	client := &http.Client{Timeout: 30 * time.Second}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, JobName, func(ctx context.Context) {
			if _, err := RunECBTick(ctx, d.DB, client, base, time.Now(), d.Logger); err != nil {
				d.Logger.Error("fx_ecb_daily tick failed", "error", err)
			}
		})
		timer.Reset(tickInterval)
	}
}

// RunECBTick fetches the last ten days of EUR/<c> fixings for each distinct
// currency in use and stores new rows. A currency that fails or that ECB does
// not publish is logged and skipped; only a failure to list currencies or a
// database write error is returned.
func RunECBTick(ctx context.Context, db fxdb.DBTX, client *http.Client, baseURL string, now time.Time, logger *slog.Logger) (int, error) {
	q := fxdb.New(db)
	raw, err := q.CurrenciesInUse(ctx)
	if err != nil {
		return 0, err
	}
	set := map[string]struct{}{}
	for _, c := range raw {
		iso, _, ok := fx.Normalize(c)
		if !ok || iso == "EUR" {
			continue
		}
		set[iso] = struct{}{}
	}
	currencies := make([]string, 0, len(set))
	for c := range set {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)

	from := now.AddDate(0, 0, -lookbackDays)
	inserted := 0
	for _, c := range currencies {
		if ctx.Err() != nil {
			return inserted, ctx.Err()
		}
		rates, err := fx.FetchECB(ctx, client, baseURL, c, from, now)
		if err != nil {
			var se *fx.StatusError
			if errors.As(err, &se) && se.Status == http.StatusNotFound {
				logger.Info("fx_ecb_daily unsupported currency", "currency", c)
			} else {
				logger.Warn("fx_ecb_daily fetch failed", "currency", c, "error", err)
			}
			continue
		}
		if len(rates) == 0 {
			// A currency we already hold rows for that suddenly returns none
			// means the fixings stopped (or the format changed): say so loudly.
			// One never published at all (e.g. TWD) is just unsupported.
			known, err := q.RatesOnOrBefore(ctx, fxdb.RatesOnOrBeforeParams{
				Quotes: []string{c}, Day: now, FromDay: time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC),
			})
			if err != nil {
				return inserted, err
			}
			if len(known) > 0 {
				logger.Warn("fx_ecb_daily no new fixings", "currency", c, "latest", known[0].Date)
			} else {
				logger.Info("fx_ecb_daily unsupported currency", "currency", c)
			}
			continue
		}
		for _, r := range rates {
			n, err := q.InsertECBRate(ctx, fxdb.InsertECBRateParams{Date: r.Date, Quote: c, Rate: r.Rate})
			if err != nil {
				return inserted, err
			}
			inserted += int(n)
		}
	}
	logger.Info("fx_ecb_daily tick", "currencies", len(currencies), "inserted", inserted)
	return inserted, nil
}
