# 模块详解

## internal/canonical —— 规范数据模型

纯数据定义，无逻辑。是所有格式转换的中枢。

### message.go（请求侧）

- `Request`：模型名、原生顶层 System、保持顺序的 Messages、Tools、ToolChoice、采样参数、Stream、Metadata。OpenAI 的 system/developer 指令保存在 Messages 中以避免提升后改变优先级；Anthropic 构建时只把正文前可表达的指令按顺序合并到 system，正文后的 developer 指令显式拒绝。
- `Message`：`Role` 支持 user / assistant / system / developer + `[]ContentBlock`。
- `ContentBlock`：**带类型的联合结构**，`Type`（BlockType）决定哪些字段有效：
  - `text` → Text
  - `image` → Image（url 或 base64）
  - `thinking` → Thinking + ThinkingSignature
  - `tool_use` → ToolUseID / ToolName / `ToolInputJSON`（`json.RawMessage`）及兼容视图；`UseNumber` 避免大整数精度损失
  - `tool_result` → ToolResultID / `[]ContentBlock` / ToolResultError，可保留文本与图片混合结果
  - `CacheControl` 标记该块参与提示缓存（Claude prompt caching）
- 工具结果作为 **user 消息里的 `tool_result` block** 承载，对齐 Claude 结构（而非 OpenAI 的独立 tool 角色消息）。

### response.go（响应侧）

- `Response`：非流式响应。ID / Model / Role / Content / StopReason / Usage。
- `StopReason`：归一化枚举（end_turn / max_tokens / tool_use / stop / error），各家停止原因双向映射。
- `Usage`：普通输入/输出 token、缓存创建/读取 token 与字段已知位，计费依赖它。OpenAI `prompt_tokens_details.cached_tokens` 会从普通输入中拆到 cache read。
- `Event`：**语义化规范流式事件**，不绑定任何一家 SSE 格式：
  - message_start / block_start / block_delta / block_stop / message_delta / message_stop / ping / error
- `Delta`：流式增量内容，按块类型区分（文本 / thinking / 工具参数 JSON 分片）。

## internal/adapter —— 供应商适配器

### adapter.go（接口）

`Adapter` 接口方法：`Name` / `ParseRequest` / `BuildRequest` / `ParseResponse` / `BuildResponse` / `NewStreamDecoder` / `NewStreamEncoder`。内置适配器同时实现可选 `ErrorCodec`，把上游 HTTP error envelope 解析成 canonical 再按客户端协议构造。

**并发约定**：`Adapter` 方法无状态、并发安全（同实例被多请求并发调用）。流式需跨块状态，故通过工厂方法为每个流式请求创建独立的 `StreamDecoder` / `StreamEncoder`（有状态、非并发安全）。

- `StreamDecoder.Decode(raw)`：处理一个上游 SSE 数据块，返回 0..N 个规范事件（心跳/空行可能产 0 个）。
- `StreamEncoder.Encode(event)`：把一个规范事件编码为目标 SSE 字节，返回 nil 表示该事件在目标格式下无需输出。
- `sse.go`：`SSEData` 忽略首个 UTF-8 BOM、统一 LF/CRLF/裸 CR，忽略 comment/event/id/retry 并按标准拼接多 data 行；两个适配器不得自行用“取第一行”解析。

### registry.go（注册表）

全局注册表。`Register`（重名 panic）/ `Get` / `MustGet` / `Names`。新增供应商在自身包 `init()` 注册，并在 `internal/adapter/all` 增加一行空导入；无需修改转发核心。

### 已实现适配器

- `adapter/openai`：OpenAI Chat Completions 格式。
- `adapter/anthropic`：Anthropic Messages 格式（Claude）。

每个包按职责拆分：`adapter.go`（注册）/ `request*.go`（请求转换）/ `response.go` / `stream_decode.go` / `stream_encode.go` / `types.go`（线格式结构体）。测试：`roundtrip_test.go`（canonical → 线格式 → canonical 往返一致）+ `stream_test.go`。

