# 架构总览

## 请求生命周期（目标形态）

```
客户端请求（OpenAI / Claude 格式）
   │
   ▼
〔中间件层〕鉴权 → Redis 限流（不触碰资金）
   │
   ▼
按「客户端格式」适配器 ParseRequest → canonical.Request
   │  校验模型权限 + 强制模型最大输出
   ▼
〔计费预授权〕按最大可计费输入 + 本次最大输出计算最坏成本
   │  PostgreSQL 事务：reserved + 扣 users.balance + ledger 流水
   │
   ▼
〔路由引擎〕Router.Select(model) → []Candidate   ← 已完成
   │  （优先级分组 + 加权随机 + 熔断过滤）
   ▼
对候选依次：Breaker.Allow() 取得 permit → 候选补丁 → URL/SSRF 校验与 prepare → MarkInFlight → send
   │  （明确未消费的 4xx 可 ReleaseAttempt；3xx/发送结果未知则停止重放）
   ▼
上游响应 → 按「渠道格式」适配器 ParseResponse → canonical.Response
   │
   ▼
按「客户端格式」适配器 BuildResponse → 返回客户端
   │
   ▼
〔计费结算〕持久消费事实 → 退差 + 流水 + usage 同事务落库
   │  usage 缺失/部分/冲突（含 total/cache 冲突）时保守保留预授权
   │  只有已持久化的 consumption 可由 Recover 续结算
```

流式（SSE）走同一骨架，但响应侧改用有状态的 `StreamDecoder` 与 `StreamEncoder`。reader 支持 UTF-8 BOM 与 LF / CRLF / 裸 CR 三种标准行结束；`adapter.SSEData` 忽略 comment/event/id/retry 并拼接多 data 行。最终 usage 必须绑定终止语义，之后出现新内容会使精确结算失效。上游响应头等待最多 30 秒、流中连续 2 分钟无字节即中止；向客户端每次写 SSE 前刷新 30 秒 deadline，防止慢读永久占用资源，但不限制整条长流总时长。

## 关键设计决策

### 为什么要一个「规范模型」中间层

若直接做「OpenAI 格式 ↔ Claude 格式」两两转换，N 家供应商就是 N² 条转换路径，且容易以某一家为基准而丢信息。LinAPI 改为「供应商格式 ↔ canonical」的星型结构：每家只需实现与 canonical 的双向转换（N 条路径），彼此解耦。

canonical 以**覆盖各家主要能力的超集**为设计目标（content-block 结构，接近 Claude Messages）。同格式直通可保留未知字段；跨格式只保证目标协议可表达且 canonical 已建模的部分。当前已支持字符串/数组联合字段、OpenAI developer role、tool_result 图片和工具参数原始 JSON/`UseNumber`；项目明确只支持 OpenAI `n=1`，请求侧提前拒绝其它值，上游异常多 choices/index 显式失败且已消费后不重试。剩余协议缺口主要是 OpenAI→Anthropic 流中迟到 input usage 无法在 Anthropic 线格式表达，Billing 会因此保守保留预授权。

### 同格式语义保真

当客户端与渠道格式相同且模型无重命名时，响应可短路 canonical。请求侧仍会重新编码：OpenAI 强制 `n=1`，覆写 stream/include_usage 等安全字段，精确重复 JSON key 在 map 解析后只输出一份；未知字段用 RawMessage 保留语义。非流式响应原样返回，流式逐记录透传，但不承诺请求字节顺序或安全字段原值不变。

同协议模型别名不再重建请求：在已规范化的 raw JSON 上只补丁 `model`，继续保留未知请求字段；响应仍经过 canonical，因此不能承诺未知响应字段无损。跨协议才走完整 canonical 转换，只保证目标协议可表达且 canonical 已建模的能力。

OpenAI 输出上限字段不从客户端猜测。release 为每个启用的 `channel/upstream_model` 显式配置 `max_tokens` 或 `max_completion_tokens`；候选确定后删除请求中的两个同义字段，只写策略指定字段，再进入 prepare/MarkInFlight。策略缺失在 release 启动时或候选触网前 fail-closed。

