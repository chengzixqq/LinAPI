# 模块详解

## internal/canonical —— 规范数据模型

纯数据定义，无逻辑。是所有格式转换的中枢。

### message.go（请求侧）

- `Request`：模型名、System（提到顶层，**不作为消息角色**）、Messages、Tools、ToolChoice、采样参数（指针类型以区分「未设置」与「显式零值」）、Stream、Metadata（保留无法归一化的供应商特有字段，直通时原样带回）。
- `Message`：`Role`（仅 user / assistant）+ `[]ContentBlock`。
- `ContentBlock`：**带类型的联合结构**，`Type`（BlockType）决定哪些字段有效：
  - `text` → Text
  - `image` → Image（url 或 base64）
  - `thinking` → Thinking + ThinkingSignature
  - `tool_use` → ToolUseID / ToolName / ToolInput
  - `tool_result` → ToolResultID / ToolResult / ToolResultError
  - `CacheControl` 标记该块参与提示缓存（Claude prompt caching）
- 工具结果作为 **user 消息里的 `tool_result` block** 承载，对齐 Claude 结构（而非 OpenAI 的独立 tool 角色消息）。

### response.go（响应侧）

- `Response`：非流式响应。ID / Model / Role / Content / StopReason / Usage。
- `StopReason`：归一化枚举（end_turn / max_tokens / tool_use / stop / error），各家停止原因双向映射。
- `Usage`：输入/输出 token + 缓存相关 token，计费依赖它。
- `Event`：**语义化规范流式事件**，不绑定任何一家 SSE 格式：
  - message_start / block_start / block_delta / block_stop / message_delta / message_stop / ping / error
- `Delta`：流式增量内容，按块类型区分（文本 / thinking / 工具参数 JSON 分片）。

## internal/adapter —— 供应商适配器

### adapter.go（接口）

`Adapter` 接口方法：`Name` / `ParseRequest` / `BuildRequest` / `ParseResponse` / `BuildResponse` / `NewStreamDecoder` / `NewStreamEncoder`。

**并发约定**：`Adapter` 方法无状态、并发安全（同实例被多请求并发调用）。流式需跨块状态，故通过工厂方法为每个流式请求创建独立的 `StreamDecoder` / `StreamEncoder`（有状态、非并发安全）。

- `StreamDecoder.Decode(raw)`：处理一个上游 SSE 数据块，返回 0..N 个规范事件（心跳/空行可能产 0 个）。
- `StreamEncoder.Encode(event)`：把一个规范事件编码为目标 SSE 字节，返回 nil 表示该事件在目标格式下无需输出。

### registry.go（注册表）

全局注册表。`Register`（重名 panic）/ `Get` / `MustGet` / `Names`。新增供应商只需在包 `init()` 里 `adapter.Register(&Adapter{})`。

### 已实现适配器

- `adapter/openai`：OpenAI Chat Completions 格式。
- `adapter/anthropic`：Anthropic Messages 格式（Claude）。

每个包按职责拆分：`adapter.go`（注册）/ `request*.go`（请求转换）/ `response.go` / `stream_decode.go` / `stream_encode.go` / `types.go`（线格式结构体）。测试：`roundtrip_test.go`（canonical → 线格式 → canonical 往返一致）+ `stream_test.go`。

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
- `Allow()`：**有副作用**，真正发起尝试前调用；随后**必须**配对 `RecordSuccess` / `RecordFailure`，否则半开探测额度泄漏、熔断器卡死。

`now func() time.Time` 便于测试注入时钟。

### router.go

- `snapshot`：不可变渠道集合 + `byModel` 预建索引（对外模型名 → 渠道列表）。
- `Router`：`snap atomic.Pointer[snapshot]` 无锁读；`breakers` 每渠道一把锁，独立于快照（快照替换时按 ID 保留既有熔断状态）；`rngPool sync.Pool` 提供并发安全随机源。
- `UpdateChannels`：原子替换快照实现**热更新**，同步熔断器集合（保留仍存在渠道的状态、清理已移除的）。
- `Select(model)`：返回有序 `[]Candidate`（Channel + Breaker）；无渠道或全被熔断返回 `ErrNoChannel`。用 `Ready()` 过滤，避免为未真正尝试的候选消耗探测额度。

