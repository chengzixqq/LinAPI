-- LinAPI 查询定义（sqlc 源）。
--
-- 每条查询上方的 `-- name: Xxx :kind` 注解是 sqlc 的代码生成指令：
--   :one  返回单行；:many 返回多行；:exec 无返回；:execrows 返回受影响行数；
--   :batchexec 生成批量执行接口（pgx 驱动）。
-- 参数占位符 $1/$2… 由 sqlc 映射为类型安全的 Go 方法入参。

-- ============================ users ============================

-- name: GetUserByExternalID :one
SELECT id, external_id, balance, enabled, created_at, updated_at
FROM users
WHERE external_id = $1;

-- name: GetBalance :one
-- 只取 PostgreSQL 权威可用余额。禁用用户视作 0 余额（闸门自然拦截）。
SELECT balance
FROM users
WHERE external_id = $1 AND enabled = TRUE;

-- name: AddBalance :one
-- 原子增减余额并返回新值，供充值/对账。delta 为负表示扣费。
UPDATE users
SET balance = balance + $2,
	 balance_version = balance_version + 1,
    updated_at = now()
WHERE external_id = $1
RETURNING balance;

-- name: CreateUser :one
INSERT INTO users (external_id, balance, enabled)
VALUES ($1, $2, $3)
RETURNING id, external_id, balance, enabled, created_at, updated_at;

-- name: ListUsers :many
-- 管理面：分页列出用户（按创建时间倒序）。
SELECT id, external_id, balance, enabled, created_at, updated_at
FROM users
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2;

-- name: SetUserEnabled :one
-- 管理面：启用/禁用用户（软删除）。
UPDATE users
SET enabled = $2,
    updated_at = now()
WHERE external_id = $1
RETURNING id, external_id, balance, enabled, created_at, updated_at;

-- ============================ api_keys ============================

-- name: ResolveAPIKey :one
-- 按密钥摘要解析调用方身份（联表取用户启用状态）。
-- 仅返回密钥与用户都启用的记录；任一禁用则查不到（等价于 KeyNotFound）。
SELECT
    k.key_id,
    k.user_external_id,
    k.rate_limit_per_min,
    k.allowed_models,
    k.enabled AS key_enabled,
    u.enabled AS user_enabled
FROM api_keys k
JOIN users u ON u.external_id = k.user_external_id
WHERE k.key_hash = $1 AND k.enabled = TRUE AND u.enabled = TRUE;

-- name: CreateAPIKey :one
INSERT INTO api_keys (
    key_hash, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, key_hash, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at;

-- name: CreateAPIKeyLimited :one
-- 同一用户用事务级 advisory lock 串行化“计数 + 插入”，防止并发越过数量上限。
WITH lock_row AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(hashtextextended($3, 0))
)
INSERT INTO api_keys (
    key_hash, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled
)
SELECT $1, $2, $3, $4, $5, $6
FROM lock_row
WHERE (SELECT count(*) FROM api_keys WHERE user_external_id = $3) < $7
RETURNING id, key_hash, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at;

-- name: ListAPIKeysByUser :many
-- 管理面：列出某用户的全部密钥（不含 key_hash，摘要不外泄）。
SELECT id, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at
FROM api_keys
WHERE user_external_id = $1
ORDER BY created_at DESC, id DESC;

-- name: SetAPIKeyEnabled :one
-- 管理面：启用/禁用密钥（软删除）。
UPDATE api_keys
SET enabled = $2
WHERE key_id = $1
RETURNING id, key_id, user_external_id, rate_limit_per_min, allowed_models, enabled, created_at;

-- name: DeleteAPIKey :execrows
-- 管理面：物理删除密钥，返回受影响行数（0 表示不存在）。
DELETE FROM api_keys
WHERE key_id = $1;

-- ============================ channels ============================

-- name: ListEnabledChannels :many
-- 供路由引擎启动加载 + 热更新拉取全部启用渠道。
SELECT
    channel_id, name, format, base_url, api_key, models, priority, weight, enabled
FROM channels
WHERE enabled = TRUE
ORDER BY priority DESC, channel_id;

-- name: ListChannelKeyMaterialsForUpdate :many
-- 启动迁移在同一事务内锁定全部渠道密钥，避免迁移期间并发写入明文。
SELECT channel_id, api_key
FROM channels
ORDER BY channel_id
FOR UPDATE;

-- name: UpdateChannelKeyMaterial :execrows
-- 仅当旧值仍与锁定时一致才替换，防止意外覆盖并发变更。
UPDATE channels
SET api_key = sqlc.arg(api_key),
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)
  AND api_key = sqlc.arg(old_api_key);

