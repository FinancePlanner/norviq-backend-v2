package pilots

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PilotFollowService limits.
const (
	ProFollowLimit     = 10
	FreeFollowLimit    = 1
	MaxStartingCapital = 10_000_000.0
	// ProPortfolioLimit is the Pro portfolio limit PortfolioManagementController.create enforces.
	ProPortfolioLimit = 25
)

// CreateRequest is PilotFollowCreateRequest after validation.
type CreateRequest struct {
	PilotSlug       string
	TargetKind      string
	PortfolioListID *string
	WatchlistListID *string
	StartingCapital *float64
}

// FollowService is PilotFollowService: creates follows. A follow never
// touches a real portfolio: portfolio targets must be hypothetical, not the
// default, and empty.
type FollowService struct {
	DB     *pgxpool.Pool
	Mirror *Mirror
	Logger *slog.Logger
}

// Create is PilotFollowService.create.
func (s *FollowService) Create(ctx context.Context, req CreateRequest, userID uuid.UUID, ent entitlements.Snapshot, now time.Time) (pilotsdb.PilotFollow, error) {
	q := pilotsdb.New(s.DB)
	pilot, err := q.ActivePilotBySlug(ctx, req.PilotSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		return pilotsdb.PilotFollow{}, httpx.NotFound("Pilot not found.")
	}
	if err != nil {
		return pilotsdb.PilotFollow{}, err
	}
	latest, err := q.LatestBookVersion(ctx, pilot.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pilotsdb.PilotFollow{}, httpx.Conflict("This pilot has no disclosures yet. Try again later.")
	}
	if err != nil {
		return pilotsdb.PilotFollow{}, err
	}

	existing, err := q.CountFollowsByUser(ctx, userID)
	if err != nil {
		return pilotsdb.PilotFollow{}, err
	}
	limit := FreeFollowLimit
	if ent.IsPro() {
		limit = ProFollowLimit
	}
	if req.TargetKind == TargetPortfolio && !ent.IsPro() {
		return pilotsdb.PilotFollow{}, entitlements.UpgradeRequired(entitlements.FeaturePilotFollows, ent.Level)
	}
	if int(existing) >= limit {
		return pilotsdb.PilotFollow{}, entitlements.UpgradeLimit(entitlements.FeaturePilotFollows, ent.Level, limit, int(existing))
	}

	var follow pilotsdb.PilotFollow
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		tq := pilotsdb.New(tx)
		switch req.TargetKind {
		case TargetPortfolio:
			capital := req.StartingCapital
			if capital == nil || !(*capital > 0) || !(*capital <= MaxStartingCapital) {
				return httpx.BadRequest("Starting capital must be between 0 and 10,000,000.")
			}
			listID, err := portfolioTarget(ctx, tq, req.PortfolioListID, pilot, userID)
			if err != nil {
				return err
			}
			account, err := findOrCreatePilotAccount(ctx, tq, userID, listID)
			if err != nil {
				return err
			}
			if err := tq.InsertCashBalance(ctx, pilotsdb.InsertCashBalanceParams{
				ID: uuid.New(), AccountID: account.ID, Currency: account.BaseCurrency, Balance: *capital, AsOf: dateOf(now), CreatedAt: time.Now(),
			}); err != nil {
				return err
			}
			if err := ensureNotFollowing(ctx, tq, userID, pilot.ID, &listID, nil); err != nil {
				return err
			}
			follow, err = tq.InsertFollow(ctx, pilotsdb.InsertFollowParams{
				ID: uuid.New(), UserID: userID, PilotID: pilot.ID, TargetKind: TargetPortfolio, PortfolioListID: &listID,
				StartingCapital: capital, Currency: account.BaseCurrency, CreatedAt: ptrTime(time.Now()),
			})
			return err
		default:
			listID, err := watchlistTarget(ctx, tq, req.WatchlistListID, pilot, userID)
			if err != nil {
				return err
			}
			if err := ensureNotFollowing(ctx, tq, userID, pilot.ID, nil, &listID); err != nil {
				return err
			}
			follow, err = tq.InsertFollow(ctx, pilotsdb.InsertFollowParams{
				ID: uuid.New(), UserID: userID, PilotID: pilot.ID, TargetKind: TargetWatchlist, WatchlistListID: &listID,
				Currency: "USD", CreatedAt: ptrTime(time.Now()),
			})
			return err
		}
	})
	if err != nil {
		return pilotsdb.PilotFollow{}, err
	}

	// Applied after the commit: quotes reach the network and must not run
	// inside the transaction. If this fails, the follow stays at version 0
	// and the mirror job catches it up on its next tick.
	ok, err := s.Mirror.Apply(ctx, s.DB, follow, pilot, latest, nil, now)
	if err != nil {
		s.Logger.Warn("pilot_follow initial apply failed follow_id=" + follow.ID.String() + " error=" + err.Error())
	} else if ok {
		follow.AppliedVersion = latest.Version
	}
	return follow, nil
}