**并发**：改动 routing 必跑 `CGO_ENABLED=1 go test -race ./internal/routing/...`。已有 `concurrent_test.go` 专门压这条路径。

## internal/redisx —— Redis 客户端封装

共享的 go-redis v9 客户端。`New(cfg RedisConfig)` 按配置构建客户端并做一次 PING 连通性探测（5s 超时），失败即返回错误——Redis 是限流/额度的强依赖，启动阶段就应暴露问题。探测失败会关闭客户端避免连接泄漏。

## internal/store —— 数据访问层

中间件只依赖 `Store` 接口，不关心数据来源。提供**内存**与 **PostgreSQL** 两套实现，由 `database.enabled` 决定用哪套，中间件层零改动（「架构干净可改」）。

### store.go（接口）

- `Identity`：一个 API Key 解析出的调用方身份——KeyID（限流维度/计费归因）、UserID、RateLimitPerMin（<=0 不限流）、AllowedModels（空表示不限制）、Enabled。方法 `Allows(model)` 做模型级准入。
- `Store` 接口：`ResolveKey(ctx, apiKey)`（不存在/禁用返回 `ErrKeyNotFound`）、`Balance(ctx, userID)`。实现须并发安全。
- `AdminStore` 接口：管理面数据访问，覆盖用户（创建/列表/取详情/启停/`AddBalance` 充值）、密钥（创建/列表/启停）、渠道（全 CRUD：创建/列表/取详情/更新/启停/删除）。内存与 PG 双实现。充值 `AddBalance` 更新冷源余额后，由 `admin.Service` 负责同步 Redis 热副本。

### memory.go（内存实现）

`MemoryStore`：`RWMutex` 保护，由 `[]KeySeed` 构建（种子来自 config 的 `auth.keys`）。`ResolveKey` 返回身份**副本**防止外部改内部状态。额外提供 `AddBalance(userID, delta)` 原子增减，供计费模块过渡期使用。仅供开发/测试（`database.enabled=false`）。

### postgres.go（PostgreSQL 实现）

`PGStore`：用 `internal/db` 的 sqlc 查询器（`db.Querier`）实现 `Store` 接口。
- `HashAPIKey(apiKey)`：SHA-256 十六进制摘要。库里只存摘要不存明文；鉴权对客户端明文 Key 做同样哈希再比对（建密钥与解析两侧共用）。
- `ResolveKey`：哈希后 `ResolveAPIKey` 联表查（已过滤密钥与用户都 `enabled=TRUE`），`pgx.ErrNoRows` 映射为 `ErrKeyNotFound`。
- `Balance`：读冷源权威余额；用户不存在/禁用（`ErrNoRows`）按 **0 余额**返回（额度闸门自然拦截），不视作错误。
- 并发安全（底层 `pgxpool` 并发安全）。

## internal/middleware —— HTTP 中间件

`/v1` 分组挂 **Auth → RateLimit → Quota**；`RequestLogger`（结构化访问日志）与 `Metrics` 为全局中间件（含 `/healthz`、`/v1`、`/admin`）；`AdminAuth` 独立守护 `/admin` 分组。

### middleware.go（共享）

- `IdentityFrom(c)`：从 `gin.Context` 取鉴权阶段注入的身份。
- `abortError`：统一 OpenAI 风格错误结构（`{"error":{"message","type"}}`）中止请求，便于客户端 SDK 直接消费。

### auth.go

`Auth(store)`：兼容两种客户端习惯——OpenAI 风格 `Authorization: Bearer sk-xxx` 与 Anthropic 风格 `x-api-key: sk-xxx`。解析身份注入 context；缺密钥/无效密钥返回 401，存储层异常返回 500（便于区分）。

### ratelimit.go

`RateLimiter` + `NewRateLimiter(rdb)`：Redis **Lua 原子令牌桶**。脚本在服务端原子执行（单次往返），避免「读→判断→写回」竞态；惰性补充（按距上次的时间差补令牌，无需后台定时任务），桶状态存 hash（tokens + ts）并设 TTL。按 KeyID 维度限流；`RateLimitPerMin <= 0` 直接放行。**Redis 故障时 fail-open**（放行而非拦截，避免限流组件抖动打挂网关，余额仍由 Quota 兜底）。回写 `X-RateLimit-*` 头，超限返回 429 + `Retry-After`。