-- name: CreateChannel :one
INSERT INTO channels (
    channel_id, name, format, base_url, api_key, models, priority, weight, enabled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at;

-- name: ListAllChannels :many
-- 管理面：列出全部渠道（含禁用），供后台管理展示。
SELECT id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
FROM channels
ORDER BY priority DESC, channel_id;

-- name: GetChannel :one
SELECT id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at
FROM channels
WHERE channel_id = $1;

-- name: UpdateChannel :one
-- 管理面：全量更新渠道可变字段。
UPDATE channels
SET name = sqlc.arg(name),
    format = sqlc.arg(format),
    base_url = sqlc.arg(base_url),
    api_key = CASE
        WHEN sqlc.arg(api_key_set)::boolean THEN sqlc.arg(api_key)::text
        ELSE api_key
    END,
    models = sqlc.arg(models),
    priority = sqlc.arg(priority),
    weight = sqlc.arg(weight),
    enabled = sqlc.arg(enabled),
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)
RETURNING id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at;

-- name: SetChannelEnabled :one
-- 管理面：启用/禁用渠道。
UPDATE channels
SET enabled = $2,
    updated_at = now()
WHERE channel_id = $1
RETURNING id, channel_id, name, format, base_url, api_key, models, priority, weight, enabled, created_at, updated_at;

-- name: DeleteChannel :execrows
-- 管理面：物理删除渠道（渠道无外键依赖，可硬删）。返回受影响行数。
DELETE FROM channels
WHERE channel_id = $1;

-- ============================ accounts ============================

-- name: CreateAccount :one
INSERT INTO accounts (username, password_hash, role, external_id)
VALUES ($1, $2, $3, $4)
RETURNING id, username, password_hash, role, external_id, group_name, enabled, session_version, created_at, updated_at;

-- name: GetAccountByUsername :one
-- 按登录名取账户（登录校验用）。
SELECT id, username, password_hash, role, external_id, group_name, enabled, session_version, created_at, updated_at
FROM accounts WHERE username = $1;

-- name: GetAccountByID :one
SELECT id, username, password_hash, role, external_id, group_name, enabled, session_version, created_at, updated_at
FROM accounts WHERE id = $1;

-- name: ListAccounts :many
-- 管理面：分页列出账户。
SELECT id, username, password_hash, role, external_id, group_name, enabled, session_version, created_at, updated_at
FROM accounts ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2;

-- name: CountAccounts :one
-- 统计账户数（概览页与 bootstrap 判断用）。
SELECT count(*) FROM accounts;

-- name: SetAccountEnabled :one
-- 启停账户；禁用时递增 session_version 使旧会话立即失效（审查 AUD-P1-17）。
-- 重新启用（$2=TRUE）不递增——无需踢已在线会话。
UPDATE accounts
SET enabled = $2,
    session_version = session_version + CASE WHEN $2 = FALSE THEN 1 ELSE 0 END,
    updated_at = now()
WHERE id = $1
RETURNING id, username, password_hash, role, external_id, group_name, enabled, session_version, created_at, updated_at;

-- name: UpdateAccountPassword :exec
-- 改密（存新的 bcrypt 哈希）并递增 session_version，使旧会话立即失效（审查 AUD-P1-17）。
UPDATE accounts
SET password_hash = $2, session_version = session_version + 1, updated_at = now()
WHERE id = $1;

-- ============================ settings ============================

-- name: GetSetting :one
-- 取单个设置项。
SELECT key, value, updated_at FROM settings WHERE key = $1;

-- name: UpsertSetting :exec
-- 写入/更新设置项（幂等）。
INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: GetSettingsSnapshot :one
-- 单条语句读取完整设置快照，避免两次 READ COMMITTED 查询拼出不存在的组合。
SELECT
    COALESCE((SELECT value FROM settings WHERE key = 'registration_enabled'), '') AS registration_enabled,
    COALESCE((SELECT value FROM settings WHERE key = 'new_user_initial_balance'), '') AS new_user_initial_balance;

