# 架构总览

## 请求生命周期（目标形态）

```
客户端请求（OpenAI / Claude 格式）
   │
   ▼
〔中间件层〕鉴权 → 限流 → 额度检查（原子预扣押金）    ← 第 5+6 步（已完成）
   │
   ▼
按「客户端格式」适配器 ParseRequest → canonical.Request
   │
   ▼
〔路由引擎〕Router.Select(model) → []Candidate   ← 已完成
   │  （优先级分组 + 加权随机 + 熔断过滤）
   ▼
对候选依次：Breaker.Allow() → 按「渠道格式」适配器 BuildRequest → 转发上游   ← 已接线（forwarder）
   │  （失败则故障转移到下一候选，并 RecordFailure；同格式无重命名时短路 canonical 往返、直通原始字节）
   ▼
上游响应 → 按「渠道格式」适配器 ParseResponse → canonical.Response
   │
   ▼
按「客户端格式」适配器 BuildResponse → 返回客户端
   │
   ▼
〔计费结算〕按真实用量退差 + 用量日志异步落库        ← 已接线（转发层终局调 Settle / Refund）
```

流式（SSE）走同一骨架，但响应侧改用有状态的 `StreamDecoder`（上游 SSE → 规范事件）和 `StreamEncoder`（规范事件 → 客户端 SSE）。

## 关键设计决策

### 为什么要一个「规范模型」中间层

若直接做「OpenAI 格式 ↔ Claude 格式」两两转换，N 家供应商就是 N² 条转换路径，且容易以某一家为基准而丢信息。LinAPI 改为「供应商格式 ↔ canonical」的星型结构：每家只需实现与 canonical 的双向转换（N 条路径），彼此解耦。

canonical 被设计成**各家格式的超集**（content-block 结构，接近 Claude Messages），因此 OpenAI 这种较扁平的格式能无损映射进来，反向构造时再按目标格式取舍。

### 同格式直通（零损耗保真）

当「客户端格式」与「选中渠道格式」是同一个适配器、且该模型在此渠道无重命名时，短路掉「解析成 canonical 再构造回去」的往返，直接透传原始字节：请求体原样发上游，非流式响应原样回客户端，流式则逐字节透传上游 SSE 记录。**已实现**（`forwardCtx.canPassthrough`）。

两重收益：省一次编解码开销；更重要的是**彻底避免 canonical 超集未覆盖字段的丢失**——客户端传的自定义/厂商扩展字段（如实验性采样参数、供应商私有字段）原样送达上游并原样返回。有模型重命名（需改写 body 里的 model）或跨格式时回退完整 canonical 往返，正确性优先。

注意：直通仅跳过**格式转换**，不跳过计费——非流式仍 `ParseResponse`、流式仍 `Decode`，只为提取 `usage` 结算，不影响透传的字节。

### 读多写少的并发取向

路由的 `Select` 在每个请求上执行（热路径），渠道配置极少变（冷路径）。因此渠道快照用 `atomic.Pointer` 无锁读、整体替换写；熔断状态每渠道一把小锁；随机源用 `sync.Pool`。这套取向贯穿全项目：热路径避免全局锁。

## 三大核心抽象的边界

| 抽象 | 包 | 职责 | 明确不做什么 |
|---|---|---|---|
| Canonical 模型 | `internal/canonical` | 定义中立的请求/响应/流式事件数据结构 | 不含任何转换逻辑，纯数据 |
| 适配器 | `internal/adapter` | 供应商线格式 ↔ canonical 双向转换 | 不发网络请求，不选渠道 |
| 路由引擎 | `internal/routing` | 模型名 → 有序候选渠道 + 熔断 | 不发网络请求，不做格式转换 |

三者互不依赖对方的实现细节，只通过 canonical 数据结构衔接。转发层（`internal/forwarder`）是把它们串起来的「胶水」，也是唯一真正发起 HTTP 的地方。

## 中间件层与数据访问（第 5 步）

`/v1` 分组前置 **Auth → RateLimit → Quota** 三个 Gin 中间件（见 [modules.md](modules.md)）。两条设计取向值得记住：

- **数据源可替换**：中间件只依赖 `internal/store` 的 `Store` 接口（身份解析 + 余额）。提供内存与 PostgreSQL 两套实现，由 `database.enabled` 选择，中间件零改动。依赖通过 `server.Deps{Store, Redis, Billing}` 注入，便于测试。
- **限流 fail-open**：限流走 Redis Lua 原子令牌桶；Redis 故障时选择放行而非拦截，避免限流组件抖动打挂整个网关——余额闸门（Quota）仍是最后一道兜底。