### quota.go

`Quota(store, billing)`：请求前**原子预扣费闸门**。读冷源（store）余额作为 Redis 惰性初始化的 seed → `billing.Reserve` 原子预扣 `default_reserve` 押金 → 成功则把 `Reservation` 句柄注入 context，余额不足返回 402（`insufficient_quota`），计费服务异常返回 500。预扣时请求体尚未解析，`model` 留空；转发层解析出真实模型后用 `SetReservationModel(c, model)` 回填，供 `Settle` 计价。另提供 `ReservationFrom(c)` 供转发层取句柄结算。

### metrics.go

`Metrics()`：全局中间件，记录每个对外请求的计数与耗时。用 `c.FullPath()`（路由模板而非实际路径）作 `path` 标签，避免路径参数导致标签基数爆炸；未匹配路由归一为 `"unmatched"`。写入 `metrics.HTTPRequestsTotal` / `HTTPRequestDuration`。

### logger.go

`RequestLogger(logger, skip...)`：结构化访问日志中间件，全局挂载（`Recovery` 之后、最前），`skip` 路径（`/healthz`、`/metrics`）不记日志。

- **request_id**：入口优先复用入站 `X-Request-Id` 头（跨服务串联），否则生成 `req_`+hex；注入 context（`RequestIDFrom(c)` 取用）+ 回写 `X-Request-Id` 响应头。转发层复用该 ID，使访问日志与计费用量日志共享同一 request_id 对账。
- **富字段回填**：转发层用 `SetLogModel` / `SetLogUpstream` / `SetLogUsage` 把模型/渠道/token 用量写入请求级 `accessLog` 载体（单请求 goroutine 顺序读写，无锁；无中间件时退化为无操作，故转发层单测无需挂它）。
- **输出**：收尾按状态码选级别（5xx→Error、4xx→Warn、其余→Info），字段含 request_id/method/path/status/latency_ms/client_ip/身份（user_id/key_id）/model/channel/用量，缺失字段省略避免噪声。
- **依赖方向**：`forwarder` 已依赖 `middleware`，故日志的 context 载体与 setter 定义在此包，由转发层回填（不反向依赖 forwarder）。

### session_auth.go

控制台会话鉴权（Task 14 起替换退役的 `admin_auth.go` 裸 token 方案）：

- `SessionAuth(m *session.Manager)`：从 HttpOnly Cookie 取会话 ID，查 Redis 拿 `SessionData`（AccountID/Username/Role/ExternalID）注入 context；无 Cookie 或会话失效返回 401。
- `RequireRole(role)`：在 `SessionAuth` 之后挂，校验会话角色。**fail-closed**——取不到会话或角色不符均返回 401/403（绝不因取不到会话而放行）。`/admin` 用 `SessionAuth` + `RequireRole(account.RoleAdmin)`。
- `SessionFrom(c)`：handler 取当前登录身份，供 `/me` 把资源操作绑定到会话账户（越权硬约束的基础）。

## internal/account —— 账户认证领域

登录账户体系，与计费实体（`internal/store` 的 `users`）解耦：`accounts` 表存用户名 / bcrypt 密码哈希 / 角色 / 关联 `external_id`。

### account.go（领域模型 + 接口）

- `Account`：账户领域视图，**刻意无 `PasswordHash` 字段**——结构层杜绝哈希经 JSON 外泄；`Credentials`（不序列化）才含哈希，仅登录校验用。
- 角色常量 `RoleAdmin` / `RoleUser`，`ValidRole` 把关（非法角色 `ErrInvalidRole`）。预留 `GroupName` / `RateMultiplier`（整数百分比倍率）存而未用。
- `AccountStore` 接口：CreateAccount / CreateUserAccount（建 user 角色**原子连带**计费实体）/ GetByUsername / GetByID / List / SetEnabled / UpdatePassword。`SettingsStore` 接口：Get / Put（注册开关 + 新用户初始额度）。哨兵 `ErrNotFound` / `ErrConflict` / `ErrInvalidRole` / `ErrPasswordTooShort`。

