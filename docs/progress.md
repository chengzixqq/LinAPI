# 开发进度

> 更新日期：2026-07-11

## 当前协作状态（重要）

> 2026-07-10 已完成三轮多智能体全面只读审查，累计确认 65 项（P0 7 / P1 34 / P2 24）。第三轮为安全专项，新增 14 项，覆盖免费额度套取、CSRF、认证滥用、SSE 慢读、SSRF、Redis/上游密钥保护、匿名资源耗尽和依赖公告。完整证据、稳定问题 ID、修复批次和验收矩阵见 [`reviews/2026-07-10-comprehensive-readonly-audit.md`](reviews/2026-07-10-comprehensive-readonly-audit.md)。
>
> **控制台与协议安全批次（截至 2026-07-11）**：先修控制台安全 + 即时止损，再做前端（Plan 2）。当前工作区已闭合或收敛：
> - **批次 A（即时止损）**：AUD-P1-01（`/models` 误扣押金，拆分 /v1 中间件）、AUD-P1-03（余额不足 key 补 TTL）、AUD-P1-09（`.gitignore` 误伤 `cmd/linapi/`，已由 010b851 修）。
> - **批次 G（控制台安全）**：P0-07、P1-26、P1-27～29、P2-21/P2-23 已落地。登录/注册在 bcrypt 前有来源 IP + 登录标识摘要预算，`TryAcquire` 满载 503；Redis Lua 原子限制每账户活跃会话。自助 Key 强制 1..5000、存储层原子限制 50 把，并叠账户总桶；注册冲突统一成功响应，不泄用户名存在性。
> - **批次 E（账户/存储契约）**：P1-17 session_version、P1-18 Settings 单 SQL 快照、P2-08/09/11/12/13/18/19/20 已按内存/PG 一致性方向修复；版本化 schema 见 P2-10 的上线验收边界。
> - **批次 C/D/F/H（路由、协议、运行时与网络）**：breaker permit generation + neutral cancel、联合字段/developer/tool RawMessage、n=1 与异常 choices 显式拒绝、HTTP/SSE 超时/大小限制、SSRF 拨号策略、Redis TLS/ACL、渠道 key 加密、鉴权前闸门、metrics 防护和 go-redis v9.7.3 均已落地。
>
> main 端到端装配已完成（`cmd/linapi/main.go` 注入 Account/Settings/Session + `bootstrapAdmin` 播种首个管理员），标准二进制在 `admin.enabled=true` 下会挂载 `/auth`、`/me`、`/admin` 并受上述防护覆盖。本轮新增回归与最终全量验证结果见下方“测试现状”。
>
> **批次 B（计费一致性）P0 已完成代码修复**：`users.balance` 成为唯一权威余额；新增 reservation 状态机和只追加 ledger；Redis 退出资金路径。Forwarder 按最坏成本预授权，release 为每个启用的 OpenAI `channel/upstream_model` 强制配置 `max_tokens|max_completion_tokens`，候选确定后只写策略字段，再 MarkInFlight/send。3xx、网络错误、408、5xx 停止重放并保留预授权，明确未消费的 4xx 才 ReleaseAttempt。usage 缺失、部分或 total/cache 冲突均保留整笔预授权。Recover 只自动完成已持久化的 `consumed_unsettled`，并回收过期 reserved/告警过期 in_flight；`RecordConsumption` 持续失败时 usage 仍无持久待办，故 P1-12 只算部分修复。
>
> **仍未闭合的代码/协议窗口**：P1-12 中 `RecordConsumption` 持续失败仍没有独立持久 outbox，reservation 保持冻结转人工对账；P2-03 中 OpenAI→Anthropic 的迟到 input usage 因目标协议无等价位置无法在线表达，账本保留预授权而不伪造精确 usage。
>
> **上线条件仍未完成**：部署前必须冻结旧资金写入，人工对账 PostgreSQL、Redis 与供应商账单；在真实 PostgreSQL 演练版本化迁移、并发预授权、提交结果未知、崩溃恢复和重放；在真实 Redis TLS/ACL 环境验证连接与证书。数据库渠道 key 迁移需维护窗一次开启 `migrate_plaintext`，成功后关闭并轮换历史供应商 key。完成这些条件前只能称代码级修复。

## 七步计划

| # | 模块 | 状态 |
|---|---|---|
| 1 | 可启动的最小骨架（go.mod / 配置 / main / server / 健康检查） | ✅ 完成 |
| 2 | 内部规范数据模型 + 适配器接口与注册表 | ✅ 完成 |
| 3 | OpenAI + Claude 适配器（请求/响应/流式转换） | ✅ 完成 |
| 4 | 路由 / 负载均衡引擎（渠道组 / 优先级 / 权重 / 故障转移 / 熔断） | ✅ 完成 |
| 5 | 鉴权 + 限流中间件（Redis 仅作非资金基础设施） | ✅ 完成 |
| 6 | 计费结算（PostgreSQL 权威账本 + 最坏成本预授权） | ✅ 完成（代码级） |
| 7 | 数据库 schema + sqlc 集成 | ✅ 完成 |

