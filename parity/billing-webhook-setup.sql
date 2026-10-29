-- Run after parity/billing-setup.sql, before each parity/billing-webhook.jsonl pass.
DELETE FROM billing_events WHERE provider_event_id LIKE 'pw-%';
DELETE FROM subscriptions WHERE provider_original_transaction_id LIKE 'pw-%';