### password.go（bcrypt 封装）

`HashPassword`（最短 8 位，短于则 `ErrPasswordTooShort`）/ `VerifyPassword`。绝不存明文。

### memory.go / postgres.go（双实现）

内存版用 map + 锁；PG 版 `CreateUserAccount` 走**事务**——同一事务内建 user 计费实体 + account 并回填 external_id，任一步失败整体回滚不留孤儿。唯一约束冲突（用户名）映射为 `ErrConflict`。

## internal/session —— Redis 会话

控制台登录态载体。`Manager`（`NewManager(rdb)`）用 crypto/rand 生成不透明会话 ID，`SessionData`（AccountID/Username/Role/ExternalID）JSON 存 Redis 带 TTL：

- `Create(ctx, data, rememberMe)`：签发会话，「记住我」延长 TTL。
- `Get(ctx, id)` / `Delete(ctx, id)`：查 / 删（登出即删，服务端失效）。

## internal/billing —— 计费结算

预扣费 + 按真实用量退差 + 用量日志异步落库。无状态、并发安全；余额一致性由 Redis 原子脚本保证。

### pricing.go

`Pricing` 计价表：`NewPricing(models, defaultInputPer1M, defaultOutputPer1M)`。单价单位为「最小计费单位 / 每 100 万 token」。`Cost(model, in, out)` 命中模型用其单价、否则用兜底价，除以 100 万时**向上取整**（`ceil`）——避免整数截断把小额用量算成 0，少收费。

### account.go

`Account` 是用户余额的 **Redis 热副本**，提供原子预扣/退差。核心是 `adjustScript` 一段 Lua：**惰性 seed**（key 不存在时用冷源余额初始化，故已扣费的热值不被旧初始值覆盖）→ **校验下限** → **INCRBY 调整**（走原字符串保证 64 位整数精确，不经 double）。

- `Reserve(userID, amount, seed)`：预扣押金，下限 0——扣后为负即拒绝（余额不足），余额不变。
- `Settle(userID, delta, seed)`：退差/补收，下限 `settleFloor`（极小负值）——**永远放行**，允许必要时轻微透支（用户已实际消费，欠费下次充值补齐，下一请求的 `Reserve` 会因余额不足拦截）。
- `Sync(userID, balance)`：用冷源权威余额 `SET` 覆盖热副本并续期。**充值场景专用**——惰性 seed 只在 key 不存在时初始化，改了冷源余额后 key 已存在不会重新 seed，必须主动 Sync 才能让新余额生效。

### recorder.go

用量日志异步落库。`Sink` 是落库接口（`Write(ctx, []UsageRecord)`，约定按 RequestID 幂等）；`NopSink` 丢弃一切（`database.enabled=false` 时用），启用 DB 时换 `PGSink` 批量 INSERT。

`Recorder`：带缓冲 channel + 后台 goroutine，攒够 `BatchSize` 或到 `FlushInterval` 即批量冲刷。**队列满时 `Record` 退化为同步写**（宁可慢也不丢账单）。`Close`（`sync.Once` 保证幂等 + 并发安全）关闭通道并等后台冲刷残留。落库失败仅记日志，不阻断主流程。

### pgsink.go（PostgreSQL Sink）

`PGSink`：用 `db.Querier` 把 `Recorder` 攒的批次逐条写入 `usage_logs`。底层 SQL `ON CONFLICT (request_id) DO NOTHING` 保证**按请求幂等**（进程崩溃重放不重复记账）。时间戳 `time.Time` → `pgtype.Timestamptz`。写失败向上抛给 Recorder 记日志（用量日志失败不阻断主流程）。

### billing.go

`Billing` 门面聚合上三者，对转发层暴露三步：

- `Reserve(ctx, userID, keyID, model, seed)`：预扣 `defaultReserve` 押金，返回 `Reservation` 句柄（余额不足 ok=false）。押金是「预授权」，可略高于预估以覆盖长回复。
- `Settle(ctx, r, channel, requestID, in, out)`：按真实用量算 `Cost`，退差 `押金 - 成本`（正退回/负补收），并异步记一条 `UsageRecord`。
- `Refund(ctx, r)`：转发彻底失败、无任何用量时全额退回押金。
- `SyncBalance(ctx, userID, balance)`：充值后用冷源余额刷新 Redis 热副本（转发/管理层调用）。

