-- Articles parity seed. Idempotent: re-run before every parity pass.
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/articles-setup.sql
-- Ids start a2710000-; the admin is the flagged Vapor's NORVIQ_ADMIN_USER_IDS entry.

BEGIN;

DELETE FROM social_reports WHERE reporter_id IN (SELECT id FROM users WHERE email LIKE 'parity-social-articles-%');
DELETE FROM articles WHERE author_id IN (SELECT id FROM users WHERE email LIKE 'parity-social-articles-%')
   OR author_id = '0a11ce00-0000-4000-8000-000000000001';
DELETE FROM article_images WHERE owner_id IN (SELECT id FROM users WHERE email LIKE 'parity-social-articles-%')
   OR owner_id = '0a11ce00-0000-4000-8000-000000000001';
DELETE FROM community_sanctions WHERE user_id IN (SELECT id FROM users WHERE email LIKE 'parity-social-articles-%');
DELETE FROM community_guidelines_acceptances WHERE user_id IN (SELECT id FROM users WHERE email LIKE 'parity-social-articles-%');
DELETE FROM personal_access_tokens WHERE id::text LIKE 'a2710000-%';
DELETE FROM users WHERE email LIKE 'parity-social-articles-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, username) VALUES
  ('a2710000-0000-4000-8000-0000000000a1', 'parity-social-articles-author@example.com', 'x', now(), true, 'parity_art_author'),
  ('a2710000-0000-4000-8000-0000000000b1', 'parity-social-articles-reader@example.com', 'x', now(), true, 'parity_art_reader'),
  ('a2710000-0000-4000-8000-0000000000c1', 'parity-social-articles-fresh@example.com', 'x', now(), true, 'parity_art_fresh'),
  ('a2710000-0000-4000-8000-0000000000d1', 'parity-social-articles-muted@example.com', 'x', now(), true, 'parity_art_muted'),
  ('a2710000-0000-4000-8000-0000000000f1', 'parity-social-articles-capped@example.com', 'x', now(), true, 'parity_art_capped');
UPDATE users SET avatar_url = 'https://example.com/a.png' WHERE id = 'a2710000-0000-4000-8000-0000000000a1';

INSERT INTO users (id, email, password_hash, created_at, is_verified, username)
VALUES ('0a11ce00-0000-4000-8000-000000000001', 'parity-admin@example.com', 'x', now(), true, 'parity_admin')
ON CONFLICT (id) DO NOTHING;
UPDATE users SET username = 'parity_admin' WHERE id = '0a11ce00-0000-4000-8000-000000000001' AND username IS NULL;
INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at)
SELECT 'a2710000-0000-4000-8000-0000000009a0', '0a11ce00-0000-4000-8000-000000000001', now()
WHERE NOT EXISTS (SELECT 1 FROM community_guidelines_acceptances WHERE user_id = '0a11ce00-0000-4000-8000-000000000001');

INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES
  ('a2710000-0000-4000-8000-0000000009a1', 'a2710000-0000-4000-8000-0000000000a1', now()),
  ('a2710000-0000-4000-8000-0000000009b1', 'a2710000-0000-4000-8000-0000000000b1', now()),
  ('a2710000-0000-4000-8000-0000000009d1', 'a2710000-0000-4000-8000-0000000000d1', now()),
  ('a2710000-0000-4000-8000-0000000009f1', 'a2710000-0000-4000-8000-0000000000f1', now());
INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_at) VALUES
  ('a2710000-0000-4000-8000-0000000008d1', 'a2710000-0000-4000-8000-0000000000d1', 'mute', 'parity', '2099-01-01T00:00:00Z', now());

-- PATs: the web's credential (listed in ARTICLES_VIEWER_CREDENTIAL_IDS on the
-- credential pass), another market:read one, and one without market:read.
INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at) VALUES
  ('a2710000-0000-4000-8000-0000000000e1', '0a11ce00-0000-4000-8000-000000000001', 'parity-web',
   '7e6fdc0c00488158094d5822c5a31cf2dbc0f66a9e7999492bad82257c57bfe2', '{market:read}', '2099-01-01T00:00:00Z', now()),
  ('a2710000-0000-4000-8000-0000000000e2', 'a2710000-0000-4000-8000-0000000000b1', 'parity-other',
   '9a7c697c359947479af875faa836fa32c405378ec7d2412920aaa9d0970a39e4', '{market:read}', '2099-01-01T00:00:00Z', now()),
  ('a2710000-0000-4000-8000-0000000000e3', 'a2710000-0000-4000-8000-0000000000b1', 'parity-noscope',
   '40ae05fbf64285bcd316ad03024afc24713e939eec9c7fa36321af863e39ba41', '{settings:read}', '2099-01-01T00:00:00Z', now());