> 七步全部完成。**转发 handler 已接线**（把适配器 + 路由 + 熔断 + 计费串起来真正发 HTTP），见下「第 8 步 · 转发层」。至此请求可端到端跑通。

## 已完成的细节

### 第 1 步 · 骨架
- Viper 配置（环境变量覆盖）、Gin server、优雅关闭；`/healthz`/`/livez` 提供进程存活探针，`/readyz` 以 2 秒预算检查 PostgreSQL/Redis 强依赖。
- 配置文件是可选输入：路径不存在时使用环境变量与默认值；其他读取/解析错误 fail-fast（AUD-P2-07 已修）。
- `config.example.yaml` 提供配置模板。

### 第 2 步 · 规范模型 + 适配器框架
- `internal/canonical`：支持有序 system/developer、文本/图片/thinking/tool block，工具参数保存原始 JSON 并用 `UseNumber` 防大整数损失；项目明确只支持 OpenAI n=1，异常多 choices 不静默截断。
- `internal/adapter`：`Adapter` 接口 + 全局注册表（`init()` 自动注册）。

### 第 3 步 · OpenAI + Claude 适配器
- 两个适配器均实现全部接口方法，含流式 SSE 编解码。
- 测试覆盖：round-trip（往返一致性）+ stream（流式分块）。

### 第 4 步 · 路由引擎
- 优先级分组 + 组内加权随机不放回抽样。
- 三态熔断器（Closed/Open/HalfOpen），`Ready()` 无副作用；`Allow()` 返回 generation permit，以 success/failure/neutral 一次性结束，旧代际迟到结果无效。
- `atomic.Pointer` 无锁读 + 渠道热更新（保留熔断状态）。
- 已通过 `go test -race`（数据竞争检测干净）。

### 第 5 步 · 鉴权 + 限流中间件
- `internal/redisx`：go-redis v9.7.3，支持 ACL、TLS CA/mTLS 与启动 PING；release 阻止未显式豁免的远程明文 Redis。
- `internal/store`：`Store` 接口（`ResolveKey` / `Balance`）+ 配置驱动的 `MemoryStore` 内存实现，
  第 7 步用 sqlc/PostgreSQL 实现同一接口替换，中间件层零改动。
- `internal/middleware`：
- **Auth**：兼容 Bearer / `x-api-key`，长度上限 512；查库前有来源 IP 桶和非阻塞并发闸门。
- **RateLimit**：Redis `TIME` + Lua 原子桶，先账户总预算再单 Key 预算。
- `/v1` 在所有早退中间件前注入协议上下文，OpenAI/Anthropic 的 401/413/429/panic 500 使用各自 error schema。生成端点由 Forwarder 预授权，`/v1/models` 不触碰资金。
- Server 设置 read/header/idle/body/header 大小边界；`/livez` 与 `/readyz` 分离，metrics 使用 token + max-inflight + timeout。
- 测试覆盖：store（身份/模型/余额/副本隔离）、auth（双头/401 路径）、rate limit 与生成端点 402 路径。

### 第 6 步 · 计费结算（持久预授权 + 幂等状态机）
- `internal/billing` 以 `Ledger` 为资金边界：生产使用 `PostgresLedger`，开发/测试使用 `MemoryLedger`。已删除 Redis `Account`、异步 `Recorder`/`PGSink` 与 `Quota` 资金路径。
- `Pricing` 为普通输入、输出、cache creation、cache read 分别配置单价，并配置输入/输出上界。预授权的输入维度按普通输入与两种缓存输入中的最高费率冻结；所有乘加与向上取整做溢出检查。release 要求每个已发布模型都有四项非零价格及显式输入/输出 token 上限。
- Forwarder 先解析请求、校验模型权限、规范化 `max_tokens`，再按“最大可计费输入 + 强制输出上限”计算最坏成本并调用 `Billing.Reserve`。PostgreSQL 用条件 `UPDATE` 在同一事务内扣减 `users.balance`，余额不足在上游 I/O 前返回 402，并发请求无法超卖。
- reservation 状态为 `reserved → in_flight(channel) → consumed_unsettled → settled`。只有明确 4xx 能证明未生成时，attempt 才可 `in_flight → reserved`；随后可安全换渠道或退款。终态退款仅允许 `reserved → refunded`。
- `RecordConsumption` 先持久化上游已消费事实（瞬时错误有限重试）；`Finalize` 在同一事务内退差、追加资金流水、写最终 `usage_logs`。网络错误、408、5xx 保留 `in_flight`，不得跨渠道重放或退款。MarkInFlight 后尚未执行网络发送，因此标记失败时先幂等 ReleaseAttempt；释放也失败才保守冻结。
- canonical usage 显式记录各字段是否已知。完整分项按四项费率计价；OpenAI cached tokens 从普通输入拆出按 cache read 计价。只有 total 时按所有价格中的最高值保守收费；total 小于缓存分项或与已知分项合计冲突时保留整笔预授权并标记 `estimated=true`。Billing 与 Ledger 双层拒绝 `cost > reservation.Amount`。
- `Billing.Recover` 启动执行一次，随后每 30 秒运行：幂等完成 `consumed_unsettled`，退款超过 5 分钟仍未发送的 `reserved`；超过 24 小时的 `in_flight` 返回歧义告警供人工按 channel 对账，但保持冻结并继续服务。新鲜 in_flight 不告警、不退款，避免多实例滚动启动误报。
- OpenAI 请求显式拒绝 `n != 1`。release 为每个启用的 `channel/upstream_model` 配置 `max_tokens|max_completion_tokens`；候选确定后删除两个同义字段，只写策略指定字段，再进入 MarkInFlight。stream 等安全字段总会重编码，精确重复 JSON key 被折叠。
- 测试覆盖状态机、独立缓存费率与最贵输入预授权、cost 上限、`n=1`、重复安全字段重编码、usage 完整性、并发预授权及发送结果未知不重放。真实 PostgreSQL 故障注入与旧余额对账仍是部署门槛。