`Reservation` 随句柄携带 `Seed`，供 `Settle`/`Refund` 复用（Redis 若被逐出可重新惰性 seed）。

> **已接线**：转发层（`internal/forwarder`）在请求终局调用 `Settle`（成功且有用量，退差 + 记用量日志）或 `Refund`（全部候选失败、无用量，全额退押金）。

## internal/db —— 数据库访问（sqlc 同构产物）

按 sqlc（engine=postgresql, sql_package=pgx/v5）生成约定**手写**的类型安全查询代码——当前环境无法联网装 sqlc 二进制，故手写等价物；能装 sqlc 后 `sqlc generate` 可原样覆盖，接口与调用方零改动。生成源见仓库根 `sqlc.yaml` / `db/schema.sql` / `db/query.sql`。

- `db.go`：`DBTX` 接口（pgx 连接抽象）+ `Queries` 骨架 + `New(db)` / `WithTx`。
- `models.go`：表模型 `User` / `ApiKey` / `Channel` / `UsageLog`（时间列用 `pgtype.Timestamptz`）。
- `querier.go`：`Querier` 接口（`emit_interface`），`Queries` 编译期断言实现之，便于上层 mock。
- `users.sql.go` / `api_keys.sql.go` / `channels.sql.go` / `usage_logs.sql.go`：各表查询实现。联表查询 `ResolveAPIKey` 返回自定义 `ResolveAPIKeyRow`；`ListEnabledChannels` 供路由热加载渠道；`InsertUsageLog` 幂等 INSERT；`SumCostByUser` 时间窗对账。
- `pool.go`：`NewPool`（`pgxpool` + 启动期 Ping 探测）+ `ApplySchema`（`//go:embed schema.sql` 幂等建表）。
- `schema.sql`：运行时 embed 的迁移副本，与根 `db/schema.sql` 内容一致（改表两处同步）。

## internal/server —— HTTP 服务器

- `New(cfg, Deps)`：构建 Gin engine（`gin.New()` + Recovery），注册路由，配置 `http.Server`。`Deps{Store, Redis, Billing, Forwarder, Admin, Account, Settings, Session, SecureCookie, Logger}` 由 main 注入，便于测试替换（`Admin`/`Account`/`Session` 等为 nil 则不挂对应端点，fail-closed）。
- `registerRoutes`：全局挂 `RequestLogger`（结构化访问日志，跳过 `/healthz`、`/metrics`）+ `Metrics()` 中间件；`/healthz`（不走鉴权）；`/metrics`（Prometheus 暴露，不走鉴权，靠部署层网络隔离）；`/v1` 分组挂 Auth → RateLimit → Quota 三中间件，下辖 `/v1/chat/completions`（`Forwarder.Handler("openai")`）、`/v1/messages`（`Forwarder.Handler("anthropic")`）、`/v1/models`（`listModels`，从 `Forwarder.Models()` 聚合返回 OpenAI models 格式）；随后挂控制台路由 `registerAuthRoutes` / `registerMeRoutes` / `registerAdminRoutes`。
- `registerAuthRoutes`：`/auth` 分组——register / login 不鉴权（未登录才能用），logout / me 挂 `SessionAuth`。register 受系统设置的注册开关约束。
- `registerMeRoutes`：`/me` 分组挂 `SessionAuth`（任意登录角色），用户自助管理自己的密钥；资源归属绑定会话身份，操作他人 key 返回 404（越权硬约束）。
- `registerAdminRoutes`：`/admin` 分组挂 `SessionAuth` + `RequireRole(account.RoleAdmin)`（先会话后角色）。下辖账户（`/admin/accounts` 增删改查启停 + 重置密码）、系统设置（`/admin/settings` 读写）、计费用户（创建/列表/详情/启停/充值）、密钥（挂用户下）、渠道（全 CRUD）。各 `register*Routes` 均有 nil 依赖守卫（`admin.enabled=false` 或依赖未注入时不挂路由、不 panic）。**裸 token 的 `AdminAuth` 已退役**。
- **关键**：故意不设 `WriteTimeout`——SSE 流式响应可能持续数分钟，写超时会中途掐断长回复。
- `Start` / `Shutdown` / `Addr`：生命周期。

