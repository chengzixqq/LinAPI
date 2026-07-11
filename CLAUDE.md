# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目简介

LinAPI 是一个从零构建的 AI API 网关（中转站场景）：接收 OpenAI / Claude 等格式的客户端请求，路由到上游供应商渠道，做格式转换、负载均衡、鉴权限流与计费。Go + Gin + PostgreSQL + Redis + sqlc，面向商用高并发。

代码注释与文档以中文为主，新增代码请沿用中文注释风格。

## 维护约定（重要）

**每完成一个任务（新模块 / 功能 / 修复），必须同步更新文档，再结束该任务：**

1. **`docs/progress.md`** —— 更新顶部日期、七步进度表状态；把新完成的细节、待接线事项记进去。
2. **`docs/modules.md` / `docs/architecture.md`** —— 若新增/改动了模块、接口或架构决策，同步补充。
3. **本文件 `CLAUDE.md`** —— 若新增了常用命令、改变了架构要点或开发约定，同步更新对应章节。
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

# 控制台前端（源码在 web/，产物 embed 进二进制）
cd web && npm install          # 首次
cd web && npm run dev          # 本地开发（:5173，proxy 到 :8080）
cd web && npm run typecheck    # tsc -b 类型门禁
cd web && npm run build        # 产物→ internal/server/web_dist，改前端后必跑