注意：直通仅跳过不必要的格式转换，不跳过请求上限、预授权或 usage 解码。usage 缺失不会被零值冒充；Billing 会按已知位保守结算。

### 读多写少的并发取向

路由的 `Select` 在每个请求上执行（热路径），渠道配置极少变（冷路径）。因此渠道快照用 `atomic.Pointer` 无锁读、整体替换写；熔断状态每渠道一把小锁；随机源用 `sync.Pool`。`Allow` 返回绑定 generation 的一次性 permit，迟到的旧代际结果被忽略；客户端取消/下游写失败用 neutral 结束，释放半开名额但不把渠道记成功或失败。

## 三大核心抽象的边界

| 抽象 | 包 | 职责 | 明确不做什么 |
|---|---|---|---|
| Canonical 模型 | `internal/canonical` | 定义中立的请求/响应/流式事件数据结构 | 不含任何转换逻辑，纯数据 |
| 适配器 | `internal/adapter` | 供应商线格式 ↔ canonical 双向转换 | 不发网络请求，不选渠道 |
| 路由引擎 | `internal/routing` | 模型名 → 有序候选渠道 + 熔断 | 不发网络请求，不做格式转换 |

三者互不依赖对方的实现细节，只通过 canonical 数据结构衔接。转发层（`internal/forwarder`）是把它们串起来的「胶水」，也是唯一真正发起 HTTP 的地方。

## 中间件层与数据访问（第 5 步）

`/v1` 在查库前先做来源 IP 预算和非阻塞鉴权并发闸门，再执行 **Auth → 账户总桶 → 单 Key 桶**。所有兼容端点在这些可能提前返回的中间件之前注入协议上下文，使 401/413/429/500 也使用对应 OpenAI/Anthropic error envelope。资金预授权位于 Forwarder 的请求解析之后，因为只有此时才知道模型与强制输出上限。

- **数据源可替换**：身份解析的 Store 与资金 Ledger 都有内存/PG 两套实现；但 release 强制使用 PostgreSQL Ledger，内存只供 debug 和测试。
- **限流与资金解耦**：限流走 Redis Lua 并可 fail-open；资金预授权始终走 Ledger，Redis 故障不会改变余额，也不能绕过 402。
- **入站资源有界**：固定 10 秒 `ReadHeaderTimeout`，可配置 `ReadTimeout`、`IdleTimeout`、body 与 header 上限；panic 由不转储敏感请求头的 Recovery 转成协议对应 500，外层日志/指标仍能收尾。
- **Redis 传输与会话最小暴露**：release 拒绝未显式豁免的远程明文 Redis，支持 ACL username、TLS CA 与客户端证书；Redis 会话 key 使用 token SHA-256 摘要，原始 bearer token 不进入 keyspace。

详见 [modules.md](modules.md)。

## 计费结算（第 6 步）

`internal/billing` 把资金操作收敛到 `Ledger`，由 Forwarder 串起**预授权 → 记录消费 → 最终结算/退款**：

- **持久预授权**：请求解析后强制 `n=1` 与输出上限，输入维度按普通输入、cache creation、cache read 中最高费率冻结，输出按上限冻结。PostgreSQL 在事务内创建 reserved、条件扣余额并追加流水；余额不足在上游 I/O 前返回 402。
- **发送前冻结状态**：每个候选先完成本地请求构造，再把 reservation 从 reserved 迁到带 channel 的 in_flight，最后才 send。MarkInFlight 失败且尚未 send 时先幂等 ReleaseAttempt；明确未执行生成的 4xx 可释放。3xx 可能是 POST 已处理后的跳转，与网络错误、408、5xx 一样保留 in_flight，不能自动换渠道或退款。HTTP 自动重定向被禁用。
- **先记录消费，再结算**：成功响应把 in_flight 迁到 consumed_unsettled；Finalize 在事务内退差、追加流水、写 usage 并迁到 settled。只有 reserved 可迁到 refunded。RecordConsumption 会有限重试，但若持续失败，精确 usage 仍无持久待办并转人工对账。
- **保守 usage 语义**：分项完整时按普通输入、输出、缓存创建、缓存读取独立费率精确计费；只有 total 时按所有维度最高单价。total 小于缓存分项或与已知分项合计冲突时保留整笔预授权并标记 estimated。Billing 与 Ledger 都拒绝实际 cost 超过 reservation。