-- name: UpsertSettingsSnapshot :exec
-- 单条语句原子写入完整设置快照。
INSERT INTO settings (key, value, updated_at) VALUES
    ('registration_enabled', $1, now()),
    ('new_user_initial_balance', $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- ============================ usage_logs ============================

-- name: SumCostByUser :one
-- 对账用：统计某用户在时间窗内的总扣费。
SELECT COALESCE(SUM(cost), 0)::BIGINT AS total_cost
FROM usage_logs
WHERE user_id = $1 AND created_at >= $2 AND created_at < $3;

-- ============================ billing ledger ============================

-- name: InsertBillingReservation :one
-- 先插 reservation，再在同一事务内条件扣余额；冲突时由调用方读取旧记录做幂等比较。
INSERT INTO billing_reservations (
    reservation_id, trace_id, user_id, key_id, model, amount,
    max_input_tokens, max_output_tokens, status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'reserved', $9, $9)
ON CONFLICT (reservation_id) DO NOTHING
RETURNING reservation_id, trace_id, user_id, key_id, model, amount,
          max_input_tokens, max_output_tokens, status, channel,
          input_tokens, output_tokens, cache_creation_input_tokens,
          cache_read_input_tokens, reported_total_tokens, cost,
          usage_complete, estimated, created_at, consumed_at,
          settled_at, refunded_at, updated_at;

-- name: GetBillingReservation :one
SELECT reservation_id, trace_id, user_id, key_id, model, amount,
       max_input_tokens, max_output_tokens, status, channel,
       input_tokens, output_tokens, cache_creation_input_tokens,
       cache_read_input_tokens, reported_total_tokens, cost,
       usage_complete, estimated, created_at, consumed_at,
       settled_at, refunded_at, updated_at
FROM billing_reservations
WHERE reservation_id = $1;

-- name: GetBillingReservationForUpdate :one
SELECT reservation_id, trace_id, user_id, key_id, model, amount,
       max_input_tokens, max_output_tokens, status, channel,
       input_tokens, output_tokens, cache_creation_input_tokens,
       cache_read_input_tokens, reported_total_tokens, cost,
       usage_complete, estimated, created_at, consumed_at,
       settled_at, refunded_at, updated_at
FROM billing_reservations
WHERE reservation_id = $1
FOR UPDATE;

-- name: DebitBalanceForReservation :one
-- 条件扣款同时承担并发额度闸门：同一用户的并发 reservation 不得超卖。
UPDATE users
SET balance = balance - $2,
    balance_version = balance_version + 1,
    updated_at = now()
WHERE external_id = $1 AND enabled = TRUE AND $2 > 0 AND balance >= $2
RETURNING balance, balance_version;

-- name: AdjustBalanceForBilling :one
-- Settle/Refund 已由 reservation 行锁保证一次性，这里只应用对应的余额差额。
UPDATE users
SET balance = balance + $2,
    balance_version = balance_version + 1,
    updated_at = now()
WHERE external_id = $1
RETURNING balance, balance_version;

-- name: RecordBillingConsumption :execrows
UPDATE billing_reservations
SET status = 'consumed_unsettled',
    channel = $2,
    input_tokens = $3,
    output_tokens = $4,
    cache_creation_input_tokens = $5,
    cache_read_input_tokens = $6,
    reported_total_tokens = $7,
    cost = $8,
    usage_complete = $9,
    estimated = $10,
    consumed_at = $11,
    updated_at = $11
WHERE reservation_id = $1 AND status = 'in_flight';

-- name: MarkBillingReservationInFlight :execrows
UPDATE billing_reservations
SET status = 'in_flight', channel = $2, updated_at = $3
WHERE reservation_id = $1 AND status = 'reserved';

-- name: ReleaseBillingAttempt :execrows
UPDATE billing_reservations
SET status = 'reserved', updated_at = $2
WHERE reservation_id = $1 AND status = 'in_flight';

-- name: MarkBillingReservationSettled :execrows
UPDATE billing_reservations
SET status = 'settled', settled_at = $2, updated_at = $2
WHERE reservation_id = $1 AND status = 'consumed_unsettled';

-- name: MarkBillingReservationRefunded :execrows
UPDATE billing_reservations
SET status = 'refunded', refunded_at = $2, updated_at = $2
WHERE reservation_id = $1 AND status = 'reserved';

-- name: InsertBillingLedgerEntry :exec
INSERT INTO billing_ledger (
    operation_id, reservation_id, user_id, kind, amount,
    balance_after, balance_version, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertFinalizedUsageLog :exec
-- 权威 usage 与余额结算同事务写入，不再依赖异步 Recorder 才能对账。
INSERT INTO usage_logs (
    request_id, user_id, key_id, model, channel,
    input_tokens, output_tokens, cache_creation_input_tokens,
    cache_read_input_tokens, reported_total_tokens, cost,
    usage_complete, estimated, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: ListConsumedUnsettledReservations :many
SELECT reservation_id
FROM billing_reservations
WHERE status = 'consumed_unsettled'
ORDER BY consumed_at, reservation_id;

-- name: ListStaleReservedReservations :many
SELECT reservation_id
FROM billing_reservations
WHERE status = 'reserved' AND updated_at < $1
ORDER BY updated_at, reservation_id;

-- name: ListStaleInFlightReservations :many
SELECT reservation_id
FROM billing_reservations
WHERE status = 'in_flight' AND updated_at < $1
ORDER BY updated_at, reservation_id;
