# LinAPI 全面只读审查记录

> 审查日期：2026-07-10
> 最后更新：2026-07-11
> 审查方式：Codex 主智能体 + 多轮并行子智能体，多模块交叉复核
> 当前状态：**本轮覆盖的转发、鉴权、适配器、存储与传输项已大面积修复；AUD-P1-12、P2-03 仍为部分修复。AUD-P1-07、P1-18、P1-19、P1-30、P1-32、P2-09、P2-10、P2-11 已落地代码但保留真实依赖/迁移验收条件；未在本轮覆盖的行继续以第 10 节为准。**
> 原审查变更边界：首次记录时只审查与记录，未修改业务代码、配置、Schema 或测试；后续修复记录保留在各问题和第 10 节跟踪表中

## 1. 文档目的

本文件是本次全面代码审查的持久化记录，也是后续 Claude / Codex / 其他 AI 的直接交接入口。它记录已经由代码证据确认的问题、触发条件、影响、建议修复方向与验收要求。

各问题段中的“原始证据 / 原始触发 / 原始影响 / 原始利用链 / 原始修复方向 / 原始可达性 / 原始验收测试”等字段只描述发现问题时的锁定快照；它们不会随修复删除，也不得作为当前实现仍有该缺陷的证据。当前事实以紧邻状态后的“修复记录”、明确标注的“当前剩余…”说明和第 10 节跟踪表为准。

后续修复与交接遵循：

- 修改前先读取当前工作区并重新验证原审查行号和调用链；原始证据是历史快照，不随修复删除。
- 不把“代码已修”误写成“生产数据已迁移或真实部署已验收”。
- 每修复一个问题，都要在本文件的状态表中回填状态、提交或 PR、测试证据。
- 按项目约定同步更新 `docs/progress.md`、必要时更新 `docs/modules.md` / `docs/architecture.md` / `AGENTS.md`。

## 2. 状态与严重度

状态取值：

| 状态 | 含义 |
|---|---|
| `待修复` | 已确认，尚未开始修改 |
| `进行中` | 已由某个 AI / 开发者认领并开始修改 |
| `代码已修` | 修复实现已落地，但仍有全量回归、真实依赖、迁移或上线验收条件；不能等同生产已验收 |
| `部分修复` | 主要利用路径已闭合，但问题定义中的一部分能力或验收仍未完成 |
| `待复核` | 已有修复，等待独立测试和代码复核 |
| `已修复` | 修复及对应回归测试均已验证 |
| `不修复` | 经明确决策接受风险，并记录理由 |

严重度取值：

| 级别 | 含义 |
|---|---|
| P0 | 会造成真实资金/额度错误、系统性免费调用或重复入账；上线阻断 |
| P1 | 会造成权限/凭证失守、持续不可用、故障转移失效、重要请求被误拒、账单或发布状态失真 |
| P2 | 条件性信息暴露、兼容性、健壮性、可观测性或资源治理问题；应纳入后续修复 |

## 3. 总览与修复批次

三轮累计确认：

- P0：7 项
- P1：34 项
- P2：24 项
- 合计：65 项（第二轮新增 27 项，第三轮安全专项新增 14 项，见第 12～21 节）。
- 三轮基线验证均通过；第三轮在同一提交上通过全量 `go test`、`go vet`、`-race` 与官方 `govulncheck` 扫描，但绿色测试不代表安全边界已经闭合。

建议分批修复，避免把财务状态机、流式协议和外围兼容性一次混在同一个大改动中：

| 批次 | 范围 | 问题 ID |
|---|---|---|
| A · 发布与即时止损 | 入口跟踪、`/models` 误扣、首次余额不足 TTL | AUD-P1-01、AUD-P1-03、AUD-P1-09 |
| B · 计费一致性重构 | 持久账本、幂等结算、额度暴露、usage 缺失、算术边界、账单落库 | AUD-P0-01～06、AUD-P1-02、AUD-P1-11～13、AUD-P1-21 |
| C · 路由与流生命周期 | 取消语义、熔断代次、截断流、重试歧义、重定向、超时、401/403 | AUD-P1-04～08、AUD-P1-14～16、AUD-P1-19～20 |
| D · 协议兼容与资源治理 | 联合字段、错误格式、别名保真、多 choice、SSE、工具参数、大小限制 | AUD-P1-10、AUD-P1-22～25、AUD-P2-01～07 |
| E · 账户与存储契约 | 会话失效、设置快照、账户不变量、迁移、Store 语义一致性 | AUD-P1-17～18、AUD-P2-08～13、AUD-P2-18～20 |
| F · 运行时与性能治理 | 冷源查询、分布式时钟、关闭预算、panic 可观测性 | AUD-P2-14～17 |
| G · 控制台安全与滥用防护 | 免费额度、CSRF、登录滥用、自助 Key、可靠登出、用户名枚举 | AUD-P0-07、AUD-P1-26～29、AUD-P2-21 |
| H · 网络、凭证与供应链 | 慢读、SSRF、Redis/上游密钥、匿名耗尽、指标、代理信任、依赖公告 | AUD-P1-30～34、AUD-P2-22～24 |

> 批次 B 已按整体状态机方案实现：PostgreSQL 余额 + reservation/ledger，Redis 退出资金路径；每个候选先完成本地 prepare，再在发送前 MarkInFlight 记录 channel。Recover 启动执行并每 30 秒重跑，完成 consumed_unsettled、退款超过 5 分钟的 reserved，只对超过 24 小时的 in_flight 告警人工对账；新鲜 in_flight 不阻断多实例启动。

> **修复进度（2026-07-11）**：P1-04/05/10/13/17/20/23/25/27/28/31/33/34 与 P2-01/02/04/05/06/08/12/13/15/17～22/24 已闭合；P1-07/18/19/30/32 与 P2-09/10/11 已落地代码但保留文中列出的真实网络、Redis 或 PostgreSQL 验收；P1-12 与 P2-03 仍为部分修复。其它条目、发布门槛和测试证据以第 10 节及各问题段为准；生产放行仍需旧余额人工对账与真实 PostgreSQL 故障注入。

## 4. P0：上线阻断问题

### AUD-P0-01 · PostgreSQL 余额未随消费扣减，Redis 丢失后历史消费恢复

- 状态：`代码已修`
- 修复记录（2026-07-10）：生产资金只在 PostgreSQL 变化；发送前 MarkInFlight 同时记录 channel。Recover 完成 consumed_unsettled、退款超过 5 分钟的 reserved，并只对超过 24 小时的 in_flight 告警人工对账；新鲜 in_flight 不触发告警或启动阻断。
- 上线验收：自动迁移会保留旧 PostgreSQL 余额，无法知道旧 Redis 中尚未回写的历史消费。首次切换前必须冻结旧资金写入，人工对账 PostgreSQL、Redis 与供应商账单并校正余额；还需用真实 PostgreSQL 验证 Redis 清空不影响余额、并发扣减、重复 Finalize 和崩溃恢复。
- 原始证据：
  - `internal/billing/billing.go:65-85`：`Settle` 只调整 Redis Account 并记录 usage。
  - `internal/billing/account.go:13-15`：Redis 余额键 TTL 为 7 天。
  - `internal/middleware/quota.go:32-41`：Redis key 不存在时重新读取 Store 余额作为 seed。
  - `db/schema.sql:17-18`：文档和 Schema 把 PostgreSQL `users.balance` 声明为权威余额。
- 原始触发：余额键过期、Redis 重启/清空、迁移或故障恢复。
- 原始影响：数据库余额从未扣除历史消费；Redis 缺失后会用旧余额重新 seed，已经消费的额度“复活”。
- 原始根因：系统同时宣称 PostgreSQL 是真相源，又把所有消费变动只保存在有 TTL 的 Redis 中。
- 原始修复方向：
  1. 引入持久化 reservation / ledger / settlement 状态，按内部请求 ID 幂等。
  2. PostgreSQL 保存可恢复的余额变动或权威账本；Redis 仅负责快速准入、预授权或派生余额。
  3. 明确崩溃恢复和 Redis 重建算法，不能从未经扣减的绝对余额直接恢复。
- 原始验收测试：
  - 完成消费后删除 Redis balance key，再请求时余额不得恢复。
  - Redis 重启后应由持久账本恢复到准确余额。
  - 同一 settlement 重放两次只能生效一次。

### AUD-P0-02 · OpenAI 同格式流式直通可能按 0 token 结算

- 状态：`代码已修`
- 修复记录（2026-07-10）：直通请求对原 JSON 做最小保真合并，强制 OpenAI `stream_options.include_usage=true` 与服务端输出上限；canonical usage 记录已知位。最终 usage 缺失、部分、矛盾或异常时，Billing 保留整笔预授权并写 `estimated=true`，不再用零值成功结算。
- 上线验收：仓库回归覆盖无 usage、提前 EOF/断流和直通未知字段保真；生产放行仍需在真实 PostgreSQL Ledger 上验证上述路径的余额与 reservation 终态。
- 原始证据：
  - `internal/forwarder/stream.go:33-39`：同格式且模型不重命名时直接透传 `rawBody`。
  - `internal/adapter/openai/request_build.go:20-23`：只有非直通的 `BuildRequest` 才强制 `stream_options.include_usage=true`。
  - `internal/forwarder/stream.go:86,112-115,160-164`：usage 默认零值，仅从流事件累计，结束时无条件结算。
- 原始触发：OpenAI 客户端发送 `stream:true`，但未携带 `stream_options.include_usage=true`，并命中同格式直通渠道；或客户端在最终 usage 块前断开。
- 原始影响：已经完成或已交付大量内容的请求按 0 / 部分 token 收费，并退回绝大部分甚至全部押金。
- 原始修复方向：
  1. 直通请求也应对 JSON 做保真最小合并，强制 `include_usage=true`，保留所有未知字段。
  2. 显式跟踪 `usageSeen`、协议 `completed` 和客户端是否提前断开。
  3. 缺少最终 usage 时不得把零值当真实用量；采用保留押金、本地保守估算或“待上游对账”状态。
- 原始验收测试：
  - 同格式流请求未提供 `stream_options`，验证实际发往上游的请求包含 `include_usage=true`。
  - 上游无 usage、客户端提前断流、上游提前 EOF 三种场景不得按 0 成本成功结算。
  - 未知扩展字段在最小 JSON 合并后仍保持原值。

### AUD-P0-03 · 上游已消费后结算失败会触发全额退款

- 状态：`代码已修`
- 修复记录（2026-07-10）：Forwarder 在发送前持久 MarkInFlight；成功响应再记录 consumed_unsettled 并 Finalize。网络错、408、5xx 等发送结果未知路径保留 in_flight，不重放也不退款；只有明确未消费并 ReleaseAttempt 回 reserved 的路径允许 Refund。
- 上线验收：仓库回归覆盖消费后结算失败不退款；生产放行前需在真实 PostgreSQL 注入“事务已提交但客户端未收到结果”、进程崩溃与重复 Recover，确认余额只变化一次。
- 原始证据：
  - `internal/forwarder/forwarder.go:61-69`：只要 `settled=false`，defer 就执行 `Refund`。
  - `internal/forwarder/forwarder.go:223-230`：Redis Settle 任意错误都返回 `false`。
  - `internal/forwarder/nonstream.go:69-93`、`stream.go:160-164`：上游成功后仍可能结算失败。
- 原始触发：Redis timeout、连接错误，或脚本已执行但响应丢失。
- 原始影响：
  - 脚本没执行：真实上游消费被全额退款，形成免费调用。
  - 脚本已执行但响应丢失：随后又退款，可能在正确退差基础上额外增加一整笔押金。
- 原始修复方向：把 `safe_to_refund`、`consumed_but_unsettled`、`settled` 分成独立状态；上游一旦成功或流已产生计费用量，结算失败只能进入幂等重试/待对账，不能立即退款。
- 原始验收测试：模拟“Redis 脚本执行成功但客户端收到 timeout”，重试和清理后最终余额只能变化一次。

## 5. P1：高优先级问题

### AUD-P1-01 · `GET /v1/models` 永久消耗默认押金

- 状态：`代码已修`
- 修复记录（2026-07-10）：旧 Quota 中间件已删除；`/v1/models` 不进入 Forwarder/Ledger，`TestModelsEndpointDoesNotConsumeBilling` 连续请求后验证权威余额不变。
- 原始证据：`internal/server/server.go:87-97` 将 `/models` 与生成端点放入同一个 Quota 分组；`internal/middleware/quota.go:40-54` 预扣后只把 reservation 放入 context；`listModels` 不结算也不退款。
- 原始影响：有效 API Key 每查询一次模型列表，余额永久减少 `billing.default_reserve`。
- 原始修复方向：把路由拆成 Auth + RateLimit 公共组，仅对产生上游用量的 POST 端点增加 Quota。
- 原始验收测试：连续调用 `/v1/models`，Redis 与持久余额均不变。

### AUD-P1-02 · 管理面充值/扣减不刷新热余额，且绝对值覆盖有并发风险

- 状态：`代码已修`
- 修复记录（2026-07-10）：Redis 余额热副本与 `SyncBalance` 已退出架构。管理面 `AddBalance` 和 PostgresLedger 共用 PostgreSQL `users.balance` 权威值，不再存在陈旧绝对值覆盖并发消费的问题。
- 原始证据：`internal/server/admin_handlers.go:101-113` 直接调用 `AdminStore.AddBalance`；`billing.Billing.SyncBalance` 全仓无调用者；`admin.Service` 没有 Billing 字段。
- 原始影响：已有 Redis key 时充值不生效、后台扣减阻止不了继续消费。若简单补 `SET`，又可能覆盖充值期间的并发消费。
- 原始修复方向：纳入 AUD-P0-01 的账本设计，以 delta / version / 原子事件同步，避免用陈旧绝对余额覆盖 Redis。
- 原始文档偏差：`docs/progress.md` 与 `docs/modules.md` 当前声称该同步已接线，实际并未实现。

### AUD-P1-03 · 首次余额不足留下永久 Redis key

- 状态：`代码已修`
- 修复记录（2026-07-10）：Redis Account 与余额 key 已从资金路径删除；余额不足由 PostgreSQL 条件预授权直接判定，不再存在 seed/TTL 屏蔽冷源充值的问题。
- 原始证据：`internal/billing/account.go:44-55` 先 `SET seed`，余额不足时在 `EXPIRE` 前返回。
- 原始影响：首次请求余额不足时 key 的 TTL 为 `-1`；之后 PostgreSQL 充值仍被旧 key 屏蔽。
- 原始修复方向：seed 时立即带 TTL（`SET ... EX`），或所有返回路径统一续期；长期方案随账本设计处理。
- 原始验收测试：首次余额不足后检查 TTL 必须大于 0；充值后新余额可见。

### AUD-P1-04 · 客户端取消被当作渠道故障并污染所有候选