### 第 7 步 · 数据库 schema + sqlc 集成
- **sqlc 工程**（仓库根）：`sqlc.yaml`（engine=postgresql, sql_package=pgx/v5）+ `db/schema.sql` + `db/query.sql`。资金相关新增 `users.balance_version`、`billing_reservations` 与只追加 `billing_ledger`；金额列统一 `BIGINT`，时间戳用 `timestamptz`。
- **⚠️ 手写同构产物**：当前环境无法联网装 sqlc 二进制，故 `internal/db/` 下的代码是**按 sqlc 生成约定手写**的等价物（`db.go` 骨架 + `models.go` 表模型 + `querier.go` 接口 + `*.sql.go` 查询实现）。一旦能装 sqlc，`sqlc generate` 可**原样覆盖**该目录，接口与调用方零改动。
- **PostgreSQL 实现 `store.Store`**：`store.PGStore` 用 sqlc 查询实现 `ResolveKey` / `Balance`。API Key 只存 SHA-256 摘要；`ResolveAPIKey` 联表过滤密钥与用户启用状态。资金准入由 PostgresLedger 的条件扣款决定，不依赖 Store 余额快照。
- **PostgreSQL 实现 `billing.Ledger`**：`PostgresLedger` 在事务内维护权威余额、reservation 状态、资金流水与最终用量日志；`usage_logs.request_id` 使用服务端生成的 reservation ID，外部 trace ID 单独保存在 reservation 中。
- **连接池 + 版本化迁移**：`db.NewPool` 建 pool + Ping。全新库用 embed schema；既有库由 `schema_migrations` + checksum + 事务 advisory lock 顺序执行 `internal/db/migrations/*.sql`。`auto_migrate=false` 时 `VerifySchema` 拒绝缺版本、迁移漂移和降级运行。
- **main 接线**：`database.enabled=true` 装配 `PGStore` + `PostgresLedger`；关闭数据库时使用同一 `MemoryStore` + `MemoryLedger`，仅供 debug 开发。release 模式强制启用 PostgreSQL。
- 迁移只处理 schema，不会把旧 Redis 消费反推到 PostgreSQL。真实 PG 增量升级尚未在本环境演练；首次部署新账本前仍须人工对账并校正 `users.balance`。

### 第 8 步 · 转发层（接线收尾）
- `internal/forwarder` 新增，把「适配器 + 路由 + 熔断 + 计费」串成真正发 HTTP 的胶水层：
  - **channels.go / output_limit.go**：统一 config/DB 渠道；坏的 models JSON 直接报错。OpenAI 输出字段 resolver 在 release 验证每个启用 `channel/upstream_model`，候选确定后只写唯一策略字段。
  - **forwarder.go**：先解析、强制 `n=1`/输出上限并持久预授权；候选级输出字段补丁和 prepare 完成后才 `MarkInFlight`。明确未消费的 4xx 才 ReleaseAttempt；发送结果未知时保留预授权并停止自动重放。
  - **target_policy.go / upstream.go**：release 默认公共 HTTPS；精确 authority 规则才可授权 HTTP/私网 CIDR。URL 静态校验 + 拨号期 DNS/IP 校验防 SSRF/rebinding。禁自动重定向；响应头 30s、非流整体 120s、SSE idle 2min，非流响应 32MiB、SSE 单记录 4MiB。
  - **nonstream.go**：非流式链路显式跟踪 usage 完整性；2xx 即视为已消费，缺失/部分 usage 走保守结算而非退款。
  - **stream.go**：标准 SSE 语义与最终 usage；每次下行写刷新 30s deadline。Anthropic→OpenAI 生成标准 `choices:[]` usage 尾块；反向迟到 input 不可表达时保守结算。