协议边界已显式收敛：OpenAI `stop` 支持字符串/数组，Anthropic `message.content` 支持字符串/block 数组，OpenAI developer/system 顺序保留，Anthropic tool_result 图片可往返。工具调用参数以原始 JSON 保存并用 `UseNumber` 提供兼容视图。网关只支持 OpenAI `n=1`：请求 `n!=1` 在触网前 4xx；非流/流上游若返回多 choices 或非零 index，解码器显式报错，Forwarder 在已消费后不重试并保守结算。

跨格式流式 usage：Anthropic→OpenAI 会生成标准 `choices:[]` usage 尾块；OpenAI→Anthropic 若 input usage 晚于 `message_start`，Anthropic 协议没有等价位置，故不伪造线上字段，账本将精确 usage 判为不完整并保留预授权。该方向性缺口对应 AUD-P2-03。

> **blank-import 已接线**：适配器包的 `init()` 注册通过汇总包 `internal/adapter/all` 触发——该包集中 blank-import 各供应商适配器，`cmd/linapi` 只需一行 `_ "linapi/internal/adapter/all"`：
> ```go
> // internal/adapter/all/all.go
> import (
>     _ "linapi/internal/adapter/openai"
>     _ "linapi/internal/adapter/anthropic"
> )
> ```
> 新增供应商时在 `all` 包补一行空导入即可，main 无需改动。

## internal/routing —— 路由 / 负载均衡引擎

纯逻辑，不发网络请求。

### channel.go

`Channel`：ID（稳定，用于熔断/日志/计费归因）、Name、Format（openai / anthropic，决定出向适配器）、BaseURL、APIKey、Models（对外名 → 上游名，空值透传）、Priority（越大越优先）、Weight（同级加权随机）、Enabled。方法：`Supports(model)` / `UpstreamModel(model)`。

### select.go（排序策略）

- `weightedShuffle`：同优先级组内，加权随机**不放回**抽样——每轮按剩余权重占比抽一个放到结果尾部。既保证首选符合权重分布，又给出完整的故障转移次序。
- `orderCandidates`：优先级降序分组 → 每组 weightedShuffle → 拼接。

### breaker.go（熔断器）

每渠道一个，并发安全，标准三态：

- **Closed**：正常放行；连续失败达 `FailureThreshold`（默认 5）转 Open。
- **Open**：拒绝放行；冷却 `CooldownPeriod`（默认 30s）后转 HalfOpen。
- **HalfOpen**：放行至多 `HalfOpenMaxProbes`（默认 1）个探测；成功转 Closed，失败立即转回 Open。

两个准入方法（**区别很重要**）：
- `Ready()`：**无副作用**，供 `Router.Select` 过滤候选，不消耗半开探测额度。
- `Allow()`：**有副作用**，返回绑定当前 generation 的一次性 `BreakerPermit`；随后必须且只能调用 `RecordSuccess` / `RecordFailure` / `RecordNeutral` 之一。客户端取消和下游写失败用 neutral，释放半开额度但不改变健康度；旧 generation 的迟到结果被忽略。

`now func() time.Time` 便于测试注入时钟。

### router.go

- `snapshot`：不可变渠道集合 + `byModel` 预建索引（对外模型名 → 渠道列表）。
- `Router`：`snap atomic.Pointer[snapshot]` 无锁读；`breakers` 每渠道一把锁，独立于快照（快照替换时按 ID 保留既有熔断状态）；`rngPool sync.Pool` 提供并发安全随机源。
- `UpdateChannels`：原子替换快照实现**热更新**，同步熔断器集合（保留仍存在渠道的状态、清理已移除的）。
- `Select(model)`：返回有序 `[]Candidate`（Channel + Breaker）；无渠道或全被熔断返回 `ErrNoChannel`。用 `Ready()` 过滤，避免为未真正尝试的候选消耗探测额度。

**并发**：改动 routing 必跑 `CGO_ENABLED=1 go test -race ./internal/routing/...`。已有 `concurrent_test.go` 专门压这条路径。

## internal/redisx —— Redis 客户端封装

