-- Fluent migrations Vapor gained after the baseline, as of norviq-backend
-- 37128c4: CreateGroupsTables, CreateAchievementTables, CreateDigestTables,
-- CreateSocialActivityTables (listed under "# goose 2" in
-- internal/migrate/fluent_migrations.txt). Generated from pg_dump of a
-- database Vapor migrated, so the schema matches column for column.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.achievement_evaluations (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    needs_backfill boolean NOT NULL,
    last_evaluated_at timestamp with time zone,
    last_swept_at timestamp with time zone
);

CREATE TABLE public.achievement_settings (
    key text NOT NULL,
    value_at timestamp with time zone NOT NULL
);

CREATE TABLE public.digest_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    email_enabled boolean NOT NULL,
    push_enabled boolean NOT NULL,
    telegram_enabled boolean NOT NULL,
    sentiment_alerts_enabled boolean NOT NULL,
    weekday bigint NOT NULL,
    local_hour bigint NOT NULL,
    timezone text NOT NULL,
    country text,
    unsubscribe_token text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE public.group_bans (
    id uuid NOT NULL,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    banned_by uuid,
    reason text,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE public.group_channel_reads (
    id uuid NOT NULL,
    channel_id uuid NOT NULL,
    user_id uuid NOT NULL,
    last_read_seq bigint NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE TABLE public.group_channels (
    id uuid NOT NULL,
    group_id uuid NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    topic text,
    "position" bigint NOT NULL,
    last_seq bigint NOT NULL,
    last_message_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone
);

CREATE TABLE public.group_invites (
    id uuid NOT NULL,
    group_id uuid NOT NULL,
    created_by uuid,
    code text,
    invitee_id uuid,
    expires_at timestamp with time zone,
    max_uses bigint,
    uses bigint NOT NULL,
    revoked_at timestamp with time zone,
    accepted_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT ck_group_invites_kind CHECK (((code IS NULL) <> (invitee_id IS NULL)))
);

CREATE TABLE public.group_members (
    id uuid NOT NULL,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    push_enabled boolean NOT NULL,
    joined_at timestamp with time zone NOT NULL
);

CREATE TABLE public.group_messages (
    id uuid NOT NULL,
    channel_id uuid NOT NULL,
    group_id uuid NOT NULL,
    author_id uuid NOT NULL,
    seq bigint NOT NULL,
    client_message_id text NOT NULL,
    body text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by uuid
);

CREATE TABLE public.groups (
    id uuid NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    icon_emoji text,
    topic text NOT NULL,
    ticker text,
    visibility text NOT NULL,
    owner_id uuid,
    member_count bigint NOT NULL,
    last_message_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone
);

CREATE TABLE public.sentiment_alerts (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    score_before double precision NOT NULL,
    score_after double precision NOT NULL,
    volume_ratio double precision NOT NULL,
    sent_at timestamp with time zone NOT NULL
);

CREATE TABLE public.social_activity (
    id uuid NOT NULL,
    actor_id uuid NOT NULL,
    kind text NOT NULL,
    user_achievement_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE public.social_congrats (
    id uuid NOT NULL,
    activity_id uuid NOT NULL,
    from_user_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE public.social_invite_redemptions (
    id uuid NOT NULL,
    invite_id uuid NOT NULL,
    inviter_id uuid NOT NULL,
    invitee_id uuid NOT NULL,
    redeemed_at timestamp with time zone NOT NULL
);

CREATE TABLE public.user_achievements (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    achievement_key text NOT NULL,
    tier text DEFAULT ''::text NOT NULL,
    unlocked_at timestamp with time zone NOT NULL,
    source text NOT NULL,
    seen_at timestamp with time zone,
    notified_at timestamp with time zone,
    showcase_rank bigint,
    created_at timestamp with time zone
);

CREATE TABLE public.weekly_digest_deliveries (
    id uuid NOT NULL,
    digest_id uuid NOT NULL,
    channel text NOT NULL,
    status text NOT NULL,
    error text,
    sent_at timestamp with time zone,
    created_at timestamp with time zone
);

CREATE TABLE public.weekly_digests (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    period_start text NOT NULL,
    period_end text NOT NULL,
    status text NOT NULL,
    payload text,
    narrative text,
    model text,
    token_count bigint,
    error text,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

ALTER TABLE public.gamification_xp_events
    ADD COLUMN exclude_from_leaderboard boolean DEFAULT false NOT NULL;

ALTER TABLE public.social_settings
    ADD COLUMN show_achievements boolean DEFAULT true NOT NULL;

ALTER TABLE ONLY public.achievement_evaluations
    ADD CONSTRAINT achievement_evaluations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.achievement_settings
    ADD CONSTRAINT achievement_settings_pkey PRIMARY KEY (key);

ALTER TABLE ONLY public.digest_preferences
    ADD CONSTRAINT digest_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.group_bans
    ADD CONSTRAINT group_bans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.group_channel_reads
    ADD CONSTRAINT group_channel_reads_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.group_invites
    ADD CONSTRAINT group_invites_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.group_members
    ADD CONSTRAINT group_members_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT group_messages_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.groups
    ADD CONSTRAINT groups_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.sentiment_alerts
    ADD CONSTRAINT sentiment_alerts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.social_activity
    ADD CONSTRAINT social_activity_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.social_congrats
    ADD CONSTRAINT social_congrats_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.social_invite_redemptions
    ADD CONSTRAINT social_invite_redemptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.achievement_evaluations
    ADD CONSTRAINT "uq:achievement_evaluations.user_id" UNIQUE (user_id);

ALTER TABLE ONLY public.digest_preferences
    ADD CONSTRAINT "uq:digest_preferences.unsubscribe_token" UNIQUE (unsubscribe_token);

ALTER TABLE ONLY public.digest_preferences
    ADD CONSTRAINT "uq:digest_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY public.group_bans
    ADD CONSTRAINT "uq:group_bans.group_id+group_bans.user_id" UNIQUE (group_id, user_id);

ALTER TABLE ONLY public.group_channel_reads
    ADD CONSTRAINT "uq:group_channel_reads.channel_id+group_channel_reads.user_id" UNIQUE (channel_id, user_id);

ALTER TABLE ONLY public.group_members
    ADD CONSTRAINT "uq:group_members.group_id+group_members.user_id" UNIQUE (group_id, user_id);

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT "uq:group_messages.author_id+group_messages.client_message_id" UNIQUE (author_id, client_message_id);

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT "uq:group_messages.channel_id+group_messages.seq" UNIQUE (channel_id, seq);

ALTER TABLE ONLY public.social_activity
    ADD CONSTRAINT "uq:social_activity.user_achievement_id" UNIQUE (user_achievement_id);

ALTER TABLE ONLY public.social_congrats
    ADD CONSTRAINT "uq:social_congrats.activity_id+social_congrats.from_user_id" UNIQUE (activity_id, from_user_id);

ALTER TABLE ONLY public.social_invite_redemptions
    ADD CONSTRAINT "uq:social_invite_redemptions.invitee_id" UNIQUE (invitee_id);

ALTER TABLE ONLY public.user_achievements
    ADD CONSTRAINT "uq:user_achievements.user_id+user_achievements.achievement_key+" UNIQUE (user_id, achievement_key, tier);

ALTER TABLE ONLY public.weekly_digest_deliveries
    ADD CONSTRAINT "uq:weekly_digest_deliveries.digest_id+weekly_digest_deliveries." UNIQUE (digest_id, channel);

ALTER TABLE ONLY public.weekly_digests
    ADD CONSTRAINT "uq:weekly_digests.user_id+weekly_digests.period_start" UNIQUE (user_id, period_start);

ALTER TABLE ONLY public.user_achievements
    ADD CONSTRAINT user_achievements_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.weekly_digest_deliveries
    ADD CONSTRAINT weekly_digest_deliveries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.weekly_digests
    ADD CONSTRAINT weekly_digests_pkey PRIMARY KEY (id);

CREATE INDEX idx_group_channels_group_id_position ON public.group_channels USING btree (group_id, "position");

CREATE INDEX idx_group_invites_group_id_created_at ON public.group_invites USING btree (group_id, created_at);

CREATE INDEX idx_group_invites_invitee_id_accepted_at ON public.group_invites USING btree (invitee_id, accepted_at);

CREATE INDEX idx_group_members_user_id ON public.group_members USING btree (user_id);

CREATE INDEX idx_group_messages_author_id_created_at ON public.group_messages USING btree (author_id, created_at);

CREATE INDEX idx_group_messages_group_id_created_at ON public.group_messages USING btree (group_id, created_at);

CREATE INDEX idx_groups_owner_id_created_at ON public.groups USING btree (owner_id, created_at);

CREATE INDEX idx_groups_public_rank ON public.groups USING btree (member_count DESC, last_message_at DESC) WHERE ((visibility = 'public'::text) AND (deleted_at IS NULL));

CREATE INDEX idx_groups_public_ticker ON public.groups USING btree (ticker) WHERE ((visibility = 'public'::text) AND (deleted_at IS NULL));

CREATE INDEX idx_sentiment_alerts_user_id_symbol_sent_at ON public.sentiment_alerts USING btree (user_id, symbol, sent_at);

CREATE INDEX idx_social_activity_actor_created ON public.social_activity USING btree (actor_id, created_at, id);

CREATE INDEX idx_social_invite_redemptions_inviter ON public.social_invite_redemptions USING btree (inviter_id);

CREATE INDEX idx_user_achievements_user_id_unlocked_at ON public.user_achievements USING btree (user_id, unlocked_at);

CREATE UNIQUE INDEX uq_group_channels_name ON public.group_channels USING btree (group_id, name) WHERE (deleted_at IS NULL);

CREATE UNIQUE INDEX uq_group_invites_code ON public.group_invites USING btree (code) WHERE (code IS NOT NULL);

CREATE UNIQUE INDEX uq_user_achievements_showcase ON public.user_achievements USING btree (user_id, showcase_rank) WHERE (showcase_rank IS NOT NULL);

ALTER TABLE ONLY public.achievement_evaluations
    ADD CONSTRAINT achievement_evaluations_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.digest_preferences
    ADD CONSTRAINT digest_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_bans
    ADD CONSTRAINT group_bans_banned_by_fkey FOREIGN KEY (banned_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.group_bans
    ADD CONSTRAINT group_bans_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_bans
    ADD CONSTRAINT group_bans_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_channel_reads
    ADD CONSTRAINT group_channel_reads_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_channel_reads
    ADD CONSTRAINT group_channel_reads_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_channels
    ADD CONSTRAINT group_channels_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_invites
    ADD CONSTRAINT group_invites_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.group_invites
    ADD CONSTRAINT group_invites_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_invites
    ADD CONSTRAINT group_invites_invitee_id_fkey FOREIGN KEY (invitee_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_members
    ADD CONSTRAINT group_members_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_members
    ADD CONSTRAINT group_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT group_messages_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT group_messages_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.group_channels(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT group_messages_deleted_by_fkey FOREIGN KEY (deleted_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.group_messages
    ADD CONSTRAINT group_messages_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.groups
    ADD CONSTRAINT groups_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.sentiment_alerts
    ADD CONSTRAINT sentiment_alerts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_activity
    ADD CONSTRAINT social_activity_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_activity
    ADD CONSTRAINT social_activity_user_achievement_id_fkey FOREIGN KEY (user_achievement_id) REFERENCES public.user_achievements(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_congrats
    ADD CONSTRAINT social_congrats_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES public.social_activity(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_congrats
    ADD CONSTRAINT social_congrats_from_user_id_fkey FOREIGN KEY (from_user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_invite_redemptions
    ADD CONSTRAINT social_invite_redemptions_invite_id_fkey FOREIGN KEY (invite_id) REFERENCES public.social_invites(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_invite_redemptions
    ADD CONSTRAINT social_invite_redemptions_invitee_id_fkey FOREIGN KEY (invitee_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_invite_redemptions
    ADD CONSTRAINT social_invite_redemptions_inviter_id_fkey FOREIGN KEY (inviter_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.user_achievements
    ADD CONSTRAINT user_achievements_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.weekly_digest_deliveries
    ADD CONSTRAINT weekly_digest_deliveries_digest_id_fkey FOREIGN KEY (digest_id) REFERENCES public.weekly_digests(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.weekly_digests
    ADD CONSTRAINT weekly_digests_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- CreateAchievementTables: legacy badges become achievements.
INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, seen_at, created_at)
SELECT gen_random_uuid(), user_id, 'legacy.' || badge_type::text, tier::text, earned_at, 'legacy_badge', earned_at, now()
FROM user_badges
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
SELECT 'migration 00002 cannot be rolled back';