# 存活 / 就绪检查
curl http://localhost:8080/livez
curl http://localhost:8080/readyz
```

配置优先级：环境变量 > 配置文件 > 内置默认值。环境变量前缀 `LINAPI_`，`.` 换成 `_`（如 `LINAPI_SERVER_PORT=9000`）。配置路径不存在时继续使用环境变量与默认值，其他读取/解析错误 fail-fast。

**数据库开关**：`database.enabled=false` 只允许 `server.mode=debug` 的本地开发，走内存 Store + MemoryLedger。`server.mode=release` 强制 `database.enabled=true` 并使用 PostgreSQL 权威账本；否则启动失败。`database.auto_migrate=true` 通过 `schema_migrations`、checksum、advisory lock 与增量 SQL 升级；关闭时验证数据库版本与二进制完全匹配。它不会自动反推旧 Redis 历史消费。

**生产凭证/网络配置**：数据库模式必须注入 `database.channel_key_encryption.key_id/key`（32 字节 base64），`channels.api_key` 只存绑定 `channel_id` AAD 的 AES-256-GCM envelope。旧明文仅可在维护窗一次启用 `migrate_plaintext`，成功后立即关闭并轮换供应商 key。release 默认只允许公共 HTTPS 上游，私网/HTTP 通过 `upstream.target_rules` 按精确 authority 授权；远程 Redis 必须配置 TLS/ACL（或显式确认可信隧道），会话 key 只暴露 token 摘要。

**sqlc**：`sqlc.yaml` + `db/schema.sql` + `db/query.sql` 是生成源。当前环境无法联网装 sqlc 二进制，故 `internal/db/` 是**按 sqlc 约定手写的同构产物**；一旦能装 sqlc，`sqlc generate` 可原样覆盖该目录（接口与调用方零改动）。**改表结构**：需同步 `db/schema.sql`（sqlc 源）与 `internal/db/schema.sql`（运行时 embed 的迁移副本）两份。

## 架构总览

请求生命周期：客户端请求 →〔鉴权/Redis 限流〕→ 按客户端格式解析 canonical → 强制模型输出上限 → 按最坏成本向 PostgreSQL Ledger 持久预授权 → 路由选渠道并转发 → 响应反向转换 → 持久记录消费并结算。Redis 不参与资金变动。

三个核心抽象彼此解耦，理解它们的边界是理解全项目的关键：

### 1. Canonical 规范模型（`internal/canonical`）

所有格式转换的中枢——以**覆盖各供应商主要能力的超集**为设计目标，采用 content-block 结构（接近 Claude Messages），字段命名保持中立。同格式且模型无重命名时可直通响应；同协议模型别名只在请求侧补丁安全字段与模型名，响应仍可能经过 canonical。跨格式只保证目标协议可表达且 canonical 已建模的部分，不能笼统宣称任意格式无损映射。

- `message.go`：请求侧模型。`Request` / `Message` / `ContentBlock`（带类型联合，`BlockType` 决定哪些字段有效）/ `Tool` / `ToolChoice`。注意 System 提到顶层、不作为消息角色；工具结果作为 user 消息里的 `BlockToolResult` 承载（对齐 Claude 结构）。
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
- `breaker.go`：每渠道熔断器，标准三态 Closed → Open →（冷却期满）HalfOpen。`Ready()` 无副作用；`Allow()` 返回绑定 generation 的 `BreakerPermit`，必须以 success/failure/neutral 之一结束。旧代际迟到结果被忽略，客户端取消使用 neutral 释放探测名额而不污染渠道健康。
- `router.go`：`Router.Select(model)` 返回有序 `[]Candidate`（渠道 + 熔断器）。

**并发模型（重要）**：读多写少。渠道快照用 `atomic.Pointer[snapshot]` 无锁读，`UpdateChannels` 原子替换指针实现热更新（并保留同 ID 渠道的既有熔断状态）；随机源用 `sync.Pool` 避免全局锁。改这里务必跑 `go test -race ./internal/routing/...`。

### 4. 计费账本（`internal/billing`）

生产唯一权威余额是 PostgreSQL `users.balance`。状态机为 `reserved → in_flight(channel) → consumed_unsettled → settled`；只有明确未消费 4xx 可 ReleaseAttempt，退款仅允许 reserved。内部 reservation/operation ID 使重放幂等。

Forwarder 强制 OpenAI `n=1` 与输出上限；直通也重编码安全字段以折叠重复 key。release 为每个启用的 OpenAI `channel/upstream_model` 显式配置 `max_tokens` 或 `max_completion_tokens`，候选确定后、MarkInFlight 前删除两个旧字段并只写策略指定字段。预授权输入按普通输入/两类缓存最高费率，实际 usage 独立计价；total 与缓存分项冲突时保留整笔预授权，cost 不得超过 reservation。每个候选先本地补丁/prepare，再 MarkInFlight，最后 send；自动重定向禁用。3xx、网络错、408、5xx 不重放不退款，明确未消费 4xx 才 release。

release 验证每个模型显式配置普通输入、输出、缓存创建、缓存读取四项非零价格及输入/输出 token 上限，并验证 OpenAI 输出上限字段策略。Recover 启动执行并每 30 秒重跑：完成 consumed_unsettled、退款超过 5 分钟的 reserved、对超过 24 小时的 in_flight 告警人工对账；新鲜 in_flight 不阻断多实例启动。只有已持久化 consumption 可自动续结算，RecordConsumption 持续失败仍需人工对账；首次切换旧资金方案仍须停写对账。

## 转发层约定（对接路由与熔断时遵守）

上层转发器拿到候选后：`Breaker.Allow` 取 permit → 本地补丁 → URL/SSRF 校验与 prepare → `Billing.MarkInFlight` → send → permit 回报 success/failure/neutral。只有明确未消费 4xx 才可 ReleaseAttempt；401/403/429 release 后可换候选。3xx、网络错、408、5xx 保留 in_flight，停止重放和退款。上游响应头 30s、SSE 空闲 2min、下行单次写 30s deadline；长流不设整体超时。

## 目录约定

- `cmd/linapi/`：入口，负责配置加载、启动、渠道加载喂给 router、渠道定时热重载 goroutine（DB 模式）、SIGINT/SIGTERM 优雅关闭（30s 超时）。空导入 `_ "linapi/internal/adapter/all"` 触发适配器注册。
- `internal/server/`：Gin 路由与 HTTP 资源边界。默认不信任代理头；配置读/闲置/body/header 上限，提供 `/livez` 与强依赖 `/readyz`；`/metrics` 需要 token 并有最大并发与超时。整流不设 `WriteTimeout`，SSE 使用逐次写 deadline。`console.go` 用 `//go:embed all:web_dist` 伺服控制台前端（`/console/*` + SPA fallback，仅 `admin.enabled=true`）。
- `web/`：控制台前端（Vite + React + TS + Mantine）。源码 `src/`（api/stores/theme/components/pages），`npm run build` 产物输出到 `internal/server/web_dist` 供 embed。`node_modules` 与 `web_dist` 已 gitignore。
- `internal/forwarder/`：唯一发起上游 HTTP 的胶水层。负责最坏成本预授权、候选输出字段、SSRF/拨号策略、响应头/SSE idle/下行写期限、协议错误转换和计费。OpenAI 只支持 `n=1`，异常多 choices/index 显式拒绝且不跨渠道重试。
- `internal/middleware/`：协议错误上下文、body/recovery、API Key 前置并发闸门、来源 IP/登录标识/账户/单 Key 限流、会话代次/活跃上限、角色、CSRF、内部 request id、日志与指标。
- `internal/billing/`：定价策略、持久预授权、Ledger 状态机与启动恢复；生产使用 PostgresLedger，MemoryLedger 只供 debug/测试。
- `internal/admin/`：管理面服务（用户/密钥/渠道 CRUD），渠道写操作触发 router 热更新。
- `internal/account/`：控制台账户认证领域（登录账户/角色/系统设置，与计费实体解耦），bcrypt 密码哈希，内存/PG 双实现（建 user 账户原子连带计费实体）。
- `internal/session/`：Redis 会话管理（token 摘要 key + TTL + 记住我 + 每账户原子活跃上限），控制台登录态载体。
- `internal/metrics/`：Prometheus 指标定义与埋点辅助函数。
- `internal/config/`：Viper 配置，含 Server / Database / Redis / Log / Auth / Admin / Billing / Upstream / Channels 九段。