- 状态：`已修复`
- 修复记录（2026-07-11）：Forwarder 将请求 context 取消和下游写中断归为 `outcomeNeutral`，立即停止候选重试；`Breaker.Allow` 返回的 permit 通过 `RecordNeutral` 释放 HalfOpen 探测名额，但不累计失败或成功。`TestClientCancellationDoesNotAffectBreakersOrRetry` 验证取消后不会命中后备渠道，两个 breaker 都保持 Closed。
- 原始证据：`internal/forwarder/nonstream.go:44-47`、`stream.go:52-54` 将所有调用错误归为 channel error；`forwarder.go:128-165` 继续尝试其它候选并记录失败。
- 原始影响：少量主动断连可以快速把同一模型的所有渠道熔断。
- 原始修复方向：增加 neutral/canceled outcome；检测请求 context 已取消时立即停止，不写渠道失败指标、不改变 breaker。
- 原始验收测试：两渠道、阈值 1，请求主动取消后两个 breaker 均保持 Closed。

### AUD-P1-05 · Open 熔断器会被旧请求的迟到成功关闭

- 状态：`已修复`
- 修复记录（2026-07-11）：`BreakerPermit` 绑定获准时的 generation，成功、失败或 neutral 结果只允许结算一次且只作用于同一代；旧代请求的迟到成功不会关闭新一代 Open breaker。`TestBreakerLateSuccessCannotCloseNewOpenGeneration` 与 neutral HalfOpen 回归已覆盖。
- 原始证据：`internal/routing/breaker.go:110-124` 在 `StateOpen` 收到 `RecordSuccess` 时直接切回 Closed。
- 原始影响：并发失败刚触发的熔断会被之前已放行、稍后完成的成功请求撤销，绕过 cooldown。
- 原始修复方向：Open 状态忽略旧成功；更完整方案是 `Allow` 返回带 generation / epoch 的 permit，结果只作用于对应代次。
- 原始验收测试：阈值 1，先允许两个请求，先失败触发 Open，再成功；最终必须仍为 Open。

### AUD-P1-06 · 截断流、畸形事件与流内 error 被记为成功

- 状态：`代码已修`
- 修复记录（2026-07-10）：EOF、读取/解码失败、缺协议终态、final usage 后继续输出及流内 EventError 均按渠道失败处理并保守结算；OpenAI/Anthropic error 事件均能进入 canonical。
- 原始证据：`internal/forwarder/stream.go:89-109` 对 committed 后的 read/decode error 只 `break`；`stream.go:160-164` 无条件返回 success；`internal/adapter/openai/stream_encode.go:94-95` 丢弃 `EventError`。
- 原始影响：客户端得到截断的 200 响应，渠道却 `RecordSuccess`；错误事件可能完全消失。
- 原始修复方向：要求看到合法结束事件；已 committed 的错误返回 `outcomeChannelError{committed:true}`，记录失败但不再切换渠道；目标适配器必须编码或明确终止流内错误。
- 原始验收测试：首块后连接 reset、畸形 JSON、缺 `[DONE]` / `message_stop`、HTTP 200 内 error 事件均应标记渠道失败。

### AUD-P1-07 · 流式客户端没有响应头超时和事件空闲超时

- 状态：`代码已修`
- 修复记录（2026-07-11）：流式与非流式共用的 Transport 已设置 30 秒 `ResponseHeaderTimeout`；流式 body 由 `idleReadCloser` 包装，首字节及任意两次读取之间停滞 2 分钟会关闭上游并返回 `errUpstreamStreamIdle`。`TestIdleReadCloserTimesOutStalledStream` 已通过；尚未另跑真实网络“响应头永不返回”集成用例。
- 原始证据：`internal/forwarder/upstream.go:27-42` 注释声称有 `ResponseHeaderTimeout`，实际没有赋值；流式 client 无整体 Timeout；SSE reader 无 idle deadline。
- 原始影响：上游不回响应头、200 后不发首事件或流中停滞，都能永久占用 handler、连接和 half-open probe。
- 原始修复方向：配置连接超时、`ResponseHeaderTimeout`、首事件 timeout 和事件间 idle timeout；为整个多候选请求设置总预算。

### AUD-P1-08 · 上游 401/403 被误判为客户端错误

- 状态：`代码已修`
- 修复记录（2026-07-10）：401/403 被归为渠道拒绝；它们同时属于明确未消费 4xx，Forwarder 先 ReleaseAttempt 回 reserved，再安全尝试下一候选。网络错误、408、5xx 不走该路径。
- 原始证据：`internal/forwarder/forwarder.go:242-246` 只把 408、429、5xx 判为渠道错误；其它 4xx 都会停止故障转移并 `RecordSuccess`。
- 原始影响：渠道 API Key 失效或渠道账户无模型权限时，健康备份不会被尝试，坏渠道反而被记健康。
- 原始修复方向：至少把上游 401、渠道账户类 402/403 归为渠道故障；混合语义状态结合供应商错误码/错误体分类。
- 原始验收测试：高优先渠道 401、低优先渠道 200，必须成功切换到低优先渠道。

### AUD-P1-09 · `.gitignore` 排除了整个程序入口

- 状态：`已修复`
- 修复记录（2026-07-10）：裸 `linapi` 规则已改为仓库根二进制锚定规则，`cmd/linapi/main.go` 已正常跟踪并可构建。
- 原始证据：`.gitignore:5` 为裸规则 `linapi`；`git check-ignore -v cmd/linapi/main.go` 命中该规则；`git ls-tree HEAD cmd/linapi` 为空。
- 第三轮历史快照证据：`0736eb1` 已在 server 层注册控制台会话路由，但当时本地 `cmd/linapi/main.go` 仍未注入 Account/Settings/Session，且因忽略规则无法进入普通提交；三个路由注册函数会命中 nil guard，标准二进制继续返回 404。当前入口已跟踪并完成依赖注入，该描述不再代表现状。
- 原始影响：当前本机能运行，但新 clone、提交或 CI 中缺少 `cmd/linapi/main.go`，普通 `git status` 也不会提醒。
- 原始修复方向：改成只匹配根二进制的 `/linapi`、`/linapi.exe` 或依赖 `/bin/`，然后显式跟踪 `cmd/linapi/main.go`。
- 原始验收测试：干净 clone 后 `go build ./cmd/linapi` 成功。

### AUD-P1-10 · 常见合法 Anthropic/OpenAI 请求在解析阶段被拒绝

- 状态：`已修复`
- 修复记录（2026-07-11）：Anthropic `message.content` 通过自定义 `messageContent.UnmarshalJSON` 同时接受字符串与 block 数组；OpenAI `stop` 同时接受字符串与字符串数组并归一到 canonical。两种联合输入均已有往返回归。
- 原始证据：
  - `internal/adapter/anthropic/types.go:25-27` 把 `message.content` 固定为 `[]block`，不能解析合法字符串内容。
  - `internal/adapter/openai/types.go:20` 把 `stop` 固定为 `[]string`，不能解析合法字符串 stop。
- 原始影响：`{"content":"hello"}` 或 `{"stop":"END"}` 在路由和直通判断之前直接返回 400。
- 原始修复方向：为两者实现 string/array 联合解码并归一到 canonical。

### AUD-P1-11 · Claude 缓存 token 被解析但完全没有计费

- 状态：`代码已修`
- 修复记录（2026-07-10）：cache creation/read token 已进入 canonical、reservation 与 usage_logs；模型策略分别配置普通输入、缓存创建、缓存读取价格。OpenAI `prompt_tokens_details.cached_tokens` 也会从普通输入拆出并按 cache read 费率计价。预授权输入维度按三类输入中的最高费率冻结，只有 total 时按所有价格中的最高值保守收费。
- 原始证据：`canonical.Usage` 和 Anthropic 非流/流适配器保留 cache creation/read token；`forwarder.settle` 与 `Billing/Pricing` 只接受 input/output；usage_logs 也没有缓存字段。
- 原始影响：prompt caching 的创建/读取用量没有成本，也无法对账。
- 原始修复方向：设计独立缓存创建/读取价格与日志字段；上游缺失细分 usage 时使用保守、模型无关的 fallback，避免按 0 处理。
- 原始验收测试：流式/非流式，cache_control 有/无，上游 usage 完整/缺失四组组合。

### AUD-P1-12 · 用量日志写失败后永久丢弃

- 状态：`部分修复`
- 修复记录（2026-07-11）：异步 Recorder/Sink 已删除。RecordConsumption 成功后，Finalize 会在同一 PostgreSQL 事务内完成余额差额、资金流水、最终 usage_logs 与 reservation 终态；Finalize 失败保留 `consumed_unsettled` 供 Recover。RecordConsumption 本身会有限幂等重试，但若持续失败，精确 usage 仍无持久待办，reservation 保持 in_flight 冻结并需人工按已记录 channel 对账，因此本项仍是部分修复。
- 原始证据：`internal/billing/recorder.go:153-160` 写 Sink 失败只记日志，batch 随即被清空；没有重试、持久队列或死信。
- 原始影响：余额已调整但账单凭证消失，无法审计和对账。
- 原始修复方向：使用持久 outbox / ledger；至少提供有界重试、退避、死信和报警。不能只依赖进程内 channel。

### AUD-P1-13 · 客户端可复用 `X-Request-Id` 让后续 usage_logs 静默冲突

- 状态：`已修复`
- 修复记录（2026-07-11）：入口始终生成服务端内部 request ID，并忽略入站 `X-Request-Id`；该内部 ID 只作为 `trace_id` 关联日志与 reservation。资金幂等与 `usage_logs.request_id` 使用另一枚服务端随机 reservation ID，客户端重复或超长头部均不能冲突账单。入站 ID 不受信与内部 ID 唯一性回归已通过。
- 原始证据：`internal/middleware/logger.go:90-95` 信任外部 request ID；`internal/db/usage_logs.sql.go:9-14` 以 request_id 为唯一键并 `DO NOTHING`。
- 原始影响：客户端重复发送相同 ID，后续请求仍扣 Redis，但账单记录被静默丢弃。
- 原始修复方向：内部账单/幂等 ID由服务端生成；外部 trace ID单独保存，仅用于链路关联。

### AUD-P1-14 · Shutdown 超时和 server 启动失败会跳过全部 defer

- 状态：`代码已修`
- 修复记录（2026-07-10）：server goroutine 只把 Start 错误送回主协程；启动失败或 Shutdown 超时均正常 return，不再调用 `log.Fatalf/os.Exit`，Redis、PG 与恢复任务的 defer 会执行。
- 原始证据：`cmd/linapi/main.go:85-104` 在 goroutine 和 Shutdown 失败路径调用 `log.Fatalf`，其底层 `os.Exit` 不执行 defer。
- 原始影响：Recorder 不冲刷、Redis/PG 不关闭、待结算 handler 被强制丢弃；与无限流式请求叠加后更易触发。
- 原始修复方向：错误传回主 goroutine，取消服务根 context；必要时 `Server.Close` 后正常 return，让 defer 完成冲刷和关闭。

## 6. P2：兼容性与健壮性问题

### AUD-P2-01 · Anthropic `tool_result` 图片 source 丢失

- 状态：`已修复`
- 修复记录（2026-07-11）：`tool_result.content` 的图片 block 复用 `imageSourceToCanonical`，URL/base64、media type 与 data 都进入 canonical，并能从 canonical 还原；字符串、文本块和图片混合往返测试已覆盖。
- 原始证据：`internal/adapter/anthropic/block.go:125-127` 遇到 image 只构造空 `BlockImage`，未解析 `source`。
- 原始影响：转发后图片无 URL/base64 内容。
- 原始修复方向：复用 `imageSourceToCanonical`，补字符串/图片混合 tool_result 往返测试。

### AUD-P2-02 · OpenAI `developer` role 在直通前被拒绝

- 状态：`已修复`
- 修复记录（2026-07-11）：canonical 新增有序 `RoleDeveloper`，OpenAI 解析/构造保持 system、developer 与正文的原顺序；同协议模型别名端到端回归验证 developer 指令到达上游。转成 Anthropic 时只把正文前的 system/developer 按顺序合并到顶层 system，正文后的 developer 会显式报错，不再静默改写优先级。
- 原始证据：`internal/adapter/openai/request.go:40-93` 仅接受 system/user/assistant/tool。
- 原始影响：现代客户端的 developer 指令无法使用，同格式直通也无法绕过 ParseRequest。
- 原始修复方向：canonical 增加中立 instruction/developer 语义并保持消息顺序，避免简单提升造成优先级变化。

### AUD-P2-03 · 跨格式 OpenAI 流式 usage 和错误事件丢失

- 状态：`部分修复`
- 修复记录（2026-07-11）：OpenAI 流内 `{"error":...}` 与 Anthropic error 已双向映射 `EventError`。Anthropic→OpenAI encoder 会跨事件累计 usage，先输出 finish choice，再输出标准 `choices:[]` 独立 usage 尾块并最终 `[DONE]`，缓存分项也能进入尾块。反向 OpenAI→Anthropic 时，OpenAI 尾块才出现的 input usage 无法在已经发出的 Anthropic `message_start` 中追补，协议线仍不可表达；Forwarder 仍在 encoder 外累计 canonical usage，账本不会因此漏计。
- 原始证据：`internal/adapter/openai/stream_encode.go:70-95` 丢弃 usage-only EventMessageDelta 和 EventError，encoder 也不累计 message_start / message_delta 的 usage。
- 原始影响：跨格式响应的 prompt_tokens 可能为 0，标准 usage 尾块缺失，流内错误消失。
- 原始修复方向：encoder 跨事件累计 usage，在结束时输出标准 `choices:[]` usage 尾块，再输出 `[DONE]`；错误按目标格式编码。

### AUD-P2-04 · 错误响应格式和上游响应头未按客户端协议转换

- 状态：`已修复`
- 修复记录（2026-07-11）：新增 canonical error model 与 OpenAI/Anthropic error codec；精确 method/path 的协议上下文在 Auth、限流、BodyLimit、Recovery 和 Forwarder 之前注入，本地及上游错误都按客户端端点协议编码。上游仅允许转发 `Retry-After`、rate-limit 头与经约束的 request ID，非 JSON/畸形 body 变为受限 `upstream_error`。跨格式、流式提交前、本地前置错误及安全头回归均已通过。
- 原始证据：网关 `writeError` 总是 OpenAI 风格；上游 4xx body 原样透传；`upstreamResponse` 不保存 Header。
- 原始影响：Anthropic SDK 或跨供应商客户端收到错误 wire schema；`Retry-After`、上游 request ID、rate-limit headers 丢失；非 JSON body 仍被标为 JSON。
- 原始修复方向：建立 canonical error model，按 clientFormat 编码；只透传安全允许列表响应头。

### AUD-P2-05 · 请求体、非流式响应和单条 SSE 记录没有大小上限