## internal/forwarder —— 转发层（胶水）

把「适配器 + 路由 + 熔断 + 计费」串成真正发 HTTP 的胶水层，是唯一发起上游网络请求的地方。

- `channels.go`：`ChannelsFromConfig` / `ChannelsFromDB` 把 config 段与 `db.ListEnabledChannels` 行统一转成 `routing.Channel`（DB 的 `models` JSON 列解析失败即报错）；`newSSEReader` 按空行切分上游 SSE 记录。
- `forwarder.go`：`Forwarder.Handler(clientFormat)` 返回 gin handler。主循环：按客户端格式 `ParseRequest` → `middleware.SetReservationModel` 回填模型供计价（并 `SetLogModel` 记访问日志）→ `router.Select(model)` 拿候选 → 逐候选 `Breaker.Allow()` 准入 → 发上游（并 `metrics.ObserveUpstream` 埋点）→ 按成败 `RecordSuccess/Failure`（并 `metrics.SetBreakerState`）决定是否故障转移。结算时 `SetLogUpstream`/`SetLogUsage` 回填渠道与用量到访问日志。终局：成功且有用量 → `billing.Settle` 退差；否则 `billing.Refund` 退押金。request_id 复用 `middleware.RequestIDFrom(c)`（未挂中间件时兜底自生成），使访问日志与用量日志同 ID 对账。`forwardCtx` 聚合每次转发在候选间共享的不变量（客户端格式/适配器、原始 body、规范请求、模型名、预扣句柄、请求 ID），避免逐候选透传大量参数；`canPassthrough(ch)` 判定是否可走同格式直通。
- `upstream.go`：`http.Client` 封装，构造带渠道凭证与上游模型名的请求；**故意不设整体超时**（SSE 长回复），仅设连接/握手超时。
- `nonstream.go`：非流式链路。直通时逐字节透传客户端 body 到上游、透传上游响应字节回客户端（仍 `ParseResponse` 提取 usage 计费）；否则 `BuildRequest`（渠道格式）→ 上游 → `ParseResponse` → `BuildResponse`（客户端格式）。
- `stream.go`：流式链路，逐 SSE 记录 `Decode` 累计用量。直通时原样透传上游 SSE 记录（`writeSSERecord` 补回记录边界，跳过 `StreamEncoder`）；否则 `Encode` 为客户端格式再 flush。**响应头惰性写出**（首块前才写），使响应提交点与 `committed` 标志一致：首块之前的上游失败仍可故障转移。
- `models.go`：`Forwarder.Models()` 聚合所有启用渠道的对外模型名（去重、字典序），供 `/v1/models`。被熔断的渠道仍算「可服务」（熔断是瞬时健康状态）。
- **同格式直通**（`canPassthrough`）：客户端格式 == 渠道格式且该模型无重命名时短路 canonical 往返——省一次编解码开销，且**彻底避免 canonical 超集未覆盖字段的丢失**（自定义/厂商扩展字段原样保留）。有重命名或跨格式则回退完整转换链路。
- **候选失败语义**：上游 5xx / 网络错 = 渠道故障（转移到下一候选）；上游 4xx = 客户端错（透传、不转移、不计用量）。

## internal/admin —— 管理面服务

用户 / 密钥 / 渠道的增删改查编排层，供 `/admin` REST 端点调用。

- `store.go`：`AdminStore` 接口 + 领域类型（`User` / `APIKey` / `Channel` + 各 `*Input`）。内存与 PG 双实现（`memory.go` / `postgres.go`），与 `store.Store` 复用同一套数据源选择逻辑。
- `service.go`：`Service` 门面聚合 `AdminStore` + `*routing.Router` + logger。核心职责：**渠道写操作（增/改/删/启停）落库后即时 `ReloadChannels`**——从存储全量拉渠道 `router.UpdateChannels` 原子热替换，无需重启。热更新失败仅记日志不影响写本身（定时重载最终收敛）。`router` 可为 nil（纯用户/密钥管理场景）。
- `channels.go`：`ChannelsToRouting` 把 admin 领域渠道转成 `routing.Channel`（复用给热更新与定时重载）。
- `keygen.go`：生成 API Key 明文 + 其 SHA-256 摘要。创建密钥时明文只返回一次，库里只落摘要。
- 充值 `AddBalance` 走 AdminStore 更新冷源余额，再由上层（handler）调 `billing.SyncBalance` 刷新 Redis 热副本。