- **候选失败语义**：permit 区分 success/failure/neutral；客户端取消不污染 breaker。只有明确 allowlist 4xx 可 ReleaseAttempt；3xx、网络错、408、5xx 和未知自定义 4xx 保留 `in_flight`。异常多 choices/index 已消费后显式失败，不重试。
- **接线三处**：`cmd/linapi` 补 `_ "linapi/internal/adapter/all"` 空导入触发适配器注册；启动时 `buildDataLayer` 一并加载渠道（PG 从 `channels` 表、否则 config）喂给 `routing.NewRouter`；`server.Deps` 新增 `Forwarder`，`/v1/chat/completions`（openai）与 `/v1/messages`（anthropic）替换 501 占位。
- **新增汇总包** `internal/adapter/all`：集中 blank-import 各供应商适配器，供 main 一行导入。
- config `channels` 段 + `config.example.yaml` 补文档化示例（含跨供应商故障转移：对外 gpt-4o 回退到 Claude）。
- 测试覆盖：channels、SSE reader、非流式/流式成功结算、明确拒绝时的安全故障转移、发送结果未知时不重放/不退款、余额不足 402 与无渠道 503。

### 第 9 步 · 管理面 + 可观测性 + 直通优化（运维增强）
- **管理面 CRUD**（`internal/admin` + `internal/server/admin_handlers.go`）：用户/密钥/渠道的增删改查 REST API。
  - `store.AdminStore` 接口 + 内存/PG 双实现：用户增查改（`AddBalance` 直接修改与账本相同的权威余额）、密钥增查改、渠道全 CRUD。
  - `admin.Service` 门面聚合 AdminStore + Router；渠道写操作后触发 `router.UpdateChannels` 从 DB 重载，即时生效无需重启。
  - 本步骤最初使用独立 `AdminAuth` token；第 11～12 步已由账户会话、角色校验、session version 与 CSRF 完整替代，旧 token/loopback 配置已退役。
  - 密钥创建返回明文一次（此后只存 SHA-256 摘要），对齐主流网关做法。
- **渠道定时热重载**（`cmd/linapi`）：后台 goroutine 按 `admin.channel_reload_interval` 定期从 DB 重载渠道喂 `router.UpdateChannels`；与管理面写触发的即时重载互补。间隔 <=0 关闭。
- **Prometheus 指标**（`internal/metrics` + `internal/middleware/metrics.go`）：`client_golang` 注册指标 + `/metrics` 暴露端点。
  - HTTP 层：请求总数（按 path/method/status）、请求耗时直方图。
  - 转发层：上游调用总数（按渠道/格式/成败）、上游耗时直方图、每渠道熔断器状态 gauge。
  - `/metrics` 使用专用 bearer token，并限制最大并发抓取和单次超时；release 空 token 拒绝启动。
- **`/v1/models` 端点**：`Forwarder.Models()` 从路由引擎的启用渠道聚合去重对外模型名，`server.listModels` 按 OpenAI models 格式返回；替换原 501 占位。
- **同格式直通优化**（`forwarder`）：客户端格式 == 渠道格式且该模型无重命名（`forwardCtx.canPassthrough`）时短路 canonical 往返——
  - 请求侧总是重新编码 JSON，强制 `n=1`、服务端输出上限、stream 与 OpenAI 流式 usage 开关；重复安全字段被折叠，未知字段语义保留；
  - 非流式响应透传上游字节回客户端（跳过 `BuildResponse`，但仍 `ParseResponse` 提取 usage 计费）；
  - 流式响应逐字节透传上游 SSE 记录（跳过 `StreamEncoder`，但仍 `Decode` 累计 usage）；
  - 收益：无重命名响应避免 canonical 未建模字段丢失；同协议模型别名请求只在 raw JSON 上补丁 model/安全字段，响应仍经过 canonical；跨格式只保证已建模能力。
  - 重构：`tryCandidate` / `forwardNonStream` / `forwardStream` 签名统一收敛为 `forwardCtx`（聚合每次转发的不变量），消除参数爆炸。
- 测试覆盖：直通最小 JSON 合并后未知字段保真、重命名不走直通（改写上游模型名）、流式直通响应保真 + usage 仍计费。

### 第 10 步 · 结构化访问日志 + 管理面测试补全（质量增强）
- **结构化访问日志中间件**（`internal/middleware/logger.go`）：`RequestLogger` 全局挂载（`Recovery` 之后、最前），跳过 `/healthz`、`/metrics` 避免探活/抓取噪声。
  - **trace 与账单 ID 分离**：入口始终生成内部 request ID，不复用客户端 `X-Request-Id`；账本另用服务端 reservation ID。响应只透传 allowlist 上游头，值长度/控制字符受限。
  - **富字段**：转发层经 `SetLogModel` / `SetLogUpstream` / `SetLogUsage` 把模型/渠道/token 用量回填到请求级 `accessLog` 载体（单请求 goroutine 顺序读写，无锁）；收尾统一输出方法/路径/状态/耗时/客户端 IP/身份（user_id/key_id）/模型/渠道/用量，缺失字段省略。
  - **级别按状态码**：5xx→Error、4xx→Warn、其余→Info。
  - **协作方向**：`forwarder` 已依赖 `middleware`，故日志字段的 context 载体与 setter 定义在 `middleware`，转发层回填（不反向依赖）；`SetLog*` 在无中间件时（如转发层单测）退化为无操作。
  - main 按 `cfg.Log`（level + json/text）用 `buildLogger` 构建 slog logger，设为全局默认并注入 forwarder / admin.Service / server.Deps.Logger（原先各处传 nil）。
