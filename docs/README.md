# LinAPI 项目文档

本目录记录 LinAPI 的整体架构、模块设计与开发进度，供新窗口 / 新成员快速接手。

## 阅读顺序

1. [architecture.md](architecture.md) —— 整体架构、请求生命周期、三大核心抽象的边界与协作
2. [modules.md](modules.md) —— canonical、适配器、路由、计费、控制台、服务器与配置等模块逐一详解
3. [progress.md](progress.md) —— 开发进度、已完成模块清单、待办与下一步计划
4. [reviews/2026-07-10-comprehensive-readonly-audit.md](reviews/2026-07-10-comprehensive-readonly-audit.md) —— 三轮多智能体全面只读审查；累计记录 65 项已确认 BUG/安全风险、证据、修复批次与 AI 交接要求

根目录的 [CLAUDE.md](../CLAUDE.md) 是给 Claude Code 的速查版（命令 + 架构要点），本目录是展开版。

> **协作提示（2026-07-11）**：路由 permit 代际/neutral 取消、HTTP/SSE 超时与大小限制、联合字段/developer/tool RawMessage/n=1 显式拒绝、控制台限速与原子上限、SSRF、Redis TLS/会话摘要、渠道密钥加密、版本化迁移、指标鉴权和依赖公告均已进入当前工作区。P1-12 仍部分修复；P2-03 只剩 OpenAI→Anthropic 迟到 input usage 的线格式不可表达，账本保守保留预授权。真实 PostgreSQL 迁移/故障注入、旧余额对账和真实 Redis TLS 集成仍是发布门槛。

## 一句话概览

LinAPI 是一个 AI API 网关（中转站）：接收 OpenAI / Claude 等格式的请求，经鉴权限流后转成内部规范格式，由路由引擎选择上游渠道并做负载均衡 / 故障转移 / 熔断，再转成目标渠道格式转发，最后计费结算。

技术栈：Go 1.23 · Gin · PostgreSQL · Redis · sqlc。

## 设计目标（不可动摇的四条）

- **架构干净可改** —— 新增供应商只需实现适配器、在自身包注册，并在 `internal/adapter/all` 补一行空导入；无需修改转发核心。
- **路由灵活** —— 渠道组 / 优先级分层 / 权重随机 / 熔断探活；仅在可证明上游未消费时自动故障转移，发送结果未知则保留预授权等待对账。
- **格式高保真（设计目标）** —— 同格式且模型无重命名时保留未知请求字段语义并直通响应；同协议模型别名只在请求侧补丁模型和安全字段，响应仍可能经过 canonical。跨格式只保证目标协议可表达且 canonical 已建模的能力，已知缺口以审查跟踪表为准。
- **计费不出错** —— PostgreSQL 权威账本；四项价格独立计价，按三类输入最高价与输出上界预授权，total/cache 冲突保留整笔预授权。只有已持久化的 consumption 可持续恢复；usage 尚未写入账本前若数据库持续故障则冻结待人工对账。

生产约束：`server.mode=release` 强制 PostgreSQL、逐模型价格/边界、逐 OpenAI channel/upstream_model 输出字段策略、metrics token、上游公共 HTTPS/精确例外、远程 Redis TLS 与渠道密钥加密。`auto_migrate=true` 用版本/checksum/advisory lock 应用增量迁移；关闭时数据库版本必须完全匹配。旧渠道明文只允许维护窗一次迁移，完成后关闭开关并轮换供应商 key。配置文件不存在时使用环境变量与默认值，其他读取/解析错误 fail-fast。