- 状态：`已修复`
- 修复记录（2026-07-11）：全局 `BodyLimit` 使用 `http.MaxBytesReader` 限制客户端 body（release 强制正值）；上游非流式响应上限为 32 MiB，单条 SSE record 上限为 4 MiB，均采用 limit+1 检测并返回显式错误。413、非流超限与 SSE record 超限回归已通过。
- 原始证据：客户端 `GetRawData`、上游非流式 `io.ReadAll`、SSE reader 累积 buffer 均无明确上限。
- 原始影响：恶意或异常客户端/上游可造成内存放大和进程 OOM。
- 原始修复方向：配置最大请求体、最大非流响应、最大 SSE record；超限返回明确错误并记录渠道失败。

### AUD-P2-06 · 管理请求字段缺失与分页在两种 Store 中语义不一致

- 状态：`已修复`
- 修复记录（2026-07-11）：启停请求改为 `*bool` 且 `binding:"required"`，`{}` 不再被解释为 false；账户/用户列表统一经 `queryInt` 校验，limit 限制为 1..500、offset 限制为 0..1,000,000,000，非法或越界参数在进入任一 Store 前返回 400。
- 原始证据：启停请求的 `Enabled bool` 无 `required` 或指针，`{}` 会被解释为 false；`queryInt` 不约束负数/极大值，内存实现会修正部分负值，PostgreSQL 可能报错。
- 原始影响：误禁用资源，或同一 API 在开发与生产环境表现不同。
- 原始修复方向：显式校验 required；统一 limit/offset 范围并在 handler 层拒绝非法参数。

### AUD-P2-07 · 配置和文档存在已确认偏差

- 状态：`已修复`
- 修复记录（2026-07-11）：`config.Load` 对显式指定但不存在的配置文件回退到内置默认值，并已有缺失文件回归测试；旧 token 管理面已由会话鉴权控制台替代，计费文档也已同步为 PostgreSQL 权威账本，不再描述 Redis 热余额同步。
- 原始证据：
  - 项目文档说配置文件缺失走默认值，但 `config.Load` 使用显式 `SetConfigFile` 后，实际缺失路径会返回错误并退出。
  - `admin.enabled=true` 且 token 为空，注释说启动失败，实际只是启动后所有管理请求返回 401。
  - `docs/progress.md` / `docs/modules.md` 声称充值会刷新 Redis、`admin.Service` 聚合 Billing，实际代码未实现。
- 原始修复方向：修代码或修文档，但最终必须保持二者一致；为 config.Load 和启动校验补测试。

## 7. 已通过的基线验证

审查期间执行并通过：

```bash
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
go mod verify
go list -deps ./...
git diff --check
```

覆盖率快照：

| 包 | 覆盖率 |
|---|---:|
| `internal/billing` | 93.3% |
| `internal/forwarder` | 74.1% |
| `internal/routing` | 75.8% |
| `internal/adapter/openai` | 58.6% |
| `internal/adapter/anthropic` | 54.6% |
| `internal/server` | 33.7% |
| `internal/store` | 37.0% |
| `cmd/linapi` / `internal/config` / `internal/db` | 0.0% |

未执行：

- `staticcheck`：当前环境未安装。
- `govulncheck`：当前环境未安装。
- `sqlc generate` 一致性检查：当前环境未安装 sqlc。
- 真实 PostgreSQL 端到端测试：现有 PG 测试主要使用 fake Querier。

## 8. 修复前必须补的回归测试矩阵

### 8.1 计费与恢复

- Redis key 删除/过期/重启后余额不恢复。
- reservation / settlement 重放幂等。
- Settle 脚本执行成功但响应 timeout。
- 用量 Sink 短暂失败、持续失败、进程重启后的恢复。
- 外部重复 X-Request-Id 不影响内部账单唯一性。
- `/v1/models` 不改变余额。
- 首次余额不足后 TTL 正常，充值可见。

### 8.2 流式

- 同格式 OpenAI 未传 include_usage。
- 上游不发 usage、只发部分 usage、usage 尾块前客户端断开。
- 首事件前 EOF、内容后 EOF、畸形 SSE、HTTP 200 内 error。
- 上游不回 header、200 后不发首事件、事件间停滞。
- OpenAI ↔ Anthropic 跨格式 usage 与错误事件。

### 8.3 路由与熔断

- 客户端主动 cancel 不影响 breaker。
- Open 后旧请求迟到成功不关闭 breaker。
- 401/403 凭证故障切换备份渠道。
- 400 等真正客户端错误不做无意义故障转移。

### 8.4 协议

- Anthropic 字符串/数组 content。
- OpenAI 字符串/数组 stop。
- Anthropic tool_result 文本+图片往返。
- OpenAI developer role 语义顺序。
- Anthropic/OpenAI 两种客户端错误格式。

## 9. 后续 AI 接手步骤

下一位 AI 应严格按以下顺序继续：

1. 读取 `git status`、`git diff` 和最近提交，确认进行中的代码与文档基线。
2. 重新运行第 7 节基线命令，并执行账本新增测试；任何失败先归因。
3. 在真实 PostgreSQL 上完成批次 B 的并发预授权、提交结果未知、重复重放与崩溃恢复测试。
4. 首次部署前冻结旧资金写入，人工对账 PostgreSQL、旧 Redis 热余额与供应商账单，校正 `users.balance` 并保存对账证据；禁止让自动迁移猜历史余额。
5. 完成上述两项后，把 P0 的“代码已修”升级为“已修复”，并记录提交、环境与验证证据。
6. 继续认领批次 C/D/E/F/H；修复后回填跟踪表，不删除原始问题描述和历史证据。

## 10. 跟踪表

| ID | 严重度 | 简称 | 状态 | 认领者 | 提交/PR | 验证 |
|---|---|---|---|---|---|---|
| AUD-P0-01 | P0 | Redis 丢失后余额恢复 | 代码已修 | Codex | 当前工作区 | PG 权威账本；30s Recover；5m reserved 退款；24h in_flight 告警；上线前对账/真实 PG 测试 |
| AUD-P0-02 | P0 | 流式 usage 缺失按 0 结算 | 代码已修 | Codex | 当前工作区 | 直通强制 include_usage + usage 已知位 + 缺失保守保留预授权；真实 PG 终态待上线验收 |
| AUD-P0-03 | P0 | 结算失败后错误退款 | 代码已修 | Codex | 当前工作区 | MarkInFlight 后仅明确未消费可 release/退款；真实 PG 提交未知/Recover 待验收 |
| AUD-P1-01 | P1 | `/v1/models` 永久扣押金 | 代码已修 | Claude/Codex | 当前工作区 | 旧 Quota 已删除；`TestModelsEndpointDoesNotConsumeBilling` 验证 models 不触碰 Ledger/余额 |
| AUD-P1-02 | P1 | 充值未同步热余额 | 代码已修 | Codex | 当前工作区 | Redis 退出资金路径；AddBalance 与 Ledger 共用 PG 权威余额 |
| AUD-P1-03 | P1 | 余额不足 key 无 TTL | 代码已修 | Codex | 当前工作区 | Redis Account/余额 key 路径已删除，余额不足由 PostgreSQL 条件预授权判定，不再存在该 TTL 状态 |
| AUD-P1-04 | P1 | 客户端取消污染 breaker | 已修复 | Codex | 当前工作区 | neutral outcome 停止重试；permit 释放但不改健康；取消端到端回归通过 |
| AUD-P1-05 | P1 | 迟到成功关闭 Open breaker | 已修复 | Codex | 当前工作区 | generation permit 忽略旧代迟到结果；late-success/neutral HalfOpen 回归通过 |
| AUD-P1-06 | P1 | 截断流记为成功 | 代码已修 | Codex | 当前工作区 | EOF/读取失败/缺终态均记渠道失败并保守结算；不再静默成功 |
| AUD-P1-07 | P1 | 流式超时缺失 | 代码已修 | Codex | 当前工作区 | 30s 响应头 + 2m 首字节/事件空闲；idle 单测通过，真实响应头挂起集成未单跑 |
| AUD-P1-08 | P1 | 上游 401/403 不故障转移 | 代码已修 | Codex | 当前工作区 | 明确未消费 401/403 先 ReleaseAttempt，再安全故障转移 |
| AUD-P1-09 | P1 | 程序入口被 Git 忽略 | 已修复 | Claude | 010b851 | `.gitignore` 裸 `linapi` 改 `/linapi` 锚定仓库根，不再误伤 `cmd/linapi/` 源码目录 |
| AUD-P1-10 | P1 | 合法联合字段被拒绝 | 已修复 | Codex | 当前工作区 | Anthropic content 字符串/数组与 OpenAI stop 字符串/数组往返通过 |
| AUD-P1-11 | P1 | Claude 缓存 token 漏计 | 代码已修 | Codex | 当前工作区 | 缓存创建/读取独立费率 + usage 入账；预授权按最贵输入维度 |
| AUD-P1-12 | P1 | 用量日志失败即丢弃 | 部分修复 | Codex | 当前工作区 | RecordConsumption 后可 Recover；其自身持续失败时精确 usage 仍无持久待办，in_flight 冻结待人工对账 |
| AUD-P1-13 | P1 | 外部 request ID 冲突账单 | 已修复 | Codex | 当前工作区 | 入站 ID 被忽略；内部 request ID 作 trace，独立 reservation ID 作账单唯一键；回归通过 |
| AUD-P1-14 | P1 | Fatalf 跳过优雅关闭 | 代码已修 | Codex | 当前工作区 | Start 错误回主协程；启动失败/Shutdown 超时正常 return，defer 会执行 |
| AUD-P2-01 | P2 | tool_result 图片丢失 | 已修复 | Codex | 当前工作区 | URL/base64 source 双向保留；混合 tool_result 往返通过 |
| AUD-P2-02 | P2 | developer role 被拒绝 | 已修复 | Codex | 当前工作区 | canonical 有序 developer；同协议保序、Anthropic 不可表达位置显式拒绝 |
| AUD-P2-03 | P2 | 跨格式流 usage/error 丢失 | 部分修复 | Codex | 当前工作区 | EventError 双向；Anthropic→OpenAI 已 choices:[] usage 尾块；反向迟到 input 线协议不可追补但账本保留 |
| AUD-P2-04 | P2 | 错误格式/响应头未转换 | 已修复 | Codex | 当前工作区 | 全链路协议 error codec + 安全响应头允许列表；前置/跨格式/流式回归通过 |
| AUD-P2-05 | P2 | IO 大小无上限 | 已修复 | Codex | 当前工作区 | 请求体、32MiB 非流响应、4MiB SSE record 限制及回归通过 |
| AUD-P2-06 | P2 | 管理参数语义不一致 | 已修复 | Codex | 当前工作区 | required bool 指针；limit/offset 有界解析后再进 Store |
| AUD-P2-07 | P2 | 配置与文档偏差 | 已修复 | Codex | 当前工作区 | 显式配置文件缺失回退默认值并有回归测试；旧 token 管理面和 Redis 热余额文档已与当前实现对齐 |
| AUD-P0-04 | P0 | 固定押金无法阻止真实成本超卖 | 代码已修 | Codex | 当前工作区 | n=1；release 每个 OpenAI channel/upstream_model 显式配置并在发送前补丁 max_tokens 或 max_completion_tokens；最贵缓存输入/输出上界预授权；cost 不得超过 reservation |
| AUD-P0-05 | P0 | Redis 自动重放非幂等余额脚本 | 代码已修 | Codex | 当前工作区 | Redis 无资金命令；PG operation ID + 状态机幂等；丢响应重放待真实 PG 验收 |
| AUD-P0-06 | P0 | 非流式 usage 缺失按零成本结算 | 代码已修 | Codex | 当前工作区 | usage 已知位 + total 保守价；total/cache 明细冲突按预授权上限；missing/partial 保留预授权；真实 PG 账单字段待验收 |
| AUD-P1-15 | P1 | 已送达但响应未知的请求被跨渠道重放 | 代码已修 | Codex | 当前工作区 | in_flight 记录 channel；网络/408/5xx 不重放不退款；24h 后人工对账 |
| AUD-P1-16 | P1 | 跨域重定向泄露 Anthropic 密钥与请求 | 代码已修 | Codex | 当前工作区 | 禁止自动 redirect；3xx 不重放不退款；跨主机 307 目标未收到请求/凭证 |
| AUD-P1-17 | P1 | 禁用或改密后旧会话继续有效 | 已修复 | Claude/Codex | 当前工作区 | session_version 禁用/改密递增；鉴权回查、陈旧会话删除并 401；端到端回归通过 |
| AUD-P1-18 | P1 | PostgreSQL 设置读写不是原子快照 | 代码已修 | Codex | 当前工作区 | 单 SELECT 快照 + 单 statement 多行 upsert；真实 PG 并发故障注入未单跑 |
| AUD-P1-19 | P1 | 入站 body 与 keep-alive 无超时 | 代码已修 | Codex | 当前工作区 | release 强制 ReadTimeout/IdleTimeout/MaxHeaderBytes；真实慢 socket 验收未单跑 |
| AUD-P1-20 | P1 | `/healthz` 永远就绪 | 已修复 | Codex | 当前工作区 | healthz/livez 仅存活；readyz 检查 Redis/启用的 PG，依赖失败 503 回归通过 |
| AUD-P1-21 | P1 | 计价溢出可产生负成本并充值 | 代码已修 | Codex | 当前工作区 | checked 乘加/取整；异常 usage 保守保留预授权 |
| AUD-P1-22 | P1 | 模型别名静默删除未建模能力 | 代码已修 | Codex | 当前工作区 | 同协议别名只补丁 model，保留 max_completion_tokens 与未知请求字段 |
| AUD-P1-23 | P1 | `n>1` 与多 choices 静默降级 | 已修复 | Codex | 当前工作区 | 触网前拒绝 n!=1；非流/流异常多 choice/index 显式失败且已消费不重试 |
| AUD-P1-24 | P1 | 合法 SSE 记录语义解析失败 | 已修复 | Codex | 当前工作区 | 共享 SSE 读取支持 UTF-8 BOM、LF/CRLF/裸 CR、comment/event/id/retry 与多 data 行，回归已覆盖 |
| AUD-P1-25 | P1 | 工具参数强制 map 导致拒绝或精度损失 | 已修复 | Codex | 当前工作区 | RawMessage+UseNumber；截断/非对象/大整数/已消费不重试回归通过 |
| AUD-P2-08 | P2 | 密码长度按 UTF-8 字节计算 | 已修复 | Codex | 当前工作区 | RuneCount + bcrypt 72 字节上限；Unicode 与 72/73 边界通过 |
| AUD-P2-09 | P2 | `CreateAccount` 可绕过 user 计费实体 | 代码已修 | Codex | 当前工作区 | user 强制原子 CreateUserAccount + schema CHECK；真实 PG 回滚故障注入未单跑 |
| AUD-P2-10 | P2 | `auto_migrate` 无法升级已有表 | 代码已修 | Codex | 当前工作区 | 版本化迁移、checksum、advisory lock、关闭时验版本；真实旧库/多实例 PG 未验 |
| AUD-P2-11 | P2 | PG 外键错误与内存 NotFound 语义不一致 | 代码已修 | Codex | 当前工作区 | SQLSTATE 23503→ErrNotFound fake 回归通过；真实 PG 外键请求未单跑 |
| AUD-P2-12 | P2 | `int→int32` 回绕可关闭限流 | 已修复 | Codex | 当前工作区 | Key rate、分页及 channel priority/weight 均在领域边界有界后才缩窄；越界回归通过 |
| AUD-P2-13 | P2 | `max_idle_conns` 被当成 `MinConns` | 已修复 | Codex | 当前工作区 | 配置改 min_idle_conns，并校验 0<=min<=max 后映射 pgx MinConns |
| AUD-P2-14 | P2 | Redis 热余额仍每请求查冷源 | 代码已修 | Codex | 当前工作区 | Quota/Account 删除，直接 PG Ledger 条件预授权 |
| AUD-P2-15 | P2 | 分布式限流使用实例本地时钟 | 已修复 | Codex | 当前工作区 | Lua 内 Redis TIME 为唯一时钟，所有 Redis 桶复用 |
| AUD-P2-16 | P2 | Recorder 关闭无总预算 | 代码已修 | Codex | 当前工作区 | Recorder 删除，最终用量与余额同步事务提交 |
| AUD-P2-17 | P2 | panic 绕过观测且 debug 日志泄密 | 已修复 | Codex | 当前工作区 | 自定义 Recovery 内置于日志/指标；panic 500 可观测且不记录凭证/panic 值 |
| AUD-P2-18 | P2 | 内存 API Key 可被静默重绑定 | 已修复 | Codex | 当前工作区 | seed/运行时同时检查明文与 KeyID，冲突不覆盖；回归通过 |
| AUD-P2-19 | P2 | 内存 Store 时间字段错误 | 已修复 | Codex | 当前工作区 | user created/updated 持久更新；API Key created_at 跨创建/列表/启停保持一致 |
| AUD-P2-20 | P2 | 内存余额溢出回绕而 PG 拒绝 | 已修复 | Codex | 当前工作区 | checked add，极值溢出返回 ErrBalanceOverflow 且余额不变 |
| AUD-P0-07 | P0 | 开放注册可无限复制赠送额度 | 已修复 | Claude | master | 自助注册恒绑定初始余额 0（忽略 `NewUserInitialBalance`）；putSettings 拒绝正初始余额双重堵路径；发放额度只能走管理面主动建号/充值；`TestRegisterGrantsNoBalance`、`TestPutSettingsRejectsPositiveInitialBalance` |
| AUD-P1-26 | P1 | Cookie 管理面缺少 CSRF 边界 | 已修复 | Claude | master | `CSRFProtect` 中间件：双重提交 token（会话绑定 CSRFToken vs X-CSRF-Token header）+ 写请求强制 JSON + Origin/Referer 校验；`/me` `/admin` 写端点全挂；csrf_test.go 全覆盖 |
| AUD-P1-27 | P1 | 登录注册无滥用限速和会话上限 | 已修复 | Claude/Codex | 当前工作区 | IP+登录标识摘要桶、TryAcquire、每账户原子会话上限及 trusted proxies=nil；回归通过 |
| AUD-P1-28 | P1 | 普通用户可建无限量不限速 Key | 已修复 | Claude/Codex | 当前工作区 | 单 Key 1..5000；原子 50 上限；所有 Key 共享账户总桶；列表由硬上限约束 |
| AUD-P1-29 | P1 | 登出撤销失败仍返回成功 | 已修复 | Claude | master | logout 用独立 3s 超时 context 删会话（不复用请求 context）；删除失败回 503 且不清 Cookie，不谎报登出；`TestLogoutFailsWhenSessionDeleteFails` |
| AUD-P1-30 | P1 | SSE 慢读可永久占用转发资源 | 代码已修 | Codex | 当前工作区 | 每事件刷新 30s 写 deadline；真实 TCP 停读验收未跑 |
| AUD-P1-31 | P1 | 渠道 URL 缺少 SSRF/明文策略 | 已修复 | Codex | 当前工作区 | 结构化 URL + release HTTPS + 精确私网规则 + 拨号期 IP/rebind 防护；单测通过 |
| AUD-P1-32 | P1 | 远程 Redis 无 TLS 可泄露会话与余额 | 代码已修 | Codex | 当前工作区 | ACL/TLS/CA/mTLS + release 远程明文拒绝 + 会话 token 摘要；真实 TLS Redis 未跑 |
| AUD-P1-33 | P1 | 上游供应商密钥明文落 PostgreSQL | 已修复 | Codex | 当前工作区 | AES-256-GCM envelope+AAD；外部主密钥；默认拒绝明文、维护窗口事务迁移；不回显测试通过 |
| AUD-P1-34 | P1 | 无效 Key 可绕过限流耗尽 PG/日志 | 已修复 | Codex | 当前工作区 | 查库前 IP 桶 + 非阻塞 DB gate + MaxHeaderBytes + 服务端 request ID/日志截断 |
| AUD-P2-21 | P2 | 注册接口泄露用户名存在性 | 已修复 | Codex | 当前工作区 | 新建/冲突统一 201+ok，且都先做同等密码工作；响应一致性回归通过 |
| AUD-P2-22 | P2 | `/metrics` 默认公开且无抓取预算 | 已修复 | Codex | 当前工作区 | token 鉴权；release 必填；并发与 timeout 预算回归通过 |
| AUD-P2-23 | P2 | 默认信任所有代理头可伪造来源 IP | 已修复 | Codex | 当前工作区 | Gin `SetTrustedProxies(nil)`，来源 IP 限流与审计仅信任 socket 对端 |
| AUD-P2-24 | P2 | go-redis 命中可达的响应错位公告 | 已修复 | Codex | 当前工作区 | 依赖升级 v9.7.3；目标包测试通过，未另做 SETINFO 故障注入 |

