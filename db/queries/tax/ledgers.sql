-- Loss carryforward ledgers (PortugalLossCarryforwardLedger,
-- GermanyStockLossLedger, GermanyGeneralLossLedger) and German fund advance
-- lump sums (GermanyFundAnnualInputService, GermanyFundAdvanceAllocationService).

-- name: CarryforwardsAsOf :many
SELECT * FROM tax_loss_carryforwards
WHERE user_id = $1 AND jurisdiction = $2 AND source_tax_year <= $3
ORDER BY source_tax_year DESC;

-- name: EligibleCarryforwards :many
SELECT * FROM tax_loss_carryforwards
WHERE user_id = @user_id AND jurisdiction = 'PT' AND source_tax_year < @tax_year AND expires_after_tax_year >= @tax_year;

-- name: CarryforwardForYear :one
SELECT * FROM tax_loss_carryforwards WHERE user_id = $1 AND jurisdiction = 'PT' AND source_tax_year = $2 LIMIT 1;

-- name: InsertCarryforward :exec
INSERT INTO tax_loss_carryforwards (id, user_id, jurisdiction, source_tax_year, expires_after_tax_year, original_amount, remaining_amount, currency, rule_version, created_at, updated_at)
VALUES (@id, @user_id, @jurisdiction, @source_tax_year, @expires_after_tax_year, @original_amount, @remaining_amount, @currency, @rule_version, @now, @now);

-- name: UpdateCarryforward :exec
UPDATE tax_loss_carryforwards
SET expires_after_tax_year = @expires_after_tax_year, original_amount = @original_amount, remaining_amount = @remaining_amount,
    currency = @currency, rule_version = @rule_version, updated_at = @now
WHERE id = @id;

-- name: UpdateCarryforwardRemaining :exec
UPDATE tax_loss_carryforwards SET remaining_amount = @remaining_amount, updated_at = @now WHERE id = @id;

-- name: CarryforwardApplicationsSorted :many
SELECT * FROM tax_loss_carryforward_applications WHERE carryforward_id = ANY(@ids::uuid[]) ORDER BY target_tax_year ASC;

-- name: CarryforwardApplicationsBefore :many
SELECT * FROM tax_loss_carryforward_applications WHERE carryforward_id = ANY(@ids::uuid[]) AND target_tax_year < @tax_year;

-- name: CarryforwardApplicationsFor :many
SELECT * FROM tax_loss_carryforward_applications WHERE carryforward_id = ANY(@ids::uuid[]) AND target_tax_year = @tax_year;

-- name: CarryforwardApplicationsOf :many
SELECT * FROM tax_loss_carryforward_applications WHERE carryforward_id = $1;

-- name: CarryforwardApplicationsOfBefore :many
SELECT * FROM tax_loss_carryforward_applications WHERE carryforward_id = $1 AND target_tax_year < $2;

-- name: DeleteCarryforwardApplication :exec
DELETE FROM tax_loss_carryforward_applications WHERE id = $1;

-- name: InsertCarryforwardApplication :exec
INSERT INTO tax_loss_carryforward_applications (id, carryforward_id, target_tax_year, amount, created_at, updated_at)
VALUES (@id, @carryforward_id, @target_tax_year, @amount, @now, @now);

-- name: StockLossYearsAsOf :many
SELECT * FROM germany_stock_loss_years WHERE user_id = $1 AND tax_year <= $2 ORDER BY tax_year ASC;

-- name: StockLossYears :many
SELECT * FROM germany_stock_loss_years WHERE user_id = $1 ORDER BY tax_year ASC;

-- name: StockLossYearFor :one
SELECT * FROM germany_stock_loss_years WHERE user_id = $1 AND tax_year = $2 LIMIT 1;

-- name: InsertStockLossYear :exec
INSERT INTO germany_stock_loss_years (id, user_id, tax_year, net_stock_result, loss_generated, prior_loss_applied, taxable_stock_gain, ending_loss_carryforward, rule_version, created_at, updated_at)
VALUES (@id, @user_id, @tax_year, @net_stock_result, 0, 0, 0, 0, @rule_version, @now, @now);

-- name: UpdateStockLossYearInput :exec
UPDATE germany_stock_loss_years SET net_stock_result = @net_stock_result, rule_version = @rule_version, updated_at = @now WHERE id = @id;

-- name: UpdateStockLossYearResult :exec
UPDATE germany_stock_loss_years
SET loss_generated = @loss_generated, prior_loss_applied = @prior_loss_applied, taxable_stock_gain = @taxable_gain,
    ending_loss_carryforward = @ending_loss_carryforward, updated_at = @now
WHERE id = @id;

-- name: StockLossApplicationsSorted :many
SELECT * FROM germany_stock_loss_applications WHERE source_year_id = ANY(@ids::uuid[]) ORDER BY target_tax_year ASC;

-- name: StockLossApplications :many
SELECT * FROM germany_stock_loss_applications WHERE source_year_id = ANY(@ids::uuid[]);

-- name: UpsertStockLossApplicationInsert :exec
INSERT INTO germany_stock_loss_applications (id, source_year_id, target_tax_year, amount, created_at, updated_at)
VALUES (@id, @source_year_id, @target_tax_year, @amount, @now, @now);

