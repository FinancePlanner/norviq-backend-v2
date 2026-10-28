-- Fixtures for parity/notifications.jsonl. Idempotent: safe to re-run on the
-- scratch database (psql "postgres://pg:pg@127.0.0.1:55499/norviq" -f ...).
-- Users: …a1 notifications/inbox, …a2 activity, …a3 badges, …a4 feedback,
-- …a5 empty (no data at all).

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('0e000000-0000-4000-8000-0000000000a1', 'parity-notif-a1@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0e000000-0000-4000-8000-0000000000a2', 'parity-notif-a2@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0e000000-0000-4000-8000-0000000000a3', 'parity-notif-a3@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0e000000-0000-4000-8000-0000000000a4', 'parity-notif-a4@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0e000000-0000-4000-8000-0000000000a5', 'parity-notif-a5@example.com', 'x', '2026-01-01T00:00:00Z', true)
ON CONFLICT (id) DO NOTHING;

-- Inbox: three events tie on created_at (cursor tie-break), one has
-- microseconds (cursor float arithmetic), one is read, one has an unknown kind.
INSERT INTO notification_events (id, user_id, kind, deduplication_key, title, body, deep_link, payload, created_at, read_at) VALUES
  ('0e000000-0000-4000-8000-000000000e01', '0e000000-0000-4000-8000-0000000000a1', 'budget', 'p-1', 'Budget threshold reached', '80% of this month''s budget remains.', 'norviq://expenses', '{"snapshot_id":"S","threshold":"80"}', '2026-10-01T10:00:00Z', NULL),
  ('0e000000-0000-4000-8000-000000000e02', '0e000000-0000-4000-8000-0000000000a1', 'budget', 'p-2', 'Tie 2', 'b', NULL, '{}', '2026-10-01T10:00:00Z', NULL),
  ('0e000000-0000-4000-8000-000000000e03', '0e000000-0000-4000-8000-0000000000a1', 'tax', 'p-3', 'Tie 3', 'b', NULL, '{}', '2026-10-01T10:00:00Z', '2026-10-02T08:30:00Z'),
  ('0e000000-0000-4000-8000-000000000e04', '0e000000-0000-4000-8000-0000000000a1', 'price_target', 'p-4', 'Price target reached', 'AAPL reached 151.5.', 'norviq://stocks/AAPL', '{"symbol":"AAPL","target_id":"T"}', '2026-10-03T09:15:42.123456Z', NULL),
  ('0e000000-0000-4000-8000-000000000e05', '0e000000-0000-4000-8000-0000000000a1', 'financial_goal', 'p-5', 'Goal', 'b', NULL, '{"goal_id":"G"}', '2026-10-04T00:00:00.000001Z', NULL),
  ('0e000000-0000-4000-8000-000000000e06', '0e000000-0000-4000-8000-0000000000a1', 'legacy_kind', 'p-6', 'Unknown kind', 'b', NULL, '{}', '2026-10-05T00:00:00Z', NULL)
ON CONFLICT (id) DO NOTHING;

-- Activity feed.
INSERT INTO user_activities (id, user_id, type, title, subtitle, amount, is_growth, symbol, created_at, reference_key) VALUES
  ('0e000000-0000-4000-8000-000000000a01', '0e000000-0000-4000-8000-0000000000a2', 'stock_added', 'Stock Added', 'AAPL - Apple Inc.', 5000, true, 'chart.line.uptrend.xyaxis', '2026-10-01T12:00:00Z', NULL),
  ('0e000000-0000-4000-8000-000000000a02', '0e000000-0000-4000-8000-0000000000a2', 'expense_recorded', 'Expense Added', 'Groceries', 150.25, false, 'house.fill', '2026-10-01T12:01:00Z', NULL),
  ('0e000000-0000-4000-8000-000000000a03', '0e000000-0000-4000-8000-0000000000a2', 'news_viewed', 'News', 'Fed holds rates', NULL, false, 'newspaper', '2026-10-01T12:02:00Z', 'news:1')
ON CONFLICT (id) DO NOTHING;

-- Badges: holdings, a pilot holding, budget months, expenses, news views.
INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('0e000000-0000-4000-8000-000000000b01', '0e000000-0000-4000-8000-0000000000a3', 'Parity', '2026-01-01T00:00:00Z')
ON CONFLICT (id) DO NOTHING;
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, source_provider) VALUES
  ('0e000000-0000-4000-8000-000000000b11', '0e000000-0000-4000-8000-0000000000a3', 'AAPL', 1, 1, '2026-01-02', '2026-01-02T00:00:00Z', '0e000000-0000-4000-8000-000000000b01', NULL),
  ('0e000000-0000-4000-8000-000000000b12', '0e000000-0000-4000-8000-0000000000a3', 'MSFT', 1, 1, '2026-01-02', '2026-01-02T00:00:00Z', '0e000000-0000-4000-8000-000000000b01', NULL),
  ('0e000000-0000-4000-8000-000000000b13', '0e000000-0000-4000-8000-0000000000a3', 'NVDA', 1, 1, '2026-01-02', '2026-01-02T00:00:00Z', '0e000000-0000-4000-8000-000000000b01', 'pilot')
ON CONFLICT (id) DO NOTHING;
INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares) VALUES
  ('0e000000-0000-4000-8000-000000000b21', '0e000000-0000-4000-8000-0000000000a3', '2026-01-01', 3000, '{}'),
  ('0e000000-0000-4000-8000-000000000b22', '0e000000-0000-4000-8000-0000000000a3', '2026-02-01', 3000, '{}')
ON CONFLICT (id) DO NOTHING;
INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, split_mode, user_share_percent) VALUES
  ('0e000000-0000-4000-8000-000000000b31', '0e000000-0000-4000-8000-000000000b21', '0e000000-0000-4000-8000-0000000000a3', 'Fun', 200, 'fun', 'personal', 100),
  ('0e000000-0000-4000-8000-000000000b32', '0e000000-0000-4000-8000-000000000b22', '0e000000-0000-4000-8000-0000000000a3', 'Fun', 200, 'fun', 'personal', 100)
ON CONFLICT (id) DO NOTHING;
INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, split_mode, user_share_percent, created_at) VALUES
  ('0e000000-0000-4000-8000-000000000b41', '0e000000-0000-4000-8000-0000000000a3', 'Rent', 1900, 'fundamentals', '2026-01-05', 'personal', 100, '2026-01-05T00:00:00Z'),
  ('0e000000-0000-4000-8000-000000000b42', '0e000000-0000-4000-8000-0000000000a3', 'Cinema', 100, 'fun', '2026-01-20', 'personal', 100, '2026-01-20T00:00:00Z'),
  ('0e000000-0000-4000-8000-000000000b43', '0e000000-0000-4000-8000-0000000000a3', 'Rent', 1200, 'fundamentals', '2026-02-03', 'personal', 100, '2026-02-03T00:00:00Z'),
  ('0e000000-0000-4000-8000-000000000b44', '0e000000-0000-4000-8000-0000000000a3', 'Concert', 300, 'Fun', '2026-02-10', 'personal', 100, '2026-02-10T00:00:00Z')
ON CONFLICT (id) DO NOTHING;
INSERT INTO user_activities (id, user_id, type, title, subtitle, amount, is_growth, symbol, created_at) VALUES
  ('0e000000-0000-4000-8000-000000000b51', '0e000000-0000-4000-8000-0000000000a3', 'news_viewed', 'News', 'n', NULL, false, 'newspaper', '2026-10-01T00:00:00Z')
ON CONFLICT (id) DO NOTHING;