## 11. 当前发布判断

截至 2026-07-11，大部分 P0/P1/P2 利用路径已完成代码修复与回归，但仍不能仅凭单元测试判定可商用发布：

- P1-12 仍缺 RecordConsumption 持续失败时的独立持久 outbox；在明确接受“冻结并人工按 channel 对账”的运维模型前，不应把该窗口描述为完全闭合。
- 首次切换新账本前必须冻结旧资金写入，人工对账 PostgreSQL、Redis 与供应商账单并校正 `users.balance`。
- 必须在真实 PostgreSQL 演练增量迁移、多实例并发预授权、提交结果未知、崩溃恢复与幂等重放；在真实 Redis TLS/ACL 环境验证证书、ServerName 与 mTLS 失败路径。
- 渠道明文密钥迁移只能在维护窗一次开启 `migrate_plaintext`，完成后立即关闭并轮换历史供应商 key。
- 不应把当前“全部测试通过”解释为生产数据已经迁移或真实依赖已经完成故障注入。

## 12. 第二轮增量审查基线

第二轮继续采用主智能体 + 3 条并行审查线，并由不同智能体对资金路径的两个新发现做了交叉复核。为避免把 Claude 正在进行的控制台后端接线误判为缺陷，本节锁定以下快照：

- 审查提交：`cfae1ab4ba80d8ebd51bc2809dcf22ee0a3dc778`。
- Claude 并行任务状态：账户、会话、部分 AdminStore 已提交；`/auth`、`/me`、新 `/admin` 路由尚未完成接线。
- 当前全量 `go test ./...` 的唯一编译失败是 `AdminConfig` 已删除旧 `Token/LoopbackOnly`、而 `server.go` 仍处于下一提交接线前的中间态。该暂态不是本审查问题。
- 第二轮所有新增项均已与第一轮 24 项去重；行号在 Claude 完成后必须重新校准。
- 本轮仍只修改审查文档，没有修改业务代码、配置、Schema 或测试。

## 13. 第二轮新增 P0

### AUD-P0-04 · 固定押金无法阻止真实成本远超余额

- 状态：`代码已修`
- 修复记录（2026-07-11）：Forwarder 强制 OpenAI `n=1` 与输出上限；release 模式要求每个 OpenAI `channel/upstream_model` 显式配置 `max_tokens` 或 `max_completion_tokens`，并在每个候选发送前按该策略补丁对应 wire 字段。预授权输入按普通输入/cache creation/cache read 最高费率，输出按上限，再持久 Reserve；直通请求也会重编码安全字段以折叠重复 key。Billing 和 Ledger 都拒绝实际 cost 超过 reservation。
- 上线验收：仓库回归覆盖并发预授权与强制上限；真实 PostgreSQL 上仍需验证同账户多实例并发请求的累计冻结额不会超过权威余额。
- 原始证据：
  - `internal/middleware/quota.go:22-23,40-42`：尚未解析 `model`、输入量和 `max_tokens` 就预扣。
  - `internal/billing/billing.go:43-58`：每个请求始终只扣固定 `defaultReserve`。
  - `internal/billing/account.go:84-88`：实际成本高于押金时允许余额跌到很大的负数。
  - `internal/billing/billing_test.go:23-43`：现有测试明确允许余额 100000、押金 5000 的请求结算 12500000 成本。
- 原始触发：单个高价长请求，或多个高成本请求在各自仅占用固定押金时并发通过。
- 原始影响：余额只能限制“并发请求数 × 固定押金”，不能限制真实最大上游成本；恶意用户可用很小余额制造远高于余额的供应商账单。
- 原始修复方向：解析并校验请求后，按输入估算、模型价格和服务端强制的最大输出 token 计算最坏成本；无输出上限时必须加服务端上限；把在途信用暴露纳入持久 reservation 账本。
- 原始验收测试：并发高 `max_tokens` 请求的累计最坏成本一旦超过余额，后续请求必须在调用上游前被拒绝。

### AUD-P0-05 · go-redis 自动重放非幂等余额脚本，可重复扣款或退款

- 状态：`代码已修`
- 修复记录（2026-07-10）：Redis 不再执行资金增量 Lua。PostgreSQL 账本使用内部 reservation ID、阶段 operation ID、唯一约束与 reserved/in_flight/consumed_unsettled 迁移；透明重试不能重复改变余额，settle/refund 互斥，发送结果未知不会被自动退款。
- 上线验收：需在真实 PostgreSQL/网络代理下模拟执行或提交成功后丢响应，并重放 Reserve、RecordConsumption、Finalize、Refund，确认每阶段资金只变化一次。
- 原始证据：
  - `go.mod:10` 使用 `go-redis/v9 v9.7.0`；该版本在未显式配置时默认最多重试 3 次。
  - `internal/redisx/redisx.go:20-24` 未设置 `MaxRetries`。
  - `internal/billing/account.go:37-56,102-109` 的脚本直接 `INCRBY`，没有 reservation ID、operation ID 或结果去重表。
- 原始触发：Redis 已执行脚本，但响应在返回前发生 EOF、`UnexpectedEOF` 或可重试读超时；客户端随后自动重发同一 `EVALSHA`。
- 原始影响：
  - `Reserve` 可能重复扣押金，甚至最后一次返回余额不足而外层没有 reservation 可退款。
  - 正向 `Settle`/ `Refund` 可重复入账，负向 `Settle` 可重复扣款。
  - 自动重放最终可能返回成功，外层完全不知道余额已经变化多次。
- 与 `AUD-P0-03` 的区别：旧项是最终结算返回错误后 Forwarder 又显式退款；本项发生在一次 `Account.adjust` 内部，同时影响 Reserve、Settle、Refund，并可能最终返回 nil。
- 原始修复方向：短期对资金命令关闭透明重试只能缩小窗口；根治需要持久 operation ID 与 `new → reserved → settled | refunded` 状态机，Lua/数据库事务按 operation ID 返回首次结果，settle 与 refund 互斥且幂等。
- 原始验收测试：用 RESP/TCP 代理模拟“执行成功后丢第一次响应”，Reserve、正/负 Settle、Refund 每个操作都只能改变一次余额。

### AUD-P0-06 · 非流式成功响应缺失或只有总 usage 时按零成本结算

- 状态：`代码已修`
- 修复记录（2026-07-11）：adapter/canonical 显式保存 usage 已知位。分项完整时按普通输入、输出、缓存创建、缓存读取独立费率；只有 total 时按所有维度最高单价；total 与普通输入/输出/cache 明细冲突时按预授权上限保守结算。其它异常同样保留整笔预授权并标记 estimated，实际 cost 超过 reservation 会被 Billing 与 Ledger 拒绝。
- 上线验收：仓库回归覆盖 missing/null/total-only/单边/冲突；生产放行前需用真实 PostgreSQL 验证这些响应写入的 cost、estimated、usage_complete 与余额一致。
- 原始证据：
  - `internal/adapter/openai/response.go:56-61`：只有 `usage != nil` 才复制分项；`total_tokens` 已解析但未用于补全。
  - `internal/adapter/anthropic/response.go:26-33`：usage 缺失时同样留下零值。
  - `internal/forwarder/nonstream.go:62-70`：2xx 解析成功后无条件用零值结算。
  - `internal/billing/pricing.go:45-50` 与 `billing.go:68-70`：0/0 得到成本 0，并由 Settle 成功退回全部押金。
- 原始触发：任一兼容上游在非流式 2xx 响应中省略 `usage`、返回 `null`，或 OpenAI 只给 `total_tokens`。
- 原始影响：同格式直通与跨格式都会形成系统性免费调用，并写入一条“成功、零成本”的账单；外层退款 guard 也发现不了。
- 与 `AUD-P0-02` 的区别：旧项限定流式 usage 尾块；本项覆盖 OpenAI/Anthropic 的所有非流式路径。
- 原始修复方向：canonical 显式表示 `missing / partial / complete`；可验证的 total+单边才允许推导，只有 total 或完全缺失时采用保守费用、保留押金或进入 `consumed_unsettled` 待对账，绝不能把缺失解释为真实 0。
- 原始验收测试：OpenAI 的 missing/null/total-only/单边/分项冲突，以及 Anthropic missing/partial，均不得全额退押金或生成零成本成功账单。

## 14. 第二轮新增 P1

### AUD-P1-15 · 请求可能已经被上游消费，却被自动跨渠道重放

