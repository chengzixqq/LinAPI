# LinAPI 管理控制台设计文档（Admin Console + 统一认证）

> 状态：设计已定稿，待复核 → 转实现计划
> 日期：2026-07-10
> 范围：为 LinAPI 网关新增一个 Web 管理控制台，并为其补齐统一账户认证体系（账户/密码/角色/会话）。前端 React + Vite + TypeScript + Semi Design，以嵌入式单二进制形态随 Go 后端交付。

---

## 0. 背景与目标

LinAPI 后端功能已完整（七步 + 转发层 + 运维增强），但只能通过裸 `admin` token 直接调 `/admin` API 管理，没有可视界面，也没有面向终端用户的自助能力。本期目标：

1. **统一 Web 控制台**：管理员与普通用户用**同一个登录页**，按账户角色分流到不同界面。
2. **统一认证体系**：后端长出账户/密码/角色/会话（此前只有裸 `admin` token 与业务 API key 两套鉴权）。
3. **用户自助**：普通用户注册后拥有额度容器，可自助创建/管理消耗自己额度的 API key（对齐 New API / sub2api 的用户侧形态）。
4. **高质量交付**：不是能用就行——UI 观感、三态、反馈、错误处理有明确可验收的硬标准。

### 对标参考

- **New API**（React + Semi Design，后端 Go）：本期前端路线与用户模型的主要对标对象。
- **sub2api**（Vue 3，后端 Go）：用户自助形态参考。

两者均验证了「Go 后端 + 独立前端 + 用户自助建 key 消耗自身额度」这一形态；本期选 React + Semi 对齐 New API。

### 非目标（本期明确不做）

- 复杂 RBAC（只有 admin / user 两个角色）。
- key 级独立额度上限（额度在账户级统一扣；New API 那套 key 级配额留后续）。
- **模型分组 / 定价倍率**：不做分组表、分组 CRUD、分组管理页，不改计费逻辑。但**预留数据列**（`accounts.group_name`、`users.rate_multiplier`），存而不用，为将来零改表落地铺路（见 3.7）。
- 完整 i18n 多语言系统（本期中文优先，结构上不写死散落文案，为将来加 i18n 留余地）。
- 邮箱验证 / 邀请码 / 找回密码等注册增强（留后续）。
- OpenTelemetry 分布式追踪、审计日志（留后续）。

---

## 1. 整体架构与部署形态

### 技术栈

| 层 | 选型 |
|----|------|
| 前端框架 | React 18 |
| 构建工具 | Vite + TypeScript |
| UI 组件库 | Semi Design（`@douyinfe/semi-ui`） |
| 前端路由 | react-router |
| 后端 | Go + Gin（现有，扩展认证层） |
| 会话存储 | Redis（现有） |
| 账户/设置存储 | PostgreSQL（现有，新增表）+ 内存实现（免 DB 开发） |

### 目录落位

前端源码放在仓库 `web/` 目录，与后端同仓（对齐 New API / sub2api 惯例）：

```
LinAPI/
├── cmd/linapi/
├── internal/
│   └── server/
│       ├── console.go        # 新增：embed 前端产物 + /console 路由伺服（SPA fallback）
│       └── web_dist/         # 新增：go:embed 目标目录（前端 build 产物拷入）
├── web/                      # 新增：前端工程根
│   ├── src/
│   │   ├── api/              # 封装 /auth /admin /me 端点的 fetch 客户端 + TS 类型
│   │   ├── pages/            # login / register / overview / users / channels / accounts / settings / portal
│   │   ├── components/       # 布局、受保护路由、通用表格封装、主题切换
│   │   ├── stores/           # 轻量状态（当前登录态：username / role / external_id）
│   │   ├── hooks/
│   │   └── main.tsx
│   ├── package.json
│   ├── vite.config.ts        # dev 代理 /auth /admin /me /v1 -> localhost:8080
│   └── tsconfig.json
```

### 部署形态：嵌入式单二进制

- **开发期**：`vite dev` 起前端（默认 5173），通过 Vite proxy 把 `/auth`、`/admin`、`/me`、`/v1` 代理到 Go 后端（8080），前端热更新。
- **生产期**：`npm run build` → 产物输出到 `internal/server/web_dist/` → Go 用 `//go:embed web_dist` 打进二进制 → `console.go` 挂 `GET /console/*` 伺服静态资源，带 **SPA fallback**（非静态资源路径一律回 `index.html`，交给前端路由）。最终仍是一个 `linapi` 二进制，访问 `http://<host>:8080/console` 打开控制台。