## 开发进度

已完成 ①~⑦：① 可启动骨架 ② canonical 模型 + 适配器接口/注册表 ③ OpenAI + Claude 适配器（含流式）④ 路由/负载均衡引擎 ⑤ 鉴权 + Redis 限流 ⑥ PostgreSQL 权威计费账本（最坏成本预授权、保守 usage 结算、幂等状态机、启动恢复）⑦ 数据库 schema + sqlc 集成。

**转发层已接线**：Forwarder 在解析请求后预授权，收到上游成功响应后先有限重试 `RecordConsumption`，再 Finalize；只有 RecordConsumption 已持久化的 consumed_unsettled 可由 Recover 自动续结算。RecordConsumption 持续失败时精确 usage 尚无持久待办，余额保持冻结并需人工对账，绝不因此退款。流式/非流式缺失 usage 都走保守结算。

**运维增强（第 8 步之后）**：⑨ 管理面 CRUD 与渠道热更新 ⑩ Prometheus 指标 ⑪ `/v1/models` ⑫ 同格式直通（请求做输出上限/stream usage 的最小保真合并，响应短路 canonical；仍解码 usage）⑬ 结构化访问日志。入口忽略入站 `X-Request-Id` 并生成内部 trace ID；账单唯一键另用内部 reservation ID。

**统一账户认证体系（控制台后端，第 14 步）**：⑭ 把管理面从裸 token 升级为「账号密码 + 会话」。`SessionData` 承载 CSRF token 与 session version；受保护路由用 `SessionAuthWithVersion`，写请求过 `CSRFProtect`。登录/注册在 bcrypt 前执行来源 IP 与账户标识预算，`TryAcquire` 满载立即 503；Redis 原子限制每账户活跃会话。自助 Key 由存储层原子限制 50 把，另有账户总桶与单 Key 上限。

**控制台前端（第 16 步）**：⑯ Vite 5 + React 18 + TS + `@douyinfe/semi-ui`，源码在 `web/`。`vite build` 产物输出 `internal/server/web_dist`，`console.go` `//go:embed all:web_dist` 打进二进制，`/console/*` 伺服 + SPA fallback（仅 `admin.enabled=true` 挂载）。`api/client.ts` 从 Cookie 读 CSRF token 注入 `X-CSRF-Token` 对齐 `CSRFProtect`；dev vite proxy 用字符串简写（`changeOrigin:false`）保持同源。**改前端后必须 `npm run build` 重新生成 `web_dist` 才会被 embed**。

**审查修复进度（2026-07-11）**：路由许可代际、HTTP/SSE 超时与大小限制、协议联合字段/工具参数/n=1 显式拒绝、控制台滥用防护、SSRF、Redis TLS、渠道密钥加密、版本化迁移、指标防护和依赖公告均已落地。P1-12 仍部分修复；P2-03 仅剩 OpenAI→Anthropic 迟到 input usage 无法在线格式表达，账本按预授权保守结算。真实 PostgreSQL 迁移/故障注入与真实 Redis TLS 集成仍待上线验收。

> **发布门槛**：上线前必须完成旧余额人工对账、真实 PostgreSQL 迁移与故障注入、真实 Redis TLS/ACL 连通；渠道明文迁移后关闭 `migrate_plaintext` 并轮换历史供应商 key。详见 [docs/progress.md](docs/progress.md)。

`docs/` 目录有更详细的架构与进度记录，新窗口接手可先读那里。