- 状态：`代码已修`
- 修复记录（2026-07-10）：每次发送前持久 MarkInFlight 并记录 channel。网络错误、408、5xx 与响应读取/解析的不确定结果保留预授权、停止跨渠道重放和退款；只有能证明未生成的 4xx 才 ReleaseAttempt，其中渠道拒绝可安全故障转移。超过 24 小时的 in_flight 按 channel 告警人工对账。
- 原始证据：`internal/forwarder/upstream.go:71-74` 把所有 `Client.Do` 错误合并；`nonstream.go:44-47,62-66` 把网络、读响应和 2xx 解析错误都视为渠道故障；`forwarder.go:155-165` 随即尝试下一个渠道。
- 原始触发：上游读完并处理请求后，在返回响应头/完整 body 前断线；或已经计费的 2xx body 不完整、格式异常。
- 原始影响：多个上游都可能产生真实供应商费用，网关只结算最终成功渠道；全部失败时还可能退款。
- 原始修复方向：通过 `httptrace` 等手段区分“请求未写出、可安全重试”和“已写出、消费状态未知”；未知状态进入待对账，除非供应商提供可靠幂等键，否则不得自动重放。
- 原始验收测试：首个上游读完 body 后主动断开，断言不会静默调用第二渠道并把第一笔消费视为零。

### AUD-P1-16 · 默认跟随跨域重定向会泄露 Anthropic 密钥和用户请求

- 状态：`代码已修`
- 修复记录（2026-07-10）：流式/非流式 `http.Client` 均设置 `CheckRedirect=http.ErrUseLastResponse`，禁止自动重放。3xx 可能是 POST 已处理后的跳转，故保留 in_flight 并停止故障转移/退款。跨主机 307 回归测试断言目标服务未收到请求或凭证，且预授权保持冻结待对账。
- 原始证据：`internal/forwarder/upstream.go:33-43` 的两个 `http.Client` 都未设置 `CheckRedirect`；`upstream.go:60-64,102-109` 设置请求体和 `x-api-key`。
- 原始触发：渠道上游返回跨域 307/308。
- 原始影响：Go 会向重定向目标重发 POST body；自定义 `x-api-key` 不在标准库跨域重定向的敏感头名单内，因此 Anthropic 渠道密钥和用户提示词会一起泄露；目标也可能是网关所在内网。
- 原始修复方向：默认禁止重定向并把 3xx 归为渠道配置错误；确有需要时只允许同 scheme、同 host 的严格白名单跳转。
- 原始验收测试：两个测试服务模拟跨主机 307，第二个服务不得收到请求体或 `x-api-key`。

### AUD-P1-17 · 禁用账户或重置密码不会使已有会话失效

- 状态：`已修复`
- 修复记录（2026-07-11）：账户新增 `session_version`；禁用账户或更新密码时原子递增，登录时把当前代次写入会话。`SessionAuthWithVersion` 每次鉴权回查账户当前代次，账户不存在、被禁用、查询失败或代次不一致都 fail-closed；陈旧会话会删除并返回 401。禁用与改密撤销旧会话的端到端回归及中间件错误路径均已通过。
- 原始证据：
  - `internal/session/session.go:32-38,50-86` 把 Role/ExternalID 作为登录快照，只提供单 token 的 Create/Get/Delete。
  - `internal/middleware/session_auth.go:20-38,43-55` 只信 Redis 快照并直接按快照角色授权。
  - `internal/account/postgres.go:129-145` 与 `memory.go:136-155` 的禁用/改密路径不触碰会话。
- 原始触发：账户已登录后被管理员禁用，或密码因泄露而重置。
- 原始影响：旧 Cookie 仍可使用 24 小时，勾选“记住我”时最长 7 天；被禁用的管理员仍保留管理权限。该问题在新控制台路由接线后立即可触发。
- 原始修复方向：引入账户 `session_version` 并在鉴权时校验，或维护账户到 token 的索引，在禁用、改密、角色变化时原子撤销全部会话。
- 原始验收测试：多设备登录后禁用/改密，所有旧 token 均返回 401，新密码重新登录成功。

### AUD-P1-18 · PostgreSQL 系统设置不是原子快照

- 状态：`代码已修`
- 修复记录（2026-07-11）：PostgreSQL `Get` 改为单条 `GetSettingsSnapshot` 查询，`Put` 改为单条多行 `INSERT ... ON CONFLICT` 的 `UpsertSettingsSnapshot`，同一 SQL statement 内原子提交两项设置，不再出现两次 SELECT/UPSERT 的撕裂窗口。查询映射测试已通过；尚未单独运行真实 PostgreSQL 并发快照故障注入。
- 原始证据：`internal/account/settings.go:26-31` 把 Put 定义为整体覆盖；`postgres.go:147-166` 却分别执行两次 SELECT 和两次 UPSERT；`memory.go:158-168` 是锁内整体读写。
- 原始触发：第二次 UPSERT 失败/请求取消，或多个管理员并发写入、读取。
- 原始影响：API 返回失败但注册开关已改变；还可能读出从未由任何一次 Put 提交过的“新开关 + 旧初始余额”，错误开放注册并发放错误额度。
- 原始修复方向：写入使用单条多行 UPSERT 或事务；读取用单条查询取得完整快照，不能只依赖 READ COMMITTED 下的两次 SELECT。
- 原始验收测试：第二次写失败必须整体回滚；并发切换两组设置时，读结果只能等于其中一个完整提交。

### AUD-P1-19 · 入站请求体和空闲 keep-alive 没有超时

- 状态：`代码已修`
- 修复记录（2026-07-11）：ServerConfig 新增并应用非零 `ReadTimeout`、`IdleTimeout` 和 `MaxHeaderBytes`，release 对非正值 fail-fast；全局 `WriteTimeout` 继续保持为零，避免误杀长 SSE。`TestLivenessAndReadinessAreSeparated` 同时断言超时字段已装配；真实 TCP 慢 body/空闲连接验收尚未单独运行。
- 原始证据：`internal/server/server.go:54-60` 只设置 `ReadHeaderTimeout`，`ReadTimeout` 与 `IdleTimeout` 均为零。
- 原始触发：客户端发完头后无限慢速发送 body，或请求结束后长期保持 keep-alive。
- 原始影响：handler、goroutine 和文件描述符可被长期占用；访问公开 `/healthz` 后保持空闲连接不需要 API Key。
- 原始修复方向：配置非零请求体读取期限和 `IdleTimeout`；继续不设置全局 `WriteTimeout`，长 SSE 的事件空闲超时单独治理。
- 原始验收测试：真实 listener 下模拟慢 body/空闲连接，按期关闭；长 SSE 不应被 IdleTimeout 中断。

### AUD-P1-20 · `/healthz` 永远返回就绪

- 状态：`已修复`
- 修复记录（2026-07-11）：`/healthz` 与 `/livez` 明确只表示进程存活；新增 `/readyz`，用短超时调用装配的 Redis PING 及启用数据库的 PG PING，依赖失败返回 503。httptest 已覆盖 live 仍 200、ready 依赖失败为 503。
- 原始证据：`internal/server/server.go:76-79` 注释称端点供探活与就绪使用，但 handler 无条件返回 200。
- 原始触发：启动后 Redis 或启用的 PostgreSQL 失联。
- 原始影响：鉴权和额度请求持续 500，负载均衡仍把实例当作可接流量。
- 原始修复方向：拆分静态 `/livez` 与带短超时、可短暂缓存的 `/readyz`；ready 至少检查 Redis 和启用的数据库。
- 原始验收测试：断开依赖后 live 仍为 200、ready 变 503；恢复后 ready 回到 200。

### AUD-P1-21 · 计价整数溢出可产生负成本并反向充值

- 状态：`代码已修`
- 修复记录（2026-07-10）：`Pricing` 对 token、单价乘法、两项相加和向上取整全部做 checked arithmetic；非法/溢出返回显式错误。结算遇到异常 usage 时保守保留预授权，不会产生负成本或反向充值。
- 原始证据：`internal/billing/pricing.go:45-51` 的 token×单价、两项相加以及 `total+999999` 均无溢出检查；`billing.go:68-70` 直接把“押金 - 成本”作为余额 delta。
- 原始触发示例：`InputPer1M=math.MaxInt64` 且上游报告 1 个 input token；向上取整前的加法溢出为负数。
- 原始影响：负成本变成巨额正 delta，结算反而给用户充值；乘法溢出到非正数时还会被当成零成本。
- 原始修复方向：价格、token、押金均校验非负和合理上限；使用安全乘加和商余式向上取整；任何溢出必须显式失败并进入异常结算，不能回绕。
- 原始验收测试：MaxInt64、乘法边界、负 token、异常巨大 usage 下，成本永远非负且不静默饱和或回绕。

### AUD-P1-22 · 模型重命名会静默删除未建模的请求能力

- 状态：`代码已修`
- 修复记录（2026-07-10）：同协议模型别名路径不再重建 canonical 请求，而是在已规范化的原始 JSON 上只重写 `model`，保留 `max_completion_tokens` 与未知扩展字段；跨格式能力缺口仍由其它协议项跟踪。
- 原始证据：
  - `internal/forwarder/forwarder.go:204-211`：只有上下游模型名完全相同才直通。
  - `nonstream.go:24-41` 与 `stream.go:33-49`：模型别名会改走 ParseRequest → BuildRequest。
  - `internal/adapter/openai/types.go:11-24`、`anthropic/types.go:9-22` 和 `canonical/message.go:9` 只建模字段子集。
- 原始触发：配置“对外模型名 → 上游模型名”映射，即使客户端与渠道使用相同协议。
- 原始影响：OpenAI `response_format`、seed、penalty、logprobs、parallel tool controls、自定义扩展字段，以及 Anthropic thinking 等能力会被静默删除；结构化输出可能退化成普通文本。
- 原始修复方向：同格式别名路径对原始 JSON 做保真补丁，只改 model，并最小合并计费所需字段；跨格式无法表达的能力应明确拒绝，不能静默忽略。
- 原始验收测试：模型别名下逐字段验证标准扩展与未知扩展原样保留。

### AUD-P1-23 · OpenAI `n>1` 与多 choices 在非直通路径静默降级

- 状态：`已修复`
- 修复记录（2026-07-11）：项目明确只支持 `n=1`：请求解析在触网前拒绝 `n!=1`；非流式响应和每个流式块都要求恰好一个 `index=0` choice，异常多 choice/index 显式报错。若异常出现在已消费响应中，Forwarder 不重试后备渠道并按保守路径结算，不再静默丢弃。请求、非流、流式及“已消费后不重试”回归均已通过，满足原验收的“完整保留或显式拒绝”。
- 原始证据：`openai/types.go:11-24` 没有 `n`；canonical Request/Response 只能表达一个候选；`openai/response.go:16-20` 固定取 `Choices[0]`，`response.go:91-101` 固定构造一个 choice；`stream_decode.go:69-83` 同样只处理首 choice。
- 原始触发：客户端发送 `n:2` 且存在模型别名/跨格式，或上游实际返回多个 choice。
- 原始影响：客户端请求多个候选却只收到一个，且无错误提示；流式不同 choice 的状态也会混失。
- 原始修复方向：完整方案是在 canonical 引入多 Choice 与按 choice index 隔离的流状态；短期在不能直通时拒绝 `n != 1`。
- 原始验收测试：别名和跨格式的流式/非流式 `n=2` 必须完整保留，或在调用上游前明确 4xx。

### AUD-P1-24 · SSE 解码不符合标准记录语义

- 状态：`已修复`
- 修复记录（2026-07-11）：共享 SSE 读取与 `adapter.SSEData` 已忽略首个 UTF-8 BOM，识别 LF、CRLF 与裸 CR 记录边界，忽略 comment/event/id/retry，并按规范用换行拼接多个 data 行；两个适配器统一使用该解析，同时兼容裸 JSON/[DONE]。回归覆盖 BOM、三种行结束、comment-only、event+id+data 和多 data 行。
- 原始证据：`internal/forwarder/upstream.go:125-155` 把完整记录交给适配器；`openai/stream_decode.go:37-53` 只从记录开头剥一次 `data:`；`anthropic/stream_decode.go:157-170` 遇到多个 data 行只取第一行。
- 原始触发：兼容上游发送注释心跳、`event:` / `id:` / `retry:` 字段，或把 payload 拆成多个 `data:` 行。
- 原始影响：合法流会在首输出前被错误故障转移，或在已输出后截断；该解析缺陷与 `AUD-P1-06` 的“错误生命周期被记成功”是不同根因。
- 原始修复方向：在 forwarder 统一解析 SSE record：忽略 comment/id/retry，按规范用换行拼接所有 data 字段，再把事件名和 payload 交给适配器。
- 原始验收测试：comment-only、event+data、两个 data 行组成 JSON、id/retry 混合记录均应正确处理。

### AUD-P1-25 · 工具参数强制转成 `map[string]any` 会拒绝响应或损坏大整数

- 状态：`已修复`
- 修复记录（2026-07-11）：canonical `ContentBlock` 同时保存 `ToolInputJSON` 原始字节与 `UseNumber` 生成的对象视图，编码时优先原始 JSON；非对象或暂时截断的 arguments 也可原样往返，大整数不再经 float64 改值。请求、响应、流式分片、模型别名及已消费转换错误不重试的回归均已通过。
- 原始证据：`canonical/message.go:84` 只用 map 保存工具参数；`openai/request.go:117-130` 与 `response.go:38-50` 强制把 arguments 字符串解成 map；`anthropic/types.go:45-48` 同样直接解为 map；默认 JSON number 进入 `float64`。
- 原始触发：模型返回暂时不完整/非对象 arguments，或参数含超过 IEEE-754 精确范围的订单号、雪花 ID，并进入模型别名或跨格式重编码。
- 原始影响：已付费的 200 响应可被误判为渠道故障并触发第二渠道调用；大整数会被静默改值。
- 原始修复方向：canonical 同时保存原始 arguments JSON / `json.RawMessage`；需要语义读取时启用 `UseNumber`；已消费的转换错误不能污染 breaker 或自动重试。
- 原始验收测试：截断参数、大整数、深层数值、别名往返，以及“200 + 无法解析 arguments”不得触发第二渠道。

## 15. 第二轮新增 P2

### AUD-P2-08 · 密码最小长度按 UTF-8 字节而非字符计算

- 状态：`已修复`
- 修复记录（2026-07-11）：最小长度改用 `utf8.RuneCountInString`，同时显式拒绝超过 bcrypt 72 字节的输入并映射为用户 4xx。7/8 个中文字符与 72/73 字节边界测试已通过。
- 原始证据：`internal/account/password.go:9-20` 声明最小长度 8，却使用 `len(string)`；三个常见中文字符通常已是 9 字节。
- 原始影响：实际字符数可远低于对用户声明的“至少 8 位”。
- 原始修复方向：若策略定义字符数，使用 `utf8.RuneCountInString`；同时显式限制 bcrypt 的 72 字节上限并映射为 4xx。
- 原始验收测试：中文 3/7/8 字符，以及总字节数 72/73 的边界。

### AUD-P2-09 · `CreateAccount` 可绕过 user 与计费实体的原子创建