共享的 go-redis v9.7.3 客户端。`New(cfg RedisConfig)` 支持 ACL username/password、TLS 1.2+、自定义 CA 与可选 mTLS，并做 5s PING；失败关闭客户端。`ValidateSecurity` 在 release 拒绝远程明文 Redis，loopback 可明文，可信隧道只能通过 `allow_insecure_remote` 显式接受风险。Redis 承担 API/登录限流与控制台会话，但不参与资金变动。

## internal/store —— 数据访问层

中间件只依赖 `Store` 接口，不关心数据来源。提供**内存**与 **PostgreSQL** 两套实现，由 `database.enabled` 决定用哪套，中间件层零改动（「架构干净可改」）。

### store.go（接口）

- `Identity`：一个 API Key 解析出的调用方身份——KeyID（限流维度/计费归因）、UserID、RateLimitPerMin（<=0 不限流）、AllowedModels（空表示不限制）、Enabled。方法 `Allows(model)` 做模型级准入。
- `Store` 接口：`ResolveKey(ctx, apiKey)`（不存在/禁用返回 `ErrKeyNotFound`）、`Balance(ctx, userID)`。实现须并发安全。
- `AdminStore` 接口：管理面数据访问，覆盖用户（创建/列表/取详情/启停/`AddBalance` 充值）、密钥（创建/列表/启停）、渠道（全 CRUD：创建/列表/取详情/更新/启停/删除）。内存与 PG 双实现。`AddBalance` 直接修改与账本共用的权威余额，不存在 Redis 余额同步步骤。

### memory.go（内存实现）

`MemoryStore`：`RWMutex` 保护，由 `[]KeySeed` 构建。除身份/余额副本隔离外，内存管理面与 PG 对齐：明文 key 与 key_id 都唯一，用户/Key 时间字段持久保存并在变更时更新，余额加减做 int64 溢出检查。`CreateAPIKeyLimited` 在同一把锁内完成数量检查与创建，避免并发越过自助上限。仅供 debug/测试。

### postgres.go（PostgreSQL 实现）

`PGStore`：用 `internal/db` 的 sqlc 查询器（`db.Querier`）实现 `Store` 接口。
- `HashAPIKey(apiKey)`：SHA-256 十六进制摘要。库里只存摘要不存明文；鉴权对客户端明文 Key 做同样哈希再比对（建密钥与解析两侧共用）。
- `ResolveKey`：哈希后 `ResolveAPIKey` 联表查（已过滤密钥与用户都 `enabled=TRUE`），`pgx.ErrNoRows` 映射为 `ErrKeyNotFound`。
- `Balance`：读冷源权威余额；用户不存在/禁用（`ErrNoRows`）按 **0 余额**返回（额度闸门自然拦截），不视作错误。
- 管理面 PG 外键错误映射为 `ErrNotFound`；数值边界在写入 int32 前校验。自助 `CreateAPIKeyLimited` 用按用户 advisory lock 串行化“计数 + 插入”。
- 并发安全（底层 `pgxpool` 并发安全）。

## internal/middleware —— HTTP 中间件

`/v1` 的实际前置链为 **来源 IP 预算 → AuthWithGate → 账户总桶 → 单 Key 桶**；资金预授权仍由 Forwarder 在解析模型/输出上限后执行。`ProtocolContext` 位于所有早退中间件之前，使 OpenAI/Anthropic 端点的 401/413/429/panic 500 使用对应 error schema。

### middleware.go（共享）

- `IdentityFrom(c)`：从 `gin.Context` 取鉴权阶段注入的身份。
- `abortError`：根据 `ProtocolContext` 输出 OpenAI `error` 对象或 Anthropic `type:error` envelope；非兼容管理端点沿用原 JSON 行为。

### auth.go

`AuthWithGate(store, semaphore)`：兼容 Bearer 与 `x-api-key`，拒绝超过 512 字节的 key；查 Store 前用非阻塞闸门限制并发，槽满 503，避免随机 key 洪泛占满 PG 连接池。入口另有未鉴权来源 IP 桶。

### ratelimit.go