四条设计取向：

- **单一真相源**：生产资金只在 PostgreSQL 的 `users.balance`、`billing_reservations`、`billing_ledger` 变化；Redis 清空、过期或客户端透明重试与资金无关。
- **状态机 + 幂等键**：服务端 reservation ID 与每阶段 operation ID 固定；数据库约束使 Reserve/Finalize/Refund 重放只生效一次，且结算与退款互斥。
- **同步凭证**：RecordConsumption 先持久化消费事实，随后 Finalize 在一笔事务中提交余额调整、流水、usage 与终态；删除原异步 Recorder/Sink 路径。RecordConsumption 自身持续不可用时 usage 尚无持久待办，仍是 P1-12 的剩余窗口。
- **持续恢复且不误伤多实例**：启动 Recover 后每 30 秒重跑；Finalize consumed_unsettled，退款超过 5 分钟的孤立 reserved。仅超过 24 小时的 in_flight 告警人工按 channel 对账并保持冻结；新鲜 in_flight 不阻断新实例启动。

详见 [modules.md](modules.md) 的 `internal/billing` 章节。

## 数据持久层（第 7 步）

`internal/db` 用 sqlc（pgx/v5）承载类型安全查询。计费核心表为 users（权威可用余额 + balance_version）、billing_reservations（状态和消费事实）、billing_ledger（只追加资金流水）与 usage_logs（最终用量凭证）。

- **一份接口，两套实现**：`billing.Ledger` 有 MemoryLedger 与 PostgresLedger；release 强制后者，避免误把进程内余额用于生产。
- **金额与幂等**：金额一律 `BIGINT`；operation ID、reservation 状态和行锁/条件 UPDATE 共同阻止重复资金变动。`usage_logs.request_id` 采用内部 reservation ID，外部 trace ID 不参与账单唯一性。
- **sqlc 手写同构**：当前环境无法联网装 sqlc，`internal/db/` 是按其生成约定手写的等价产物，能装 sqlc 后 `sqlc generate` 可原样覆盖。全新库 schema 需同步根 `db/schema.sql` 与 `internal/db/schema.sql`；既有库升级必须追加不可改写的 `internal/db/migrations/<version>_<name>.sql`。
- **版本化迁移**：`schema_migrations` 记录版本、名称、SHA-256 checksum；`ApplySchema` 在事务级 advisory lock 下只执行缺失版本，多实例启动只有一个迁移者。`auto_migrate=false` 时 `VerifySchema` 拒绝缺版本、checksum 漂移、数据库高于当前二进制等状态。当前自动化覆盖解析与事务契约，但真实 PostgreSQL 升级演练仍是上线条件。

版本化迁移只处理 schema，不会猜测旧 Redis 热余额中的历史消费。首次上线必须冻结旧资金写入并人工对账 PostgreSQL、Redis 与供应商账单，校正 `users.balance` 后再切换。

配置文件是可选输入：路径不存在时使用环境变量与默认值；文件存在但读取或解析失败仍立即退出。release 的数据库、模型价格/边界和 OpenAI 输出字段策略不会因配置文件缺失而放宽。

详见 [modules.md](modules.md) 的 `internal/db` 与 `internal/store` / `internal/billing` 章节。

## 转发层（接线收尾）

`internal/forwarder` 是把前述所有抽象串起来、真正发起上游 HTTP 的胶水层。`Forwarder.Handler(clientFormat)` 挂到 `/v1/*` 端点，一次请求的完整生命周期在此闭合：

