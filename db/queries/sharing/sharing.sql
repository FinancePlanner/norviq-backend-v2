-- PortfolioShareController.publicShare (GET /v1/public/portfolio-shares/:slug).

-- name: ActiveShareLinkBySlug :many
SELECT user_id, portfolio_list_id FROM portfolio_share_links
WHERE slug = @slug AND revoked_at IS NULL
LIMIT 1;