`RateLimiter` 使用 Redis Lua 原子令牌桶和 Redis `TIME`，避免多实例本地时钟偏差。`AccountMiddleware` 先按 UserID 约束所有 Key 的账户总吞吐，再按 KeyID 执行单 Key 桶；Redis 故障限流 fail-open，但生成请求仍需 PostgreSQL 预授权。响应回写各自 limit/remaining 与 `Retry-After`。

### authlimit.go / csrf.go

- `IPRateLimiter`：匿名 `/auth/login`、`/auth/register` 在 bcrypt 前按来源 IP 使用 Redis 桶；Server 默认 `SetTrustedProxies(nil)`。
- `IdentifierRateLimiter`：把标准化登录标识做 SHA-256 摘要后按 endpoint scope 使用独立 Redis 桶，避免把用户名明文放进 keyspace；存在/不存在账户共用同一预算口径。
- release 启动校验要求 `/v1` 匿名/账户预算为正；启用管理面时，认证 IP、登录标识与活跃会话上限同样必须为正，禁止生产配置静默关闭。
- `Semaphore.TryAcquire`：非阻塞取得 bcrypt 或鉴权查库并发槽，满载立即 503，不堆积等待 handler/goroutine。
- `CSRFProtect`：Cookie 鉴权写请求强制 JSON，校验会话绑定 token 与 `X-CSRF-Token` 双重提交，并校验 Origin/Referer；GET 等安全方法放行。

`bodylimit.go` 用 `Content-Length` 快速拒绝并以 `http.MaxBytesReader` 限制 chunked body；`recovery.go` 不转储请求头，在 panic 后输出协议对应 500，让外层日志与 Metrics 仍能收尾。`protocol.go` 只按精确 method/path 设置协议，避免把管理面错误误包装。

### metrics.go

`Metrics()`：全局中间件，记录每个对外请求的计数与耗时。用 `c.FullPath()`（路由模板而非实际路径）作 `path` 标签，避免路径参数导致标签基数爆炸；未匹配路由归一为 `"unmatched"`。写入 `metrics.HTTPRequestsTotal` / `HTTPRequestDuration`。

### logger.go

`RequestLogger(logger, skip...)`：结构化访问日志中间件位于 Recovery 外层，`skip` 路径（`/healthz`、`/livez`、`/readyz`、`/metrics`）不记日志。

- **request_id**：入口永远生成服务端内部 ID，不复用入站 `X-Request-Id`，并限制上游透传头的名字和值长度。Forwarder 将内部 ID 作为 trace_id；账本再生成独立 reservation ID 作为资金幂等键与 `usage_logs.request_id`。
- **富字段回填**：转发层用 `SetLogModel` / `SetLogUpstream` / `SetLogUsage` 把模型/渠道/token 用量写入请求级 `accessLog` 载体（单请求 goroutine 顺序读写，无锁；无中间件时退化为无操作，故转发层单测无需挂它）。
- **输出**：收尾按状态码选级别（5xx→Error、4xx→Warn、其余→Info），字段含 request_id/method/path/status/latency_ms/client_ip/身份（user_id/key_id）/model/channel/用量，缺失字段省略避免噪声。
- **依赖方向**：`forwarder` 已依赖 `middleware`，故日志的 context 载体与 setter 定义在此包，由转发层回填（不反向依赖 forwarder）。

### session_auth.go

控制台会话鉴权（Task 14 起替换退役的 `admin_auth.go` 裸 token 方案）：

- `SessionAuthWithVersion(m, checker)`：读取会话后回查账户当前 session version；禁用/改密使代次变化时删除旧会话并 401，账户库异常返回 503。受保护的 `/auth`、`/me`、`/admin` 使用此版本。
- `RequireRole(role)`：在会话鉴权之后校验角色。**fail-closed**——取不到会话或角色不符均返回 401/403。`/admin` 使用 `SessionAuthWithVersion` + `RequireRole(account.RoleAdmin)` + `CSRFProtect`。
- `SessionFrom(c)`：handler 取当前登录身份，供 `/me` 把资源操作绑定到会话账户（越权硬约束的基础）。

## internal/account —— 账户认证领域