### 后端改动边界

- **新增文件**：`internal/server/console.go`（embed + `/console` 伺服）。
- **认证层新增**（见第 3~5 节）：`internal/account`（账户/设置领域 + store）、`internal/middleware` 增 `SessionAuth` / `RequireRole`、`/auth` 与 `/me` 路由分组。
- **改动现有**：`/admin/*` 鉴权从裸 token 改为 `SessionAuth + RequireRole("admin")`；`AdminAuth` 中间件及 `admin.token` / `admin.loopback_only` 配置项退役（见第 4 节）。

### 安全边界（贯穿全文的硬约束）

- `/console` 伺服的是静态资源，**本身不做鉴权**（标准 SPA 做法）。
- **真正的安全边界始终在后端 API 层**：`/auth` 之外的端点由 `SessionAuth` 守护，管理端点额外由 `RequireRole("admin")` 守护。
- **前端按角色分流、按角色渲染导航只是 UX，不是安全边界**。普通用户即使手改前端路由跳到管理页，后端 `/admin/*` 的角色守护也会返回 403。此约束不可协商。

---

## 2. 认证模型总览

三套鉴权并存，各司其职：

| 鉴权 | 服务对象 | 端点 | 机制 |
|------|----------|------|------|
| **会话（session）** | 人（管理员 / 用户） | `/auth/logout`、`/auth/me`、`/admin/*`、`/me/*` | Redis 不透明 session token + HttpOnly Cookie |
| **API key** | 程序 | `/v1/*` | 现有，不变 |
| ~~裸 admin token~~ | ~~自动化~~ | ~~`/admin/*`~~ | **本期废弃**（见第 4 节） |

- **人用账密 + session**：可主动吊销、带角色、TTL 到期自动登出。
- **程序用 API key**：无状态、长期有效。
- 两者本就该分开，也对齐 New API 的做法。

**角色**：仅 `admin` 与 `user` 两种。

- `admin`：可访问 `/admin/*` 全部管理端点；不自动关联计费实体（不消耗额度）。
- `user`：可访问 `/me/*` 自助端点；账户创建时**自动绑定一个计费 User**（额度容器），可自助建 key 消耗自身额度。

---

## 3. 后端认证层：数据模型与会话

### 3.1 accounts 表（新增）

登录账户与现有计费 `User` 实体**职责分离**：`users` 表是计费实体（余额/额度归属），`accounts` 表是登录账户（用户名/密码/角色）。一个 user 账户软关联一个计费 User。

> **约定**：schema 需**同步两份**——`db/schema.sql`（sqlc 源）与 `internal/db/schema.sql`（运行时 embed 的迁移副本）。沿用现有风格：`GENERATED ALWAYS AS IDENTITY`、`IF NOT EXISTS`、`timestamptz`、中文注释。

```sql
-- 登录账户：控制台的鉴权主体（与计费实体 users 职责分离）。
CREATE TABLE IF NOT EXISTS accounts (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- username 登录名，全局唯一。
    username      TEXT        NOT NULL UNIQUE,
    -- password_hash 存 bcrypt 哈希，绝不落明文，绝不用快哈希（MD5/SHA）。
    password_hash TEXT        NOT NULL,
    -- role 仅 'admin' | 'user'。
    role          TEXT        NOT NULL,
    -- external_id 软关联 users.external_id：user 角色必填（额度容器），admin 可空。
    external_id   TEXT,
    -- group_name 预留：定价分组名，本期存而不用（见 3.7）。
    group_name    TEXT        NOT NULL DEFAULT 'default',
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_accounts_role ON accounts (role);
```

### 3.2 settings 表（新增，运行时可变系统设置）

「是否开放注册」等需在控制台改、且**立即生效不重启**的开关，不能只放 config（config 改了要重启）。用一张轻量 KV 表承载：