- **候选级安全补丁先于触网**：路由选出候选后，Forwarder 才知道真实 upstream model；此时写入策略指定的唯一 OpenAI 输出上限字段，再完成 prepare。补丁或 prepare 失败尚未触网，可尝试下一候选。
- **上游目标双重校验**：release 默认只允许公共 HTTPS；私网 CIDR/HTTP 只能在 `upstream.target_rules` 按精确 `host:port` 授权。结构化 URL 校验拒绝 userinfo/query/fragment/非规范路径；自定义 `DialContext` 在每次拨号时重新解析 DNS，只连接策略允许的 IP，阻断 DNS rebinding 与特殊地址 SSRF。
- **故障转移必须证明未消费**：每次发送前先 MarkInFlight。只有明确未生成的响应可 ReleaseAttempt；其中 401/403/429 等渠道拒绝才换下一候选。3xx、网络错、408、5xx 可能发生在上游已收请求之后，不能自动重放。
- **消费事实独立于本地结算结果**：成功响应迁到 consumed_unsettled；Settle 失败留待 Recover，绝不能触发退款。解析失败、无渠道或明确未消费并 release 后的请求才可退款。
- **两个提交点分别治理**：响应头惰性写出保证客户端 HTTP 提交状态准确；in_flight 负责上游发送是否可能已消费。首块未写并不等于上游未收请求，故不能单靠 `committed=false` 决定跨渠道重放。
- **permit 与取消语义分离**：每次 `Allow` 返回一次性 permit；渠道成功/故障分别回报 success/failure，客户端取消和下游写失败回报 neutral。permit 的 generation 与 `sync.Once` 防止迟到结果和重复回报破坏熔断状态。

详见 [modules.md](modules.md) 的 `internal/forwarder` 章节。

## 管理面与渠道热更新

`internal/admin` 提供用户/密钥/渠道的 CRUD，挂在受会话+角色鉴权保护的 `/admin` 分组。三条设计取向：

- **鉴权走控制台会话**：`/admin` 用 `SessionAuthWithVersion` + `RequireRole(admin)` + `CSRFProtect`，与 `/v1` 的业务密钥鉴权互不相通。`admin.enabled` 默认关闭（最小暴露面）。
- **写操作即时热更新路由**：渠道的增/改/删/启停落库后，`admin.Service` 立即 `router.UpdateChannels` 原子热替换，无需重启即生效（复用路由层「读多写少」的无锁热更新能力）。热更新失败仅记日志、不回滚写操作——由定时重载最终收敛。
- **定时重载兜底多实例**：单进程内写操作能即时热更新自己的路由，但多实例部署时他实例感知不到。故 `database.enabled=true` 时起一个后台 goroutine 按 `channel_reload_interval` 定期从 DB 全量重载渠道。内存模式无共享存储，定时重载只会把本进程内存态原样写回，无意义，因此不启动。
- **渠道凭证信封加密**：PostgreSQL 的 `channels.api_key` 只保存带版本与 key id 的 AES-256-GCM envelope；每次加密使用 `crypto/rand` nonce，`channel_id` 作为 AAD，密文不能跨渠道调包。主密钥由进程外配置注入且不与密文共库；数据库模式缺失/非法密钥、未知 envelope 或认证失败均拒绝启动。历史明文必须在维护窗口通过显式开关完成单事务迁移，默认不会静默兼容。

生产升级顺序固定为：先备份并进入维护窗，注入加密主密钥；仅一次开启 `database.channel_key_encryption.migrate_plaintext=true` 完成全表事务迁移和约束验证；随后立刻关闭开关、重启确认无明文，并轮换历史上游供应商 key。迁移主密钥只应来自环境变量/Secret Manager，不能与数据库备份同域保存。

## 控制台认证架构

管理控制台后端把网关从「裸 token 管理面」升级为「账号密码 + 会话」的多账户体系（`internal/account` + `internal/session` + `middleware/session_auth.go`）。核心取向：

