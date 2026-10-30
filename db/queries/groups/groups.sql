-- GroupsController, GroupMessagesController, GroupsService and GroupNotifier
-- (Groups/*.swift). Fluent's soft delete: `GroupRecord` and
-- `GroupChannelRecord` queries leave out rows with deleted_at set unless the
-- Swift called `.withDeleted()`. `GroupMessageRecord.deleted_at` is a plain
-- field, so message queries see tombstones.

-- ---- Groups -----------------------------------------------------------------

-- name: GroupByID :many
SELECT * FROM groups WHERE id = $1 AND deleted_at IS NULL;

-- name: GroupsByIDs :many
SELECT * FROM groups WHERE id = ANY(@ids::uuid[]) AND deleted_at IS NULL;

-- name: GroupCountCreatedSince :one
-- `.withDeleted()`: deleted groups still count toward the daily cap.
SELECT COUNT(*) FROM groups WHERE owner_id = $1 AND created_at > $2;

-- name: GroupCountOwned :one
SELECT COUNT(*) FROM groups WHERE owner_id = $1 AND deleted_at IS NULL;

-- name: GroupInsert :exec
INSERT INTO groups (id, name, description, icon_emoji, topic, ticker, visibility, owner_id, member_count, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $9);

-- name: GroupUpdate :exec
UPDATE groups
SET name = $2, description = $3, icon_emoji = $4, topic = $5, ticker = $6, visibility = $7, updated_at = $8
WHERE id = $1;

-- name: GroupSetOwner :exec
UPDATE groups SET owner_id = $2, updated_at = $3 WHERE id = $1;

-- name: GroupSoftDelete :exec
UPDATE groups SET deleted_at = $2 WHERE id = $1;

-- name: GroupRefreshMemberCount :exec
UPDATE groups SET member_count = (SELECT COUNT(*) FROM group_members WHERE group_id = @group_id::uuid)
WHERE id = @group_id::uuid;

-- name: GroupSetLastMessageAt :exec
UPDATE groups SET last_message_at = $2 WHERE id = $1;

-- name: GroupDiscover :many
-- Public groups, most members first. Fluent sorts `.descending`, so groups
-- without messages (NULL last_message_at) come first among equal counts.
SELECT * FROM groups
WHERE visibility = 'public' AND deleted_at IS NULL
  AND (cardinality(@tickers::text[]) = 0 OR ticker = ANY(@tickers::text[]))
  AND (@name_pattern::text = '' OR name ILIKE @name_pattern::text)
ORDER BY member_count DESC, last_message_at DESC, id ASC
LIMIT @lim::bigint OFFSET @off::bigint;

-- ---- Members ----------------------------------------------------------------

-- name: MemberFor :many
SELECT * FROM group_members WHERE group_id = $1 AND user_id = $2 LIMIT 1;

-- name: MembersOfUser :many
SELECT * FROM group_members WHERE user_id = $1;

-- name: MembersOfGroup :many
SELECT * FROM group_members WHERE group_id = $1;

-- name: MembersOfGroupByJoined :many
SELECT * FROM group_members WHERE group_id = $1 ORDER BY joined_at ASC;

-- name: MembershipsOfUserIn :many
SELECT * FROM group_members WHERE group_id = ANY(@group_ids::uuid[]) AND user_id = @user_id;

-- name: MembersOfGroupAmong :many
SELECT user_id FROM group_members WHERE group_id = @group_id AND user_id = ANY(@user_ids::uuid[]);

-- name: MemberCountOfGroup :one
SELECT COUNT(*) FROM group_members WHERE group_id = $1;

-- name: MemberCountOfUser :one
SELECT COUNT(*) FROM group_members WHERE user_id = $1;

-- name: MemberCountOthers :one
SELECT COUNT(*) FROM group_members WHERE group_id = $1 AND user_id <> $2;

-- name: MemberInsert :exec
INSERT INTO group_members (id, group_id, user_id, role, push_enabled, joined_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: MemberDelete :exec
DELETE FROM group_members WHERE group_id = $1 AND user_id = $2;

-- name: MemberSetRole :exec
UPDATE group_members SET role = $2 WHERE id = $1;

-- name: MemberSetPush :exec
UPDATE group_members SET push_enabled = $2 WHERE id = $1;

-- name: PushRecipients :many
SELECT user_id FROM group_members WHERE group_id = $1 AND push_enabled = true AND user_id <> $2;

-- ---- Channels ---------------------------------------------------------------

-- name: ChannelByID :many
SELECT * FROM group_channels WHERE id = $1 AND deleted_at IS NULL;

-- name: ChannelsOfGroups :many
SELECT * FROM group_channels
WHERE group_id = ANY(@group_ids::uuid[]) AND deleted_at IS NULL
ORDER BY position ASC, created_at ASC;

-- name: ChannelNameTaken :one
SELECT EXISTS (SELECT 1 FROM group_channels WHERE group_id = $1 AND name = $2 AND deleted_at IS NULL);

-- name: ChannelCount :one
SELECT COUNT(*) FROM group_channels WHERE group_id = $1 AND deleted_at IS NULL;

-- name: ChannelInsert :exec
INSERT INTO group_channels (id, group_id, name, kind, topic, position, last_seq, created_at)
VALUES ($1, $2, $3, 'text', $4, $5, 0, $6);

-- name: ChannelUpdate :exec
UPDATE group_channels SET name = $2, topic = $3, position = $4 WHERE id = $1;

-- name: ChannelSoftDelete :exec
UPDATE group_channels SET deleted_at = $2 WHERE id = $1;

-- name: ChannelBumpSeq :many
-- Locks the channel row, so seqs commit in order with no gaps.
UPDATE group_channels SET last_seq = last_seq + 1, last_message_at = $2
WHERE id = $1 AND deleted_at IS NULL
RETURNING last_seq;

-- ---- Read state -------------------------------------------------------------

-- name: ReadsMarkGroupRead :exec
-- addMember: every live channel read up to now.
INSERT INTO group_channel_reads (id, channel_id, user_id, last_read_seq, updated_at)
SELECT gen_random_uuid(), c.id, @user_id::uuid, c.last_seq, @now::timestamptz
FROM group_channels c WHERE c.group_id = @group_id::uuid AND c.deleted_at IS NULL
ON CONFLICT (channel_id, user_id) DO UPDATE SET last_read_seq = EXCLUDED.last_read_seq, updated_at = EXCLUDED.updated_at;

-- name: ReadsDeleteForGroup :exec
DELETE FROM group_channel_reads
WHERE user_id = @user_id::uuid
  AND channel_id IN (SELECT id FROM group_channels WHERE group_id = @group_id::uuid);

-- name: ReadUpsert :exec
INSERT INTO group_channel_reads (id, channel_id, user_id, last_read_seq, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (channel_id, user_id) DO UPDATE
SET last_read_seq = GREATEST(group_channel_reads.last_read_seq, EXCLUDED.last_read_seq),
    updated_at = EXCLUDED.updated_at;

-- name: ReadFor :many
SELECT * FROM group_channel_reads WHERE channel_id = $1 AND user_id = $2 LIMIT 1;

-- name: ReadStates :many
SELECT c.id AS channel_id,
       COALESCE(r.last_read_seq, 0)::bigint AS last_read,
       (SELECT COUNT(*) FROM group_messages m
        WHERE m.channel_id = c.id
          AND m.seq > COALESCE(r.last_read_seq, 0)
          AND m.deleted_at IS NULL
          AND m.author_id <> @user_id::uuid
          AND NOT (m.author_id = ANY(@hidden::uuid[])))::bigint AS unread
FROM group_channels c
LEFT JOIN group_channel_reads r ON r.channel_id = c.id AND r.user_id = @user_id::uuid
WHERE c.id = ANY(@channel_ids::uuid[]);

-- name: TotalUnread :one
SELECT COUNT(*) AS unread
FROM group_members gm
JOIN groups g ON g.id = gm.group_id AND g.deleted_at IS NULL
JOIN group_channels c ON c.group_id = gm.group_id AND c.deleted_at IS NULL
LEFT JOIN group_channel_reads r ON r.channel_id = c.id AND r.user_id = gm.user_id
JOIN group_messages m ON m.channel_id = c.id AND m.seq > COALESCE(r.last_read_seq, 0)
WHERE gm.user_id = @user_id::uuid
  AND m.deleted_at IS NULL
  AND m.author_id <> @user_id::uuid
  AND NOT (m.author_id = ANY(@hidden::uuid[]));

-- ---- Messages ---------------------------------------------------------------

-- name: MessageByID :many
SELECT * FROM group_messages WHERE id = $1;

-- name: MessageByClientID :many
SELECT * FROM group_messages WHERE author_id = $1 AND client_message_id = $2 LIMIT 1;

-- name: MessageCountByAuthorSince :one
SELECT COUNT(*) FROM group_messages WHERE author_id = $1 AND created_at > $2;

-- name: MessageInsert :exec
INSERT INTO group_messages (id, channel_id, group_id, author_id, seq, client_message_id, body, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: MessageSoftDelete :exec
UPDATE group_messages SET deleted_at = $2, deleted_by = $3 WHERE id = $1;

-- name: MessagesAfter :many
SELECT * FROM group_messages
WHERE channel_id = @channel_id AND NOT (author_id = ANY(@hidden::uuid[])) AND seq > @after::bigint
ORDER BY seq ASC
LIMIT @lim::bigint;

-- name: MessagesBefore :many
SELECT * FROM group_messages
WHERE channel_id = @channel_id AND NOT (author_id = ANY(@hidden::uuid[]))
  AND (sqlc.narg('before')::bigint IS NULL OR seq < sqlc.narg('before')::bigint)
ORDER BY seq DESC
LIMIT @lim::bigint;

-- name: LatestMessagePreviews :many
SELECT DISTINCT ON (m.group_id) m.group_id, m.author_id, m.body, m.deleted_at, m.created_at
FROM group_messages m
JOIN group_channels c ON c.id = m.channel_id AND c.deleted_at IS NULL
WHERE m.group_id = ANY(@group_ids::uuid[])
  AND NOT (m.author_id = ANY(@hidden::uuid[]))
ORDER BY m.group_id, m.created_at DESC;

-- ---- Invites ----------------------------------------------------------------

-- name: InviteByID :many
SELECT * FROM group_invites WHERE id = $1;

-- name: InviteByCode :many
SELECT * FROM group_invites WHERE code = $1 LIMIT 1;

-- name: InviteInsert :exec
INSERT INTO group_invites (id, group_id, created_by, code, invitee_id, expires_at, max_uses, uses, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8);

-- name: InviteLinksOpen :many
-- activeLinks before the in-memory expiry and use filter.
SELECT * FROM group_invites
WHERE group_id = $1 AND code IS NOT NULL AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: InviteRevoke :exec
UPDATE group_invites SET revoked_at = $2 WHERE id = $1;

-- name: InviteAccept :exec
UPDATE group_invites SET accepted_at = $2 WHERE id = $1;

-- name: InviteClaimUse :many
UPDATE group_invites SET uses = uses + 1
WHERE id = $1 AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > $2)
  AND (max_uses IS NULL OR uses < max_uses)
RETURNING id;

-- name: InvitationsPendingFor :many
SELECT * FROM group_invites
WHERE invitee_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: InviteesPendingAmong :many
SELECT invitee_id FROM group_invites
WHERE group_id = @group_id AND invitee_id = ANY(@user_ids::uuid[])
  AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: InvitationsRevokeForInvitee :exec
-- The ban's sweep: revoked_at is overwritten even on already revoked rows.
UPDATE group_invites SET revoked_at = $3
WHERE group_id = $1 AND invitee_id = $2 AND accepted_at IS NULL;

-- ---- Bans -------------------------------------------------------------------

-- name: BanExists :one
SELECT EXISTS (SELECT 1 FROM group_bans WHERE group_id = $1 AND user_id = $2);

-- name: BansOfGroup :many
SELECT * FROM group_bans WHERE group_id = $1 ORDER BY created_at DESC LIMIT 500;

-- name: BannedAmong :many
SELECT user_id FROM group_bans WHERE group_id = @group_id AND user_id = ANY(@user_ids::uuid[]);

-- name: BanInsert :exec
INSERT INTO group_bans (id, group_id, user_id, banned_by, reason, created_at) VALUES ($1, $2, $3, $4, $5, $6);

-- name: BanDelete :exec
DELETE FROM group_bans WHERE group_id = $1 AND user_id = $2;

-- ---- Users and reports ------------------------------------------------------

-- name: GroupUsernames :many
SELECT id, username FROM users WHERE id = ANY(@ids::uuid[]);

-- name: GroupReportInsert :exec
INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, note, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'open', $7);