登录账户体系，与计费实体（`internal/store` 的 `users`）解耦：`accounts` 表存用户名 / bcrypt 密码哈希 / 角色 / 关联 `external_id`。

### account.go（领域模型 + 接口）

- `Account`：账户领域视图，**刻意无 `PasswordHash` 字段**——结构层杜绝哈希经 JSON 外泄；`Credentials`（不序列化）才含哈希，仅登录校验用。
- 角色常量 `RoleAdmin` / `RoleUser`，`ValidRole` 把关（非法角色 `ErrInvalidRole`）。预留 `GroupName` / `RateMultiplier`（整数百分比倍率）存而未用。
- `AccountStore` 接口：CreateAccount / CreateUserAccount（建 user 角色**原子连带**计费实体）/ GetByUsername / GetByID / List / SetEnabled / UpdatePassword。`SettingsStore` 接口：Get / Put（注册开关 + 新用户初始额度）。哨兵 `ErrNotFound` / `ErrConflict` / `ErrInvalidRole` / `ErrPasswordTooShort`。

### password.go（bcrypt 封装）

`HashPassword` 按 Unicode 字符数要求至少 8 个字符，并遵守 bcrypt 72 字节上限；错误分别为 `ErrPasswordTooShort` / `ErrPasswordTooLong`。绝不存明文。

### memory.go / postgres.go（双实现）

内存版用 map + 锁；PG 版 `CreateUserAccount` 走事务——同一事务内建 user 计费实体 + account 并回填 external_id，任一步失败整体回滚。`CreateAccount` 只允许无 external_id 的 admin，user 必须走原子连带入口。PG Settings 的 Get/Put 各用一条 SQL 读取/写入完整快照，避免两个 KV 键被并发读成混合版本。

## internal/session —— Redis 会话

控制台登录态载体。`Manager` 用 crypto/rand 生成不透明 token；Cookie 持有原值，Redis key 与账户 ZSET 成员只保存 SHA-256 摘要。`SessionData` 保存 AccountID/Username/Role/ExternalID、`CSRFToken` 与 `SessionVersion`：

- `Create(ctx, data, ttl)`：Lua 原子清理过期索引、检查每账户活跃上限、写 session 与 ZSET；超限返回 `ErrTooManyActiveSessions`。默认上限 10，可由 `admin.max_active_sessions_per_account` 配置。
- `NewCSRFToken()`：生成登录会话绑定的 CSRF token。
- `Get(ctx, id)` / `Delete(ctx, id)`：查 / 删（登出即删，服务端失效）。

## internal/billing —— 计费结算

持久预授权 + 状态机结算。`users.balance` 是生产唯一权威可用余额；Redis 完全退出资金路径。

### pricing.go

`ModelPolicy` 分别定义普通输入、输出、cache creation、cache read 四项单价，以及输入/输出 token 上界。`ReservationCost` 的输入维度取三类输入最高费率；`CostUsageChecked` 按四项真实 usage 独立计价，只有 total 时按所有价格中的最高值保守计价。total 小于缓存分项或与已知分项合计冲突时保留整笔预授权。所有乘加与向上取整做溢出检查；release 要求每个路由模型都有四项非零价格及显式输入/输出 token 上限。

### ledger.go / memory_ledger.go / postgres_ledger.go

`Ledger` 定义 `Reserve`、`MarkInFlight`、`ReleaseAttempt`、`RecordConsumption`、`Finalize`、`Refund`、`Recover`。reservation 状态迁移固定为：

`reserved → in_flight → consumed_unsettled → settled`；只有明确未消费响应允许 `in_flight → reserved`，终态退款为 `reserved → refunded`。

