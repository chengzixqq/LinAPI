# 开发进度

> 更新日期：2026-07-10

## 当前协作状态（重要）

> 2026-07-10 已完成三轮多智能体全面只读审查，累计确认 65 项（P0 7 / P1 34 / P2 24）。第三轮为安全专项，新增 14 项，覆盖免费额度套取、CSRF、认证滥用、SSE 慢读、SSRF、Redis/上游密钥保护、匿名资源耗尽和依赖公告。完整证据、稳定问题 ID、修复批次和验收矩阵见 [`reviews/2026-07-10-comprehensive-readonly-audit.md`](reviews/2026-07-10-comprehensive-readonly-audit.md)。
>
> **控制台安全批次已修复（2026-07-10，master 分支）**：先修控制台安全 + 即时止损，再做前端（Plan 2）——因为开前端需 `admin.enabled=true`，会点亮整个控制台攻击面。本轮已闭合：
> - **批次 A（即时止损）**：AUD-P1-01（`/models` 误扣押金，拆分 /v1 中间件）、AUD-P1-03（余额不足 key 补 TTL）、AUD-P1-09（`.gitignore` 误伤 `cmd/linapi/`，已由 010b851 修）。
> - **批次 G（控制台安全）**：AUD-P0-07（注册恒不送额度 + putSettings 拒绝正初始余额）、AUD-P1-26（CSRF 双重提交 token + 强制 JSON + Origin 校验）、AUD-P1-27（登录注册 IP 令牌桶限速 + bcrypt 并发信号量）、AUD-P1-28（自助 Key 每账户 ≤50 把、限速 ∈[1,5000]）、AUD-P1-29（可靠登出：删除失败回 503 不谎报）；P2-21 用户名枚举随 login/register 恒定工作量一并闭合。
> - **批次 E（部分）**：AUD-P1-17（账户加 `session_version`，禁用/改密递增使旧会话立即失效；`SessionAuthWithVersion` 在 /auth、/me、/admin 三处接线）。
>
> main 端到端装配已完成（`cmd/linapi/main.go` 注入 Account/Settings/Session + `bootstrapAdmin` 播种首个管理员），标准二进制在 `admin.enabled=true` 下会挂载 `/auth`、`/me`、`/admin` 并受上述防护覆盖。全量 `go build`、`go vet`、`CGO_ENABLED=1 go test -race ./...` 全绿。
>
> **仍待处理**：批次 B（计费账本 AUD-P0-01～06 及相关 P1）需先做设计评审，本轮未动——在这些 P0/P1 关闭前，不应把项目视为已满足真实商用计费上线条件。批次 C/D/F/H 及其余 P2 亦待后续认领。逐项状态见审查文档第 10 节跟踪表。

## 七步计划

| # | 模块 | 状态 |
|---|---|---|
| 1 | 可启动的最小骨架（go.mod / 配置 / main / server / 健康检查） | ✅ 完成 |
| 2 | 内部规范数据模型 + 适配器接口与注册表 | ✅ 完成 |
| 3 | OpenAI + Claude 适配器（请求/响应/流式转换） | ✅ 完成 |
| 4 | 路由 / 负载均衡引擎（渠道组 / 优先级 / 权重 / 故障转移 / 熔断） | ✅ 完成 |
| 5 | 鉴权 + 限流 + 额度中间件（Redis） | ✅ 完成 |
| 6 | 计费结算（预扣费 + 异步落库） | ✅ 完成 |
| 7 | 数据库 schema + sqlc 集成 | ✅ 完成 |

> 七步全部完成。**转发 handler 已接线**（把适配器 + 路由 + 熔断 + 计费串起来真正发 HTTP），见下「第 8 步 · 转发层」。至此请求可端到端跑通。

## 已完成的细节

### 第 1 步 · 骨架
- Viper 配置（环境变量覆盖）、Gin server、优雅关闭、`/healthz`。
- `config.example.yaml` 提供配置模板。

### 第 2 步 · 规范模型 + 适配器框架
- `internal/canonical`：请求/响应/流式事件的完整数据模型（各家格式的超集）。
- `internal/adapter`：`Adapter` 接口 + 全局注册表（`init()` 自动注册）。

