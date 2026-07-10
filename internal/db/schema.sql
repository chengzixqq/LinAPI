-- LinAPI 数据库 schema（运行时迁移副本）。
--
-- 本文件供 internal/db/pool.go 的 //go:embed 编译期嵌入，启动时幂等应用。
-- 内容与仓库根 db/schema.sql（sqlc 代码生成的源）保持一致——改表结构时两处必须同步。
--
-- 设计取向：
--   * 金额一律用 BIGINT 存「最小计费单位」（如 microcents），杜绝浮点误差。
--   * 时间戳用 timestamptz，统一按 UTC 落库。
--   * 软删除用 disabled/enabled 布尔而非物理删，保留审计与对账线索。

-- 用户：计费与额度的归属主体。
CREATE TABLE IF NOT EXISTS users (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    external_id TEXT        NOT NULL UNIQUE,
    balance     BIGINT      NOT NULL DEFAULT 0,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- API 密钥：客户端凭证，解析出调用方身份。
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

-- 渠道：上游供应商端点 + 凭证 + 能力，供路由引擎热加载。
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

-- 用量日志：每次成功计费的凭证，异步批量落库。
CREATE TABLE IF NOT EXISTS usage_logs (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id    TEXT        NOT NULL UNIQUE,
    user_id       TEXT        NOT NULL,
    key_id        TEXT        NOT NULL,
    model         TEXT        NOT NULL,
    channel       TEXT        NOT NULL,
    input_tokens  INT         NOT NULL DEFAULT 0,
    output_tokens INT         NOT NULL DEFAULT 0,
    cost          BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_logs_user ON usage_logs (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created ON usage_logs (created_at);
