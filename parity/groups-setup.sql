-- Parity fixtures for the groups and digest slice (parity/groups*.jsonl).
-- Idempotent: every run deletes and recreates the rows it owns (ids
-- 9a0e0000-...), plus anything those users created during a previous run.
--   docker exec -i norviq-v2-schema-pg psql -U pg -d norviq < parity/groups-setup.sql
BEGIN;

CREATE TEMP TABLE parity_groups_users ON COMMIT DROP AS
  SELECT id FROM users WHERE id::text LIKE '9a0e0000-%' OR email LIKE 'parity-groups-%@example.com';
CREATE TEMP TABLE parity_groups_groups ON COMMIT DROP AS
  SELECT id FROM groups WHERE id::text LIKE '9a0e0000-%' OR owner_id IN (SELECT id FROM parity_groups_users);

DELETE FROM group_channel_reads WHERE user_id IN (SELECT id FROM parity_groups_users)
  OR channel_id IN (SELECT id FROM group_channels WHERE group_id IN (SELECT id FROM parity_groups_groups));
DELETE FROM group_messages WHERE group_id IN (SELECT id FROM parity_groups_groups) OR author_id IN (SELECT id FROM parity_groups_users);
DELETE FROM group_invites WHERE group_id IN (SELECT id FROM parity_groups_groups) OR invitee_id IN (SELECT id FROM parity_groups_users);
DELETE FROM group_bans WHERE group_id IN (SELECT id FROM parity_groups_groups) OR user_id IN (SELECT id FROM parity_groups_users);
DELETE FROM group_members WHERE group_id IN (SELECT id FROM parity_groups_groups) OR user_id IN (SELECT id FROM parity_groups_users);
DELETE FROM group_channels WHERE group_id IN (SELECT id FROM parity_groups_groups);
DELETE FROM groups WHERE id IN (SELECT id FROM parity_groups_groups);
DELETE FROM social_reports WHERE reporter_id IN (SELECT id FROM parity_groups_users);
DELETE FROM social_friendships WHERE user_id IN (SELECT id FROM parity_groups_users) OR friend_id IN (SELECT id FROM parity_groups_users);
DELETE FROM social_blocks WHERE blocker_id IN (SELECT id FROM parity_groups_users) OR blocked_id IN (SELECT id FROM parity_groups_users);
DELETE FROM community_sanctions WHERE user_id IN (SELECT id FROM parity_groups_users);
DELETE FROM community_guidelines_acceptances WHERE user_id IN (SELECT id FROM parity_groups_users);
DELETE FROM weekly_digest_deliveries WHERE digest_id IN (SELECT id FROM weekly_digests WHERE user_id IN (SELECT id FROM parity_groups_users));
DELETE FROM weekly_digests WHERE user_id IN (SELECT id FROM parity_groups_users);
DELETE FROM digest_preferences WHERE user_id IN (SELECT id FROM parity_groups_users);
DELETE FROM users WHERE id IN (SELECT id FROM parity_groups_users);

INSERT INTO users (id, email, password_hash, username, created_at, is_verified) VALUES
  ('9a0e0000-0000-4000-8000-000000000001', 'parity-groups-ana@example.com', 'x', 'parity_grp_ana', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000002', 'parity-groups-bo@example.com', 'x', 'parity_grp_bo', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000003', 'parity-groups-cy@example.com', 'x', 'parity_grp_cy', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000004', 'parity-groups-dee@example.com', 'x', 'parity_grp_dee', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000005', 'parity-groups-banned@example.com', 'x', 'parity_grp_banned', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000006', 'parity-groups-eve@example.com', 'x', 'parity_grp_eve', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000007', 'parity-groups-fay@example.com', 'x', 'parity_grp_fay', '2026-10-01T00:00:00Z', true),
  ('9a0e0000-0000-4000-8000-000000000008', 'parity-groups-gus@example.com', 'x', 'parity_grp_gus', '2026-10-01T00:00:00Z', true);

-- dee (04) has not accepted the guidelines.
INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES
  ('9a0e0000-0000-4000-8000-0000000000a1', '9a0e0000-0000-4000-8000-000000000001', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000a2', '9a0e0000-0000-4000-8000-000000000002', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000a3', '9a0e0000-0000-4000-8000-000000000003', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000a5', '9a0e0000-0000-4000-8000-000000000005', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000a6', '9a0e0000-0000-4000-8000-000000000006', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000a7', '9a0e0000-0000-4000-8000-000000000007', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000a8', '9a0e0000-0000-4000-8000-000000000008', '2026-10-01T00:00:00Z');

INSERT INTO community_sanctions (id, user_id, kind, reason, created_at) VALUES
  ('9a0e0000-0000-4000-8000-0000000000b5', '9a0e0000-0000-4000-8000-000000000005', 'ban', 'Parity', '2026-10-01T00:00:00Z');