- **登录账户与计费实体解耦**：`accounts` 表（登录：用户名/bcrypt 哈希/角色/关联 external_id）与 `users` 表（计费：余额/额度）是两个概念。一个登录账户（`role=user`）通过 `external_id` 关联到一个计费实体；`role=admin` 账户管理系统但自身不必是计费主体。建 user 账户时**原子连带**创建计费实体（PG 走事务，失败整体回滚不留孤儿）——避免「有登录态却无计费账户」的裂缝。
- **密码与哈希绝不外泄**：密码 bcrypt 存储（最少 8 个 Unicode 字符、最多 72 字节）。`Account` 领域视图**结构层就没有** `PasswordHash` 字段，哈希只活在不序列化的 `Credentials` 里。
- **会话是服务端不透明令牌**：登录成功签发 crypto/rand 会话 ID，`SessionData` 保存账户身份、会话代次与会话绑定 CSRF token，按 TTL 存 Redis。Cookie 携带原始 token，但 Redis key 只保存其摘要；Lua 脚本以每账户 ZSET 原子回收过期会话并限制活跃数量。「记住我」只延长 TTL，不绕过服务端撤销或数量上限。
- **鉴权与撤销 fail-closed**：受保护路由使用 `SessionAuthWithVersion` 回查账户当前代次；禁用或改密后旧会话立即 401，回查依赖失败返回 503。`/admin` 再叠 `RequireRole(admin)`；`/me` 与 `/admin` 写请求叠 `CSRFProtect`，强制 JSON、双重提交 token 与 Origin/Referer 边界。依赖未注入则整组不挂。
- **匿名认证滥用防线**：Gin 默认 `SetTrustedProxies(nil)`；登录/注册在 bcrypt 前同时消耗来源 IP 与标准化账户标识的 Redis 预算，存在/不存在用户名共享相同工作量；bcrypt `TryAcquire` 满载立即 503；成功登录还受每账户活跃会话原子上限约束。
- **自助 Key 不能叠加绕限流**：每把 Key 强制 `rate_limit_per_min ∈ [1,5000]`；内存锁/PG advisory lock 把“检查数量 + 创建”收敛为原子操作，每账户最多 50 把。`/v1` 另有账户总令牌桶，无法靠多 Key 线性叠加吞吐；账户/计费用户列表使用有界 limit/offset，Key 列表本身由 50 上限控制。
- **越权硬约束**：`/me` 用户自助端点把资源归属绑定到会话身份——操作不属于自己的密钥返回 **404**（而非 403），连「该资源存在」都不泄露。
- **首个管理员幂等播种**：`bootstrapAdmin` 在启动时按 `admin.bootstrap` 播种首个 admin——用户名已存在则跳过（不覆盖已有密码），密码为空则告警跳过（绝不建空密码账户），日志只记用户名。密码建议经 `LINAPI_ADMIN_BOOTSTRAP_PASSWORD` 环境变量注入，避免落配置文件。

## 可观测性

`internal/metrics` 用 `prometheus/client_golang` 定义指标，经 `/metrics` 暴露。release 要求配置专用 bearer token；抓取同时受 `metrics_max_requests_in_flight` 与 `metrics_timeout_seconds` 限制，避免匿名拓扑泄露和采集放大。核心取向：

- **标签基数可控**：只用有限枚举（path/method/status/format/result/channel_id）作标签，**绝不**把模型名、用户 ID、请求 ID 等高基数值放进标签——否则时间序列爆炸拖垮 Prometheus。HTTP 层用路由模板（`c.FullPath()`）而非实际路径作 path 标签，同理。
- **三个观测面**：对外 HTTP（请求数 + 耗时直方图）、上游调用（按渠道/格式/成败的请求数 + 耗时）、每渠道熔断器状态 gauge。埋点集中在全局 `Metrics()` 中间件（HTTP 层）与转发层候选循环（上游层 + 熔断状态），业务代码无侵入。
- **健康语义分离**：`/healthz` 与 `/livez` 只表示进程存活；`/readyz` 用独立 2 秒 context 检查 Redis/PostgreSQL 等强依赖，失败返回 503，供负载均衡停止送入新流量。

详见 [modules.md](modules.md) 的 `internal/admin` / `internal/metrics` 章节。