```sql
-- 系统设置：运行时可变的 KV 配置，控制台可改、即时生效。
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

初始键（首次启动播种，缺失时用默认值）：

| key | 类型 | 默认 | 说明 |
|-----|------|------|------|
| `registration_enabled` | bool | **false** | 安全默认：开放前须管理员显式打开 |
| `new_user_initial_balance` | int64 | **0** | 新注册用户初始赠送额度（最小计费单位） |

### 3.3 领域层与存储

- 新增 `internal/account` 包：`Account` 领域模型、`Settings` 读写、`AccountStore` 接口。
- **双实现**：PostgreSQL（`database.enabled=true`）+ 内存（免 DB 本地开发），与现有 `store` / `admin` 的双实现模式一致。
- 编译期断言两套实现都满足接口。

### 3.4 密码哈希

- `golang.org/x/crypto/bcrypt`，cost 默认（10）。
- 注册 / 改密时 `GenerateFromPassword`；登录时 `CompareHashAndPassword`。
- **绝不存明文，绝不用 MD5/SHA 等快哈希。**

### 3.5 会话机制

- 登录成功 → 生成随机 session ID（32 字节 hex）→ Redis 存 `session:<id>`，value 为 `{account_id, username, role, external_id}`。
- **TTL**：默认 **24h**；勾选「记住我」延长至 **7 天**（Cookie `Max-Age` 同步）。
- session ID 通过 **HttpOnly + Secure + SameSite=Strict Cookie** 下发，**不放 localStorage / sessionStorage**——HttpOnly 让前端 JS 读不到，天然免疫 XSS 窃取。
- 登出 → 删 Redis 键 + 清 Cookie。TTL 到期自动登出。
- **Redis 不可用时登录失败（fail-closed）**，绝不降级为无鉴权。

### 3.6 CSRF 防护

- HttpOnly Cookie 会被浏览器自动带上，理论上需防 CSRF。
- 但本期是**同源嵌入式部署**（`/console` 与 API 同域）+ **SameSite=Strict**，跨站请求根本带不上 Cookie，已基本免疫 CSRF。
- 因此**不额外加 CSRF token**（在同源部署下属过度设计）。此结论写明于此，将来若改为跨域分离部署需重新评估。

### 3.7 分组 / 倍率预留字段（本期存而不用）

**背景**：现有计费 `Cost(model, input, output)` 纯按模型名查单价算成本，**不感知用户**。将来要做的 New API 式分组 / 定价倍率，本质是在基础成本上乘一个倍率：`cost = baseCost × 用户倍率`，倍率来源两处——「用户所属分组」和「单用户特殊覆盖」。

**决策**：本期**不实现任何分组 / 倍率逻辑**，但**预留两个数据列**（加列是改表痛点，值得提前；计费逻辑一律不碰——计费是钱，不为未启用的功能改动）：

| 表 | 列 | 类型 | 默认 | 含义（本期不读） |
|----|-----|------|------|------------------|
| `accounts` | `group_name` | TEXT | `'default'` | 用户所属定价分组名 |
| `users` | `rate_multiplier` | INT | `100` | 单用户倍率覆盖，百分比整数（100 = 1.00x，避免浮点） |

**边界（防止实现时误做半个功能）**：
- ✅ 本期做：`accounts`、`users` 两表加上述列（含内存实现的对应字段），带默认值，落库即可。
- ❌ 本期**不做**：分组表 `groups`、分组 CRUD、分组管理页、把倍率接进 `Cost` / `Settle`、任何倍率计算。
- 将来落地分组时：数据列已在，只需加 `groups` 表 + 在 `Cost` / `Settle` 接倍率 + 管理页，无需改动 `accounts` / `users` 表结构。

---

## 4. 认证端点、中间件与 bootstrap

### 4.1 /auth 分组（登录态管理）

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/auth/register` | 无（受 `registration_enabled` 开关） | `{username, password}` → 校验唯一 + 密码强度 → 建 user 账户 + 计费实体 → 初始余额取 `new_user_initial_balance`。开关关闭时返回 403 |
| POST | `/auth/login` | 无 | `{username, password, remember?}` → 校验 → 建 session → 下发 HttpOnly Cookie。返回 `{username, role}`（供前端分流） |
| POST | `/auth/logout` | session | 删 Redis session + 清 Cookie |
| GET | `/auth/me` | session | 返回 `{username, role, external_id}`。前端刷新 / 直接进入时用它恢复会话（Cookie 在但前端内存态已丢） |

> `/auth/me` 是关键一环：HttpOnly Cookie 前端 JS 读不到，前端无法自行判断登录态，必须靠 `/auth/me` 探活——200 则已登录（顺带拿角色决定去向），401 则跳登录页。

### 4.2 新增中间件（`internal/middleware`）

