# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## 项目简介

LinAPI 是一个从零构建的 AI API 网关（中转站场景）：接收 OpenAI / Anthropic（Claude）等格式的客户端请求，路由到上游供应商渠道，做格式转换、负载均衡、鉴权限流与计费。Go + Gin + PostgreSQL + Redis + sqlc，面向商用高并发。

代码注释与文档以中文为主，新增代码请沿用中文注释风格。

## 维护约定（重要）

**每完成一个任务（新模块 / 功能 / 修复），必须同步更新文档，再结束该任务：**

1. **`docs/progress.md`** —— 更新顶部日期、七步进度表状态；把新完成的细节、待接线事项记进去。
2. **`docs/modules.md` / `docs/architecture.md`** —— 若新增/改动了模块、接口或架构决策，同步补充。
3. **本文件 `AGENTS.md`** —— 若新增了常用命令、改变了架构要点或开发约定，同步更新对应章节。
4. **持久记忆**（`linapi-progress`）—— 进度推进后一并更新，保证新窗口一开即知最新状态。

文档与代码保持一致是硬性要求：宁可多写一句，不要让文档落后于代码。

```bash
# 运行（默认读 ./config.yaml，可用 -config 指定）
go run ./cmd/linapi
go run ./cmd/linapi -config /path/to/config.yaml

# 构建
go build -o bin/linapi ./cmd/linapi

# 全部测试
go test ./...

# 竞态检测（需要 C 编译器 / cgo；gcc 已装在 C:\ProgramData\mingw64\mingw64\bin）
CGO_ENABLED=1 go test -race ./...

# 单包 / 单测试
go test ./internal/routing/...
go test ./internal/routing/ -run TestBreaker -v

# 依赖整理
go mod tidy

# 存活 / 就绪检查
curl http://localhost:8080/livez     # {"status":"ok"}
curl http://localhost:8080/readyz    # {"status":"ready"}；依赖异常时 503
```

配置优先级：环境变量 > 配置文件 > 内置默认值。环境变量前缀 `LINAPI_`，`.` 换成 `_`（如 `LINAPI_SERVER_PORT=9000`）。配置文件路径不存在时继续使用环境变量与内置默认值；其他读取/解析错误 fail-fast。

**数据库开关**：`database.enabled=false` 只允许 `server.mode=debug` 的本地开发，走内存 Store + MemoryLedger。`server.mode=release` 强制 `database.enabled=true`，使用 PostgreSQL 权威余额和持久账本；未启用数据库会在启动时致命退出。`database.auto_migrate=true`（默认）通过 `schema_migrations`、checksum 与 PostgreSQL advisory lock 顺序应用 `internal/db/migrations/*.sql`；关闭时 `VerifySchema` 要求数据库版本与当前二进制完全匹配。迁移框架**不会**把旧 Redis 热余额中的历史消费自动反推到 PostgreSQL。

**渠道密钥加密**：只要 `database.enabled=true`，必须通过 `database.channel_key_encryption.key_id` 与 `key`（建议对应 `LINAPI_DATABASE_CHANNEL_KEY_ENCRYPTION_KEY_ID/KEY`）注入 32 字节 base64 主密钥。`channels.api_key` 只存 AES-256-GCM v1 envelope，AAD 绑定 `channel_id`。旧库检测到明文会拒绝启动；维护窗口仅一次启用 `migrate_plaintext=true` 进行事务迁移，成功后立即关闭并轮换历史上游密钥。

**网络与凭证边界**：release 默认只允许公共 HTTPS 上游；私网 CIDR 或 HTTP 必须在 `upstream.target_rules` 按精确 `host:port` 授权，URL 静态校验与拨号期 DNS/IP 校验同时执行。远程 Redis 在 release 必须启用 `redis.tls`（支持 ACL username、CA 与 mTLS），除非显式确认由可信隧道保护；会话 token 只以 SHA-256 摘要出现在 Redis key 中。

**sqlc**：`sqlc.yaml` + `db/schema.sql` + `db/query.sql` 是生成源。当前环境无法联网装 sqlc 二进制，故 `internal/db/` 是**按 sqlc 约定手写的同构产物**；一旦能装 sqlc，`sqlc generate` 可原样覆盖该目录（接口与调用方零改动）。**改表结构**：需同步 `db/schema.sql`（sqlc 源）与 `internal/db/schema.sql`（运行时 embed 的迁移副本）两份。

