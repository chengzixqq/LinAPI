# LinAPI 项目文档

本目录记录 LinAPI 的整体架构、模块设计与开发进度，供新窗口 / 新成员快速接手。

## 阅读顺序

1. [architecture.md](architecture.md) —— 整体架构、请求生命周期、三大核心抽象的边界与协作
2. [modules.md](modules.md) —— 各模块（canonical / adapter / routing / server / config）逐一详解
3. [progress.md](progress.md) —— 开发进度、已完成模块清单、待办与下一步计划

根目录的 [CLAUDE.md](../CLAUDE.md) 是给 Claude Code 的速查版（命令 + 架构要点），本目录是展开版。

## 一句话概览

LinAPI 是一个 AI API 网关（中转站）：接收 OpenAI / Claude 等格式的请求，经鉴权限流后转成内部规范格式，由路由引擎选择上游渠道并做负载均衡 / 故障转移 / 熔断，再转成目标渠道格式转发，最后计费结算。

技术栈：Go 1.23 · Gin · PostgreSQL · Redis · sqlc。

## 设计目标（不可动摇的四条）

- **架构干净可改** —— 加供应商 = 实现一个适配器接口 + 注册一行，不碰其它代码。
- **路由灵活** —— 渠道组 / 优先级分层 / 权重随机 / 自动故障转移 / 熔断探活。
- **格式高保真** —— 同格式直通（零损耗），跨格式才转换，不丢 thinking / 工具调用等细节。
- **计费不出错** —— Redis 原子预扣费 + 按实际用量退差 + 用量日志异步落库。
