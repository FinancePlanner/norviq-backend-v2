package pilots

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var bg = context.Background()

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(bg, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(bg, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func newUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, pool, `INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES ($1, $2, 'x', now(), true)`,
		id, "ledger_"+id.String()[:8]+"@example.com")
	return id
}

func makePilot(t *testing.T, pool *pgxpool.Pool, kind string) pilotsdb.Pilot {
	t.Helper()
	id := uuid.New()
	slug := "p-" + id.String()[:6]
	var cik, chamber *string
	if kind == KindFund {
		cik = str("0000000001")
	} else {
		chamber = str("house")
	}
	mustExec(t, pool, `INSERT INTO pilots (id, kind, slug, display_name, chamber, cik, name_aliases, active, created_at)
		VALUES ($1, $2, $3, 'Test Pilot', $4, $5, '{"Test Pilot"}', true, now())`, id, kind, slug, chamber, cik)
	p, err := pilotsdb.New(pool).PilotByID(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func addVersion(t *testing.T, pool *pgxpool.Pool, pilotID uuid.UUID, v int, weights map[string]float64) pilotsdb.PilotBookVersion {
	t.Helper()
	raw, _ := json.Marshal(weights)
	q := pilotsdb.New(pool)
	if err := q.InsertBookVersion(bg, pilotsdb.InsertBookVersionParams{ID: uuid.New(), PilotID: pilotID, Version: int32(v), ComputedAt: testNow, Weights: raw}); err != nil {
		t.Fatal(err)
	}
	row, err := q.BookVersion(bg, pilotsdb.BookVersionParams{PilotID: pilotID, Version: int32(v)})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func hypothetical(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, pool, `INSERT INTO portfolio_lists (id, user_id, name, mode, created_at) VALUES ($1, $2, $3, 'hypothetical', now())`, id, user, "Sim "+id.String()[:6])
	return id
}

func portfolioFollow(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, pilot pilotsdb.Pilot, cash float64) pilotsdb.PilotFollow {
	t.Helper()
	listID := hypothetical(t, pool, user)
	q := pilotsdb.New(pool)
	acct, err := findOrCreatePilotAccount(bg, q, user, listID)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, $3, $4, $5, now())`,
		uuid.New(), acct.ID, acct.BaseCurrency, cash, testNow)
	f, err := q.InsertFollow(bg, pilotsdb.InsertFollowParams{
		ID: uuid.New(), UserID: user, PilotID: pilot.ID, TargetKind: TargetPortfolio,
		PortfolioListID: &listID, StartingCapital: &cash, Currency: "USD", CreatedAt: ptrTime(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func watchlistFollow(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, pilot pilotsdb.Pilot) (pilotsdb.PilotFollow, uuid.UUID) {
	t.Helper()
	listID := uuid.New()
	mustExec(t, pool, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, listID, user, "Feed "+listID.String()[:4])
	f, err := pilotsdb.New(pool).InsertFollow(bg, pilotsdb.InsertFollowParams{
		ID: uuid.New(), UserID: user, PilotID: pilot.ID,
		TargetKind: TargetWatchlist, WatchlistListID: &listID, Currency: "USD", CreatedAt: ptrTime(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, listID
}

func reload(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) pilotsdb.PilotFollow {
	t.Helper()
	f, err := pilotsdb.New(pool).FollowByID(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// stubMirror quotes from prices (a missing symbol is a quote error).
func stubMirror(prices map[string]float64) *Mirror {
	return &Mirror{
		Quote: func(_ context.Context, s string) (float64, error) {
			p, ok := prices[s]
			if !ok {
				return 0, errors.New("bad gateway")
			}
			return p, nil
		},
		Instrument:     func(context.Context, string) *uuid.UUID { return nil },
		WatchlistLimit: func(context.Context, pilotsdb.DBTX, uuid.UUID, int) error { return nil },
	}
}

// flatMirror quotes every symbol at 100.
func flatMirror() *Mirror {
	m := stubMirror(nil)
	m.Quote = func(context.Context, string) (float64, error) { return 100, nil }
	return m
}

func snap(user uuid.UUID, pro bool) entitlements.Snapshot {
	if pro {
		return entitlements.Snapshot{UserID: user, Level: "pro"}
	}
	return entitlements.Snapshot{UserID: user, Level: "free"}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func cashOf(t *testing.T, pool *pgxpool.Pool, accountID uuid.UUID) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(bg, `SELECT COALESCE(sum(balance), 0) FROM cash_balances WHERE account_id = $1`, accountID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