INSERT INTO article_images (id, owner_id, content_type, bytes, width, height, created_at) VALUES
  ('a2710000-0000-4000-8000-000000000201', 'a2710000-0000-4000-8000-0000000000a1', 'image/png',
   '\x89504e470d0a1a0a0000000d494844520000000a0000000a080600000000000000', 10, 10, now()),
  ('a2710000-0000-4000-8000-000000000202', 'a2710000-0000-4000-8000-0000000000a1', 'image/png',
   '\x89504e470d0a1a0a0000000d494844520000000a0000000a080600000000000000', 10, 10, now()),
  ('a2710000-0000-4000-8000-000000000203', 'a2710000-0000-4000-8000-0000000000a1', 'image/png',
   '\x89504e470d0a1a0a0000000d494844520000000a0000000a080600000000000000', 10, 10, now());
-- The capped user has used today's ten uploads.
INSERT INTO article_images (id, owner_id, content_type, bytes, width, height, created_at)
SELECT ('a2710000-0000-4000-8000-0000000003' || lpad(n::text, 2, '0'))::uuid, 'a2710000-0000-4000-8000-0000000000f1', 'image/png',
   '\x89504e470d0a1a0a0000000d494844520000000a0000000a080600000000000000', 10, 10, now()
FROM generate_series(1, 10) AS n;

INSERT INTO articles (id, code, author_id, slug, title, body_markdown, bullet_points, tickers, disclosure,
  cover_image_id, status, source, view_count, upvote_count, word_count, published_at, edited_at, created_at, updated_at) VALUES
  ('a2710000-0000-4000-8000-000000000101', 'pzaaaaaa', 'a2710000-0000-4000-8000-0000000000a1', 'first-parity-article', 'First parity article',
   repeat('Revenue visibility is unusually long. ', 12), '{"First LNG from Train 1 is targeted for 1H 2027."}', '{NEXT}', 'I hold a position in $NEXT.',
   'a2710000-0000-4000-8000-000000000201', 'published', 'web', 3, 1, 60, '2026-10-20T10:00:00.123Z', NULL, now(), now()),
  ('a2710000-0000-4000-8000-000000000102', 'pzbbbbbb', 'a2710000-0000-4000-8000-0000000000b1', 'second-parity-article', 'Second parity article',
   repeat('Revenue visibility is unusually long. ', 12), '{"Point one is long enough.","Point two is long enough."}', '{NVDA,NEXT}', 'No position.',
   NULL, 'published', 'discord', 0, 0, 60, '2026-10-21T10:00:00.456Z', NULL, now(), now()),
  ('a2710000-0000-4000-8000-000000000103', 'pzcccccc', 'a2710000-0000-4000-8000-0000000000a1', 'third-parity-article', 'Third parity article',
   repeat('Revenue visibility is unusually long. ', 12), '{"Point one is long enough."}', '{NVDA}', 'No position.',
   NULL, 'published', 'ios', 0, 0, 60, '2026-10-22T10:00:00.789Z', '2026-10-22T11:00:00Z', now(), now()),
  ('a2710000-0000-4000-8000-000000000104', 'pzhhhhhh', 'a2710000-0000-4000-8000-0000000000a1', 'hidden-parity-article', 'Hidden parity article',
   repeat('Revenue visibility is unusually long. ', 12), '{"Point one is long enough."}', '{NEXT}', 'No position.',
   'a2710000-0000-4000-8000-000000000203', 'hidden', 'web', 0, 0, 60, '2026-10-23T10:00:00Z', NULL, now(), now()),
  ('a2710000-0000-4000-8000-000000000105', 'pzdddddd', 'a2710000-0000-4000-8000-0000000000a1', 'deleted-parity-article', 'Deleted parity article',
   repeat('Revenue visibility is unusually long. ', 12), '{"Point one is long enough."}', '{NEXT}', 'No position.',
   NULL, 'deleted', 'web', 0, 0, 60, '2026-10-24T10:00:00Z', NULL, now(), now()),
  ('a2710000-0000-4000-8000-000000000106', 'pzvvvvvv', 'a2710000-0000-4000-8000-0000000000a1', 'vote-parity-article', 'Vote parity article',
   repeat('Revenue visibility is unusually long. ', 12), '{"Point one is long enough."}', '{NEXT}', 'No position.',
   NULL, 'published', 'web', 0, 0, 60, '2026-10-25T10:00:00Z', NULL, now(), now());
-- The capped user published three today.
INSERT INTO articles (id, code, author_id, slug, title, body_markdown, bullet_points, tickers, disclosure,
  cover_image_id, status, source, view_count, upvote_count, word_count, published_at, edited_at, created_at, updated_at)
SELECT ('a2710000-0000-4000-8000-0000000001f' || n)::uuid, 'pzcap00' || n, 'a2710000-0000-4000-8000-0000000000f1', 'capped', 'Capped article',
   'x', '{"Point one is long enough."}', '{NEXT}', 'No position.', NULL, 'deleted', 'web', 0, 0, 1, now() - interval '1 hour', NULL, now(), now()
FROM generate_series(1, 3) AS n;

COMMIT;