## 架构总览

请求生命周期：客户端请求 →〔鉴权/Redis 限流〕→ 按客户端格式解析 canonical → 强制模型输出上限 → 按最坏成本向 PostgreSQL Ledger 持久预授权 → 路由选渠道并转发 → 响应反向转换 → 持久记录消费并结算。Redis 不参与资金变动。

三个核心抽象彼此解耦，理解它们的边界是理解全项目的关键：

### 1. Canonical 规范模型（`internal/canonical`）

所有格式转换的中枢——以**覆盖各供应商主要能力的超集**为设计目标，采用 content-block 结构（接近 Anthropic Messages），字段命名保持中立。同格式且模型无重命名时可直通响应；同协议模型别名只在请求侧补丁安全字段与模型名，响应仍可能经过 canonical。跨格式只保证目标协议可表达且 canonical 已建模的能力，不能笼统宣称任意格式无损映射。

- `message.go`：请求侧模型。`Request` / `Message` / `ContentBlock`（带类型联合，`BlockType` 决定哪些字段有效）/ `Tool` / `ToolChoice`。注意 System 提到顶层、不作为消息角色；工具结果作为 user 消息里的 `BlockToolResult` 承载（对齐 Anthropic 结构）。
- `response.go`：响应侧模型。非流式 `Response`；流式用**语义化的规范事件** `Event`（`message_start` / `block_start` / `block_delta` / …），不绑定任何一家 SSE 格式。`StopReason` 是归一化枚举，各家停止原因双向映射到这里。

### 2. 适配器（`internal/adapter`）

`adapter.go` 定义 `Adapter` 接口：`ParseRequest` / `BuildRequest` / `ParseResponse` / `BuildResponse` + 流式的 `NewStreamDecoder` / `NewStreamEncoder`。

**并发约定（重要）**：`Adapter` 方法必须无状态、并发安全——同一实例被多个请求 goroutine 并发调用。流式转换需要跨块状态，因此**不**放在 Adapter 上，而是通过工厂方法为每个流式请求创建独立的 `StreamDecoder` / `StreamEncoder`（有状态、非并发安全）。

**注册机制**：`registry.go` 是全局注册表。新增供应商 = 实现接口 + 在包 `init()` 里 `adapter.Register(&Adapter{})`，不碰其它代码。同名重复注册会 panic（启动即暴露配置错误）。

已实现：`openai`（Chat Completions）、`anthropic`（Messages）。每个适配器包按职责拆成 `request*.go` / `response.go` / `stream_decode.go` / `stream_encode.go` / `types.go`，配 `roundtrip_test.go` 和 `stream_test.go`。

SSE reader 支持 UTF-8 BOM 以及 LF / CRLF / 裸 CR 三种标准行结束；`adapter.SSEData` 忽略 comment / `event` / `id` / `retry`，多个 `data` 行用换行拼接。OpenAI 流内错误可双向映射 `EventError`。`UsageFinal` 只有绑定终止事件/尾块时才具最终权威；其后若出现新内容或无法识别记录，精确 usage 立即失效并改走保守结算。

> 适配器包通过汇总包 `internal/adapter/all` 触发 `init()` 注册——`cmd/linapi` 空导入 `_ "linapi/internal/adapter/all"` 即可。新增供应商时在 `all` 包补一行空导入，main 无需改动。注意：单跑某些包的测试时若直接调 `adapter.Get`，需自行 blank-import 对应适配器包（或 `all`）。

### 3. 路由引擎（`internal/routing`）

纯逻辑，**不发起网络请求**——把「对外模型名」解析为一组可依次尝试的候选渠道，支撑故障转移。真实转发由上层执行。

- `channel.go`：`Channel`（供应商端点 + 凭证 + 能力）。`Models` 是「对外模型名 → 上游实际模型名」映射（值为空表示透传）。
- `select.go`：排序策略 = 优先级降序分组 + 组内**加权随机不放回抽样**（既保证首选符合权重分布，又给出完整故障转移次序）。
- `breaker.go`：每渠道熔断器，标准三态 Closed → Open →（冷却期满）HalfOpen。`Ready()` 只做无副作用预判；`Allow()` 返回绑定当前 generation 的 `BreakerPermit`。每枚 permit 必须且只能以 `RecordSuccess`、`RecordFailure` 或 `RecordNeutral`（客户端取消/下游写失败）结束；旧 generation 的迟到结果不会污染新状态，neutral 会释放半开探测名额但不改变健康判断。
- `router.go`：`Router.Select(model)` 返回有序 `[]Candidate`（渠道 + 熔断器）。

