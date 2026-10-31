-- PortfolioManagementController (/v1/portfolios/*, /v1/portfolio-invitations).
-- Access checks, default-list resolution and the shared inserts live in
-- db/queries/portfoliocore (internal/platform/portfolios).

-- name: OwnedPortfolioLists :many
SELECT * FROM portfolio_lists
WHERE user_id = $1
ORDER BY is_default DESC, created_at ASC;

-- name: ActiveMembershipPortfolioIDs :many
SELECT portfolio_id FROM portfolio_memberships
WHERE user_id = $1 AND status = 'active';

-- name: PortfolioListsByIDs :many
SELECT * FROM portfolio_lists WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CountActiveOwnedPortfolioLists :one
SELECT count(*) FROM portfolio_lists WHERE user_id = $1 AND archived_at IS NULL;

-- name: UpdatePortfolioListDetails :one
UPDATE portfolio_lists
SET name = $2, purpose = $3, ownership = $4, base_currency = $5, updated_at = $6
WHERE id = $1
RETURNING *;

-- name: ArchivePortfolioList :one
UPDATE portfolio_lists SET archived_at = $2, updated_at = $2 WHERE id = $1
RETURNING *;

-- name: SetPortfolioListOwnership :exec
UPDATE portfolio_lists SET ownership = $2, updated_at = $3 WHERE id = $1;

-- name: DeletePortfolioList :exec
DELETE FROM portfolio_lists WHERE id = $1;

-- name: CountAccountsInPortfolio :one
SELECT count(*) FROM accounts WHERE portfolio_id = $1;

-- name: CountAccountsInPortfolioExceptBroker :one
SELECT count(*) FROM accounts WHERE portfolio_id = $1 AND broker <> $2;

-- name: AccountsInPortfolio :many
SELECT * FROM accounts WHERE portfolio_id = $1;

-- name: OwnedAccount :many
SELECT * FROM accounts WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: UserBasics :many
SELECT id, username, email, is_verified FROM users WHERE id = $1;

-- name: UsersByIDs :many
SELECT id, username, email FROM users WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: UserIDsByEmail :many
SELECT id FROM users WHERE email = $1 LIMIT 1;

-- name: ActiveMemberships :many
-- Join order (Vapor's query had no ORDER BY).
SELECT * FROM portfolio_memberships WHERE portfolio_id = $1 AND status = 'active'
ORDER BY joined_at ASC NULLS LAST, created_at ASC NULLS LAST, id;

-- name: CountActiveMemberships :one
SELECT count(*) FROM portfolio_memberships WHERE portfolio_id = $1 AND status = 'active';

-- name: ActiveMembershipForUser :many
SELECT * FROM portfolio_memberships
WHERE portfolio_id = $1 AND user_id = $2 AND status = 'active'
LIMIT 1;

-- name: MembershipForUser :many
SELECT * FROM portfolio_memberships WHERE portfolio_id = $1 AND user_id = $2 LIMIT 1;

-- name: ActiveMembershipByID :many
SELECT * FROM portfolio_memberships
WHERE id = $1 AND portfolio_id = $2 AND status = 'active'
LIMIT 1;

-- name: InsertMembership :one
INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, joined_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
RETURNING *;

-- name: ActivateMembership :one
UPDATE portfolio_memberships SET role = $2, status = $3, joined_at = $4, updated_at = $4
WHERE id = $1
RETURNING *;

-- name: SetMembershipStatus :exec
UPDATE portfolio_memberships SET status = $2, updated_at = $3 WHERE id = $1;

-- name: InvitationsForPortfolio :many
SELECT * FROM portfolio_invitations WHERE portfolio_id = $1 ORDER BY created_at DESC;

-- name: PendingInvitationForEmail :many
SELECT id FROM portfolio_invitations
WHERE portfolio_id = $1 AND email = $2 AND status = 'pending'
LIMIT 1;

-- name: CountPendingInvitations :one
SELECT count(*) FROM portfolio_invitations WHERE portfolio_id = $1 AND status = 'pending';

-- name: InsertInvitation :one
INSERT INTO portfolio_invitations (id, portfolio_id, email, role, status, token_hash, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
RETURNING *;

-- name: PendingInvitationByTokenHash :many
SELECT * FROM portfolio_invitations WHERE token_hash = $1 AND status = 'pending' LIMIT 1;

-- name: InvitationByID :many
SELECT * FROM portfolio_invitations WHERE id = $1 AND portfolio_id = $2 LIMIT 1;

-- name: SetInvitationStatus :exec
UPDATE portfolio_invitations SET status = $2, updated_at = $3 WHERE id = $1;

-- name: AcceptInvitation :exec
UPDATE portfolio_invitations SET status = 'accepted', accepted_at = $2, updated_at = $2 WHERE id = $1;

-- name: CashPositionsForPortfolio :many
SELECT * FROM portfolio_cash_positions WHERE portfolio_id = $1 ORDER BY as_of DESC;

-- name: CashPositionByID :many
SELECT * FROM portfolio_cash_positions WHERE id = $1 AND portfolio_id = $2 LIMIT 1;

-- name: InsertCashPosition :one
INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
RETURNING *;

-- name: UpdateCashPosition :one
UPDATE portfolio_cash_positions
SET label = $2, currency = $3, balance = $4, as_of = $5, updated_at = $6
WHERE id = $1
RETURNING *;

-- name: DeleteCashPosition :exec
DELETE FROM portfolio_cash_positions WHERE id = $1;

-- name: StocksForPortfolio :many
SELECT * FROM stocks WHERE portfolio_list_id = $1;

-- name: InsertStock :exec
INSERT INTO stocks (
    id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, notes,
    category, source_provider, source_account_id, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, $11, $11
);

-- name: CashPositionsToCopy :many
SELECT * FROM portfolio_cash_positions WHERE portfolio_id = $1;

-- name: RetirementPlanForPortfolio :many
SELECT * FROM retirement_plans WHERE portfolio_id = $1 LIMIT 1;

-- name: InsertRetirementPlan :exec
INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5);