- **管理面测试补全**（此前 `internal/admin`、`internal/server` 零覆盖）：
  - `internal/admin`：MemoryStore 用户/密钥/渠道 CRUD（含冲突/未找到/分页/充值）、密钥对热路径即时可见与禁用即拒、Service 渠道写操作热更新 router（创建/删除/启停后 `router.Select` 立即反映）、nil router 不 panic、GenerateKey 前缀/长度/千次不重复。
  - `internal/server`：admin handler HTTP 全链路（无令牌 401、用户生命周期、密钥明文仅回显一次且列表不含明文、渠道上游 api_key 脱敏、非法 format 400、删除 204/再删 404）。
- `internal/middleware`：logger 中间件行为（始终生成内部 request_id、不信任入站 ID、响应头、skip 路径、字段回填、级别映射、无中间件不 panic）。

### 第 11 步 · 统一账户认证体系（控制台后端）
把管理面从「裸 token」升级为「账号密码 + 会话」的完整控制台后端，账户体系与计费实体解耦。分 16 个子任务经子代理驱动开发（每任务 TDD + 独立复核）落地。
- **账户领域**（`internal/account`）：登录账户（`accounts` 表：用户名/bcrypt 密码哈希/角色/关联 external_id）与计费实体（`users` 表）职责分离。`AccountStore` + `SettingsStore` 接口，内存与 PostgreSQL 双实现。建 user 账户**原子连带**创建计费实体并回填 external_id（PG 走事务，任一步失败整体回滚不留孤儿）。`Account` 领域视图刻意无 `PasswordHash` 字段（结构层杜绝哈希外泄），仅 `Credentials`（不序列化）含哈希供登录校验。角色仅 `admin`/`user`（`ValidRole` 把关）。预留 `group_name` / `rate_multiplier`（整数百分比倍率）存而未用。
- **密码哈希**（`internal/account`）：bcrypt；至少 8 个 Unicode 字符、最多 72 字节。绝不存明文。
- **会话管理**（`internal/session`）：Redis key/ZSET 只存 token 摘要；Lua 原子清理过期项并限制每账户活跃数量。`SessionData` 承载 CSRFToken/SessionVersion。
- **鉴权中间件**：受保护路由使用 `SessionAuthWithVersion`，禁用/改密后旧会话立即失效；`/admin` 叠 `RequireRole(admin)`，`/me` 与 `/admin` 写请求叠 `CSRFProtect`。
- **控制台端点**：`/auth`（register 受注册开关约束 / login / logout / me）、`/me`（用户自助：改自己的密钥，key 归属绑定会话身份，**越权硬约束**——操作他人 key 返回 404 而非 403，不泄存在性）、`/admin/accounts`（账户增删改查启停 + 重置密码）、`/admin/settings`（注册开关 + 新用户初始额度）。
- **鉴权收口**：`/admin` 由裸 token 改为 `SessionAuthWithVersion` + `RequireRole(admin)` + `CSRFProtect`；`/me` 使用 session version + CSRF；register/login 为匿名端点但先过来源 IP 限流和 bcrypt 并发闸门。各路由有 nil 依赖守卫。
- **启动播种**（`cmd/linapi`）：`bootstrapAdmin` 在配置了 `admin.bootstrap.username` 且该用户名不存在时播种首个管理员（幂等，不覆盖已有；密码为空则告警跳过，绝不建空密码账户；日志只记 username 不记密码）。密码建议经 `LINAPI_ADMIN_BOOTSTRAP_PASSWORD` 环境变量注入。
- config `admin` 段改造：去 `token` / `loopback_only`，加 `bootstrap`（username/password）；`SecureCookie = (server.mode == "release")`。schema 双写（`db/schema.sql` + `internal/db/schema.sql`）新增 `accounts` / `settings` 表 + `users.rate_multiplier` 列。
- **附带修复**：`.gitignore` 裸 `linapi` 规则改 `/linapi` 锚定仓库根——原规则误伤 `cmd/linapi/` 源码目录，导致入口 `main.go` 长期未被 Git 跟踪（对应审查文档 `AUD-P1-09`）。
- 测试覆盖：account（密码哈希、内存/PG 双实现 CRUD、建 user 连带计费实体、角色校验）、session（会话往返/TTL/删除）、server（/auth、/me 越权硬约束、/admin 账户/设置 HTTP 全链路、密码哈希不外泄、角色分流）、middleware（SessionAuth/RequireRole fail-closed）；全过 `-race`。

