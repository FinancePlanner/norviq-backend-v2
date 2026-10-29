package portfolios

import (
	"context"
	"strings"

	"norviq-api/internal/store/portfoliocoredb"

	"github.com/google/uuid"
)

// ManualBroker is ManualAccountResolver.broker.
const ManualBroker = "manual"

// PilotBroker is PilotAccountResolver.broker.
const PilotBroker = "pilot"

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
		BaseCurrency: "USD",
		PortfolioID:  &portfolioID,
		CreatedAt:    Now(),
	})
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
