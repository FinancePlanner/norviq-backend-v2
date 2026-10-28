-- Baseline: the schema Vapor's 159 Fluent migrations produce, as of
-- norviq-backend e6e35df, plus the rows its seed migrations insert
-- (pilots, tax replacement instruments).
--
-- On a database Vapor already migrated, `norviq-api migrate` stamps this
-- version as applied without running it (see internal/migrate). Never edit it;
-- add a new migration instead.

-- +goose Up
-- +goose StatementBegin


CREATE TYPE asset_category AS ENUM (
    'stock',
    'etf',
    'crypto',
    'mutual_fund',
    'cash',
    'bond',
    'real_estate',
    'commodity'
);

CREATE TYPE badge_tier AS ENUM (
    'bronze',
    'silver',
    'gold'
);

CREATE TYPE badge_type AS ENUM (
    'first_purchase',
    'news_reader',
    'investor',
    'saver',
    'frugal_fun',
    'spending_detox',
    'growth_mindset'
);

CREATE TYPE expense_split_mode AS ENUM (
    'personal',
    'shared'
);

CREATE TYPE trial_warning_type AS ENUM (
    'expiring_soon',
    'expired'
);

CREATE TYPE user_activity_type AS ENUM (
    'stock_added',
    'expense_recorded',
    'stock_updated',
    'expense_updated',
    'news_viewed'
);

CREATE TABLE accounts (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    external_id text NOT NULL,
    broker text NOT NULL,
    display_name text,
    base_currency text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    tax_wrapper text,
    tax_jurisdiction text,
    tax_owner_member_id text,
    lot_selection_method text,
    portfolio_id uuid
);