- 状态：`代码已修`
- 修复记录（2026-07-11）：两种 Store 的 `CreateAccount` 收窄为仅允许无 external_id 的 admin；user 必须走 `CreateUserAccount`。PostgreSQL 路径在单事务内先创建 `users` 计费实体再创建 account，schema 增加 role/external_id 条件 CHECK；内存路径在同一锁内完成两者。非法直接 user 回归已通过；真实 PostgreSQL 事务回滚故障注入尚未单独运行。
- 原始证据：`internal/account/account.go:62-67` 把 user 创建定义为 `CreateUserAccount`，而 `postgres.go:71-86` 与 `memory.go:57-67` 的 `CreateAccount` 只校验角色合法，允许 `RoleUser + ExternalID=""`；`db/schema.sql:85-100` 也没有角色/关联约束。
- 原始影响：错误调用可生成能登录但没有额度容器的 user，后续 `/me`、密钥和额度操作异常，并破坏领域核心不变量。
- 原始修复方向：把接口收窄为 `CreateAdminAccount`，所有 user 强制走原子创建；数据库增加 role CHECK、user/external_id 条件约束，并评估外键。
- 原始验收测试：两种 Store 与真实数据库都必须拒绝直接创建非法 user。

### AUD-P2-10 · `auto_migrate` 不能升级已有表

- 状态：`代码已修`
- 修复记录（2026-07-11）：新增内嵌、版本化 `internal/db/migrations/*.sql` 与 `schema_migrations`；启动在 PostgreSQL advisory transaction lock 下校验版本/name/checksum，只执行未应用迁移并记录结果，关闭 auto_migrate 时改为验证数据库已处于当前版本。迁移排序、checksum 与历史升级内容单测已通过；尚未在真实 PostgreSQL 上执行“旧版 schema → 当前版”和并发多实例迁移验收。
- 原始证据：`internal/db/pool.go:59-65` 只执行全量 `CREATE TABLE IF NOT EXISTS`；`db/schema.sql:13-24` 对 users 仍只有 CREATE；提交 `65a6ca1` 已给既有 users 增加 `rate_multiplier`，而 Schema 注释声称存在的 `migrations/` 实际不存在。
- 原始触发：用字段加入前创建的数据库启动新版。
- 原始影响：旧表整体被跳过，新列/约束没有应用，但启动仍显示成功；当前列未被查询，所以漂移会潜伏到未来功能才爆炸。
- 原始修复方向：引入版本化迁移与 `schema_migrations`，并验证 schema 版本；建新库脚本不能冒充升级迁移。
- 原始验收测试：从上一版 schema 升级后与全新建库的最终 schema 完全一致。

### AUD-P2-11 · PostgreSQL 外键错误与内存 Store 的 NotFound 语义不同

- 状态：`代码已修`
- 修复记录（2026-07-11）：Admin PGStore 的 `mapWriteErr` 已把 SQLSTATE `23503` 归一为 `ErrNotFound`，handler 因而与内存 Store 一致返回 404；fake `pgconn.PgError` 回归已通过。尚未单独以真实 PostgreSQL 外键违规请求复验。
- 原始证据：`db/schema.sql:33-34` 有 API Key 用户外键；`internal/admin/postgres.go:26-38,112-123` 未把 SQLSTATE 23503 映射为 NotFound；`internal/store/memory.go:213-217` 则显式返回用户不存在。
- 原始影响：给不存在用户创建 Key 时，内存模式返回 404，生产 PostgreSQL 返回 500。
- 原始修复方向：按 23503 加具体 constraint name 映射，避免把所有外键错误笼统吞成同一领域错误。
- 原始验收测试：真实 PostgreSQL 和 fake PgError 均断言不存在用户得到 404。

### AUD-P2-12 · 未校验的 `int → int32` 缩窄可把限流变成不限流

- 状态：`已修复`
- 修复记录（2026-07-11）：API Key 的 `rate_limit_per_min` 已在领域/HTTP 边界限制为 0..5000，自助 Key 进一步限制为 1..5000；分页 limit/offset 在缩窄前有界校验。渠道输入也在两种 Store 共同的领域校验中限制 priority 为 -1,000,000..1,000,000、weight 为 1..1,000,000，再执行 PostgreSQL int32 转换；极值越界契约回归已通过。
- 原始证据：`internal/server/admin_handlers.go:117-143` 接收平台 `int`；`internal/admin/postgres.go:112-119` 直接转成 `int32`；`middleware/ratelimit.go:78-90` 把非正值视为不限流。渠道 priority/weight 也存在同类转换。
- 原始触发：提交 `rate_limit_per_min: 2147483648`。
- 原始影响：PostgreSQL 保存为负数并跳过限流，内存模式仍保留正数；priority/weight 也会回绕并改变路由。
- 原始修复方向：HTTP/领域边界限制到明确宽度范围，非法值返回 400；领域类型避免依赖平台 int。
- 原始验收测试：MaxInt32、MaxInt32+1、负值及两种 Store 的契约一致性。

### AUD-P2-13 · `max_idle_conns` 实际被映射为 pgx `MinConns`

- 状态：`已修复`
- 修复记录（2026-07-11）：误导性的 `max_idle_conns` 已改名为 `min_idle_conns`，配置结构、示例、启动校验和 `PoolConfig.MinConns` 全链路语义一致，并强制 `0 <= min_idle_conns <= max_open_conns`。
- 原始证据：`internal/config/config.go:43-50` 与 `config.example.yaml:12-13` 暴露 `max_idle_conns`；`cmd/linapi/main.go:186-190` 把它传给 `PoolConfig.MinConns`；`internal/db/pool.go:33-38` 最终设置 `pc.MinConns`。
- 原始影响：默认“最多 10 个空闲连接”变成“每实例持续维持至少 min(10, MaxConns) 个连接”，多副本可能意外耗尽 PG 连接；真正的最大空闲语义未实现。
- 原始修复方向：配置改为 `min_conns`，另加 `max_conn_idle_time`；兼容旧字段时显式迁移和告警。
- 原始验收测试：抽出配置映射函数并验证 pgxpool 的精确字段语义。

### AUD-P2-14 · Redis 热余额已存在时仍每请求查询冷源

- 状态：`代码已修`
- 修复记录（2026-07-10）：Quota 与 Redis Account 已删除；不存在“先查 PostgreSQL 再 seed Redis 余额”的双重路径。生成请求直接在 PostgresLedger Reserve 的单个事务中做权威余额条件扣减。
- 原始证据：`internal/middleware/quota.go:32-41` 每次无条件调用 `Store.Balance`；`internal/billing/account.go:44-46` 的 Lua 只有 key miss 才使用 seed。
- 原始影响：PG 模式每个请求在鉴权查询外再增加一个串行数据库往返，高并发吞吐和尾延迟仍受数据库限制，“惰性 seed”名不副实。
- 原始修复方向：鉴权查询顺带返回余额，或先由 Redis 报告 miss，仅 miss 时单飞读取冷源并原子初始化；最终随持久账本一起重构。
- 原始验收测试：预热 Redis 后连续 Reserve 不再调用 Balance；并发冷启动只允许有限次冷源读取。

### AUD-P2-15 · 分布式令牌桶使用各实例本地时钟

- 状态：`已修复`
- 修复记录（2026-07-11）：共享令牌桶 Lua 不再接收应用 `time.Now()`，每次原子执行内使用 Redis `TIME` 计算毫秒时钟；IP、登录标识、单 Key 与账户级限流都复用该脚本，实例时钟偏差不再参与补充令牌。
- 原始证据：`internal/middleware/ratelimit.go:43-56` 用传入 now 补充并覆盖 ts；`ratelimit.go:115-122` 的 now 来自应用 `time.Now()`。
- 原始影响：快慢时钟实例交替写共享桶时，快实例可反复把偏差当作经过时间并补充令牌，实际速率高于配置；回拨也会制造异常。
- 原始修复方向：Lua 内用 Redis `TIME` 作为唯一时钟。
- 原始验收测试：交替注入快/慢时间戳可复现旧算法重复补满，新脚本不受实例时钟影响。

### AUD-P2-16 · Recorder 关闭没有统一总预算

- 状态：`代码已修`
- 修复记录（2026-07-10）：Recorder/Sink 后台队列已删除，最终 usage 与余额在 Finalize 事务同步提交；关闭阶段不再存在逐批 5 秒等待或进程内残留账单。
- 原始证据：`internal/billing/recorder.go:129-160` 关闭时逐批 flush，每批重新获得 5 秒；`recorder.go:163-168` 无界 `wg.Wait`。主流程 30 秒只包 HTTP Shutdown，Recorder 在后续 defer 中运行。
- 原始影响：默认 4096 队列、128 batch 在故障 Sink 下理论上可等待约 32×5 秒；容器 termination grace 到期后仍被 SIGKILL，剩余账单和后续清理丢失。
- 原始修复方向：改为 `Close(ctx) error`，所有批次共享总截止时间；未写入记录交给持久 outbox。
- 原始验收测试：阻塞 Sink + 满队列时，Close 在预算内返回明确错误且记录可恢复。

### AUD-P2-17 · panic 绕过访问日志和指标，debug Recovery 还可能打印凭证

- 状态：`已修复`
- 修复记录（2026-07-11）：自定义 Recovery 放在访问日志和指标中间件内侧，panic 转为协议对应 500 后外层观测仍正常收尾；恢复日志只记录 request_id/method/path，不记录请求头或 panic 值。回归验证 panic 500、访问日志和指标存在，Cookie、x-api-key 与 panic secret 均未进入日志。
- 原始证据：`internal/server/server.go:42-45,71-74` 把 `gin.Recovery()` 放在 Logger/Metrics 外层；`logger.go:100-145` 与 `metrics.go:17-30` 都只在 `c.Next()` 正常返回后收尾。已核对 Gin 1.10 debug Recovery 只脱敏 Authorization，不脱敏 `x-api-key` 或 Cookie。
- 原始影响：panic 500 从结构化日志和 Prometheus 消失；debug 环境的恢复日志可能包含 API Key 或会话 Cookie。
- 原始修复方向：使用项目自定义 Recovery，或让观测收尾使用 defer 并统一脱敏所有凭证头。
- 原始验收测试：handler 主动 panic，指标和 request_id 日志必须存在，捕获日志不得含任何凭证。

### AUD-P2-18 · 内存 Store 不保证明文 API Key 唯一

- 状态：`已修复`
- 修复记录（2026-07-11）：配置种子同时检查明文 Key 与 KeyID 唯一，重复项在启动构造时 panic；管理创建在同一锁内同时检查两个索引，冲突返回领域错误，不会覆盖旧归属。重复 seed、运行时重复明文及原绑定保持回归已通过。
- 原始证据：`internal/store/memory.go:60-70,212-230` 只检查/覆盖 keyID 索引，随后直接写 `s.keys[apiKey]`；PostgreSQL 在 `db/schema.sql:29-32` 同时约束 key_hash 与 key_id 唯一。
- 原始触发：配置种子或存储调用使用相同明文 Key、不同 KeyID。
- 原始影响：同一凭证被静默重绑定到后一个用户，旧 KeyID 形成无法正确禁用/删除的幽灵记录。
- 原始修复方向：创建前同时检查两个索引；重复种子应在启动时拒绝，而不是后写覆盖。
- 原始验收测试：相同明文、不同 KeyID 必须冲突，冲突后原归属与启停语义不变。

### AUD-P2-19 · 内存 Store 的时间字段丢失或永远不更新

- 状态：`已修复`
- 修复记录（2026-07-11）：内存用户用 `memUser` 持久保存 created_at/updated_at，启停与充值会刷新 updated_at；API Key 的 Identity 也持久保存 created_at，创建、列表与启停返回同一非零时间。用户写路径与 Key 创建/列表/启停时间一致性回归已通过。
- 原始证据：`internal/store/memory.go:212-267` 只在创建 Key 的当次返回临时填时间，Identity 不保存；`internal/admin/memory.go:199-217` 直接暴露零值，并总令用户 UpdatedAt=CreatedAt。
- 原始影响：列表显示 `0001-01-01T00:00:00Z`，用户充值/启停后 updated_at 不变，与 PG 和控制台预期不同。
- 原始修复方向：内存 user/key 记录持久保存 createdAt/updatedAt，所有写操作更新时间。
- 原始验收测试：创建后重新列表时间非零；启停/充值后 updated_at 单调增加；两种 Store 跑同一契约测试。

### AUD-P2-20 · 内存余额算术溢出会回绕，PostgreSQL 则拒绝

- 状态：`已修复`
- 修复记录（2026-07-11）：内存余额变动统一使用 checked add，正负方向在执行前比较 `math.MaxInt64/MinInt64`，溢出返回 `ErrBalanceOverflow` 且保留原余额。两端极值回归已通过。
- 原始证据：`internal/store/memory.go:201-209` 直接执行 `balance += delta`，没有溢出检查；PostgreSQL BIGINT 溢出会让 UPDATE 报错并保持原值。
- 原始影响：极值加减在内存模式可从最小负数回绕为巨额正余额，开发/生产语义不一致；管理 handler 接受完整 int64 范围。
- 原始修复方向：在领域边界做 checked add 和合理金额范围校验，两种 Store 返回同一领域错误。
- 原始验收测试：MaxInt64/MinInt64 加减 1 不得回绕，内存与 PG 结果一致。

## 16. 第二轮验证与复验要求

在第二轮锁定快照上，以下命令通过：

```bash
CGO_ENABLED=1 go test -race -count=1 ./internal/account/... ./internal/admin/... ./internal/billing/... ./internal/forwarder/... ./internal/routing/... ./internal/store/... ./internal/session/... ./internal/middleware/... ./internal/config/... ./internal/db/...
go vet ./internal/account/... ./internal/admin/... ./internal/billing/... ./internal/forwarder/... ./internal/routing/... ./internal/store/... ./internal/session/... ./internal/middleware/... ./internal/config/... ./internal/db/...
```

适配器增量专项也通过现有测试：

```bash
go test -count=1 ./internal/adapter/... ./internal/forwarder/...
```

这些绿色结果不覆盖：

- Redis 执行成功但响应丢失后的透明命令重放。
- 请求已被上游处理、但响应状态未知的跨渠道重试。
- 真实 PostgreSQL 的版本迁移、事务快照和 SQLSTATE 语义。
- 多实例时钟偏差、慢 body/空闲连接、阻塞 Sink 关闭预算。
- usage 缺失、模型别名保真、多 choice、标准 SSE record、大整数工具参数。
- 禁用/改密后的多设备会话撤销。

Claude 完成当前控制台任务后，开始任何修复前必须先：

