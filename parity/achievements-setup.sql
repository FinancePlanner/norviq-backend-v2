-- Fixtures for parity/achievements.jsonl. Resets and re-seeds the slice's
-- rows, so it is safe to re-run before every parity run:
--   psql "postgres://pg:pg@127.0.0.1:55499/norviq" -f parity/achievements-setup.sql
--
-- Users (prefix 5a000000-…):
--   …a1 viewer   friends with a2 and a5, blocks a5
--   …a2 friend   show_return_percent off (performance unlocks hidden)
--   …a3 stranger no friendship
--   …a4 suspended (social_settings.suspended_at set)
--   …a5 blocked friend
--   …a6 friend with show_achievements off
-- Every user gets an achievement_evaluations row stamped far in the future,
-- so neither the listing's evaluation (60 s debounce) nor the sweep job of
-- either backend writes new unlocks between the two requests of a case.
--
-- Timestamps are absolute so feed cursors in the cases stay fixed; the feed
-- window is 90 days, so the feed cases expire after 2027-01-25 (move the
-- 2026-10-27 dates forward and recompute the two cursors then).

BEGIN;

DELETE FROM social_congrats WHERE activity_id IN (SELECT id FROM social_activity WHERE actor_id::text LIKE '5a000000-%')
   OR from_user_id::text LIKE '5a000000-%';
DELETE FROM social_activity WHERE actor_id::text LIKE '5a000000-%';
DELETE FROM user_achievements WHERE user_id::text LIKE '5a000000-%';
DELETE FROM achievement_evaluations WHERE user_id::text LIKE '5a000000-%';
DELETE FROM social_friendships WHERE user_id::text LIKE '5a000000-%' OR friend_id::text LIKE '5a000000-%';
DELETE FROM social_blocks WHERE blocker_id::text LIKE '5a000000-%' OR blocked_id::text LIKE '5a000000-%';
DELETE FROM social_settings WHERE user_id::text LIKE '5a000000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, username) VALUES
  ('5a000000-0000-4000-8000-0000000000a1', 'parity-social-achievements-a1@example.com', 'x', '2026-01-01T00:00:00Z', true, 'parity_ach_viewer'),
  ('5a000000-0000-4000-8000-0000000000a2', 'parity-social-achievements-a2@example.com', 'x', '2026-01-01T00:00:00Z', true, 'parity_ach_friend'),
  ('5a000000-0000-4000-8000-0000000000a3', 'parity-social-achievements-a3@example.com', 'x', '2026-01-01T00:00:00Z', true, 'parity_ach_stranger'),
  ('5a000000-0000-4000-8000-0000000000a4', 'parity-social-achievements-a4@example.com', 'x', '2026-01-01T00:00:00Z', true, 'parity_ach_suspended'),
  ('5a000000-0000-4000-8000-0000000000a5', 'parity-social-achievements-a5@example.com', 'x', '2026-01-01T00:00:00Z', true, 'parity_ach_blocked'),
  ('5a000000-0000-4000-8000-0000000000a6', 'parity-social-achievements-a6@example.com', 'x', '2026-01-01T00:00:00Z', true, NULL)
ON CONFLICT (id) DO NOTHING;

INSERT INTO achievement_evaluations (id, user_id, needs_backfill, last_evaluated_at, last_swept_at)
SELECT ('5a000000-0000-4000-8000-0000000e00' || right(u, 2))::uuid, ('5a000000-0000-4000-8000-0000000000' || right(u, 2))::uuid,
       false, '2099-01-01T00:00:00Z', '2099-01-01T00:00:00Z'
FROM unnest(ARRAY['a1','a2','a3','a4','a5','a6']) AS u;

INSERT INTO social_settings (id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x, show_return_percent,
                             show_streaks, show_xp, leaderboard_opt_in, created_at, suspended_at, show_achievements) VALUES
  ('5a000000-0000-4000-8000-0000000005a2', '5a000000-0000-4000-8000-0000000000a2', 'everyone', true, true, false, true, true, true, '2026-01-01T00:00:00Z', NULL, true),
  ('5a000000-0000-4000-8000-0000000005a4', '5a000000-0000-4000-8000-0000000000a4', 'everyone', true, true, true, true, true, true, '2026-01-01T00:00:00Z', '2026-10-01T00:00:00Z', true),
  ('5a000000-0000-4000-8000-0000000005a6', '5a000000-0000-4000-8000-0000000000a6', 'everyone', true, true, true, true, true, true, '2026-01-01T00:00:00Z', NULL, false);

-- Friendships are stored both ways, from 2026-10-01.
INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES
  ('5a000000-0000-4000-8000-0000000f0012', '5a000000-0000-4000-8000-0000000000a1', '5a000000-0000-4000-8000-0000000000a2', '2026-10-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-0000000f0021', '5a000000-0000-4000-8000-0000000000a2', '5a000000-0000-4000-8000-0000000000a1', '2026-10-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-0000000f0015', '5a000000-0000-4000-8000-0000000000a1', '5a000000-0000-4000-8000-0000000000a5', '2026-10-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-0000000f0051', '5a000000-0000-4000-8000-0000000000a5', '5a000000-0000-4000-8000-0000000000a1', '2026-10-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-0000000f0016', '5a000000-0000-4000-8000-0000000000a1', '5a000000-0000-4000-8000-0000000000a6', '2026-10-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-0000000f0061', '5a000000-0000-4000-8000-0000000000a6', '5a000000-0000-4000-8000-0000000000a1', '2026-10-01T00:00:00Z');

