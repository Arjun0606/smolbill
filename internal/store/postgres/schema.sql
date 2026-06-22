-- smolbill schema, v0 (build plan §8).
-- Postgres-only, single binary, no Kafka/ClickHouse/Temporal — the simplicity
-- is the wedge. The deterministic engine treats this as durable storage for the
-- event-sourced core; all money math happens in Go, never in SQL.

CREATE TABLE IF NOT EXISTS customers (
    id          TEXT PRIMARY KEY,
    external_id TEXT,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS meters (
    id           TEXT PRIMARY KEY,
    code         TEXT UNIQUE NOT NULL,
    name         TEXT NOT NULL,
    aggregation  TEXT NOT NULL CHECK (aggregation IN ('count','sum','max','unique')),
    property_key TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency lives here: dedup on idempotency_key within a documented window.
CREATE TABLE IF NOT EXISTS events (
    id              TEXT PRIMARY KEY,
    idempotency_key TEXT UNIQUE NOT NULL,
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    meter_code      TEXT NOT NULL REFERENCES meters(code),
    event_time      TIMESTAMPTZ NOT NULL,        -- attribution time (drives aggregation)
    properties      JSONB NOT NULL DEFAULT '{}',
    ingested_at     TIMESTAMPTZ NOT NULL DEFAULT now()  -- arrival time (drives late-event detection)
);
CREATE INDEX IF NOT EXISTS events_customer_meter_time_idx ON events (customer_id, meter_code, event_time);

-- Plans are versioned and immutable once published.
CREATE TABLE IF NOT EXISTS plans (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    version    INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (name, version)
);

CREATE TABLE IF NOT EXISTS prices (
    id          TEXT PRIMARY KEY,
    plan_id     TEXT NOT NULL REFERENCES plans(id),
    meter_code  TEXT REFERENCES meters(code),   -- NULL for a pure flat fee
    model       TEXT NOT NULL CHECK (model IN ('flat','per_unit','tiered_graduated','tiered_volume')),
    currency    TEXT NOT NULL,
    unit_amount NUMERIC,        -- per_unit
    flat_amount NUMERIC,        -- flat
    tiers       JSONB,          -- tiered_graduated / tiered_volume: [{up_to, unit_amount, flat_amount}]
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id                   TEXT PRIMARY KEY,
    customer_id          TEXT NOT NULL REFERENCES customers(id),
    plan_id              TEXT NOT NULL REFERENCES plans(id),
    plan_version         INT NOT NULL,
    status               TEXT NOT NULL CHECK (status IN ('active','paused','canceled')),
    current_period_start TIMESTAMPTZ NOT NULL,
    current_period_end   TIMESTAMPTZ NOT NULL,
    started_at           TIMESTAMPTZ NOT NULL,
    canceled_at          TIMESTAMPTZ
);

-- The INTENT log (§8): every subscription change is an immutable event. This is
-- what the AI writes to (attach_plan, add_credit) — never money math.
CREATE TABLE IF NOT EXISTS subscription_events (
    id              TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
    type            TEXT NOT NULL CHECK (type IN ('START','UPGRADE','DOWNGRADE','ADD_CREDIT','PAUSE','CANCEL')),
    payload         JSONB NOT NULL DEFAULT '{}',
    effective_at    TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS entitlements (
    id           TEXT PRIMARY KEY,
    customer_id  TEXT NOT NULL REFERENCES customers(id),
    feature      TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('boolean','metered')),
    limit_value  NUMERIC,
    used_value   NUMERIC NOT NULL DEFAULT 0,
    period_start TIMESTAMPTZ,
    period_end   TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS wallets (
    id          TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL REFERENCES customers(id),
    balance     NUMERIC NOT NULL DEFAULT 0,
    currency    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS wallet_transactions (
    id              TEXT PRIMARY KEY,
    wallet_id       TEXT NOT NULL REFERENCES wallets(id),
    amount          NUMERIC NOT NULL,
    reason          TEXT NOT NULL,
    idempotency_key TEXT UNIQUE NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS invoices (
    id                TEXT PRIMARY KEY,
    customer_id       TEXT NOT NULL REFERENCES customers(id),
    subscription_id   TEXT NOT NULL REFERENCES subscriptions(id),
    period_start      TIMESTAMPTZ NOT NULL,
    period_end        TIMESTAMPTZ NOT NULL,
    status            TEXT NOT NULL,
    stripe_invoice_id TEXT,
    total             NUMERIC NOT NULL,
    currency          TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS invoice_lines (
    id         TEXT PRIMARY KEY,
    invoice_id TEXT NOT NULL REFERENCES invoices(id),
    meter_code TEXT,
    quantity   NUMERIC NOT NULL,
    unit_price NUMERIC NOT NULL,
    amount     NUMERIC NOT NULL,
    ledger_ref TEXT
);

-- THE HEADLINE (§9 GET /reconcile/{invoice_id}): a queryable proof that
-- raw events -> meter -> entitlement -> invoice line never silently disagree.
CREATE TABLE IF NOT EXISTS reconciliation_ledger (
    id                TEXT PRIMARY KEY,
    invoice_id        TEXT NOT NULL REFERENCES invoices(id),
    raw_event_count   BIGINT NOT NULL,
    meter_value       NUMERIC NOT NULL,
    entitlement_state JSONB,
    invoice_line_amount NUMERIC NOT NULL,
    diffs             JSONB,            -- non-empty => drift detected
    computed_hash     TEXT NOT NULL,    -- matches invoice.Result.Hash
    computed_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Phase 2: metered entitlements need to know which meter measures usage.
ALTER TABLE entitlements ADD COLUMN IF NOT EXISTS meter_code TEXT;

-- Phase 2: ledger is one row per invoice line; record which meter it traces.
ALTER TABLE reconciliation_ledger ADD COLUMN IF NOT EXISTS meter_code TEXT;

-- Phase 3: proactive spend alerts (50/80/100% of budget).
CREATE TABLE IF NOT EXISTS alerts (
    id          TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL REFERENCES customers(id),
    budget      NUMERIC NOT NULL,
    currency    TEXT NOT NULL,
    thresholds  JSONB NOT NULL,          -- e.g. [50,80,100]
    webhook_url TEXT NOT NULL,
    max_fired   INT NOT NULL DEFAULT 0,  -- high-water mark of fired thresholds
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Phase 6: outbound event webhooks (invoice.finalized, drift.detected).
CREATE TABLE IF NOT EXISTS webhooks (
    id         TEXT PRIMARY KEY,
    url        TEXT NOT NULL,
    events     JSONB NOT NULL DEFAULT '[]'::jsonb, -- subscribed event types; [] = all
    secret     TEXT NOT NULL,                      -- HMAC-SHA256 signing secret
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Phase 8: dunning / failed-payment recovery state, one row per unpaid invoice.
CREATE TABLE IF NOT EXISTS collections (
    invoice_id      TEXT PRIMARY KEY REFERENCES invoices(id),
    external_id     TEXT NOT NULL,                     -- processor invoice the charge targets
    status          TEXT NOT NULL,                     -- scheduled|retrying|requires_action|paid|uncollectible
    attempts        INT NOT NULL DEFAULT 0,
    first_failed_at TIMESTAMPTZ,                       -- null until the first failure
    next_attempt_at TIMESTAMPTZ,                       -- null in terminal / requires_action states
    last_reason     TEXT NOT NULL DEFAULT '',
    currency        TEXT NOT NULL,
    amount_minor    BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Index the due-collections scan (the dunning tick): status + when the retry fires.
CREATE INDEX IF NOT EXISTS idx_collections_due ON collections (status, next_attempt_at);