1. 重新读取最新提交与工作树，校准第 13～15 节全部行号。
2. 跑完整 `go test -count=1 ./...`、`go vet ./...` 和全量 `-race`。
3. 对 Claude 新增的登录、Cookie、bootstrap、`/me` 所有权和 Admin 路由再做一次增量安全复核。
4. 优先设计并修复全部 P0；P0-01、03～06 必须放入同一套持久 reservation/ledger/idempotency 设计，不能各打补丁。

## 17. 第三轮安全专项基线

第三轮按 Go 后端安全审查清单，由主智能体与三个并行子智能体分别复核认证/授权、网络/SSRF、供应链/秘密/资源耗尽，并与前 51 项逐条去重。

- 锁定快照：`0736eb132829ce032da03267990ba33e88ec47c4`。第三轮主体取证完成于 `a8dd9b3`；`f171c72` 只增加测试断言，`0736eb1` 随后正式提交会话路由，相关 server 行号已在本节校准。
- 原始快照可达性：`internal/server/server.go:105-180` 当时已注册 `/auth`、`/me`、`/admin`，但 `cmd/linapi/main.go` 尚未向 `server.Deps` 注入 Account/Settings/Session，三个注册函数会因 nil guard 返回，标准二进制请求仍为 404；入口文件也仍被 `.gitignore` 排除（见 `AUD-P1-09`）。
- 当前装配状态（2026-07-11）：`main` 已完成 Account/Settings/Session 依赖注入，`/auth`、`/me`、`/admin` 已端到端接线；Gin 已设置 `SetTrustedProxies(nil)`。因此以下问题段中的“main 尚未装配”均只描述锁定快照，不再描述当前可达性。
- 当前修复状态：AUD-P0-07、P1-26～29、P1-31～34、P2-21～24 已修复；P1-30 已落地逐事件写 deadline 但仍待真实慢读 TCP 验收。各项精确状态和验证边界以第 10 节为准。
- 当前已经可达的重点攻击面：控制台会话路由、`/v1` 匿名鉴权查询、SSE 下行、公开指标、上游出站、PostgreSQL 渠道凭证与 Redis 连接。
- 第三轮原始执行边界：当时只修改审查文档、索引与进度记录，没有修改业务代码、配置、Schema 或测试；后续修复已在当前工作区落地并逐项回填。

第三轮新增 14 项：P0 1 项、P1 9 项、P2 4 项。以下原始证据、触发、影响和修复方向均以锁定快照为准；当前状态以各条修复记录和第 10 节跟踪表为准。

## 18. 第三轮新增 P0

### AUD-P0-07 · 开放注册可无限复制赠送额度

- 状态：`已修复`
- 修复记录（2026-07-10）：自助注册恒以 0 初始余额创建计费实体，并忽略可变赠送额；设置写接口同时拒绝正 `new_user_initial_balance`，赠送额度只能由管理面主动发放。
- 原始可达性（第三轮快照）：当时 main 尚未注入会话依赖；若完成装配，且 `admin.enabled=true`、`registration_enabled=true`、`new_user_initial_balance>0`，原利用链会激活。当前 main 已完成装配，但正初始余额已被拒绝，赠送复制路径已闭合。
- 原始证据：`internal/server/auth_handlers.go:59-77` 把全局初始额度直接交给 `CreateUserAccount`；`account_handlers.go:150-160` 可同时打开注册并设置额度；身份唯一成本只有可任意生成的用户名；`me_handlers.go:93-128` 允许注册用户随后创建调用 Key。
- 原始利用链：批量注册不同用户名 → 每个账户各获一份余额 → 登录并创建 Key → 消耗真实上游资源。
- 原始影响：赠送额度可被无限复制，形成系统性免费调用和真实上游账单损失，符合 P0 上线阻断定义。
- 原始修复方向：赠送权益必须绑定已验证且全局唯一的外部身份/邀请码；同一身份只能领取一次；增加注册速率、设备/IP 风控、活动总预算和紧急熔断。修复前保持注册关闭或初始额度为 0。
- 原始验收测试：同一验证身份、并发注册、大小写/Unicode 变体及多设备场景都只能领取一次；达到活动总预算后新账户不得再获赠额。

## 19. 第三轮新增 P1

### AUD-P1-26 · Cookie 管理面缺少 CSRF 边界，可被同站子域创建新管理员

- 状态：`已修复`
- 修复记录（2026-07-10）：会话绑定双重提交 CSRF token；Cookie 鉴权写请求强制 JSON，并校验 Origin/Referer；`/me` 与 `/admin` 写端点已接线。
- 原始可达性（第三轮快照）：当时 main 尚未装配控制台依赖；装配后若存在不可信同站子域且管理员已登录，原利用链会激活。当前 main 已完成装配，CSRF 边界已接线。
- 原始证据：`internal/server/auth_handlers.go:46-50` 只设置 `HttpOnly + SameSite=Strict`；`account_handlers.go:55-88` 允许创建 `role=admin`；所有写 handler 使用 `ShouldBindJSON`，没有 CSRF token、自定义头、精确 `Origin` 或 Fetch Metadata 校验，也没有强制 JSON Content-Type。
- 原始利用链：攻击者控制 `evil.example.com`，管理员登录 `api.example.com`；SameSite Strict 对同站异源仍携带 Cookie。攻击页可用无需预检的 `text/plain` 表单构造合法 JSON，调用 `POST /admin/accounts` 创建攻击者已知密码的新管理员。
- 原始影响：完整管理面接管；也可改密、充值或配置恶意渠道。
- 原始修复方向：所有 Cookie 鉴权写操作使用可靠 CSRF token；同时强制 `application/json`、校验精确同源 `Origin`/`Sec-Fetch-Site`，Cookie 使用 `__Host-` 前缀。不能只依赖 SameSite。
- 原始误报/缓解：若生产不存在任何不可信同站子域，且边缘已做精确同源校验，利用条件降低；仓库内没有这种部署证明。
- 原始验收测试：同站异源、`text/plain`、无 token 的管理员 POST 必须 403；合法同源带 token 请求成功。

### AUD-P1-27 · 登录和注册没有滥用限速，匿名请求可耗尽 bcrypt CPU

- 状态：`已修复`
- 修复记录（2026-07-11）：认证入口在 bcrypt 前叠加来源 IP 桶与登录标识桶；后者按规范化 username 的 SHA-256 摘要、按 login/register 独立 scope 存储，不查询账户，因此存在与不存在的用户名走相同预算路径。全局 bcrypt 信号量使用非阻塞 `TryAcquire`，满载立即 503；Gin `SetTrustedProxies(nil)` 防止客户端伪造来源 IP。会话创建用 Redis Lua 原子清理过期 ZSET 条目、检查每账户上限并写入 token 摘要索引，默认最多 10 个活跃会话，删除会释放名额。IP/标识桶、标识预算在账户查询与 bcrypt 前生效、TryAcquire、会话上限和代理信任回归均已通过。
- 原始可达性（第三轮快照）：当时 main 尚未装配 `/auth` 依赖；当前 main 已完成装配，登录路径可达，注册路径仍受注册开关控制。
- 原始证据：`internal/server/auth_handlers.go:53-86,90-120` 每次注册/登录执行 bcrypt；`server.go:90-98` 唯一限流器只服务已通过 API Key 鉴权的 `/v1`；`session/session.go:50-65` 建会话没有账户级数量上限。
- 原始影响：匿名在线撞库、弱密码爆破和 CPU 耗尽；持有任意有效账户者还能反复 remember 登录，向 Redis 写入无限个 7 天会话。
- 原始修复方向：在 bcrypt 前增加来源 IP、账户与全局三层速率/并发限制；设置 bcrypt 并发信号量；每账户限制活跃会话并淘汰旧会话。不能只按用户名硬锁，否则攻击者可锁死受害账户。
- 原始误报/缓解：若边缘 WAF 已强制上述限速则风险降低，但仓库没有部署配置证明。
- 原始验收测试：存在/不存在用户名受相同预算；并发错误登录不会让 bcrypt goroutine 无界增长；重复成功登录不能无限增加 Redis session。

### AUD-P1-28 · 普通用户可创建无限量且不限速的 API Key

- 状态：`已修复`
- 修复记录（2026-07-11）：自助创建强制 `rate_limit_per_min ∈ [1,5000]`；每账户 50 把上限已下沉为 Store 原子操作，PostgreSQL 用账户 advisory transaction lock 串行化 count+insert，内存实现用同一互斥锁完成。`/v1` 在单 Key 桶外再叠加所有 Key 共享的账户总桶，多 Key 不再线性叠加平台预算。列表虽仍为全量，但被 50 把硬上限约束；原子上限、账户共享桶和边界值回归均已通过。
- 原始可达性（第三轮快照）：当时 main 尚未装配 `/me` 依赖；后续 main 完成装配后，该攻击面一度可达。当前并发 Key 上限、账户总预算与有界列表问题已按上方修复记录闭合。
- 原始证据：`internal/server/me_handlers.go:87-116` 原样接受并持久化用户提交的 `rate_limit_per_min`，没有上下限和 Key 数量限制；`middleware/ratelimit.go:78-90` 把 `<=0` 解释为不限流；`ratelimit.go:115-122` 每个 KeyID 使用独立桶；`me_handlers.go:73-84,33-45` 列表和所有权检查还会加载该用户全部 Key。
- 原始利用链：创建 `rate_limit_per_min=0` 的 Key即可关闭限流；即使改为正数，批量创建 Key 也能线性叠加每 Key 配额，并使数据库及 O(n) 列表/归属检查持续膨胀。
- 原始影响：平台限流失效、上游并发滥用、PG/内存存储耗尽。该问题不依赖 `AUD-P2-12` 的整数回绕，普通 `0/-1` 即可利用。
- 原始修复方向：服务端决定默认值和最大值；设置每账户 Key 硬上限、账户级总令牌桶和并发上限；列表分页；用户模型集合必须与账户策略求交集。
- 原始验收测试：普通用户不能提交 0/负数/超上限值；超过 Key 上限返回 409/429；轮换多个 Key 仍受同一账户总预算。

### AUD-P1-29 · 登出删除会话失败仍清 Cookie 并返回成功

- 状态：`已修复`
- 修复记录（2026-07-10）：登出使用独立 3 秒超时 context 删除会话；删除失败返回 503 且不清 Cookie，不再谎报成功。
- 原始可达性（第三轮快照）：当时 main 尚未装配 `/auth/logout`；当前 main 已完成装配，可靠登出修复已接线。
- 原始证据：`internal/server/auth_handlers.go:123-130` 忽略 `sessions.Delete` 错误，随后清 Cookie 并返回 200；`internal/session/session.go:84-86` 的 Redis `DEL` 明确可能失败。
- 原始触发：攻击者已复制受害者 token，受害者登出时 Redis 短暂故障或请求 context 被取消。
- 原始影响：用户看到“退出成功”，本地 Cookie 也消失，但被盗 token 在 Redis 恢复后最长继续有效 7 天。
- 原始修复方向：用独立短超时 context 执行撤销并检查结果；删除失败不得宣称安全登出；结合账户会话版本/账户到 token 索引提供“撤销全部会话”。
- 原始验收测试：注入 `DEL` 失败时不得返回成功；恢复后旧 token 必须失效。该项与 `AUD-P1-17` 的禁用/改密不撤销不同。

### AUD-P1-30 · SSE 慢读客户端可永久占用转发资源

- 状态：`代码已修`
- 修复记录（2026-07-11）：每次 SSE 事件写入前通过 `http.ResponseController.SetWriteDeadline` 刷新独立 30 秒下行写期限；合法长流不会受固定总时长限制，停止读取的客户端则不能无限阻塞一次 Write/Flush。尚未执行真实 TCP 客户端停止读取的端到端验收。
- 原始证据：`internal/server/server.go:60-66` 全局 `WriteTimeout` 为 0；`internal/forwarder/stream.go:118-156` 直接 `Write/Flush`，没有逐次下行写期限。
- 原始触发：攻击者持有效 API Key 发起流式请求，读完响应头后停止读取但不关闭 TCP。
- 原始影响：客户端接收窗口耗尽后写调用可无限阻塞，同时占用 handler goroutine、上下游连接和 breaker permit；连接累积可拖垮网关。
- 原始去重说明：`AUD-P1-07` 是上游响应头/事件空闲超时，`AUD-P1-19` 是慢入站 body/keep-alive，本项是下游慢读。
- 原始修复方向：用 `http.ResponseController.SetWriteDeadline` 实现逐事件写入空闲期限，每次正常写前刷新；增加账户/API Key 流并发上限。不要设置会中断合法长回复的固定总 WriteTimeout。
- 原始误报/缓解：若反向代理会完整吸收应用 SSE，并独立限制边缘慢客户端，则应用阻塞风险降低，需用真实部署验证。
- 原始验收测试：真实 TCP 客户端停止读取后，handler 和上游连接须在期限内退出；持续读取的长流不能被总时长误杀。

### AUD-P1-31 · 渠道 URL 缺少 SSRF、路径边界和明文传输策略

- 状态：`已修复`
- 修复记录（2026-07-11）：新增 `UpstreamTargetPolicy`：结构化解析并拼接 endpoint，拒绝 userinfo/query/fragment、转义/非规范路径与 IPv6 zone；release 默认只允许 HTTPS，私网或显式 HTTP 必须按精确 authority 配置 CIDR/开关。自定义 `DialContext` 在每次 DNS 解析后逐 IP 拒绝 loopback、private、link-local、CGNAT、文档/保留网段及 mapped 地址，防 DNS rebinding。启动加载、管理 CRUD/启用与定时重载都复用校验；URL confusion、私网例外、rebind 和地址分类回归已通过。
- 原始可达性：配置/数据库入口当前只应由可信运维控制；server 已注册渠道 CRUD，main 完成会话装配后应用管理员可直接触发。
- 原始证据：`internal/server/admin_channels.go:48-99` 只验证 format，不验证 `base_url`；`internal/config/config.go:123-148` 加载后无 URL 策略；`internal/forwarder/upstream.go:58-97` 直接字符串拼接 URL 并发送。
- 原始利用方式：允许 loopback、RFC1918、IPv6 loopback、link-local、云 metadata、内部 DNS 和 `http://`；`http://internal/privileged?ignored=` 会让追加的 `/v1/...` 落入 query，实际 POST 仍命中 `/privileged`；没有拨号期 IP 校验，DNS rebinding 也未阻止。
- 原始影响：应用管理员权限可跨越到宿主机/内网控制面；明文 HTTP 还会泄露上游密钥和用户提示词。
- 原始修复方向：规范化解析 URL，拒绝 userinfo/query/fragment，默认仅 HTTPS；私网/本地目标必须显式白名单；自定义 `DialContext` 在每次解析/拨号时阻止特殊地址并固定已校验 IP；使用 `url.URL` 构造路径。重定向继续按 `AUD-P1-16` 修复。
- 原始误报/缓解：若应用管理员被明确等同宿主机 root，且 egress ACL 已隔离 metadata/管理网段，可接受部分风险；仓库无此证明。
- 原始验收测试：覆盖 IPv4/IPv6 私网、IPv4-mapped IPv6、link-local、内部 DNS、DNS 重绑定、query/fragment 绕路径、HTTP 明文和合法 HTTPS 白名单。