详见 [modules.md](modules.md)。

## 计费结算（第 6 步）

`internal/billing` 的 `Billing` 门面把计费拆成**预扣 → 结算**两步，横跨中间件层与转发层：

- **预扣（Quota 中间件）**：请求进入即用 Redis 原子预扣一笔押金（`default_reserve`），余额不足直接 402 拦截。这是「预授权」——先冻结再按实际用量退差，避免并发请求同时判断余额导致超卖。
- **结算（转发层）**：转发拿到真实 `usage` 后，`Settle` 按 `Pricing` 算实际成本，退回「押金 − 成本」的差额（成本超押金则补收），并异步记一条用量日志；全部候选失败无用量时 `Refund` 全额退押金。

三条设计取向：

- **一致性在 Redis，凭证可异步**：余额增减用一段 Lua 原子完成（`Reserve`/`Settle` 共用，惰性 seed + 校验下限 + INCRBY），保证多实例一致、并发不超卖；用量日志属记账凭证，容忍毫秒级延迟，故走带缓冲的后台 goroutine 批量落库，降低 DB 压力。队列满则退化同步写，宁可慢也不丢账单。
- **热副本 + 冷源 seed**：权威余额在冷源（PostgreSQL 的 `users.balance`，`database.enabled=false` 时为内存 Store），Redis 只是热副本。key 不存在时用冷源余额惰性初始化，之后所有增减都在 Redis 上原子完成。副作用：线上充值改冷源后，必须调 `Billing.SyncBalance` 主动刷新 Redis 热副本才生效。
- **结算永远放行**：预扣有余额闸门（扣后不得为负），但结算即便导致余额为负也放行——用户已实际消费，欠费记账、下次充值补齐，下一请求的预扣会因余额不足自然拦截。

详见 [modules.md](modules.md) 的 `internal/billing` 章节。

## 数据持久层（第 7 步）

`internal/db` 用 sqlc（pgx/v5）承载类型安全查询，四张表：users（权威余额）/ api_keys（凭证，只存 SHA-256 摘要）/ channels（渠道热加载源）/ usage_logs（记账凭证）。三条设计取向：

- **一份接口，两套实现，一个开关**：`store.Store` 与 `billing.Sink` 各有内存/NopSink 与 PostgreSQL 两套实现，`database.enabled` 决定装配哪套（`buildDataLayer`）。本地开发免装 PG，生产打开开关即用，上层零改动——延续「架构干净可改」。
- **金额与幂等**：金额一律 `BIGINT` 存最小计费单位，杜绝浮点误差；用量日志按 `request_id` 唯一约束 + `ON CONFLICT DO NOTHING`，保证进程崩溃重放不重复记账。
- **sqlc 手写同构**：当前环境无法联网装 sqlc，`internal/db/` 是按其生成约定手写的等价产物，能装 sqlc 后 `sqlc generate` 可原样覆盖。表结构改动需同步根 `db/schema.sql`（sqlc 源）与 `internal/db/schema.sql`（运行时 embed 建表副本）两份。

详见 [modules.md](modules.md) 的 `internal/db` 与 `internal/store` / `internal/billing` 章节。

## 转发层（接线收尾）

`internal/forwarder` 是把前述所有抽象串起来、真正发起上游 HTTP 的胶水层。`Forwarder.Handler(clientFormat)` 挂到 `/v1/*` 端点，一次请求的完整生命周期在此闭合。三条设计取向：

- **候选遍历即故障转移**：`router.Select` 返回有序候选，转发层对每个候选走 `Breaker.Allow()` 准入 → 发上游 → `RecordSuccess/Failure`。上游 5xx / 网络错判为渠道故障，转移到下一候选；上游 4xx 判为客户端错，直接透传、不转移、不计用量——区分「渠道坏了」与「请求本身错了」，避免把用户的参数错误误判成渠道故障而空烧候选。
- **计费终局单点结算**：无论成功、失败、还是无渠道，都在 handler 退出前恰好结算一次——成功且有用量走 `Settle`（按真实用量退差 + 记日志），其余走 `Refund`（全额退押金）。预扣在 Quota 中间件、结算在转发层，两端配对，保证押金不泄漏。
- **流式提交点与故障转移的边界**：流式响应头**惰性写出**（首个输出块前才写），使「响应已提交」与内部 `committed` 标志严格一致。首块之前上游失败仍可静默故障转移到下一候选；一旦首块已发给客户端，再断只能结束并按已消费用量结算，不能重试（HTTP 语义：状态码已发不可撤回）。

详见 [modules.md](modules.md) 的 `internal/forwarder` 章节。