### 第 12 步 · 控制台安全加固（审查批次 A/G + P1-17）
在开前端（Plan 2）前先闭合控制台攻击面——开前端需 `admin.enabled=true`，会点亮 `/auth` `/me` `/admin` 全部端点。按 codex 审查（`docs/reviews/2026-07-10-*.md`）逐项 TDD 修复：
- **AUD-P1-01 · `/models` 误扣押金**：当时先把固定预扣只挂生成端点，确保 `/models` 不触碰资金；第 13 步进一步删除 Quota，预授权改为 Forwarder 解析后执行。
- **AUD-P1-03 · 余额不足 key 无 TTL**：当时为 Redis Account seed 补齐 TTL；第 13 步已删除整个 Redis 资金路径，因此该故障面不再存在。
- **AUD-P0-07 · 注册无限复制赠送额度**：自助注册恒绑定初始余额 0（忽略 `settings.NewUserInitialBalance`）；`putSettings` 拒绝把 `NewUserInitialBalance` 设为正数，双重堵死路径。发放额度只能走管理面主动建号 / 充值（可信操作）。
- **AUD-P1-26 · CSRF 防护**：`middleware.CSRFProtect` 对 Cookie 鉴权的写请求做①双重提交 token（会话绑定的 `CSRFToken` vs 请求头 `X-CSRF-Token`）②强制 `Content-Type: application/json`③Origin/Referer 校验。登录下发非 HttpOnly 的 CSRF Cookie 供前端读取回传；`/me` `/admin` 写端点全挂，GET 自动放行。
- **AUD-P1-27 · 登录注册滥用限速（代码已修）**：来源 IP + 登录标识摘要 Redis 桶在 bcrypt 前执行；Gin 不信任代理头；`TryAcquire` 满载 503；会话创建以 Lua 原子限制每账户活跃数量。release 启用管理面时三项预算/上限均强制为正，不能用零值关闭。
- **AUD-P1-28 · 自助 Key 无上限（已修）**：单 Key 1..5000；内存锁/PG advisory lock 原子执行“计数+创建”，每账户最多 50 把；`/v1` 叠账户总桶，无法用多 Key 线性放大吞吐。账户/用户列表有界分页，Key 列表由 50 上限约束。
- **AUD-P1-29 · 登出假成功**：logout 用独立 3s 超时 context 删会话（不复用请求 context，避免客户端断开取消删除），删除失败回 503 且不清 Cookie，绝不让用户误以为已安全登出而服务端 token 仍有效。
- **AUD-P1-17 · 会话撤销**：`accounts` 表加 `session_version`（schema 双写），禁用 / 改密时在数据层递增。登录把当前代次快照进会话，`SessionAuthWithVersion` 鉴权时回查账户当前代次比对：不一致即判定为陈旧会话，主动删除并 401；回查出错 fail-closed 回 503。在 `/auth`（logout/me）、`/me`、`/admin` 三处接线。使被盗 token、被禁用户 / 被禁管理员的旧 Cookie 立即失效。
- 全量 `go build` / `go vet` / `CGO_ENABLED=1 go test -race ./...` 全绿。逐项证据见审查文档第 10 节跟踪表。

### 第 13 步 · PostgreSQL 权威计费账本（审查批次 B）
- 闭合 `AUD-P0-01～06`：资金从 Redis Lua 迁到 PostgreSQL 权威余额与持久 reservation/ledger 状态机；预授权金额按模型可证明的最坏成本计算；usage 缺失不再按 0；上游已消费后结算失败不再退款。
- 随架构闭合 `AUD-P1-02`、`P1-06`、`P1-08`、`P1-11`、`P1-13`～`P1-16`、`P1-21`～`P1-24`、`P2-03` 的 Anthropic→OpenAI 方向、`P2-07`、`P2-14`、`P2-16`、`P2-23`。P1-23 以“请求 n!=1 提前拒绝 + 异常上游 choices/index 显式失败且不重试”闭合。`P1-12` 与 `P2-03` 反向迟到 usage 保持部分修复。
- release 强制 `database.enabled=true`，逐模型验证价格/边界，并逐一验证启用 OpenAI `channel/upstream_model` 的输出字段策略。启动 Recover 的普通错误拒绝服务；`ErrAmbiguousReservations` 仅告警并保持冻结，周期恢复错误记日志后继续重试。
- **迁移边界**：代码和 Schema 不能判断旧 Redis 热余额中包含哪些未落 PostgreSQL 的历史消费。首次上线前必须停写、人工对账并校正 `users.balance`，再验证余额总额与供应商账单；此步骤不允许由自动迁移猜测完成。
- **验证边界**：内存状态机和 PostgreSQL 查询/事务契约已有自动化测试；生产放行前仍需在真实 PostgreSQL 上验证并发预授权、执行成功但响应丢失、事务提交结果未知、进程崩溃后 Recover、重复 Finalize/Refund 等故障路径。