**并发模型（重要）**：读多写少。渠道快照用 `atomic.Pointer[snapshot]` 无锁读，`UpdateChannels` 原子替换指针实现热更新（并保留同 ID 渠道的既有熔断状态）；随机源用 `sync.Pool` 避免全局锁。改这里务必跑 `go test -race ./internal/routing/...`。

### 4. 计费账本（`internal/billing`）

生产唯一权威余额是 PostgreSQL `users.balance`。状态机为 `reserved → in_flight(channel) → consumed_unsettled → settled`；只有明确未消费的 4xx 允许 ReleaseAttempt，退款仅允许 reserved。`billing_reservations` 保存发送/消费状态，`billing_ledger` 保存只追加资金流水。

Forwarder 强制 OpenAI `n=1` 与输出上限；直通请求也总是重编码安全字段，折叠重复 key。release 要求每个启用的 OpenAI `channel/upstream_model` 显式声明上游真正识别 `max_tokens` 还是 `max_completion_tokens`，并在候选已确定后、MarkInFlight 前删除两个旧值并只写策略指定字段。普通输入、输出、缓存创建输入、缓存读取输入四项价格独立计价；预授权输入维度按三类输入价格中的最高值冻结，total 与缓存分项冲突时保留整笔预授权，cost 不得超过 reservation。每个候选先完成本地请求 `prepare`，再 MarkInFlight 并记录 channel，最后才执行网络 `send`；MarkInFlight 失败且尚未 send 时调用 ReleaseAttempt 收敛回可退款状态。HTTP 自动重定向禁用；3xx、网络错、408、5xx 均可能发生在 POST 已处理后，故不重放不退款；只有明确未消费 4xx 才 release。

release 启动验证每个模型都显式配置普通输入、输出、缓存创建输入、缓存读取输入四项非零价格及输入/输出 token 上限，并验证 OpenAI 输出上限字段策略。Recover 启动执行并每 30 秒重跑：完成 consumed_unsettled、退款超过 5 分钟的 reserved、对超过 24 小时的 in_flight 告警人工按 channel 对账；新鲜 in_flight 不阻断多实例启动。只有已经持久化 consumption 的记录可自动续结算，RecordConsumption 持续失败仍需人工对账；首次切换旧余额仍须停写对账。

## 转发层约定（对接路由与熔断时遵守）

上层转发器拿到候选后：`permit, ok := Breaker.Allow()` → 本地构造请求并按真实 upstream model 补丁输出上限字段 → URL/SSRF 校验与 `prepare` → `Billing.MarkInFlight(reservation, channel)` → 网络 `send` → 用 permit 回报 success/failure/neutral。候选级补丁或 prepare 失败尚未触网，可尝试下一候选；MarkInFlight 失败尚未 send，应先 ReleaseAttempt，只有释放成功才可继续/退款。明确未消费 4xx 才能 ReleaseAttempt，其中 401/403/429 可安全换候选；3xx、网络错、408、5xx 保留带 channel 的 in_flight，停止重放和退款。上游响应头等待 30 秒、SSE 空闲 2 分钟；下行每次 SSE 写入有 30 秒 deadline，但整条长流不设总 `WriteTimeout`。

## 目录约定