- `PostgresLedger.Reserve` 在事务内插入 reservation、条件扣减 `users.balance` 并追加 reserve 流水；余额不足整笔回滚。并发请求以数据库条件更新阻止超卖。
- `MarkInFlight` 在真实网络发送前同时提交状态与 channel。网络错误、408、5xx 等发送结果未知路径保留 in_flight，禁止自动换渠道和退款；`ReleaseAttempt` 只用于能证明未生成的明确 4xx。
- `RecordConsumption` 从 in_flight 持久化“上游已经消费”的事实；此后退款状态迁移非法。调用方会对瞬时错误做有限幂等重试；若它持续失败，精确 usage 尚无持久待办，reservation 保持冻结并转人工对账，因此审查 P1-12 只算部分修复。
- `Finalize` 在一笔事务内调整预授权差额、追加 settle 流水、写 `usage_logs`、标记 settled。`operation_id` 和 `(reservation_id, kind)` 唯一约束使提交结果未知后的重放幂等。
- `Refund` 只接受 reserved；in_flight / settled / consumed_unsettled 不得退款。生产 Recover 重试 consumed_unsettled，自动退款超过 5 分钟的孤立 reserved，只对超过 24 小时的 in_flight 返回歧义告警；新鲜 in_flight 被忽略，避免多实例正常活跃请求误报。
- `MemoryLedger` 实现同一状态契约，只供 debug 开发与单元测试。

### billing.go

`Billing` 聚合 `Pricing` 与 `Ledger`，对转发层暴露：

- `NormalizeMaxOutput` / `ValidateModel`：建立请求和 release 启动期的模型边界。
- `Reserve(ReserveRequest)`：按最大输入与本次强制输出上限计算最坏成本，生成服务端 reservation ID 并持久预授权。
- `Settle(reservation, channel, usage)`：完整 usage 按普通输入/输出/缓存创建/缓存读取独立费率计价；只有 total 时按最高单价；异常时保留整笔预授权并标记 estimated。Billing 与 Ledger 都拒绝实际 cost 超过 reservation。
- `Refund`：只用于能证明没有上游消费的失败路径。
- `Recover`：启动执行一次并由 main 每 30 秒重跑。完成 consumed_unsettled、回收超过 5 分钟的 reserved；超过 24 小时的 in_flight 保持冻结并告警人工按 channel 对账，不阻断新鲜 in_flight 所在的多实例启动。

## internal/db —— 数据库访问（sqlc 同构产物）

按 sqlc（engine=postgresql, sql_package=pgx/v5）生成约定**手写**的类型安全查询代码——当前环境无法联网装 sqlc 二进制，故手写等价物；能装 sqlc 后 `sqlc generate` 可原样覆盖，接口与调用方零改动。生成源见仓库根 `sqlc.yaml` / `db/schema.sql` / `db/query.sql`。

- `db.go`：`DBTX` 接口（pgx 连接抽象）+ `Queries` 骨架 + `New(db)` / `WithTx`。
- `models.go`：除 `User` / `ApiKey` / `Channel` / `UsageLog` 外，包含 `BillingReservation` / `BillingLedger`；时间列用 `pgtype.Timestamptz`。
- `querier.go`：`Querier` 接口（`emit_interface`），`Queries` 编译期断言实现之，便于上层 mock。
- `users.sql.go` / `api_keys.sql.go` / `channels.sql.go` / `usage_logs.sql.go` / `billing.sql.go`：各表查询实现。账本查询包含条件扣款、行锁状态迁移、资金流水与最终用量写入。
- `pool.go`：`NewPool` + 版本化迁移。`ApplySchema` 创建/读取 `schema_migrations`，校验 name/checksum，在事务 advisory lock 下初始化新库或顺序应用缺失的 `migrations/*.sql`；`VerifySchema` 在 `auto_migrate=false` 时拒绝缺版、漂移或降级运行。
- `schema.sql`：全新数据库快照，与根 `db/schema.sql` 同步；发布后的既有库变更必须新增不可改写的 migration，不能只修改 CREATE TABLE。

## internal/server —— HTTP 服务器