### 第 3 步 · OpenAI + Claude 适配器
- 两个适配器均实现全部接口方法，含流式 SSE 编解码。
- 测试覆盖：round-trip（往返一致性）+ stream（流式分块）。

### 第 4 步 · 路由引擎
- 优先级分组 + 组内加权随机不放回抽样。
- 三态熔断器（Closed/Open/HalfOpen），`Ready()` / `Allow()` 分离。
- `atomic.Pointer` 无锁读 + 渠道热更新（保留熔断状态）。
- 已通过 `go test -race`（数据竞争检测干净）。

### 第 5 步 · 鉴权 + 限流 + 额度中间件
- `internal/redisx`：共享 Redis 客户端封装，从 config 构建 + 启动期 PING 连通性探测。
- `internal/store`：`Store` 接口（`ResolveKey` / `Balance`）+ 配置驱动的 `MemoryStore` 内存实现，
  第 7 步用 sqlc/PostgreSQL 实现同一接口替换，中间件层零改动。
- `internal/middleware`：
  - **Auth**：兼容 `Authorization: Bearer` 与 `x-api-key` 两种头，解析身份注入 `gin.Context`。
  - **RateLimit**：Redis Lua 原子令牌桶（单次往返、惰性补充），按 KeyID 维度，Redis 故障时 fail-open。
  - **Quota**：请求前余额闸门（余额 <=0 返回 402），预扣费钩子留给第 6 步。
- 三者按 Auth → RateLimit → Quota 顺序挂在 `/v1` 分组；`server.New` 改为接收 `Deps{Store, Redis}` 注入。
- config 新增 `auth.keys` 段驱动 MemoryStore；对外错误统一 OpenAI 风格结构。
- 测试覆盖：store（身份/模型/余额/副本隔离）、auth（双头/401 路径）、quota（余额闸门 200/402）。

### 第 6 步 · 计费结算（预扣费 + 异步落库）
- `internal/billing` 新增，四个文件各司其职：
  - **pricing.go**：`Pricing` 计价表（模型单价 + 兜底价），单位「最小计费单位 / 每 100 万 token」。`Cost` 除法**向上取整**，避免整数截断少收费。
  - **account.go**：`Account` 是余额的 Redis 热副本。`adjustScript` 一段 Lua 原子完成「惰性 seed + 校验下限 + 调整」——`Reserve`（预扣，下限 0，余额不足即拒）与 `Settle`（退差/补收，下限 `settleFloor`，永远放行，允许必要透支）共用。INCRBY 走原字符串保证 64 位整数精确。
  - **recorder.go**：`Sink` 落库接口 + `NopSink`（当前）；`Recorder` 带缓冲 channel + 后台 goroutine 批量写（攒够 `BatchSize` 或到 `FlushInterval` 冲刷），**队列满退化为同步写**保证不丢账单，`Close`（`sync.Once`）冲刷残留。
  - **billing.go**：`Billing` 门面聚合三者，对转发层提供 `Reserve`（预扣押金→句柄 `Reservation`）/ `Settle`（按真实用量退差 + 记用量日志）/ `Refund`（转发全败全额退押金）。