## internal/metrics —— Prometheus 指标

集中定义并注册网关指标，包级单例注册到默认 Registry，经 server `/metrics` 暴露。**设计原则：标签基数可控**——只用有限枚举（path/method/status/format/result/channel_id）作标签，绝不把模型名、用户 ID 等高基数值放进标签，避免时间序列爆炸。

- HTTP 层：`HTTPRequestsTotal`（path/method/status）、`HTTPRequestDuration`（path/method 直方图）。
- 上游层：`UpstreamRequestsTotal`（channel_id/format/result）、`UpstreamRequestDuration`（channel_id/format 直方图）。
- 熔断：`CircuitBreakerState` gauge（0=closed 1=half-open 2=open）。
- 辅助函数：`ObserveUpstream(channelID, format, success, seconds)`、`SetBreakerState(channelID, state)` 供转发层埋点。

## internal/config —— 配置

Viper 加载，优先级：环境变量（前缀 `LINAPI_`，`.`→`_`）> 配置文件 > 默认值。配置文件缺失不报错。分七段：Server（port / mode）、Database（`enabled` 开关 + `dsn` + 连接池 + `auto_migrate` 启动建表）、Redis（addr / password / db）、Log（level / format）、Auth（`keys` 列表，驱动内存 Store，`database.enabled=true` 时退居开发用途）、Billing（`default_reserve` 默认预扣额 + 兜底单价 + `models` 计价表；含非零默认值，防止误配为 0 导致「免费」）、Admin（`enabled` 挂载控制台与 `/auth` `/me` `/admin` 端点、`bootstrap`〔首个管理员 username/password，幂等播种〕、`channel_reload_interval` 渠道定时热重载间隔秒〔默认 60，<=0 关闭〕）。**注**：Admin 段自控制台后端起已去除裸 `token` / `loopback_only`，改由「账号密码 + 会话 + admin 角色」鉴权。

## cmd/linapi —— 入口

配置加载 → 初始化 Redis（`redisx.New`，失败退出）→ `buildDataLayer` 选数据层（`database.enabled=true`：建 `pgxpool` +（可选 `auto_migrate`）建表 + 装配 `PGStore`/`PGSink`/`admin.NewPGStore`/`account.NewPGStore`，连不上致命退出；`=false`：内存 `MemoryStore` + `NopSink` + `admin.NewMemoryStore` + `account.NewMemoryStore`；account store 同时充当 AccountStore 与 SettingsStore）→ 加载渠道喂给 `routing.NewRouter`（PG 从 `channels` 表、否则 config 段）→ 构建计费门面（`buildBilling` 接收上一步选定的 Sink，持有 `Recorder` 供关闭时冲刷）→ 构建 `forwarder.New(router, billing, logger)` → 构建 `admin.NewService(adminStore, router, ...)` → `session.NewManager(rdb)` 会话管理器 → `bootstrapAdmin`（配置了 `admin.bootstrap.username` 且不存在则幂等播种首个管理员；密码空则告警跳过，日志不记密码）→ `server.New(cfg, Deps{Store, Redis, Billing, Forwarder, Admin, Account, Settings, Session, SecureCookie, Logger})`（`SecureCookie = server.mode=="release"`）→ `startChannelReload`（**仅 dbEnabled 且 `channel_reload_interval>0`** 才起后台 goroutine 定期 `ReloadChannels`；内存模式无共享存储，定时重载只会把本进程内存态原样写回，无意义）→ goroutine 启动 server → 监听 SIGINT/SIGTERM → 优雅关闭（30s 超时，`defer` 停 reload goroutine、`recorder.Close()` 冲刷残留用量日志、`pool.Close()` 关连接池）。空导入 `_ "linapi/internal/adapter/all"` 触发适配器注册。