func ensureNotFollowing(ctx context.Context, q *pilotsdb.Queries, userID, pilotID uuid.UUID, portfolioListID, watchlistListID *uuid.UUID) error {
	n, err := q.CountFollowsForTarget(ctx, pilotsdb.CountFollowsForTargetParams{
		UserID: userID, PilotID: pilotID, PortfolioListID: portfolioListID, WatchlistListID: watchlistListID,
	})
	if err != nil {
		return err
	}
	if n > 0 {
		return httpx.Conflict("You already follow this pilot there.")
	}
	return nil
}

func portfolioTarget(ctx context.Context, q *pilotsdb.Queries, rawID *string, pilot pilotsdb.Pilot, userID uuid.UUID) (uuid.UUID, error) {
	if rawID != nil {
		id, ok := parseSwiftUUID(*rawID)
		if !ok {
			return uuid.Nil, httpx.NotFound("Portfolio not found.")
		}
		list, err := q.PortfolioListForUser(ctx, pilotsdb.PortfolioListForUserParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, httpx.NotFound("Portfolio not found.")
		}
		if err != nil {
			return uuid.Nil, err
		}
		if list.Mode != "hypothetical" || list.IsDefault || list.ArchivedAt != nil {
			return uuid.Nil, httpx.Unprocessable("Pilots can only be followed into a hypothetical portfolio, never your main or a real one.")
		}
		held, err := q.CountStocksInList(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		followed, err := q.CountFollowsByPortfolio(ctx, &id)
		if err != nil {
			return uuid.Nil, err
		}
		accountIDs, err := q.AccountIDsForPortfolio(ctx, &id)
		if err != nil {
			return uuid.Nil, err
		}
		var ledger int64
		if len(accountIDs) > 0 {
			c, err := q.CountCashBalancesForAccounts(ctx, accountIDs)
			if err != nil {
				return uuid.Nil, err
			}
			t, err := q.CountTransactionsForAccounts(ctx, accountIDs)
			if err != nil {
				return uuid.Nil, err
			}
			ledger = c + t
		}
		cashPositions, err := q.CountCashPositions(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		if held != 0 || followed != 0 || ledger != 0 || cashPositions != 0 {
			return uuid.Nil, httpx.Unprocessable("Choose an empty hypothetical portfolio, or let Norviq create one.")
		}
		// A broker sync writes real holdings into its bound list.
		bound, err := q.CountBrokerConnectionsForList(ctx, &id)
		if err != nil {
			return uuid.Nil, err
		}
		if bound != 0 {
			return uuid.Nil, httpx.Unprocessable("This portfolio is linked to a broker connection. Choose another, or let Norviq create one.")
		}
		return id, nil
	}
	count, err := q.CountActivePortfolioLists(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	if count >= ProPortfolioLimit {
		return uuid.Nil, entitlements.UpgradeLimit(entitlements.FeaturePortfolioLists, "pro", ProPortfolioLimit, int(count))
	}
	names, err := q.PortfolioListNames(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	if err := q.InsertHypotheticalPortfolio(ctx, pilotsdb.InsertHypotheticalPortfolioParams{
		ID: id, UserID: userID, Name: uniqueName(pilot.DisplayName+" copy", names), CreatedAt: time.Now(),
	}); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func watchlistTarget(ctx context.Context, q *pilotsdb.Queries, rawID *string, pilot pilotsdb.Pilot, userID uuid.UUID) (uuid.UUID, error) {
	if rawID != nil {
		id, ok := parseSwiftUUID(*rawID)
		if !ok {
			return uuid.Nil, httpx.NotFound("Watchlist not found.")
		}
		list, err := q.WatchlistListForUser(ctx, pilotsdb.WatchlistListForUserParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, httpx.NotFound("Watchlist not found.")
		}
		if err != nil {
			return uuid.Nil, err
		}
		// The mirror rewrites statuses and notes, so it must never adopt
		// items the user wrote by hand.
		if list.IsDefault {
			return uuid.Nil, httpx.Unprocessable("Pilots can't be followed into your main watchlist. Choose another, or let Norviq create one.")
		}
		items, err := q.CountWatchlistItemsInList(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		followed, err := q.CountFollowsByWatchlist(ctx, &id)
		if err != nil {
			return uuid.Nil, err
		}
		if items != 0 || followed != 0 {
			return uuid.Nil, httpx.Unprocessable("Choose an empty watchlist, or let Norviq create one.")
		}
		return id, nil
	}
	names, err := q.WatchlistListNames(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	taken := map[string]bool{}
	for _, n := range names {
		taken[n] = true
	}
	base := pilot.DisplayName + " feed"
	name := base
	for n := 2; taken[name]; n++ {
		name = base + " " + strconv.Itoa(n)
	}
	id := uuid.New()
	if err := q.InsertWatchlistList(ctx, pilotsdb.InsertWatchlistListParams{ID: id, UserID: userID, Name: name, CreatedAt: time.Now()}); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// uniqueName is "X copy", then "X copy 2", and so on.
func uniqueName(base string, names []string) string {
	taken := map[string]bool{}
	for _, n := range names {
		taken[n] = true
	}
	if !taken[base] {
		return base
	}
	n := 2
	for taken[base+" "+strconv.Itoa(n)] {
		n++
	}
	return base + " " + strconv.Itoa(n)
}

// parseSwiftUUID is UUID(uuidString:): only the 36-character hyphenated form.
func parseSwiftUUID(raw string) (uuid.UUID, bool) {
	if len(raw) != 36 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}