### 第 14 步 · PostgreSQL 渠道凭证加密（AUD-P1-33）
- `channels.api_key` 改为应用层 AES-256-GCM v1 envelope：`crypto/rand` nonce、显式 key id、`channel_id` AAD；PG 创建/更新只写密文，读取在 `AdminStore` 边界解密后才进入路由，管理 API 的 `Channel` 结构从类型层禁止序列化密钥。
- `database.enabled=true` 时主密钥缺失、非 32 字节 base64 或 key id 非法均 fail-closed；内存 debug 模式继续可用。主密钥建议经 `LINAPI_DATABASE_CHANNEL_KEY_ENCRYPTION_KEY_ID/KEY` 或 Secret Manager 注入。
- 旧库默认检测到任一明文即拒绝启动。维护窗口仅一次开启 `migrate_plaintext`，启动事务用 `FOR UPDATE` 锁定全表、验证已有 envelope、条件改写明文并整体提交；schema 的 `NOT VALID` 检查约束从安装起阻止新增明文，迁移后再全表验证。迁移完成后必须关闭开关并轮换原有供应商密钥。
- 渠道 PUT 省略 `api_key` 时由 SQL 原子保留旧密文；显式提供才换新密文。单元、SQL、配置、handler、`-race` 与 `go vet` 回归已覆盖。

### 第 15 步 · 审查剩余批次集中闭合
- **路由/取消**：P1-04/P1-05 用 generation permit 与 neutral cancel 解决半开名额泄漏和迟到结果污染。
- **超时与资源**：P1-07/P1-19/P1-20/P1-30、P2-05/P2-17/P2-22 已落实响应头/SSE idle/下行写期限、入站读闲置/body/header、live/ready、脱敏 Recovery、metrics token/max-inflight/timeout。
- **协议**：P1-10/P1-23/P1-25、P2-01/02/03/04 已落实联合字段、developer、tool_result 图片、RawMessage/UseNumber、n=1 明确边界、错误 envelope/安全头与标准 OpenAI usage 尾块。
- **网络与凭证**：P1-31/P1-32/P1-34、P2-24 已落实 URL+拨号双层 SSRF、Redis TLS/ACL/session digest、鉴权前 IP/并发闸门、内部 request ID/header 上限与 go-redis v9.7.3。真实 Redis TLS 集成尚未执行。
- **管理/数据契约**：P1-18、P2-06、P2-08～13/P2-15/P2-18～21 已按参数边界、原子快照、Unicode/bcrypt、账户不变量、PG/内存错误与数值语义、Redis TIME、唯一性/时间/溢出、注册枚举方向修复；P2-10 迁移框架代码完成但真实 PG 未验。

### 第 16 步 · 控制台前端（Plan 2，React + Mantine，已 embed）
- 技术栈：Vite 5 + React 18 + TypeScript + `@mantine/core`（配 `@mantine/form`/`hooks`/`modals`/`notifications` + `@tabler/icons-react`）。产物 `vite build` 输出到 `internal/server/web_dist`，由 `console.go` 的 `//go:embed all:web_dist` 打进二进制，`/console/*` 伺服 + SPA fallback（仅 `admin.enabled=true` 挂载，与认证端点同开关）。
- 目录 `web/`：`src/api`（client 注入 CSRF + 401 处理 / types / endpoints）、`src/stores/auth.tsx`（会话态）、`src/theme`（Mantine `createTheme`：主色 violet + 大圆角，`global.css` 布局补充）、`src/notify.ts`（Toast 等价封装）、`src/components`（ProtectedRoute/Layout/DataTable/ConfirmButton/PlaintextKeyModal）、`src/pages`（Login/Register + admin: Overview/Users/Channels/Accounts/Settings + portal: PortalHome/PortalKeys）。
- **UI 选型（规避抄袭观感）**：刻意避开 New API 同款 `@douyinfe/semi-ui`，换用 Mantine——组件库与视觉体系与 New API 无关联；主色 violet（非 Semi/AntD 默认蓝）、圆角加大差异化。暗色模式用 Mantine color scheme 机制（自带持久化 + 跟随系统 + `index.html` 防闪烁内联脚本）。业务逻辑层（`api`/`hooks`/`stores`/`text.ts`）与 UI 无关，换皮零改动。
- **CSRF 对接**：`api/client.ts` 非安全方法自 Cookie 读 CSRF token 注入 `X-CSRF-Token`，与后端 `CSRFProtect` 双重提交对齐；dev 用 vite proxy 字符串简写（`changeOrigin:false`）保持同源，Host 不改写，避免写请求 403。
- **契约对齐**：注册页明示"初始额度 0"（AUD-P0-07），设置页移除 `new_user_initial_balance` 输入（后端恒 0 且拒绝非 0 写入）；自助建 key 表单限速 1..5000、默认 60（对齐 me_handlers 硬约束）。
- **注册入口精确化（2026-07-11 补齐）**：后端新增公开只读端点 `GET /auth/registration-status`（匿名可达、同挂 IP 限流、读设置失败 fail-closed 返回 false），登录页据此决定是否渲染注册入口（查询失败保守隐藏），取代原"始终显示 + 点击后 403 兜底"的从简做法。TDD 覆盖 `TestRegistrationStatusReflectsSetting`。
- **构建门禁全绿**：`npm run build`（`tsc -b && vite build`，7002 模块）、`go build ./...`、`go test ./internal/server/...` 全通过。Mantine 迁移要点：`DataTable` 基于 `Table` 基元 + 客户端分页自实现并保留 `ColumnDef`（title/dataIndex/render）抽象让页面零改动；`Popconfirm`→`modals.openConfirmModal`、`SideSheet`→`Drawer`、`Form`→`@mantine/form`；渠道表单 `useForm<ChannelFormValues>` 显式标注保住 `format` 字面量联合。
- **embed 占位文件自愈（2026-07-11）**：`vite.config.ts` 的 `emptyOutDir:true` 每次构建会清空 `web_dist` 连带删掉被 git 追踪的 `.gitkeep`，而 `//go:embed all:web_dist` 要求该目录在编译期存在——全新 checkout 若无产物又丢占位文件会导致 `go build` 失败。加 `keepGitkeep` 插件在 `closeBundle` 收尾重建 `.gitkeep`，让产物目录永远可编译。
- **API 端到端已验（2026-07-11，运行中网关 `config.dev.yaml`）**：预编译 `bin/linapi.exe` + `cmd/devredis` 起本地栈，对活网关跑通 login→me→CSRF 拦截/放行→登出全链路，并逐项验证本轮安全批次：CSRF 写请求缺 `X-CSRF-Token` 403 / 带正确 token 201；P0-07 开注册后自助注册新用户余额恒 0；P1-28 建 key 限速 0/6000 越界 400、60 合法 201 且明文只回显一次；P1-17 admin 改密后旧会话立即 401、新密码可登录。构建门禁全绿（typecheck / go build / vite build 7002 模块 / go vet）。
- **待验收边界**：真机浏览器可视化 UI 走查（点击式）本轮未做——chrome-devtools MCP 写死找 Chrome，本机仅 Edge 150，改由用户在 Edge 手动按自测清单点检；功能契约已由上述 API 端到端覆盖。打包体积单 chunk >500KB（未做 code-split，非阻塞）。