- **`SessionAuth`**：从 Cookie 取 session ID → 查 Redis → 有效则把 `{account, role, external_id}` 注入 `gin.Context`，无效返回 **401**。挂在所有需登录的端点。
- **`RequireRole("admin")`**：在 `SessionAuth` 之后校验 context 中的角色，不符返回 **403**。挂在管理端点。

### 4.3 路由分组最终形态

```
/auth/register           # 无鉴权（受 registration_enabled 开关）
/auth/login              # 无鉴权
/auth/logout, /auth/me   # SessionAuth
/admin/*                 # SessionAuth + RequireRole("admin")   ← 由裸 token 改为会话+角色
/me/*                    # SessionAuth（user 自助）
/v1/*                    # 现有 API key 鉴权（不变）
/console/*               # 静态资源（无鉴权）
/healthz, /metrics       # 无鉴权（不变）
```

### 4.4 废弃裸 admin token

- `/admin/*` 由「裸 token 鉴权」改为「`SessionAuth` + `RequireRole("admin")`」。
- `middleware.AdminAuth` 中间件退役；`config` 中 `admin.token`、`admin.loopback_only` 配置项一并清理。
- `admin.enabled` 开关**保留**，改为控制「是否挂载控制台与认证端点」（关闭时不伺服 `/console`、不挂 `/auth` `/admin` `/me`，维持最小暴露面）。
- **影响**：任何已依赖裸 token 直调 `/admin` 的脚本 / 工具将失效，需改用会话登录。此为已确认的取舍（换取单一、干净的鉴权模型）。

### 4.5 bootstrap 首个管理员

账密模式的「先有鸡」问题：全新部署时没有任何账户，谁来登录建账户？靠启动时从 config 播种。

```yaml
admin:
  enabled: true
  bootstrap:
    username: "admin"
    # 建议用环境变量注入：LINAPI_ADMIN_BOOTSTRAP_PASSWORD
    password: ""
```

- 启动时若 `bootstrap.username` 不存在 → bcrypt 哈希后建为 **admin 账户**；已存在则跳过（幂等，不覆盖已改的密码）。
- **空密码则不播种并告警**（绝不建无密码管理员）。

---

## 5. 用户自助与计费绑定

### 5.1 建 user 账户即自动建计费实体

对齐 New API「注册即有额度容器」：

- 无论**自助注册**还是 **admin 创建 user 账户**，都自动创建一个计费 `User` 并回填 `external_id`（用 username 或生成的 ID）。
  - **自助注册**：初始余额取 `new_user_initial_balance`。
  - **admin 创建**：admin 可在创建表单显式指定初始余额（缺省时取 `new_user_initial_balance`）。
- 用户登录即有额度容器，可立刻建 key。
- **admin 账户不自动建计费实体**（不消耗额度，`external_id` 可空）。
- **`external_id` 由后端自动生成 / 回填，非前端手填**：建 user 账户是「建账户 + 自动建计费实体 + 回填关联」的原子动作，前端不提供 external_id 输入框。
- 建账户 + 建计费实体需保证一致性：任一步失败则整体失败、不留孤儿账户（DB 模式用事务；内存模式顺序写 + 失败回滚）。

### 5.2 开放注册 + 管理员开关

- `POST /auth/register` 受 `settings.registration_enabled` 控制：关闭时返回 403，登录页也不显示注册入口。
- 默认**关闭**：开放前须管理员在系统设置页显式打开。
- 注册校验：用户名唯一 + 密码强度（最小长度等，见 5.4）。

### 5.3 /me 自助端点（user 角色）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/me/profile` | 自己的账户信息 + 关联计费 User 余额 |
| GET | `/me/keys` | 自己名下密钥列表（脱敏，不回显明文） |
| POST | `/me/keys` | 自助创建密钥，明文仅回显一次 |
| PATCH | `/me/keys/:keyid/enabled` | 启停自己的密钥 |
| DELETE | `/me/keys/:keyid` | 删除自己的密钥 |

### 5.4 越权硬约束（不可协商，写进实现与测试）

- **绑定用户强制取自 session 的 `external_id`**：`POST /me/keys` **完全忽略**前端传入的任何 `user_id` / `external_id`，绑定对象只认 session。否则用户能给别人建 key、蹭别人额度。
- **操作前校验归属**：`/me/keys/:keyid` 的启停 / 删除，先校验 `key.user_id == session.external_id`，不属于则返回 **404**（不泄露他人 key 是否存在）。
- **额度账户级统一扣**：user 建的 key 消耗都记到其账户余额，本期不做 key 级独立额度上限。
- **密码强度**：注册 / 改密最小长度（建议 ≥ 8），前端 + 后端双重校验。

