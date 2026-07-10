-- LinAPI 数据库 schema。
--
-- 这是 sqlc 代码生成与数据库迁移的单一真相源：
--   * sqlc 读它推断查询的参数/返回类型（见 sqlc.yaml）。
--   * migrations/ 下的迁移脚本与本文件保持一致（首版即全量建表）。
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
    -- balance 是权威余额（最小计费单位）。Redis 只是热副本，这里才是冷源真相。
    balance     BIGINT      NOT NULL DEFAULT 0,
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
    -- api_key 是访问上游的密钥（此处存明文，生产可换 KMS/密文列）。
    api_key     TEXT        NOT NULL,
    -- models 是「对外模型名 -> 上游实际模型名」映射；值为空表示透传。
    models      JSONB       NOT NULL DEFAULT '{}',
    priority    INT         NOT NULL DEFAULT 0,
    weight      INT         NOT NULL DEFAULT 1,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 用量日志：每次成功计费的凭证，异步批量落库。
CREATE TABLE IF NOT EXISTS usage_logs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- request_id 关联追踪 ID，UNIQUE 保证按请求幂等（重放不重复记账）。
    request_id    TEXT        NOT NULL UNIQUE,
    user_id       TEXT        NOT NULL,
    key_id        TEXT        NOT NULL,
    model         TEXT        NOT NULL,
    channel       TEXT        NOT NULL,
    input_tokens  INT         NOT NULL DEFAULT 0,
    output_tokens INT         NOT NULL DEFAULT 0,
    -- cost 是本次实际扣费（最小计费单位）。
    cost          BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_logs_user ON usage_logs (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created ON usage_logs (created_at);

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
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_accounts_role ON accounts (role);

-- 系统设置：运行时可变的 KV 配置，控制台可改、即时生效。
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
