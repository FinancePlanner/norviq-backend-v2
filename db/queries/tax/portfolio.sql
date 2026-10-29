-- Portfolio tables the tax engine reads (Account, Instrument, Lot, Position,
-- Transaction, LotDisposal, LotAdjustment, Dividend in Vapor). Tax queries
-- them itself; the portfolio domain owns their writes. No ORDER BY where
-- Fluent's `.all()` had none, so row order matches Vapor's.

-- name: AccountsForUser :many
SELECT * FROM accounts WHERE user_id = $1;

-- name: AccountsForUserExceptBroker :many
SELECT * FROM accounts WHERE user_id = $1 AND broker <> $2;

-- name: AccountsByIDsForUser :many
SELECT * FROM accounts WHERE user_id = @user_id AND id = ANY(@ids::uuid[]);

-- name: AccountByID :one
SELECT * FROM accounts WHERE id = $1;

-- name: AccountByIDForUser :one
SELECT * FROM accounts WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: FirstAccountByIDs :one
SELECT * FROM accounts WHERE id = ANY(@ids::uuid[]) LIMIT 1;

-- name: UpdateAccountTaxClassification :exec
UPDATE accounts
SET tax_wrapper = @tax_wrapper, tax_jurisdiction = @tax_jurisdiction,
    tax_owner_member_id = @tax_owner_member_id, lot_selection_method = @lot_selection_method,
    updated_at = @now
WHERE id = @id;

-- name: TransactionInstrumentIDsForAccounts :many
SELECT instrument_id FROM transactions WHERE account_id = ANY(@account_ids::uuid[]);

-- name: TransactionExistsForAccountsInstrument :one
SELECT EXISTS (
    SELECT 1 FROM transactions WHERE account_id = ANY(@account_ids::uuid[]) AND instrument_id = @instrument_id
);

-- name: LotExistsForAccountsInstrument :one
SELECT EXISTS (
    SELECT 1 FROM lots WHERE account_id = ANY(@account_ids::uuid[]) AND instrument_id = @instrument_id
);

-- name: InstrumentsByIDs :many
SELECT * FROM instruments WHERE id = ANY(@ids::uuid[]);

-- name: InstrumentByID :one
SELECT * FROM instruments WHERE id = $1;

-- name: InstrumentsBySymbols :many
SELECT * FROM instruments WHERE symbol = ANY(@symbols::text[]);

-- name: InstrumentIDsByTaxIdentityGroup :many
SELECT id FROM instruments WHERE tax_identity_group = $1;

-- name: UpdateInstrumentMarketAdmission :exec
UPDATE instruments
SET regulated_market_status = @status, regulated_market_source = @source,
    regulated_market_reviewed_at = @reviewed_at, updated_at = @now
WHERE id = @id;

-- name: UpdateInstrumentFundClassification :exec
UPDATE instruments SET fund_classification = @fund_classification, updated_at = @now WHERE id = @id;

-- name: LotsForAccounts :many
SELECT * FROM lots WHERE account_id = ANY(@account_ids::uuid[]);

-- name: OpenLotsForAccounts :many
SELECT * FROM lots WHERE account_id = ANY(@account_ids::uuid[]) AND status = 'open';

-- name: LotsByIDs :many
SELECT * FROM lots WHERE id = ANY(@ids::uuid[]);

-- name: LotsForAccountInstrumentByOpenDate :many
SELECT * FROM lots WHERE account_id = $1 AND instrument_id = $2 ORDER BY open_date ASC;

-- name: LotsClosedInRange :many
SELECT * FROM lots
WHERE account_id = ANY(@account_ids::uuid[]) AND close_date >= @start::timestamptz AND close_date < @end_at::timestamptz;

-- name: LotAdjustmentsForLots :many
SELECT * FROM lot_adjustments WHERE lot_id = ANY(@lot_ids::uuid[]);

-- name: DisposalsForLots :many
SELECT * FROM lot_disposals WHERE lot_id = ANY(@lot_ids::uuid[]);

-- name: DisposalsByIDs :many
SELECT * FROM lot_disposals WHERE id = ANY(@ids::uuid[]);

-- name: DisposalsByTransactionIDs :many
SELECT * FROM lot_disposals WHERE transaction_id = ANY(@transaction_ids::uuid[]);

-- name: PositionsForAccounts :many
SELECT * FROM positions WHERE account_id = ANY(@account_ids::uuid[]);

-- name: TransactionsByIDs :many
SELECT * FROM transactions WHERE id = ANY(@ids::uuid[]);

-- name: TransactionsByIDsInRange :many
SELECT * FROM transactions
WHERE id = ANY(@ids::uuid[]) AND trade_date >= @start::timestamptz AND trade_date < @end_at::timestamptz;

-- name: SellTransactionsInRange :many
SELECT * FROM transactions
WHERE account_id = ANY(@account_ids::uuid[]) AND type = 'SELL'
  AND trade_date >= @start::timestamptz AND trade_date < @end_at::timestamptz;

-- name: TransactionExistsForAccountInstrument :one
SELECT EXISTS (SELECT 1 FROM transactions WHERE account_id = $1 AND instrument_id = $2);

-- name: RecentBuys :many
SELECT * FROM transactions
WHERE account_id = ANY(@account_ids::uuid[]) AND instrument_id = ANY(@instrument_ids::uuid[])
  AND trade_date >= @since::timestamptz AND type = ANY(ARRAY['BUY', 'BOT', 'buy']);

-- name: BuysBetween :many
SELECT * FROM transactions
WHERE account_id = ANY(@account_ids::uuid[]) AND type = 'BUY'
  AND trade_date >= @start::timestamptz AND trade_date <= @end_at::timestamptz;

-- name: DividendsPaidInRange :many
SELECT * FROM dividends
WHERE account_id = ANY(@account_ids::uuid[]) AND pay_date >= @start::timestamptz AND pay_date < @end_at::timestamptz;

-- name: AdjustedCloses :many
SELECT date, adjusted_close FROM market_price_bars
WHERE instrument_key = @instrument_key AND date BETWEEN @start::timestamptz AND @end_at::timestamptz
ORDER BY date ASC;
