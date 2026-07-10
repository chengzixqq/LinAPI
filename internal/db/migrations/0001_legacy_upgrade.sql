-- 从版本化迁移引入前的 LinAPI schema 升级到当前基线。
-- 本文件发布后不可改写；后续变更必须新增更高版本迁移。

ALTER TABLE users ADD COLUMN IF NOT EXISTS balance_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS rate_multiplier INT NOT NULL DEFAULT 100;

CREATE TABLE IF NOT EXISTS api_keys (
    id                 BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key_hash           TEXT        NOT NULL UNIQUE,
    key_id             TEXT        NOT NULL UNIQUE,
    user_external_id   TEXT        NOT NULL REFERENCES users (external_id),
    rate_limit_per_min INT         NOT NULL DEFAULT 0,
    allowed_models     TEXT[]      NOT NULL DEFAULT '{}',
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys (user_external_id);

CREATE TABLE IF NOT EXISTS channels (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    channel_id  TEXT        NOT NULL UNIQUE,
    name        TEXT        NOT NULL,
    format      TEXT        NOT NULL,
    base_url    TEXT        NOT NULL,
    api_key     TEXT        NOT NULL,
    models      JSONB       NOT NULL DEFAULT '{}',
    priority    INT         NOT NULL DEFAULT 0,
    weight      INT         NOT NULL DEFAULT 1,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channels_api_key_envelope_check'
          AND conrelid = 'channels'::regclass
    ) THEN
        ALTER TABLE channels ADD CONSTRAINT channels_api_key_envelope_check
            CHECK (api_key LIKE 'linapi:channel-key:%') NOT VALID;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS usage_logs (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id    TEXT        NOT NULL UNIQUE,
    user_id       TEXT        NOT NULL,
    key_id        TEXT        NOT NULL,
    model         TEXT        NOT NULL,
    channel       TEXT        NOT NULL,
    input_tokens  INT         NOT NULL DEFAULT 0,
    output_tokens INT         NOT NULL DEFAULT 0,
    cache_creation_input_tokens INT NOT NULL DEFAULT 0,
    cache_read_input_tokens     INT NOT NULL DEFAULT 0,
    reported_total_tokens       INT NOT NULL DEFAULT 0,
    usage_complete BOOLEAN     NOT NULL DEFAULT TRUE,
    estimated      BOOLEAN     NOT NULL DEFAULT FALSE,
    cost          BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_creation_input_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_read_input_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS reported_total_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS usage_complete BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS estimated BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_usage_logs_user ON usage_logs (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created ON usage_logs (created_at);

CREATE TABLE IF NOT EXISTS billing_reservations (
    reservation_id TEXT        PRIMARY KEY,
    trace_id       TEXT        NOT NULL,
    user_id        TEXT        NOT NULL REFERENCES users (external_id),
    key_id         TEXT        NOT NULL,
    model          TEXT        NOT NULL,
    amount         BIGINT      NOT NULL CHECK (amount > 0),
    max_input_tokens  INT      NOT NULL CHECK (max_input_tokens >= 0),
    max_output_tokens INT      NOT NULL CHECK (max_output_tokens > 0),
    status          TEXT       NOT NULL,
    channel         TEXT       NOT NULL DEFAULT '',
    input_tokens    INT        NOT NULL DEFAULT 0,
    output_tokens   INT        NOT NULL DEFAULT 0,
    cache_creation_input_tokens INT NOT NULL DEFAULT 0,
    cache_read_input_tokens     INT NOT NULL DEFAULT 0,
    reported_total_tokens       INT NOT NULL DEFAULT 0,
    cost            BIGINT     NOT NULL DEFAULT 0,
    usage_complete  BOOLEAN    NOT NULL DEFAULT FALSE,
    estimated       BOOLEAN    NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    consumed_at     TIMESTAMPTZ,
    settled_at      TIMESTAMPTZ,
    refunded_at     TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE billing_reservations DROP CONSTRAINT IF EXISTS billing_reservations_status_check;
ALTER TABLE billing_reservations ADD CONSTRAINT billing_reservations_status_check
    CHECK (status IN ('reserved', 'in_flight', 'consumed_unsettled', 'settled', 'refunded'));
ALTER TABLE billing_reservations DROP CONSTRAINT IF EXISTS billing_reservations_cost_check;
ALTER TABLE billing_reservations ADD CONSTRAINT billing_reservations_cost_check
    CHECK (cost >= 0 AND cost <= amount);
CREATE INDEX IF NOT EXISTS idx_billing_reservations_pending
    ON billing_reservations (status, updated_at);
CREATE INDEX IF NOT EXISTS idx_billing_reservations_user
    ON billing_reservations (user_id, created_at);

CREATE TABLE IF NOT EXISTS billing_ledger (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operation_id    TEXT        NOT NULL UNIQUE,
    reservation_id  TEXT        NOT NULL REFERENCES billing_reservations (reservation_id),
    user_id         TEXT        NOT NULL REFERENCES users (external_id),
    kind            TEXT        NOT NULL CHECK (kind IN ('reserve', 'settle', 'refund')),
    amount          BIGINT      NOT NULL,
    balance_after   BIGINT      NOT NULL,
    balance_version BIGINT      NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (reservation_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_billing_ledger_user
    ON billing_ledger (user_id, created_at);

CREATE TABLE IF NOT EXISTS accounts (
    id              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username        TEXT        NOT NULL UNIQUE,
    password_hash   TEXT        NOT NULL,
    role            TEXT        NOT NULL,
    external_id     TEXT,
    group_name      TEXT        NOT NULL DEFAULT 'default',
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    session_version INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_version INT NOT NULL DEFAULT 0;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'accounts_role_external_id_check'
          AND conrelid = 'accounts'::regclass
    ) THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_role_external_id_check CHECK (
            (role = 'admin' AND external_id IS NULL) OR
            (role = 'user' AND external_id IS NOT NULL)
        );
    END IF;
END
$$;
CREATE INDEX IF NOT EXISTS idx_accounts_role ON accounts (role);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
