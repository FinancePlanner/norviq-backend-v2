-- Parity fixtures for the boards slice (parity/boards.jsonl). Idempotent:
-- every run deletes and recreates the rows it owns (ids b0a4d000-...).
--   docker exec -i norviq-v2-schema-pg psql -U pg -d norviq < parity/boards-setup.sql
BEGIN;

DELETE FROM board_notifications WHERE recipient_id::text LIKE 'b0a4d000-%' OR actor_id::text LIKE 'b0a4d000-%';
DELETE FROM board_notification_settings WHERE user_id::text LIKE 'b0a4d000-%';
DELETE FROM board_votes WHERE user_id::text LIKE 'b0a4d000-%';
DELETE FROM board_post_views WHERE user_id::text LIKE 'b0a4d000-%';
DELETE FROM board_comments WHERE author_id::text LIKE 'b0a4d000-%' OR post_id::text LIKE 'b0a4d000-%';
DELETE FROM board_posts WHERE author_id::text LIKE 'b0a4d000-%' OR board_id::text LIKE 'b0a4d000-%';
DELETE FROM boards WHERE creator_id::text LIKE 'b0a4d000-%' OR id::text LIKE 'b0a4d000-%';
DELETE FROM social_reports WHERE reporter_id::text LIKE 'b0a4d000-%';
DELETE FROM social_blocks WHERE blocker_id::text LIKE 'b0a4d000-%' OR blocked_id::text LIKE 'b0a4d000-%';
DELETE FROM social_settings WHERE user_id::text LIKE 'b0a4d000-%';
DELETE FROM community_sanctions WHERE user_id::text LIKE 'b0a4d000-%';
DELETE FROM community_guidelines_acceptances WHERE user_id::text LIKE 'b0a4d000-%';
DELETE FROM users WHERE id::text LIKE 'b0a4d000-%';

INSERT INTO users (id, email, password_hash, username, created_at, is_verified) VALUES
  ('b0a4d000-0000-4000-8000-000000000001', 'parity-social-boards-alice@example.com', 'x', 'parity_brd_alice', '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000002', 'parity-social-boards-bob@example.com', 'x', 'parity_brd_bob', '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000003', 'parity-social-boards-fresh@example.com', 'x', 'parity_brd_fresh', '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000004', 'parity-social-boards-nouser@example.com', 'x', NULL, '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000005', 'parity-social-boards-muted@example.com', 'x', 'parity_brd_muted', '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000006', 'parity-social-boards-banned@example.com', 'x', 'parity_brd_banned', '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000007', 'parity-social-boards-susp@example.com', 'x', 'parity_brd_susp', '2026-10-01T00:00:00Z', true),
  ('b0a4d000-0000-4000-8000-000000000008', 'parity-social-boards-carol@example.com', 'x', 'parity_brd_carol', '2026-10-01T00:00:00Z', true);
-- The admin the 18124 Vapor names in NORVIQ_ADMIN_USER_IDS (shared; left alone if present).
INSERT INTO users (id, email, password_hash, username, created_at, is_verified)
VALUES ('0a11ce00-0000-4000-8000-000000000001', 'parity-social-admin@example.com', 'x', 'parity_admin', '2026-10-01T00:00:00Z', true)
ON CONFLICT (id) DO NOTHING;

INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES
  ('b0a4d000-0000-4000-8000-0000000000a1', 'b0a4d000-0000-4000-8000-000000000001', '2026-10-01T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000a2', 'b0a4d000-0000-4000-8000-000000000002', '2026-10-01T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000a5', 'b0a4d000-0000-4000-8000-000000000005', '2026-10-01T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000a8', 'b0a4d000-0000-4000-8000-000000000008', '2026-10-01T00:00:00Z');

INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_by, created_at) VALUES
  ('b0a4d000-0000-4000-8000-0000000000c5', 'b0a4d000-0000-4000-8000-000000000005', 'mute', 'Spam', '2099-01-01T00:00:00Z', NULL, '2026-10-02T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000c6', 'b0a4d000-0000-4000-8000-000000000006', 'ban', 'Abuse', NULL, NULL, '2026-10-02T00:00:00Z');

INSERT INTO social_settings (id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x, show_return_percent,
  show_streaks, show_xp, leaderboard_opt_in, created_at, suspended_at)
VALUES ('b0a4d000-0000-4000-8000-0000000000d7', 'b0a4d000-0000-4000-8000-000000000007', 'everyone', true, true, true, true, true, true,
  '2026-10-01T00:00:00Z', '2026-10-03T00:00:00Z');

-- Two boards by Alice; posts with fixed timestamps so sorts are stable.
INSERT INTO boards (id, slug, name, description, creator_id, post_count, last_activity_at, created_at) VALUES
  ('b0a4d000-0000-4000-8000-0000000000b1', 'parity-etfs', 'Parity ETFs', 'About ETFs', 'b0a4d000-0000-4000-8000-000000000001', 4, '2026-10-20T10:00:00Z', '2026-10-10T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000b2', 'parity-quiet', 'Parity quiet', '', 'b0a4d000-0000-4000-8000-000000000001', 0, '2026-10-11T00:00:00Z', '2026-10-11T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000b3', 'parity-gone', 'Parity gone', '', 'b0a4d000-0000-4000-8000-000000000001', 1, '2026-10-12T00:00:00Z', '2026-10-12T00:00:00Z');