INSERT INTO social_blocks (id, blocker_id, blocked_id, created_at) VALUES
  ('5a000000-0000-4000-8000-0000000b0015', '5a000000-0000-4000-8000-0000000000a1', '5a000000-0000-4000-8000-0000000000a5', '2026-10-26T00:00:00Z');

-- Unlocks. Viewer a1: two unseen (live/sweep), one seen tiered, one
-- backfilled (never unseen), one legacy badge. Friend a2: three plus one
-- performance unlock hidden by show_return_percent. a5/a6 one each.
INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, seen_at, notified_at, showcase_rank, created_at) VALUES
  ('5a000000-0000-4000-8000-00000000a101', '5a000000-0000-4000-8000-0000000000a1', 'start.first_holding', '', '2026-10-20T10:00:00Z', 'live', NULL, NULL, NULL, '2026-10-20T10:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a102', '5a000000-0000-4000-8000-0000000000a1', 'start.first_budget', '', '2026-10-20T11:00:00Z', 'sweep', NULL, NULL, NULL, '2026-10-20T11:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a103', '5a000000-0000-4000-8000-0000000000a1', 'discipline.check_in', 'bronze', '2026-10-10T08:00:00Z', 'live', '2026-10-10T09:00:00Z', NULL, 1, '2026-10-10T08:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a104', '5a000000-0000-4000-8000-0000000000a1', 'start.first_goal', '', '2026-10-01T08:00:00Z', 'backfill', '2026-10-01T08:00:00Z', NULL, NULL, '2026-10-01T08:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a105', '5a000000-0000-4000-8000-0000000000a1', 'legacy.saver', 'gold', '2026-09-01T08:00:00Z', 'legacy', '2026-09-01T08:00:00Z', NULL, NULL, '2026-09-01T08:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a201', '5a000000-0000-4000-8000-0000000000a2', 'start.first_goal', '', '2026-10-27T10:00:00Z', 'live', NULL, NULL, NULL, '2026-10-27T10:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a202', '5a000000-0000-4000-8000-0000000000a2', 'discipline.check_in', 'bronze', '2026-10-27T11:00:00Z', 'live', NULL, NULL, 1, '2026-10-27T11:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a203', '5a000000-0000-4000-8000-0000000000a2', 'perf.comeback', '', '2026-10-27T12:00:00Z', 'live', NULL, NULL, NULL, '2026-10-27T12:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a204', '5a000000-0000-4000-8000-0000000000a2', 'start.first_holding', '', '2026-09-01T00:00:00Z', 'live', NULL, NULL, NULL, '2026-09-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a501', '5a000000-0000-4000-8000-0000000000a5', 'start.first_holding', '', '2026-10-27T12:00:00Z', 'live', NULL, NULL, NULL, '2026-10-27T12:00:00Z'),
  ('5a000000-0000-4000-8000-00000000a601', '5a000000-0000-4000-8000-0000000000a6', 'start.first_holding', '', '2026-10-27T12:00:00Z', 'live', NULL, NULL, NULL, '2026-10-27T12:00:00Z');

-- Activity. a2's first_holding activity predates the friendship (excluded);
-- the perf activity is hidden by settings; a5 is blocked; a6 hides it.
INSERT INTO social_activity (id, actor_id, kind, user_achievement_id, created_at) VALUES
  ('5a000000-0000-4000-8000-0000000ac201', '5a000000-0000-4000-8000-0000000000a2', 'achievement_unlocked', '5a000000-0000-4000-8000-00000000a201', '2026-10-27T10:00:00Z'),
  ('5a000000-0000-4000-8000-0000000ac202', '5a000000-0000-4000-8000-0000000000a2', 'achievement_unlocked', '5a000000-0000-4000-8000-00000000a202', '2026-10-27T11:00:00Z'),
  ('5a000000-0000-4000-8000-0000000ac203', '5a000000-0000-4000-8000-0000000000a2', 'achievement_unlocked', '5a000000-0000-4000-8000-00000000a203', '2026-10-27T12:00:00Z'),
  ('5a000000-0000-4000-8000-0000000ac204', '5a000000-0000-4000-8000-0000000000a2', 'achievement_unlocked', '5a000000-0000-4000-8000-00000000a204', '2026-09-01T00:00:00Z'),
  ('5a000000-0000-4000-8000-0000000ac501', '5a000000-0000-4000-8000-0000000000a5', 'achievement_unlocked', '5a000000-0000-4000-8000-00000000a501', '2026-10-27T12:00:00Z'),
  ('5a000000-0000-4000-8000-0000000ac601', '5a000000-0000-4000-8000-0000000000a6', 'achievement_unlocked', '5a000000-0000-4000-8000-00000000a601', '2026-10-27T12:00:00Z');

-- One existing congrats from a5 (blocked by a1, still counted? see the case).
INSERT INTO social_congrats (id, activity_id, from_user_id, created_at) VALUES
  ('5a000000-0000-4000-8000-0000000cc001', '5a000000-0000-4000-8000-0000000ac202', '5a000000-0000-4000-8000-0000000000a5', '2026-10-27T11:30:00Z');

COMMIT;
