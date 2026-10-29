// Package pilots ports Pilots/*.swift: PilotController (GET /v1/pilots,
// /v1/pilots/:slug and the /v1/pilot-follows CRUD), PilotFollowService,
// PilotMirrorService, PilotIngestionService with its disclosure sources
// (FMP congress feeds, SEC EDGAR 13F + OpenFIGI), and PilotIngestionJob /
// PilotMirrorJob. Everything 404s unless PILOTS_ENABLED, read per request
// as Vapor's controller did.
package pilots

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// Feature mounts PilotController.
type Feature struct {
	// Mirror overrides the live mirror (tests).
	Mirror func(d *server.Deps) *Mirror
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "pilots" }

// Enabled is envBool("PILOTS_ENABLED", default: false).
func Enabled() bool { return config.Bool(os.Getenv("PILOTS_ENABLED"), false) }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	build := f.Mirror
	if build == nil {
		build = LiveMirror
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	h := &handlers{
		q:       pilotsdb.New(d.DB),
		ent:     entitlements.NewFromEnv(d.DB),
		follows: &FollowService{DB: d.DB, Mirror: build(d), Logger: d.Logger},
		now:     now,
	}

	// routes.swift: api.grouped(ScopedBearerAuthenticator(), RateLimit(30/60s,
	// "ratelimit:pilots"), Idempotency("idempotency:pilots")); the
	// controller's boot then adds ScopedBearerAuthenticator + the session
	// guard + a scope requirement. The authenticator runs first so the
	// limiter buckets are per user; the second authenticator is a no-op.
	outer := []echo.MiddlewareFunc{
		d.Auth.Scoped(),
		d.Limiter.Limit(30, 60*time.Second, "ratelimit:pilots"),
		d.Limiter.Idempotency("idempotency:pilots"),
		auth.Guard(),
	}
	read := append(append([]echo.MiddlewareFunc{}, outer...), auth.RequireScope(auth.ScopePortfolioRead))
	write := append(append([]echo.MiddlewareFunc{}, outer...), auth.RequireScope(auth.ScopePortfolioWrite))

	r.V1.GET("/pilots", h.listPilots, read...)
	r.V1.GET("/pilots/:slug", h.pilotDetail, read...)
	r.V1.GET("/pilot-follows", h.listFollows, read...)
	r.V1.GET("/pilot-follows/:followId", h.getFollow, read...)
	r.V1.GET("/pilot-follows/:followId/events", h.events, read...)
	r.V1.GET("/pilot-follows/:followId/snapshots", h.snapshots, read...)
	r.V1.POST("/pilot-follows", h.createFollow, write...)
	r.V1.PATCH("/pilot-follows/:followId", h.updateFollow, write...)
	r.V1.DELETE("/pilot-follows/:followId", h.deleteFollow, write...)
	return nil
}

// LiveMirror is PilotWiring.mirror: live quotes from the market data
// service, CsvPortfolioImportService.manualEntryInstrument for instruments,
// and the usage-counter watchlist item limit.
func LiveMirror(d *server.Deps) *Mirror {
	ent := entitlements.NewFromEnv(d.DB)
	svc, err := market.SharedService(d)
	if err != nil {
		d.Logger.Error("pilots: market data service", "error", err)
	}
	q := pilotsdb.New(d.DB)
	return &Mirror{
		Quote: func(ctx context.Context, symbol string) (float64, error) {
			if svc == nil {
				return 0, err
			}
			res, err := svc.Quote(ctx, symbol)
			if err != nil {
				return 0, err
			}
			return res.CurrentPrice, nil
		},
		Instrument: func(ctx context.Context, symbol string) *uuid.UUID {
			var search func(ctx context.Context, q string) ([]marketdata.SearchResultResponse, error)
			if svc != nil {
				search = svc.Search
			}
			id, err := ManualEntryInstrument(ctx, q, search, symbol)
			if err != nil {
				return nil
			}
			return &id
		},
		WatchlistLimit: func(ctx context.Context, db pilotsdb.DBTX, userID uuid.UUID, current int) error {
			return ent.WithDB(db).EnforceResourceLimit(ctx, entitlements.FeatureWatchlistItems, userID, current, 1)
		},
	}
}

// Endpoints are the external base URLs ingestion calls; empty means the
// production default.
type Endpoints struct {
	EdgarData   string
	EdgarArchiv string
	OpenFIGI    string
	Client      *http.Client
	// Pause runs between OpenFIGI batches (2.5 s by default).
	Pause func(ctx context.Context)
}

// LiveIngestion is PilotWiring.ingestion: FMP's latest congress feeds (a
// missing FMP key disables congress only) and SEC EDGAR 13F filings with
// OpenFIGI CUSIP mapping. Built once per run, which keeps the warning to one
// per run and the congress memo inside the run.
func LiveIngestion(d *server.Deps, userAgent string, ep Endpoints) *Ingestion {
	var fmp *marketdata.FMP
	if svc, err := market.SharedService(d); err == nil {
		fmp = svc.FMP
	}
	return NewIngestion(d.DB, d.Logger, fmp, userAgent, ep)
}

// NewIngestion wires the sources around an FMP client (nil: congress off).
func NewIngestion(db *pgxpool.Pool, logger *slog.Logger, fmp *marketdata.FMP, userAgent string, ep Endpoints) *Ingestion {
	client := ep.Client
	if client == nil {
		client = &http.Client{}
	}
	if userAgent == "" {
		userAgent = DefaultSECUserAgent
	}
	pause := ep.Pause
	if pause == nil {
		pause = func(ctx context.Context) {
			select {
			case <-ctx.Done():
			case <-time.After(2500 * time.Millisecond):
			}
		}
	}
	q := pilotsdb.New(db)
	resolver := &CusipResolver{Post: HTTPPoster(client, orDefault(ep.OpenFIGI, DefaultOpenFIGIURL)), Pause: pause}
	funds := &EdgarSource{
		Get: HTTPGetter(client, userAgent),
		Resolve: func(ctx context.Context, cusips []string) (map[string]string, error) {
			return resolver.Resolve(ctx, q, cusips)
		},
		DataURL:    ep.EdgarData,
		ArchiveURL: ep.EdgarArchiv,
	}
	return &Ingestion{DB: db, Politicians: CongressSourceFor(logger, fmp), Funds: funds}
}

// CongressSourceFor is PilotWiring.congressSource.
func CongressSourceFor(logger *slog.Logger, fmp *marketdata.FMP) DisclosureSource {
	if fmp == nil {
		logger.Warn("pilot congress ingestion disabled: FMP not configured")
		return DisabledSource{}
	}
	return NewCongressSource(func(ctx context.Context, chamber string) ([]marketdata.FMPCongressTrade, error) {
		if chamber == "senate" {
			return fmp.LatestSenateTrades(ctx, CongressFeedLimit)
		}
		return fmp.LatestHouseTrades(ctx, CongressFeedLimit)
	}, 0, nil)
}