### AUD-P1-32 · 远程 Redis 无 TLS，可泄露管理员会话与余额状态

- 状态：`代码已修`
- 修复记录（2026-07-11）：Redis 配置新增 ACL username、TLS 1.2+、ServerName、CA 与可选 mTLS client cert/key；release 对非 loopback 明文地址默认拒绝启动，仅显式 `allow_insecure_remote` 可承担隧道例外。会话 Redis key 与账户索引只保存 token 的 SHA-256 摘要，不再把可重放 token 放在 key 名。安全策略与 TLSConfig 单测已通过；真实 TLS-only Redis、错误 CA/ServerName 和 mTLS 集成尚未运行，故不能标为生产验收完成。
- 原始可达性：Redis 仅走 loopback 或已有可靠 mTLS 隧道时不触发；远程直连时激活。
- 原始证据：`internal/config/config.go:55-58` 只有 addr/password/db；`internal/redisx/redisx.go:20-24` 未设置 `TLSConfig`/ACL username；`internal/session/session.go:27,33-37,58-70` 把可重放 token 放在 `session:<token>` 键名且 payload 含角色；余额调整也经同一明文连接。
- 原始影响：能监听链路者可捕获 Redis 密码、管理员 bearer session、角色与余额命令；主动篡改或取得 Redis 密码后可伪造 admin 会话、修改额度或破坏限流。
- 原始修复方向：增加 CA、ServerName、客户端证书和 ACL username 配置并设置 `redis.Options.TLSConfig`；release 模式下非回环 Redis 默认要求 TLS；Redis 键只保存 session token 摘要，避免服务端数据直接成为可重放凭证。
- 原始缓解：专用 Redis、最小权限 ACL、仅私网访问、mTLS sidecar/service mesh。
- 原始验收测试：TLS-only Redis 集成通过；错误 CA/ServerName 启动失败；release 下远程明文 Redis 被拒绝。

### AUD-P1-33 · 上游供应商 API Key 明文落 PostgreSQL

- 状态：`已修复`
- 修复记录（2026-07-11）：`channels.api_key` 只保存 `linapi:channel-key:v1` AES-256-GCM envelope，含 key id 与随机 nonce，并以 `channel_id` 作为 AAD。数据库模式要求外部注入 32 字节 base64 主密钥；未知 key id、密文损坏、AAD 不匹配或缺失密钥均 fail-closed。PG 创建/更新在 AdminStore 边界加密、读取时解密，管理 JSON 永不序列化 API Key。
- 维护迁移：启动事务 `FOR UPDATE` 锁定并验证全部渠道；默认发现历史明文即拒绝启动，只有维护窗口显式开启 `database.channel_key_encryption.migrate_plaintext` 才原子迁移，成功后必须立即关闭。schema 约束阻止新增明文；生产迁移后仍须轮换所有曾明文保存的上游密钥。加密/AAD/篡改、SQL、配置、迁移规划与管理 handler 不回显测试已通过。
- 原始证据：`db/schema.sql:55` 与 `internal/db/schema.sql:44` 定义 `channels.api_key TEXT NOT NULL`；`internal/admin/postgres.go:184-203,233-252` 创建/更新时直接写入明文，读取后也直接装入路由渠道。
- 原始触发：攻击者取得只读 SQL、只读副本、备份、快照或数据库导出权限。
- 原始影响：本来只读的数据泄漏升级为可直接盗刷供应商账户的凭证泄漏，攻击者可绕过网关消耗真实上游额度。
- 原始修复方向：优先只存 Secret Manager/KMS 引用；否则使用 KMS envelope encryption，主密钥不得与密文共库；迁移后轮换全部既存上游密钥。
- 原始缓解：列级最小权限、备份加密、读取审计、短轮换周期。普通磁盘加密不能保护已获 SQL 读取权限的攻击者。
- 原始验收测试：数据库只保存密文或 secret reference；管理 API/日志不回显；KMS 不可用时 fail-closed。

### AUD-P1-34 · 无效 API Key 流量绕过限流，可匿名耗尽 PostgreSQL 与日志

- 状态：`已修复`
- 修复记录（2026-07-11）：`/v1` 在 Store 鉴权前先执行来源 IP Redis 预算，再用容量与 PG 最大连接数对齐的非阻塞 `AuthWithGate` 限制查库并发，满载立即 503；随机 Key 不再能无限占用 PG handler。Server 同时应用较小 `MaxHeaderBytes`，RequestLogger 始终生成服务端随机 ID、不信任入站 `X-Request-Id`，path 等外部日志字段有固定截断。前置 429、鉴权 gate 与入站 request ID 回归已通过。
- 原始证据：`internal/server/server.go:93-98` 的顺序为 `Auth → per-key RateLimiter`；`middleware/auth.go:19-38` 对每个非空随机 Key 都调用 Store；`store/postgres.go:35-41` 每次执行一次 PG 查询，401 后不会进入限流器；`middleware/logger.go:90-115` 还原样回显并记录不限长度的 `X-Request-Id` 与 URL path；HTTP Server 未收紧 `MaxHeaderBytes`。
- 原始利用链：匿名攻击者持续发送随机 Key，每次占用一次数据库查询；同时用接近默认 header 上限的 request ID 放大响应和结构化日志。
- 原始影响：耗尽 PG 连接池、拖慢合法鉴权；日志管道/磁盘可被快速填满。内存 Store 只能缓解 PG 部分，不能消除日志放大。
- 原始修复方向：存储鉴权前增加全局/来源 IP 并发与速率限制，并先完成可信代理配置；内部 request ID 始终由服务端生成，外部 trace ID 单独存储且限制为 64～128 个安全字符；设置更小的 `MaxHeaderBytes` 并统一截断日志字段。
- 原始误报/缓解：若边缘已严格限制匿名速率、并发和 header 大小则风险降低；仓库没有部署证明。
- 原始验收测试：随机 Key 洪泛下 `ResolveKey` 调用数受前置预算约束；超长 header 在进入日志前被拒绝；所有外部日志字段有固定上限。

## 20. 第三轮新增 P2

### AUD-P2-21 · 开放注册接口泄露用户名是否存在

- 状态：`已修复`
- 修复记录（2026-07-11）：注册对“新建成功”和“用户名已存在”统一返回 201 与 `{"ok":true}`；两条路径都先执行相同密码校验/hash 与认证预算，登录原有 dummy bcrypt 仍保留。重复注册响应一致性回归已通过。
- 原始可达性（第三轮快照）：当时 main 尚未装配 `/auth/register`；当前 main 已完成装配，开启注册后该枚举路径可达。
- 原始证据：`internal/server/auth_handlers.go:77-86` 对重复用户名返回明确 409“用户名已存在”，成功则 201。
- 原始影响：可枚举普通用户和管理员登录名，降低后续撞库成本；登录路径自身虽已使用统一错误和 dummy bcrypt，仍会被注册旁路抵消。
- 原始修复方向：首先落实 `AUD-P1-27` 的注册限速；高风险部署使用统一接受响应并异步处理冲突，或把登录名设计为非秘密公开标识后明确接受风险。
- 原始验收测试：在要求隐藏用户名的部署模式中，存在/不存在用户名的状态码、响应结构和可观测时序一致。

### AUD-P2-22 · `/metrics` 默认公开在业务监听器，且没有抓取并发预算

- 状态：`已修复`
- 修复记录（2026-07-11）：`/metrics` 增加常量时间 token 鉴权；release 缺 token 直接拒绝启动。Prometheus handler 配置 `MaxRequestsInFlight` 与处理 timeout，release 同样强制正值；鉴权、并发超限 503 与超时回归已通过。端点仍与业务共用 listener，但默认不再匿名暴露且已有抓取预算。
- 原始可达性：应用端口可被公网直连且边缘未按路径隔离时激活。
- 原始证据：`internal/server/server.go:60-61,77,87-88` 绑定全部接口并无鉴权挂 `/metrics`，且访问日志显式跳过；`internal/metrics/metrics.go:27-45` 暴露 channel_id、format、成功率、延迟与 breaker 状态；默认 `promhttp.Handler()` 没有并发/超时预算。
- 原始影响：匿名侦察渠道拓扑、流量和故障状态；并发抓取还会持续消耗采集、编码、压缩的 CPU/内存，而且业务日志看不到请求。
- 原始修复方向：迁到独立 loopback/内网 listener，用网络策略、mTLS 或专用鉴权保护；设置 `MaxRequestsInFlight` 与超时。
- 原始误报/缓解：若业务端口不可公网直连且边缘已严格隔离 `/metrics`，信息暴露不成立；仓库没有部署清单证明。
- 原始验收测试：业务 listener 的 `/metrics` 返回 404/403；仅内部 listener 可采集；超过并发预算返回 503。

### AUD-P2-23 · Gin 默认信任所有代理头，审计来源 IP 可伪造

- 状态：`已修复`
- 修复记录（2026-07-11）：Gin engine 显式调用 `SetTrustedProxies(nil)`，直连模式只使用 socket 对端地址；登录/注册来源 IP 限流与审计日志不再接受客户端伪造的 `X-Forwarded-For` / `X-Real-IP`。
- 原始证据：`internal/server/server.go:50-51` 创建 engine 后没有 `SetTrustedProxies`；`internal/middleware/logger.go:115` 使用 `c.ClientIP()`；Gin 1.10 未配置时默认信任全部代理网段。
- 原始影响：直连客户端可用 `X-Forwarded-For`/`X-Real-IP` 伪造审计来源，规避基于日志的封禁、告警和事件追踪。当前 IP 未用于鉴权/限流，因此尚不是直接权限绕过。
- 原始修复方向：直连模式 `SetTrustedProxies(nil)`；代理模式只接受配置中的精确 CIDR，配置非法时拒绝启动；边缘删除外部 forwarded headers 后重写。
- 原始误报/缓解：若所有直连路径都被网络层阻断，且唯一可信代理强制覆盖这些头，风险降低。
- 原始验收测试：不可信来源的伪造 XFF 必须记录 socket 对端；可信代理链只接受正确位置的客户端 IP。

### AUD-P2-24 · go-redis 命中可达的建连响应错位安全公告

- 状态：`已修复`
- 修复记录（2026-07-11）：`github.com/redis/go-redis/v9` 已从 v9.7.0 升级到官方修复版本 v9.7.3；当前目标包测试全部通过。未另做 `CLIENT SETINFO` 超时故障注入，但受影响版本已从依赖图移除。
- 原始证据：`go.mod` 使用 `github.com/redis/go-redis/v9 v9.7.0`；官方 `govulncheck` 命中可达的 [GO-2025-3540](https://pkg.go.dev/vuln/GO-2025-3540)，调用链从 `internal/redisx/redisx.go:29` 的 PING 进入 `baseClient.initConn`。公告说明 `CLIENT SETINFO` 建连超时时可能造成后续响应错位，修复版本为 v9.7.3。
- 原始影响：异常网络时 Redis 命令与响应可能错配，造成启动/请求错误；上游评级为 Low，但该项目把 Redis 用于会话、限流和余额，不应继续运行在已知受影响版本。
- 原始修复方向：升级到 go-redis v9.7.3 或更新且兼容版本，跑全量 Redis/Lua/竞态测试；临时禁用 identity 命令只能作为短期缓解。
- 原始去重说明：`AUD-P0-05` 是客户端自动重放非幂等 Lua，本项是建连初始化导致的响应序列错位，根因与修复不同。
- 原始验收测试：在 `CLIENT SETINFO` 超时/断连故障注入下，下一条业务命令不得消费前一条响应。

## 21. 第三轮验证、依赖判断与阴性结果

第三轮取证先在 `a8dd9b3` 完成；server 会话路由提交 `0736eb132829ce032da03267990ba33e88ec47c4` 后又重新校准并执行以下验证：

```bash
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

`govulncheck` 的非零退出码来自三条符号级可达公告，而非扫描器执行失败；逐条复核后只有一条新增独立问题 ID：

- GO-2025-3540 已作为 `AUD-P2-24` 登记。
- `github.com/jackc/pgx/v5 v5.7.2` 还命中 [GO-2026-5004](https://pkg.go.dev/vuln/GO-2026-5004)，但公告要求同时使用非默认 simple protocol、SQL 含 dollar-quoted literal、且对应 placeholder 值受攻击者控制。当前项目使用默认 extended protocol与固定参数化 SQL，未确认可利用，因此不增加问题 ID；仍建议升级到 v5.9.2+ 并禁止生产 DSN 切到 simple protocol。
- 路由提交后 `golang.org/x/net v0.26.0` 还命中 [GO-2025-3595](https://pkg.go.dev/vuln/GO-2025-3595)。静态链路从 `ShouldBindJSON` 进入 validator 的内置 HTML 校验注册表，但项目没有使用 `html` 校验 tag、`x/net/html` 解析、HTML 模板或网页输出；公告所需的 SVG/MathML DOM/tokenizer 处理路径未确认可达，因此不增加问题 ID。仍建议把 x/net 升到 v0.38.0+。
- 扫描器另报 13 条“已导入但未调用”和 19 条“模块存在但未调用”的公告；没有把不可达版本告警冒充项目漏洞。

秘密与危险能力扫描的阴性结果：

- 当前树及全部 Git 历史的高置信 secret 模式只命中 `config.example.yaml` 占位值，未发现真实提交凭证。
- 未发现应用代码使用 `unsafe`、cgo、OS 命令执行、动态模板或文件路径执行；生产依赖图也没有 CgoFiles。
- 会话与 API Key 使用 `crypto/rand`；`math/rand` 只用于非安全敏感的路由加权。
- 未发现新的 SQL 字符串拼接、客户端 Host 驱动重定向、凭证头泛化转发、CORS 放开或自定义 HTTP smuggling 路径。
- `/me` 已强制从 Session 取得 external_id，启停/删除前校验 Key 所有权；本轮未确认新的直接 IDOR。禁用/改密不撤销旧会话仍由 `AUD-P1-17` 覆盖。

main 已完成账户/设置/会话装配；后续修复与发布前还必须继续验证：

1. `/auth`、`/me`、`/admin` 的真实 middleware 顺序和所有路由是否与本节条件性判断一致。
2. bootstrap 是否原子且只在空账户库执行，Cookie `Secure` 是否由真实 HTTPS 部署条件决定。
3. CSRF PoC、匿名 bcrypt/PG 洪泛、免费额度批量注册和无限 Key 创建能否在集成路由上复现。
4. Claude 新增代码是否引入 CORS、静态文件、模板、上传、额外出站 URL 或新的凭证日志面。
