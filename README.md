# LinAPI

一个从零构建的 AI API 网关，面向中转站场景。用 Go 编写，追求干净可改的架构、灵活的路由/负载均衡、清晰的计费额度，以及多供应商格式的高保真兼容。

## 设计目标

- **架构干净可改** —— 新增供应商只需实现适配器、在自身包注册，并在 `internal/adapter/all` 增加一行空导入；无需修改转发核心。
- **路由灵活** —— 渠道组 / 优先级分层 / 权重随机 / 熔断探活；仅在可证明上游未消费时自动故障转移，发送结果未知时保留预授权等待对账。
- **格式高保真（设计目标）** —— 同格式且模型无重命名时保留未知请求字段语义并直通响应；同协议模型别名只在请求侧补丁模型和安全字段，响应仍可能经过 canonical。跨格式尽量保留目标协议可表达且 canonical 已建模的能力，不能视为无损保证。
- **计费不出错** —— PostgreSQL 权威账本；普通输入、输出、缓存创建输入、缓存读取输入四项价格独立计价，按三类输入价格中的最高值与输出上界预授权，usage 异常保守结算。
- **公网边界 fail-closed** —— release 默认公共 HTTPS 上游、远程 Redis TLS/ACL、数据库渠道密钥 AES-256-GCM；请求/响应大小、HTTP/SSE 超时、鉴权前并发和指标抓取均有硬预算。

## 技术栈

| 层 | 选择 |
|---|---|
| 语言 | Go 1.23+ |
| Web 框架 | Gin |
| 数据库 | PostgreSQL |
| 缓存 | Redis |
| 数据访问 | sqlc（类型安全 SQL）|

## 目录结构

```
cmd/linapi/        程序入口
internal/
  canonical/       供应商无关的请求/响应/流事件
  adapter/         OpenAI / Anthropic 格式适配器
  routing/         渠道选择、故障转移与熔断
  forwarder/       上游转发与计费生命周期
  billing/         定价、持久预授权与账本状态机
  account/         控制台账户与系统设置
  session/         Redis 登录会话
  admin/           管理面 CRUD 与渠道热更新
  middleware/      鉴权、限流、CSRF、日志与指标中间件
  metrics/         Prometheus 指标
  db/              sqlc 同构查询与运行时 Schema
  store/           身份与管理数据访问
  config/          配置加载（Viper）
  server/          HTTP 服务器与路由
```

## 本地运行

前置：安装 Go 1.23+、Redis。debug 模式可使用内存 Store/Ledger；release 模式必须使用 PostgreSQL，并通过环境变量或 Secret Manager 注入渠道密钥加密主密钥。远程 Redis 必须配置 TLS/ACL。

```bash
# 1. 准备配置
cp config.example.yaml config.yaml
# 仅本地内存开发时设置 server.mode=debug、database.enabled=false

# 2. 拉取依赖
go mod tidy

# 3. 启动
go run ./cmd/linapi

# 4. 验证
curl http://localhost:8080/healthz
# {"status":"ok"}
curl http://localhost:8080/readyz
# {"status":"ready"}；Redis/PostgreSQL 不可用时 503
```

## 开发进度

- [x] 可启动的最小骨架（配置 / 服务器 / 健康检查）
- [x] 内部规范数据模型 + 适配器接口与注册表
- [x] OpenAI + Claude 适配器
- [x] 路由 / 负载均衡引擎
- [x] 鉴权 + Redis 限流
- [x] PostgreSQL 权威计费账本与保守 usage 结算（代码级）
- [x] 数据库 schema + sqlc 集成
- [x] 转发层、管理面、控制台后端、指标与结构化日志
- [x] 路由取消语义、协议兼容、资源上限、SSRF/Redis/渠道密钥与版本化迁移安全批次（代码级）

生产仍有强制门槛：release 要求逐模型四项价格/输入输出上界、逐 OpenAI `channel/upstream_model` 输出字段策略、`server.metrics_token`、远程 Redis TLS 与数据库渠道加密 key。旧明文渠道 key 只能在维护窗一次开启 `migrate_plaintext`，成功后关闭并轮换供应商 key。首次切换新账本前还须冻结旧资金写入并人工对账；随后在真实 PostgreSQL 完成版本化迁移、并发预授权、提交结果未知、重放与崩溃恢复测试，并在真实 Redis TLS/ACL 环境做连通验证。详见 [开发进度](docs/progress.md) 和 [审查跟踪表](docs/reviews/2026-07-10-comprehensive-readonly-audit.md)。