## 测试现状

- 2026-07-11 当前工作区已新增 breaker generation/neutral、协议 union/developer/tool/choices、HTTP/SSE timeout、SSRF、登录/Key 限额、Redis TLS/session digest、渠道加密、迁移、协议错误、metrics 预算等回归；最终提交以根任务本轮全量 go test/vet/race 结果为准。
- 真实 PostgreSQL 的版本升级、账本故障注入/重放及旧余额对账未在本环境完成；真实 Redis TLS/ACL 连接也未跑。这些是上线验收缺口，不得由 mock/单元测试替代。

## 端到端现状

七步骨架 + 转发层 + 安全批次已齐：协议上下文/body/recovery → 未鉴权 IP/查库闸门 → 账户/Key 限流 → 解析并强制模型上限 → PG 最坏成本预授权 → generation permit → 候选字段与 SSRF 校验 → MarkInFlight → 带超时发送 → 反向转换/安全错误头 → 持久结算。usage 缺失/冲突保守结算，只在明确未消费时故障转移。

## 后续可选增强（非阻塞）

当前实现已可用且具备基本运维能力（管理面 / 指标 / 热重载 / 直通优化已落地）。以下为仍可继续的增强：
- **计费上线验证不是可选增强**：旧余额人工对账与真实 PostgreSQL 故障注入尚未完成，完成前不得把 release 部署视为财务正确性已验收。
- **链路追踪**：结构化访问日志（`RequestLogger`，request_id 贯通）+ Prometheus 指标已铺开，但尚无分布式追踪（OpenTelemetry span 传播）。
- **控制台前端**：✅ 已完成（第 16 步）。React + Mantine 控制台已 embed 进二进制，`/console/*` 伺服；登录/注册 + admin 管理台（概览/用户/渠道/账户/设置）+ user 面板（我的概览/我的密钥）齐备。UI 刻意避开 New API 同款 Semi 以规避抄袭观感。真实浏览器端到端联调待跑。
- **安全上线验证**：真实 Redis TLS/ACL 集成、真实 PG 迁移/故障注入、渠道明文维护迁移与历史 key 轮换尚未完成；这些不是可选增强。
- **认证后续增强**：基础 IP/标识/bcrypt/会话/账户总桶/原子 Key 上限已落地；更细粒度 RBAC、专用安全审计日志与可选 MFA 属后续增强。
- **更多供应商适配器**：当前 openai / anthropic 两家；Gemini 等可按注册机制扩展。
- **sqlc 为手写同构产物**：`internal/db/` 是按 sqlc 约定手写的等价代码（环境无法联网装 sqlc）；能装 sqlc 后 `sqlc generate` 可原样覆盖。改表结构时记得同步根 `db/schema.sql` 与 `internal/db/schema.sql` 两份。