-- ana and fay are friends; cy blocked gus.
INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES
  ('9a0e0000-0000-4000-8000-0000000000c1', '9a0e0000-0000-4000-8000-000000000001', '9a0e0000-0000-4000-8000-000000000007', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-0000000000c2', '9a0e0000-0000-4000-8000-000000000007', '9a0e0000-0000-4000-8000-000000000001', '2026-10-01T00:00:00Z');
INSERT INTO social_blocks (id, blocker_id, blocked_id, created_at) VALUES
  ('9a0e0000-0000-4000-8000-0000000000d1', '9a0e0000-0000-4000-8000-000000000003', '9a0e0000-0000-4000-8000-000000000008', '2026-10-01T00:00:00Z');

-- g1: private stock group (ana owner, eve admin, bo and gus members).
-- g2: public community group (ana owner). g3: public, owned by gus.
-- g4: deleted.
INSERT INTO groups (id, name, description, icon_emoji, topic, ticker, visibility, owner_id, member_count, last_message_at, created_at, updated_at, deleted_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000101', 'Parity holders', 'AAPL / long term', '🍎', 'stock', 'AAPL', 'private', '9a0e0000-0000-4000-8000-000000000001', 4, '2026-10-20T10:03:00Z', '2026-10-02T00:00:00Z', NULL, NULL),
  ('9a0e0000-0000-4000-8000-000000000102', 'Parity open club', '', NULL, 'community', NULL, 'public', '9a0e0000-0000-4000-8000-000000000001', 1, NULL, '2026-10-03T00:00:00Z', NULL, NULL),
  ('9a0e0000-0000-4000-8000-000000000103', 'Parity nvda bulls', 'Chips', '🚀', 'stock', 'NVDA', 'public', '9a0e0000-0000-4000-8000-000000000008', 1, NULL, '2026-10-04T00:00:00Z', NULL, NULL),
  ('9a0e0000-0000-4000-8000-000000000104', 'Parity gone', '', NULL, 'custom', NULL, 'public', '9a0e0000-0000-4000-8000-000000000001', 1, NULL, '2026-10-05T00:00:00Z', NULL, '2026-10-06T00:00:00Z');

INSERT INTO group_members (id, group_id, user_id, role, push_enabled, joined_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000111', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', 'owner', true, '2026-10-02T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000112', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000006', 'admin', true, '2026-10-02T01:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000113', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000002', 'member', true, '2026-10-02T02:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000114', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000008', 'member', false, '2026-10-02T03:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000121', '9a0e0000-0000-4000-8000-000000000102', '9a0e0000-0000-4000-8000-000000000001', 'owner', true, '2026-10-03T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000131', '9a0e0000-0000-4000-8000-000000000103', '9a0e0000-0000-4000-8000-000000000008', 'owner', true, '2026-10-04T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000141', '9a0e0000-0000-4000-8000-000000000104', '9a0e0000-0000-4000-8000-000000000001', 'owner', true, '2026-10-05T00:00:00Z');

INSERT INTO group_channels (id, group_id, name, kind, topic, position, last_seq, last_message_at, created_at, deleted_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000201', '9a0e0000-0000-4000-8000-000000000101', 'general', 'text', NULL, 0, 4, '2026-10-20T10:03:00Z', '2026-10-02T00:00:00Z', NULL),
  ('9a0e0000-0000-4000-8000-000000000202', '9a0e0000-0000-4000-8000-000000000101', 'earnings', 'text', 'Q3 / Q4', 1, 0, NULL, '2026-10-02T00:01:00Z', NULL),
  ('9a0e0000-0000-4000-8000-000000000203', '9a0e0000-0000-4000-8000-000000000102', 'general', 'text', NULL, 0, 0, NULL, '2026-10-03T00:00:00Z', NULL),
  ('9a0e0000-0000-4000-8000-000000000204', '9a0e0000-0000-4000-8000-000000000103', 'general', 'text', NULL, 0, 0, NULL, '2026-10-04T00:00:00Z', NULL),
  ('9a0e0000-0000-4000-8000-000000000205', '9a0e0000-0000-4000-8000-000000000101', 'old', 'text', NULL, 2, 0, NULL, '2026-10-02T00:02:00Z', '2026-10-03T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000206', '9a0e0000-0000-4000-8000-000000000104', 'general', 'text', NULL, 0, 0, NULL, '2026-10-05T00:00:00Z', NULL);

INSERT INTO group_messages (id, channel_id, group_id, author_id, seq, client_message_id, body, created_at, deleted_at, deleted_by) VALUES
  ('9a0e0000-0000-4000-8000-000000000301', '9a0e0000-0000-4000-8000-000000000201', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', 1, 'p-1', 'hello world', '2026-10-20T10:00:00Z', NULL, NULL),
  ('9a0e0000-0000-4000-8000-000000000302', '9a0e0000-0000-4000-8000-000000000201', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000002', 2, 'p-2', 'a/b <c> & ''d'' "e" — ✓', '2026-10-20T10:01:00Z', NULL, NULL),
  ('9a0e0000-0000-4000-8000-000000000303', '9a0e0000-0000-4000-8000-000000000201', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000002', 3, 'p-3', 'oops', '2026-10-20T10:02:00Z', '2026-10-20T10:02:30Z', '9a0e0000-0000-4000-8000-000000000002'),
  ('9a0e0000-0000-4000-8000-000000000304', '9a0e0000-0000-4000-8000-000000000201', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000008', 4, 'p-4', 'gus here', '2026-10-20T10:03:00Z', NULL, NULL);

INSERT INTO group_channel_reads (id, channel_id, user_id, last_read_seq, updated_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000501', '9a0e0000-0000-4000-8000-000000000201', '9a0e0000-0000-4000-8000-000000000001', 1, '2026-10-20T10:00:00Z');

INSERT INTO group_invites (id, group_id, created_by, code, invitee_id, expires_at, max_uses, uses, revoked_at, accepted_at, created_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000401', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', 'PARITYCODE1', NULL, '2030-01-01T00:00:00Z', 10, 0, NULL, NULL, '2026-10-10T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000402', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', 'PARITYEXP1', NULL, '2020-01-01T00:00:00Z', NULL, 0, NULL, NULL, '2026-10-10T00:00:01Z'),
  ('9a0e0000-0000-4000-8000-000000000403', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', NULL, '9a0e0000-0000-4000-8000-000000000007', NULL, NULL, 0, NULL, NULL, '2026-10-10T00:00:02Z'),
  ('9a0e0000-0000-4000-8000-000000000404', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', 'PARITYREV1', NULL, NULL, NULL, 0, '2026-10-11T00:00:00Z', NULL, '2026-10-10T00:00:03Z'),
  ('9a0e0000-0000-4000-8000-000000000405', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000001', 'PARITYFULL', NULL, NULL, 1, 1, NULL, NULL, '2026-10-10T00:00:04Z');

INSERT INTO group_bans (id, group_id, user_id, banned_by, reason, created_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000601', '9a0e0000-0000-4000-8000-000000000101', '9a0e0000-0000-4000-8000-000000000003', '9a0e0000-0000-4000-8000-000000000001', 'spam', '2026-10-12T00:00:00Z');

-- Digest: ana has preferences, a composed digest with a payload and one
-- without; bo has none.
INSERT INTO digest_preferences (id, user_id, email_enabled, push_enabled, telegram_enabled, sentiment_alerts_enabled, weekday, local_hour, timezone, country, unsubscribe_token, created_at, updated_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000701', '9a0e0000-0000-4000-8000-000000000001', true, false, false, false, 7, 18, 'Europe/Lisbon', 'PT', 'parity-groups-unsub-ana', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z'),
  ('9a0e0000-0000-4000-8000-000000000706', '9a0e0000-0000-4000-8000-000000000006', true, false, false, false, 7, 18, 'UTC', NULL, 'parity-groups-unsub-eve', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z');

INSERT INTO weekly_digests (id, user_id, period_start, period_end, status, payload, narrative, created_at, updated_at) VALUES
  ('9a0e0000-0000-4000-8000-000000000801', '9a0e0000-0000-4000-8000-000000000001', '2026-10-19', '2026-10-25', 'sent',
   '{"schemaVersion":1,"periodStart":"2026-10-19","periodEnd":"2026-10-25","holdings":[{"currency":"USD","startValue":2000,"endValue":2400,"change":400,"changePercent":20},{"currency":"EUR","startValue":300,"endValue":294,"change":-6,"changePercent":-2}],"pricedPositions":3,"totalPositions":4,"movers":[{"symbol":"NVDA","currency":"USD","shares":5,"startPrice":200,"endPrice":260,"contribution":300,"changePercent":30},{"symbol":"SAP","currency":"EUR","shares":2,"startPrice":150,"endPrice":147,"contribution":-6,"changePercent":-2,"why":"Guidance cut"}],"budget":{"monthStart":"2026-10-01","currency":"EUR","lines":[{"title":"Travel","target":200,"actual":50,"drift":-150,"driftPercent":-75}]},"inflationAsOfSeen":"2026-09","earnings":[{"symbol":"AAPL","date":"2026-10-28","hour":"amc"}]}',
   NULL, '2026-10-25T18:05:00Z', '2026-10-25T18:05:10Z'),
  ('9a0e0000-0000-4000-8000-000000000802', '9a0e0000-0000-4000-8000-000000000001', '2026-10-12', '2026-10-18', 'failed', NULL, NULL, '2026-10-18T18:05:00Z', '2026-10-18T18:05:10Z'),
  ('9a0e0000-0000-4000-8000-000000000803', '9a0e0000-0000-4000-8000-000000000001', '2026-10-05', '2026-10-11', 'sent',
   '{"schemaVersion":1,"periodStart":"2026-10-05","periodEnd":"2026-10-11","holdings":[],"pricedPositions":0,"totalPositions":0,"movers":[],"earnings":[]}',
   'A quiet week.', '2026-10-11T18:05:00Z', '2026-10-11T18:05:10Z');

COMMIT;