- `cmd/linapi/`：入口，负责配置加载、release 防线、渠道加载与定时热重载、计费恢复、服务器启动及 SIGINT/SIGTERM 优雅关闭（30s 超时）。空导入 `_ "linapi/internal/adapter/all"` 触发适配器注册。
- `internal/server/`：Gin 服务器与路由；默认 `SetTrustedProxies(nil)`。入站设置 10s `ReadHeaderTimeout`、可配置 `ReadTimeout`/`IdleTimeout`/body/header 上限；`/healthz` 与 `/livez` 只判进程存活，`/readyz` 用 2s 预算检查 Redis/PostgreSQL；`/metrics` 需 token 并受并发/超时预算保护。**注意**：HTTP server 不设整流 `WriteTimeout`，长 SSE 由逐次写 deadline 治理。
- `internal/forwarder/`：唯一发起上游 HTTP 的胶水层，负责请求安全字段补丁、候选级输出上限策略、URL/SSRF 与拨号期地址校验、响应/错误转换、超时和计费生命周期。项目只支持 OpenAI `n=1`；请求侧提前拒绝其它值，上游异常多 choices/index 会显式报错且不跨渠道重放。
- `internal/middleware/`：协议上下文、body 上限、panic 恢复、API Key 鉴权前置闸门、来源 IP/账户/单 Key 限流、登录标识预算、bcrypt `TryAcquire`、会话代次鉴权、角色、CSRF、日志与指标。入口始终生成内部 request id，不接受客户端覆盖。
- release 模式下 `/v1` 匿名/账户预算，以及启用管理面时的认证 IP、登录标识与活跃会话上限都必须为正数；不能用配置零值静默关闭这些生产防线。
- `internal/billing/`：定价策略、持久预授权、Ledger 状态机与启动恢复；生产使用 PostgresLedger，MemoryLedger 只供 debug/测试。
- `internal/admin/`：管理面用户/密钥/渠道 CRUD 与渠道热更新；PG 渠道凭证由 AES-256-GCM envelope 加密，启动迁移与 CRUD 解密边界均在本包。
- `internal/account/`：控制台账户、角色、密码与系统设置。
- `internal/session/`：Redis 登录会话，承载 CSRF token 与 session version；Redis key 使用 token 摘要，并以原子 ZSET 索引限制每账户活跃会话数。
- `internal/metrics/`：Prometheus 指标。
- `internal/db/`：sqlc 同构查询与运行时 Schema；资金表改动必须同步根/内部两份 schema。
- `internal/config/`：Viper 配置，含 Server / Database / Redis / Log / Auth / Billing / Upstream / Channels / Admin 九段。

## 开发进度

已完成 ①~⑦：① 可启动骨架 ② canonical 模型 + 适配器接口/注册表 ③ OpenAI + Anthropic 适配器（含流式）④ 路由/负载均衡引擎 ⑤ 鉴权 + Redis 限流 ⑥ PostgreSQL 权威计费账本（最坏成本预授权、保守 usage 结算、幂等状态机、启动恢复）⑦ 数据库 schema + sqlc 集成。其后已接入管理面、控制台账户/会话、指标、日志、同格式保真路径与审查修复批次。

**转发层已接线**：Forwarder 在解析请求后预授权，候选确定后按真实 upstream model 写唯一输出上限字段，再 MarkInFlight 并发送。收到上游成功响应后先尝试 RecordConsumption，再 Finalize；只有 RecordConsumption 已成功持久化的 consumed_unsettled 能由 Recover 自动续结算。RecordConsumption 经有限重试仍持续失败时，精确 usage 尚无持久待办，余额保持 in_flight 冻结并需人工按 channel 对账；绝不因此退款。流式/非流式缺失或冲突 usage 都走保守结算。

**审查修复进度（2026-07-11）**：审查中的路由代际、超时/资源上限、协议联合字段与工具参数、控制台滥用防护、SSRF/Redis/渠道密钥、迁移框架、指标与供应链项均已落地代码与回归。`AUD-P1-12` 仍为部分修复（RecordConsumption 持续失败没有独立持久 outbox）；`AUD-P2-03` 仍有 OpenAI→Anthropic 迟到 input usage 无法在线格式表达的方向性缺口，但账本保留预授权。真实 PostgreSQL 迁移/故障注入与真实 Redis TLS 集成仍是上线验收，不得用单元测试替代。完整状态见 [审查跟踪表](docs/reviews/2026-07-10-comprehensive-readonly-audit.md)。

> **发布门槛**：账本代码完成不等于旧余额已迁移。上线前必须完成旧余额人工对账、真实 PostgreSQL 故障注入和版本化迁移演练、真实 Redis TLS/ACL 连通验证；数据库渠道密钥迁移后立即关闭 `migrate_plaintext` 并轮换历史供应商 key。其余边界见 [docs/progress.md](docs/progress.md) 与审查跟踪表。

`docs/` 目录有更详细的架构与进度记录，新窗口接手可先读那里。