---

## 6. 前端页面全景与 UI 质量规范

### 6.1 页面清单

**公共**
- `/console/login` — 统一登录页（用户名 / 密码 + 「记住我」）。登录后按角色跳转：admin → `/console/overview`，user → `/console/portal`。`registration_enabled` 为真时显示注册入口。
- `/console/register` — 注册页（用户名 / 密码），仅在开放注册时可达。

**管理后台（admin 角色）**
- `/console/overview` — 概览首页：汇总卡片（用户数 / 渠道数 / 启用模型数 / 账户数），从现有列表 API 聚合，不接 `/metrics`。
- `/console/users` — 用户 + 密钥管理：用户表（列表 / 分页 / 创建 / 启停 / 充值），抽屉或展开区管该用户密钥（生成 / 列表 / 启停，明文弹窗仅显示一次）。
- `/console/channels` — 渠道管理：渠道表（列表 / 创建 / 编辑 / 删除 / 启停，含 format / base_url / 优先级 / 权重 / 模型映射）。
- `/console/accounts` — 账户管理：管理登录账户（创建 admin / user 账户、改密、启停）。创建 user 账户时后端自动建并绑定计费实体（见 5.1），admin 可指定初始余额；表单不含 external_id 手填项。认证体系必需的管理面。
- `/console/settings` — 系统设置：`registration_enabled` 开关 + `new_user_initial_balance`（即时生效）。

**用户门户（user 角色）**
- `/console/portal` — 我的概览（余额 / 账户信息）。
- `/console/portal/keys` — 我的密钥（可建 / 启停 / 删，明文仅回显一次）。

**导航**：侧边栏按角色渲染——admin 见概览 / 用户 / 渠道 / 账户 / 设置；user 见我的概览 / 我的密钥。（再次强调：仅 UX，后端授权才是边界。）

### 6.2 主题与视觉基调

- 基于 Semi Design Token 定制，**不裸用默认皮肤**（默认皮肤 = 平庸的关键）。定一套主色（中性偏冷，避开 AntD 经典蓝的「满大街感」）+ 统一圆角、阴影层级。
- **暗色模式一等公民**：Semi 原生 Token 切换，跟随系统 + 手动切换，选择持久化。
- 排版四级层级（页面标题 / 区块标题 / 正文 / 辅助文字）字号字重固定成规范，全站一致。

### 6.3 布局骨架

- 经典后台三段：顶栏（logo / 环境标识 / 用户菜单 / 主题切换）+ 侧边导航（按角色渲染）+ 内容区。
- 内容区统一容器：最大宽度约束、一致的页面内边距节奏。

### 6.4 高质量四硬指标（验收项）

1. **三态齐全**：每个数据视图必须有**加载态**（骨架屏 / spin）、**空态**（有插画 / 引导文案，非白屏）、**错误态**（可重试）。缺一不算完成。
2. **反馈即时**：所有写操作（创建 / 删除 / 启停 / 充值）有 loading 按钮态 + 成功 / 失败 Toast；危险操作（删渠道、禁用账户）二次确认弹窗。
3. **表单校验前置**：提交前客户端校验（必填 / 格式 / 范围），错误就地红字，不靠后端 400 兜底。
4. **响应式可用**：桌面优先，侧边栏可收起、表格可横向滚动，窄屏不塌。

### 6.5 表格与敏感信息规范

- 统一封装一个表格组件：分页、加载态、空态、工具栏（搜索 / 新建）、行操作（编辑 / 启停 / 删）成型式复用，不重复造。
- 密钥列表只显示前缀 + 掩码（`sk-xxxx…`）；渠道 API key 永不显示（后端已脱敏）。

### 6.6 关键交互：密钥明文仅回显一次

- 创建密钥成功 → 弹窗大字显示完整明文 + 一键复制 + 明确警告「仅此一次，关闭后无法再查看」。关闭后列表只见掩码。
- admin 代建与 user 自建交互一致。

### 6.7 错误与边界的诚实处理

- 网络错误、401（session 失效 → 统一拦截 → 清态 → 踢回登录页）、403（越权 → 友好「无权限」提示而非白屏）、500，都有用户可读提示，不把原始错误甩给用户。

---

## 7. 完整端点汇总

### 新增

