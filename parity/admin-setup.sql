-- Parity fixtures for the admin / export / misc slice (parity/admin*.jsonl).
-- Idempotent: every run deletes and recreates the rows it owns (ids
-- ad000000-..., emails parity-admin-*@example.com).
-- The ready export points at a file both servers can read; pass its absolute
-- path (any small file, e.g. parity/admin-export-fixture.csv):
--   docker exec -i norviq-v2-schema-pg psql -U pg -d norviq -v export_file=$PWD/parity/admin-export-fixture.csv < parity/admin-setup.sql
BEGIN;

DELETE FROM data_exports WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM transactions WHERE account_id::text LIKE 'ad000000-%';
DELETE FROM accounts WHERE id::text LIKE 'ad000000-%';
DELETE FROM instruments WHERE id::text LIKE 'ad000000-%';
DELETE FROM research_notes WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM watchlist_items WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM watchlist_lists WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM stocks WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM portfolio_share_links WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM group_messages WHERE group_id::text LIKE 'ad000000-%';
DELETE FROM group_channels WHERE group_id::text LIKE 'ad000000-%';
DELETE FROM groups WHERE id::text LIKE 'ad000000-%';
DELETE FROM board_comments WHERE author_id::text LIKE 'ad000000-%';
DELETE FROM board_posts WHERE author_id::text LIKE 'ad000000-%';
DELETE FROM boards WHERE id::text LIKE 'ad000000-%';
DELETE FROM social_reports WHERE reporter_id::text LIKE 'ad000000-%';
DELETE FROM community_sanctions WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM social_friendships WHERE user_id::text LIKE 'ad000000-%' OR friend_id::text LIKE 'ad000000-%';
DELETE FROM social_friend_requests WHERE from_user_id::text LIKE 'ad000000-%' OR to_user_id::text LIKE 'ad000000-%';
DELETE FROM social_settings WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM articles WHERE author_id::text LIKE 'ad000000-%';
DELETE FROM community_guidelines_acceptances WHERE user_id::text LIKE 'ad000000-%';
DELETE FROM users WHERE id::text LIKE 'ad000000-%';

-- 01 admin, 02 troll, 03 reporter, 04 author, 05/06 deletion subjects,
-- 07 exporter, 08 sharer, 09 system account.
INSERT INTO users (id, email, password_hash, username, created_at, is_verified) VALUES
  ('ad000000-0000-4000-8000-000000000001', 'parity-admin-boss@example.com', 'x', 'parity_admin_boss', '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000002', 'parity-admin-troll@example.com', 'x', 'parity_admin_troll', '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000003', 'parity-admin-rep@example.com', 'x', 'parity_admin_rep', '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000004', 'parity-admin-author@example.com', 'x', 'parity_admin_author', '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000005', 'parity-admin-leaver@example.com', 'x', NULL, '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000006', 'parity-admin-leaver2@example.com', 'x', NULL, '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000007', 'parity-admin-exporter@example.com', 'x', NULL, '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000008', 'parity-admin-sharer@example.com', 'x', NULL, '2026-10-01T00:00:00Z', true),
  ('ad000000-0000-4000-8000-000000000009', 'parity-admin-sys@system.norviq.invalid', 'x', 'parity_admin_sys', '2026-10-01T00:00:00Z', true);

INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES
  ('ad000000-0000-4000-8000-0000000000a1', 'ad000000-0000-4000-8000-000000000001', '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-0000000000a4', 'ad000000-0000-4000-8000-000000000004', '2026-10-01T00:00:00Z');