CREATE TABLE advanced_report_artifacts (
    id uuid NOT NULL,
    run_id uuid NOT NULL,
    format text NOT NULL,
    filename text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    storage_key text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE advanced_report_deliveries (
    id uuid NOT NULL,
    run_id uuid NOT NULL,
    recipient_user_id uuid NOT NULL,
    recipient_email text NOT NULL,
    status text NOT NULL,
    attempt_count bigint NOT NULL,
    last_attempt_at timestamp with time zone,
    delivered_at timestamp with time zone,
    failure_reason text,
    link_expires_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE advanced_report_runs (
    id uuid NOT NULL,
    template_id uuid NOT NULL,
    schedule_id uuid,
    requested_by_user_id uuid NOT NULL,
    template_revision bigint NOT NULL,
    template_input_json text NOT NULL,
    output_formats_json text NOT NULL,
    recipient_user_ids_json text NOT NULL,
    status text NOT NULL,
    scheduled_for timestamp with time zone,
    claimed_at timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    failure_reason text,
    attempt_count bigint NOT NULL,
    retry_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE advanced_report_schedules (
    id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    input_json text NOT NULL,
    next_run_at timestamp with time zone,
    last_run_at timestamp with time zone,
    paused_reason text,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE advanced_report_templates (
    id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    input_json text NOT NULL,
    revision bigint NOT NULL,
    is_starter_template boolean NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE ai_action_audits (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    pending_action_id uuid NOT NULL,
    tool_name text NOT NULL,
    status text NOT NULL,
    details_encrypted bytea,
    created_at timestamp with time zone
);

CREATE TABLE ai_conversations (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    title_encrypted bytea NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL
);

CREATE TABLE ai_messages (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    content_encrypted bytea NOT NULL,
    created_at timestamp with time zone,
    origin text,
    source_label text
);

CREATE TABLE ai_pending_actions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    conversation_id uuid,
    tool_name text NOT NULL,
    arguments_encrypted bytea NOT NULL,
    summary_encrypted bytea NOT NULL,
    status text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE ai_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    proactive_tips_enabled boolean DEFAULT false NOT NULL,
    push_enabled boolean DEFAULT false NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE ai_tips (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    title_encrypted bytea NOT NULL,
    body_encrypted bytea NOT NULL,
    importance bigint NOT NULL,
    action_path text,
    is_seen boolean DEFAULT false NOT NULL,
    is_dismissed boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL
);

CREATE TABLE ai_usage_monthly (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    month_start date NOT NULL,
    request_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE allocation_buckets (
    id uuid NOT NULL,
    model_id uuid NOT NULL,
    name text NOT NULL,
    target_bps bigint NOT NULL,
    alert_threshold_bps bigint,
    sector_key text,
    sort_order bigint NOT NULL
);

CREATE TABLE allocation_leaves (
    id uuid NOT NULL,
    model_id uuid NOT NULL,
    bucket_id uuid NOT NULL,
    kind text NOT NULL,
    symbol text,
    name text NOT NULL,
    target_bps bigint NOT NULL,
    alert_threshold_bps bigint,
    sort_order bigint NOT NULL
);

CREATE TABLE allocation_models (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    name text NOT NULL,
    grouping_mode text NOT NULL,
    is_active boolean NOT NULL,
    revision bigint NOT NULL,
    base_currency text NOT NULL,
    default_target_threshold_bps bigint NOT NULL,
    total_threshold_bps bigint NOT NULL,
    fractional_shares_enabled boolean NOT NULL,
    quantity_increment double precision NOT NULL,
    minimum_trade_amount double precision NOT NULL,
    flat_fee double precision NOT NULL,
    variable_fee_bps bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE analyst_estimates_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    period text NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE article_images (
    id uuid NOT NULL,
    owner_id uuid NOT NULL,
    content_type text NOT NULL,
    bytes bytea NOT NULL,
    width bigint NOT NULL,
    height bigint NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE article_views (
    id uuid NOT NULL,
    article_id uuid NOT NULL,
    viewer_key text NOT NULL,
    day text NOT NULL
);

CREATE TABLE article_votes (
    id uuid NOT NULL,
    article_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE articles (
    id uuid NOT NULL,
    code text NOT NULL,
    author_id uuid NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    body_markdown text NOT NULL,
    bullet_points text[] NOT NULL,
    tickers text[] NOT NULL,
    disclosure text NOT NULL,
    cover_image_id uuid,
    status text NOT NULL,
    source text NOT NULL,
    view_count bigint NOT NULL,
    upvote_count bigint NOT NULL,
    word_count bigint NOT NULL,
    published_at timestamp with time zone NOT NULL,
    edited_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE assistant_watches (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    title_encrypted bytea NOT NULL,
    spec_encrypted bytea NOT NULL,
    condition_encrypted bytea,
    schedule_human text NOT NULL,
    interval_minutes bigint NOT NULL,
    next_run_at timestamp with time zone NOT NULL,
    last_run_at timestamp with time zone,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE autobuys (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    ticker text,
    label text NOT NULL,
    amount double precision NOT NULL,
    cadence text NOT NULL,
    percent double precision,
    active boolean NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE automation_job_leases (
    name text NOT NULL,
    owner text NOT NULL,
    locked_until timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE bank_accounts (
    id uuid NOT NULL,
    connection_id uuid NOT NULL,
    provider_account_id text NOT NULL,
    name text NOT NULL,
    mask text,
    currency text,
    type text,
    balance double precision,
    balance_as_of timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    identification_hash text
);

CREATE TABLE bank_connections (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    institution_id text,
    institution_name text,
    provider_item_id text NOT NULL,
    access_token_enc text,
    sync_cursor text,
    status text NOT NULL,
    consent_expires_at timestamp with time zone,
    last_synced_at timestamp with time zone,
    last_sync_status text,
    last_sync_error text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE bank_link_flows (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    reference text NOT NULL,
    requisition_id text NOT NULL,
    institution_id text,
    app_redirect_uri text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE bank_transactions (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider_tx_id text NOT NULL,
    dedupe_hash text NOT NULL,
    amount double precision NOT NULL,
    currency text,
    occurred_on timestamp with time zone NOT NULL,
    merchant text,
    description_text text,
    pending boolean NOT NULL,
    status text NOT NULL,
    provider_category text,
    expense_id uuid,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE basic_financials_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE billing_events (
    id uuid NOT NULL,
    provider text NOT NULL,
    provider_event_id text NOT NULL,
    user_id uuid,
    event_type text NOT NULL,
    raw_payload text NOT NULL,
    processed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE board_comments (
    id uuid NOT NULL,
    post_id uuid NOT NULL,
    parent_id uuid,
    author_id uuid NOT NULL,
    body text NOT NULL,
    depth bigint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone
);

CREATE TABLE board_notification_settings (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    reply_push boolean NOT NULL,
    upvote_push boolean NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE board_notifications (
    id uuid NOT NULL,
    recipient_id uuid NOT NULL,
    kind text NOT NULL,
    actor_id uuid,
    post_id uuid NOT NULL,
    comment_id uuid,
    created_at timestamp with time zone NOT NULL,
    read_at timestamp with time zone
);

CREATE TABLE board_post_views (
    id uuid NOT NULL,
    post_id uuid NOT NULL,
    user_id uuid NOT NULL,
    last_seen_comment_count bigint NOT NULL,
    last_seen_at timestamp with time zone NOT NULL
);

CREATE TABLE board_posts (
    id uuid NOT NULL,
    board_id uuid NOT NULL,
    author_id uuid NOT NULL,
    kind text NOT NULL,
    title text NOT NULL,
    url text,
    domain text,
    body text,
    tags text[] NOT NULL,
    score bigint NOT NULL,
    comment_count bigint NOT NULL,
    participant_count bigint NOT NULL,
    view_count bigint NOT NULL,
    last_activity_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone,
    ai_generated boolean DEFAULT false NOT NULL,
    ai_topic text,
    trading_date text
);

CREATE TABLE board_votes (
    id uuid NOT NULL,
    post_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE boards (
    id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    creator_id uuid,
    post_count bigint NOT NULL,
    last_activity_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone
);

CREATE TABLE broker_connections (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    external_id text,
    access_token text,
    refresh_token text,
    expires_at timestamp with time zone,
    status text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    display_name text,
    status_detail text,
    connected_at timestamp with time zone,
    last_synced_at timestamp with time zone,
    portfolio_list_id uuid
);

CREATE TABLE broker_oauth_flows (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    state text NOT NULL,
    redirect_uri text NOT NULL,
    portfolio_list_id uuid,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE budget_drift_alert_states (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    scope_key text NOT NULL,
    level text NOT NULL,
    breach_sequence bigint DEFAULT 0 NOT NULL,
    last_notified_level text,
    last_notified_at timestamp with time zone,
    evaluated_at timestamp with time zone,
    cleared_at timestamp with time zone
);

CREATE TABLE budget_plan_items (
    id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    planned_amount double precision NOT NULL,
    pillar text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    split_mode expense_split_mode DEFAULT 'personal'::expense_split_mode NOT NULL,
    user_share_percent double precision DEFAULT 100 NOT NULL,
    category_id uuid,
    target_type text DEFAULT 'fixed'::text NOT NULL,
    income_percentage double precision,
    threshold_override double precision,
    allocation_kind text DEFAULT 'expense'::text NOT NULL,
    reallocation_eligible boolean DEFAULT false NOT NULL,
    destination_financial_goal_id uuid,
    destination_portfolio_list_id uuid
);

CREATE TABLE budget_reallocation_events (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    request_id uuid NOT NULL,
    source_snapshot_id uuid NOT NULL,
    target_snapshot_id uuid NOT NULL,
    effective_month date NOT NULL,
    freed_capital double precision NOT NULL,
    financial_goal_id uuid,
    portfolio_list_id uuid,
    adjustments jsonb NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE budget_snapshots (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    month_start date NOT NULL,
    net_salary double precision NOT NULL,
    target_shares jsonb NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    last_budget_alert_threshold bigint,
    currency_code text DEFAULT 'USD'::text NOT NULL,
    category_drift_threshold double precision DEFAULT 15 NOT NULL,
    total_drift_threshold double precision DEFAULT 10 NOT NULL,
    alerts_enabled boolean DEFAULT true NOT NULL,
    alert_on_unbudgeted boolean DEFAULT true NOT NULL,
    revision bigint DEFAULT 0 NOT NULL
);

CREATE TABLE budget_templates (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    items jsonb NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE cash_balances (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    currency text NOT NULL,
    balance double precision NOT NULL,
    as_of date NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE community_guidelines_acceptances (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    accepted_at timestamp with time zone NOT NULL
);

CREATE TABLE community_sanctions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    reason text NOT NULL,
    expires_at timestamp with time zone,
    created_by uuid,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE coupon_redemptions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    coupon_id uuid NOT NULL,
    code text NOT NULL,
    trial_days_granted bigint NOT NULL,
    discount_percentage bigint,
    discount_amount bigint,
    currency text,
    redeemed_at timestamp with time zone
);

CREATE TABLE coupons (
    id uuid NOT NULL,
    code text NOT NULL,
    trial_days bigint NOT NULL,
    discount_percentage bigint,
    discount_amount bigint,
    currency text,
    max_uses bigint,
    current_uses bigint DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    grant_type text DEFAULT 'trial'::text NOT NULL
);

CREATE TABLE crypto_portfolio_items (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    name text NOT NULL,
    quantity double precision NOT NULL,
    average_buy_price double precision NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE cusip_symbols (
    cusip text NOT NULL,
    symbol text,
    resolved_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE daily_board_post_runs (
    trading_date text NOT NULL,
    status text NOT NULL,
    post_id uuid,
    topic text,
    created_at timestamp with time zone
);

CREATE TABLE data_exports (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    type text NOT NULL,
    format text NOT NULL,
    filters jsonb,
    status text NOT NULL,
    file_path text,
    file_size_bytes bigint,
    expires_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE dividends (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    instrument_id uuid NOT NULL,
    external_id text,
    amount double precision NOT NULL,
    currency text NOT NULL,
    ex_date date,
    pay_date date NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    withholding_tax double precision,
    gross_amount double precision,
    source_country text
);

CREATE TABLE earnings_notification_deliveries (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    earnings_date text NOT NULL,
    lead_days bigint NOT NULL,
    sent_at timestamp with time zone NOT NULL
);

CREATE TABLE earnings_notification_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    enabled boolean NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE entitlements (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    level text NOT NULL,
    subscription_id uuid,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE expense_categories (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    pillar text,
    is_default boolean NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE expense_import_sessions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    status text NOT NULL,
    file_name text NOT NULL,
    sheet_count bigint NOT NULL,
    row_count bigint NOT NULL,
    ai_available boolean NOT NULL,
    ai_model text,
    ai_confidence double precision,
    sheets_encrypted bytea NOT NULL,
    mapping_encrypted bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    committed_at timestamp with time zone
);

CREATE TABLE expenses (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    amount double precision NOT NULL,
    pillar text NOT NULL,
    occurred_on date NOT NULL,
    linked_item_id uuid,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    split_mode expense_split_mode DEFAULT 'personal'::expense_split_mode NOT NULL,
    user_share_percent double precision DEFAULT 100 NOT NULL,
    category_id uuid,
    foreign_amount double precision,
    foreign_currency text,
    exchange_rate double precision,
    notes text,
    receipt_metadata jsonb
);

CREATE TABLE feedbacks (
    id uuid NOT NULL,
    topic text NOT NULL,
    message text NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financial_goal_adjustment_drafts (
    id uuid NOT NULL,
    suggestion_id uuid NOT NULL,
    user_id uuid NOT NULL,
    destination text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financial_goal_contributions (
    id uuid NOT NULL,
    goal_id uuid NOT NULL,
    user_id uuid NOT NULL,
    amount double precision NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    note text,
    created_at timestamp with time zone
);

CREATE TABLE financial_goal_expense_category_links (
    id uuid NOT NULL,
    goal_id uuid NOT NULL,
    user_id uuid NOT NULL,
    category_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financial_goal_portfolio_allocations (
    id uuid NOT NULL,
    goal_id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    allocation_percentage double precision NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financial_goal_progress_snapshots (
    id uuid NOT NULL,
    goal_id uuid NOT NULL,
    user_id uuid NOT NULL,
    current_value double precision NOT NULL,
    planned_value double precision NOT NULL,
    projected_value double precision NOT NULL,
    drift_state text NOT NULL,
    is_month_end boolean NOT NULL,
    calculated_at timestamp with time zone NOT NULL
);

CREATE TABLE financial_goal_suggestions (
    id uuid NOT NULL,
    goal_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    title text NOT NULL,
    explanation text NOT NULL,
    monthly_amount double precision,
    allocation_percentage double precision,
    estimated_months_changed bigint,
    status text NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financial_goals (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    name text NOT NULL,
    target_amount double precision NOT NULL,
    target_date timestamp with time zone NOT NULL,
    base_currency text NOT NULL,
    monthly_contribution double precision NOT NULL,
    annual_contribution_growth double precision NOT NULL,
    inflation_assumption double precision NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    goal_type text DEFAULT 'custom'::text NOT NULL,
    starting_capital double precision DEFAULT 0 NOT NULL,
    risk_profile text DEFAULT 'moderate'::text NOT NULL,
    expected_annual_return double precision DEFAULT 0.06 NOT NULL,
    status text DEFAULT 'active'::text NOT NULL
);

CREATE TABLE financial_growth_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    period text NOT NULL,
    "limit" bigint NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE financing_affordability_assumptions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    income_scope text NOT NULL,
    net_monthly_income_override double precision,
    gross_monthly_income double precision,
    external_monthly_debt_payments double precision NOT NULL,
    safety_buffer_percent double precision NOT NULL,
    monthly_savings_target_override double precision,
    updated_at timestamp with time zone
);

CREATE TABLE financing_expense_matches (
    id uuid NOT NULL,
    plan_id uuid NOT NULL,
    installment_number bigint NOT NULL,
    expense_id uuid NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financing_plan_revisions (
    id uuid NOT NULL,
    plan_id uuid NOT NULL,
    effective_installment bigint NOT NULL,
    terms jsonb NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE financing_plans (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    market text NOT NULL,
    purchase_type text NOT NULL,
    currency text NOT NULL,
    status text NOT NULL,
    user_share_percent double precision NOT NULL,
    source_domain text,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE fx_daily_rates (
    id uuid NOT NULL,
    date date NOT NULL,
    base text NOT NULL,
    quote text NOT NULL,
    rate numeric(18,8) NOT NULL
);

CREATE TABLE fx_rates (
    id uuid NOT NULL,
    date date NOT NULL,
    base text NOT NULL,
    quote text NOT NULL,
    rate double precision NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE gamification_budget_streaks (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    months bigint NOT NULL,
    best_months bigint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE gamification_check_ins (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    local_date text NOT NULL,
    time_zone text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE gamification_xp_events (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    type text NOT NULL,
    points bigint NOT NULL,
    dedupe_key text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE germany_fund_advance_allocations (
    id uuid NOT NULL,
    annual_holding_id uuid NOT NULL,
    disposal_id uuid NOT NULL,
    quantity double precision NOT NULL,
    gross_advance_amount double precision NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE germany_fund_annual_holdings (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    account_id uuid NOT NULL,
    instrument_id uuid NOT NULL,
    calculation_year bigint NOT NULL,
    client_holding_id text NOT NULL,
    lot_id uuid,
    quantity double precision,
    remaining_quantity double precision,
    currency text NOT NULL,
    beginning_market_value double precision NOT NULL,
    ending_market_value double precision NOT NULL,
    distributions double precision NOT NULL,
    acquisition_month bigint,
    fund_classification text NOT NULL,
    basis_rate double precision NOT NULL,
    gross_advance_lump_sum double precision NOT NULL,
    remaining_gross_advance double precision NOT NULL,
    taxable_advance_lump_sum double precision NOT NULL,
    rule_version text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE germany_general_loss_applications (
    id uuid NOT NULL,
    source_year_id uuid NOT NULL,
    target_tax_year bigint NOT NULL,
    amount double precision NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE germany_general_loss_years (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    net_capital_result double precision NOT NULL,
    loss_generated double precision NOT NULL,
    prior_loss_applied double precision NOT NULL,
    taxable_capital_gain double precision NOT NULL,
    ending_loss_carryforward double precision NOT NULL,
    rule_version text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE germany_stock_loss_applications (
    id uuid NOT NULL,
    source_year_id uuid NOT NULL,
    target_tax_year bigint NOT NULL,
    amount double precision NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE germany_stock_loss_years (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    net_stock_result double precision NOT NULL,
    loss_generated double precision NOT NULL,
    prior_loss_applied double precision NOT NULL,
    taxable_stock_gain double precision NOT NULL,
    ending_loss_carryforward double precision NOT NULL,
    rule_version text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE goals (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    status text NOT NULL,
    status_updated_by text,
    completed_at timestamp without time zone
);

CREATE TABLE holding_risk_profiles (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    holding_id uuid NOT NULL,
    asset_category text NOT NULL,
    sector text,
    region text,
    benchmark_proxy text,
    manual_value double precision,
    duration double precision,
    convexity double precision,
    factor_overrides jsonb NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE insight_events (
    id uuid NOT NULL,
    dedupe_key text NOT NULL,
    source text NOT NULL,
    topic text NOT NULL,
    title text,
    summary text,
    sentiment_label text,
    sentiment_score double precision,
    source_url text,
    author text,
    observed_at timestamp with time zone NOT NULL,
    raw_payload text,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE instruments (
    id uuid NOT NULL,
    conid text NOT NULL,
    symbol text NOT NULL,
    exchange text NOT NULL,
    currency text NOT NULL,
    name text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    instrument_type text,
    isin text,
    cusip text,
    tax_identity_group text,
    listing_exchange text,
    regulated_market_status text,
    regulated_market_source text,
    regulated_market_reviewed_at timestamp with time zone,
    fund_classification text
);

CREATE TABLE lot_adjustments (
    id uuid NOT NULL,
    lot_id uuid NOT NULL,
    source_transaction_id uuid,
    kind text NOT NULL,
    amount double precision NOT NULL,
    quantity double precision,
    currency text NOT NULL,
    effective_date date NOT NULL,
    metadata_json text,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE lot_disposals (
    id uuid NOT NULL,
    lot_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    quantity double precision NOT NULL,
    proceeds double precision NOT NULL,
    cost_basis double precision NOT NULL,
    realized_pnl double precision NOT NULL,
    holding_period text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE lots (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    instrument_id uuid NOT NULL,
    open_transaction_id uuid,
    close_transaction_id uuid,
    open_date date NOT NULL,
    close_date date,
    open_quantity double precision NOT NULL,
    remaining_quantity double precision NOT NULL,
    open_price double precision NOT NULL,
    currency text NOT NULL,
    realized_pnl double precision,
    status text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE macro_series_points (
    id uuid NOT NULL,
    country text NOT NULL,
    series_key text NOT NULL,
    period_date text NOT NULL,
    value double precision NOT NULL,
    unit text NOT NULL,
    source text NOT NULL,
    vintage_date timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE macro_snapshots (
    id uuid NOT NULL,
    country text NOT NULL,
    as_of text NOT NULL,
    source text NOT NULL,
    payload text NOT NULL,
    fetched_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE market_brief_notification_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    enabled boolean NOT NULL,
    language text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE market_briefs (
    id uuid NOT NULL,
    trading_date text NOT NULL,
    slot text NOT NULL,
    language text NOT NULL,
    payload text NOT NULL,
    model text NOT NULL,
    degraded boolean NOT NULL,
    generated_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE market_news_archive (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    headline text NOT NULL,
    source text,
    url text,
    summary text,
    published_at timestamp with time zone NOT NULL,
    fetched_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    image_url text,
    story_id uuid
);

CREATE TABLE market_price_bars (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
)
PARTITION BY RANGE (date);

CREATE TABLE market_price_bars_1990 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1991 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1992 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1993 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1994 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1995 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1996 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1997 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1998 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_1999 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2000 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2001 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2002 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2003 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2004 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2005 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2006 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2007 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2008 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2009 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2010 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2011 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2012 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2013 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2014 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2015 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2016 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2017 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2018 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2019 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2020 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2021 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2022 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2023 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2024 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2025 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2026 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2027 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2028 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2029 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2030 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2031 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2032 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2033 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2034 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE market_price_bars_2035 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    instrument_key text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    adjusted_close double precision NOT NULL,
    volume double precision,
    currency text NOT NULL,
    provider text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE messaging_link_codes (
    id uuid NOT NULL,
    code_hash bytea NOT NULL,
    user_id uuid NOT NULL,
    platform text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    redeemed_at timestamp with time zone,
    created_at timestamp with time zone
);

CREATE TABLE messaging_links (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    platform text NOT NULL,
    external_id text NOT NULL,
    last_update_id bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone,
    last_seen_at timestamp with time zone,
    conversation_id uuid
);

CREATE TABLE messaging_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    platform text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    quiet_hours_start bigint,
    quiet_hours_end bigint,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE mfa_challenges (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    purpose text NOT NULL,
    channel text NOT NULL,
    destination text NOT NULL,
    code_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    failed_attempts bigint NOT NULL,
    resend_count bigint NOT NULL,
    last_sent_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE net_worth_forecast_runs (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    forecast_id uuid NOT NULL,
    status text NOT NULL,
    starting_value double precision NOT NULL,
    assumptions jsonb NOT NULL,
    timeline jsonb NOT NULL,
    target_probability double precision,
    seed text NOT NULL,
    failure_reason text,
    created_at timestamp with time zone,
    completed_at timestamp with time zone
);

CREATE TABLE net_worth_forecasts (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    name text NOT NULL,
    base_currency text NOT NULL,
    horizon_months bigint NOT NULL,
    include_cash boolean NOT NULL,
    include_crypto boolean NOT NULL,
    annual_income_growth double precision NOT NULL,
    annual_spending_growth double precision NOT NULL,
    inflation_assumption double precision NOT NULL,
    monthly_income_override double precision,
    monthly_spending_override double precision,
    target_amount double precision,
    path_count bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE net_worth_snapshots (
    id uuid NOT NULL,
    dedupe_key text NOT NULL,
    total_value double precision,
    currency text NOT NULL,
    captured_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE news_items (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    headline text NOT NULL,
    source text,
    url text,
    summary text,
    published_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE news_ticker_feed_subscriptions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    feed_url text NOT NULL,
    title text,
    created_at timestamp with time zone
);

CREATE TABLE news_ticker_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    enabled boolean NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE notification_deliveries (
    id uuid NOT NULL,
    event_id uuid NOT NULL,
    channel text NOT NULL,
    status text NOT NULL,
    attempt_count bigint NOT NULL,
    last_error text,
    next_attempt_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE notification_events (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    deduplication_key text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    deep_link text,
    payload jsonb NOT NULL,
    created_at timestamp with time zone,
    read_at timestamp with time zone
);

CREATE TABLE oauth_authorization_flows (
    id uuid NOT NULL,
    client_id text NOT NULL,
    user_id uuid,
    scopes text[] NOT NULL,
    redirect_uri text NOT NULL,
    state text,
    code_challenge text NOT NULL,
    code_hash text,
    status text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE oauth_clients (
    id uuid NOT NULL,
    client_id text NOT NULL,
    client_name text NOT NULL,
    redirect_uris text[] NOT NULL,
    token_endpoint_auth_method text NOT NULL,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE oauth_flows (
    id uuid NOT NULL,
    provider text NOT NULL,
    state text NOT NULL,
    nonce text NOT NULL,
    code_verifier text NOT NULL,
    redirect_uri text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    purpose text DEFAULT 'login'::text NOT NULL,
    user_id uuid
);

CREATE TABLE oauth_identities (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    provider_user_id text NOT NULL,
    email text,
    email_verified boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE oauth_tokens (
    id uuid NOT NULL,
    client_id text NOT NULL,
    user_id uuid NOT NULL,
    access_token_hash text NOT NULL,
    refresh_token_hash text NOT NULL,
    scopes text[] NOT NULL,
    access_expires_at timestamp with time zone NOT NULL,
    refresh_expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    replaced_by uuid,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE onboarding_state (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    funnel_step text,
    funnel_completed_at timestamp with time zone,
    first_holding_at timestamp with time zone,
    first_budget_at timestamp with time zone,
    first_goal_at timestamp with time zone,
    guided_start_dismissed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone
);

CREATE TABLE password_reset_tokens (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    code_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    failed_attempts bigint DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone
);

CREATE TABLE personal_access_tokens (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    scopes text[] NOT NULL,
    last_used_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE pilot_book_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pilot_id uuid NOT NULL,
    version integer NOT NULL,
    computed_at timestamp with time zone NOT NULL,
    weights jsonb NOT NULL,
    skipped_puts integer DEFAULT 0 NOT NULL
);

CREATE TABLE pilot_disclosures (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pilot_id uuid NOT NULL,
    source_key text NOT NULL,
    symbol text NOT NULL,
    side text NOT NULL,
    instrument text NOT NULL,
    transaction_date text,
    disclosure_date text,
    amount_min double precision,
    amount_max double precision,
    shares double precision,
    market_value double precision,
    period text,
    raw jsonb,
    discovered_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE pilot_follow_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    follow_id uuid NOT NULL,
    book_version integer NOT NULL,
    kind text NOT NULL,
    symbol text NOT NULL,
    quantity double precision,
    price double precision,
    priced_at timestamp with time zone NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now()
);

CREATE TABLE pilot_follow_snapshots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    follow_id uuid NOT NULL,
    captured_on timestamp with time zone NOT NULL,
    value double precision NOT NULL,
    cash double precision NOT NULL,
    created_at timestamp with time zone DEFAULT now()
);

CREATE TABLE pilot_follows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    pilot_id uuid NOT NULL,
    target_kind text NOT NULL,
    portfolio_list_id uuid,
    watchlist_list_id uuid,
    starting_capital double precision,
    currency text DEFAULT 'USD'::text NOT NULL,
    applied_version integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone,
    CONSTRAINT pilot_follows_check CHECK ((((target_kind = 'portfolio'::text) AND (portfolio_list_id IS NOT NULL) AND (watchlist_list_id IS NULL)) OR ((target_kind = 'watchlist'::text) AND (watchlist_list_id IS NOT NULL) AND (portfolio_list_id IS NULL)))),
    CONSTRAINT pilot_follows_status_check CHECK ((status = ANY (ARRAY['active'::text, 'paused'::text]))),
    CONSTRAINT pilot_follows_target_kind_check CHECK ((target_kind = ANY (ARRAY['portfolio'::text, 'watchlist'::text])))
);

CREATE TABLE pilots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    slug text NOT NULL,
    display_name text NOT NULL,
    chamber text,
    bioguide_id text,
    cik text,
    name_aliases text[] DEFAULT '{}'::text[] NOT NULL,
    active boolean DEFAULT true NOT NULL,
    last_ingested_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone,
    CONSTRAINT pilots_kind_check CHECK ((kind = ANY (ARRAY['politician'::text, 'fund'::text])))
);

CREATE TABLE planning_scenarios (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    is_default boolean NOT NULL,
    input_json text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE portfolio_cash_positions (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    label text NOT NULL,
    currency text NOT NULL,
    balance double precision NOT NULL,
    as_of timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE portfolio_invitations (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    email text NOT NULL,
    role text NOT NULL,
    status text NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    accepted_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE portfolio_lists (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    purpose text DEFAULT 'personal'::text NOT NULL,
    ownership text DEFAULT 'individual'::text NOT NULL,
    mode text DEFAULT 'actual'::text NOT NULL,
    base_currency text DEFAULT 'USD'::text NOT NULL,
    source_portfolio_id uuid,
    cloned_at timestamp with time zone,
    archived_at timestamp with time zone
);

CREATE TABLE portfolio_memberships (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    status text NOT NULL,
    joined_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE portfolio_scenario_snapshots (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    base_currency text NOT NULL,
    valuation_timestamp timestamp with time zone NOT NULL,
    payload jsonb NOT NULL,
    warnings jsonb NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE portfolio_share_links (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid,
    slug text NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE portfolio_simulation_legs (
    id uuid NOT NULL,
    simulation_id uuid NOT NULL,
    symbol text NOT NULL,
    display_name text,
    target_bps bigint NOT NULL,
    sort_order bigint NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE portfolio_simulations (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    mode text NOT NULL,
    source_portfolio_id uuid,
    name text NOT NULL,
    base_currency text NOT NULL,
    target_capital double precision NOT NULL,
    fractional_shares_enabled boolean NOT NULL,
    quantity_increment double precision NOT NULL,
    minimum_trade_amount double precision NOT NULL,
    flat_fee double precision NOT NULL,
    variable_fee_bps bigint NOT NULL,
    revision bigint NOT NULL,
    share_slug text,
    share_enabled boolean NOT NULL,
    share_show_capital boolean NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE portfolio_value_snapshots (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    captured_on date NOT NULL,
    currency text NOT NULL,
    market_value double precision NOT NULL,
    cost_basis double precision NOT NULL,
    cash_balance double precision NOT NULL,
    position_count bigint NOT NULL,
    source text NOT NULL,
    priced_symbols bigint NOT NULL,
    missing_symbols bigint NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE position_memos (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    conversation_id uuid,
    asked_symbol text NOT NULL,
    primary_symbol text NOT NULL,
    title_encrypted bytea NOT NULL,
    mark_encrypted bytea NOT NULL,
    sections_encrypted bytea NOT NULL,
    verdict_encrypted bytea NOT NULL,
    sources_encrypted bytea NOT NULL,
    evidence_encrypted bytea NOT NULL,
    bookmarked boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE positions (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    instrument_id uuid NOT NULL,
    quantity double precision NOT NULL,
    average_cost double precision NOT NULL,
    currency text NOT NULL,
    market_value double precision,
    last_price double precision,
    last_price_date date,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE price_history (
    id uuid NOT NULL,
    symbol text NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    volume bigint,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE prices (
    id uuid NOT NULL,
    instrument_id uuid NOT NULL,
    date date NOT NULL,
    open double precision NOT NULL,
    high double precision NOT NULL,
    low double precision NOT NULL,
    close double precision NOT NULL,
    volume bigint,
    currency text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE profile_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    country text,
    currency text,
    estimate_currency text,
    exchange text,
    finnhub_industry text,
    ipo text,
    logo text,
    market_capitalization double precision,
    name text,
    phone text,
    share_outstanding double precision,
    ticker text,
    weburl text,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE push_devices (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    device_token text NOT NULL,
    platform text NOT NULL,
    apns_environment text NOT NULL,
    authorization_status text NOT NULL,
    is_active boolean NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    capabilities_json text DEFAULT '[]'::text NOT NULL
);

CREATE TABLE quote_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    currency text NOT NULL,
    price double precision NOT NULL,
    as_of timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    change double precision,
    percent_change double precision,
    high double precision,
    low double precision,
    open double precision,
    previous_close double precision
);

CREATE TABLE ratios_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    period text NOT NULL,
    "limit" bigint NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE ratios_ttm_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    symbol text NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE rebalance_events (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    status text NOT NULL,
    preview jsonb NOT NULL,
    created_at timestamp with time zone,
    confirmed_at timestamp with time zone,
    dismissed_at timestamp with time zone
);

CREATE TABLE rebalance_plans (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    model_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    model_revision bigint NOT NULL,
    name text,
    status text NOT NULL,
    base_currency text NOT NULL,
    drift_before_bps bigint NOT NULL,
    drift_after_bps bigint NOT NULL,
    total_value double precision NOT NULL,
    estimated_fees double precision NOT NULL,
    estimated_realized_gain_loss double precision NOT NULL,
    simulation_json text NOT NULL,
    completion_note text,
    exported_at timestamp with time zone,
    completed_at timestamp with time zone,
    cancelled_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE rebalancing_alerts (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    model_id uuid NOT NULL,
    user_id uuid NOT NULL,
    scope_id text NOT NULL,
    scope_name text NOT NULL,
    drift_bps bigint NOT NULL,
    threshold_bps bigint NOT NULL,
    status text NOT NULL,
    active_scope_key text,
    triggered_at timestamp with time zone NOT NULL,
    acknowledged_at timestamp with time zone,
    resolved_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE rebalancing_notification_preferences (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    user_id uuid NOT NULL,
    push_enabled boolean NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE rebalancing_policies (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    base_currency text NOT NULL,
    cadence text NOT NULL,
    drift_threshold double precision,
    targets jsonb NOT NULL,
    enabled boolean NOT NULL,
    last_confirmed_at timestamp with time zone,
    last_triggered_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE recaps (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    period_kind text NOT NULL,
    period_key text NOT NULL,
    payload text NOT NULL,
    archetype text,
    schema_version bigint NOT NULL,
    generated_at timestamp with time zone NOT NULL,
    opened_at timestamp with time zone,
    completed_at timestamp with time zone
);

CREATE TABLE recurring_templates (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    amount double precision NOT NULL,
    pillar text NOT NULL,
    category_id uuid,
    frequency text NOT NULL,
    split_mode expense_split_mode NOT NULL,
    user_share_percent double precision NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE refresh_tokens (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE report_suggestion_dismissals (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    suggestion_id text NOT NULL,
    dismissed_at timestamp with time zone NOT NULL
);

CREATE TABLE research_notes (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    title text,
    thesis text NOT NULL,
    risks text,
    catalysts text,
    reference_links text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE retirement_plans (
    id uuid NOT NULL,
    portfolio_id uuid NOT NULL,
    rule_version text NOT NULL,
    input_json text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE scenario_definitions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    portfolio_list_id uuid NOT NULL,
    financial_goal_id uuid,
    name text NOT NULL,
    kind text NOT NULL,
    configuration jsonb NOT NULL,
    is_saved boolean NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE scenario_runs (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    scenario_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    state text NOT NULL,
    progress double precision NOT NULL,
    seed text NOT NULL,
    deduplication_hash text NOT NULL,
    engine_version text NOT NULL,
    catalog_version text NOT NULL,
    result jsonb,
    error_message text,
    lease_owner text,
    lease_expires_at timestamp with time zone,
    created_at timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    expires_at timestamp with time zone
);

CREATE TABLE search_cache (
    id uuid NOT NULL,
    provider text NOT NULL,
    normalized_query text NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE sentiment_snapshots (
    id uuid NOT NULL,
    dedupe_key text NOT NULL,
    scope text NOT NULL,
    scope_key text,
    window_days bigint NOT NULL,
    average_score double precision NOT NULL,
    label text NOT NULL,
    event_count bigint NOT NULL,
    positive_count bigint NOT NULL,
    neutral_count bigint NOT NULL,
    negative_count bigint NOT NULL,
    captured_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE sentiment_universe_symbols (
    id uuid NOT NULL,
    symbol text NOT NULL,
    tier text NOT NULL,
    added_at timestamp with time zone NOT NULL,
    last_ingested_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE social_blocks (
    id uuid NOT NULL,
    blocker_id uuid NOT NULL,
    blocked_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE social_facebook_friends (
    user_id uuid NOT NULL,
    facebook_id text NOT NULL
);

CREATE TABLE social_friend_requests (
    id uuid NOT NULL,
    from_user_id uuid NOT NULL,
    to_user_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE social_friendships (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    friend_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE social_invites (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    code text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE social_reports (
    id uuid NOT NULL,
    reporter_id uuid NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    reason text NOT NULL,
    note text,
    status text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    resolution text,
    resolution_note text,
    resolved_by uuid,
    resolved_at timestamp with time zone
);

CREATE TABLE social_settings (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    search_visibility text NOT NULL,
    discoverable_by_contacts boolean NOT NULL,
    discoverable_by_x boolean NOT NULL,
    show_return_percent boolean NOT NULL,
    show_streaks boolean NOT NULL,
    show_xp boolean NOT NULL,
    leaderboard_opt_in boolean NOT NULL,
    email_hash text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    suspended_at timestamp with time zone,
    discoverable_by_facebook boolean DEFAULT true NOT NULL
);

CREATE TABLE statistics_snapshots (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    as_of_date date NOT NULL,
    generated_at timestamp with time zone NOT NULL,
    payload text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE stock_valuations (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    bear_low double precision NOT NULL,
    bear_high double precision NOT NULL,
    base_low double precision NOT NULL,
    base_high double precision NOT NULL,
    bull_low double precision NOT NULL,
    bull_high double precision NOT NULL,
    rationale text,
    target_date date,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE stocks (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    shares double precision NOT NULL,
    buy_price double precision NOT NULL,
    buy_date date NOT NULL,
    notes text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    category asset_category DEFAULT 'stock'::asset_category NOT NULL,
    source_provider text,
    source_account_id uuid,
    portfolio_list_id uuid NOT NULL
);

CREATE TABLE subscriptions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    provider_customer_id text NOT NULL,
    provider_original_transaction_id text NOT NULL,
    product_id text NOT NULL,
    plan text NOT NULL,
    status text NOT NULL,
    period_started_at timestamp with time zone,
    period_ends_at timestamp with time zone,
    trial_ends_at timestamp with time zone,
    grace_period_ends_at timestamp with time zone,
    cancelled_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    store text,
    pending_product_id text,
    pending_plan text,
    pending_plan_effective_at timestamp with time zone
);

CREATE TABLE symbol_sentiment_daily (
    id uuid NOT NULL,
    symbol text NOT NULL,
    as_of_date text NOT NULL,
    window_days bigint NOT NULL,
    score double precision,
    label text NOT NULL,
    confidence double precision,
    post_count bigint NOT NULL,
    positive_count bigint NOT NULL,
    neutral_count bigint NOT NULL,
    negative_count bigint NOT NULL,
    source_counts jsonb NOT NULL,
    volume_z double precision,
    delta_1d double precision,
    themes jsonb,
    themes_generated_at timestamp with time zone,
    captured_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE targets (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    scenario text NOT NULL,
    target_price double precision NOT NULL,
    target_date date,
    rationale text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    alert_triggered_at timestamp with time zone,
    alert_triggered_price double precision
);

CREATE TABLE tax_action_legs (
    id uuid NOT NULL,
    action_plan_id uuid NOT NULL,
    account_id uuid NOT NULL,
    portfolio_id uuid,
    instrument_id uuid NOT NULL,
    symbol text NOT NULL,
    side text NOT NULL,
    quantity double precision,
    notional double precision NOT NULL,
    currency text NOT NULL,
    lot_ids_json text NOT NULL,
    status text NOT NULL,
    matched_transaction_id uuid,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE tax_action_plans (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    scenario_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL,
    response_json text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    kind text DEFAULT 'harvest'::text NOT NULL,
    executed_at timestamp with time zone,
    confirmation_note text
);

CREATE TABLE tax_action_rebalancing_plans (
    id uuid NOT NULL,
    action_plan_id uuid NOT NULL,
    rebalancing_plan_id uuid NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE tax_loss_carryforward_applications (
    id uuid NOT NULL,
    carryforward_id uuid NOT NULL,
    target_tax_year bigint NOT NULL,
    amount double precision NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE tax_loss_carryforwards (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    jurisdiction text NOT NULL,
    source_tax_year bigint NOT NULL,
    expires_after_tax_year bigint NOT NULL,
    original_amount double precision NOT NULL,
    remaining_amount double precision NOT NULL,
    currency text NOT NULL,
    rule_version text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE tax_notification_deliveries (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    opportunity_id text NOT NULL,
    instrument_id uuid NOT NULL,
    estimated_benefit double precision NOT NULL,
    currency text NOT NULL,
    delivered_at timestamp with time zone NOT NULL
);

CREATE TABLE tax_notification_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    enabled boolean NOT NULL,
    minimum_benefit double precision,
    cooldown_days bigint NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE tax_opportunity_decisions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    opportunity_id text NOT NULL,
    status text NOT NULL,
    estimated_benefit double precision NOT NULL,
    currency text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE tax_pack_entitlements (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    jurisdiction text,
    source text NOT NULL,
    product_id text NOT NULL,
    provider_event_id text,
    granted_at timestamp with time zone
);

CREATE TABLE tax_profiles (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    jurisdiction text NOT NULL,
    tax_year bigint NOT NULL,
    filing_status text NOT NULL,
    reporting_currency text NOT NULL,
    profile_json text NOT NULL,
    is_complete boolean NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE tax_projection_jobs (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    reason text NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL,
    attempt_count bigint NOT NULL,
    next_attempt_at timestamp with time zone NOT NULL,
    last_error text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE tax_projection_snapshots (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    jurisdiction text NOT NULL,
    rule_version text NOT NULL,
    status text NOT NULL,
    response_json text NOT NULL,
    input_hash text NOT NULL,
    generated_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE tax_reports (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tax_year bigint NOT NULL,
    kind text NOT NULL,
    format text NOT NULL,
    status text NOT NULL,
    file_path text,
    expires_at timestamp with time zone,
    error_message text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    attempt_count bigint,
    next_attempt_at timestamp with time zone
);

CREATE TABLE tax_restriction_windows (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    action_leg_id uuid NOT NULL,
    jurisdiction text NOT NULL,
    tax_identity_key text NOT NULL,
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone NOT NULL,
    status text NOT NULL,
    violating_transaction_id uuid,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE tax_scenarios (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    request_json text NOT NULL,
    response_json text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    kind text DEFAULT 'harvest'::text NOT NULL
);

CREATE TABLE terminal_positions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    ticker text NOT NULL,
    shares_outstanding double precision,
    terminal_share_count double precision NOT NULL,
    terminal_market_cap double precision NOT NULL,
    value_wanted double precision NOT NULL,
    shares_owned double precision NOT NULL,
    current_share_price double precision,
    notes text,
    sort_order bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE thesis_watch_notification_preferences (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    enabled boolean NOT NULL,
    timezone text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE thesis_watch_stories (
    id uuid NOT NULL,
    cluster_key text NOT NULL,
    representative_news_id uuid NOT NULL,
    event_type text NOT NULL,
    severity text NOT NULL,
    first_seen_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE thesis_watch_user_states (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    story_id uuid NOT NULL,
    symbol text,
    impact text NOT NULL,
    confidence double precision,
    summary text,
    why_it_matters text,
    feedback text,
    read_at timestamp with time zone,
    dismissed_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE ticker_sentiment_posts (
    id uuid NOT NULL,
    dedupe_key text NOT NULL,
    symbol text NOT NULL,
    author text,
    author_handle text,
    text text NOT NULL,
    url text,
    sentiment_label text NOT NULL,
    sentiment_score double precision,
    confidence double precision,
    posted_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    source text
);

CREATE TABLE transactions (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    instrument_id uuid NOT NULL,
    external_id text,
    type text NOT NULL,
    quantity double precision,
    price double precision,
    currency text NOT NULL,
    trade_date date NOT NULL,
    settle_date date,
    fees double precision,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE trial_warnings (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    warning_type trial_warning_type NOT NULL,
    sent_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE usage_counters (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    period_start timestamp with time zone NOT NULL,
    holding_count bigint NOT NULL,
    watchlist_item_count bigint NOT NULL,
    csv_import_count bigint NOT NULL,
    target_alert_count bigint NOT NULL,
    report_generation_count bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE user_activities (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    type user_activity_type NOT NULL,
    title text NOT NULL,
    subtitle text NOT NULL,
    amount double precision,
    is_growth boolean NOT NULL,
    symbol text NOT NULL,
    created_at timestamp with time zone,
    reference_key text
);

CREATE TABLE user_ai_provider_credentials (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    label text NOT NULL,
    api_key_encrypted text NOT NULL,
    key_hint text NOT NULL,
    key_fingerprint text NOT NULL,
    base_url text,
    default_model text,
    status text NOT NULL,
    last_verified_at timestamp with time zone,
    last_used_at timestamp with time zone,
    last_error_code text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE user_badges (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    badge_type badge_type NOT NULL,
    tier badge_tier NOT NULL,
    earned_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone
);

CREATE TABLE users (
    id uuid NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    failed_login_attempts bigint DEFAULT 0 NOT NULL,
    lockout_until timestamp with time zone,
    is_verified boolean DEFAULT false NOT NULL,
    username text,
    date_of_birth timestamp with time zone,
    bio text,
    avatar_url text,
    banner_avatar_url text,
    household_partner_display_name text,
    household_default_user_share_percent double precision,
    date_of_birth_encrypted bytea,
    bio_encrypted bytea,
    household_partner_display_name_encrypted bytea,
    trial_started_at timestamp with time zone,
    trial_days bigint,
    trial_tier text,
    trial_warning_sent_at timestamp with time zone,
    had_trial boolean DEFAULT false NOT NULL,
    dca_symbol text,
    email_verified_at timestamp with time zone,
    password_set_at timestamp with time zone,
    sessions_valid_after timestamp with time zone,
    ai_data_consent_at timestamp with time zone
);

CREATE TABLE wash_sale_matches (
    id uuid NOT NULL,
    disposal_id uuid NOT NULL,
    replacement_lot_id uuid NOT NULL,
    matched_quantity double precision NOT NULL,
    disallowed_loss double precision NOT NULL,
    currency text NOT NULL,
    is_permanent boolean NOT NULL,
    rule_version text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE watchlist_items (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    symbol text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone,
    note text,
    status text NOT NULL,
    last_reviewed_at timestamp without time zone,
    next_review_at timestamp without time zone,
    watchlist_list_id uuid NOT NULL
);

CREATE TABLE watchlist_lists (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE watchlist_screen_evaluations (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    screen_id uuid NOT NULL,
    symbol_count bigint NOT NULL,
    match_symbols text[] NOT NULL,
    result jsonb NOT NULL,
    is_alert_baseline boolean NOT NULL,
    evaluated_at timestamp with time zone
);

CREATE TABLE watchlist_screens (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    watchlist_list_ids uuid[] NOT NULL,
    logical_operator text NOT NULL,
    groups jsonb NOT NULL,
    alerts_enabled boolean NOT NULL,
    last_evaluated_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);

CREATE TABLE webauthn_credentials (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    credential_id text NOT NULL,
    public_key bytea NOT NULL,
    sign_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone
);

CREATE TABLE webauthn_login_challenges (
    id uuid NOT NULL,
    challenge bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE webauthn_register_challenges (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    challenge bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1990 FOR VALUES FROM ('1990-01-01') TO ('1991-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1991 FOR VALUES FROM ('1991-01-01') TO ('1992-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1992 FOR VALUES FROM ('1992-01-01') TO ('1993-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1993 FOR VALUES FROM ('1993-01-01') TO ('1994-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1994 FOR VALUES FROM ('1994-01-01') TO ('1995-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1995 FOR VALUES FROM ('1995-01-01') TO ('1996-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1996 FOR VALUES FROM ('1996-01-01') TO ('1997-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1997 FOR VALUES FROM ('1997-01-01') TO ('1998-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1998 FOR VALUES FROM ('1998-01-01') TO ('1999-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_1999 FOR VALUES FROM ('1999-01-01') TO ('2000-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2000 FOR VALUES FROM ('2000-01-01') TO ('2001-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2001 FOR VALUES FROM ('2001-01-01') TO ('2002-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2002 FOR VALUES FROM ('2002-01-01') TO ('2003-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2003 FOR VALUES FROM ('2003-01-01') TO ('2004-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2004 FOR VALUES FROM ('2004-01-01') TO ('2005-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2005 FOR VALUES FROM ('2005-01-01') TO ('2006-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2006 FOR VALUES FROM ('2006-01-01') TO ('2007-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2007 FOR VALUES FROM ('2007-01-01') TO ('2008-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2008 FOR VALUES FROM ('2008-01-01') TO ('2009-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2009 FOR VALUES FROM ('2009-01-01') TO ('2010-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2010 FOR VALUES FROM ('2010-01-01') TO ('2011-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2011 FOR VALUES FROM ('2011-01-01') TO ('2012-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2012 FOR VALUES FROM ('2012-01-01') TO ('2013-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2013 FOR VALUES FROM ('2013-01-01') TO ('2014-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2014 FOR VALUES FROM ('2014-01-01') TO ('2015-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2015 FOR VALUES FROM ('2015-01-01') TO ('2016-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2016 FOR VALUES FROM ('2016-01-01') TO ('2017-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2017 FOR VALUES FROM ('2017-01-01') TO ('2018-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2018 FOR VALUES FROM ('2018-01-01') TO ('2019-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2019 FOR VALUES FROM ('2019-01-01') TO ('2020-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2020 FOR VALUES FROM ('2020-01-01') TO ('2021-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2021 FOR VALUES FROM ('2021-01-01') TO ('2022-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2022 FOR VALUES FROM ('2022-01-01') TO ('2023-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2023 FOR VALUES FROM ('2023-01-01') TO ('2024-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2025 FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2026 FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2027 FOR VALUES FROM ('2027-01-01') TO ('2028-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2028 FOR VALUES FROM ('2028-01-01') TO ('2029-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2029 FOR VALUES FROM ('2029-01-01') TO ('2030-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2030 FOR VALUES FROM ('2030-01-01') TO ('2031-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2031 FOR VALUES FROM ('2031-01-01') TO ('2032-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2032 FOR VALUES FROM ('2032-01-01') TO ('2033-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2033 FOR VALUES FROM ('2033-01-01') TO ('2034-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2034 FOR VALUES FROM ('2034-01-01') TO ('2035-01-01');

ALTER TABLE ONLY market_price_bars ATTACH PARTITION market_price_bars_2035 FOR VALUES FROM ('2035-01-01') TO ('2036-01-01');

ALTER TABLE ONLY accounts
    ADD CONSTRAINT accounts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY advanced_report_artifacts
    ADD CONSTRAINT advanced_report_artifacts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY advanced_report_deliveries
    ADD CONSTRAINT advanced_report_deliveries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY advanced_report_runs
    ADD CONSTRAINT advanced_report_runs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY advanced_report_schedules
    ADD CONSTRAINT advanced_report_schedules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY advanced_report_templates
    ADD CONSTRAINT advanced_report_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_action_audits
    ADD CONSTRAINT ai_action_audits_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_conversations
    ADD CONSTRAINT ai_conversations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_messages
    ADD CONSTRAINT ai_messages_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_pending_actions
    ADD CONSTRAINT ai_pending_actions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_preferences
    ADD CONSTRAINT ai_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_tips
    ADD CONSTRAINT ai_tips_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ai_usage_monthly
    ADD CONSTRAINT ai_usage_monthly_pkey PRIMARY KEY (id);

ALTER TABLE ONLY allocation_buckets
    ADD CONSTRAINT allocation_buckets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY allocation_leaves
    ADD CONSTRAINT allocation_leaves_pkey PRIMARY KEY (id);

ALTER TABLE ONLY allocation_models
    ADD CONSTRAINT allocation_models_pkey PRIMARY KEY (id);

ALTER TABLE ONLY analyst_estimates_cache
    ADD CONSTRAINT analyst_estimates_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY article_images
    ADD CONSTRAINT article_images_pkey PRIMARY KEY (id);

ALTER TABLE ONLY article_views
    ADD CONSTRAINT article_views_pkey PRIMARY KEY (id);

ALTER TABLE ONLY article_votes
    ADD CONSTRAINT article_votes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY articles
    ADD CONSTRAINT articles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY assistant_watches
    ADD CONSTRAINT assistant_watches_pkey PRIMARY KEY (id);

ALTER TABLE ONLY autobuys
    ADD CONSTRAINT autobuys_pkey PRIMARY KEY (id);

ALTER TABLE ONLY automation_job_leases
    ADD CONSTRAINT automation_job_leases_pkey PRIMARY KEY (name);

ALTER TABLE ONLY bank_accounts
    ADD CONSTRAINT bank_accounts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY bank_connections
    ADD CONSTRAINT bank_connections_pkey PRIMARY KEY (id);

ALTER TABLE ONLY bank_link_flows
    ADD CONSTRAINT bank_link_flows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY bank_transactions
    ADD CONSTRAINT bank_transactions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY basic_financials_cache
    ADD CONSTRAINT basic_financials_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing_events
    ADD CONSTRAINT billing_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY board_comments
    ADD CONSTRAINT board_comments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY board_notification_settings
    ADD CONSTRAINT board_notification_settings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY board_notifications
    ADD CONSTRAINT board_notifications_pkey PRIMARY KEY (id);

ALTER TABLE ONLY board_post_views
    ADD CONSTRAINT board_post_views_pkey PRIMARY KEY (id);

ALTER TABLE ONLY board_posts
    ADD CONSTRAINT board_posts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY board_votes
    ADD CONSTRAINT board_votes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY boards
    ADD CONSTRAINT boards_pkey PRIMARY KEY (id);

ALTER TABLE ONLY broker_connections
    ADD CONSTRAINT broker_connections_pkey PRIMARY KEY (id);

ALTER TABLE ONLY broker_oauth_flows
    ADD CONSTRAINT broker_oauth_flows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY budget_drift_alert_states
    ADD CONSTRAINT budget_drift_alert_states_pkey PRIMARY KEY (id);

ALTER TABLE ONLY budget_plan_items
    ADD CONSTRAINT budget_plan_items_pkey PRIMARY KEY (id);

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT budget_reallocation_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY budget_snapshots
    ADD CONSTRAINT budget_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY budget_templates
    ADD CONSTRAINT budget_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY cash_balances
    ADD CONSTRAINT cash_balances_pkey PRIMARY KEY (id);

ALTER TABLE ONLY community_guidelines_acceptances
    ADD CONSTRAINT community_guidelines_acceptances_pkey PRIMARY KEY (id);

ALTER TABLE ONLY community_sanctions
    ADD CONSTRAINT community_sanctions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY coupons
    ADD CONSTRAINT coupons_pkey PRIMARY KEY (id);

ALTER TABLE ONLY crypto_portfolio_items
    ADD CONSTRAINT crypto_portfolio_items_pkey PRIMARY KEY (id);

ALTER TABLE ONLY cusip_symbols
    ADD CONSTRAINT cusip_symbols_pkey PRIMARY KEY (cusip);

ALTER TABLE ONLY daily_board_post_runs
    ADD CONSTRAINT daily_board_post_runs_pkey PRIMARY KEY (trading_date);

ALTER TABLE ONLY data_exports
    ADD CONSTRAINT data_exports_pkey PRIMARY KEY (id);

ALTER TABLE ONLY dividends
    ADD CONSTRAINT dividends_pkey PRIMARY KEY (id);

ALTER TABLE ONLY earnings_notification_deliveries
    ADD CONSTRAINT earnings_notification_deliveries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY earnings_notification_preferences
    ADD CONSTRAINT earnings_notification_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY entitlements
    ADD CONSTRAINT entitlements_pkey PRIMARY KEY (id);

ALTER TABLE ONLY expense_categories
    ADD CONSTRAINT expense_categories_pkey PRIMARY KEY (id);

ALTER TABLE ONLY expense_import_sessions
    ADD CONSTRAINT expense_import_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY expenses
    ADD CONSTRAINT expenses_pkey PRIMARY KEY (id);

ALTER TABLE ONLY feedbacks
    ADD CONSTRAINT feedbacks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goal_adjustment_drafts
    ADD CONSTRAINT financial_goal_adjustment_drafts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goal_contributions
    ADD CONSTRAINT financial_goal_contributions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goal_expense_category_links
    ADD CONSTRAINT financial_goal_expense_category_links_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goal_portfolio_allocations
    ADD CONSTRAINT financial_goal_portfolio_allocations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goal_progress_snapshots
    ADD CONSTRAINT financial_goal_progress_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goal_suggestions
    ADD CONSTRAINT financial_goal_suggestions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_goals
    ADD CONSTRAINT financial_goals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financial_growth_cache
    ADD CONSTRAINT financial_growth_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financing_affordability_assumptions
    ADD CONSTRAINT financing_affordability_assumptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financing_expense_matches
    ADD CONSTRAINT financing_expense_matches_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financing_plan_revisions
    ADD CONSTRAINT financing_plan_revisions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY financing_plans
    ADD CONSTRAINT financing_plans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY fx_daily_rates
    ADD CONSTRAINT fx_daily_rates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY fx_rates
    ADD CONSTRAINT fx_rates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY gamification_budget_streaks
    ADD CONSTRAINT gamification_budget_streaks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY gamification_check_ins
    ADD CONSTRAINT gamification_check_ins_pkey PRIMARY KEY (id);

ALTER TABLE ONLY gamification_xp_events
    ADD CONSTRAINT gamification_xp_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY germany_fund_advance_allocations
    ADD CONSTRAINT germany_fund_advance_allocations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY germany_fund_annual_holdings
    ADD CONSTRAINT germany_fund_annual_holdings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY germany_general_loss_applications
    ADD CONSTRAINT germany_general_loss_applications_pkey PRIMARY KEY (id);

ALTER TABLE ONLY germany_general_loss_years
    ADD CONSTRAINT germany_general_loss_years_pkey PRIMARY KEY (id);

ALTER TABLE ONLY germany_stock_loss_applications
    ADD CONSTRAINT germany_stock_loss_applications_pkey PRIMARY KEY (id);

ALTER TABLE ONLY germany_stock_loss_years
    ADD CONSTRAINT germany_stock_loss_years_pkey PRIMARY KEY (id);

ALTER TABLE ONLY goals
    ADD CONSTRAINT goals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY holding_risk_profiles
    ADD CONSTRAINT holding_risk_profiles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY insight_events
    ADD CONSTRAINT insight_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY instruments
    ADD CONSTRAINT instruments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lot_adjustments
    ADD CONSTRAINT lot_adjustments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lot_disposals
    ADD CONSTRAINT lot_disposals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lots
    ADD CONSTRAINT lots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY macro_series_points
    ADD CONSTRAINT macro_series_points_pkey PRIMARY KEY (id);

ALTER TABLE ONLY macro_snapshots
    ADD CONSTRAINT macro_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY market_brief_notification_preferences
    ADD CONSTRAINT market_brief_notification_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY market_briefs
    ADD CONSTRAINT market_briefs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY market_news_archive
    ADD CONSTRAINT market_news_archive_pkey PRIMARY KEY (id);

ALTER TABLE ONLY market_price_bars
    ADD CONSTRAINT market_price_bars_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1990
    ADD CONSTRAINT market_price_bars_1990_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars
    ADD CONSTRAINT market_price_bars_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1990
    ADD CONSTRAINT market_price_bars_1990_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1991
    ADD CONSTRAINT market_price_bars_1991_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1991
    ADD CONSTRAINT market_price_bars_1991_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1992
    ADD CONSTRAINT market_price_bars_1992_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1992
    ADD CONSTRAINT market_price_bars_1992_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1993
    ADD CONSTRAINT market_price_bars_1993_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1993
    ADD CONSTRAINT market_price_bars_1993_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1994
    ADD CONSTRAINT market_price_bars_1994_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1994
    ADD CONSTRAINT market_price_bars_1994_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1995
    ADD CONSTRAINT market_price_bars_1995_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1995
    ADD CONSTRAINT market_price_bars_1995_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1996
    ADD CONSTRAINT market_price_bars_1996_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1996
    ADD CONSTRAINT market_price_bars_1996_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1997
    ADD CONSTRAINT market_price_bars_1997_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1997
    ADD CONSTRAINT market_price_bars_1997_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1998
    ADD CONSTRAINT market_price_bars_1998_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1998
    ADD CONSTRAINT market_price_bars_1998_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_1999
    ADD CONSTRAINT market_price_bars_1999_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_1999
    ADD CONSTRAINT market_price_bars_1999_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2000
    ADD CONSTRAINT market_price_bars_2000_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2000
    ADD CONSTRAINT market_price_bars_2000_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2001
    ADD CONSTRAINT market_price_bars_2001_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2001
    ADD CONSTRAINT market_price_bars_2001_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2002
    ADD CONSTRAINT market_price_bars_2002_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2002
    ADD CONSTRAINT market_price_bars_2002_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2003
    ADD CONSTRAINT market_price_bars_2003_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2003
    ADD CONSTRAINT market_price_bars_2003_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2004
    ADD CONSTRAINT market_price_bars_2004_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2004
    ADD CONSTRAINT market_price_bars_2004_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2005
    ADD CONSTRAINT market_price_bars_2005_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2005
    ADD CONSTRAINT market_price_bars_2005_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2006
    ADD CONSTRAINT market_price_bars_2006_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2006
    ADD CONSTRAINT market_price_bars_2006_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2007
    ADD CONSTRAINT market_price_bars_2007_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2007
    ADD CONSTRAINT market_price_bars_2007_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2008
    ADD CONSTRAINT market_price_bars_2008_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2008
    ADD CONSTRAINT market_price_bars_2008_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2009
    ADD CONSTRAINT market_price_bars_2009_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2009
    ADD CONSTRAINT market_price_bars_2009_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2010
    ADD CONSTRAINT market_price_bars_2010_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2010
    ADD CONSTRAINT market_price_bars_2010_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2011
    ADD CONSTRAINT market_price_bars_2011_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2011
    ADD CONSTRAINT market_price_bars_2011_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2012
    ADD CONSTRAINT market_price_bars_2012_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2012
    ADD CONSTRAINT market_price_bars_2012_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2013
    ADD CONSTRAINT market_price_bars_2013_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2013
    ADD CONSTRAINT market_price_bars_2013_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2014
    ADD CONSTRAINT market_price_bars_2014_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2014
    ADD CONSTRAINT market_price_bars_2014_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2015
    ADD CONSTRAINT market_price_bars_2015_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2015
    ADD CONSTRAINT market_price_bars_2015_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2016
    ADD CONSTRAINT market_price_bars_2016_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2016
    ADD CONSTRAINT market_price_bars_2016_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2017
    ADD CONSTRAINT market_price_bars_2017_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2017
    ADD CONSTRAINT market_price_bars_2017_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2018
    ADD CONSTRAINT market_price_bars_2018_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2018
    ADD CONSTRAINT market_price_bars_2018_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2019
    ADD CONSTRAINT market_price_bars_2019_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2019
    ADD CONSTRAINT market_price_bars_2019_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2020
    ADD CONSTRAINT market_price_bars_2020_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2020
    ADD CONSTRAINT market_price_bars_2020_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2021
    ADD CONSTRAINT market_price_bars_2021_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2021
    ADD CONSTRAINT market_price_bars_2021_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2022
    ADD CONSTRAINT market_price_bars_2022_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2022
    ADD CONSTRAINT market_price_bars_2022_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2023
    ADD CONSTRAINT market_price_bars_2023_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2023
    ADD CONSTRAINT market_price_bars_2023_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2024
    ADD CONSTRAINT market_price_bars_2024_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2024
    ADD CONSTRAINT market_price_bars_2024_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2025
    ADD CONSTRAINT market_price_bars_2025_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2025
    ADD CONSTRAINT market_price_bars_2025_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2026
    ADD CONSTRAINT market_price_bars_2026_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2026
    ADD CONSTRAINT market_price_bars_2026_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2027
    ADD CONSTRAINT market_price_bars_2027_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2027
    ADD CONSTRAINT market_price_bars_2027_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2028
    ADD CONSTRAINT market_price_bars_2028_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2028
    ADD CONSTRAINT market_price_bars_2028_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2029
    ADD CONSTRAINT market_price_bars_2029_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2029
    ADD CONSTRAINT market_price_bars_2029_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2030
    ADD CONSTRAINT market_price_bars_2030_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2030
    ADD CONSTRAINT market_price_bars_2030_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2031
    ADD CONSTRAINT market_price_bars_2031_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2031
    ADD CONSTRAINT market_price_bars_2031_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2032
    ADD CONSTRAINT market_price_bars_2032_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2032
    ADD CONSTRAINT market_price_bars_2032_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2033
    ADD CONSTRAINT market_price_bars_2033_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2033
    ADD CONSTRAINT market_price_bars_2033_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2034
    ADD CONSTRAINT market_price_bars_2034_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2034
    ADD CONSTRAINT market_price_bars_2034_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY market_price_bars_2035
    ADD CONSTRAINT market_price_bars_2035_instrument_key_date_key UNIQUE (instrument_key, date);

ALTER TABLE ONLY market_price_bars_2035
    ADD CONSTRAINT market_price_bars_2035_pkey PRIMARY KEY (id, date);

ALTER TABLE ONLY messaging_link_codes
    ADD CONSTRAINT messaging_link_codes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY messaging_links
    ADD CONSTRAINT messaging_links_pkey PRIMARY KEY (id);

ALTER TABLE ONLY messaging_preferences
    ADD CONSTRAINT messaging_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY mfa_challenges
    ADD CONSTRAINT mfa_challenges_pkey PRIMARY KEY (id);

ALTER TABLE ONLY net_worth_forecast_runs
    ADD CONSTRAINT net_worth_forecast_runs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY net_worth_forecasts
    ADD CONSTRAINT net_worth_forecasts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY net_worth_snapshots
    ADD CONSTRAINT net_worth_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY news_items
    ADD CONSTRAINT news_items_pkey PRIMARY KEY (id);

ALTER TABLE ONLY news_ticker_feed_subscriptions
    ADD CONSTRAINT news_ticker_feed_subscriptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY news_ticker_preferences
    ADD CONSTRAINT news_ticker_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY notification_events
    ADD CONSTRAINT notification_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY oauth_authorization_flows
    ADD CONSTRAINT oauth_authorization_flows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY oauth_clients
    ADD CONSTRAINT oauth_clients_pkey PRIMARY KEY (id);

ALTER TABLE ONLY oauth_flows
    ADD CONSTRAINT oauth_flows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY oauth_identities
    ADD CONSTRAINT oauth_identities_pkey PRIMARY KEY (id);

ALTER TABLE ONLY oauth_tokens
    ADD CONSTRAINT oauth_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY onboarding_state
    ADD CONSTRAINT onboarding_state_pkey PRIMARY KEY (id);

ALTER TABLE ONLY onboarding_state
    ADD CONSTRAINT onboarding_state_user_id_key UNIQUE (user_id);

ALTER TABLE ONLY password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY personal_access_tokens
    ADD CONSTRAINT personal_access_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilot_book_versions
    ADD CONSTRAINT pilot_book_versions_pilot_id_version_key UNIQUE (pilot_id, version);

ALTER TABLE ONLY pilot_book_versions
    ADD CONSTRAINT pilot_book_versions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilot_disclosures
    ADD CONSTRAINT pilot_disclosures_pilot_id_source_key_key UNIQUE (pilot_id, source_key);

ALTER TABLE ONLY pilot_disclosures
    ADD CONSTRAINT pilot_disclosures_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilot_follow_events
    ADD CONSTRAINT pilot_follow_events_follow_id_book_version_symbol_key UNIQUE (follow_id, book_version, symbol);

ALTER TABLE ONLY pilot_follow_events
    ADD CONSTRAINT pilot_follow_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilot_follow_snapshots
    ADD CONSTRAINT pilot_follow_snapshots_follow_id_captured_on_key UNIQUE (follow_id, captured_on);

ALTER TABLE ONLY pilot_follow_snapshots
    ADD CONSTRAINT pilot_follow_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilot_follows
    ADD CONSTRAINT pilot_follows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilots
    ADD CONSTRAINT pilots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pilots
    ADD CONSTRAINT pilots_slug_key UNIQUE (slug);

ALTER TABLE ONLY planning_scenarios
    ADD CONSTRAINT planning_scenarios_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_cash_positions
    ADD CONSTRAINT portfolio_cash_positions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_invitations
    ADD CONSTRAINT portfolio_invitations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_lists
    ADD CONSTRAINT portfolio_lists_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_memberships
    ADD CONSTRAINT portfolio_memberships_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_scenario_snapshots
    ADD CONSTRAINT portfolio_scenario_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_share_links
    ADD CONSTRAINT portfolio_share_links_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_simulation_legs
    ADD CONSTRAINT portfolio_simulation_legs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_simulations
    ADD CONSTRAINT portfolio_simulations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY portfolio_value_snapshots
    ADD CONSTRAINT portfolio_value_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY position_memos
    ADD CONSTRAINT position_memos_pkey PRIMARY KEY (id);

ALTER TABLE ONLY positions
    ADD CONSTRAINT positions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY price_history
    ADD CONSTRAINT price_history_pkey PRIMARY KEY (id);

ALTER TABLE ONLY prices
    ADD CONSTRAINT prices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY profile_cache
    ADD CONSTRAINT profile_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY push_devices
    ADD CONSTRAINT push_devices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY quote_cache
    ADD CONSTRAINT quote_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ratios_cache
    ADD CONSTRAINT ratios_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ratios_ttm_cache
    ADD CONSTRAINT ratios_ttm_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY rebalance_events
    ADD CONSTRAINT rebalance_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY rebalance_plans
    ADD CONSTRAINT rebalance_plans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY rebalancing_alerts
    ADD CONSTRAINT rebalancing_alerts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY rebalancing_notification_preferences
    ADD CONSTRAINT rebalancing_notification_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY rebalancing_policies
    ADD CONSTRAINT rebalancing_policies_pkey PRIMARY KEY (id);

ALTER TABLE ONLY recaps
    ADD CONSTRAINT recaps_pkey PRIMARY KEY (id);

ALTER TABLE ONLY recurring_templates
    ADD CONSTRAINT recurring_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY refresh_tokens
    ADD CONSTRAINT refresh_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY report_suggestion_dismissals
    ADD CONSTRAINT report_suggestion_dismissals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY research_notes
    ADD CONSTRAINT research_notes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY retirement_plans
    ADD CONSTRAINT retirement_plans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY scenario_definitions
    ADD CONSTRAINT scenario_definitions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY scenario_runs
    ADD CONSTRAINT scenario_runs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY search_cache
    ADD CONSTRAINT search_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY sentiment_snapshots
    ADD CONSTRAINT sentiment_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY sentiment_universe_symbols
    ADD CONSTRAINT sentiment_universe_symbols_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_blocks
    ADD CONSTRAINT social_blocks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_facebook_friends
    ADD CONSTRAINT social_facebook_friends_pkey PRIMARY KEY (user_id, facebook_id);

ALTER TABLE ONLY social_friend_requests
    ADD CONSTRAINT social_friend_requests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_friendships
    ADD CONSTRAINT social_friendships_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_invites
    ADD CONSTRAINT social_invites_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_reports
    ADD CONSTRAINT social_reports_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_settings
    ADD CONSTRAINT social_settings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY statistics_snapshots
    ADD CONSTRAINT statistics_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY stock_valuations
    ADD CONSTRAINT stock_valuations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY stocks
    ADD CONSTRAINT stocks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY subscriptions
    ADD CONSTRAINT subscriptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY symbol_sentiment_daily
    ADD CONSTRAINT symbol_sentiment_daily_pkey PRIMARY KEY (id);

ALTER TABLE ONLY targets
    ADD CONSTRAINT targets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_action_legs
    ADD CONSTRAINT tax_action_legs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_action_plans
    ADD CONSTRAINT tax_action_plans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_action_rebalancing_plans
    ADD CONSTRAINT tax_action_rebalancing_plans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_loss_carryforward_applications
    ADD CONSTRAINT tax_loss_carryforward_applications_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_loss_carryforwards
    ADD CONSTRAINT tax_loss_carryforwards_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_notification_deliveries
    ADD CONSTRAINT tax_notification_deliveries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_notification_preferences
    ADD CONSTRAINT tax_notification_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_opportunity_decisions
    ADD CONSTRAINT tax_opportunity_decisions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_pack_entitlements
    ADD CONSTRAINT tax_pack_entitlements_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_profiles
    ADD CONSTRAINT tax_profiles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_projection_jobs
    ADD CONSTRAINT tax_projection_jobs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_projection_snapshots
    ADD CONSTRAINT tax_projection_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_reports
    ADD CONSTRAINT tax_reports_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_restriction_windows
    ADD CONSTRAINT tax_restriction_windows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tax_scenarios
    ADD CONSTRAINT tax_scenarios_pkey PRIMARY KEY (id);

ALTER TABLE ONLY terminal_positions
    ADD CONSTRAINT terminal_positions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY thesis_watch_notification_preferences
    ADD CONSTRAINT thesis_watch_notification_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY thesis_watch_stories
    ADD CONSTRAINT thesis_watch_stories_pkey PRIMARY KEY (id);

ALTER TABLE ONLY thesis_watch_user_states
    ADD CONSTRAINT thesis_watch_user_states_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ticker_sentiment_posts
    ADD CONSTRAINT ticker_sentiment_posts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY transactions
    ADD CONSTRAINT transactions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY trial_warnings
    ADD CONSTRAINT trial_warnings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts
    ADD CONSTRAINT "uq:accounts.broker+accounts.external_id" UNIQUE (broker, external_id);

ALTER TABLE ONLY advanced_report_deliveries
    ADD CONSTRAINT "uq:advanced_report_deliveries.run_id+advanced_report_deliveries" UNIQUE (run_id, recipient_user_id);

ALTER TABLE ONLY ai_action_audits
    ADD CONSTRAINT "uq:ai_action_audits.pending_action_id" UNIQUE (pending_action_id);

ALTER TABLE ONLY ai_preferences
    ADD CONSTRAINT "uq:ai_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY ai_usage_monthly
    ADD CONSTRAINT "uq:ai_usage_monthly.user_id+ai_usage_monthly.month_start" UNIQUE (user_id, month_start);

ALTER TABLE ONLY allocation_leaves
    ADD CONSTRAINT "uq:allocation_leaves.model_id+allocation_leaves.symbol" UNIQUE (model_id, symbol);

ALTER TABLE ONLY analyst_estimates_cache
    ADD CONSTRAINT "uq:analyst_estimates_cache.provider+analyst_estimates_cache.sym" UNIQUE (provider, symbol, period);

ALTER TABLE ONLY article_views
    ADD CONSTRAINT "uq:article_views.article_id+article_views.viewer_key+article_vi" UNIQUE (article_id, viewer_key, day);

ALTER TABLE ONLY article_votes
    ADD CONSTRAINT "uq:article_votes.article_id+article_votes.user_id" UNIQUE (article_id, user_id);

ALTER TABLE ONLY articles
    ADD CONSTRAINT "uq:articles.code" UNIQUE (code);

ALTER TABLE ONLY bank_accounts
    ADD CONSTRAINT "uq:bank_accounts.connection_id+bank_accounts.provider_account_i" UNIQUE (connection_id, provider_account_id);

ALTER TABLE ONLY bank_connections
    ADD CONSTRAINT "uq:bank_connections.provider+bank_connections.provider_item_id" UNIQUE (provider, provider_item_id);

ALTER TABLE ONLY bank_link_flows
    ADD CONSTRAINT "uq:bank_link_flows.reference" UNIQUE (reference);

ALTER TABLE ONLY bank_transactions
    ADD CONSTRAINT "uq:bank_transactions.account_id+bank_transactions.provider_tx_i" UNIQUE (account_id, provider_tx_id);

ALTER TABLE ONLY basic_financials_cache
    ADD CONSTRAINT "uq:basic_financials_cache.provider+basic_financials_cache.symbo" UNIQUE (provider, symbol);

ALTER TABLE ONLY billing_events
    ADD CONSTRAINT "uq:billing_events.provider_event_id" UNIQUE (provider_event_id);

ALTER TABLE ONLY board_notification_settings
    ADD CONSTRAINT "uq:board_notification_settings.user_id" UNIQUE (user_id);

ALTER TABLE ONLY board_post_views
    ADD CONSTRAINT "uq:board_post_views.post_id+board_post_views.user_id" UNIQUE (post_id, user_id);

ALTER TABLE ONLY board_votes
    ADD CONSTRAINT "uq:board_votes.post_id+board_votes.user_id" UNIQUE (post_id, user_id);

ALTER TABLE ONLY boards
    ADD CONSTRAINT "uq:boards.slug" UNIQUE (slug);

ALTER TABLE ONLY broker_connections
    ADD CONSTRAINT "uq:broker_connections.user_id+broker_connections.provider" UNIQUE (user_id, provider);

ALTER TABLE ONLY budget_drift_alert_states
    ADD CONSTRAINT "uq:budget_drift_alert_states.snapshot_id+budget_drift_alert_sta" UNIQUE (snapshot_id, scope_key);

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT "uq:budget_reallocation_events.user_id+budget_reallocation_event" UNIQUE (user_id, request_id);

ALTER TABLE ONLY budget_snapshots
    ADD CONSTRAINT "uq:budget_snapshots.user_id+budget_snapshots.month_start" UNIQUE (user_id, month_start);

ALTER TABLE ONLY budget_templates
    ADD CONSTRAINT "uq:budget_templates.user_id+budget_templates.name" UNIQUE (user_id, name);

ALTER TABLE ONLY cash_balances
    ADD CONSTRAINT "uq:cash_balances.account_id+cash_balances.currency+cash_balance" UNIQUE (account_id, currency, as_of);

ALTER TABLE ONLY community_guidelines_acceptances
    ADD CONSTRAINT "uq:community_guidelines_acceptances.user_id" UNIQUE (user_id);

ALTER TABLE ONLY coupon_redemptions
    ADD CONSTRAINT "uq:coupon_redemptions.user_id+coupon_redemptions.coupon_id" UNIQUE (user_id, coupon_id);

ALTER TABLE ONLY coupons
    ADD CONSTRAINT "uq:coupons.code" UNIQUE (code);

ALTER TABLE ONLY dividends
    ADD CONSTRAINT "uq:dividends.account_id+dividends.external_id" UNIQUE (account_id, external_id);

ALTER TABLE ONLY earnings_notification_deliveries
    ADD CONSTRAINT "uq:earnings_notification_deliveries.user_id+earnings_notificati" UNIQUE (user_id, symbol, earnings_date, lead_days);

ALTER TABLE ONLY earnings_notification_preferences
    ADD CONSTRAINT "uq:earnings_notification_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY entitlements
    ADD CONSTRAINT "uq:entitlements.user_id" UNIQUE (user_id);

ALTER TABLE ONLY financial_goal_expense_category_links
    ADD CONSTRAINT "uq:financial_goal_expense_category_links.goal_id+financial_goal" UNIQUE (goal_id, category_id, role);

ALTER TABLE ONLY financial_goal_portfolio_allocations
    ADD CONSTRAINT "uq:financial_goal_portfolio_allocations.goal_id+financial_goal_" UNIQUE (goal_id, portfolio_list_id);

ALTER TABLE ONLY financial_growth_cache
    ADD CONSTRAINT "uq:financial_growth_cache.provider+financial_growth_cache.symbo" UNIQUE (provider, symbol, period, "limit");

ALTER TABLE ONLY financing_affordability_assumptions
    ADD CONSTRAINT "uq:financing_affordability_assumptions.user_id" UNIQUE (user_id);

ALTER TABLE ONLY financing_expense_matches
    ADD CONSTRAINT "uq:financing_expense_matches.expense_id" UNIQUE (expense_id);

ALTER TABLE ONLY financing_expense_matches
    ADD CONSTRAINT "uq:financing_expense_matches.plan_id+financing_expense_matches." UNIQUE (plan_id, installment_number);

ALTER TABLE ONLY financing_plan_revisions
    ADD CONSTRAINT "uq:financing_plan_revisions.plan_id+financing_plan_revisions.ef" UNIQUE (plan_id, effective_installment);

ALTER TABLE ONLY fx_daily_rates
    ADD CONSTRAINT "uq:fx_daily_rates.date+fx_daily_rates.base+fx_daily_rates.quote" UNIQUE (date, base, quote);

ALTER TABLE ONLY fx_rates
    ADD CONSTRAINT "uq:fx_rates.date+fx_rates.base+fx_rates.quote" UNIQUE (date, base, quote);

ALTER TABLE ONLY gamification_budget_streaks
    ADD CONSTRAINT "uq:gamification_budget_streaks.user_id" UNIQUE (user_id);

ALTER TABLE ONLY gamification_check_ins
    ADD CONSTRAINT "uq:gamification_check_ins.user_id+gamification_check_ins.local_" UNIQUE (user_id, local_date);

ALTER TABLE ONLY gamification_xp_events
    ADD CONSTRAINT "uq:gamification_xp_events.user_id+gamification_xp_events.dedupe" UNIQUE (user_id, dedupe_key);

ALTER TABLE ONLY germany_fund_advance_allocations
    ADD CONSTRAINT "uq:germany_fund_advance_allocations.annual_holding_id+germany_f" UNIQUE (annual_holding_id, disposal_id);

ALTER TABLE ONLY germany_fund_annual_holdings
    ADD CONSTRAINT "uq:germany_fund_annual_holdings.user_id+germany_fund_annual_hol" UNIQUE (user_id, account_id, instrument_id, calculation_year, client_holding_id);

ALTER TABLE ONLY germany_general_loss_applications
    ADD CONSTRAINT "uq:germany_general_loss_applications.source_year_id+germany_gen" UNIQUE (source_year_id, target_tax_year);

ALTER TABLE ONLY germany_general_loss_years
    ADD CONSTRAINT "uq:germany_general_loss_years.user_id+germany_general_loss_year" UNIQUE (user_id, tax_year);

ALTER TABLE ONLY germany_stock_loss_applications
    ADD CONSTRAINT "uq:germany_stock_loss_applications.source_year_id+germany_stock" UNIQUE (source_year_id, target_tax_year);

ALTER TABLE ONLY germany_stock_loss_years
    ADD CONSTRAINT "uq:germany_stock_loss_years.user_id+germany_stock_loss_years.ta" UNIQUE (user_id, tax_year);

ALTER TABLE ONLY holding_risk_profiles
    ADD CONSTRAINT "uq:holding_risk_profiles.user_id+holding_risk_profiles.holding_" UNIQUE (user_id, holding_id);

ALTER TABLE ONLY insight_events
    ADD CONSTRAINT "uq:insight_events.dedupe_key" UNIQUE (dedupe_key);

ALTER TABLE ONLY instruments
    ADD CONSTRAINT "uq:instruments.conid+instruments.exchange+instruments.currency" UNIQUE (conid, exchange, currency);

ALTER TABLE ONLY lot_disposals
    ADD CONSTRAINT "uq:lot_disposals.lot_id+lot_disposals.transaction_id" UNIQUE (lot_id, transaction_id);

ALTER TABLE ONLY macro_series_points
    ADD CONSTRAINT "uq:macro_series_points.country+macro_series_points.series_key+m" UNIQUE (country, series_key, period_date, source, vintage_date);

ALTER TABLE ONLY macro_snapshots
    ADD CONSTRAINT "uq:macro_snapshots.country+macro_snapshots.as_of+macro_snapshot" UNIQUE (country, as_of, source);

ALTER TABLE ONLY market_brief_notification_preferences
    ADD CONSTRAINT "uq:market_brief_notification_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY market_briefs
    ADD CONSTRAINT "uq:market_briefs.trading_date+market_briefs.slot+market_briefs." UNIQUE (trading_date, slot, language);

ALTER TABLE ONLY messaging_link_codes
    ADD CONSTRAINT "uq:messaging_link_codes.code_hash" UNIQUE (code_hash);

ALTER TABLE ONLY messaging_links
    ADD CONSTRAINT "uq:messaging_links.platform+messaging_links.external_id" UNIQUE (platform, external_id);

ALTER TABLE ONLY messaging_preferences
    ADD CONSTRAINT "uq:messaging_preferences.user_id+messaging_preferences.platform" UNIQUE (user_id, platform, kind);

ALTER TABLE ONLY net_worth_forecasts
    ADD CONSTRAINT "uq:net_worth_forecasts.user_id+net_worth_forecasts.portfolio_li" UNIQUE (user_id, portfolio_list_id);

ALTER TABLE ONLY net_worth_snapshots
    ADD CONSTRAINT "uq:net_worth_snapshots.dedupe_key" UNIQUE (dedupe_key);

ALTER TABLE ONLY news_ticker_feed_subscriptions
    ADD CONSTRAINT "uq:news_ticker_feed_subscriptions.user_id+news_ticker_feed_subs" UNIQUE (user_id, feed_url);

ALTER TABLE ONLY news_ticker_preferences
    ADD CONSTRAINT "uq:news_ticker_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT "uq:notification_deliveries.event_id+notification_deliveries.cha" UNIQUE (event_id, channel);

ALTER TABLE ONLY notification_events
    ADD CONSTRAINT "uq:notification_events.user_id+notification_events.deduplicatio" UNIQUE (user_id, deduplication_key);

ALTER TABLE ONLY oauth_clients
    ADD CONSTRAINT "uq:oauth_clients.client_id" UNIQUE (client_id);

ALTER TABLE ONLY oauth_flows
    ADD CONSTRAINT "uq:oauth_flows.state" UNIQUE (state);

ALTER TABLE ONLY oauth_identities
    ADD CONSTRAINT "uq:oauth_identities.provider+oauth_identities.provider_user_id" UNIQUE (provider, provider_user_id);

ALTER TABLE ONLY oauth_tokens
    ADD CONSTRAINT "uq:oauth_tokens.access_token_hash" UNIQUE (access_token_hash);

ALTER TABLE ONLY oauth_tokens
    ADD CONSTRAINT "uq:oauth_tokens.refresh_token_hash" UNIQUE (refresh_token_hash);

ALTER TABLE ONLY personal_access_tokens
    ADD CONSTRAINT "uq:personal_access_tokens.token_hash" UNIQUE (token_hash);

ALTER TABLE ONLY planning_scenarios
    ADD CONSTRAINT "uq:planning_scenarios.user_id+planning_scenarios.name" UNIQUE (user_id, name);

ALTER TABLE ONLY portfolio_invitations
    ADD CONSTRAINT "uq:portfolio_invitations.token_hash" UNIQUE (token_hash);

ALTER TABLE ONLY portfolio_lists
    ADD CONSTRAINT "uq:portfolio_lists.user_id+portfolio_lists.name" UNIQUE (user_id, name);

ALTER TABLE ONLY portfolio_memberships
    ADD CONSTRAINT "uq:portfolio_memberships.portfolio_id+portfolio_memberships.use" UNIQUE (portfolio_id, user_id);

ALTER TABLE ONLY portfolio_share_links
    ADD CONSTRAINT "uq:portfolio_share_links.slug" UNIQUE (slug);

ALTER TABLE ONLY portfolio_simulation_legs
    ADD CONSTRAINT "uq:portfolio_simulation_legs.simulation_id+portfolio_simulation" UNIQUE (simulation_id, symbol);

ALTER TABLE ONLY portfolio_simulations
    ADD CONSTRAINT "uq:portfolio_simulations.share_slug" UNIQUE (share_slug);

ALTER TABLE ONLY portfolio_value_snapshots
    ADD CONSTRAINT "uq:portfolio_value_snapshots.user_id+portfolio_value_snapshots." UNIQUE (user_id, portfolio_list_id, captured_on);

ALTER TABLE ONLY positions
    ADD CONSTRAINT "uq:positions.account_id+positions.instrument_id" UNIQUE (account_id, instrument_id);

ALTER TABLE ONLY price_history
    ADD CONSTRAINT "uq:price_history.symbol+price_history.date" UNIQUE (symbol, date);

ALTER TABLE ONLY prices
    ADD CONSTRAINT "uq:prices.instrument_id+prices.date" UNIQUE (instrument_id, date);

ALTER TABLE ONLY profile_cache
    ADD CONSTRAINT "uq:profile_cache.provider+profile_cache.symbol" UNIQUE (provider, symbol);

ALTER TABLE ONLY push_devices
    ADD CONSTRAINT "uq:push_devices.device_token" UNIQUE (device_token);

ALTER TABLE ONLY quote_cache
    ADD CONSTRAINT "uq:quote_cache.provider+quote_cache.symbol" UNIQUE (provider, symbol);

ALTER TABLE ONLY ratios_cache
    ADD CONSTRAINT "uq:ratios_cache.provider+ratios_cache.symbol+ratios_cache.perio" UNIQUE (provider, symbol, period, "limit");

ALTER TABLE ONLY ratios_ttm_cache
    ADD CONSTRAINT "uq:ratios_ttm_cache.provider+ratios_ttm_cache.symbol" UNIQUE (provider, symbol);

ALTER TABLE ONLY rebalancing_alerts
    ADD CONSTRAINT "uq:rebalancing_alerts.active_scope_key" UNIQUE (active_scope_key);

ALTER TABLE ONLY rebalancing_notification_preferences
    ADD CONSTRAINT "uq:rebalancing_notification_preferences.portfolio_id+rebalancin" UNIQUE (portfolio_id, user_id);

ALTER TABLE ONLY rebalancing_policies
    ADD CONSTRAINT "uq:rebalancing_policies.user_id+rebalancing_policies.portfolio_" UNIQUE (user_id, portfolio_list_id);

ALTER TABLE ONLY recaps
    ADD CONSTRAINT "uq:recaps.user_id+recaps.period_kind+recaps.period_key" UNIQUE (user_id, period_kind, period_key);

ALTER TABLE ONLY refresh_tokens
    ADD CONSTRAINT "uq:refresh_tokens.token_hash" UNIQUE (token_hash);

ALTER TABLE ONLY report_suggestion_dismissals
    ADD CONSTRAINT "uq:report_suggestion_dismissals.user_id+report_suggestion_dismi" UNIQUE (user_id, suggestion_id);

ALTER TABLE ONLY retirement_plans
    ADD CONSTRAINT "uq:retirement_plans.portfolio_id" UNIQUE (portfolio_id);

ALTER TABLE ONLY search_cache
    ADD CONSTRAINT "uq:search_cache.provider+search_cache.normalized_query" UNIQUE (provider, normalized_query);

ALTER TABLE ONLY sentiment_snapshots
    ADD CONSTRAINT "uq:sentiment_snapshots.dedupe_key" UNIQUE (dedupe_key);

ALTER TABLE ONLY sentiment_universe_symbols
    ADD CONSTRAINT "uq:sentiment_universe_symbols.symbol" UNIQUE (symbol);

ALTER TABLE ONLY social_blocks
    ADD CONSTRAINT "uq:social_blocks.blocker_id+social_blocks.blocked_id" UNIQUE (blocker_id, blocked_id);

ALTER TABLE ONLY social_friend_requests
    ADD CONSTRAINT "uq:social_friend_requests.from_user_id+social_friend_requests.t" UNIQUE (from_user_id, to_user_id);

ALTER TABLE ONLY social_friendships
    ADD CONSTRAINT "uq:social_friendships.user_id+social_friendships.friend_id" UNIQUE (user_id, friend_id);

ALTER TABLE ONLY social_invites
    ADD CONSTRAINT "uq:social_invites.code" UNIQUE (code);

ALTER TABLE ONLY social_invites
    ADD CONSTRAINT "uq:social_invites.user_id" UNIQUE (user_id);

ALTER TABLE ONLY social_settings
    ADD CONSTRAINT "uq:social_settings.user_id" UNIQUE (user_id);

ALTER TABLE ONLY statistics_snapshots
    ADD CONSTRAINT "uq:statistics_snapshots.user_id+statistics_snapshots.kind+stati" UNIQUE (user_id, kind, as_of_date);

ALTER TABLE ONLY stock_valuations
    ADD CONSTRAINT "uq:stock_valuations.user_id+stock_valuations.symbol" UNIQUE (user_id, symbol);

ALTER TABLE ONLY subscriptions
    ADD CONSTRAINT "uq:subscriptions.provider+subscriptions.provider_original_trans" UNIQUE (provider, provider_original_transaction_id);

ALTER TABLE ONLY symbol_sentiment_daily
    ADD CONSTRAINT "uq:symbol_sentiment_daily.symbol+symbol_sentiment_daily.as_of_d" UNIQUE (symbol, as_of_date, window_days);

ALTER TABLE ONLY tax_action_plans
    ADD CONSTRAINT "uq:tax_action_plans.user_id+tax_action_plans.idempotency_key" UNIQUE (user_id, idempotency_key);

ALTER TABLE ONLY tax_action_rebalancing_plans
    ADD CONSTRAINT "uq:tax_action_rebalancing_plans.action_plan_id+tax_action_rebal" UNIQUE (action_plan_id, rebalancing_plan_id);

ALTER TABLE ONLY tax_loss_carryforward_applications
    ADD CONSTRAINT "uq:tax_loss_carryforward_applications.carryforward_id+tax_loss_" UNIQUE (carryforward_id, target_tax_year);

ALTER TABLE ONLY tax_loss_carryforwards
    ADD CONSTRAINT "uq:tax_loss_carryforwards.user_id+tax_loss_carryforwards.jurisd" UNIQUE (user_id, jurisdiction, source_tax_year);

ALTER TABLE ONLY tax_notification_preferences
    ADD CONSTRAINT "uq:tax_notification_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY tax_opportunity_decisions
    ADD CONSTRAINT "uq:tax_opportunity_decisions.user_id+tax_opportunity_decisions." UNIQUE (user_id, tax_year, opportunity_id);

ALTER TABLE ONLY tax_pack_entitlements
    ADD CONSTRAINT "uq:tax_pack_entitlements.user_id+tax_pack_entitlements.tax_year" UNIQUE (user_id, tax_year);

ALTER TABLE ONLY tax_profiles
    ADD CONSTRAINT "uq:tax_profiles.user_id+tax_profiles.jurisdiction+tax_profiles." UNIQUE (user_id, jurisdiction, tax_year);

ALTER TABLE ONLY tax_projection_jobs
    ADD CONSTRAINT "uq:tax_projection_jobs.idempotency_key" UNIQUE (idempotency_key);

ALTER TABLE ONLY tax_restriction_windows
    ADD CONSTRAINT "uq:tax_restriction_windows.action_leg_id" UNIQUE (action_leg_id);

ALTER TABLE ONLY thesis_watch_notification_preferences
    ADD CONSTRAINT "uq:thesis_watch_notification_preferences.user_id" UNIQUE (user_id);

ALTER TABLE ONLY thesis_watch_stories
    ADD CONSTRAINT "uq:thesis_watch_stories.cluster_key" UNIQUE (cluster_key);

ALTER TABLE ONLY thesis_watch_user_states
    ADD CONSTRAINT "uq:thesis_watch_user_states.user_id+thesis_watch_user_states.st" UNIQUE (user_id, story_id);

ALTER TABLE ONLY ticker_sentiment_posts
    ADD CONSTRAINT "uq:ticker_sentiment_posts.dedupe_key" UNIQUE (dedupe_key);

ALTER TABLE ONLY transactions
    ADD CONSTRAINT "uq:transactions.account_id+transactions.external_id" UNIQUE (account_id, external_id);

ALTER TABLE ONLY trial_warnings
    ADD CONSTRAINT "uq:trial_warnings.user_id+trial_warnings.warning_type" UNIQUE (user_id, warning_type);

ALTER TABLE ONLY usage_counters
    ADD CONSTRAINT "uq:usage_counters.user_id" UNIQUE (user_id);

ALTER TABLE ONLY user_ai_provider_credentials
    ADD CONSTRAINT "uq:user_ai_provider_credentials.user_id+user_ai_provider_creden" UNIQUE (user_id, provider);

ALTER TABLE ONLY user_badges
    ADD CONSTRAINT "uq:user_badges.user_id+user_badges.badge_type+user_badges.tier" UNIQUE (user_id, badge_type, tier);

ALTER TABLE ONLY users
    ADD CONSTRAINT "uq:users.email" UNIQUE (email);

ALTER TABLE ONLY users
    ADD CONSTRAINT "uq:users.username" UNIQUE (username);

ALTER TABLE ONLY wash_sale_matches
    ADD CONSTRAINT "uq:wash_sale_matches.disposal_id+wash_sale_matches.replacement_" UNIQUE (disposal_id, replacement_lot_id);

ALTER TABLE ONLY watchlist_items
    ADD CONSTRAINT "uq:watchlist_items.user_id+watchlist_items.symbol" UNIQUE (user_id, symbol);

ALTER TABLE ONLY watchlist_lists
    ADD CONSTRAINT "uq:watchlist_lists.user_id+watchlist_lists.name" UNIQUE (user_id, name);

ALTER TABLE ONLY webauthn_credentials
    ADD CONSTRAINT "uq:webauthn_credentials.credential_id" UNIQUE (credential_id);

ALTER TABLE ONLY usage_counters
    ADD CONSTRAINT usage_counters_pkey PRIMARY KEY (id);

ALTER TABLE ONLY user_activities
    ADD CONSTRAINT user_activities_pkey PRIMARY KEY (id);

ALTER TABLE ONLY user_ai_provider_credentials
    ADD CONSTRAINT user_ai_provider_credentials_pkey PRIMARY KEY (id);

ALTER TABLE ONLY user_badges
    ADD CONSTRAINT user_badges_pkey PRIMARY KEY (id);

ALTER TABLE ONLY users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY wash_sale_matches
    ADD CONSTRAINT wash_sale_matches_pkey PRIMARY KEY (id);

ALTER TABLE ONLY watchlist_items
    ADD CONSTRAINT watchlist_items_pkey PRIMARY KEY (id);

ALTER TABLE ONLY watchlist_lists
    ADD CONSTRAINT watchlist_lists_pkey PRIMARY KEY (id);

ALTER TABLE ONLY watchlist_screen_evaluations
    ADD CONSTRAINT watchlist_screen_evaluations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY watchlist_screens
    ADD CONSTRAINT watchlist_screens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY webauthn_credentials
    ADD CONSTRAINT webauthn_credentials_pkey PRIMARY KEY (id);

ALTER TABLE ONLY webauthn_login_challenges
    ADD CONSTRAINT webauthn_login_challenges_pkey PRIMARY KEY (id);

ALTER TABLE ONLY webauthn_register_challenges
    ADD CONSTRAINT webauthn_register_challenges_pkey PRIMARY KEY (id);

CREATE INDEX advanced_report_artifacts_expiry_idx ON advanced_report_artifacts USING btree (expires_at);

CREATE INDEX advanced_report_runs_status_retry_idx ON advanced_report_runs USING btree (status, retry_at, created_at);

CREATE INDEX advanced_report_schedules_due_idx ON advanced_report_schedules USING btree (next_run_at) WHERE (paused_reason IS NULL);

CREATE INDEX ai_action_audits_pending_created_idx ON ai_action_audits USING btree (pending_action_id, created_at);

CREATE INDEX ai_conversations_user_updated_idx ON ai_conversations USING btree (user_id, updated_at);

CREATE INDEX ai_messages_conversation_created_idx ON ai_messages USING btree (conversation_id, created_at);

CREATE INDEX ai_pending_actions_user_status_idx ON ai_pending_actions USING btree (user_id, status);

CREATE INDEX ai_tips_user_created_idx ON ai_tips USING btree (user_id, created_at);

CREATE INDEX allocation_models_portfolio_active_idx ON allocation_models USING btree (portfolio_id, is_active);

CREATE INDEX article_images_owner_idx ON article_images USING btree (owner_id, created_at);

CREATE INDEX articles_author_idx ON articles USING btree (author_id, published_at DESC);

CREATE INDEX articles_cover_idx ON articles USING btree (cover_image_id) WHERE (cover_image_id IS NOT NULL);

CREATE INDEX articles_feed_idx ON articles USING btree (status, published_at DESC, id DESC);

CREATE INDEX articles_tickers_idx ON articles USING gin (tickers);

CREATE INDEX assistant_watches_due_idx ON assistant_watches USING btree (next_run_at) WHERE enabled;

CREATE INDEX assistant_watches_user_idx ON assistant_watches USING btree (user_id);

CREATE UNIQUE INDEX board_posts_ai_trading_date_idx ON board_posts USING btree (trading_date) WHERE (ai_generated AND (deleted_at IS NULL));

CREATE INDEX expense_import_sessions_expires_idx ON expense_import_sessions USING btree (expires_at);

CREATE INDEX expense_import_sessions_user_created_idx ON expense_import_sessions USING btree (user_id, created_at);

CREATE INDEX idx_accounts_user_id ON accounts USING btree (user_id);

CREATE INDEX idx_autobuys_user_id ON autobuys USING btree (user_id);

CREATE INDEX idx_bank_accounts_connection_id ON bank_accounts USING btree (connection_id);

CREATE INDEX idx_bank_connections_user_id ON bank_connections USING btree (user_id);

CREATE INDEX idx_bank_transactions_user_id_status ON bank_transactions USING btree (user_id, status);

CREATE INDEX idx_billing_events_user_id ON billing_events USING btree (user_id);

CREATE INDEX idx_board_comments_post_id_author_id ON board_comments USING btree (post_id, author_id);

CREATE INDEX idx_board_comments_post_id_created_at ON board_comments USING btree (post_id, created_at);

CREATE INDEX idx_board_notifications_recipient_id_created_at_id ON board_notifications USING btree (recipient_id, created_at, id);

CREATE INDEX idx_board_notifications_unread ON board_notifications USING btree (recipient_id) WHERE (read_at IS NULL);

CREATE INDEX idx_board_posts_author_id_created_at ON board_posts USING btree (author_id, created_at);

CREATE INDEX idx_board_posts_board_id_created_at_id ON board_posts USING btree (board_id, created_at, id);

CREATE INDEX idx_board_posts_board_id_last_activity_at_id ON board_posts USING btree (board_id, last_activity_at, id);

CREATE INDEX idx_board_posts_tags ON board_posts USING gin (tags);

CREATE INDEX idx_boards_creator_id_created_at ON boards USING btree (creator_id, created_at);

CREATE INDEX idx_broker_connections_user_id ON broker_connections USING btree (user_id);

CREATE INDEX idx_broker_connections_user_id_updated_at ON broker_connections USING btree (user_id, updated_at);

CREATE INDEX idx_broker_oauth_flows_provider_expires_at ON broker_oauth_flows USING btree (provider, expires_at);

CREATE INDEX idx_broker_oauth_flows_user_id ON broker_oauth_flows USING btree (user_id);

CREATE INDEX idx_budget_drift_alert_states_user_id_level ON budget_drift_alert_states USING btree (user_id, level);

CREATE INDEX idx_budget_plan_items_user_snapshot ON budget_plan_items USING btree (user_id, snapshot_id);

CREATE INDEX idx_budget_reallocation_events_user_id_created_at ON budget_reallocation_events USING btree (user_id, created_at);

CREATE INDEX idx_budget_templates_user_id ON budget_templates USING btree (user_id);

CREATE INDEX idx_cash_balances_account_id ON cash_balances USING btree (account_id);

CREATE INDEX idx_community_sanctions_user_id_revoked_at ON community_sanctions USING btree (user_id, revoked_at);

CREATE INDEX idx_coupon_redemptions_code ON coupon_redemptions USING btree (code);

CREATE INDEX idx_coupon_redemptions_coupon_id ON coupon_redemptions USING btree (coupon_id);

CREATE INDEX idx_coupon_redemptions_user_id ON coupon_redemptions USING btree (user_id);

CREATE INDEX idx_crypto_portfolio_items_user_id_symbol ON crypto_portfolio_items USING btree (user_id, symbol);

CREATE INDEX idx_dividends_account_external_id ON dividends USING btree (account_id, external_id);

CREATE INDEX idx_earnings_notification_deliveries_user_id_earnings_date ON earnings_notification_deliveries USING btree (user_id, earnings_date);

CREATE INDEX idx_entitlements_user_id ON entitlements USING btree (user_id);

CREATE INDEX idx_expenses_user_id_occurred_on_category_id ON expenses USING btree (user_id, occurred_on, category_id);

CREATE INDEX idx_expenses_user_occurred_on ON expenses USING btree (user_id, occurred_on);

CREATE INDEX idx_financial_goals_user_id ON financial_goals USING btree (user_id);

CREATE INDEX idx_fx_rates_base_quote_date ON fx_rates USING btree (base, quote, date);

CREATE INDEX idx_gamification_xp_events_user_id_created_at ON gamification_xp_events USING btree (user_id, created_at);

CREATE INDEX idx_holding_risk_profiles_benchmark_proxy ON holding_risk_profiles USING btree (benchmark_proxy);

CREATE INDEX idx_insight_events_observed_at ON insight_events USING btree (observed_at);

CREATE INDEX idx_insight_events_topic ON insight_events USING btree (topic);

CREATE INDEX idx_insight_events_topic_observed_at ON insight_events USING btree (topic, observed_at);

CREATE INDEX idx_instruments_symbol ON instruments USING btree (symbol);

CREATE INDEX idx_lots_account_id_instrument_id ON lots USING btree (account_id, instrument_id);

CREATE INDEX idx_lots_status ON lots USING btree (status);

CREATE INDEX idx_macro_series_points_country_series_key_period_date ON macro_series_points USING btree (country, series_key, period_date);

CREATE INDEX idx_macro_series_points_country_series_key_vintage_date ON macro_series_points USING btree (country, series_key, vintage_date);

CREATE INDEX idx_macro_snapshots_country_fetched_at ON macro_snapshots USING btree (country, fetched_at);

CREATE INDEX idx_market_briefs_language_generated_at ON market_briefs USING btree (language, generated_at);

CREATE INDEX idx_market_news_archive_provider_symbol_fetched_at ON market_news_archive USING btree (provider, symbol, fetched_at);

CREATE INDEX idx_market_news_archive_provider_symbol_published_at ON market_news_archive USING btree (provider, symbol, published_at);

CREATE INDEX idx_market_news_archive_story ON market_news_archive USING btree (story_id);

CREATE INDEX idx_market_price_bars_date_brin ON ONLY market_price_bars USING brin (date);

CREATE INDEX idx_market_price_bars_lookup ON ONLY market_price_bars USING btree (instrument_key, date DESC);

CREATE INDEX idx_mfa_challenges_expires_at ON mfa_challenges USING btree (expires_at);

CREATE INDEX idx_mfa_challenges_user_id ON mfa_challenges USING btree (user_id);

CREATE INDEX idx_mfa_challenges_user_id_purpose_consumed_at ON mfa_challenges USING btree (user_id, purpose, consumed_at);

CREATE INDEX idx_net_worth_forecast_runs_user_id_created_at ON net_worth_forecast_runs USING btree (user_id, created_at);

CREATE INDEX idx_net_worth_snapshots_captured_at ON net_worth_snapshots USING btree (captured_at);

CREATE INDEX idx_news_items_user_id ON news_items USING btree (user_id);

CREATE INDEX idx_news_items_user_id_published_at ON news_items USING btree (user_id, published_at);

CREATE INDEX idx_news_items_user_id_symbol ON news_items USING btree (user_id, symbol);

CREATE INDEX idx_notification_events_user_id_created_at ON notification_events USING btree (user_id, created_at);

CREATE INDEX idx_notification_events_user_id_read_at ON notification_events USING btree (user_id, read_at);

CREATE INDEX idx_oauth_authorization_flows_code_hash ON oauth_authorization_flows USING btree (code_hash);

CREATE INDEX idx_oauth_flows_provider_expires_at ON oauth_flows USING btree (provider, expires_at);

CREATE INDEX idx_oauth_flows_user_id ON oauth_flows USING btree (user_id);

CREATE INDEX idx_oauth_identities_user_id ON oauth_identities USING btree (user_id);

CREATE INDEX idx_oauth_tokens_user_id ON oauth_tokens USING btree (user_id);

CREATE INDEX idx_password_reset_tokens_code_hash ON password_reset_tokens USING btree (code_hash);

CREATE INDEX idx_password_reset_tokens_expires_at ON password_reset_tokens USING btree (expires_at);

CREATE INDEX idx_password_reset_tokens_locked_until ON password_reset_tokens USING btree (locked_until);

CREATE INDEX idx_password_reset_tokens_user_id ON password_reset_tokens USING btree (user_id);

CREATE INDEX idx_personal_access_tokens_user_id ON personal_access_tokens USING btree (user_id);

CREATE INDEX idx_pilot_follows_portfolio_list_id ON pilot_follows USING btree (portfolio_list_id);

CREATE INDEX idx_pilot_follows_watchlist_list_id ON pilot_follows USING btree (watchlist_list_id);

CREATE INDEX idx_portfolio_lists_user_id_created_at ON portfolio_lists USING btree (user_id, created_at);

CREATE INDEX idx_portfolio_scenario_snapshots_user_id_created_at ON portfolio_scenario_snapshots USING btree (user_id, created_at);

CREATE INDEX idx_portfolio_share_links_user_id ON portfolio_share_links USING btree (user_id);

CREATE INDEX idx_portfolio_value_snapshots_user_id_portfolio_list_id_capture ON portfolio_value_snapshots USING btree (user_id, portfolio_list_id, captured_on);

CREATE INDEX idx_position_memos_user_id_bookmarked ON position_memos USING btree (user_id, bookmarked);

CREATE INDEX idx_positions_account_id ON positions USING btree (account_id);

CREATE INDEX idx_price_history_symbol_date ON price_history USING btree (symbol, date);

CREATE INDEX idx_prices_instrument_id_date ON prices USING btree (instrument_id, date);

CREATE INDEX idx_push_devices_user_id_is_active ON push_devices USING btree (user_id, is_active);

CREATE INDEX idx_quote_cache_provider_symbol_as_of ON quote_cache USING btree (provider, symbol, as_of);

CREATE INDEX idx_quote_cache_symbol_as_of ON quote_cache USING btree (symbol, as_of);

CREATE INDEX idx_rebalance_events_policy_id_status ON rebalance_events USING btree (policy_id, status);

CREATE INDEX idx_recaps_user_id ON recaps USING btree (user_id);

CREATE INDEX idx_refresh_tokens_expires_at ON refresh_tokens USING btree (expires_at);

CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens USING btree (user_id);

CREATE INDEX idx_research_notes_user_id_symbol ON research_notes USING btree (user_id, symbol);

CREATE INDEX idx_research_notes_user_id_updated_at ON research_notes USING btree (user_id, updated_at);

CREATE INDEX idx_scenario_definitions_user_id ON scenario_definitions USING btree (user_id);

CREATE INDEX idx_scenario_runs_deduplication_hash ON scenario_runs USING btree (deduplication_hash);

CREATE INDEX idx_scenario_runs_state_lease_expires_at ON scenario_runs USING btree (state, lease_expires_at);

CREATE INDEX idx_scenario_runs_user_id_created_at ON scenario_runs USING btree (user_id, created_at);

CREATE INDEX idx_search_cache_normalized_query ON search_cache USING btree (normalized_query);

CREATE INDEX idx_search_cache_provider_query_updated_at ON search_cache USING btree (provider, normalized_query, updated_at);

CREATE INDEX idx_sentiment_snapshots_scope_captured_at ON sentiment_snapshots USING btree (scope, captured_at);

CREATE INDEX idx_sentiment_universe_symbols_tier ON sentiment_universe_symbols USING btree (tier);

CREATE INDEX idx_social_blocks_blocked_id ON social_blocks USING btree (blocked_id);

CREATE INDEX idx_social_friend_requests_to_user_id ON social_friend_requests USING btree (to_user_id);

CREATE INDEX idx_social_friendships_friend_id ON social_friendships USING btree (friend_id);

CREATE INDEX idx_social_reports_status ON social_reports USING btree (status);

CREATE INDEX idx_social_settings_email_hash ON social_settings USING btree (email_hash);

CREATE INDEX idx_statistics_snapshots_user_kind_generated_at ON statistics_snapshots USING btree (user_id, kind, generated_at);

CREATE INDEX idx_stocks_source_account_id ON stocks USING btree (source_account_id);

CREATE INDEX idx_stocks_user_id_created_at ON stocks USING btree (user_id, created_at);

CREATE INDEX idx_stocks_user_id_source_provider_symbol ON stocks USING btree (user_id, source_provider, symbol);

CREATE INDEX idx_stocks_user_id_symbol ON stocks USING btree (user_id, symbol);

CREATE INDEX idx_stocks_user_list_created_at ON stocks USING btree (user_id, portfolio_list_id, created_at);

CREATE INDEX idx_stocks_user_list_symbol ON stocks USING btree (user_id, portfolio_list_id, symbol);

CREATE INDEX idx_subscriptions_provider_provider_original_transaction_id ON subscriptions USING btree (provider, provider_original_transaction_id);

CREATE INDEX idx_subscriptions_user_id ON subscriptions USING btree (user_id);

CREATE INDEX idx_symbol_sentiment_daily_as_of_date_score ON symbol_sentiment_daily USING btree (as_of_date, score);

CREATE INDEX idx_symbol_sentiment_daily_as_of_date_volume_z ON symbol_sentiment_daily USING btree (as_of_date, volume_z);

CREATE INDEX idx_symbol_sentiment_daily_symbol_as_of_date ON symbol_sentiment_daily USING btree (symbol, as_of_date);

CREATE INDEX idx_targets_user_id_symbol ON targets USING btree (user_id, symbol);

CREATE INDEX idx_targets_user_id_updated_at ON targets USING btree (user_id, updated_at);

CREATE INDEX idx_tax_action_legs_account_id_instrument_id_side_status ON tax_action_legs USING btree (account_id, instrument_id, side, status);

CREATE INDEX idx_tax_notification_deliveries_user_id_instrument_id_delivered ON tax_notification_deliveries USING btree (user_id, instrument_id, delivered_at);

CREATE INDEX idx_tax_projection_jobs_status_next_attempt_at ON tax_projection_jobs USING btree (status, next_attempt_at);

CREATE INDEX idx_tax_projection_snapshots_user_id_tax_year_generated_at ON tax_projection_snapshots USING btree (user_id, tax_year, generated_at);

CREATE INDEX idx_tax_restriction_windows_user_id_tax_identity_key_ends_at_st ON tax_restriction_windows USING btree (user_id, tax_identity_key, ends_at, status);

CREATE INDEX idx_terminal_positions_user_id_sort_order ON terminal_positions USING btree (user_id, sort_order);

CREATE INDEX idx_thesis_watch_stories_last_seen ON thesis_watch_stories USING btree (last_seen_at DESC);

CREATE INDEX idx_thesis_watch_user_states_user ON thesis_watch_user_states USING btree (user_id, dismissed_at);

CREATE INDEX idx_ticker_sentiment_posts_symbol_posted_at ON ticker_sentiment_posts USING btree (symbol, posted_at);

CREATE INDEX idx_ticker_sentiment_posts_symbol_source_posted_at ON ticker_sentiment_posts USING btree (symbol, source, posted_at);

CREATE INDEX idx_transactions_account_id_trade_date ON transactions USING btree (account_id, trade_date);

CREATE INDEX idx_transactions_instrument_id_trade_date ON transactions USING btree (instrument_id, trade_date);

CREATE INDEX idx_trial_warnings_user_id ON trial_warnings USING btree (user_id);

CREATE INDEX idx_usage_counters_user_id ON usage_counters USING btree (user_id);

CREATE UNIQUE INDEX idx_user_activities_unique_reference ON user_activities USING btree (user_id, type, reference_key) WHERE (reference_key IS NOT NULL);

CREATE INDEX idx_user_activities_user_id_created_at ON user_activities USING btree (user_id, created_at);

CREATE INDEX idx_user_ai_provider_credentials_user_id ON user_ai_provider_credentials USING btree (user_id);

CREATE INDEX idx_user_badges_user_id ON user_badges USING btree (user_id);

CREATE INDEX idx_users_username ON users USING btree (username);

CREATE INDEX idx_watchlist_items_user_id_created_at ON watchlist_items USING btree (user_id, created_at);

CREATE INDEX idx_watchlist_items_user_list_created_at ON watchlist_items USING btree (user_id, watchlist_list_id, created_at);

CREATE UNIQUE INDEX idx_watchlist_items_user_list_symbol_unique ON watchlist_items USING btree (user_id, watchlist_list_id, symbol);

CREATE INDEX idx_watchlist_lists_user_id_created_at ON watchlist_lists USING btree (user_id, created_at);

CREATE INDEX idx_watchlist_screen_evaluations_screen_id_evaluated_at ON watchlist_screen_evaluations USING btree (screen_id, evaluated_at);

CREATE INDEX idx_watchlist_screens_user_id ON watchlist_screens USING btree (user_id);

CREATE INDEX idx_webauthn_credentials_user_id ON webauthn_credentials USING btree (user_id);

CREATE INDEX idx_webauthn_login_challenges_expires_at ON webauthn_login_challenges USING btree (expires_at);

CREATE INDEX idx_webauthn_register_challenges_expires_at ON webauthn_register_challenges USING btree (expires_at);

CREATE INDEX idx_webauthn_register_challenges_user_id ON webauthn_register_challenges USING btree (user_id);

CREATE INDEX market_price_bars_1990_date_idx ON market_price_bars_1990 USING brin (date);

CREATE INDEX market_price_bars_1990_instrument_key_date_idx ON market_price_bars_1990 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1991_date_idx ON market_price_bars_1991 USING brin (date);

CREATE INDEX market_price_bars_1991_instrument_key_date_idx ON market_price_bars_1991 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1992_date_idx ON market_price_bars_1992 USING brin (date);

CREATE INDEX market_price_bars_1992_instrument_key_date_idx ON market_price_bars_1992 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1993_date_idx ON market_price_bars_1993 USING brin (date);

CREATE INDEX market_price_bars_1993_instrument_key_date_idx ON market_price_bars_1993 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1994_date_idx ON market_price_bars_1994 USING brin (date);

CREATE INDEX market_price_bars_1994_instrument_key_date_idx ON market_price_bars_1994 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1995_date_idx ON market_price_bars_1995 USING brin (date);

CREATE INDEX market_price_bars_1995_instrument_key_date_idx ON market_price_bars_1995 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1996_date_idx ON market_price_bars_1996 USING brin (date);

CREATE INDEX market_price_bars_1996_instrument_key_date_idx ON market_price_bars_1996 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1997_date_idx ON market_price_bars_1997 USING brin (date);

CREATE INDEX market_price_bars_1997_instrument_key_date_idx ON market_price_bars_1997 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1998_date_idx ON market_price_bars_1998 USING brin (date);

CREATE INDEX market_price_bars_1998_instrument_key_date_idx ON market_price_bars_1998 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_1999_date_idx ON market_price_bars_1999 USING brin (date);

CREATE INDEX market_price_bars_1999_instrument_key_date_idx ON market_price_bars_1999 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2000_date_idx ON market_price_bars_2000 USING brin (date);

CREATE INDEX market_price_bars_2000_instrument_key_date_idx ON market_price_bars_2000 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2001_date_idx ON market_price_bars_2001 USING brin (date);

CREATE INDEX market_price_bars_2001_instrument_key_date_idx ON market_price_bars_2001 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2002_date_idx ON market_price_bars_2002 USING brin (date);

CREATE INDEX market_price_bars_2002_instrument_key_date_idx ON market_price_bars_2002 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2003_date_idx ON market_price_bars_2003 USING brin (date);

CREATE INDEX market_price_bars_2003_instrument_key_date_idx ON market_price_bars_2003 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2004_date_idx ON market_price_bars_2004 USING brin (date);

CREATE INDEX market_price_bars_2004_instrument_key_date_idx ON market_price_bars_2004 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2005_date_idx ON market_price_bars_2005 USING brin (date);

CREATE INDEX market_price_bars_2005_instrument_key_date_idx ON market_price_bars_2005 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2006_date_idx ON market_price_bars_2006 USING brin (date);

CREATE INDEX market_price_bars_2006_instrument_key_date_idx ON market_price_bars_2006 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2007_date_idx ON market_price_bars_2007 USING brin (date);

CREATE INDEX market_price_bars_2007_instrument_key_date_idx ON market_price_bars_2007 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2008_date_idx ON market_price_bars_2008 USING brin (date);

CREATE INDEX market_price_bars_2008_instrument_key_date_idx ON market_price_bars_2008 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2009_date_idx ON market_price_bars_2009 USING brin (date);

CREATE INDEX market_price_bars_2009_instrument_key_date_idx ON market_price_bars_2009 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2010_date_idx ON market_price_bars_2010 USING brin (date);

CREATE INDEX market_price_bars_2010_instrument_key_date_idx ON market_price_bars_2010 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2011_date_idx ON market_price_bars_2011 USING brin (date);

CREATE INDEX market_price_bars_2011_instrument_key_date_idx ON market_price_bars_2011 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2012_date_idx ON market_price_bars_2012 USING brin (date);

CREATE INDEX market_price_bars_2012_instrument_key_date_idx ON market_price_bars_2012 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2013_date_idx ON market_price_bars_2013 USING brin (date);

CREATE INDEX market_price_bars_2013_instrument_key_date_idx ON market_price_bars_2013 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2014_date_idx ON market_price_bars_2014 USING brin (date);

CREATE INDEX market_price_bars_2014_instrument_key_date_idx ON market_price_bars_2014 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2015_date_idx ON market_price_bars_2015 USING brin (date);

CREATE INDEX market_price_bars_2015_instrument_key_date_idx ON market_price_bars_2015 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2016_date_idx ON market_price_bars_2016 USING brin (date);

CREATE INDEX market_price_bars_2016_instrument_key_date_idx ON market_price_bars_2016 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2017_date_idx ON market_price_bars_2017 USING brin (date);

CREATE INDEX market_price_bars_2017_instrument_key_date_idx ON market_price_bars_2017 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2018_date_idx ON market_price_bars_2018 USING brin (date);

CREATE INDEX market_price_bars_2018_instrument_key_date_idx ON market_price_bars_2018 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2019_date_idx ON market_price_bars_2019 USING brin (date);

CREATE INDEX market_price_bars_2019_instrument_key_date_idx ON market_price_bars_2019 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2020_date_idx ON market_price_bars_2020 USING brin (date);

CREATE INDEX market_price_bars_2020_instrument_key_date_idx ON market_price_bars_2020 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2021_date_idx ON market_price_bars_2021 USING brin (date);

CREATE INDEX market_price_bars_2021_instrument_key_date_idx ON market_price_bars_2021 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2022_date_idx ON market_price_bars_2022 USING brin (date);

CREATE INDEX market_price_bars_2022_instrument_key_date_idx ON market_price_bars_2022 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2023_date_idx ON market_price_bars_2023 USING brin (date);

CREATE INDEX market_price_bars_2023_instrument_key_date_idx ON market_price_bars_2023 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2024_date_idx ON market_price_bars_2024 USING brin (date);

CREATE INDEX market_price_bars_2024_instrument_key_date_idx ON market_price_bars_2024 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2025_date_idx ON market_price_bars_2025 USING brin (date);

CREATE INDEX market_price_bars_2025_instrument_key_date_idx ON market_price_bars_2025 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2026_date_idx ON market_price_bars_2026 USING brin (date);

CREATE INDEX market_price_bars_2026_instrument_key_date_idx ON market_price_bars_2026 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2027_date_idx ON market_price_bars_2027 USING brin (date);

CREATE INDEX market_price_bars_2027_instrument_key_date_idx ON market_price_bars_2027 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2028_date_idx ON market_price_bars_2028 USING brin (date);

CREATE INDEX market_price_bars_2028_instrument_key_date_idx ON market_price_bars_2028 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2029_date_idx ON market_price_bars_2029 USING brin (date);

CREATE INDEX market_price_bars_2029_instrument_key_date_idx ON market_price_bars_2029 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2030_date_idx ON market_price_bars_2030 USING brin (date);

CREATE INDEX market_price_bars_2030_instrument_key_date_idx ON market_price_bars_2030 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2031_date_idx ON market_price_bars_2031 USING brin (date);

CREATE INDEX market_price_bars_2031_instrument_key_date_idx ON market_price_bars_2031 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2032_date_idx ON market_price_bars_2032 USING brin (date);

CREATE INDEX market_price_bars_2032_instrument_key_date_idx ON market_price_bars_2032 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2033_date_idx ON market_price_bars_2033 USING brin (date);

CREATE INDEX market_price_bars_2033_instrument_key_date_idx ON market_price_bars_2033 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2034_date_idx ON market_price_bars_2034 USING brin (date);

CREATE INDEX market_price_bars_2034_instrument_key_date_idx ON market_price_bars_2034 USING btree (instrument_key, date DESC);

CREATE INDEX market_price_bars_2035_date_idx ON market_price_bars_2035 USING brin (date);

CREATE INDEX market_price_bars_2035_instrument_key_date_idx ON market_price_bars_2035 USING btree (instrument_key, date DESC);

CREATE INDEX messaging_link_codes_expires_at_idx ON messaging_link_codes USING btree (expires_at);

CREATE INDEX messaging_link_codes_user_id_idx ON messaging_link_codes USING btree (user_id);

CREATE INDEX messaging_links_user_id_idx ON messaging_links USING btree (user_id);

CREATE INDEX pilot_disclosures_pilot_date ON pilot_disclosures USING btree (pilot_id, transaction_date);

CREATE UNIQUE INDEX pilot_follows_target_unique ON pilot_follows USING btree (user_id, pilot_id, COALESCE(portfolio_list_id, watchlist_list_id));

CREATE INDEX portfolio_invitations_email_status_idx ON portfolio_invitations USING btree (lower(email), status);

CREATE INDEX portfolio_memberships_user_status_idx ON portfolio_memberships USING btree (user_id, status);

CREATE INDEX portfolio_simulation_legs_simulation_idx ON portfolio_simulation_legs USING btree (simulation_id, sort_order);

CREATE INDEX portfolio_simulations_share_slug_idx ON portfolio_simulations USING btree (share_slug);

CREATE INDEX portfolio_simulations_user_updated_idx ON portfolio_simulations USING btree (user_id, updated_at);

CREATE INDEX rebalance_plans_portfolio_created_idx ON rebalance_plans USING btree (portfolio_id, created_at);

CREATE INDEX rebalancing_alerts_user_status_idx ON rebalancing_alerts USING btree (user_id, status);

CREATE UNIQUE INDEX uq_board_notifications_upvote ON board_notifications USING btree (post_id, actor_id) WHERE (kind = 'upvote'::text);

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1990_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1990_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1990_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1990_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1991_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1991_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1991_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1991_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1992_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1992_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1992_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1992_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1993_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1993_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1993_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1993_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1994_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1994_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1994_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1994_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1995_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1995_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1995_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1995_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1996_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1996_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1996_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1996_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1997_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1997_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1997_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1997_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1998_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1998_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1998_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1998_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_1999_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_1999_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_1999_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_1999_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2000_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2000_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2000_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2000_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2001_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2001_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2001_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2001_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2002_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2002_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2002_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2002_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2003_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2003_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2003_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2003_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2004_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2004_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2004_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2004_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2005_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2005_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2005_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2005_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2006_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2006_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2006_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2006_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2007_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2007_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2007_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2007_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2008_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2008_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2008_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2008_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2009_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2009_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2009_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2009_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2010_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2010_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2010_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2010_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2011_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2011_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2011_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2011_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2012_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2012_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2012_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2012_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2013_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2013_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2013_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2013_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2014_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2014_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2014_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2014_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2015_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2015_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2015_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2015_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2016_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2016_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2016_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2016_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2017_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2017_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2017_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2017_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2018_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2018_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2018_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2018_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2019_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2019_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2019_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2019_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2020_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2020_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2020_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2020_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2021_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2021_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2021_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2021_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2022_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2022_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2022_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2022_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2023_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2023_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2023_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2023_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2024_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2024_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2024_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2024_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2025_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2025_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2025_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2025_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2026_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2026_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2026_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2026_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2027_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2027_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2027_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2027_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2028_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2028_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2028_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2028_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2029_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2029_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2029_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2029_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2030_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2030_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2030_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2030_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2031_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2031_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2031_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2031_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2032_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2032_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2032_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2032_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2033_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2033_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2033_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2033_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2034_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2034_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2034_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2034_pkey;

ALTER INDEX idx_market_price_bars_date_brin ATTACH PARTITION market_price_bars_2035_date_idx;

ALTER INDEX idx_market_price_bars_lookup ATTACH PARTITION market_price_bars_2035_instrument_key_date_idx;

ALTER INDEX market_price_bars_instrument_key_date_key ATTACH PARTITION market_price_bars_2035_instrument_key_date_key;

ALTER INDEX market_price_bars_pkey ATTACH PARTITION market_price_bars_2035_pkey;

ALTER TABLE ONLY accounts
    ADD CONSTRAINT accounts_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE RESTRICT;

ALTER TABLE ONLY accounts
    ADD CONSTRAINT accounts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY advanced_report_artifacts
    ADD CONSTRAINT advanced_report_artifacts_run_id_fkey FOREIGN KEY (run_id) REFERENCES advanced_report_runs(id) ON DELETE CASCADE;

ALTER TABLE ONLY advanced_report_deliveries
    ADD CONSTRAINT advanced_report_deliveries_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY advanced_report_deliveries
    ADD CONSTRAINT advanced_report_deliveries_run_id_fkey FOREIGN KEY (run_id) REFERENCES advanced_report_runs(id) ON DELETE CASCADE;

ALTER TABLE ONLY advanced_report_runs
    ADD CONSTRAINT advanced_report_runs_requested_by_user_id_fkey FOREIGN KEY (requested_by_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY advanced_report_runs
    ADD CONSTRAINT advanced_report_runs_schedule_id_fkey FOREIGN KEY (schedule_id) REFERENCES advanced_report_schedules(id) ON DELETE SET NULL;

ALTER TABLE ONLY advanced_report_runs
    ADD CONSTRAINT advanced_report_runs_template_id_fkey FOREIGN KEY (template_id) REFERENCES advanced_report_templates(id) ON DELETE RESTRICT;

ALTER TABLE ONLY advanced_report_schedules
    ADD CONSTRAINT advanced_report_schedules_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY advanced_report_templates
    ADD CONSTRAINT advanced_report_templates_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_action_audits
    ADD CONSTRAINT ai_action_audits_pending_action_id_fkey FOREIGN KEY (pending_action_id) REFERENCES ai_pending_actions(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_action_audits
    ADD CONSTRAINT ai_action_audits_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_conversations
    ADD CONSTRAINT ai_conversations_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_messages
    ADD CONSTRAINT ai_messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES ai_conversations(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_messages
    ADD CONSTRAINT ai_messages_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_pending_actions
    ADD CONSTRAINT ai_pending_actions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES ai_conversations(id) ON DELETE SET NULL;

ALTER TABLE ONLY ai_pending_actions
    ADD CONSTRAINT ai_pending_actions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_preferences
    ADD CONSTRAINT ai_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_tips
    ADD CONSTRAINT ai_tips_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY ai_usage_monthly
    ADD CONSTRAINT ai_usage_monthly_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY allocation_buckets
    ADD CONSTRAINT allocation_buckets_model_id_fkey FOREIGN KEY (model_id) REFERENCES allocation_models(id) ON DELETE CASCADE;

ALTER TABLE ONLY allocation_leaves
    ADD CONSTRAINT allocation_leaves_bucket_id_fkey FOREIGN KEY (bucket_id) REFERENCES allocation_buckets(id) ON DELETE CASCADE;

ALTER TABLE ONLY allocation_leaves
    ADD CONSTRAINT allocation_leaves_model_id_fkey FOREIGN KEY (model_id) REFERENCES allocation_models(id) ON DELETE CASCADE;

ALTER TABLE ONLY allocation_models
    ADD CONSTRAINT allocation_models_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE RESTRICT;

ALTER TABLE ONLY allocation_models
    ADD CONSTRAINT allocation_models_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY article_images
    ADD CONSTRAINT article_images_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY article_views
    ADD CONSTRAINT article_views_article_id_fkey FOREIGN KEY (article_id) REFERENCES articles(id) ON DELETE CASCADE;

ALTER TABLE ONLY article_votes
    ADD CONSTRAINT article_votes_article_id_fkey FOREIGN KEY (article_id) REFERENCES articles(id) ON DELETE CASCADE;

ALTER TABLE ONLY article_votes
    ADD CONSTRAINT article_votes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY articles
    ADD CONSTRAINT articles_author_id_fkey FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY articles
    ADD CONSTRAINT articles_cover_image_id_fkey FOREIGN KEY (cover_image_id) REFERENCES article_images(id) ON DELETE SET NULL;

ALTER TABLE ONLY assistant_watches
    ADD CONSTRAINT assistant_watches_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES ai_conversations(id) ON DELETE CASCADE;

ALTER TABLE ONLY assistant_watches
    ADD CONSTRAINT assistant_watches_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY autobuys
    ADD CONSTRAINT autobuys_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY bank_accounts
    ADD CONSTRAINT bank_accounts_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES bank_connections(id) ON DELETE CASCADE;

ALTER TABLE ONLY bank_connections
    ADD CONSTRAINT bank_connections_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY bank_link_flows
    ADD CONSTRAINT bank_link_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY bank_transactions
    ADD CONSTRAINT bank_transactions_account_id_fkey FOREIGN KEY (account_id) REFERENCES bank_accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY bank_transactions
    ADD CONSTRAINT bank_transactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY billing_events
    ADD CONSTRAINT billing_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE ONLY board_comments
    ADD CONSTRAINT board_comments_author_id_fkey FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_comments
    ADD CONSTRAINT board_comments_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES board_comments(id) ON DELETE SET NULL;

ALTER TABLE ONLY board_comments
    ADD CONSTRAINT board_comments_post_id_fkey FOREIGN KEY (post_id) REFERENCES board_posts(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_notification_settings
    ADD CONSTRAINT board_notification_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_notifications
    ADD CONSTRAINT board_notifications_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE ONLY board_notifications
    ADD CONSTRAINT board_notifications_comment_id_fkey FOREIGN KEY (comment_id) REFERENCES board_comments(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_notifications
    ADD CONSTRAINT board_notifications_post_id_fkey FOREIGN KEY (post_id) REFERENCES board_posts(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_notifications
    ADD CONSTRAINT board_notifications_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_post_views
    ADD CONSTRAINT board_post_views_post_id_fkey FOREIGN KEY (post_id) REFERENCES board_posts(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_post_views
    ADD CONSTRAINT board_post_views_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_posts
    ADD CONSTRAINT board_posts_author_id_fkey FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_posts
    ADD CONSTRAINT board_posts_board_id_fkey FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_votes
    ADD CONSTRAINT board_votes_post_id_fkey FOREIGN KEY (post_id) REFERENCES board_posts(id) ON DELETE CASCADE;

ALTER TABLE ONLY board_votes
    ADD CONSTRAINT board_votes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY boards
    ADD CONSTRAINT boards_creator_id_fkey FOREIGN KEY (creator_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE ONLY broker_connections
    ADD CONSTRAINT broker_connections_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY broker_oauth_flows
    ADD CONSTRAINT broker_oauth_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_drift_alert_states
    ADD CONSTRAINT budget_drift_alert_states_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES budget_snapshots(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_drift_alert_states
    ADD CONSTRAINT budget_drift_alert_states_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_plan_items
    ADD CONSTRAINT budget_plan_items_category_id_fkey FOREIGN KEY (category_id) REFERENCES expense_categories(id) ON DELETE SET NULL;

ALTER TABLE ONLY budget_plan_items
    ADD CONSTRAINT budget_plan_items_destination_financial_goal_id_fkey FOREIGN KEY (destination_financial_goal_id) REFERENCES financial_goals(id) ON DELETE SET NULL;

ALTER TABLE ONLY budget_plan_items
    ADD CONSTRAINT budget_plan_items_destination_portfolio_list_id_fkey FOREIGN KEY (destination_portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE SET NULL;

ALTER TABLE ONLY budget_plan_items
    ADD CONSTRAINT budget_plan_items_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES budget_snapshots(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_plan_items
    ADD CONSTRAINT budget_plan_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT budget_reallocation_events_financial_goal_id_fkey FOREIGN KEY (financial_goal_id) REFERENCES financial_goals(id) ON DELETE SET NULL;

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT budget_reallocation_events_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE SET NULL;

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT budget_reallocation_events_source_snapshot_id_fkey FOREIGN KEY (source_snapshot_id) REFERENCES budget_snapshots(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT budget_reallocation_events_target_snapshot_id_fkey FOREIGN KEY (target_snapshot_id) REFERENCES budget_snapshots(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_reallocation_events
    ADD CONSTRAINT budget_reallocation_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_snapshots
    ADD CONSTRAINT budget_snapshots_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY budget_templates
    ADD CONSTRAINT budget_templates_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY cash_balances
    ADD CONSTRAINT cash_balances_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY community_guidelines_acceptances
    ADD CONSTRAINT community_guidelines_acceptances_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY community_sanctions
    ADD CONSTRAINT community_sanctions_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE ONLY community_sanctions
    ADD CONSTRAINT community_sanctions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_coupon_id_fkey FOREIGN KEY (coupon_id) REFERENCES coupons(id) ON DELETE CASCADE;

ALTER TABLE ONLY coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY crypto_portfolio_items
    ADD CONSTRAINT crypto_portfolio_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY daily_board_post_runs
    ADD CONSTRAINT daily_board_post_runs_post_id_fkey FOREIGN KEY (post_id) REFERENCES board_posts(id) ON DELETE SET NULL;

ALTER TABLE ONLY data_exports
    ADD CONSTRAINT data_exports_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY earnings_notification_deliveries
    ADD CONSTRAINT earnings_notification_deliveries_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY earnings_notification_preferences
    ADD CONSTRAINT earnings_notification_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY entitlements
    ADD CONSTRAINT entitlements_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE SET NULL;

ALTER TABLE ONLY entitlements
    ADD CONSTRAINT entitlements_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY expense_categories
    ADD CONSTRAINT expense_categories_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY expense_import_sessions
    ADD CONSTRAINT expense_import_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY expenses
    ADD CONSTRAINT expenses_category_id_fkey FOREIGN KEY (category_id) REFERENCES expense_categories(id) ON DELETE SET NULL;

ALTER TABLE ONLY expenses
    ADD CONSTRAINT expenses_linked_item_id_fkey FOREIGN KEY (linked_item_id) REFERENCES budget_plan_items(id) ON DELETE SET NULL;

ALTER TABLE ONLY expenses
    ADD CONSTRAINT expenses_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY feedbacks
    ADD CONSTRAINT feedbacks_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_adjustment_drafts
    ADD CONSTRAINT financial_goal_adjustment_drafts_suggestion_id_fkey FOREIGN KEY (suggestion_id) REFERENCES financial_goal_suggestions(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_adjustment_drafts
    ADD CONSTRAINT financial_goal_adjustment_drafts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_contributions
    ADD CONSTRAINT financial_goal_contributions_goal_id_fkey FOREIGN KEY (goal_id) REFERENCES financial_goals(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_contributions
    ADD CONSTRAINT financial_goal_contributions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_expense_category_links
    ADD CONSTRAINT financial_goal_expense_category_links_category_id_fkey FOREIGN KEY (category_id) REFERENCES expense_categories(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_expense_category_links
    ADD CONSTRAINT financial_goal_expense_category_links_goal_id_fkey FOREIGN KEY (goal_id) REFERENCES financial_goals(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_expense_category_links
    ADD CONSTRAINT financial_goal_expense_category_links_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_portfolio_allocations
    ADD CONSTRAINT financial_goal_portfolio_allocations_goal_id_fkey FOREIGN KEY (goal_id) REFERENCES financial_goals(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_portfolio_allocations
    ADD CONSTRAINT financial_goal_portfolio_allocations_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_portfolio_allocations
    ADD CONSTRAINT financial_goal_portfolio_allocations_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_progress_snapshots
    ADD CONSTRAINT financial_goal_progress_snapshots_goal_id_fkey FOREIGN KEY (goal_id) REFERENCES financial_goals(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_progress_snapshots
    ADD CONSTRAINT financial_goal_progress_snapshots_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_suggestions
    ADD CONSTRAINT financial_goal_suggestions_goal_id_fkey FOREIGN KEY (goal_id) REFERENCES financial_goals(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goal_suggestions
    ADD CONSTRAINT financial_goal_suggestions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goals
    ADD CONSTRAINT financial_goals_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY financial_goals
    ADD CONSTRAINT financial_goals_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financing_affordability_assumptions
    ADD CONSTRAINT financing_affordability_assumptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY financing_expense_matches
    ADD CONSTRAINT financing_expense_matches_expense_id_fkey FOREIGN KEY (expense_id) REFERENCES expenses(id) ON DELETE CASCADE;

ALTER TABLE ONLY financing_expense_matches
    ADD CONSTRAINT financing_expense_matches_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES financing_plans(id) ON DELETE CASCADE;

ALTER TABLE ONLY financing_plan_revisions
    ADD CONSTRAINT financing_plan_revisions_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES financing_plans(id) ON DELETE CASCADE;

ALTER TABLE ONLY financing_plans
    ADD CONSTRAINT financing_plans_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY gamification_budget_streaks
    ADD CONSTRAINT gamification_budget_streaks_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY gamification_check_ins
    ADD CONSTRAINT gamification_check_ins_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY gamification_xp_events
    ADD CONSTRAINT gamification_xp_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_fund_advance_allocations
    ADD CONSTRAINT germany_fund_advance_allocations_annual_holding_id_fkey FOREIGN KEY (annual_holding_id) REFERENCES germany_fund_annual_holdings(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_fund_advance_allocations
    ADD CONSTRAINT germany_fund_advance_allocations_disposal_id_fkey FOREIGN KEY (disposal_id) REFERENCES lot_disposals(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_fund_annual_holdings
    ADD CONSTRAINT germany_fund_annual_holdings_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_fund_annual_holdings
    ADD CONSTRAINT germany_fund_annual_holdings_instrument_id_fkey FOREIGN KEY (instrument_id) REFERENCES instruments(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_fund_annual_holdings
    ADD CONSTRAINT germany_fund_annual_holdings_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES lots(id) ON DELETE SET NULL;

ALTER TABLE ONLY germany_fund_annual_holdings
    ADD CONSTRAINT germany_fund_annual_holdings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_general_loss_applications
    ADD CONSTRAINT germany_general_loss_applications_source_year_id_fkey FOREIGN KEY (source_year_id) REFERENCES germany_general_loss_years(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_general_loss_years
    ADD CONSTRAINT germany_general_loss_years_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_stock_loss_applications
    ADD CONSTRAINT germany_stock_loss_applications_source_year_id_fkey FOREIGN KEY (source_year_id) REFERENCES germany_stock_loss_years(id) ON DELETE CASCADE;

ALTER TABLE ONLY germany_stock_loss_years
    ADD CONSTRAINT germany_stock_loss_years_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY goals
    ADD CONSTRAINT goals_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY holding_risk_profiles
    ADD CONSTRAINT holding_risk_profiles_holding_id_fkey FOREIGN KEY (holding_id) REFERENCES stocks(id) ON DELETE CASCADE;

ALTER TABLE ONLY holding_risk_profiles
    ADD CONSTRAINT holding_risk_profiles_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY lot_adjustments
    ADD CONSTRAINT lot_adjustments_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES lots(id) ON DELETE CASCADE;

ALTER TABLE ONLY lot_adjustments
    ADD CONSTRAINT lot_adjustments_source_transaction_id_fkey FOREIGN KEY (source_transaction_id) REFERENCES transactions(id) ON DELETE SET NULL;

ALTER TABLE ONLY lot_disposals
    ADD CONSTRAINT lot_disposals_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES lots(id) ON DELETE CASCADE;

ALTER TABLE ONLY lot_disposals
    ADD CONSTRAINT lot_disposals_transaction_id_fkey FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE;

ALTER TABLE ONLY lots
    ADD CONSTRAINT lots_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY lots
    ADD CONSTRAINT lots_close_transaction_id_fkey FOREIGN KEY (close_transaction_id) REFERENCES transactions(id) ON DELETE SET NULL;

ALTER TABLE ONLY lots
    ADD CONSTRAINT lots_instrument_id_fkey FOREIGN KEY (instrument_id) REFERENCES instruments(id) ON DELETE CASCADE;

ALTER TABLE ONLY lots
    ADD CONSTRAINT lots_open_transaction_id_fkey FOREIGN KEY (open_transaction_id) REFERENCES transactions(id) ON DELETE SET NULL;

ALTER TABLE ONLY market_brief_notification_preferences
    ADD CONSTRAINT market_brief_notification_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY market_news_archive
    ADD CONSTRAINT market_news_archive_story_id_fkey FOREIGN KEY (story_id) REFERENCES thesis_watch_stories(id) ON DELETE SET NULL;

ALTER TABLE ONLY messaging_link_codes
    ADD CONSTRAINT messaging_link_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY messaging_links
    ADD CONSTRAINT messaging_links_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES ai_conversations(id) ON DELETE SET NULL;

ALTER TABLE ONLY messaging_links
    ADD CONSTRAINT messaging_links_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY messaging_preferences
    ADD CONSTRAINT messaging_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY mfa_challenges
    ADD CONSTRAINT mfa_challenges_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY net_worth_forecast_runs
    ADD CONSTRAINT net_worth_forecast_runs_forecast_id_fkey FOREIGN KEY (forecast_id) REFERENCES net_worth_forecasts(id) ON DELETE CASCADE;

ALTER TABLE ONLY net_worth_forecast_runs
    ADD CONSTRAINT net_worth_forecast_runs_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY net_worth_forecasts
    ADD CONSTRAINT net_worth_forecasts_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY net_worth_forecasts
    ADD CONSTRAINT net_worth_forecasts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY news_items
    ADD CONSTRAINT news_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY news_ticker_feed_subscriptions
    ADD CONSTRAINT news_ticker_feed_subscriptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY news_ticker_preferences
    ADD CONSTRAINT news_ticker_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_event_id_fkey FOREIGN KEY (event_id) REFERENCES notification_events(id) ON DELETE CASCADE;

ALTER TABLE ONLY notification_events
    ADD CONSTRAINT notification_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY oauth_authorization_flows
    ADD CONSTRAINT oauth_authorization_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY oauth_flows
    ADD CONSTRAINT oauth_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY oauth_identities
    ADD CONSTRAINT oauth_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY oauth_tokens
    ADD CONSTRAINT oauth_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY onboarding_state
    ADD CONSTRAINT onboarding_state_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY personal_access_tokens
    ADD CONSTRAINT personal_access_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_book_versions
    ADD CONSTRAINT pilot_book_versions_pilot_id_fkey FOREIGN KEY (pilot_id) REFERENCES pilots(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_disclosures
    ADD CONSTRAINT pilot_disclosures_pilot_id_fkey FOREIGN KEY (pilot_id) REFERENCES pilots(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_follow_events
    ADD CONSTRAINT pilot_follow_events_follow_id_fkey FOREIGN KEY (follow_id) REFERENCES pilot_follows(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_follow_snapshots
    ADD CONSTRAINT pilot_follow_snapshots_follow_id_fkey FOREIGN KEY (follow_id) REFERENCES pilot_follows(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_follows
    ADD CONSTRAINT pilot_follows_pilot_id_fkey FOREIGN KEY (pilot_id) REFERENCES pilots(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_follows
    ADD CONSTRAINT pilot_follows_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_follows
    ADD CONSTRAINT pilot_follows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY pilot_follows
    ADD CONSTRAINT pilot_follows_watchlist_list_id_fkey FOREIGN KEY (watchlist_list_id) REFERENCES watchlist_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY planning_scenarios
    ADD CONSTRAINT planning_scenarios_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_cash_positions
    ADD CONSTRAINT portfolio_cash_positions_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_invitations
    ADD CONSTRAINT portfolio_invitations_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_lists
    ADD CONSTRAINT portfolio_lists_source_portfolio_id_fkey FOREIGN KEY (source_portfolio_id) REFERENCES portfolio_lists(id) ON DELETE SET NULL;

ALTER TABLE ONLY portfolio_lists
    ADD CONSTRAINT portfolio_lists_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_memberships
    ADD CONSTRAINT portfolio_memberships_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_memberships
    ADD CONSTRAINT portfolio_memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_scenario_snapshots
    ADD CONSTRAINT portfolio_scenario_snapshots_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_scenario_snapshots
    ADD CONSTRAINT portfolio_scenario_snapshots_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_share_links
    ADD CONSTRAINT portfolio_share_links_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_share_links
    ADD CONSTRAINT portfolio_share_links_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_simulation_legs
    ADD CONSTRAINT portfolio_simulation_legs_simulation_id_fkey FOREIGN KEY (simulation_id) REFERENCES portfolio_simulations(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_simulations
    ADD CONSTRAINT portfolio_simulations_source_portfolio_id_fkey FOREIGN KEY (source_portfolio_id) REFERENCES portfolio_lists(id) ON DELETE SET NULL;

ALTER TABLE ONLY portfolio_simulations
    ADD CONSTRAINT portfolio_simulations_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY portfolio_value_snapshots
    ADD CONSTRAINT portfolio_value_snapshots_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY position_memos
    ADD CONSTRAINT position_memos_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY positions
    ADD CONSTRAINT positions_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY positions
    ADD CONSTRAINT positions_instrument_id_fkey FOREIGN KEY (instrument_id) REFERENCES instruments(id) ON DELETE CASCADE;

ALTER TABLE ONLY prices
    ADD CONSTRAINT prices_instrument_id_fkey FOREIGN KEY (instrument_id) REFERENCES instruments(id) ON DELETE CASCADE;

ALTER TABLE ONLY push_devices
    ADD CONSTRAINT push_devices_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalance_events
    ADD CONSTRAINT rebalance_events_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES rebalancing_policies(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalance_events
    ADD CONSTRAINT rebalance_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalance_plans
    ADD CONSTRAINT rebalance_plans_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE RESTRICT;

ALTER TABLE ONLY rebalance_plans
    ADD CONSTRAINT rebalance_plans_model_id_fkey FOREIGN KEY (model_id) REFERENCES allocation_models(id) ON DELETE RESTRICT;

ALTER TABLE ONLY rebalance_plans
    ADD CONSTRAINT rebalance_plans_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_alerts
    ADD CONSTRAINT rebalancing_alerts_model_id_fkey FOREIGN KEY (model_id) REFERENCES allocation_models(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_alerts
    ADD CONSTRAINT rebalancing_alerts_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_alerts
    ADD CONSTRAINT rebalancing_alerts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_notification_preferences
    ADD CONSTRAINT rebalancing_notification_preferences_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_notification_preferences
    ADD CONSTRAINT rebalancing_notification_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_policies
    ADD CONSTRAINT rebalancing_policies_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY rebalancing_policies
    ADD CONSTRAINT rebalancing_policies_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY recaps
    ADD CONSTRAINT recaps_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY recurring_templates
    ADD CONSTRAINT recurring_templates_category_id_fkey FOREIGN KEY (category_id) REFERENCES expense_categories(id) ON DELETE SET NULL;

ALTER TABLE ONLY recurring_templates
    ADD CONSTRAINT recurring_templates_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY refresh_tokens
    ADD CONSTRAINT refresh_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY report_suggestion_dismissals
    ADD CONSTRAINT report_suggestion_dismissals_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY research_notes
    ADD CONSTRAINT research_notes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY retirement_plans
    ADD CONSTRAINT retirement_plans_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY scenario_definitions
    ADD CONSTRAINT scenario_definitions_financial_goal_id_fkey FOREIGN KEY (financial_goal_id) REFERENCES financial_goals(id) ON DELETE SET NULL;

ALTER TABLE ONLY scenario_definitions
    ADD CONSTRAINT scenario_definitions_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE CASCADE;

ALTER TABLE ONLY scenario_definitions
    ADD CONSTRAINT scenario_definitions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY scenario_runs
    ADD CONSTRAINT scenario_runs_scenario_id_fkey FOREIGN KEY (scenario_id) REFERENCES scenario_definitions(id) ON DELETE CASCADE;

ALTER TABLE ONLY scenario_runs
    ADD CONSTRAINT scenario_runs_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES portfolio_scenario_snapshots(id) ON DELETE RESTRICT;

ALTER TABLE ONLY scenario_runs
    ADD CONSTRAINT scenario_runs_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_blocks
    ADD CONSTRAINT social_blocks_blocked_id_fkey FOREIGN KEY (blocked_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_blocks
    ADD CONSTRAINT social_blocks_blocker_id_fkey FOREIGN KEY (blocker_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_facebook_friends
    ADD CONSTRAINT social_facebook_friends_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_friend_requests
    ADD CONSTRAINT social_friend_requests_from_user_id_fkey FOREIGN KEY (from_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_friend_requests
    ADD CONSTRAINT social_friend_requests_to_user_id_fkey FOREIGN KEY (to_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_friendships
    ADD CONSTRAINT social_friendships_friend_id_fkey FOREIGN KEY (friend_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_friendships
    ADD CONSTRAINT social_friendships_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_invites
    ADD CONSTRAINT social_invites_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_reports
    ADD CONSTRAINT social_reports_reporter_id_fkey FOREIGN KEY (reporter_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_settings
    ADD CONSTRAINT social_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY statistics_snapshots
    ADD CONSTRAINT statistics_snapshots_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY stock_valuations
    ADD CONSTRAINT stock_valuations_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY stocks
    ADD CONSTRAINT stocks_portfolio_list_id_fkey FOREIGN KEY (portfolio_list_id) REFERENCES portfolio_lists(id) ON DELETE RESTRICT;

ALTER TABLE ONLY stocks
    ADD CONSTRAINT stocks_source_account_id_fkey FOREIGN KEY (source_account_id) REFERENCES accounts(id) ON DELETE SET NULL;

ALTER TABLE ONLY stocks
    ADD CONSTRAINT stocks_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY subscriptions
    ADD CONSTRAINT subscriptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY targets
    ADD CONSTRAINT targets_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_action_legs
    ADD CONSTRAINT tax_action_legs_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_action_legs
    ADD CONSTRAINT tax_action_legs_action_plan_id_fkey FOREIGN KEY (action_plan_id) REFERENCES tax_action_plans(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_action_legs
    ADD CONSTRAINT tax_action_legs_instrument_id_fkey FOREIGN KEY (instrument_id) REFERENCES instruments(id) ON DELETE RESTRICT;

ALTER TABLE ONLY tax_action_legs
    ADD CONSTRAINT tax_action_legs_matched_transaction_id_fkey FOREIGN KEY (matched_transaction_id) REFERENCES transactions(id) ON DELETE SET NULL;

ALTER TABLE ONLY tax_action_legs
    ADD CONSTRAINT tax_action_legs_portfolio_id_fkey FOREIGN KEY (portfolio_id) REFERENCES portfolio_lists(id) ON DELETE SET NULL;

ALTER TABLE ONLY tax_action_plans
    ADD CONSTRAINT tax_action_plans_scenario_id_fkey FOREIGN KEY (scenario_id) REFERENCES tax_scenarios(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_action_plans
    ADD CONSTRAINT tax_action_plans_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_action_rebalancing_plans
    ADD CONSTRAINT tax_action_rebalancing_plans_action_plan_id_fkey FOREIGN KEY (action_plan_id) REFERENCES tax_action_plans(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_action_rebalancing_plans
    ADD CONSTRAINT tax_action_rebalancing_plans_rebalancing_plan_id_fkey FOREIGN KEY (rebalancing_plan_id) REFERENCES rebalance_plans(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_loss_carryforward_applications
    ADD CONSTRAINT tax_loss_carryforward_applications_carryforward_id_fkey FOREIGN KEY (carryforward_id) REFERENCES tax_loss_carryforwards(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_loss_carryforwards
    ADD CONSTRAINT tax_loss_carryforwards_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_notification_deliveries
    ADD CONSTRAINT tax_notification_deliveries_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_notification_preferences
    ADD CONSTRAINT tax_notification_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_opportunity_decisions
    ADD CONSTRAINT tax_opportunity_decisions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_pack_entitlements
    ADD CONSTRAINT tax_pack_entitlements_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_profiles
    ADD CONSTRAINT tax_profiles_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_projection_jobs
    ADD CONSTRAINT tax_projection_jobs_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_projection_snapshots
    ADD CONSTRAINT tax_projection_snapshots_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES tax_profiles(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_projection_snapshots
    ADD CONSTRAINT tax_projection_snapshots_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_reports
    ADD CONSTRAINT tax_reports_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_restriction_windows
    ADD CONSTRAINT tax_restriction_windows_action_leg_id_fkey FOREIGN KEY (action_leg_id) REFERENCES tax_action_legs(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_restriction_windows
    ADD CONSTRAINT tax_restriction_windows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_restriction_windows
    ADD CONSTRAINT tax_restriction_windows_violating_transaction_id_fkey FOREIGN KEY (violating_transaction_id) REFERENCES transactions(id) ON DELETE SET NULL;

ALTER TABLE ONLY tax_scenarios
    ADD CONSTRAINT tax_scenarios_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES tax_profiles(id) ON DELETE CASCADE;

ALTER TABLE ONLY tax_scenarios
    ADD CONSTRAINT tax_scenarios_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY terminal_positions
    ADD CONSTRAINT terminal_positions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY thesis_watch_notification_preferences
    ADD CONSTRAINT thesis_watch_notification_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY thesis_watch_stories
    ADD CONSTRAINT thesis_watch_stories_representative_news_id_fkey FOREIGN KEY (representative_news_id) REFERENCES market_news_archive(id) ON DELETE CASCADE;

ALTER TABLE ONLY thesis_watch_user_states
    ADD CONSTRAINT thesis_watch_user_states_story_id_fkey FOREIGN KEY (story_id) REFERENCES thesis_watch_stories(id) ON DELETE CASCADE;

ALTER TABLE ONLY thesis_watch_user_states
    ADD CONSTRAINT thesis_watch_user_states_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY transactions
    ADD CONSTRAINT transactions_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY transactions
    ADD CONSTRAINT transactions_instrument_id_fkey FOREIGN KEY (instrument_id) REFERENCES instruments(id) ON DELETE CASCADE;

ALTER TABLE ONLY trial_warnings
    ADD CONSTRAINT trial_warnings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY usage_counters
    ADD CONSTRAINT usage_counters_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY user_activities
    ADD CONSTRAINT user_activities_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY user_ai_provider_credentials
    ADD CONSTRAINT user_ai_provider_credentials_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY user_badges
    ADD CONSTRAINT user_badges_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY wash_sale_matches
    ADD CONSTRAINT wash_sale_matches_disposal_id_fkey FOREIGN KEY (disposal_id) REFERENCES lot_disposals(id) ON DELETE CASCADE;

ALTER TABLE ONLY wash_sale_matches
    ADD CONSTRAINT wash_sale_matches_replacement_lot_id_fkey FOREIGN KEY (replacement_lot_id) REFERENCES lots(id) ON DELETE CASCADE;

ALTER TABLE ONLY watchlist_items
    ADD CONSTRAINT watchlist_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY watchlist_items
    ADD CONSTRAINT watchlist_items_watchlist_list_id_fkey FOREIGN KEY (watchlist_list_id) REFERENCES watchlist_lists(id) ON DELETE RESTRICT;

ALTER TABLE ONLY watchlist_lists
    ADD CONSTRAINT watchlist_lists_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY watchlist_screen_evaluations
    ADD CONSTRAINT watchlist_screen_evaluations_screen_id_fkey FOREIGN KEY (screen_id) REFERENCES watchlist_screens(id) ON DELETE CASCADE;

ALTER TABLE ONLY watchlist_screen_evaluations
    ADD CONSTRAINT watchlist_screen_evaluations_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY watchlist_screens
    ADD CONSTRAINT watchlist_screens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY webauthn_credentials
    ADD CONSTRAINT webauthn_credentials_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY webauthn_register_challenges
    ADD CONSTRAINT webauthn_register_challenges_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;


INSERT INTO instruments VALUES ('a23ab130-4afe-4f27-8ec5-3526672b112e', 'tax-catalog-iusb', 'IUSB', 'NASDAQ', 'USD', 'iShares Core Total USD Bond Market ETF', '2026-10-28 19:27:15.64068+00', '2026-10-28 19:27:15.64068+00', 'etf', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO instruments VALUES ('1a0afbe1-786a-4292-b7a5-025035728ef3', 'tax-catalog-schd', 'SCHD', 'ARCA', 'USD', 'Schwab U.S. Dividend Equity ETF', '2026-10-28 19:27:15.644022+00', '2026-10-28 19:27:15.644022+00', 'etf', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO instruments VALUES ('c6e92908-f748-4354-bd89-3c6b7d30b806', 'tax-catalog-schg', 'SCHG', 'ARCA', 'USD', 'Schwab U.S. Large-Cap Growth ETF', '2026-10-28 19:27:15.648334+00', '2026-10-28 19:27:15.648334+00', 'etf', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO instruments VALUES ('e9875c85-09ee-4883-be32-601db070ebe7', 'tax-catalog-voo', 'VOO', 'ARCA', 'USD', 'Vanguard S&P 500 ETF', '2026-10-28 19:27:15.652129+00', '2026-10-28 19:27:15.652129+00', 'etf', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO instruments VALUES ('08b0de8d-a4cd-418d-bf59-5d6c4b47f1e8', 'tax-catalog-vti', 'VTI', 'ARCA', 'USD', 'Vanguard Total Stock Market ETF', '2026-10-28 19:27:15.656297+00', '2026-10-28 19:27:15.656297+00', 'etf', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO instruments VALUES ('76da5a5c-b1e5-4e03-b40a-d280ce5ff62d', 'tax-catalog-vym', 'VYM', 'ARCA', 'USD', 'Vanguard High Dividend Yield ETF', '2026-10-28 19:27:15.65991+00', '2026-10-28 19:27:15.65991+00', 'etf', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO pilots VALUES ('6f44cafc-3a70-4cd9-b686-e09d51beb12a', 'politician', 'nancy-pelosi', 'Nancy Pelosi', 'house', 'P000197', NULL, '{"Nancy Pelosi"}', true, NULL, '2026-10-28 19:27:16.448348+00', NULL);
INSERT INTO pilots VALUES ('5c87b574-c97b-4813-b9ac-64e866adef18', 'politician', 'dan-crenshaw', 'Dan Crenshaw', 'house', 'C001120', NULL, '{"Daniel Crenshaw","Dan Crenshaw"}', true, NULL, '2026-10-28 19:27:16.4514+00', NULL);
INSERT INTO pilots VALUES ('b4770075-eecf-4cea-85ef-1dfcf89ccc57', 'politician', 'josh-gottheimer', 'Josh Gottheimer', 'house', 'G000583', NULL, '{"Josh Gottheimer","Joshua Gottheimer"}', true, NULL, '2026-10-28 19:27:16.453167+00', NULL);
INSERT INTO pilots VALUES ('ee61b865-9568-4892-9287-88e7d6f00ece', 'politician', 'ro-khanna', 'Ro Khanna', 'house', 'K000389', NULL, '{"Ro Khanna","Rohit Khanna"}', true, NULL, '2026-10-28 19:27:16.454787+00', NULL);
INSERT INTO pilots VALUES ('ad6f7b08-27a8-4f62-8fa4-e226adfdf32d', 'politician', 'michael-mccaul', 'Michael McCaul', 'house', 'M001157', NULL, '{"Michael McCaul","Michael T. McCaul"}', true, NULL, '2026-10-28 19:27:16.456427+00', NULL);
INSERT INTO pilots VALUES ('6ee56d32-617b-4cd3-b460-6b3ad5a007a5', 'politician', 'kevin-hern', 'Kevin Hern', 'house', 'H001082', NULL, '{"Kevin Hern"}', true, NULL, '2026-10-28 19:27:16.458396+00', NULL);
INSERT INTO pilots VALUES ('54520993-d2e8-4753-b93a-7d01617af7ca', 'politician', 'daniel-goldman', 'Daniel Goldman', 'house', 'G000599', NULL, '{"Daniel Goldman","Dan Goldman"}', true, NULL, '2026-10-28 19:27:16.460301+00', NULL);
INSERT INTO pilots VALUES ('b9b72ed8-fc68-413b-8e3a-a6b695d260eb', 'politician', 'jared-moskowitz', 'Jared Moskowitz', 'house', 'M001217', NULL, '{"Jared Moskowitz"}', true, NULL, '2026-10-28 19:27:16.464003+00', NULL);
INSERT INTO pilots VALUES ('8e95306f-f05b-4c82-a7fe-f350a9ac2562', 'politician', 'debbie-wasserman-schultz', 'Debbie Wasserman Schultz', 'house', 'W000797', NULL, '{"Debbie Wasserman Schultz"}', true, NULL, '2026-10-28 19:27:16.466193+00', NULL);
INSERT INTO pilots VALUES ('0227f084-4e0d-44eb-8991-a737bc0deb2d', 'politician', 'tommy-tuberville', 'Tommy Tuberville', 'senate', 'T000278', NULL, '{"Tommy Tuberville","Thomas Tuberville"}', true, NULL, '2026-10-28 19:27:16.46803+00', NULL);
INSERT INTO pilots VALUES ('11adb8fd-a494-4320-bd10-96c53ac917a2', 'politician', 'shelley-moore-capito', 'Shelley Moore Capito', 'senate', 'C001047', NULL, '{"Shelley Moore Capito","Shelley Capito"}', true, NULL, '2026-10-28 19:27:16.46948+00', NULL);
INSERT INTO pilots VALUES ('1508f95a-2777-4d09-92e6-d8bea93460de', 'politician', 'sheldon-whitehouse', 'Sheldon Whitehouse', 'senate', 'W000802', NULL, '{"Sheldon Whitehouse"}', true, NULL, '2026-10-28 19:27:16.471102+00', NULL);
INSERT INTO pilots VALUES ('a4a866ec-3292-4492-9a36-e8c9a1d4dbb2', 'politician', 'rick-scott', 'Rick Scott', 'senate', 'S001217', NULL, '{"Rick Scott","Richard Scott"}', true, NULL, '2026-10-28 19:27:16.47246+00', NULL);
INSERT INTO pilots VALUES ('dd9910a8-569f-4288-8cf4-922c23ab6a63', 'fund', 'berkshire-hathaway', 'Berkshire Hathaway', NULL, NULL, '0001067983', '{}', true, NULL, '2026-10-28 19:27:16.47458+00', NULL);
INSERT INTO pilots VALUES ('b32f859c-f83b-473d-8911-ab812e871db6', 'fund', 'appaloosa', 'Appaloosa Management', NULL, NULL, '0001656456', '{}', true, NULL, '2026-10-28 19:27:16.476144+00', NULL);
INSERT INTO pilots VALUES ('127e30f6-76eb-4e60-ba1e-0134ea007b98', 'fund', 'duquesne-family-office', 'Duquesne Family Office', NULL, NULL, '0001536411', '{}', true, NULL, '2026-10-28 19:27:16.477694+00', NULL);
INSERT INTO pilots VALUES ('74ea79d9-48a3-437e-bb31-f252f2a88281', 'fund', 'third-point', 'Third Point', NULL, NULL, '0001040273', '{}', true, NULL, '2026-10-28 19:27:16.479274+00', NULL);
INSERT INTO pilots VALUES ('f0bf5f4b-cb0b-44b0-aa48-f88491c87be4', 'fund', 'baupost', 'Baupost Group', NULL, NULL, '0001061768', '{}', true, NULL, '2026-10-28 19:27:16.480695+00', NULL);
INSERT INTO pilots VALUES ('7a96e118-122f-4dd6-aed4-ae521fa88f9a', 'fund', 'himalaya-capital', 'Himalaya Capital', NULL, NULL, '0001709323', '{}', true, NULL, '2026-10-28 19:27:16.482429+00', NULL);
-- +goose StatementEnd

-- +goose Down
SELECT 'baseline cannot be rolled back';
