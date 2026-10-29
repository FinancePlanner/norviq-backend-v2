-- Fixtures for parity/social.jsonl (social graph + gamification). Idempotent:
-- run before every parity pass, against each database the two backends use
-- (scratch databases only, never production). See the header of
-- parity/social.jsonl for the two-database setup the write cases need.
--
-- Users are 5050c1a1-0000-4000-8000-0000000000NN, emails
-- parity-social-social-NN@example.com, usernames pss_<name>. Deleting the
-- users cascades to every social_* and gamification_* row they own.
--
--   01 alice   the viewer in most cases; owns invite code pssInvite01
--   02 bob     alice's friend (check-ins, XP, achievements, showcase)
--   03 carol   pending request carol -> alice  (accept)
--   04 dave    pending request dave -> alice   (decline)
--   05 erin    pending request alice -> erin   (cancel)
--   06 frank   stranger: search hit, request target, block/unblock target
--   07 gina    search visibility "nobody"
--   08 hank    blocked by alice
--   09 ivan    owns invite code pssInvite09 (alice redeems it)
--   10 jules   suspended by a moderator
--   11 kim     pending request kim -> alice, accepted by a reverse request

BEGIN;

DELETE FROM users WHERE id::text LIKE '5050c1a1-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, email_verified_at, password_set_at, username, had_trial)
SELECT ('5050c1a1-0000-4000-8000-0000000000' || n)::uuid, 'parity-social-social-' || n || '@example.com',
       '$2b$11$tC9a6D7mNxanY5Tn1lRvp.TMVLtgAZfe3yNJtosmgtB3C1r21p9Ru', '2026-09-01T10:00:00Z', true,
       '2026-09-01T10:00:00Z', '2026-09-01T10:00:00Z', 'pss_' || name, false
FROM (VALUES ('01', 'alice'), ('02', 'bob'), ('03', 'carol'), ('04', 'dave'), ('05', 'erin'), ('06', 'frank'),
             ('07', 'gina'), ('08', 'hank'), ('09', 'ivan'), ('10', 'jules'), ('11', 'kim')) AS u(n, name);

-- Privacy: explicit rows for alice, bob (shares return %), gina (unsearchable)
-- and jules (suspended). Everyone else gets defaults on first touch.
INSERT INTO social_settings (id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x,
    discoverable_by_facebook, show_return_percent, show_streaks, show_xp, leaderboard_opt_in, show_achievements,
    email_hash, created_at, updated_at, suspended_at) VALUES
  ('5050c1a1-0000-4000-8001-000000000001', '5050c1a1-0000-4000-8000-000000000001', 'everyone', true, true, true, false, true, true, true, true,
    NULL, '2026-09-01T10:00:00Z', '2026-09-01T10:00:00Z', NULL),
  ('5050c1a1-0000-4000-8001-000000000002', '5050c1a1-0000-4000-8000-000000000002', 'everyone', true, true, true, true, true, true, true, true,
    NULL, '2026-09-01T10:00:00Z', '2026-09-01T10:00:00Z', NULL),
  ('5050c1a1-0000-4000-8001-000000000007', '5050c1a1-0000-4000-8000-000000000007', 'nobody', true, true, true, false, true, true, true, true,
    NULL, '2026-09-01T10:00:00Z', '2026-09-01T10:00:00Z', NULL),
  ('5050c1a1-0000-4000-8001-000000000010', '5050c1a1-0000-4000-8000-000000000010', 'everyone', true, true, true, false, true, true, true, true,
    NULL, '2026-09-01T10:00:00Z', '2026-09-01T10:00:00Z', '2026-09-02T10:00:00Z');

-- alice <-> bob are friends.
INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES
  ('5050c1a1-0000-4000-8002-000000000001', '5050c1a1-0000-4000-8000-000000000001', '5050c1a1-0000-4000-8000-000000000002', '2026-09-01T11:00:00Z'),
  ('5050c1a1-0000-4000-8002-000000000002', '5050c1a1-0000-4000-8000-000000000002', '5050c1a1-0000-4000-8000-000000000001', '2026-09-01T11:00:00Z');

INSERT INTO social_friend_requests (id, from_user_id, to_user_id, created_at) VALUES
  ('5050c1a1-0000-4000-8003-000000000003', '5050c1a1-0000-4000-8000-000000000003', '5050c1a1-0000-4000-8000-000000000001', '2026-09-03T10:00:00Z'),
  ('5050c1a1-0000-4000-8003-000000000004', '5050c1a1-0000-4000-8000-000000000004', '5050c1a1-0000-4000-8000-000000000001', '2026-09-04T10:00:00Z'),
  ('5050c1a1-0000-4000-8003-000000000005', '5050c1a1-0000-4000-8000-000000000001', '5050c1a1-0000-4000-8000-000000000005', '2026-09-05T10:00:00Z'),
  ('5050c1a1-0000-4000-8003-000000000011', '5050c1a1-0000-4000-8000-000000000011', '5050c1a1-0000-4000-8000-000000000001', '2026-09-06T10:00:00Z');