-- name: UpdateStockLossApplication :exec
UPDATE germany_stock_loss_applications SET amount = @amount, updated_at = @now WHERE id = @id;

-- name: DeleteStockLossApplication :exec
DELETE FROM germany_stock_loss_applications WHERE id = $1;

-- name: GeneralLossYearsAsOf :many
SELECT * FROM germany_general_loss_years WHERE user_id = $1 AND tax_year <= $2 ORDER BY tax_year ASC;

-- name: GeneralLossYears :many
SELECT * FROM germany_general_loss_years WHERE user_id = $1 ORDER BY tax_year ASC;

-- name: GeneralLossYearFor :one
SELECT * FROM germany_general_loss_years WHERE user_id = $1 AND tax_year = $2 LIMIT 1;

-- name: InsertGeneralLossYear :exec
INSERT INTO germany_general_loss_years (id, user_id, tax_year, net_capital_result, loss_generated, prior_loss_applied, taxable_capital_gain, ending_loss_carryforward, rule_version, created_at, updated_at)
VALUES (@id, @user_id, @tax_year, @net_capital_result, 0, 0, 0, 0, @rule_version, @now, @now);

-- name: UpdateGeneralLossYearInput :exec
UPDATE germany_general_loss_years SET net_capital_result = @net_capital_result, rule_version = @rule_version, updated_at = @now WHERE id = @id;

-- name: UpdateGeneralLossYearResult :exec
UPDATE germany_general_loss_years
SET loss_generated = @loss_generated, prior_loss_applied = @prior_loss_applied, taxable_capital_gain = @taxable_gain,
    ending_loss_carryforward = @ending_loss_carryforward, updated_at = @now
WHERE id = @id;

-- name: GeneralLossApplicationsSorted :many
SELECT * FROM germany_general_loss_applications WHERE source_year_id = ANY(@ids::uuid[]) ORDER BY target_tax_year ASC;

-- name: GeneralLossApplications :many
SELECT * FROM germany_general_loss_applications WHERE source_year_id = ANY(@ids::uuid[]);

-- name: InsertGeneralLossApplication :exec
INSERT INTO germany_general_loss_applications (id, source_year_id, target_tax_year, amount, created_at, updated_at)
VALUES (@id, @source_year_id, @target_tax_year, @amount, @now, @now);

-- name: UpdateGeneralLossApplication :exec
UPDATE germany_general_loss_applications SET amount = @amount, updated_at = @now WHERE id = @id;

-- name: DeleteGeneralLossApplication :exec
DELETE FROM germany_general_loss_applications WHERE id = $1;

-- name: FundAnnualHoldings :many
SELECT * FROM germany_fund_annual_holdings
WHERE user_id = $1 AND account_id = $2 AND instrument_id = $3 AND calculation_year = $4
ORDER BY client_holding_id ASC;

-- name: DeleteFundAnnualHoldings :exec
DELETE FROM germany_fund_annual_holdings
WHERE user_id = $1 AND account_id = $2 AND instrument_id = $3 AND calculation_year = $4;

-- name: InsertFundAnnualHolding :exec
INSERT INTO germany_fund_annual_holdings (id, user_id, account_id, instrument_id, calculation_year, client_holding_id, lot_id,
    quantity, remaining_quantity, currency, beginning_market_value, ending_market_value, distributions, acquisition_month,
    fund_classification, basis_rate, gross_advance_lump_sum, remaining_gross_advance, taxable_advance_lump_sum, rule_version,
    created_at, updated_at)
VALUES (@id, @user_id, @account_id, @instrument_id, @calculation_year, @client_holding_id, @lot_id,
    @quantity, @remaining_quantity, @currency, @beginning_market_value, @ending_market_value, @distributions, @acquisition_month,
    @fund_classification, @basis_rate, @gross_advance_lump_sum, @remaining_gross_advance, @taxable_advance_lump_sum, @rule_version,
    @now, @now);

-- name: FundAnnualHoldingsForLots :many
SELECT * FROM germany_fund_annual_holdings WHERE lot_id = ANY(@lot_ids::uuid[]) ORDER BY calculation_year ASC;

-- name: UpdateFundAnnualHoldingRemaining :exec
UPDATE germany_fund_annual_holdings SET remaining_quantity = @remaining_quantity, remaining_gross_advance = @remaining_gross_advance, updated_at = @now
WHERE id = @id;

-- name: FundAdvanceAllocationsForHoldings :many
SELECT * FROM germany_fund_advance_allocations WHERE annual_holding_id = ANY(@ids::uuid[]);

-- name: InsertFundAdvanceAllocation :exec
INSERT INTO germany_fund_advance_allocations (id, annual_holding_id, disposal_id, quantity, gross_advance_amount, created_at, updated_at)
VALUES (@id, @annual_holding_id, @disposal_id, @quantity, @gross_advance_amount, @now, @now);

-- name: UpdateFundAdvanceAllocation :exec
UPDATE germany_fund_advance_allocations SET quantity = @quantity, gross_advance_amount = @gross_advance_amount, updated_at = @now WHERE id = @id;

-- name: DeleteFundAdvanceAllocation :exec
DELETE FROM germany_fund_advance_allocations WHERE id = $1;
