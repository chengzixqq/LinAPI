-- LinAPI 数据库 schema。
--
-- 这是 sqlc 代码生成与全新数据库最终结构的真相源：
--   * sqlc 读它推断查询的参数/返回类型（见 sqlc.yaml）。
--   * 既有数据库升级必须另增 internal/db/migrations/<version>_<name>.sql，
--     已发布迁移不可改写。
--
-- 设计取向：
--   * 金额一律用 BIGINT 存「最小计费单位」（如 microcents），杜绝浮点误差。
--   * 时间戳用 timestamptz，统一按 UTC 落库。
--   * 软删除用 disabled/enabled 布尔而非物理删，保留审计与对账线索。

-- 用户：计费与额度的归属主体。
CREATE TABLE IF NOT EXISTS users (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- external_id 是对外暴露的稳定用户标识（与 api_keys.user_id 对应，避免暴露自增主键）。
    external_id TEXT        NOT NULL UNIQUE,
    -- balance 是可用权威余额（最小计费单位）；在途 reservation 已从该值预扣。
    balance     BIGINT      NOT NULL DEFAULT 0,
    -- balance_version 每次资金变动递增，供派生缓存拒绝陈旧绝对余额覆盖。
    balance_version BIGINT  NOT NULL DEFAULT 0,
    -- rate_multiplier 预留：单用户定价倍率覆盖，百分比整数（100=1.00x），本期存而不用。
    rate_multiplier INT     NOT NULL DEFAULT 100,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- API 密钥：客户端凭证，解析出调用方身份。
CREATE TABLE IF NOT EXISTS api_keys (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- key_hash 存密钥的 SHA-256 十六进制摘要，绝不落明文。
    key_hash           TEXT        NOT NULL UNIQUE,
    -- key_id 是稳定的对外标识（限流维度、日志、计费归因）。
    key_id             TEXT        NOT NULL UNIQUE,
    -- user_external_id 关联 users.external_id。
    user_external_id   TEXT        NOT NULL REFERENCES users (external_id),
    -- rate_limit_per_min <=0 表示不限流。
    rate_limit_per_min INT         NOT NULL DEFAULT 0,
    -- allowed_models 为空数组表示不做模型级限制。
    allowed_models     TEXT[]      NOT NULL DEFAULT '{}',
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys (user_external_id);

-- 渠道：上游供应商端点 + 凭证 + 能力，供路由引擎热加载。
CREATE TABLE IF NOT EXISTS channels (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- channel_id 是稳定的对外标识（熔断状态、日志、计费归因）。
    channel_id  TEXT        NOT NULL UNIQUE,
    name        TEXT        NOT NULL,
    -- format 决定出向适配器：openai | anthropic。
    format      TEXT        NOT NULL,
    base_url    TEXT        NOT NULL,
    -- api_key 只存版本化 AES-256-GCM envelope，明文仅在进程内短暂解密。
    api_key     TEXT        NOT NULL,
    -- models 是「对外模型名 -> 上游实际模型名」映射；值为空表示透传。
    models      JSONB       NOT NULL DEFAULT '{}',
    priority    INT         NOT NULL DEFAULT 0,
    weight      INT         NOT NULL DEFAULT 1,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NOT VALID 允许旧库先启动一次显式事务迁移；约束从创建起即阻止新增明文，
-- 迁移完成后由启动流程 VALIDATE，避免滚动升级期间旧实例重新写入明文。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'channels_api_key_envelope_check'
          AND conrelid = 'channels'::regclass
    ) THEN
        ALTER TABLE channels
            ADD CONSTRAINT channels_api_key_envelope_check
            CHECK (api_key LIKE 'linapi:channel-key:%') NOT VALID;
    END IF;
END
$$;

-- 用量日志：每次成功计费的权威凭证，由最终结算事务同步落库。
CREATE TABLE IF NOT EXISTS usage_logs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- request_id 保存服务端 reservation ID，UNIQUE 保证账单重放不重复记账；
    -- 外部链路 ID 单独保存在 billing_reservations.trace_id。
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
    -- cost 是本次实际扣费（最小计费单位）。
    cost          BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_logs_user ON usage_logs (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created ON usage_logs (created_at);

-- 持久预授权：记录一次内部请求从预扣到结算/退款的完整状态。
CREATE TABLE IF NOT EXISTS billing_reservations (
    reservation_id TEXT        PRIMARY KEY,
    trace_id       TEXT        NOT NULL,
    user_id        TEXT        NOT NULL REFERENCES users (external_id),
    key_id         TEXT        NOT NULL,
    model          TEXT        NOT NULL,
    amount         BIGINT      NOT NULL CHECK (amount > 0),
    max_input_tokens  INT      NOT NULL CHECK (max_input_tokens >= 0),
    max_output_tokens INT      NOT NULL CHECK (max_output_tokens > 0),
    status          TEXT       NOT NULL CHECK (status IN ('reserved', 'in_flight', 'consumed_unsettled', 'settled', 'refunded')),
    channel         TEXT       NOT NULL DEFAULT '',
    input_tokens    INT        NOT NULL DEFAULT 0,
    output_tokens   INT        NOT NULL DEFAULT 0,
    cache_creation_input_tokens INT NOT NULL DEFAULT 0,
    cache_read_input_tokens     INT NOT NULL DEFAULT 0,
    reported_total_tokens       INT NOT NULL DEFAULT 0,
    cost            BIGINT     NOT NULL DEFAULT 0 CHECK (cost >= 0 AND cost <= amount),
    usage_complete  BOOLEAN    NOT NULL DEFAULT FALSE,
    estimated       BOOLEAN    NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    consumed_at     TIMESTAMPTZ,
    settled_at      TIMESTAMPTZ,
    refunded_at     TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 兼容账本状态机升级：显式重建 CHECK，使已存在的表接受 in_flight 并保证
-- 最终成本不突破预授权金额。
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

-- 资金流水：只追加不修改。operation_id 与 (reservation_id, kind) 双重唯一，
-- 使网络超时后的同阶段重放不会再次改变余额。
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

-- 登录账户：控制台的鉴权主体（与计费实体 users 职责分离）。
CREATE TABLE IF NOT EXISTS accounts (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- username 登录名，全局唯一。
    username      TEXT        NOT NULL UNIQUE,
    -- password_hash 存 bcrypt 哈希，绝不落明文，绝不用快哈希（MD5/SHA）。
    password_hash TEXT        NOT NULL,
    -- role 仅 'admin' | 'user'。
    role          TEXT        NOT NULL,
    -- external_id 软关联 users.external_id：user 角色必填（额度容器），admin 可空。
    external_id   TEXT,
    -- group_name 预留：定价分组名，本期存而不用。
    group_name    TEXT        NOT NULL DEFAULT 'default',
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    -- session_version 会话代次（审查 AUD-P1-17）：禁用/改密时递增，使旧会话立即失效。
    session_version INT       NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_role_external_id_check CHECK (
        (role = 'admin' AND external_id IS NULL) OR
        (role = 'user' AND external_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_accounts_role ON accounts (role);

-- 兼容既有部署：CREATE TABLE IF NOT EXISTS 不会给已存在的表补列，故显式幂等补列
-- （审查 AUD-P1-17，session_version 为后加字段）。ADD COLUMN IF NOT EXISTS 需 PG 9.6+。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_version INT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'accounts_role_external_id_check'
          AND conrelid = 'accounts'::regclass
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_role_external_id_check CHECK (
                (role = 'admin' AND external_id IS NULL) OR
                (role = 'user' AND external_id IS NOT NULL)
            );
    END IF;
END
$$;

-- 兼容账本上线前创建的 users / usage_logs 表。
ALTER TABLE users ADD COLUMN IF NOT EXISTS balance_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_creation_input_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_read_input_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS reported_total_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS usage_complete BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS estimated BOOLEAN NOT NULL DEFAULT FALSE;

-- 系统设置：运行时可变的 KV 配置，控制台可改、即时生效。
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
