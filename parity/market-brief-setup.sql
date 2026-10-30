-- Parity fixtures for the market brief (parity/market-brief*.jsonl).
-- Idempotent: every run deletes and recreates the rows it owns (users
-- ae00b000-..., briefs dated 2026-09-*).
--   docker exec -i norviq-v2-schema-pg psql -U pg -d norviq < parity/market-brief-setup.sql
-- Payloads are what Vapor's repository writes (JSONEncoder .sortedKeys).
-- One item has an unknown kind ("poem"): the shared decoder drops it.
BEGIN;

DELETE FROM market_brief_notification_preferences WHERE user_id::text LIKE 'ae00b000-%';
DELETE FROM market_briefs WHERE trading_date LIKE '2026-09-%';
DELETE FROM users WHERE id::text LIKE 'ae00b000-%';

INSERT INTO users (id, email, password_hash, username, created_at, is_verified) VALUES
  ('ae00b000-0000-4000-8000-000000000001', 'parity-news-brief-alice@example.com', 'x', 'parity_brief_alice', '2026-10-01T00:00:00Z', true),
  ('ae00b000-0000-4000-8000-000000000002', 'parity-news-brief-bob@example.com', 'x', 'parity_brief_bob', '2026-10-01T00:00:00Z', true),
  ('ae00b000-0000-4000-8000-000000000003', 'parity-news-brief-carol@example.com', 'x', 'parity_brief_carol', '2026-10-01T00:00:00Z', true);

-- bob opted in (pt-PT); alice and carol have no row. carol is the PUT target.
INSERT INTO market_brief_notification_preferences (id, user_id, enabled, language, created_at, updated_at) VALUES
  ('ae00b000-0000-4000-8000-0000000000b2', 'ae00b000-0000-4000-8000-000000000002', true, 'pt-PT', now(), now());

-- Friday 2026-09-25 evening, Monday 2026-09-28 morning and evening, en + pt-PT;
-- 2026-09-24 morning in en only.
INSERT INTO market_briefs (id, trading_date, slot, language, payload, model, degraded, generated_at, created_at) VALUES
  ('ae00b000-0000-4000-8000-000000000a01', '2026-09-25', 'evening', 'en',
   '{"degraded":true,"enabled":true,"generatedAt":"2026-09-25T21:30:05Z","greeting":"Here is the close.","groups":[{"id":"us_close","rows":[{"changePercent":"0.41%","direction":"up","flag":"🇺🇸","level":"6,612","name":"S&P 500","symbol":"^GSPC"}],"title":"Wall Street closed higher","tone":"up"}],"items":[{"kind":"story","text":"Stocks rose.","tickers":["SPY"]},{"kind":"poem","text":"dropped","tickers":[]}],"language":"en","slot":"evening","tradingDate":"2026-09-25"}',
   'fallback-chain', true, '2026-09-25T21:30:05Z', now()),
  ('ae00b000-0000-4000-8000-000000000a02', '2026-09-25', 'evening', 'pt-PT',
   '{"degraded":true,"enabled":true,"generatedAt":"2026-09-25T21:30:05Z","groups":[],"items":[{"kind":"story","text":"As ações subiram.","tickers":[]}],"language":"pt-PT","slot":"evening","tradingDate":"2026-09-25"}',
   'fallback-chain', true, '2026-09-25T21:30:05Z', now()),
  ('ae00b000-0000-4000-8000-000000000a03', '2026-09-28', 'morning', 'en',
   '{"degraded":false,"enabled":true,"generatedAt":"2026-09-28T07:15:02Z","greeting":"Good morning,","groups":[{"id":"eu_open","rows":[{"changePercent":"0.77%","direction":"down","flag":"🇩🇪","level":"25,032","name":"DAX","symbol":"^GDAXI"}],"title":"European open lower","tone":"down"}],"items":[{"kind":"highlight","sourceUrl":"https:\/\/www.reuters.com\/markets","text":"Risk-off in Europe.","tickers":[]},{"kind":"earnings","text":"$PEP reports.","tickers":["PEP"]}],"language":"en","slot":"morning","tradingDate":"2026-09-28"}',
   'anthropic/claude-haiku-4.5:online', false, '2026-09-28T07:15:02Z', now()),
  ('ae00b000-0000-4000-8000-000000000a04', '2026-09-28', 'morning', 'pt-PT',
   '{"degraded":false,"enabled":true,"generatedAt":"2026-09-28T07:15:02Z","greeting":"Bom dia,","groups":[],"items":[{"kind":"highlight","text":"Aversão ao risco na Europa.","tickers":[]}],"language":"pt-PT","slot":"morning","tradingDate":"2026-09-28"}',
   'anthropic/claude-haiku-4.5:online', false, '2026-09-28T07:15:02Z', now()),
  ('ae00b000-0000-4000-8000-000000000a05', '2026-09-28', 'evening', 'en',
   '{"degraded":false,"enabled":true,"generatedAt":"2026-09-28T21:30:01Z","greeting":"The close.","groups":[],"items":[{"kind":"story","text":"Late rally.","tickers":[]}],"language":"en","slot":"evening","tradingDate":"2026-09-28"}',
   'anthropic/claude-haiku-4.5:online', false, '2026-09-28T21:30:01Z', now()),
  ('ae00b000-0000-4000-8000-000000000a06', '2026-09-28', 'evening', 'pt-PT',
   '{"degraded":false,"enabled":true,"generatedAt":"2026-09-28T21:30:01Z","groups":[],"items":[{"kind":"story","text":"Recuperação tardia.","tickers":[]}],"language":"pt-PT","slot":"evening","tradingDate":"2026-09-28"}',
   'anthropic/claude-haiku-4.5:online', false, '2026-09-28T21:30:01Z', now()),
  ('ae00b000-0000-4000-8000-000000000a07', '2026-09-24', 'morning', 'en',
   '{"degraded":false,"enabled":true,"generatedAt":"2026-09-24T07:15:00Z","groups":[],"items":[],"language":"en","slot":"morning","tradingDate":"2026-09-24"}',
   'm', false, '2026-09-24T07:15:00Z', now());

COMMIT;