| 方法 | 路径 | 鉴权 | 角色 |
|------|------|------|------|
| POST | `/auth/register` | 无（受开关） | — |
| POST | `/auth/login` | 无 | — |
| POST | `/auth/logout` | session | any |
| GET | `/auth/me` | session | any |
| GET | `/me/profile` | session | user |
| GET | `/me/keys` | session | user |
| POST | `/me/keys` | session | user |
| PATCH | `/me/keys/:keyid/enabled` | session | user |
| DELETE | `/me/keys/:keyid` | session | user |
| GET | `/admin/accounts` | session | admin |
| POST | `/admin/accounts` | session | admin |
| PATCH | `/admin/accounts/:id/enabled` | session | admin |
| POST | `/admin/accounts/:id/password` | session | admin |
| GET | `/admin/settings` | session | admin |
| PUT | `/admin/settings` | session | admin |
| GET | `/console/*` | 无（静态） | — |

### 鉴权变更

| 路径 | 原 | 新 |
|------|----|----|
| `/admin/*`（现有 CRUD） | 裸 admin token | `SessionAuth` + `RequireRole("admin")` |

### 不变

`/v1/*`（API key）、`/healthz`、`/metrics`、现有 `/admin` 的用户 / 密钥 / 渠道 CRUD 处理逻辑（仅鉴权层变）。

---

## 8. 验收标准

**后端**
- [ ] `accounts` / `settings` 两表在 `db/schema.sql` 与 `internal/db/schema.sql` 两份同步；DB 模式幂等建表通过。
- [ ] 预留列落地：`accounts.group_name`（默认 `'default'`）、`users.rate_multiplier`（默认 `100`）两列在两份 schema 同步 + 内存实现对应字段；**不接入任何计费 / 分组逻辑**（见 3.7）。
- [ ] `internal/account` 双实现（PG + 内存）均满足接口，编译期断言。
- [ ] 密码 bcrypt 哈希，无任何明文 / 快哈希路径。
- [ ] session 存 Redis，HttpOnly + Secure + SameSite=Strict Cookie；TTL 24h / 记住我 7d；Redis 不可用 fail-closed。
- [ ] `SessionAuth` / `RequireRole` 中间件；`/admin/*` 改会话鉴权，裸 token 与相关 config 已清理。
- [ ] bootstrap 幂等建管理员；空密码不播种并告警。
- [ ] 建 user 账户自动建并绑定计费实体；失败不留孤儿。
- [ ] 越权硬约束有单测覆盖：建 key 只认 session、操作他人 key 返回 404、注册开关 403。
- [ ] `CGO_ENABLED=1 go test -race ./...` 全绿。

**前端**
- [ ] 统一登录页按角色分流；`/auth/me` 恢复会话；401 统一踢回登录。
- [ ] admin 五页 + user 两页齐全，导航按角色渲染。
- [ ] 六节四硬指标（三态 / 反馈 / 校验 / 响应式）逐页满足。
- [ ] 密钥明文仅回显一次交互正确；掩码显示。
- [ ] 暗色模式可用且持久化。
- [ ] `npm run build` 产物 embed 进二进制，`/console` 可访问、SPA fallback 正确。

**文档与记忆**（遵守 CLAUDE.md 维护约定）
- [ ] `docs/progress.md`（顶部日期 + 进度）、`docs/modules.md`、`docs/architecture.md`、`CLAUDE.md` 同步。
- [ ] 持久记忆（`linapi-progress` + `MEMORY.md`）更新。

---

## 9. 待实现计划（下一步）

本设计定稿后转入 writing-plans，产出分阶段实现计划。建议阶段划分：

1. **后端认证地基**：`accounts` / `settings` 表（含预留列 `accounts.group_name`；并给现有 `users` 表加 `rate_multiplier`）+ `internal/account` 双实现 + bcrypt + Redis session + `SessionAuth` / `RequireRole` + bootstrap（含单测）。
2. **认证端点**：`/auth/*`、`/me/*`、`/admin/accounts`、`/admin/settings`；`/admin/*` 切换会话鉴权、清理裸 token（含单测，重点覆盖越权硬约束）。
3. **前端工程搭建**：Vite + React + TS + Semi 脚手架、API 客户端 + 类型、布局 + 受保护路由 + 主题、`console.go` embed。
4. **前端页面**：登录 / 注册 → admin 五页 → user 两页，逐页落实四硬指标。
5. **收尾**：端到端验证、`-race`、文档与记忆同步。
