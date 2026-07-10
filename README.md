# LinAPI

一个从零构建的 AI API 网关，面向中转站场景。用 Go 编写，追求干净可改的架构、灵活的路由/负载均衡、清晰的计费额度，以及多供应商格式的高保真兼容。

## 设计目标

- **架构干净可改** —— 加供应商 = 实现一个适配器接口 + 注册一行，不碰其它代码。
- **路由灵活** —— 渠道组 / 优先级分层 / 权重随机 / 自动故障转移 / 熔断探活。
- **格式高保真** —— 同格式直通（零损耗），跨格式才转换，不丢 thinking / 工具调用等细节。
- **计费不出错** —— Redis 原子预扣费 + 按实际用量退差 + 用量日志异步落库。

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
  config/          配置加载（Viper）
  server/          HTTP 服务器与路由
```
（模块推进中，后续将新增 adapter / router / billing / store 等）

## 本地运行

前置：安装 Go 1.23+、PostgreSQL、Redis。

```bash
# 1. 准备配置
cp config.example.yaml config.yaml

# 2. 拉取依赖
go mod tidy

# 3. 启动
go run ./cmd/linapi

# 4. 验证
curl http://localhost:8080/healthz
# {"status":"ok"}
```

## 开发进度

- [x] 可启动的最小骨架（配置 / 服务器 / 健康检查）
- [ ] 内部规范数据模型 + 适配器接口与注册表
- [ ] OpenAI + Claude 适配器
- [ ] 路由 / 负载均衡引擎
- [ ] 鉴权 + 限流 + 额度中间件
- [ ] 计费结算
- [ ] 数据库 schema + sqlc 集成
