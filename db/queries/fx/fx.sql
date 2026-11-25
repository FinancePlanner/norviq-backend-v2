-- name: RatesOnOrBefore :many
SELECT date, quote, rate::float8 AS rate
FROM fx_daily_rates
WHERE base = 'EUR' AND quote = ANY(@quotes::text[]) AND date <= @day AND date >= @from_day
ORDER BY quote, date DESC;

-- name: InsertECBRate :execrows
INSERT INTO fx_daily_rates (id, date, base, quote, rate)
VALUES (gen_random_uuid(), @date, 'EUR', @quote, CAST(@rate::text AS numeric))
ON CONFLICT DO NOTHING;