- `New(cfg, Deps)`：`SetTrustedProxies(nil)`；HTTP 设置 10s `ReadHeaderTimeout`、可配置 `ReadTimeout`/`IdleTimeout`/`MaxHeaderBytes`，整流不设 `WriteTimeout`，SSE 改由每次写入 30 秒 deadline 治理。控制台依赖缺失时整组不挂。
- `registerRoutes`：先 ProtocolContext，再 RequestLogger/Metrics/Recovery/BodyLimit。`/healthz`、`/livez` 只存活；`/readyz` 以 2s context 检查强依赖；`/metrics` 用专用 token、最大在途和超时预算。
- `/v1`：未鉴权 IP 桶 → `AuthWithGate` → AccountMiddleware → per-Key limiter；生成端点由 Forwarder 预授权，models 不触碰资金。
- `registerAuthRoutes`：register/login 在 bcrypt 前依次过 IP 与标识摘要预算，再 `TryAcquire`；成功登录受活跃会话原子上限。logout/me 挂 `SessionAuthWithVersion`。
- `registerMeRoutes`：`SessionAuthWithVersion` + `CSRFProtect`；用户资源归属绑定会话身份，操作他人 key 返回 404。
- `registerAdminRoutes`：`SessionAuthWithVersion` + `RequireRole(admin)` + `CSRFProtect`。下辖账户、设置、计费用户、密钥和渠道；裸 token `AdminAuth` 已退役。
- **关键**：故意不设覆盖整条响应的 `WriteTimeout`——SSE 流式响应可能持续数分钟；`stream.go` 对每次下行写入单独设置 30 秒 deadline，既保留长流又能清理慢读客户端。
- `Start` / `Shutdown` / `Addr`：生命周期。

## internal/forwarder —— 转发层（胶水）

把「适配器 + 路由 + 熔断 + 计费」串成真正发 HTTP 的胶水层，是唯一发起上游网络请求的地方。

- `channels.go`：`ChannelsFromConfig` / `ChannelsFromDB` 把 config 段与 DB 行统一转成 `routing.Channel`；坏的 models JSON 直接报错。`newSSEReader` 按 BOM 与 LF/CRLF/裸 CR 标准记录边界读取。
- `output_limit.go`：`OpenAIOutputLimitResolver` 按 `channel_id/upstream_model` 或 upstream model 选择 `max_tokens|max_completion_tokens`。release 启动验证所有启用 OpenAI 模型；候选确定后删除两个旧值并只写策略字段，发生在 prepare/MarkInFlight 前。
- `forwarder.go`：解析并最坏成本预授权后，每次真实发送前持久 `MarkInFlight(reservation, channel)`。明确未消费的 4xx 才 ReleaseAttempt；3xx、网络错误、408、5xx 停止重放并保留预授权。401/403/429 release 后可安全换渠道。
- `target_policy.go`：release 默认公共 HTTPS；精确 authority 规则才能放行 HTTP/私网 CIDR。URL 拒绝 userinfo/query/fragment/非规范路径/IPv6 zone；拨号期重新解析 DNS 并逐 IP 校验，防 DNS rebinding 和特殊地址 SSRF。
- `upstream.go`：prepare 与 send 分离，禁自动重定向。非流整体 120s、响应头 30s、TLS 握手 10s；SSE 不设整体 timeout，但 2min idle 会关闭 body。非流响应最大 32 MiB，SSE 单记录最大 4 MiB。
- `nonstream.go`：非流式链路解析 canonical usage 的已知位；missing / total-only / partial / conflict 都交给 Billing 保守结算，不能退化为 0/0。
- `stream.go`：标准 SSE 解析；最终 usage 必须绑定终止语义。每次下行写前用 `ResponseController` 刷新 30s deadline，慢读不会永久占用资源。Anthropic→OpenAI 输出标准 choices 空数组 usage 尾块；反向迟到 input 无法表达时保守结算。
- `models.go`：`Forwarder.Models()` 聚合所有启用渠道的对外模型名（去重、字典序），供 `/v1/models`。被熔断的渠道仍算「可服务」（熔断是瞬时健康状态）。
- **同协议保真路径**：格式相同且模型无重命名时响应短路 canonical；别名响应仍过 canonical。多 choice 不做静默截断：请求只允许 n=1，异常上游 choice/index 在流/非流显式失败，已消费后不重试。
- `response_error.go`：跨格式错误经 `ErrorCodec` 转换；只转发 `Retry-After`、限流头等 allowlist，并把上游请求 ID 改放 `X-Upstream-Request-Id`；拒绝 Cookie、连接语义和超长/含控制字符的头。
- **候选失败语义**：明确 401/403/429 等拒绝可在 ReleaseAttempt 后安全换候选；普通确定性 4xx 透传并退款。网络错、408、5xx 可能已被上游消费，保留 in_flight、停止自动重放并要求后续对账。