UPDATE boards SET deleted_at = '2026-10-13T00:00:00Z' WHERE id = 'b0a4d000-0000-4000-8000-0000000000b3';

INSERT INTO board_posts (id, board_id, author_id, kind, title, url, domain, body, tags, score, comment_count, participant_count,
  view_count, last_activity_at, created_at) VALUES
  ('b0a4d000-0000-4000-8000-0000000000e1', 'b0a4d000-0000-4000-8000-0000000000b1', 'b0a4d000-0000-4000-8000-000000000001', 'text',
   'Older post', NULL, NULL, 'Body one', '{vanguard}', 2, 2, 2, 3, '2026-10-20T10:00:00Z', '2026-10-15T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000e2', 'b0a4d000-0000-4000-8000-0000000000b1', 'b0a4d000-0000-4000-8000-000000000001', 'ask',
   'Ask: which ETF?', NULL, NULL, NULL, '{}', 0, 0, 1, 0, '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000e3', 'b0a4d000-0000-4000-8000-0000000000b1', 'b0a4d000-0000-4000-8000-000000000002', 'link',
   'Bob link', 'https://www.example.com/x', 'example.com', NULL, '{dca,etf}', 1, 0, 1, 1, '2026-10-17T00:00:00Z', '2026-10-17T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000e4', 'b0a4d000-0000-4000-8000-0000000000b1', 'b0a4d000-0000-4000-8000-000000000008', 'show',
   'Carol show', NULL, NULL, 'Mine', '{}', 0, 0, 1, 0, '2026-10-18T00:00:00Z', '2026-10-18T00:00:00Z'),
  ('b0a4d000-0000-4000-8000-0000000000e5', 'b0a4d000-0000-4000-8000-0000000000b3', 'b0a4d000-0000-4000-8000-000000000001', 'text',
   'On a gone board', NULL, NULL, NULL, '{}', 0, 0, 1, 0, '2026-10-12T00:00:00Z', '2026-10-12T00:00:00Z');

INSERT INTO board_comments (id, post_id, parent_id, author_id, body, depth, created_at, deleted_at) VALUES
  ('b0a4d000-0000-4000-8000-0000000000f1', 'b0a4d000-0000-4000-8000-0000000000e1', NULL, 'b0a4d000-0000-4000-8000-000000000002', 'Bob says hi', 0, '2026-10-19T00:00:00Z', NULL),
  ('b0a4d000-0000-4000-8000-0000000000f2', 'b0a4d000-0000-4000-8000-0000000000e1', 'b0a4d000-0000-4000-8000-0000000000f1', 'b0a4d000-0000-4000-8000-000000000001', 'Deleted reply', 1, '2026-10-20T10:00:00Z', '2026-10-21T00:00:00Z');

INSERT INTO board_votes (id, post_id, user_id, created_at) VALUES
  ('b0a4d000-0000-4000-8000-000000000101', 'b0a4d000-0000-4000-8000-0000000000e1', 'b0a4d000-0000-4000-8000-000000000002', '2026-10-19T00:00:00Z');

-- Alice saw e1 at 1 comment; bob saw it at 2.
INSERT INTO board_post_views (id, post_id, user_id, last_seen_comment_count, last_seen_at) VALUES
  ('b0a4d000-0000-4000-8000-000000000201', 'b0a4d000-0000-4000-8000-0000000000e1', 'b0a4d000-0000-4000-8000-000000000001', 1, '2026-10-19T00:00:00Z');

INSERT INTO board_notifications (id, recipient_id, kind, actor_id, post_id, comment_id, created_at, read_at) VALUES
  ('b0a4d000-0000-4000-8000-000000000301', 'b0a4d000-0000-4000-8000-000000000001', 'reply', 'b0a4d000-0000-4000-8000-000000000002', 'b0a4d000-0000-4000-8000-0000000000e1', 'b0a4d000-0000-4000-8000-0000000000f1', '2026-10-19T00:00:00Z', NULL),
  ('b0a4d000-0000-4000-8000-000000000302', 'b0a4d000-0000-4000-8000-000000000001', 'upvote', 'b0a4d000-0000-4000-8000-000000000002', 'b0a4d000-0000-4000-8000-0000000000e1', NULL, '2026-10-19T00:00:00Z', '2026-10-19T01:00:00Z'),
  ('b0a4d000-0000-4000-8000-000000000303', 'b0a4d000-0000-4000-8000-000000000001', 'weird', NULL, 'b0a4d000-0000-4000-8000-0000000000e1', NULL, '2026-10-18T00:00:00Z', NULL),
  ('b0a4d000-0000-4000-8000-000000000304', 'b0a4d000-0000-4000-8000-000000000001', 'reply', 'b0a4d000-0000-4000-8000-000000000008', 'b0a4d000-0000-4000-8000-0000000000e5', NULL, '2026-10-17T00:00:00Z', NULL);

INSERT INTO board_notification_settings (id, user_id, reply_push, upvote_push, updated_at) VALUES
  ('b0a4d000-0000-4000-8000-000000000401', 'b0a4d000-0000-4000-8000-000000000002', false, true, '2026-10-01T00:00:00Z');

COMMIT;