-- Boards: one to delete, one whose post/comment get reported.
INSERT INTO boards (id, slug, name, description, creator_id, post_count, last_activity_at, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000b1', 'parity-admin-doomed', 'Doomed', 'd', 'ad000000-0000-4000-8000-000000000004', 0, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-0000000000b2', 'parity-admin-board', 'Board', 'd', NULL, 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-0000000000b3', 'parity-admin-doomed2', 'Doomed 2', 'd', NULL, 0, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z');
INSERT INTO board_posts (id, board_id, author_id, kind, title, tags, score, comment_count, participant_count, view_count, last_activity_at, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000c1', 'ad000000-0000-4000-8000-0000000000b2', 'ad000000-0000-4000-8000-000000000004', 'text', 'Parity admin post about NEXT', '{}', 0, 1, 1, 0, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z');
INSERT INTO board_comments (id, post_id, author_id, body, depth, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000c2', 'ad000000-0000-4000-8000-0000000000c1', 'ad000000-0000-4000-8000-000000000004', 'A reported comment with "quotes" & <tags>', 0, '2026-10-01T00:00:00Z');
INSERT INTO groups (id, name, description, topic, visibility, owner_id, member_count, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000d1', 'Parity admin group', 'd', 'general', 'private', 'ad000000-0000-4000-8000-000000000004', 1, '2026-10-01T00:00:00Z');
INSERT INTO group_channels (id, group_id, name, kind, position, last_seq, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000d2', 'ad000000-0000-4000-8000-0000000000d1', 'general', 'text', 0, 1, '2026-10-01T00:00:00Z');
INSERT INTO group_messages (id, channel_id, group_id, author_id, seq, client_message_id, body, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000d3', 'ad000000-0000-4000-8000-0000000000d2', 'ad000000-0000-4000-8000-0000000000d1', 'ad000000-0000-4000-8000-000000000004', 1, 'parity-admin-1', 'A reported group message', '2026-10-01T00:00:00Z');

-- Reports: community (post, comment, group message, gone post) and social
-- (user, message).
INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, status, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000e1', 'ad000000-0000-4000-8000-000000000003', 'board_post', 'ad000000-0000-4000-8000-0000000000c1', 'spam', 'open', '2026-10-02T00:00:01Z'),
  ('ad000000-0000-4000-8000-0000000000e2', 'ad000000-0000-4000-8000-000000000003', 'board_comment', 'AD000000-0000-4000-8000-0000000000C2', 'weird', 'open', '2026-10-02T00:00:02Z'),
  ('ad000000-0000-4000-8000-0000000000e3', 'ad000000-0000-4000-8000-000000000003', 'group_message', 'ad000000-0000-4000-8000-0000000000d3', 'hate', 'open', '2026-10-02T00:00:03Z'),
  ('ad000000-0000-4000-8000-0000000000e4', 'ad000000-0000-4000-8000-000000000003', 'board_post', 'ad000000-0000-4000-8000-0000000000ff', 'scam', 'open', '2026-10-02T00:00:04Z'),
  ('ad000000-0000-4000-8000-0000000000e5', 'ad000000-0000-4000-8000-000000000003', 'user', 'ad000000-0000-4000-8000-000000000002', 'spam', 'open', '2026-10-02T00:00:05Z'),
  ('ad000000-0000-4000-8000-0000000000e6', 'ad000000-0000-4000-8000-000000000003', 'message', 'parity-admin-msg', 'hate', 'open', '2026-10-02T00:00:06Z');

INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_at) VALUES
  ('ad000000-0000-4000-8000-0000000000f1', 'ad000000-0000-4000-8000-000000000002', 'mute', 'Seeded', '2027-01-01T00:00:00Z', '2026-10-01T00:00:00Z');

INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000101', 'ad000000-0000-4000-8000-000000000002', 'ad000000-0000-4000-8000-000000000003', '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-000000000102', 'ad000000-0000-4000-8000-000000000003', 'ad000000-0000-4000-8000-000000000002', '2026-10-01T00:00:00Z');

-- An article to hide and show.
INSERT INTO articles (id, code, author_id, slug, title, body_markdown, bullet_points, tickers, disclosure, status, source, view_count, upvote_count, word_count, published_at, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000201', 'padmin01', 'ad000000-0000-4000-8000-000000000004', 'parity-admin-article', 'Parity admin article', 'Body', '{"One"}', '{"NEXT"}', 'No position.', 'published', 'web', 0, 0, 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z');

-- Exports: one ready (file on disk), one expired, one pending.
INSERT INTO data_exports (id, user_id, type, format, filters, status, file_path, file_size_bytes, expires_at, created_at, updated_at) VALUES
  ('ad000000-0000-4000-8000-000000000301', 'ad000000-0000-4000-8000-000000000007', 'portfolio', 'csv', '{"dateFrom":"2026-01-01T00:00:00Z"}', 'ready', :'export_file', 9, '2099-01-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-000000000302', 'ad000000-0000-4000-8000-000000000007', 'all', 'json', NULL, 'ready', :'export_file', 9, '2026-10-02T00:00:00Z', '2026-09-30T00:00:00Z', NULL),
  ('ad000000-0000-4000-8000-000000000303', 'ad000000-0000-4000-8000-000000000007', 'watchlist', 'json', NULL, 'pending', NULL, NULL, NULL, '2026-09-29T00:00:00Z', NULL);

-- Share: a portfolio with two holdings, an active link and a revoked one.
INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000401', 'ad000000-0000-4000-8000-000000000008', 'Main', true, '2026-10-01T00:00:00Z');
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('ad000000-0000-4000-8000-000000000402', 'ad000000-0000-4000-8000-000000000008', 'PARITYAAA', 3, 200, '2026-01-02', '2026-10-01T00:00:00Z', 'ad000000-0000-4000-8000-000000000401'),
  ('ad000000-0000-4000-8000-000000000403', 'ad000000-0000-4000-8000-000000000008', 'PARITYBBB', 1, 200, '2026-01-02', '2026-10-01T00:00:00Z', 'ad000000-0000-4000-8000-000000000401');
INSERT INTO portfolio_share_links (id, user_id, portfolio_list_id, slug, created_at, revoked_at) VALUES
  ('ad000000-0000-4000-8000-000000000404', 'ad000000-0000-4000-8000-000000000008', NULL, 'pparityadmin00000000000', '2026-10-01T00:00:00Z', NULL),
  ('ad000000-0000-4000-8000-000000000405', 'ad000000-0000-4000-8000-000000000008', 'ad000000-0000-4000-8000-000000000401', 'pparityadminrevoked0000', '2026-10-01T00:00:00Z', '2026-10-02T00:00:00Z');

-- Deletion subject 05 has rows in a few cascading tables.
INSERT INTO feedbacks (id, topic, message, user_id) VALUES
  ('ad000000-0000-4000-8000-000000000501', 'bug', 'bye', 'ad000000-0000-4000-8000-000000000005');
INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000502', 'ad000000-0000-4000-8000-000000000005', 'Main', true, '2026-10-01T00:00:00Z');

-- Export content for 07: awkward strings and numbers, so generated files
-- can be compared byte for byte (see parity/admin-exports.jsonl).
INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000601', 'ad000000-0000-4000-8000-000000000007', 'Main', true, '2026-10-01T00:00:00Z');
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, notes, category, created_at, portfolio_list_id) VALUES
  ('ad000000-0000-4000-8000-000000000602', 'ad000000-0000-4000-8000-000000000007', 'PARITYCCC', 1.5, 123.456, '2026-01-02', 'He said "buy", a/b é 🚀
line two', 'etf', '2026-10-01T10:11:12Z', 'ad000000-0000-4000-8000-000000000601'),
  ('ad000000-0000-4000-8000-000000000603', 'ad000000-0000-4000-8000-000000000007', 'PARITYDDD', 10, 0.1, '2026-02-03', NULL, 'stock', '2026-10-01T10:11:13.5Z', 'ad000000-0000-4000-8000-000000000601');
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000604', 'ad000000-0000-4000-8000-000000000007', 'Main', true, '2026-10-01T00:00:00Z');
INSERT INTO watchlist_items (id, user_id, symbol, note, status, watchlist_list_id, last_reviewed_at, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000605', 'ad000000-0000-4000-8000-000000000007', 'PARITYEEE', 'tab	and \ backslash, "q"', 'watching', 'ad000000-0000-4000-8000-000000000604', '2026-10-05T01:02:03Z', '2026-10-01T00:00:00Z');
INSERT INTO research_notes (id, user_id, symbol, title, thesis, risks, catalysts, reference_links, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000606', 'ad000000-0000-4000-8000-000000000007', 'PARITYFFF', NULL, 'Thesis with <html> & "quotes"', 'Risk', NULL, 'https://example.com/a?b=1', '2026-10-01T00:00:00Z');
INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000607', 'parity-admin-607', 'PARITYCCC', 'NYSE', 'USD', '2026-10-01T00:00:00Z');
INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES
  ('ad000000-0000-4000-8000-000000000608', 'ad000000-0000-4000-8000-000000000007', 'parity-admin-acct', 'manual', 'USD', '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-000000000609', 'ad000000-0000-4000-8000-000000000007', 'parity-admin-pilot', 'pilot', 'USD', '2026-10-01T00:00:00Z');
INSERT INTO transactions (id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, settle_date, fees, created_at) VALUES
  ('ad000000-0000-4000-8000-00000000060a', 'ad000000-0000-4000-8000-000000000608', 'ad000000-0000-4000-8000-000000000607', 'x-1', 'BUY', 2.25, 99.99, 'USD', '2026-03-04', '2026-03-06', 1.0, '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-00000000060b', 'ad000000-0000-4000-8000-000000000608', 'ad000000-0000-4000-8000-000000000607', NULL, 'SELL', 1e-7, 1e21, 'USD', '2025-12-31', NULL, NULL, '2026-10-01T00:00:00Z'),
  ('ad000000-0000-4000-8000-00000000060c', 'ad000000-0000-4000-8000-000000000609', 'ad000000-0000-4000-8000-000000000607', NULL, 'BUY', 1, 1, 'USD', '2026-03-04', NULL, NULL, '2026-10-01T00:00:00Z');

COMMIT;
