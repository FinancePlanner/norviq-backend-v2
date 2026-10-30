-- BrokerController, BrokersService, DatabaseBrokersRepository,
-- IBKRBrokerSyncService, IBKRSyncJob and CsvPortfolioImportService.

-- name: ListConnections :many
-- DatabaseBrokersRepository.list: updated_at DESC, then created_at DESC
-- (Postgres puts NULLs first on DESC, as Fluent's sort did).
SELECT * FROM broker_connections
WHERE user_id = $1
ORDER BY updated_at DESC, created_at DESC;

-- name: FindConnection :many
SELECT * FROM broker_connections WHERE user_id = $1 AND provider = $2 LIMIT 1;

-- name: ConnectedConnections :many
SELECT * FROM broker_connections WHERE provider = $1 AND status = 'connected';

-- name: InsertConnection :one
INSERT INTO broker_connections (
    id, user_id, provider, external_id, access_token, refresh_token, expires_at,
    status, display_name, status_detail, connected_at, last_synced_at,
    portfolio_list_id, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, sqlc.arg(now), sqlc.arg(now)
)
RETURNING *;

-- name: UpdateConnection :one
UPDATE broker_connections SET
    external_id = $2, access_token = $3, refresh_token = $4, expires_at = $5,
    status = $6, display_name = $7, status_detail = $8, connected_at = $9,
    last_synced_at = $10, portfolio_list_id = $11, updated_at = sqlc.arg(now)
WHERE id = $1
RETURNING *;

-- name: InsertFlow :one
INSERT INTO broker_oauth_flows (
    id, user_id, provider, state, redirect_uri, portfolio_list_id, expires_at,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: OpenFlowByID :many
SELECT * FROM broker_oauth_flows
WHERE provider = $1 AND used_at IS NULL AND id = $2
LIMIT 1;

-- name: OpenFlowByState :many
SELECT * FROM broker_oauth_flows
WHERE provider = $1 AND used_at IS NULL AND state = $2
LIMIT 1;

-- name: MarkFlowUsed :exec
UPDATE broker_oauth_flows SET used_at = sqlc.arg(now), updated_at = sqlc.arg(now) WHERE id = $1;

-- name: UserStocks :many
SELECT * FROM stocks WHERE user_id = $1;

-- name: HoldingsPage :many
-- DatabaseStocksRepository.list(limit: 100, cursor: nil).
SELECT * FROM stocks WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100;

-- name: ImportedStocks :many
SELECT * FROM stocks
WHERE user_id = $1 AND source_provider = $2 AND source_account_id = $3;

-- name: DeleteImportedStocksBySymbol :many
DELETE FROM stocks
WHERE user_id = $1 AND source_provider = $2 AND source_account_id = $3 AND symbol = $4
RETURNING id;

-- name: DeleteProviderStocks :exec
DELETE FROM stocks WHERE user_id = $1 AND source_provider = $2;

-- name: StocksInListForSymbols :many
SELECT * FROM stocks
WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = ANY(sqlc.arg(symbols)::text[]);

-- name: DeleteStocksInListBySymbol :exec
DELETE FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = $3;

-- name: HasImportedSymbol :one
SELECT EXISTS (
    SELECT 1 FROM stocks
    WHERE user_id = $1 AND symbol = $2 AND source_provider = $3
      AND (sqlc.narg(source_account_id)::uuid IS NULL OR source_account_id = sqlc.narg(source_account_id)::uuid)
);

-- name: InsertStock :one
INSERT INTO stocks (
    id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, notes,
    category, source_provider, source_account_id, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, sqlc.arg(now), sqlc.arg(now)
)
RETURNING *;

-- name: AccountByExternal :many
SELECT * FROM accounts WHERE user_id = $1 AND broker = $2 AND external_id = $3 LIMIT 1;

-- name: InsertAccount :one
INSERT INTO accounts (
    id, user_id, external_id, broker, display_name, base_currency, portfolio_id,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: UpdateAccountDisplayName :one
UPDATE accounts SET display_name = $2, updated_at = sqlc.arg(now) WHERE id = $1 RETURNING *;

-- name: GetAccount :many
SELECT * FROM accounts WHERE id = $1;

-- name: UserAccounts :many
SELECT * FROM accounts WHERE user_id = $1;

-- name: NonPilotAccountIDs :many
SELECT id FROM accounts WHERE user_id = $1 AND broker <> 'pilot';

-- name: InstrumentBySymbol :many
SELECT * FROM instruments WHERE symbol = $1 LIMIT 1;

-- name: InsertInstrument :one
INSERT INTO instruments (id, conid, symbol, exchange, currency, name, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: GetInstrument :many
SELECT * FROM instruments WHERE id = $1;

-- name: AllInstruments :many
SELECT * FROM instruments;

-- name: InstrumentsByIDs :many
SELECT * FROM instruments WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: SetInstrumentListing :exec
UPDATE instruments SET listing_exchange = $2, regulated_market_status = $3, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: TransactionByExternal :many
SELECT * FROM transactions WHERE account_id = $1 AND external_id = $2 LIMIT 1;

-- name: InsertTransaction :one
INSERT INTO transactions (
    id, account_id, instrument_id, external_id, type, quantity, price, currency,
    trade_date, settle_date, fees, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: UpdateTransaction :one
UPDATE transactions SET
    instrument_id = $2, type = $3, quantity = $4, price = $5, currency = $6,
    trade_date = $7, settle_date = $8, fees = $9, updated_at = sqlc.arg(now)
WHERE id = $1
RETURNING *;

-- name: SetTransactionQuantity :exec
UPDATE transactions SET quantity = $2, updated_at = sqlc.arg(now) WHERE id = $1;

-- name: AccountTransactionsOfTypes :many
SELECT * FROM transactions WHERE account_id = $1 AND type = ANY(sqlc.arg(types)::text[]);

-- name: AccountInstrumentTransactions :many
SELECT * FROM transactions WHERE account_id = $1 AND instrument_id = $2;

-- name: AccountSellsForInstruments :many
SELECT * FROM transactions
WHERE account_id = $1 AND instrument_id = ANY(sqlc.arg(instrument_ids)::uuid[]) AND type = 'SELL';

-- name: BuysInAccountsBetween :many
SELECT * FROM transactions
WHERE account_id = ANY(sqlc.arg(account_ids)::uuid[]) AND type = 'BUY'
  AND trade_date >= sqlc.arg(start_date)::timestamptz AND trade_date <= sqlc.arg(end_date)::timestamptz;

-- name: LotByOpenTransaction :many
SELECT * FROM lots WHERE open_transaction_id = $1 LIMIT 1;

-- name: InsertLot :one
INSERT INTO lots (
    id, account_id, instrument_id, open_transaction_id, open_date, open_quantity,
    remaining_quantity, open_price, currency, status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: UpdateLotDisposal :exec
UPDATE lots SET
    remaining_quantity = $2, realized_pnl = $3, status = $4, close_date = $5,
    close_transaction_id = $6, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: SetLotQuantities :exec
UPDATE lots SET open_quantity = $2, remaining_quantity = $3, updated_at = sqlc.arg(now) WHERE id = $1;

-- name: AccountLots :many
SELECT * FROM lots WHERE account_id = $1;

-- name: OpenLots :many
SELECT * FROM lots WHERE account_id = $1 AND instrument_id = $2 AND status = 'open';

-- name: DeleteLot :exec
DELETE FROM lots WHERE id = $1;

-- name: DeleteAccountInstrumentLots :many
DELETE FROM lots WHERE account_id = $1 AND instrument_id = $2 RETURNING id;

-- name: LotsInWindow :many
SELECT * FROM lots
WHERE account_id = ANY(sqlc.arg(account_ids)::uuid[])
  AND instrument_id = ANY(sqlc.arg(instrument_ids)::uuid[])
  AND open_date >= sqlc.arg(start_date)::timestamptz AND open_date <= sqlc.arg(end_date)::timestamptz;

-- name: DisposalExistsForTransaction :one
SELECT EXISTS (SELECT 1 FROM lot_disposals WHERE transaction_id = $1);

-- name: DisposalsForTransaction :many
SELECT * FROM lot_disposals WHERE transaction_id = $1;

-- name: InsertDisposal :one
INSERT INTO lot_disposals (
    id, lot_id, transaction_id, quantity, proceeds, cost_basis, realized_pnl,
    holding_period, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.arg(now))
RETURNING *;

-- name: AdjustmentsForLots :many
SELECT * FROM lot_adjustments WHERE lot_id = ANY(sqlc.arg(lot_ids)::uuid[]);

-- name: InsertAdjustment :exec
INSERT INTO lot_adjustments (
    id, lot_id, source_transaction_id, kind, amount, quantity, currency,
    effective_date, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.arg(now));

-- name: WashMatchExists :one
SELECT EXISTS (SELECT 1 FROM wash_sale_matches WHERE disposal_id = $1 AND replacement_lot_id = $2);

-- name: InsertWashMatch :exec
INSERT INTO wash_sale_matches (
    id, disposal_id, replacement_lot_id, matched_quantity, disallowed_loss,
    currency, is_permanent, rule_version, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.arg(now));

-- name: DeleteOnePosition :exec
DELETE FROM positions
WHERE id = (SELECT p.id FROM positions p WHERE p.account_id = $1 AND p.instrument_id = $2 LIMIT 1);

-- name: InsertPosition :exec
INSERT INTO positions (
    id, account_id, instrument_id, quantity, average_cost, currency, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now), sqlc.arg(now));

-- name: DeleteAccountCash :exec
DELETE FROM cash_balances WHERE account_id = $1;

-- name: InsertCash :exec
INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, sqlc.arg(now), sqlc.arg(now));

-- name: DividendByExternal :many
SELECT * FROM dividends WHERE account_id = $1 AND external_id = $2 LIMIT 1;

-- name: InsertDividend :exec
INSERT INTO dividends (
    id, account_id, instrument_id, external_id, amount, currency, pay_date,
    withholding_tax, gross_amount, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, sqlc.arg(now), sqlc.arg(now));

-- name: SetDividendWithholding :exec
UPDATE dividends SET withholding_tax = $2, gross_amount = $3, updated_at = sqlc.arg(now) WHERE id = $1;

-- name: HasCompleteTaxProfile :one
SELECT EXISTS (
    SELECT 1 FROM tax_profiles WHERE user_id = $1 AND jurisdiction = $2 AND is_complete = true
);

-- name: PlannedLegs :many
SELECT * FROM tax_action_legs
WHERE account_id = $1 AND instrument_id = $2 AND side = $3 AND status = 'planned';

-- name: GetLeg :many
SELECT * FROM tax_action_legs WHERE id = $1;

-- name: MatchLeg :exec
UPDATE tax_action_legs SET status = $2, matched_transaction_id = $3, updated_at = sqlc.arg(now) WHERE id = $1;

-- name: PlanLegs :many
SELECT * FROM tax_action_legs WHERE action_plan_id = $1;

-- name: UserPlan :many
SELECT * FROM tax_action_plans WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: SetPlanStatus :exec
UPDATE tax_action_plans SET
    status = $2,
    executed_at = COALESCE(sqlc.narg(executed_at)::timestamptz, executed_at),
    updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: WindowExistsForLeg :one
SELECT EXISTS (SELECT 1 FROM tax_restriction_windows WHERE action_leg_id = $1);

-- name: InsertWindow :exec
INSERT INTO tax_restriction_windows (
    id, user_id, action_leg_id, jurisdiction, tax_identity_key, starts_at, ends_at,
    status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.arg(now), sqlc.arg(now));

-- name: ActiveWindows :many
SELECT * FROM tax_restriction_windows
WHERE user_id = $1 AND tax_identity_key = $2 AND status = 'active'
  AND starts_at <= sqlc.arg(at) AND ends_at >= sqlc.arg(at);

-- name: ViolateWindow :exec
UPDATE tax_restriction_windows SET status = 'violated', violating_transaction_id = $2, updated_at = sqlc.arg(now)
WHERE id = $1;
