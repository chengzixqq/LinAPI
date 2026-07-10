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

# 健康检查
curl http://localhost:8080/healthz   # {"status":"ok"}
```

配置优先级：环境变量 > 配置文件 > 内置默认值。环境变量前缀 `LINAPI_`，`.` 换成 `_`（如 `LINAPI_SERVER_PORT=9000`）。配置文件缺失不报错，全部走默认值。

**数据库开关**：默认 `database.enabled=false`——走内存 Store + 丢弃用量日志，本地开发免装 PG。设 `database.enabled=true`（或 `LINAPI_DATABASE_ENABLED=true`）启用 PostgreSQL；`database.auto_migrate=true`（默认）启动时幂等建表。启用后连不上库会致命退出。

**sqlc**：`sqlc.yaml` + `db/schema.sql` + `db/query.sql` 是生成源。当前环境无法联网装 sqlc 二进制，故 `internal/db/` 是**按 sqlc 约定手写的同构产物**；一旦能装 sqlc，`sqlc generate` 可原样覆盖该目录（接口与调用方零改动）。**改表结构**：需同步 `db/schema.sql`（sqlc 源）与 `internal/db/schema.sql`（运行时 embed 的迁移副本）两份。

## 架构总览

请求生命周期（目标形态）：客户端请求 →〔鉴权/限流/额度中间件〕→ 按**客户端格式**用适配器 `ParseRequest` 解析为 canonical →〔路由引擎〕选渠道 → 按**渠道格式**用适配器 `BuildRequest` 构造上游请求 → 转发 → 响应反向转换回客户端格式 →〔计费结算〕。

三个核心抽象彼此解耦，理解它们的边界是理解全项目的关键：

### 1. Canonical 规范模型（`internal/canonical`）

所有格式转换的中枢——**各供应商格式的超集**，采用 content-block 结构（表达力最强，接近 Claude Messages），字段命名保持中立。任何供应商格式无损映射进来，避免以某一家为内部标准时丢失 thinking / 结构化工具调用 / 多模态等信息。

- `message.go`：请求侧模型。`Request` / `Message` / `ContentBlock`（带类型联合，`BlockType` 决定哪些字段有效）/ `Tool` / `ToolChoice`。注意 System 提到顶层、不作为消息角色；工具结果作为 user 消息里的 `BlockToolResult` 承载（对齐 Claude 结构）。
- `response.go`：响应侧模型。非流式 `Response`；流式用**语义化的规范事件** `Event`（`message_start` / `block_start` / `block_delta` / …），不绑定任何一家 SSE 格式。`StopReason` 是归一化枚举，各家停止原因双向映射到这里。

### 2. 适配器（`internal/adapter`）

`adapter.go` 定义 `Adapter` 接口：`ParseRequest` / `BuildRequest` / `ParseResponse` / `BuildResponse` + 流式的 `NewStreamDecoder` / `NewStreamEncoder`。

**并发约定（重要）**：`Adapter` 方法必须无状态、并发安全——同一实例被多个请求 goroutine 并发调用。流式转换需要跨块状态，因此**不**放在 Adapter 上，而是通过工厂方法为每个流式请求创建独立的 `StreamDecoder` / `StreamEncoder`（有状态、非并发安全）。

**注册机制**：`registry.go` 是全局注册表。新增供应商 = 实现接口 + 在包 `init()` 里 `adapter.Register(&Adapter{})`，不碰其它代码。同名重复注册会 panic（启动即暴露配置错误）。

已实现：`openai`（Chat Completions）、`anthropic`（Messages）。每个适配器包按职责拆成 `request*.go` / `response.go` / `stream_decode.go` / `stream_encode.go` / `types.go`，配 `roundtrip_test.go` 和 `stream_test.go`。

> 适配器包通过汇总包 `internal/adapter/all` 触发 `init()` 注册——`cmd/linapi` 空导入 `_ "linapi/internal/adapter/all"` 即可。新增供应商时在 `all` 包补一行空导入，main 无需改动。注意：单跑某些包的测试时若直接调 `adapter.Get`，需自行 blank-import 对应适配器包（或 `all`）。

### 3. 路由引擎（`internal/routing`）

纯逻辑，**不发起网络请求**——把「对外模型名」解析为一组可依次尝试的候选渠道，支撑故障转移。真实转发由上层执行。

- `channel.go`：`Channel`（供应商端点 + 凭证 + 能力）。`Models` 是「对外模型名 → 上游实际模型名」映射（值为空表示透传）。
- `select.go`：排序策略 = 优先级降序分组 + 组内**加权随机不放回抽样**（既保证首选符合权重分布，又给出完整故障转移次序）。
- `breaker.go`：每渠道熔断器，标准三态 Closed → Open →（冷却期满）HalfOpen。区分两个准入方法：`Ready()` 无副作用（供 `Select` 过滤候选，不消耗半开探测额度）、`Allow()` 有副作用（真正发起尝试前调用，且**必须**配对 `RecordSuccess` / `RecordFailure` 释放探测额度）。
- `router.go`：`Router.Select(model)` 返回有序 `[]Candidate`（渠道 + 熔断器）。

**并发模型（重要）**：读多写少。渠道快照用 `atomic.Pointer[snapshot]` 无锁读，`UpdateChannels` 原子替换指针实现热更新（并保留同 ID 渠道的既有熔断状态）；随机源用 `sync.Pool` 避免全局锁。改这里务必跑 `go test -race ./internal/routing/...`。

## 转发层约定（对接路由与熔断时遵守）

上层转发器拿到 `[]Candidate` 后，对每个候选依次：`Candidate.Breaker.Allow()` 准入 → 获准才发请求 → 按成败调 `RecordSuccess` / `RecordFailure`。漏调 Record 会导致半开探测额度泄漏、熔断器卡死。

## 目录约定

- `cmd/linapi/`：入口，负责配置加载、启动、渠道加载喂给 router、渠道定时热重载 goroutine（DB 模式）、SIGINT/SIGTERM 优雅关闭（30s 超时）。空导入 `_ "linapi/internal/adapter/all"` 触发适配器注册。
- `internal/server/`：Gin 服务器与路由。全局挂 `RequestLogger`（结构化访问日志）+ `Metrics()`；`/healthz`、`/metrics` 不走鉴权也不记访问日志；`/v1/chat/completions`（openai）与 `/v1/messages`（anthropic）由 `Forwarder.Handler` 处理，`/v1/models` 聚合渠道模型；控制台端点 `/auth`（注册/登录/登出）、`/me`（用户自助）、`/admin/*`（可选，`admin.enabled`）由会话鉴权守护（`/admin` 需 admin 角色）。**注意**：HTTP server 故意不设 `WriteTimeout`——流式（SSE）响应可能持续数分钟，写超时会中途掐断长回复。
- `internal/forwarder/`：转发层胶水，把适配器 + 路由 + 熔断 + 计费串起来真正发上游 HTTP，是唯一发起网络请求的地方。同格式无重命名时走直通（逐字节透传，短路 canonical 往返）。复用 `middleware` 注入的 request_id，并回填 model/channel/usage 到访问日志。
- `internal/middleware/`：HTTP 中间件——Auth / RateLimit / Quota（挂 `/v1`）、SessionAuth / RequireRole（守护 `/me` 与 `/admin`）、Metrics + RequestLogger（全局）。`RequestLogger` 分配/复用 request_id 并输出结构化访问日志（模型/渠道/用量由转发层回填）。
- `internal/admin/`：管理面服务（用户/密钥/渠道 CRUD），渠道写操作触发 router 热更新。
- `internal/account/`：控制台账户认证领域（登录账户/角色/系统设置，与计费实体解耦），bcrypt 密码哈希，内存/PG 双实现（建 user 账户原子连带计费实体）。
- `internal/session/`：Redis 会话管理（不透明会话 ID + TTL + 记住我），控制台登录态载体。
- `internal/metrics/`：Prometheus 指标定义与埋点辅助函数。
- `internal/config/`：Viper 配置，含 Server / Database / Redis / Log / Auth / Billing / Channels / Admin 八段（Admin 段自控制台后端起改为 `bootstrap` 首个管理员播种 + 会话鉴权，去除裸 token）。

## 开发进度

已完成 ①~⑦：① 可启动骨架 ② canonical 模型 + 适配器接口/注册表 ③ OpenAI + Claude 适配器（含流式）④ 路由/负载均衡引擎（优先级/权重/故障转移/熔断，已过 -race）⑤ 鉴权 + 限流 + 额度中间件（Redis）⑥ 计费结算（Redis 原子预扣费 + 按实际用量退差 + 用量日志异步落库，已过 -race）⑦ 数据库 schema + sqlc 集成（PostgreSQL 实现 Store/Sink，pgxpool 连接池 + 幂等建表，已过 -race）。

**转发层已接线**（收尾核心）：`internal/forwarder` 把适配器 + 路由 + 熔断 + 计费串成真正发 HTTP 的 handler，请求可端到端跑通（非流式 + 流式 SSE，含跨供应商故障转移）。已接线三处：适配器 blank-import（经 `internal/adapter/all` 汇总包）、启动时加载渠道喂给 router、`/v1` 端点由转发层处理（替换 501 占位）。计费在转发终局结算：成功且有用量调 `billing.Settle` 退差 + 记用量，否则 `billing.Refund` 退押金。

**运维增强（第 8 步之后）**：⑨ 管理面 CRUD（`internal/admin` + `/admin` 分组，用户/密钥/渠道；渠道写操作即时热更新 router + DB 模式定时重载兜底多实例）⑩ Prometheus 指标（`internal/metrics` + `/metrics`，HTTP/上游/熔断埋点，标签基数可控）⑪ `/v1/models`（从启用渠道聚合对外模型名）⑫ 同格式直通（客户端格式==渠道格式且无重命名时短路 canonical 往返、逐字节透传，保真且省编解码；仍解码提取 usage 计费）⑬ 结构化访问日志（`middleware.RequestLogger`，request_id 贯通并与用量日志对账，输出模型/渠道/用量/耗时；管理面 + 中间件测试补全，全过 -race）。

**统一账户认证体系（控制台后端，第 14 步）**：⑭ 把管理面从裸 token 升级为「账号密码 + 会话」的多账户体系。`internal/account`（登录账户/角色/系统设置双实现，与计费实体解耦，建 user 账户原子连带计费实体）+ `internal/session`（Redis 会话）+ `middleware.SessionAuth`/`RequireRole`（fail-closed）。端点：`/auth`（注册受开关约束/登录/登出/me）、`/me`（用户自助，越权硬约束——操作他人 key 返回 404）、`/admin/accounts` + `/admin/settings`。`/admin` 改会话+admin 角色鉴权，`AdminAuth` 裸 token 彻底退役。启动 `bootstrapAdmin` 幂等播种首个管理员（拒空密码、日志不记密码）。密码 bcrypt、schema 双写（accounts/settings 表 + users.rate_multiplier）。附带修复 `.gitignore` 误伤 `cmd/linapi/` 的裸 `linapi` 规则。全过 -race。

> **后续可选增强（非阻塞）**：控制台前端（Plan 2：登录页/管理台/用户面板）、分布式追踪（OpenTelemetry）、认证增强（审计日志/更细 RBAC/CSRF/匿名注册限流）、更多供应商适配器（Gemini 等）。详见 [docs/progress.md](docs/progress.md)。

`docs/` 目录有更详细的架构与进度记录，新窗口接手可先读那里。