INSERT INTO social_blocks (id, blocker_id, blocked_id, created_at) VALUES
  ('5050c1a1-0000-4000-8004-000000000008', '5050c1a1-0000-4000-8000-000000000001', '5050c1a1-0000-4000-8000-000000000008', '2026-09-01T12:00:00Z');

INSERT INTO social_invites (id, user_id, code, created_at) VALUES
  ('5050c1a1-0000-4000-8005-000000000009', '5050c1a1-0000-4000-8000-000000000009', 'pssInvite09', '2026-09-01T12:00:00Z'),
  ('5050c1a1-0000-4000-8005-000000000001', '5050c1a1-0000-4000-8000-000000000001', 'pssInvite01', '2026-09-01T12:00:00Z');

-- bob: checked in today and the three days before (UTC), plus XP for each.
INSERT INTO gamification_check_ins (id, user_id, local_date, time_zone, created_at)
SELECT ('5050c1a1-0000-4000-8006-00000000000' || d)::uuid, '5050c1a1-0000-4000-8000-000000000002',
       to_char((now() AT TIME ZONE 'UTC')::date - d, 'YYYY-MM-DD'), 'GMT', '2026-09-01T12:00:00Z'
FROM generate_series(0, 3) AS d;
INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, exclude_from_leaderboard, created_at)
SELECT ('5050c1a1-0000-4000-8007-00000000000' || d)::uuid, '5050c1a1-0000-4000-8000-000000000002', 'check_in', 10,
       'check_in:' || to_char((now() AT TIME ZONE 'UTC')::date - d, 'YYYY-MM-DD'),
       false, date_trunc('day', now()) - make_interval(days => d)
FROM generate_series(0, 3) AS d;
INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, exclude_from_leaderboard, created_at) VALUES
  ('5050c1a1-0000-4000-8007-000000000010', '5050c1a1-0000-4000-8000-000000000002', 'badge_earned', 250, 'parity:badge',
    true, '2026-09-10T12:00:00Z');

-- alice: already checked in today, so the check-in case is the idempotent
-- "already checked in" answer on both backends.
INSERT INTO gamification_check_ins (id, user_id, local_date, time_zone, created_at) VALUES
  ('5050c1a1-0000-4000-8006-000000000101', '5050c1a1-0000-4000-8000-000000000001',
    to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD'), 'GMT', '2026-09-01T12:00:00Z');
INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, exclude_from_leaderboard, created_at) VALUES
  ('5050c1a1-0000-4000-8007-000000000101', '5050c1a1-0000-4000-8000-000000000001', 'check_in', 10,
    'check_in:' || to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD'), false, date_trunc('day', now()));

INSERT INTO gamification_budget_streaks (id, user_id, months, best_months, created_at, updated_at) VALUES
  ('5050c1a1-0000-4000-8008-000000000002', '5050c1a1-0000-4000-8000-000000000002', 3, 5, '2026-09-01T12:00:00Z', NULL);

-- bob's achievements: three live, one performance (hidden unless return %
-- is shared: bob shares it), one legacy badge (never shown).
INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, showcase_rank, created_at) VALUES
  ('5050c1a1-0000-4000-8009-000000000001', '5050c1a1-0000-4000-8000-000000000002', 'craft.thesis', 'bronze', '2026-09-05T10:00:00Z', 'live', NULL, '2026-09-05T10:00:00Z'),
  ('5050c1a1-0000-4000-8009-000000000002', '5050c1a1-0000-4000-8000-000000000002', 'craft.target_hit', 'gold', '2026-09-06T10:00:00Z', 'live', NULL, '2026-09-06T10:00:00Z'),
  ('5050c1a1-0000-4000-8009-000000000003', '5050c1a1-0000-4000-8000-000000000002', 'social.friends', 'silver', '2026-09-07T10:00:00Z', 'live', 1, '2026-09-07T10:00:00Z'),
  ('5050c1a1-0000-4000-8009-000000000004', '5050c1a1-0000-4000-8000-000000000002', 'perf.comeback', '', '2026-09-08T10:00:00Z', 'live', NULL, '2026-09-08T10:00:00Z'),
  ('5050c1a1-0000-4000-8009-000000000005', '5050c1a1-0000-4000-8000-000000000002', 'legacy.investor', 'gold', '2026-09-09T10:00:00Z', 'live', NULL, '2026-09-09T10:00:00Z');

-- An X identity for frank (X import is off on the flagged Vapor, so this only
-- documents the shape) and a Facebook identity for bob.
INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, created_at) VALUES
  ('5050c1a1-0000-4000-800a-000000000002', '5050c1a1-0000-4000-8000-000000000002', 'facebook', 'pss-fb-bob', '2026-09-01T12:00:00Z');

-- Vapor's hourly AchievementSweepJob would otherwise backfill badges (and
-- badge XP) for these brand-new users on its next tick; v2 has no sweep in
-- this slice. Mark everyone as already swept and backfilled.
INSERT INTO achievement_evaluations (id, user_id, needs_backfill, last_evaluated_at, last_swept_at)
SELECT gen_random_uuid(), id, false, now(), now() FROM users WHERE id::text LIKE '5050c1a1-0000-4000-8000-%';

COMMIT;