- **预扣时机**：`Quota` 中间件升级为真正的预扣费闸门——读冷源余额作 seed，`billing.Reserve` 原子预扣 `default_reserve`，成功则把 `Reservation` 注入 context；余额不足 402。此时请求体未解析，`model` 留空，转发层解析后用 `middleware.SetReservationModel` 回填供计价。
- config 新增 `billing` 段：`default_reserve` + 兜底单价 + `models` 计价表；含非零默认值防「误配为 0 免费」。
- main 构建计费门面并持有 `Recorder`，优雅关闭时 `defer recorder.Close()` 冲刷用量日志。
- 测试覆盖：pricing（取整/兜底/nil）、account（seed/预扣/不足/透支/**并发 100 goroutine 不超卖**）、recorder（批量/定时/满兜底/幂等 Close）、billing（预扣→结算往返/不足/退款）；用 `miniredis` 真实执行 Lua 脚本，全过 `-race`。

### 第 7 步 · 数据库 schema + sqlc 集成
- **sqlc 工程**（仓库根）：`sqlc.yaml`（engine=postgresql, sql_package=pgx/v5）+ `db/schema.sql`（四表：users / api_keys / channels / usage_logs）+ `db/query.sql`（带 sqlc 注解的查询定义）。金额列统一 `BIGINT`（最小计费单位，杜绝浮点误差），时间戳 `timestamptz`，软删除用 `enabled` 布尔。
- **⚠️ 手写同构产物**：当前环境无法联网装 sqlc 二进制，故 `internal/db/` 下的代码是**按 sqlc 生成约定手写**的等价物（`db.go` 骨架 + `models.go` 表模型 + `querier.go` 接口 + `*.sql.go` 查询实现）。一旦能装 sqlc，`sqlc generate` 可**原样覆盖**该目录，接口与调用方零改动。
- **PostgreSQL 实现 `store.Store`**：`store.PGStore` 用 sqlc 查询实现 `ResolveKey` / `Balance`。API Key 只存 **SHA-256 摘要**（`HashAPIKey`），不落明文；`ResolveAPIKey` 联表过滤 `enabled=TRUE`，任一禁用/不存在映射为 `ErrKeyNotFound`；余额未命中按 0 返回（闸门自然拦截）。
- **PostgreSQL 实现 `billing.Sink`**：`billing.PGSink` 把用量日志写 `usage_logs`，SQL 用 `ON CONFLICT (request_id) DO NOTHING` 保证**按请求幂等**（进程崩溃重放不重复记账）。
- **连接池 + 建表**：`db.NewPool` 建 `pgxpool` + 启动期 Ping 探测；`db.ApplySchema` 用 `//go:embed schema.sql` 幂等建表（全部 `IF NOT EXISTS`）。`internal/db/schema.sql` 是运行时迁移副本，与根 `db/schema.sql` 内容一致（改表两处同步）。
- **充值热副本同步**：`billing.Account.Sync` / `Billing.SyncBalance` 用冷源权威余额 `SET` 覆盖 Redis 热副本并续期——补上惰性 seed 覆盖不到的充值场景。
- **main 接线**：新增 `buildDataLayer`——`database.enabled=true` 则建池 +（可选 `auto_migrate`）建表 + 装配 `PGStore`/`PGSink`（连不上视为致命）；`=false` 回退内存 `MemoryStore` + `NopSink`（本地开发免装 DB）。config `database` 段新增 `enabled` / `auto_migrate` 开关。
- 测试覆盖：PGStore（哈希稳定性/身份映射/`ErrNoRows`→`ErrKeyNotFound`/余额未命中归零，用 fake Querier）、PGSink（字段+时间映射/错误透传/空批次）、Account.Sync（覆盖旧热副本/新建热副本，用 miniredis），全过 `-race`。

### 第 8 步 · 转发层（接线收尾）
- `internal/forwarder` 新增，把「适配器 + 路由 + 熔断 + 计费」串成真正发 HTTP 的胶水层：
  - **channels.go**：`ChannelsFromConfig` / `ChannelsFromDB` 把两种渠道来源（config 段、`db.ListEnabledChannels` 行）统一转成 `routing.Channel`；DB 的 `models` JSON 列解析失败即报错（不静默污染路由）。含 `newSSEReader`：按空行切分 SSE 记录的读取器。
  - **forwarder.go**：`Forwarder.Handler(clientFormat)` 返回 gin handler。主循环：`ParseRequest`（客户端格式）→ 回填 `middleware.SetReservationModel` 供计价 → `router.Select(model)` 拿候选 → 逐候选 `Breaker.Allow()` 准入 → 发上游 → 按成败 `RecordSuccess/Failure` 并决定故障转移。终局统一结算：成功且有用量 → `billing.Settle` 退差；否则 `billing.Refund` 全额退押金。
  - **upstream.go**：`http.Client` 封装，`BuildRequest` 构造上游请求（注入渠道凭证与上游模型名），区分流式/非流式响应。**故意不设整体超时**（SSE 长回复），仅设连接/握手超时。
  - **nonstream.go**：非流式链路 `ParseResponse`（渠道格式）→ `BuildResponse`（客户端格式）→ 透传状态码与用量。
  - **stream.go**：流式链路，逐 SSE 记录 `StreamDecoder.Decode` → 累计用量 → `StreamEncoder.Encode` → flush。**响应头惰性写出**：首个输出块前才 `setSSEHeaders`，使「响应提交点」与 `committed` 标志一致——首块之前的上游失败仍可故障转移，已提交后再断则只结算不重试。
- **候选失败语义**：上游 5xx / 网络错 = 渠道故障（`RecordFailure` + 故障转移到下一候选）；上游 4xx = 客户端错（透传、不转移、不计费用量）。
- **接线三处**：`cmd/linapi` 补 `_ "linapi/internal/adapter/all"` 空导入触发适配器注册；启动时 `buildDataLayer` 一并加载渠道（PG 从 `channels` 表、否则 config）喂给 `routing.NewRouter`；`server.Deps` 新增 `Forwarder`，`/v1/chat/completions`（openai）与 `/v1/messages`（anthropic）替换 501 占位。
- **新增汇总包** `internal/adapter/all`：集中 blank-import 各供应商适配器，供 main 一行导入。
- config `channels` 段 + `config.example.yaml` 补文档化示例（含跨供应商故障转移：对外 gpt-4o 回退到 Claude）。
- 测试覆盖：channels（config/DB 转换、坏 JSON、空 models）、SSE reader（多记录/无尾空行/EOF）、集成（非流式成功+计费、跨格式 OpenAI→Anthropic、故障转移、全败 502 退款、上游 4xx 透传不转移、余额不足 402、无渠道 503）、流式（同格式直通、跨格式转换、chunk 计数、**故障转移前未提交可换渠道**）；全过 `-race`。

### 第 9 步 · 管理面 + 可观测性 + 直通优化（运维增强）
- **管理面 CRUD**（`internal/admin` + `internal/server/admin_handlers.go`）：用户/密钥/渠道的增删改查 REST API。
  - `store.AdminStore` 接口 + 内存/PG 双实现：用户增查改（含 `AddBalance` 充值，同步刷新 Redis 热副本）、密钥增查改、渠道全 CRUD。
  - `admin.Service` 门面聚合 AdminStore + Router + Billing；渠道写操作（增/改/删/启停）后触发 `router.UpdateChannels` 从 DB 重载，**即时生效无需重启**。
  - `/admin` 分组受独立 `AdminAuth` 中间件保护：独立 token（与 `/v1` 鉴权隔离）+ 可选回环地址限制（`loopback_only`）。
  - 密钥创建返回明文一次（此后只存 SHA-256 摘要），对齐主流网关做法。
- **渠道定时热重载**（`cmd/linapi`）：后台 goroutine 按 `admin.reload_interval` 定期从 DB 重载渠道喂 `router.UpdateChannels`；与管理面写触发的即时重载互补，兜底多实例部署下他实例的改动。间隔 <=0 关闭。
- **Prometheus 指标**（`internal/metrics` + `internal/middleware/metrics.go`）：`client_golang` 注册指标 + `/metrics` 暴露端点。
  - HTTP 层：请求总数（按 path/method/status）、请求耗时直方图。
  - 转发层：上游调用总数（按渠道/格式/成败）、上游耗时直方图、每渠道熔断器状态 gauge。
  - `/metrics` 不走鉴权，依赖部署层网络隔离（仅内网/监控可达）。
- **`/v1/models` 端点**：`Forwarder.Models()` 从路由引擎的启用渠道聚合去重对外模型名，`server.listModels` 按 OpenAI models 格式返回；替换原 501 占位。
- **同格式直通优化**（`forwarder`）：客户端格式 == 渠道格式且该模型无重命名（`forwardCtx.canPassthrough`）时短路 canonical 往返——
  - 请求侧逐字节透传客户端原始 body 到上游（跳过 `BuildRequest`）；
  - 非流式响应透传上游字节回客户端（跳过 `BuildResponse`，但仍 `ParseResponse` 提取 usage 计费）；
  - 流式响应逐字节透传上游 SSE 记录（跳过 `StreamEncoder`，但仍 `Decode` 累计 usage）；
  - 收益：省一次编解码开销 + **彻底避免 canonical 超集未覆盖字段的丢失**（自定义/厂商扩展字段原样保留）。有重命名或跨格式则回退原转换链路。
  - 重构：`tryCandidate` / `forwardNonStream` / `forwardStream` 签名统一收敛为 `forwardCtx`（聚合每次转发的不变量），消除参数爆炸。
- 测试覆盖：直通逐字节透传（请求+响应含自定义字段保真）、重命名不走直通（改写上游模型名）、流式直通透传保真 + usage 仍计费；全过 `-race`。

### 第 10 步 · 结构化访问日志 + 管理面测试补全（质量增强）
- **结构化访问日志中间件**（`internal/middleware/logger.go`）：`RequestLogger` 全局挂载（`Recovery` 之后、最前），跳过 `/healthz`、`/metrics` 避免探活/抓取噪声。
  - **request_id 贯通**：入口优先复用入站 `X-Request-Id` 头（跨服务串联），否则生成 `req_`+hex 随机 ID；注入 `gin.Context` 并回写 `X-Request-Id` 响应头。转发层改为复用该 ID（原先内部自生成、与访问日志割裂），使**访问日志与计费用量日志共享同一 request_id**，天然对账。
  - **富字段**：转发层经 `SetLogModel` / `SetLogUpstream` / `SetLogUsage` 把模型/渠道/token 用量回填到请求级 `accessLog` 载体（单请求 goroutine 顺序读写，无锁）；收尾统一输出方法/路径/状态/耗时/客户端 IP/身份（user_id/key_id）/模型/渠道/用量，缺失字段省略。
  - **级别按状态码**：5xx→Error、4xx→Warn、其余→Info。
  - **协作方向**：`forwarder` 已依赖 `middleware`，故日志字段的 context 载体与 setter 定义在 `middleware`，转发层回填（不反向依赖）；`SetLog*` 在无中间件时（如转发层单测）退化为无操作。
  - main 按 `cfg.Log`（level + json/text）用 `buildLogger` 构建 slog logger，设为全局默认并注入 forwarder / admin.Service / server.Deps.Logger（原先各处传 nil）。
- **管理面测试补全**（此前 `internal/admin`、`internal/server` 零覆盖）：
  - `internal/admin`：MemoryStore 用户/密钥/渠道 CRUD（含冲突/未找到/分页/充值）、密钥对热路径即时可见与禁用即拒、Service 渠道写操作热更新 router（创建/删除/启停后 `router.Select` 立即反映）、nil router 不 panic、GenerateKey 前缀/长度/千次不重复。
  - `internal/server`：admin handler HTTP 全链路（无令牌 401、用户生命周期、密钥明文仅回显一次且列表不含明文、渠道上游 api_key 脱敏、非法 format 400、删除 204/再删 404）。
  - `internal/middleware`：logger 中间件行为（生成/复用 request_id、响应头、skip 路径、字段回填、级别映射、无中间件不 panic）。

### 第 11 步 · 统一账户认证体系（控制台后端）
把管理面从「裸 token」升级为「账号密码 + 会话」的完整控制台后端，账户体系与计费实体解耦。分 16 个子任务经子代理驱动开发（每任务 TDD + 独立复核）落地。
- **账户领域**（`internal/account`）：登录账户（`accounts` 表：用户名/bcrypt 密码哈希/角色/关联 external_id）与计费实体（`users` 表）职责分离。`AccountStore` + `SettingsStore` 接口，内存与 PostgreSQL 双实现。建 user 账户**原子连带**创建计费实体并回填 external_id（PG 走事务，任一步失败整体回滚不留孤儿）。`Account` 领域视图刻意无 `PasswordHash` 字段（结构层杜绝哈希外泄），仅 `Credentials`（不序列化）含哈希供登录校验。角色仅 `admin`/`user`（`ValidRole` 把关）。预留 `group_name` / `rate_multiplier`（整数百分比倍率）存而未用。
- **密码哈希**（`internal/account`）：bcrypt 封装，`HashPassword`（最短 8 位，短于则 `ErrPasswordTooShort`）/ `VerifyPassword`。绝不存明文。
- **会话管理**（`internal/session`）：Redis 会话，`Manager` 生成不透明会话 ID（crypto/rand），`SessionData`（AccountID/Username/Role/ExternalID）JSON 存 Redis 带 TTL；「记住我」延长 TTL。登出即删。
- **鉴权中间件**（`internal/middleware/session_auth.go`）：`SessionAuth` 从 HttpOnly Cookie 取会话 ID 查 Redis，注入 `SessionData` 到 context（无会话 401）；`RequireRole` 校验角色（取不到会话或角色不符均拒，**fail-closed**）。Cookie 属性 HttpOnly + SameSite=Strict + 可选 Secure（生产 HTTPS）。
- **控制台端点**：`/auth`（register 受注册开关约束 / login / logout / me）、`/me`（用户自助：改自己的密钥，key 归属绑定会话身份，**越权硬约束**——操作他人 key 返回 404 而非 403，不泄存在性）、`/admin/accounts`（账户增删改查启停 + 重置密码）、`/admin/settings`（注册开关 + 新用户初始额度）。
- **鉴权收口**：`/admin` 由裸 token 改为 `SessionAuth` + `RequireRole(admin)`（顺序：先会话后角色），退役 `AdminAuth` 中间件（全树无残留）；`/me` 挂 `SessionAuth`（任意登录角色）；`/auth` 的 register/login 不鉴权。各 `register*Routes` 有 nil 依赖守卫（依赖装配不全时不挂路由，fail-closed 而非请求期 panic）。
- **启动播种**（`cmd/linapi`）：`bootstrapAdmin` 在配置了 `admin.bootstrap.username` 且该用户名不存在时播种首个管理员（幂等，不覆盖已有；密码为空则告警跳过，绝不建空密码账户；日志只记 username 不记密码）。密码建议经 `LINAPI_ADMIN_BOOTSTRAP_PASSWORD` 环境变量注入。
- config `admin` 段改造：去 `token` / `loopback_only`，加 `bootstrap`（username/password）；`SecureCookie = (server.mode == "release")`。schema 双写（`db/schema.sql` + `internal/db/schema.sql`）新增 `accounts` / `settings` 表 + `users.rate_multiplier` 列。
- **附带修复**：`.gitignore` 裸 `linapi` 规则改 `/linapi` 锚定仓库根——原规则误伤 `cmd/linapi/` 源码目录，导致入口 `main.go` 长期未被 Git 跟踪（对应审查文档 `AUD-P1-09`）。
- 测试覆盖：account（密码哈希、内存/PG 双实现 CRUD、建 user 连带计费实体、角色校验）、session（会话往返/TTL/删除）、server（/auth、/me 越权硬约束、/admin 账户/设置 HTTP 全链路、密码哈希不外泄、角色分流）、middleware（SessionAuth/RequireRole fail-closed）；全过 `-race`。

### 第 12 步 · 控制台安全加固（审查批次 A/G + P1-17）
在开前端（Plan 2）前先闭合控制台攻击面——开前端需 `admin.enabled=true`，会点亮 `/auth` `/me` `/admin` 全部端点。按 codex 审查（`docs/reviews/2026-07-10-*.md`）逐项 TDD 修复：
- **AUD-P1-01 · `/models` 误扣押金**：拆分 `/v1` 中间件分组——`Auth` + `RateLimit` 作公共前置，`Quota`（预扣押金）只挂真正产生上游用量的生成端点（`/chat/completions`、`/messages`）。`/models` 只读元数据端点不再每查一次就永久扣掉一笔 `default_reserve`。
- **AUD-P1-03 · 余额不足 key 无 TTL**：Redis 计费 `adjustScript` seed 时即带 `EX ttl`。原实现余额不足会在 `EXPIRE` 前提前 return，留下永久 key，使后续冷源充值被陈旧热副本永久屏蔽。
- **AUD-P0-07 · 注册无限复制赠送额度**：自助注册恒绑定初始余额 0（忽略 `settings.NewUserInitialBalance`）；`putSettings` 拒绝把 `NewUserInitialBalance` 设为正数，双重堵死路径。发放额度只能走管理面主动建号 / 充值（可信操作）。
- **AUD-P1-26 · CSRF 防护**：`middleware.CSRFProtect` 对 Cookie 鉴权的写请求做①双重提交 token（会话绑定的 `CSRFToken` vs 请求头 `X-CSRF-Token`）②强制 `Content-Type: application/json`③Origin/Referer 校验。登录下发非 HttpOnly 的 CSRF Cookie 供前端读取回传；`/me` `/admin` 写端点全挂，GET 自动放行。
- **AUD-P1-27 · 登录注册滥用限速**：`/auth/login` `/auth/register` 在 bcrypt 之前按来源 IP 令牌桶限流（`IPRateLimiter`）+ 全局 bcrypt 并发信号量（容量 = CPU 核数），排不上队回 503，堵住撞库与 CPU 耗尽。
- **AUD-P1-28 · 自助 Key 无上限**：自助建 key 强制 `rate_limit_per_min ∈ [1,5000]`（杜绝 0/负数=不限流与超大值绕过限流）、每账户 ≤50 把。管理面建 key 面向运维不受此限。
- **AUD-P1-29 · 登出假成功**：logout 用独立 3s 超时 context 删会话（不复用请求 context，避免客户端断开取消删除），删除失败回 503 且不清 Cookie，绝不让用户误以为已安全登出而服务端 token 仍有效。
- **AUD-P1-17 · 会话撤销**：`accounts` 表加 `session_version`（schema 双写），禁用 / 改密时在数据层递增。登录把当前代次快照进会话，`SessionAuthWithVersion` 鉴权时回查账户当前代次比对：不一致即判定为陈旧会话，主动删除并 401；回查出错 fail-closed 回 503。在 `/auth`（logout/me）、`/me`、`/admin` 三处接线。使被盗 token、被禁用户 / 被禁管理员的旧 Cookie 立即失效。
- 全量 `go build` / `go vet` / `CGO_ENABLED=1 go test -race ./...` 全绿。逐项证据见审查文档第 10 节跟踪表。

## 测试现状

- 172 个测试函数，分布在 38 个文件。
- 全部通过，且 `CGO_ENABLED=1 go test -race ./...` 干净（gcc 已装好，路径见 CLAUDE.md）。
- billing / account / session 用 `miniredis`（内嵌 Lua）真实执行原子脚本；PGStore / PGSink / account.PGStore 用 fake Querier 单测（不依赖真实 PG）；转发层用 `httptest` 起模拟上游 + `miniredis`，走鉴权→额度→转发全链路集成测试（含流式与同格式直通保真）；管理面与控制台（admin/account/server）用内存 Store + `httptest` 走 HTTP 全链路；访问日志中间件用 `bytes.Buffer` 捕获 JSON 日志断言字段。控制台安全（第 12 步）：CSRF 双重提交 / IP 限速 / bcrypt 信号量 / 会话代次撤销均有针对性测试，会话失效走「登录→禁用或改密→旧 Cookie 再访问应 401」端到端断言。

## 端到端现状

七步骨架 + 转发层 + 运维增强已齐，请求可端到端跑通：客户端（OpenAI / Claude 格式）→ 鉴权/限流/额度预扣 → 转发层解析→路由选渠道→（同格式直通 或 跨格式转换）→发上游→反向转换→计费结算。非流式与流式（SSE）均已打通，支持跨供应商故障转移与熔断。管理面（用户/密钥/渠道 CRUD，渠道改动即时热生效）、Prometheus 指标（`/metrics`）、`/v1/models` 端点均已就绪。

## 后续可选增强（非阻塞）

当前实现已可用且具备基本运维能力（管理面 / 指标 / 热重载 / 直通优化已落地）。以下为仍可继续的增强：
- **链路追踪**：结构化访问日志（`RequestLogger`，request_id 贯通）+ Prometheus 指标已铺开，但尚无分布式追踪（OpenTelemetry span 传播）。
- **控制台前端**：本轮完成的是控制台**后端**（`/auth` `/me` `/admin`）；登录页 / 管理控制台 UI / 用户面板属另一份前端计划（Plan 2），尚未实现。
- **认证体系可继续增强**：账户认证（账号密码 + 会话 + admin/user 角色）+ 控制台安全加固（CSRF、登录注册限速、自助 Key 上限、会话代次撤销、注册不送额度）已落地（见第 12 步）；仍可扩展的有——审计日志、更细粒度 RBAC（当前仅 admin/user 两级）、`rate_multiplier` / `group_name` 预留字段的实际启用、每账户活跃会话数上限。
- **更多供应商适配器**：当前 openai / anthropic 两家；Gemini 等可按注册机制扩展。
- **sqlc 为手写同构产物**：`internal/db/` 是按 sqlc 约定手写的等价代码（环境无法联网装 sqlc）；能装 sqlc 后 `sqlc generate` 可原样覆盖。改表结构时记得同步根 `db/schema.sql` 与 `internal/db/schema.sql` 两份。
