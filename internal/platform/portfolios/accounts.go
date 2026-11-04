package portfolios

import (
	"context"
	"strings"

	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// ManualBroker is ManualAccountResolver.broker.
const ManualBroker = "manual"

// PilotBroker is PilotAccountResolver.broker.
const PilotBroker = "pilot"

// ManualAccountBaseCurrency is the base currency FindOrCreateManualAccount
// gives a new manual account.
const ManualAccountBaseCurrency = "USD"

// TotalCashBalance is PortfolioCashResolver.totalCashBalance: the latest
// balance per (account, currency), floored at zero, summed; plus the
// portfolio's manual cash positions (floored at zero) when a portfolio is
// named.
func TotalCashBalance(ctx context.Context, db portfoliocoredb.DBTX, userID uuid.UUID, portfolioID *uuid.UUID) (float64, error) {
	q := portfoliocoredb.New(db)
	var ids []uuid.UUID
	var err error
	if portfolioID != nil {
		ids, err = q.AccountIDsForUserPortfolio(ctx, portfoliocoredb.AccountIDsForUserPortfolioParams{UserID: userID, PortfolioID: portfolioID})
	} else {
		ids, err = q.AccountIDsForUser(ctx, userID)
	}
	if err != nil {
		return 0, err
	}
	var balances []portfoliocoredb.CashBalance
	if len(ids) > 0 {
		if balances, err = q.CashBalancesForAccounts(ctx, ids); err != nil {
			return 0, err
		}
	}
	latest := map[string]portfoliocoredb.CashBalance{}
	for _, b := range balances {
		key := strings.ToLower(b.AccountID.String()) + "::" + strings.ToUpper(b.Currency)
		if existing, ok := latest[key]; ok {
			if b.AsOf.After(existing.AsOf) {
				latest[key] = b
			}
		} else {
			latest[key] = b
		}
	}
	total := 0.0
	for _, b := range latest {
		total += max(0, b.Balance)
	}
	if portfolioID == nil {
		return total, nil
	}
	manual, err := q.PortfolioCashPositionBalances(ctx, *portfolioID)
	if err != nil {
		return 0, err
	}
	m := 0.0
	for _, v := range manual {
		m += max(0, v)
	}
	return total + m, nil
}

// FindOrCreateManualAccount is ManualAccountResolver.findOrCreate: the
// user's "manual" account for the portfolio, adopting a legacy portfolio-less
// one, else inserting "manual-<user>" (suffixed with the portfolio id when
// the plain external id is taken).
func FindOrCreateManualAccount(ctx context.Context, db portfoliocoredb.DBTX, userID, portfolioID uuid.UUID) (portfoliocoredb.Account, error) {
	q := portfoliocoredb.New(db)
	existing, err := q.AccountByUserPortfolioBroker(ctx, portfoliocoredb.AccountByUserPortfolioBrokerParams{UserID: userID, PortfolioID: &portfolioID, Broker: ManualBroker})
	if err != nil {
		return portfoliocoredb.Account{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	legacy, err := q.LegacyAccountByUserBroker(ctx, portfoliocoredb.LegacyAccountByUserBrokerParams{UserID: userID, Broker: ManualBroker})
	if err != nil {
		return portfoliocoredb.Account{}, err
	}
	if len(legacy) > 0 {
		now := Now()
		return q.SetAccountPortfolio(ctx, portfoliocoredb.SetAccountPortfolioParams{ID: legacy[0].ID, PortfolioID: &portfolioID, UpdatedAt: &now})
	}
	base := "manual-" + strings.ToLower(userID.String())
	taken, err := q.CountAccountsByBrokerExternalID(ctx, portfoliocoredb.CountAccountsByBrokerExternalIDParams{Broker: ManualBroker, ExternalID: base})
	if err != nil {
		return portfoliocoredb.Account{}, err
	}
	external := base
	if taken > 0 {
		external = base + "-" + strings.ToLower(portfolioID.String())
	}
	display := "Manual Cash Account"
	return q.InsertAccount(ctx, portfoliocoredb.InsertAccountParams{
		ID:           uuid.New(),
		UserID:       userID,
		ExternalID:   external,
		Broker:       ManualBroker,
		DisplayName:  &display,
		BaseCurrency: ManualAccountBaseCurrency,
		PortfolioID:  &portfolioID,
		CreatedAt:    Now(),
	})
}

// PeekManualAccount makes FindOrCreateManualAccount's lookups and writes
// nothing: the account that call would use, including a legacy one it would
// adopt, or nil when it would create one.
func PeekManualAccount(ctx context.Context, db portfoliocoredb.DBTX, userID, portfolioID uuid.UUID) (*portfoliocoredb.Account, error) {
	q := portfoliocoredb.New(db)
	existing, err := q.AccountByUserPortfolioBroker(ctx, portfoliocoredb.AccountByUserPortfolioBrokerParams{UserID: userID, PortfolioID: &portfolioID, Broker: ManualBroker})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return &existing[0], nil
	}
	legacy, err := q.LegacyAccountByUserBroker(ctx, portfoliocoredb.LegacyAccountByUserBrokerParams{UserID: userID, Broker: ManualBroker})
	if err != nil {
		return nil, err
	}
	if len(legacy) > 0 {
		return &legacy[0], nil
	}
	return nil, nil
}

// PeekAccountForManualEdit is AccountForManualEdit with PeekManualAccount: the
// account that call would use, or nil when it would create one.
func PeekAccountForManualEdit(ctx context.Context, db portfoliocoredb.DBTX, userID, portfolioID uuid.UUID) (*portfoliocoredb.Account, error) {
	pilot, err := portfoliocoredb.New(db).AccountByUserPortfolioBroker(ctx, portfoliocoredb.AccountByUserPortfolioBrokerParams{UserID: userID, PortfolioID: &portfolioID, Broker: PilotBroker})
	if err != nil {
		return nil, err
	}
	if len(pilot) > 0 {
		return &pilot[0], nil
	}
	return PeekManualAccount(ctx, db, userID, portfolioID)
}

// ListIsIBKRSynced reports whether a live IBKR sync writes to the list: a
// connection in connected or error status (a failed run marks it error, and it
// keeps its binding and can sync again) bound to the list, or unbound when the
// list is where an unbound sync lands, per the brokers package's
// holdingsTargetListID. Writes nothing.
func ListIsIBKRSynced(ctx context.Context, db portfoliocoredb.DBTX, userID, listID uuid.UUID) (bool, error) {
	target, err := peekDefaultPortfolioListID(ctx, db, userID)
	if err != nil {
		return false, err
	}
	ids, err := portfoliodb.New(db).BrokerConnectionsForList(ctx, portfoliodb.BrokerConnectionsForListParams{
		UserID: userID, ListID: listID, IsDefaultTarget: target != nil && *target == listID,
	})
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// peekDefaultPortfolioListID is the list EnsureDefaultPortfolioListID would
// settle on among the user's existing lists, without its promotions and
// demotions; nil when it would have to create one.
func peekDefaultPortfolioListID(ctx context.Context, db portfoliocoredb.DBTX, userID uuid.UUID) (*uuid.UUID, error) {
	q := portfoliocoredb.New(db)
	existing, err := q.DefaultPortfolioList(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		followed, err := IsFollowed(ctx, db, existing[0].ID)
		if err != nil {
			return nil, err
		}
		if !followed {
			return &existing[0].ID, nil
		}
	}
	followedRaw, err := q.FollowedPortfolioListIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	followed := make([]uuid.UUID, 0, len(followedRaw))
	for _, f := range followedRaw {
		if f != nil {
			followed = append(followed, *f)
		}
	}
	fallback, err := q.OldestPortfolioListExcluding(ctx, portfoliocoredb.OldestPortfolioListExcludingParams{UserID: userID, Excluded: followed})
	if err != nil {
		return nil, err
	}
	if len(fallback) > 0 {
		return &fallback[0].ID, nil
	}
	return nil, nil
}

// AccountForManualEdit is PortfolioAccountResolver.forManualEdit: the
// portfolio's pilot account when one exists (it holds the simulation's
// cash), else the manual account.
func AccountForManualEdit(ctx context.Context, db portfoliocoredb.DBTX, userID, portfolioID uuid.UUID) (portfoliocoredb.Account, error) {
	pilot, err := portfoliocoredb.New(db).AccountByUserPortfolioBroker(ctx, portfoliocoredb.AccountByUserPortfolioBrokerParams{UserID: userID, PortfolioID: &portfolioID, Broker: PilotBroker})
	if err != nil {
		return portfoliocoredb.Account{}, err
	}
	if len(pilot) > 0 {
		return pilot[0], nil
	}
	return FindOrCreateManualAccount(ctx, db, userID, portfolioID)
}

// RemovePilotLeftovers is PilotPortfolioLeftovers.remove: a stopped follow's
// simulated stocks (source_provider "pilot") and its pilot accounts, with
// their cash balances and transactions, go with the portfolio. Run it inside
// the transaction that deletes the portfolio.
func RemovePilotLeftovers(ctx context.Context, db portfoliocoredb.DBTX, portfolioListID uuid.UUID) error {
	q := portfoliocoredb.New(db)
	broker := PilotBroker
	if err := q.DeleteStocksInListBySource(ctx, portfoliocoredb.DeleteStocksInListBySourceParams{PortfolioListID: portfolioListID, SourceProvider: &broker}); err != nil {
		return err
	}
	ids, err := q.AccountIDsForPortfolioBroker(ctx, portfoliocoredb.AccountIDsForPortfolioBrokerParams{PortfolioID: &portfolioListID, Broker: broker})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := q.DeleteCashBalancesForAccountID(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteTransactionsForAccountID(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteAccountByID(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
