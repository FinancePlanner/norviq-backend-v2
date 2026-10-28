-- APNS device registry (PushDeviceService).

-- name: UpsertPushDevice :one
-- A token belongs to whoever registered it last: re-registering moves it to
-- the caller and reactivates it, keeping the row id.
INSERT INTO push_devices (
    id, user_id, device_token, platform, apns_environment, authorization_status,
    is_active, last_seen_at, capabilities_json, created_at, updated_at
) VALUES (
    @id, @user_id, @device_token, @platform, @apns_environment, @authorization_status,
    true, @now, @capabilities_json, @now, @now
)
ON CONFLICT (device_token) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    platform = EXCLUDED.platform,
    apns_environment = EXCLUDED.apns_environment,
    authorization_status = EXCLUDED.authorization_status,
    capabilities_json = EXCLUDED.capabilities_json,
    is_active = true,
    last_seen_at = EXCLUDED.last_seen_at,
    updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: DeactivateUserPushDevice :exec
UPDATE push_devices
SET is_active = false, last_seen_at = @now, updated_at = @now
WHERE user_id = @user_id AND device_token = @device_token;

-- name: DeactivatePushDeviceToken :exec
UPDATE push_devices
SET is_active = false, last_seen_at = @now, updated_at = @now
WHERE device_token = @device_token;

-- name: ActivePushDevices :many
SELECT * FROM push_devices WHERE user_id = $1 AND is_active = true;

-- name: ActivePushDevicesForUsers :many
SELECT * FROM push_devices WHERE user_id = ANY(@user_ids::uuid[]) AND is_active = true;