## 管理面与渠道热更新

`internal/admin` 提供用户/密钥/渠道的 CRUD，挂在受会话+角色鉴权保护的 `/admin` 分组。三条设计取向：

- **鉴权走控制台会话**：`/admin` 用 `SessionAuth` + `RequireRole(admin)`（详见下节「控制台认证架构」），与 `/v1` 的业务密钥鉴权互不相通。`admin.enabled` 默认关闭（最小暴露面）。
- **写操作即时热更新路由**：渠道的增/改/删/启停落库后，`admin.Service` 立即 `router.UpdateChannels` 原子热替换，无需重启即生效（复用路由层「读多写少」的无锁热更新能力）。热更新失败仅记日志、不回滚写操作——由定时重载最终收敛。
- **定时重载兜底多实例**：单进程内写操作能即时热更新自己的路由，但多实例部署时他实例感知不到。故 `database.enabled=true` 时起一个后台 goroutine 按 `channel_reload_interval` 定期从 DB 全量重载渠道。内存模式无共享存储，定时重载只会把本进程内存态原样写回，无意义，因此不启动。

## 控制台认证架构

管理控制台后端把网关从「裸 token 管理面」升级为「账号密码 + 会话」的多账户体系（`internal/account` + `internal/session` + `middleware/session_auth.go`）。核心取向：

- **登录账户与计费实体解耦**：`accounts` 表（登录：用户名/bcrypt 哈希/角色/关联 external_id）与 `users` 表（计费：余额/额度）是两个概念。一个登录账户（`role=user`）通过 `external_id` 关联到一个计费实体；`role=admin` 账户管理系统但自身不必是计费主体。建 user 账户时**原子连带**创建计费实体（PG 走事务，失败整体回滚不留孤儿）——避免「有登录态却无计费账户」的裂缝。
- **密码与哈希绝不外泄**：密码 bcrypt 存储（`HashPassword` 最短 8 位）。`Account` 领域视图**结构层就没有** `PasswordHash` 字段，哈希只活在不序列化的 `Credentials` 里——即便 handler 误把 `Account` 整个 JSON 返回也不会漏哈希。
- **会话是服务端不透明令牌**：登录成功签发 crypto/rand 会话 ID，`SessionData`（账户 ID/用户名/角色/external_id）存 Redis 带 TTL；会话 ID 经 HttpOnly + SameSite=Strict Cookie 下发（生产 HTTPS 加 Secure），客户端 JS 读不到、跨站请求带不上。登出即删 Redis 记录（服务端失效，非仅清 Cookie）。「记住我」延长 TTL。
- **鉴权 fail-closed**：`SessionAuth` 无有效会话即 401；`RequireRole` 取不到会话或角色不符即拒——任何环节缺失都是「拒绝」而非「放行」。`/admin` 双中间件顺序为先 `SessionAuth`（注入身份）后 `RequireRole(admin)`（校验角色），普通 user 与匿名都进不去。路由装配亦 fail-closed：依赖未注入则整组不挂，不会退化成无鉴权端点。
- **越权硬约束**：`/me` 用户自助端点把资源归属绑定到会话身份——操作不属于自己的密钥返回 **404**（而非 403），连「该资源存在」都不泄露。
- **首个管理员幂等播种**：`bootstrapAdmin` 在启动时按 `admin.bootstrap` 播种首个 admin——用户名已存在则跳过（不覆盖已有密码），密码为空则告警跳过（绝不建空密码账户），日志只记用户名。密码建议经 `LINAPI_ADMIN_BOOTSTRAP_PASSWORD` 环境变量注入，避免落配置文件。

## 可观测性

`internal/metrics` 用 `prometheus/client_golang` 定义指标，经 `/metrics` 端点暴露（不走鉴权，靠部署层网络隔离仅内网/监控可达）。核心取向：

- **标签基数可控**：只用有限枚举（path/method/status/format/result/channel_id）作标签，**绝不**把模型名、用户 ID、请求 ID 等高基数值放进标签——否则时间序列爆炸拖垮 Prometheus。HTTP 层用路由模板（`c.FullPath()`）而非实际路径作 path 标签，同理。
- **三个观测面**：对外 HTTP（请求数 + 耗时直方图）、上游调用（按渠道/格式/成败的请求数 + 耗时）、每渠道熔断器状态 gauge。埋点集中在全局 `Metrics()` 中间件（HTTP 层）与转发层候选循环（上游层 + 熔断状态），业务代码无侵入。

详见 [modules.md](modules.md) 的 `internal/admin` / `internal/metrics` 章节。
