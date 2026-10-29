-- Pilots, their disclosures and book versions (PilotController,
-- PilotIngestionService, CusipSymbolResolver).

-- name: ListActivePilots :many
SELECT * FROM pilots WHERE active = true ORDER BY display_name;

-- name: ActivePilotBySlug :one
SELECT * FROM pilots WHERE slug = $1 AND active = true LIMIT 1;

-- name: PilotByID :one
SELECT * FROM pilots WHERE id = $1;

-- name: LockPilot :exec
-- Serializes ingestion per pilot so two pods cannot write the same version.
SELECT id FROM pilots WHERE id = $1 FOR UPDATE;

-- name: StampPilotIngested :exec
UPDATE pilots SET last_ingested_at = @last_ingested_at, updated_at = @updated_at WHERE id = @id;

-- name: LatestBookVersion :one
SELECT * FROM pilot_book_versions WHERE pilot_id = $1 ORDER BY version DESC LIMIT 1;

-- name: BookVersion :one
SELECT * FROM pilot_book_versions WHERE pilot_id = $1 AND version = $2 LIMIT 1;

-- name: InsertBookVersion :exec
INSERT INTO pilot_book_versions (id, pilot_id, version, computed_at, weights, skipped_puts)
VALUES (@id, @pilot_id, @version, @computed_at, @weights, @skipped_puts);

-- name: RecentDisclosures :many
SELECT * FROM pilot_disclosures
WHERE pilot_id = $1
ORDER BY transaction_date DESC, discovered_at DESC
LIMIT 25;

-- name: DisclosuresInDiscoveryOrder :many
SELECT * FROM pilot_disclosures WHERE pilot_id = $1 ORDER BY discovered_at, id;

-- name: LatestDisclosureForSymbol :one
SELECT * FROM pilot_disclosures
WHERE pilot_id = $1 AND symbol = $2
ORDER BY transaction_date DESC, period DESC
LIMIT 1;

-- name: InsertDisclosure :many
INSERT INTO pilot_disclosures (pilot_id, source_key, symbol, side, instrument, transaction_date, disclosure_date,
                               amount_min, amount_max, shares, market_value, period, discovered_at)
VALUES (@pilot_id, @source_key, @symbol, @side, @instrument, @transaction_date, @disclosure_date,
        @amount_min, @amount_max, @shares, @market_value, @period, @discovered_at)
ON CONFLICT (pilot_id, source_key) DO NOTHING
RETURNING id;

-- name: CachedCusips :many
SELECT cusip, symbol FROM cusip_symbols WHERE cusip = ANY(@cusips::text[]);

-- name: InsertCusip :exec
INSERT INTO cusip_symbols (cusip, symbol) VALUES (@cusip, @symbol)
ON CONFLICT (cusip) DO NOTHING;