## internal/admin —— 管理面服务

用户 / 密钥 / 渠道的增删改查编排层，供 `/admin` REST 端点调用。

- `store.go`：`AdminStore` 接口 + 领域类型。数值写入有安全范围；`CreateAPIKeyLimited` 在存储层原子执行自助数量上限，`ErrLimitReached` 映射为明确 409。计费用户/账户列表支持有界 limit/offset，Key 列表由每账户 50 硬上限约束。
- `channel_key_crypto.go` / `channel_key_migration.go`：渠道上游密钥使用 AES-256-GCM v1 envelope，随机 nonce、key id 与 `channel_id` AAD；PG CRUD 只写密文并在存储边界解密。启动期在单事务内锁定并验证全部行，默认发现历史明文即失败；只有显式迁移开关允许一次性改写，schema 的 `NOT VALID` 约束同时阻止新增明文。
- `service.go`：`Service` 门面聚合 `AdminStore` + `*routing.Router` + logger。核心职责：**渠道写操作（增/改/删/启停）落库后即时 `ReloadChannels`**——从存储全量拉渠道 `router.UpdateChannels` 原子热替换，无需重启。热更新失败仅记日志不影响写本身（定时重载最终收敛）。`router` 可为 nil（纯用户/密钥管理场景）。
- `channels.go`：`ChannelsToRouting` 把 admin 领域渠道转成 `routing.Channel`（复用给热更新与定时重载）。
- `keygen.go`：生成 API Key 明文 + 其 SHA-256 摘要。创建密钥时明文只返回一次，库里只落摘要。
- 充值 `AddBalance` 直接更新与 PostgresLedger 共用的 `users.balance`；Redis 中没有余额副本需要刷新。

## internal/metrics —— Prometheus 指标

集中定义并注册网关指标，包级单例注册到默认 Registry。`/metrics` 需 `server.metrics_token`，并由 `metrics_max_requests_in_flight` / `metrics_timeout_seconds` 约束采集资源。标签只用有限枚举，避免高基数爆炸。

- HTTP 层：`HTTPRequestsTotal`（path/method/status）、`HTTPRequestDuration`（path/method 直方图）。
- 上游层：`UpstreamRequestsTotal`（channel_id/format/result）、`UpstreamRequestDuration`（channel_id/format 直方图）。
- 熔断：`CircuitBreakerState` gauge（0=closed 1=half-open 2=open）。
- 辅助函数：`ObserveUpstream(channelID, format, success, seconds)`、`SetBreakerState(channelID, state)` 供转发层埋点。

## internal/config —— 配置

九段配置：Server / Database / Redis / Log / Auth / Admin / Billing / Upstream / Channels。Server 管理入站 body/header/read/idle 与 metrics token/max-inflight/timeout；Redis 支持 ACL/TLS 与远程明文策略；Auth 管 `/v1` 未鉴权来源预算与账户总桶；Admin 管认证 IP/登录标识预算、活跃会话上限、bootstrap/热重载；Upstream 以 authority 规则授权 HTTP/私网 CIDR。Billing 仍要求四项价格、输入/输出上界和每个 OpenAI channel/upstream_model 的唯一输出字段策略。数据库模式必须配置 channel encryption key。

## cmd/linapi —— 入口

配置加载后执行 release 防线：PostgreSQL、迁移版本、Redis 传输、metrics token、模型计费/输出字段、上游目标策略和渠道密钥加密均 fail-closed。数据库模式先 ApplySchema/VerifySchema，再执行历史渠道明文迁移与约束验证。启动以 10s 预算 Recover；普通错误致命，歧义 reservation 告警冻结。周期每 30s 恢复；关闭时停止 goroutine。
