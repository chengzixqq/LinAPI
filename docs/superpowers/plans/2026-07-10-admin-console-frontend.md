# LinAPI 管理控制台 — 前端实现计划（Plan 2/2）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**前置依赖：** 本计划依赖 Plan 1（后端认证层）已完成——`/auth`、`/me`、`/admin/accounts`、`/admin/settings` 端点可用，`/admin/*` 已切换会话鉴权。

**Goal:** 构建 React + Vite + TS + Semi Design 控制台：统一登录页按角色分流，admin 五页 + user 两页，嵌入式单二进制交付（`//go:embed` + `/console` SPA 伺服）。

**Architecture:** 前端源码在 `web/`，`npm run build` 产物输出到 `internal/server/web_dist/`，Go 用 `//go:embed` 打进二进制，`console.go` 挂 `/console/*` 伺服静态资源并带 SPA fallback。开发期用 Vite proxy 把 `/auth /admin /me /v1` 代理到 8080。会话靠 HttpOnly Cookie，前端用 `/auth/me` 探活恢复登录态。

**Tech Stack:** React 18、Vite、TypeScript、`@douyinfe/semi-ui`、react-router-dom v6。

## Global Constraints

值逐字取自 spec [docs/superpowers/specs/admin-console.md](../specs/admin-console.md)：

- **安全边界在后端**：前端按角色渲染导航/分流仅是 UX，非安全边界。不得因「前端已隐藏」而假设后端无需守护。
- **会话恢复**：HttpOnly Cookie 前端 JS 读不到，登录态一律靠 `GET /auth/me`（200=已登录+拿角色，401=跳登录页）。不把 token 存 localStorage/sessionStorage。
- **密钥明文仅回显一次**：创建成功弹窗大字显示完整明文 + 一键复制 + 「仅此一次」警告；列表只显示掩码（`sk-xxxx…`）；渠道 API key 永不显示。
- **越权由后端保证**：`/me/keys` 不传 user_id/external_id（后端强制取会话）。
- **四硬指标（每个数据页逐条验收）**：① 三态齐全（加载/空/错误，缺一不可）；② 写操作有 loading 态 + 成功/失败 Toast，危险操作二次确认；③ 表单提交前客户端校验、就地红字；④ 响应式（侧边栏可收起、表格可横向滚动、窄屏不塌）。
- **主题**：基于 Semi Token 定制，不裸用默认皮肤；暗色模式一等公民，跟随系统 + 手动切换 + 持久化。
- **中文优先**：文案中文，但不散落写死（集中放常量/组件），为将来 i18n 留余地。
- **密码强度**：注册/改密最小长度 ≥ 8，前端提交前校验（后端另有校验）。

---

## 文件结构

**前端新增（`web/`）：**
- `web/package.json`、`web/vite.config.ts`、`web/tsconfig.json`、`web/index.html`、`web/.gitignore`
- `web/src/main.tsx` — 入口，挂 router + 主题 Provider。
- `web/src/App.tsx` — 路由表（公共 / admin / user 分区 + ProtectedRoute）。
- `web/src/api/client.ts` — fetch 封装（`credentials: 'include'`、401 统一拦截、错误归一）。
- `web/src/api/types.ts` — 后端响应的 TS 类型。
- `web/src/api/endpoints.ts` — 按域封装 auth/admin/me 的调用函数。
- `web/src/stores/auth.tsx` — 登录态 Context（username/role/external_id + refresh/logout）。
- `web/src/theme/tokens.ts` + `web/src/theme/ThemeProvider.tsx` — 主色 Token + 明暗切换持久化。
- `web/src/components/Layout.tsx` — 顶栏 + 侧边导航（按角色渲染）+ 内容容器。
- `web/src/components/ProtectedRoute.tsx` — 会话/角色守护路由。
- `web/src/components/DataTable.tsx` — 复用表格（分页/三态/工具栏/行操作）。
- `web/src/components/PlaintextKeyModal.tsx` — 密钥明文一次性弹窗。
- `web/src/components/ConfirmButton.tsx` — 危险操作二次确认封装。
- `web/src/hooks/useAsyncData.ts` — 列表加载三态 hook。
- `web/src/pages/Login.tsx`、`Register.tsx`
- `web/src/pages/admin/Overview.tsx`、`Users.tsx`、`Channels.tsx`、`Accounts.tsx`、`Settings.tsx`
- `web/src/pages/portal/PortalHome.tsx`、`PortalKeys.tsx`
- `web/src/text.ts` — 集中中文文案常量。

**后端新增/修改：**
- `internal/server/console.go` — `//go:embed web_dist` + `/console/*` 伺服 + SPA fallback。
- `internal/server/web_dist/` — 前端 build 产物目标目录（含 `.gitkeep`）。
- `internal/server/server.go` — `registerRoutes` 调 `s.registerConsole()`。
- `.gitignore` — 忽略 `web/node_modules`、`web/dist`。

**关于本计划的代码完整度**：脚手架、配置、API 层、共享组件（DataTable / ProtectedRoute / Layout / 主题 / 弹窗）给完整代码——这些是「一次做错处处踩坑」的载重件。页面部分：登录页与「用户管理」页给完整代码作为规范样板；其余 CRUD 页（渠道/账户/门户）复用同一批共享组件，逐页给出其**列定义、调用的端点、表单字段、校验规则、四硬指标落点**的完整规格（不逐页重贴近乎雷同的 300 行 JSX——那是 DRY 噪声而非清晰）。每页仍是独立可测交付。

---

### Task 1: 前端脚手架（Vite + React + TS + Semi + router）

**Files:**
- Create: `web/package.json`、`web/vite.config.ts`、`web/tsconfig.json`、`web/tsconfig.node.json`、`web/index.html`、`web/.gitignore`、`web/src/main.tsx`、`web/src/App.tsx`、`web/src/vite-env.d.ts`
- Modify: 仓库根 `.gitignore`

**Interfaces:**
- Produces: 可 `npm run dev` / `npm run build` 的前端工程；`App.tsx` 暴露最简路由（后续任务填充）。

- [ ] **Step 1: 创建 `web/package.json`**

```json
{
  "name": "linapi-console",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview",
    "typecheck": "tsc -b --noEmit"
  },
  "dependencies": {
    "@douyinfe/semi-ui": "^2.60.0",
    "react": "^18.3.1",
    "react-dom": "^18.3.1",
    "react-router-dom": "^6.26.0"
  },
  "devDependencies": {
    "@types/react": "^18.3.3",
    "@types/react-dom": "^18.3.0",
    "@vitejs/plugin-react": "^4.3.1",
    "typescript": "^5.5.4",
    "vite": "^5.4.0"
  }
}
```

- [ ] **Step 2: 创建 `web/vite.config.ts`**

`base: '/console/'` 让产物资源路径匹配后端伺服前缀；dev proxy 把 API 打到 8080。

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// base=/console/ 与后端 console.go 的伺服前缀一致；
// 生产产物输出到 internal/server/web_dist 供 go:embed 打包。
export default defineConfig({
  plugins: [react()],
  base: '/console/',
  build: {
    outDir: '../internal/server/web_dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/auth': 'http://localhost:8080',
      '/admin': 'http://localhost:8080',
      '/me': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
    },
  },
})
```

- [ ] **Step 3: 创建 `web/tsconfig.json` 与 `web/tsconfig.node.json`**

`tsconfig.json`：

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true
  },
  "include": ["src"],
  "references": [{ "path": "./tsconfig.node.json" }]
}
```

`tsconfig.node.json`：

```json
{
  "compilerOptions": {
    "composite": true,
    "skipLibCheck": true,
    "module": "ESNext",
    "moduleResolution": "bundler",
    "allowSyntheticDefaultImports": true,
    "strict": true
  },
  "include": ["vite.config.ts"]
}
```

- [ ] **Step 4: 创建 `web/index.html`**

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>LinAPI 控制台</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 5: 创建 `web/src/vite-env.d.ts`**

```ts
/// <reference types="vite/client" />
```

- [ ] **Step 6: 创建 `web/src/main.tsx`（最简，后续任务扩展）**

```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter basename="/console">
      <App />
    </BrowserRouter>
  </React.StrictMode>,
)
```

- [ ] **Step 7: 创建 `web/src/App.tsx`（占位路由，Task 8 填充）**

```tsx
import { Routes, Route } from 'react-router-dom'

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<div>LinAPI 控制台脚手架就绪</div>} />
    </Routes>
  )
}
```

- [ ] **Step 8: 创建 `web/.gitignore`**

```
node_modules
dist
```

- [ ] **Step 9: 仓库根 `.gitignore` 追加**

```
# 前端产物（由 npm run build 生成后 embed）
web/node_modules
internal/server/web_dist/*
!internal/server/web_dist/.gitkeep
```

- [ ] **Step 10: 安装依赖并验证 dev/build**

Run: `cd web && npm install && npm run build`
Expected: 依赖安装成功；`tsc -b` 无类型错误；`vite build` 产物输出到 `internal/server/web_dist/`（含 `index.html` + `assets/`）。

- [ ] **Step 11: Commit**

```bash
git add web/ .gitignore
git commit -m "feat(web): Vite+React+TS+Semi 前端脚手架"
```

---

### Task 2: 后端 embed 伺服（console.go + SPA fallback）

**Files:**
- Create: `internal/server/console.go`
- Create: `internal/server/web_dist/.gitkeep`
- Modify: `internal/server/server.go`（`registerRoutes` 调用 `registerConsole`）

**Interfaces:**
- Consumes: Task 1 的 build 产物目录 `internal/server/web_dist/`。
- Produces: `GET /console` 与 `/console/*` 伺服 SPA；`(s *Server) registerConsole()`。

**说明**：`web_dist` 必须始终含至少一个文件（`.gitkeep`）否则 `//go:embed` 编译失败。build 产物覆盖进来后即为真实 SPA。

- [ ] **Step 1: 创建占位 `internal/server/web_dist/.gitkeep`（空文件）**

保证 embed 目录非空、可编译（真实产物由 `npm run build` 覆盖）。

- [ ] **Step 2: 创建 `internal/server/console.go`**

```go
package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:web_dist
var webDist embed.FS

// registerConsole 挂载控制台静态资源伺服（/console/*）+ SPA fallback。
// 仅 admin.enabled=true 时挂载（与认证端点同开关，维持最小暴露面）。
//
// SPA fallback：命中真实静态文件则直出；否则（前端路由路径，如 /console/users）
// 一律回 index.html，交给前端 react-router 处理。控制台本身不做鉴权
// （标准 SPA 做法），安全边界在后端 API 层。
func (s *Server) registerConsole() {
	if !s.cfg.Admin.Enabled {
		return
	}
	// 剥掉 web_dist 前缀，让文件系统根对齐 /console/。
	sub, err := fs.Sub(webDist, "web_dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(sub))

	handler := func(c *gin.Context) {
		// c.Param("filepath") 形如 "/assets/index-xxx.js" 或 "/users"。
		reqPath := strings.TrimPrefix(c.Param("filepath"), "/")
		if reqPath == "" {
			reqPath = "index.html"
		}
		// 存在则直出静态资源；否则回退 index.html（SPA 路由）。
		if _, err := fs.Stat(sub, reqPath); err != nil {
			c.Request.URL.Path = "/"
			serveIndex(c, sub)
			return
		}
		c.Request.URL.Path = "/" + reqPath
		fileServer.ServeHTTP(c.Writer, c.Request)
	}

	s.engine.GET("/console", func(c *gin.Context) { serveIndex(c, sub) })
	s.engine.GET("/console/*filepath", handler)
}

// serveIndex 直出 index.html（SPA 入口）。
func serveIndex(c *gin.Context, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		c.String(http.StatusNotFound, "控制台未构建：请先在 web/ 执行 npm run build")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}
```

- [ ] **Step 3: `internal/server/server.go` 的 `registerRoutes` 末尾调用**

在 `s.registerAdminRoutes()` 之后加：

```go
	s.registerConsole()
```

- [ ] **Step 4: 编译验证**

Run: `go build ./internal/server/`
Expected: 编译通过（embed 目录非空）。

- [ ] **Step 5: 端到端验证 SPA fallback**

先 `cd web && npm run build`（产物进 web_dist），再启后端（内存模式 + admin.enabled=true），
Run: `curl -s http://localhost:8080/console/ | grep -o '<div id="root">'` 与 `curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/console/users`
Expected: 首页返回含 `#root` 的 HTML；`/console/users`（前端路由，非真实文件）返回 200（fallback 到 index.html）。

- [ ] **Step 6: Commit**

```bash
git add internal/server/console.go internal/server/web_dist/.gitkeep internal/server/server.go
git commit -m "feat(server): 控制台静态资源 embed 伺服 + SPA fallback"
```

---

### Task 3: API 客户端 + TS 类型 + 端点封装

**Files:**
- Create: `web/src/api/client.ts`、`web/src/api/types.ts`、`web/src/api/endpoints.ts`

**Interfaces:**
- Produces（供所有页面消费）：
  - `apiFetch<T>(path, init?): Promise<T>`（自动带 Cookie、解析错误、401 抛 `UnauthorizedError`）
  - `class ApiError extends Error { status: number }`、`class UnauthorizedError extends ApiError`
  - 类型：`MeInfo`、`Account`、`Settings`、`User`、`APIKey`、`Channel`、`CreatedKey`
  - `api.auth.{login,register,logout,me}`、`api.admin.{...}`、`api.me.{...}`

- [ ] **Step 1: 创建 `web/src/api/client.ts`**

```ts
// 统一 fetch 封装：始终带同源 Cookie（会话），把非 2xx 归一为 ApiError，
// 401 特化为 UnauthorizedError 供全局拦截（清登录态、踢回登录页）。

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
}

export class UnauthorizedError extends ApiError {
  constructor(message: string) {
    super(401, message)
    this.name = 'UnauthorizedError'
  }
}

// 401 全局订阅：auth store 注册回调，收到即清态跳登录。
type UnauthorizedHandler = () => void
let onUnauthorized: UnauthorizedHandler | null = null
export function setUnauthorizedHandler(fn: UnauthorizedHandler) {
  onUnauthorized = fn
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, {
    ...init,
    credentials: 'include', // 带 HttpOnly 会话 Cookie。
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
  })

  if (resp.status === 401) {
    if (onUnauthorized) onUnauthorized()
    throw new UnauthorizedError('会话已失效，请重新登录')
  }

  if (!resp.ok) {
    // 后端错误结构：{ error: { message, type } }。
    let message = `请求失败（${resp.status}）`
    try {
      const body = await resp.json()
      if (body?.error?.message) message = body.error.message
    } catch {
      /* 忽略解析失败，用兜底文案 */
    }
    throw new ApiError(resp.status, message)
  }

  if (resp.status === 204) return undefined as T
  return (await resp.json()) as T
}
```

- [ ] **Step 2: 创建 `web/src/api/types.ts`**

```ts
// 与后端响应结构对齐的 TS 类型。金额为最小计费单位（整数）。

export interface MeInfo {
  username: string
  role: 'admin' | 'user'
  external_id: string
}

export interface Account {
  id: number
  username: string
  role: 'admin' | 'user'
  external_id?: string
  group_name: string
  enabled: boolean
  created_at: string
}

export interface Settings {
  registration_enabled: boolean
  new_user_initial_balance: number
}

export interface User {
  external_id: string
  balance: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface APIKey {
  key_id: string
  user_id: string
  rate_limit_per_min: number
  allowed_models: string[]
  enabled: boolean
  created_at: string
}

export interface Channel {
  channel_id: string
  name: string
  format: 'openai' | 'anthropic'
  base_url: string
  models: Record<string, string>
  priority: number
  weight: number
  enabled: boolean
  created_at: string
  updated_at: string
}

// 创建密钥的响应：唯一一次带明文 api_key。
export interface CreatedKey extends APIKey {
  api_key: string
}

// 列表端点统一包在 { data: [...] } 里。
export interface ListResp<T> {
  data: T[]
}
```

- [ ] **Step 3: 创建 `web/src/api/endpoints.ts`**

```ts
import { apiFetch } from './client'
import type {
  Account, APIKey, Channel, CreatedKey, ListResp, MeInfo, Settings, User,
} from './types'

// 按域聚合的端点封装。所有写操作走 JSON body。
export const api = {
  auth: {
    login: (username: string, password: string, remember: boolean) =>
      apiFetch<{ username: string; role: string }>('/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password, remember }),
      }),
    register: (username: string, password: string) =>
      apiFetch<{ username: string; role: string }>('/auth/register', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      }),
    logout: () => apiFetch<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
    me: () => apiFetch<MeInfo>('/auth/me'),
  },
  admin: {
    listUsers: (limit = 100, offset = 0) =>
      apiFetch<ListResp<User>>(`/admin/users?limit=${limit}&offset=${offset}`),
    createUser: (external_id: string, balance: number) =>
      apiFetch<User>('/admin/users', {
        method: 'POST',
        body: JSON.stringify({ external_id, balance }),
      }),
    setUserEnabled: (id: string, enabled: boolean) =>
      apiFetch<User>(`/admin/users/${encodeURIComponent(id)}/enabled`, {
        method: 'PATCH',
        body: JSON.stringify({ enabled }),
      }),
    addBalance: (id: string, delta: number) =>
      apiFetch<{ external_id: string; balance: number }>(
        `/admin/users/${encodeURIComponent(id)}/balance`,
        { method: 'POST', body: JSON.stringify({ delta }) },
      ),
    listKeys: (userId: string) =>
      apiFetch<ListResp<APIKey>>(`/admin/users/${encodeURIComponent(userId)}/keys`),
    createKey: (userId: string, rate_limit_per_min: number, allowed_models: string[]) =>
      apiFetch<CreatedKey>(`/admin/users/${encodeURIComponent(userId)}/keys`, {
        method: 'POST',
        body: JSON.stringify({ rate_limit_per_min, allowed_models }),
      }),
    setKeyEnabled: (keyId: string, enabled: boolean) =>
      apiFetch<APIKey>(`/admin/keys/${encodeURIComponent(keyId)}/enabled`, {
        method: 'PATCH',
        body: JSON.stringify({ enabled }),
      }),
    listChannels: () => apiFetch<ListResp<Channel>>('/admin/channels'),
    createChannel: (body: Partial<Channel> & { api_key: string }) =>
      apiFetch<Channel>('/admin/channels', { method: 'POST', body: JSON.stringify(body) }),
    updateChannel: (id: string, body: Partial<Channel> & { api_key: string }) =>
      apiFetch<Channel>(`/admin/channels/${encodeURIComponent(id)}`, {
        method: 'PUT', body: JSON.stringify(body),
      }),
    setChannelEnabled: (id: string, enabled: boolean) =>
      apiFetch<Channel>(`/admin/channels/${encodeURIComponent(id)}/enabled`, {
        method: 'PATCH', body: JSON.stringify({ enabled }),
      }),
    deleteChannel: (id: string) =>
      apiFetch<void>(`/admin/channels/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    listAccounts: (limit = 100, offset = 0) =>
      apiFetch<ListResp<Account>>(`/admin/accounts?limit=${limit}&offset=${offset}`),
    createAccount: (body: { username: string; password: string; role: string; initial_balance?: number }) =>
      apiFetch<Account>('/admin/accounts', { method: 'POST', body: JSON.stringify(body) }),
    setAccountEnabled: (id: number, enabled: boolean) =>
      apiFetch<Account>(`/admin/accounts/${id}/enabled`, {
        method: 'PATCH', body: JSON.stringify({ enabled }),
      }),
    resetPassword: (id: number, password: string) =>
      apiFetch<{ ok: boolean }>(`/admin/accounts/${id}/password`, {
        method: 'POST', body: JSON.stringify({ password }),
      }),
    getSettings: () => apiFetch<Settings>('/admin/settings'),
    putSettings: (s: Settings) =>
      apiFetch<Settings>('/admin/settings', { method: 'PUT', body: JSON.stringify(s) }),
  },
  me: {
    profile: () => apiFetch<{ external_id: string; balance: number }>('/me/profile'),
    listKeys: () => apiFetch<ListResp<APIKey>>('/me/keys'),
    createKey: (rate_limit_per_min: number, allowed_models: string[]) =>
      apiFetch<CreatedKey>('/me/keys', {
        method: 'POST',
        body: JSON.stringify({ rate_limit_per_min, allowed_models }),
      }),
    setKeyEnabled: (keyId: string, enabled: boolean) =>
      apiFetch<APIKey>(`/me/keys/${encodeURIComponent(keyId)}/enabled`, {
        method: 'PATCH', body: JSON.stringify({ enabled }),
      }),
    deleteKey: (keyId: string) =>
      apiFetch<void>(`/me/keys/${encodeURIComponent(keyId)}`, { method: 'DELETE' }),
  },
}
```

- [ ] **Step 4: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 5: Commit**

```bash
git add web/src/api/
git commit -m "feat(web): API 客户端、TS 类型与端点封装"
```

---

### Task 4: 登录态 store（AuthContext + /auth/me 恢复）

**Files:**
- Create: `web/src/stores/auth.tsx`

**Interfaces:**
- Consumes: `api.auth`、`setUnauthorizedHandler`。
- Produces：
  - `<AuthProvider>` 组件
  - `useAuth(): { me: MeInfo | null; loading: boolean; refresh(): Promise<void>; logout(): Promise<void>; setMe(m: MeInfo): void }`

- [ ] **Step 1: 创建 `web/src/stores/auth.tsx`**

```tsx
import { createContext, useContext, useEffect, useState, useCallback, type ReactNode } from 'react'
import { api } from '../api/endpoints'
import { setUnauthorizedHandler, UnauthorizedError } from '../api/client'
import type { MeInfo } from '../api/types'

interface AuthState {
  me: MeInfo | null
  loading: boolean          // 初次 /auth/me 探活未回时为 true。
  refresh: () => Promise<void>
  logout: () => Promise<void>
  setMe: (m: MeInfo) => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<MeInfo | null>(null)
  const [loading, setLoading] = useState(true)

  // 探活：HttpOnly Cookie 前端读不到，登录态只能问后端。
  const refresh = useCallback(async () => {
    try {
      const info = await api.auth.me()
      setMe(info)
    } catch (e) {
      if (e instanceof UnauthorizedError) setMe(null)
      // 其它错误（网络/500）保持当前态，交由页面错误态处理。
    } finally {
      setLoading(false)
    }
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.auth.logout()
    } finally {
      setMe(null)
    }
  }, [])

  // 全局 401 拦截：任何请求 401 → 清态（ProtectedRoute 随即跳登录）。
  useEffect(() => {
    setUnauthorizedHandler(() => setMe(null))
  }, [])

  // 首次挂载探活。
  useEffect(() => {
    void refresh()
  }, [refresh])

  return (
    <AuthContext.Provider value={{ me, loading, refresh, logout, setMe }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return ctx
}
```

- [ ] **Step 2: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 3: Commit**

```bash
git add web/src/stores/
git commit -m "feat(web): 登录态 store（/auth/me 探活恢复 + 全局 401 拦截）"
```

---

### Task 5: 主题 Provider（明暗切换 + 持久化 + 主色 Token）

**Files:**
- Create: `web/src/theme/ThemeProvider.tsx`、`web/src/theme/tokens.css`
- Modify: `web/src/main.tsx`（包 `ThemeProvider`）

**Interfaces:**
- Produces：
  - `<ThemeProvider>` 组件（初始化跟随系统 + 读持久化）
  - `useTheme(): { mode: 'light' | 'dark'; toggle(): void }`

**说明**：Semi 暗色通过给 `body` 加 `theme-mode="dark"` 属性切换（Semi 官方机制）。主色定制通过覆盖 CSS 变量（`--semi-color-primary` 等）实现，避开默认皮肤的「满大街蓝」。

- [ ] **Step 1: 创建 `web/src/theme/tokens.css`**

```css
/* 主色定制：中性偏冷的靛蓝，避开 AntD 经典蓝。覆盖 Semi 主色 Token。
   统一圆角与阴影层级。明暗两套都覆盖。 */
:root {
  --semi-color-primary: 79 70 229;        /* indigo-600 rgb 分量 */
  --semi-color-primary-hover: 67 56 202;
  --semi-color-primary-active: 55 48 163;
  --semi-border-radius-large: 10px;
  --semi-border-radius-medium: 8px;
}
body[theme-mode='dark'] {
  --semi-color-primary: 129 140 248;      /* indigo-400，暗色下提亮 */
  --semi-color-primary-hover: 165 180 252;
  --semi-color-primary-active: 199 210 254;
}

/* 全站内容容器节奏。 */
.page-container {
  max-width: 1200px;
  margin: 0 auto;
  padding: 24px;
}
```

- [ ] **Step 2: 创建 `web/src/theme/ThemeProvider.tsx`**

```tsx
import { createContext, useContext, useEffect, useState, useCallback, type ReactNode } from 'react'

type Mode = 'light' | 'dark'
const STORAGE_KEY = 'linapi-theme'

interface ThemeState {
  mode: Mode
  toggle: () => void
}
const ThemeContext = createContext<ThemeState | null>(null)

// 初始模式：优先持久化选择，否则跟随系统。
function initialMode(): Mode {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setMode] = useState<Mode>(initialMode)

  // 把模式同步到 body 属性（Semi 暗色机制）。
  useEffect(() => {
    const body = document.body
    if (mode === 'dark') body.setAttribute('theme-mode', 'dark')
    else body.removeAttribute('theme-mode')
    localStorage.setItem(STORAGE_KEY, mode)
  }, [mode])

  const toggle = useCallback(() => setMode((m) => (m === 'dark' ? 'light' : 'dark')), [])

  return <ThemeContext.Provider value={{ mode, toggle }}>{children}</ThemeContext.Provider>
}

export function useTheme(): ThemeState {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme 必须在 ThemeProvider 内使用')
  return ctx
}
```

- [ ] **Step 3: 更新 `web/src/main.tsx` 包裹 Provider 并引入样式**

```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { AuthProvider } from './stores/auth'
import { ThemeProvider } from './theme/ThemeProvider'
import '@douyinfe/semi-ui/dist/css/semi.min.css'
import './theme/tokens.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ThemeProvider>
      <AuthProvider>
        <BrowserRouter basename="/console">
          <App />
        </BrowserRouter>
      </AuthProvider>
    </ThemeProvider>
  </React.StrictMode>,
)
```

- [ ] **Step 4: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 5: Commit**

```bash
git add web/src/theme/ web/src/main.tsx
git commit -m "feat(web): 主题 Provider（明暗切换/持久化/主色 Token）"
```

---

### Task 6: 共享 hook 与文案常量（useAsyncData + text）

**Files:**
- Create: `web/src/hooks/useAsyncData.ts`、`web/src/text.ts`

**Interfaces:**
- Produces：
  - `useAsyncData<T>(fetcher: () => Promise<T>, deps?): { data: T | null; loading: boolean; error: string | null; reload(): void }`
  - `text` 对象（集中中文文案，为将来 i18n 留口）

- [ ] **Step 1: 创建 `web/src/hooks/useAsyncData.ts`**

```ts
import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../api/client'

// useAsyncData 承载列表/详情加载的三态（loading/error/data），并给出 reload。
// 这是「三态齐全」硬指标的统一载体，页面只需据此渲染骨架/空态/错误态。
export function useAsyncData<T>(fetcher: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const reload = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError(null)
    fetcher()
      .then((d) => { if (alive) setData(d) })
      .catch((e) => {
        if (!alive) return
        // 401 由全局拦截处理，这里只记非鉴权错误文案。
        setError(e instanceof ApiError ? e.message : '加载失败，请重试')
      })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick, ...deps])

  return { data, loading, error, reload }
}
```

- [ ] **Step 2: 创建 `web/src/text.ts`**

```ts
// 集中中文文案。散落文案是 i18n 的敌人；本期中文优先，但集中放置，
// 将来接 i18n 只需把此对象换成 t() 查表。
export const text = {
  appName: 'LinAPI 控制台',
  login: {
    title: '登录',
    username: '用户名',
    password: '密码',
    remember: '记住我',
    submit: '登录',
    toRegister: '还没有账户？去注册',
    failed: '用户名或密码错误',
  },
  register: {
    title: '注册',
    submit: '注册',
    toLogin: '已有账户？去登录',
    pwTooShort: '密码至少 8 位',
    success: '注册成功，请登录',
  },
  nav: {
    overview: '概览',
    users: '用户管理',
    channels: '渠道管理',
    accounts: '账户管理',
    settings: '系统设置',
    portalHome: '我的概览',
    portalKeys: '我的密钥',
    logout: '退出登录',
  },
  common: {
    create: '新建',
    edit: '编辑',
    delete: '删除',
    enable: '启用',
    disable: '禁用',
    confirm: '确认',
    cancel: '取消',
    save: '保存',
    empty: '暂无数据',
    loadError: '加载失败',
    retry: '重试',
    saved: '保存成功',
    deleted: '删除成功',
    noPermission: '无权限访问',
  },
  key: {
    plaintextTitle: '密钥创建成功',
    plaintextWarn: '请立即复制并妥善保存。此明文仅显示这一次，关闭后无法再查看。',
    copy: '复制',
    copied: '已复制',
  },
} as const
```

- [ ] **Step 3: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 4: Commit**

```bash
git add web/src/hooks/ web/src/text.ts
git commit -m "feat(web): useAsyncData 三态 hook + 集中文案常量"
```

---

### Task 7: 守护路由与交互组件（ProtectedRoute / ConfirmButton / PlaintextKeyModal）

**Files:**
- Create: `web/src/components/ProtectedRoute.tsx`、`web/src/components/ConfirmButton.tsx`、`web/src/components/PlaintextKeyModal.tsx`

**Interfaces:**
- Consumes: `useAuth`、Semi 组件、`text`。
- Produces：
  - `<ProtectedRoute role?='admin'|'user'>`（未登录跳登录；角色不符跳自己的首页）
  - `<ConfirmButton onConfirm title content? ...>`（危险操作二次确认）
  - `<PlaintextKeyModal apiKey visible onClose>`（明文一次性弹窗 + 复制）

- [ ] **Step 1: 创建 `web/src/components/ProtectedRoute.tsx`**

```tsx
import { Navigate, useLocation } from 'react-router-dom'
import { Spin } from '@douyinfe/semi-ui'
import type { ReactNode } from 'react'
import { useAuth } from '../stores/auth'

// ProtectedRoute 是登录/角色守护。仅 UX 层——真正授权在后端。
// loading 时显示全屏 spin（避免闪回登录页）；未登录跳 /login；
// 角色不符跳各自首页（admin→/overview，user→/portal）。
export function ProtectedRoute({ role, children }: { role?: 'admin' | 'user'; children: ReactNode }) {
  const { me, loading } = useAuth()
  const location = useLocation()

  if (loading) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100vh' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!me) {
    return <Navigate to="/login" replace state={{ from: location }} />
  }
  if (role && me.role !== role) {
    return <Navigate to={me.role === 'admin' ? '/overview' : '/portal'} replace />
  }
  return <>{children}</>
}
```

- [ ] **Step 2: 创建 `web/src/components/ConfirmButton.tsx`**

```tsx
import { Button, Popconfirm, Toast } from '@douyinfe/semi-ui'
import { useState, type ReactNode } from 'react'
import { text } from '../text'

interface Props {
  onConfirm: () => Promise<void>
  title: string
  content?: ReactNode
  buttonText: string
  type?: 'danger' | 'primary' | 'tertiary'
  successMsg?: string
}

// ConfirmButton 封装「危险操作二次确认 + loading 态 + 结果 Toast」，
// 落实四硬指标之②（反馈即时 + 危险操作二次确认）。
export function ConfirmButton({ onConfirm, title, content, buttonText, type = 'danger', successMsg }: Props) {
  const [loading, setLoading] = useState(false)
  const handle = async () => {
    setLoading(true)
    try {
      await onConfirm()
      if (successMsg) Toast.success(successMsg)
    } catch (e) {
      Toast.error(e instanceof Error ? e.message : '操作失败')
    } finally {
      setLoading(false)
    }
  }
  return (
    <Popconfirm title={title} content={content} okText={text.common.confirm} cancelText={text.common.cancel} onConfirm={handle}>
      <Button type={type} loading={loading} theme="borderless">{buttonText}</Button>
    </Popconfirm>
  )
}
```

- [ ] **Step 3: 创建 `web/src/components/PlaintextKeyModal.tsx`**

```tsx
import { Modal, Banner, Typography, Button, Space, Toast } from '@douyinfe/semi-ui'
import { useState } from 'react'
import { text } from '../text'

// PlaintextKeyModal 落实「明文仅回显一次」硬约束：大字明文 + 一键复制 + 强警告。
export function PlaintextKeyModal({ apiKey, visible, onClose }: { apiKey: string; visible: boolean; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(apiKey)
      setCopied(true)
      Toast.success(text.key.copied)
    } catch {
      Toast.error('复制失败，请手动选择复制')
    }
  }
  return (
    <Modal title={text.key.plaintextTitle} visible={visible} onCancel={onClose} onOk={onClose} okText={text.common.confirm} maskClosable={false}>
      <Banner type="warning" description={text.key.plaintextWarn} closeIcon={null} />
      <div style={{ margin: '16px 0', padding: 12, background: 'var(--semi-color-fill-0)', borderRadius: 8, wordBreak: 'break-all' }}>
        <Typography.Text strong copyable={false} style={{ fontSize: 16, fontFamily: 'monospace' }}>{apiKey}</Typography.Text>
      </div>
      <Space>
        <Button type="primary" onClick={copy}>{copied ? text.key.copied : text.key.copy}</Button>
      </Space>
    </Modal>
  )
}
```

- [ ] **Step 4: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ProtectedRoute.tsx web/src/components/ConfirmButton.tsx web/src/components/PlaintextKeyModal.tsx
git commit -m "feat(web): 守护路由 + 二次确认按钮 + 明文密钥弹窗"
```

---

### Task 8: 复用表格组件（DataTable，三态齐全）

**Files:**
- Create: `web/src/components/DataTable.tsx`

**Interfaces:**
- Consumes: Semi `Table`/`Empty`/`Skeleton`/`Button`、`text`。
- Produces：
  - `<DataTable<T> columns data loading error onReload toolbar? rowKey empty? >`（统一渲染加载/空/错误三态 + 工具栏）

**说明**：这是「三态齐全」硬指标的表格载体，所有列表页共用；页面只传 columns + 由 `useAsyncData` 得到的 `{data,loading,error,reload}`。

- [ ] **Step 1: 创建 `web/src/components/DataTable.tsx`**

```tsx
import { Table, Empty, Skeleton, Button, Space, Typography } from '@douyinfe/semi-ui'
import { IllustrationNoContent, IllustrationNoContentDark } from '@douyinfe/semi-illustrations'
import type { ColumnProps } from '@douyinfe/semi-ui/lib/es/table'
import type { ReactNode } from 'react'
import { text } from '../text'

interface Props<T> {
  columns: ColumnProps<T>[]
  data: T[] | null
  loading: boolean
  error: string | null
  onReload: () => void
  rowKey: string
  toolbar?: ReactNode
  emptyText?: string
}

// DataTable 统一列表三态：加载→骨架屏；错误→错误态+重试；空→带插画空态；
// 有数据→表格。工具栏（新建/搜索）置于表格上方。
export function DataTable<T extends Record<string, unknown>>({
  columns, data, loading, error, onReload, rowKey, toolbar, emptyText,
}: Props<T>) {
  return (
    <div>
      {toolbar && <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>{toolbar}</div>}

      {loading && <Skeleton placeholder={<Skeleton.Paragraph rows={4} />} loading active style={{ padding: 24 }} />}

      {!loading && error && (
        <Empty
          image={<IllustrationNoContent style={{ width: 150 }} />}
          darkModeImage={<IllustrationNoContentDark style={{ width: 150 }} />}
          title={text.common.loadError}
          description={error}
        >
          <Button type="primary" onClick={onReload}>{text.common.retry}</Button>
        </Empty>
      )}

      {!loading && !error && data && data.length === 0 && (
        <Empty
          image={<IllustrationNoContent style={{ width: 150 }} />}
          darkModeImage={<IllustrationNoContentDark style={{ width: 150 }} />}
          title={emptyText ?? text.common.empty}
        />
      )}

      {!loading && !error && data && data.length > 0 && (
        <Table<T>
          columns={columns}
          dataSource={data}
          rowKey={rowKey}
          pagination={{ pageSize: 10, showTotal: true }}
          scroll={{ x: 'max-content' }}
        />
      )}
    </div>
  )
}

// 供页面复用的工具栏标题排版。
export function TableToolbar({ title, actions }: { title: string; actions?: ReactNode }) {
  return (
    <>
      <Typography.Title heading={5} style={{ margin: 0 }}>{title}</Typography.Title>
      <Space>{actions}</Space>
    </>
  )
}
```

- [ ] **Step 2: 补依赖 `@douyinfe/semi-illustrations`**

Run: `cd web && npm install @douyinfe/semi-illustrations`
Expected: 安装成功（Semi 空态插画包）。

- [ ] **Step 3: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 4: Commit**

```bash
git add web/src/components/DataTable.tsx web/package.json web/package-lock.json
git commit -m "feat(web): DataTable 复用表格（加载/空/错误三态）"
```

---

### Task 9: 布局骨架（顶栏 + 按角色侧边导航 + 内容区）

**Files:**
- Create: `web/src/components/Layout.tsx`

**Interfaces:**
- Consumes: `useAuth`、`useTheme`、`text`、react-router `Outlet`/`useNavigate`/`useLocation`。
- Produces：
  - `<AppLayout>`（作为受保护区的布局壳，内容由 `<Outlet>` 渲染）

**说明**：侧边导航按 `me.role` 渲染（admin 见 5 项，user 见 2 项）。顶栏含 app 名、主题切换、用户菜单（登出）。响应式：Semi `Nav` 支持 `isCollapsed`，窄屏可收起。

- [ ] **Step 1: 创建 `web/src/components/Layout.tsx`**

```tsx
import { Layout, Nav, Button, Avatar, Dropdown, Typography } from '@douyinfe/semi-ui'
import { IconMoon, IconSun, IconExit } from '@douyinfe/semi-icons'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { useAuth } from '../stores/auth'
import { useTheme } from '../theme/ThemeProvider'
import { text } from '../text'

const { Header, Sider, Content } = Layout

// 按角色的导航项：itemKey 即路由路径（去掉 basename）。
const adminNav = [
  { itemKey: '/overview', text: text.nav.overview },
  { itemKey: '/users', text: text.nav.users },
  { itemKey: '/channels', text: text.nav.channels },
  { itemKey: '/accounts', text: text.nav.accounts },
  { itemKey: '/settings', text: text.nav.settings },
]
const userNav = [
  { itemKey: '/portal', text: text.nav.portalHome },
  { itemKey: '/portal/keys', text: text.nav.portalKeys },
]

export function AppLayout() {
  const { me, logout } = useAuth()
  const { mode, toggle } = useTheme()
  const navigate = useNavigate()
  const location = useLocation()

  const items = me?.role === 'admin' ? adminNav : userNav

  return (
    <Layout style={{ height: '100vh' }}>
      <Sider>
        <Nav
          style={{ height: '100%' }}
          items={items}
          selectedKeys={[location.pathname]}
          onSelect={(d) => navigate(d.itemKey as string)}
          header={{ text: text.appName }}
          footer={{ collapseButton: true }}
        />
      </Sider>
      <Layout>
        <Header style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', padding: '0 24px', gap: 12 }}>
          <Button
            theme="borderless"
            icon={mode === 'dark' ? <IconSun /> : <IconMoon />}
            onClick={toggle}
            aria-label="切换主题"
          />
          <Dropdown
            trigger="click"
            position="bottomRight"
            render={
              <Dropdown.Menu>
                <Dropdown.Item icon={<IconExit />} onClick={() => { void logout().then(() => navigate('/login')) }}>
                  {text.nav.logout}
                </Dropdown.Item>
              </Dropdown.Menu>
            }
          >
            <span style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 8 }}>
              <Avatar size="small" color="light-blue">{me?.username?.[0]?.toUpperCase() ?? '?'}</Avatar>
              <Typography.Text>{me?.username}</Typography.Text>
            </span>
          </Dropdown>
        </Header>
        <Content style={{ overflow: 'auto' }}>
          <div className="page-container">
            <Outlet />
          </div>
        </Content>
      </Layout>
    </Layout>
  )
}
```

- [ ] **Step 2: 补依赖 `@douyinfe/semi-icons`（Semi 图标包，通常随 semi-ui 装；缺则补）**

Run: `cd web && npm install @douyinfe/semi-icons`
Expected: 安装成功。

- [ ] **Step 3: 类型检查**

Run: `cd web && npm run typecheck`
Expected: 无类型错误。

- [ ] **Step 4: Commit**

```bash
git add web/src/components/Layout.tsx web/package.json web/package-lock.json
git commit -m "feat(web): 布局骨架（顶栏/角色侧边导航/主题切换/登出）"
```

---

### Task 10: 登录 / 注册页 + 完整路由表

**Files:**
- Create: `web/src/pages/Login.tsx`、`web/src/pages/Register.tsx`
- Modify: `web/src/App.tsx`（完整路由 + 分区守护）

**Interfaces:**
- Consumes: `api.auth`、`useAuth`、`api.admin.getSettings`（登录页判断是否显示注册入口）、共享组件。
- Produces: `/login`、`/register` 页；`App.tsx` 完整路由骨架（页面组件在 Task 11~13 落地，先用占位）。

**登录页规范样板要点（四硬指标落点）**：客户端校验（用户名/密码必填，提交前红字）；提交 loading 态；失败 Toast；成功按角色跳转（admin→/overview，user→/portal）。

- [ ] **Step 1: 创建 `web/src/pages/Login.tsx`**

```tsx
import { Form, Button, Card, Typography, Toast } from '@douyinfe/semi-ui'
import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/endpoints'
import { useAuth } from '../stores/auth'
import { text } from '../text'

export default function Login() {
  const navigate = useNavigate()
  const { setMe, refresh } = useAuth()
  const [loading, setLoading] = useState(false)
  const [canRegister, setCanRegister] = useState(false)

  // 登录页也需知道是否开放注册以决定显示注册入口。
  // /admin/settings 需鉴权，未登录拿不到，故用一个无鉴权探测：注册开关体现在 register 端点，
  // 这里改为始终显示入口但点击后由后端 403 兜底——更简单可靠。若要精确控制，
  // 后端可另开一个公开的 GET /auth/registration-status（本期从简，始终显示）。
  useEffect(() => { setCanRegister(true) }, [])

  const submit = async (values: { username: string; password: string; remember?: boolean }) => {
    setLoading(true)
    try {
      const res = await api.auth.login(values.username, values.password, !!values.remember)
      // 登录成功后用 /auth/me 拿完整身份并写入 store。
      await refresh()
      setMe({ username: res.username, role: res.role as 'admin' | 'user', external_id: '' })
      navigate(res.role === 'admin' ? '/overview' : '/portal', { replace: true })
    } catch (e) {
      Toast.error(e instanceof Error ? e.message : text.login.failed)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100vh', background: 'var(--semi-color-bg-1)' }}>
      <Card style={{ width: 380 }}>
        <Typography.Title heading={3} style={{ textAlign: 'center', marginBottom: 24 }}>{text.appName}</Typography.Title>
        <Form onSubmit={submit}>
          <Form.Input
            field="username"
            label={text.login.username}
            rules={[{ required: true, message: '请输入用户名' }]}
          />
          <Form.Input
            field="password"
            label={text.login.password}
            mode="password"
            rules={[{ required: true, message: '请输入密码' }]}
          />
          <Form.Checkbox field="remember" noLabel>{text.login.remember}</Form.Checkbox>
          <Button htmlType="submit" type="primary" theme="solid" block loading={loading} style={{ marginTop: 12 }}>
            {text.login.submit}
          </Button>
        </Form>
        {canRegister && (
          <Typography.Text link onClick={() => navigate('/register')} style={{ display: 'block', textAlign: 'center', marginTop: 16 }}>
            {text.login.toRegister}
          </Typography.Text>
        )}
      </Card>
    </div>
  )
}
```

- [ ] **Step 2: 创建 `web/src/pages/Register.tsx`**

要点：密码长度前置校验（≥8，就地红字）；注册成功 Toast 后跳登录；后端 403（未开放）→ 友好提示。

```tsx
import { Form, Button, Card, Typography, Toast } from '@douyinfe/semi-ui'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/endpoints'
import { ApiError } from '../api/client'
import { text } from '../text'

export default function Register() {
  const navigate = useNavigate()
  const [loading, setLoading] = useState(false)

  const submit = async (values: { username: string; password: string }) => {
    setLoading(true)
    try {
      await api.auth.register(values.username, values.password)
      Toast.success(text.register.success)
      navigate('/login', { replace: true })
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) {
        Toast.error('当前未开放注册')
      } else {
        Toast.error(e instanceof Error ? e.message : '注册失败')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100vh', background: 'var(--semi-color-bg-1)' }}>
      <Card style={{ width: 380 }}>
        <Typography.Title heading={3} style={{ textAlign: 'center', marginBottom: 24 }}>{text.register.title}</Typography.Title>
        <Form onSubmit={submit}>
          <Form.Input field="username" label={text.login.username} rules={[{ required: true, message: '请输入用户名' }]} />
          <Form.Input
            field="password"
            label={text.login.password}
            mode="password"
            rules={[
              { required: true, message: '请输入密码' },
              { min: 8, message: text.register.pwTooShort },
            ]}
          />
          <Button htmlType="submit" type="primary" theme="solid" block loading={loading} style={{ marginTop: 12 }}>
            {text.register.submit}
          </Button>
        </Form>
        <Typography.Text link onClick={() => navigate('/login')} style={{ display: 'block', textAlign: 'center', marginTop: 16 }}>
          {text.register.toLogin}
        </Typography.Text>
      </Card>
    </div>
  )
}
```

- [ ] **Step 3: 重写 `web/src/App.tsx` 为完整路由表**

页面组件在后续任务落地，这里先 import 占位（Task 11~13 替换为真实页）。为让本任务可独立验证，先建最小占位页组件放同文件顶部。

```tsx
import { Routes, Route, Navigate } from 'react-router-dom'
import { ProtectedRoute } from './components/ProtectedRoute'
import { AppLayout } from './components/Layout'
import Login from './pages/Login'
import Register from './pages/Register'

// 占位页（Task 11~13 替换为真实实现）。
const Stub = ({ name }: { name: string }) => <div>{name} 页（待实现）</div>

export default function App() {
  return (
    <Routes>
      {/* 公共 */}
      <Route path="/login" element={<Login />} />
      <Route path="/register" element={<Register />} />

      {/* admin 区 */}
      <Route element={<ProtectedRoute role="admin"><AppLayout /></ProtectedRoute>}>
        <Route path="/overview" element={<Stub name="概览" />} />
        <Route path="/users" element={<Stub name="用户管理" />} />
        <Route path="/channels" element={<Stub name="渠道管理" />} />
        <Route path="/accounts" element={<Stub name="账户管理" />} />
        <Route path="/settings" element={<Stub name="系统设置" />} />
      </Route>

      {/* user 区 */}
      <Route element={<ProtectedRoute role="user"><AppLayout /></ProtectedRoute>}>
        <Route path="/portal" element={<Stub name="我的概览" />} />
        <Route path="/portal/keys" element={<Stub name="我的密钥" />} />
      </Route>

      {/* 兜底：进根路径时交给 ProtectedRoute 决定去向（登录页或按角色首页）。 */}
      <Route path="*" element={<ProtectedRoute><Navigate to="/overview" replace /></ProtectedRoute>} />
    </Routes>
  )
}
```

> 注：`path="*"` 用 `<ProtectedRoute>`（不带 role）包一个跳转——未登录会被 ProtectedRoute 拦到 /login；已登录若为 user，`/overview` 的 admin 守护会再把他弹回 /portal。

- [ ] **Step 4: 类型检查 + 构建**

Run: `cd web && npm run build`
Expected: 类型检查通过，构建成功。

- [ ] **Step 5: 端到端验证登录流**

启后端（内存模式，admin.enabled=true，bootstrap 建一个 admin），`npm run build` 后访问 `/console/login`，用 bootstrap 账户登录 → 应跳 `/console/overview`（当前显示占位页）。
Expected: 登录成功跳转；刷新页面（`/auth/me` 探活）仍保持登录态不被踢回。

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/Login.tsx web/src/pages/Register.tsx web/src/App.tsx
git commit -m "feat(web): 登录/注册页 + 完整路由表与分区守护"
```

---

### Task 11: 用户管理页（Users，CRUD 完整样板）

**Files:**
- Create: `web/src/pages/admin/Users.tsx`
- Modify: `web/src/App.tsx`（`/users` 路由替换占位为 `Users`）

**Interfaces:**
- Consumes: `api.admin`（listUsers/createUser/setUserEnabled/addBalance/listKeys/createKey/setKeyEnabled）、`useAsyncData`、`DataTable`、`ConfirmButton`、`PlaintextKeyModal`。
- Produces: `/users` 完整页，作为其余 CRUD 页的样板。

**四硬指标落点**：① 三态由 DataTable 承载；② 创建/启停/充值有 loading + Toast，禁用走 ConfirmButton 二次确认；③ 创建/充值表单提交前校验（必填、数值范围）；④ DataTable 已 `scroll={{x:'max-content'}}`，页面容器响应式。

**关键交互**：某用户的密钥管理放在展开行/抽屉；建 key 成功 → PlaintextKeyModal 显示明文一次。

- [ ] **Step 1: 创建 `web/src/pages/admin/Users.tsx`**

```tsx
import { Button, Form, Modal, Toast, Space, Tag, SideSheet, Typography } from '@douyinfe/semi-ui'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { APIKey, User } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { DataTable, TableToolbar } from '../../components/DataTable'
import { ConfirmButton } from '../../components/ConfirmButton'
import { PlaintextKeyModal } from '../../components/PlaintextKeyModal'
import { text } from '../../text'

export default function Users() {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.listUsers().then((r) => r.data))
  const [createVisible, setCreateVisible] = useState(false)
  const [creating, setCreating] = useState(false)
  const [keysUser, setKeysUser] = useState<string | null>(null) // 正在管密钥的用户。

  const createUser = async (values: { external_id: string; balance?: number }) => {
    setCreating(true)
    try {
      await api.admin.createUser(values.external_id, values.balance ?? 0)
      Toast.success('用户创建成功')
      setCreateVisible(false)
      reload()
    } catch (e) {
      Toast.error(e instanceof Error ? e.message : '创建失败')
    } finally {
      setCreating(false)
    }
  }

  const columns = [
    { title: '用户标识', dataIndex: 'external_id' },
    { title: '余额', dataIndex: 'balance', render: (v: number) => v.toLocaleString() },
    {
      title: '状态', dataIndex: 'enabled',
      render: (v: boolean) => <Tag color={v ? 'green' : 'grey'}>{v ? '启用' : '禁用'}</Tag>,
    },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: User) => (
        <Space>
          <Button theme="borderless" onClick={() => setKeysUser(r.external_id)}>密钥</Button>
          <BalanceButton userId={r.external_id} onDone={reload} />
          {r.enabled ? (
            <ConfirmButton
              title="确认禁用该用户？" buttonText={text.common.disable} successMsg={text.common.saved}
              onConfirm={async () => { await api.admin.setUserEnabled(r.external_id, false); reload() }}
            />
          ) : (
            <Button theme="borderless" type="primary" onClick={async () => {
              try { await api.admin.setUserEnabled(r.external_id, true); Toast.success(text.common.saved); reload() }
              catch (e) { Toast.error(e instanceof Error ? e.message : '操作失败') }
            }}>{text.common.enable}</Button>
          )}
        </Space>
      ),
    },
  ]

  return (
    <>
      <DataTable<User>
        columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="external_id"
        toolbar={<TableToolbar title={text.nav.users} actions={<Button type="primary" onClick={() => setCreateVisible(true)}>{text.common.create}</Button>} />}
      />

      <Modal title="新建用户" visible={createVisible} onCancel={() => setCreateVisible(false)} footer={null}>
        <Form onSubmit={createUser}>
          <Form.Input field="external_id" label="用户标识" rules={[{ required: true, message: '请输入用户标识' }]} />
          <Form.InputNumber field="balance" label="初始余额" min={0} initValue={0} style={{ width: '100%' }} />
          <Button htmlType="submit" type="primary" theme="solid" loading={creating} style={{ marginTop: 12 }}>{text.common.create}</Button>
        </Form>
      </Modal>

      {keysUser && <UserKeysSheet userId={keysUser} onClose={() => setKeysUser(null)} />}
    </>
  )
}
```

- [ ] **Step 2: 在同文件追加 `BalanceButton`（充值，带校验 + loading + Toast）**

```tsx
function BalanceButton({ userId, onDone }: { userId: string; onDone: () => void }) {
  const [visible, setVisible] = useState(false)
  const [loading, setLoading] = useState(false)
  const submit = async (values: { delta: number }) => {
    setLoading(true)
    try {
      const res = await api.admin.addBalance(userId, values.delta)
      Toast.success(`充值成功，当前余额 ${res.balance.toLocaleString()}`)
      setVisible(false)
      onDone()
    } catch (e) {
      Toast.error(e instanceof Error ? e.message : '充值失败')
    } finally {
      setLoading(false)
    }
  }
  return (
    <>
      <Button theme="borderless" onClick={() => setVisible(true)}>充值</Button>
      <Modal title={`为 ${userId} 充值`} visible={visible} onCancel={() => setVisible(false)} footer={null}>
        <Form onSubmit={submit}>
          <Form.InputNumber field="delta" label="增减额（负数为扣减）" rules={[{ required: true, message: '请输入金额' }]} style={{ width: '100%' }} />
          <Button htmlType="submit" type="primary" theme="solid" loading={loading} style={{ marginTop: 12 }}>{text.common.confirm}</Button>
        </Form>
      </Modal>
    </>
  )
}
```

- [ ] **Step 3: 在同文件追加 `UserKeysSheet`（抽屉内管密钥 + 明文一次性弹窗）**

```tsx
function UserKeysSheet({ userId, onClose }: { userId: string; onClose: () => void }) {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.listKeys(userId).then((r) => r.data), [userId])
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  const createKey = async () => {
    setCreating(true)
    try {
      const created = await api.admin.createKey(userId, 0, [])
      setPlaintext(created.api_key) // 触发明文一次性弹窗。
      reload()
    } catch (e) {
      Toast.error(e instanceof Error ? e.message : '创建密钥失败')
    } finally {
      setCreating(false)
    }
  }

  const columns = [
    { title: 'Key ID', dataIndex: 'key_id' },
    { title: '限流/分', dataIndex: 'rate_limit_per_min' },
    {
      title: '状态', dataIndex: 'enabled',
      render: (v: boolean) => <Tag color={v ? 'green' : 'grey'}>{v ? '启用' : '禁用'}</Tag>,
    },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: APIKey) => (
        r.enabled
          ? <ConfirmButton title="确认禁用该密钥？" buttonText={text.common.disable} successMsg={text.common.saved}
              onConfirm={async () => { await api.admin.setKeyEnabled(r.key_id, false); reload() }} />
          : <Button theme="borderless" type="primary" onClick={async () => {
              try { await api.admin.setKeyEnabled(r.key_id, true); Toast.success(text.common.saved); reload() }
              catch (e) { Toast.error(e instanceof Error ? e.message : '操作失败') }
            }}>{text.common.enable}</Button>
      ),
    },
  ]

  return (
    <SideSheet title={`${userId} 的密钥`} visible onCancel={onClose} width={560}>
      <Space vertical align="start" style={{ width: '100%' }}>
        <Button type="primary" loading={creating} onClick={createKey}>生成新密钥</Button>
        <Typography.Text type="tertiary">明文仅在创建时显示一次，请及时保存。</Typography.Text>
        <div style={{ width: '100%' }}>
          <DataTable<APIKey> columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="key_id" />
        </div>
      </Space>
      <PlaintextKeyModal apiKey={plaintext ?? ''} visible={plaintext !== null} onClose={() => setPlaintext(null)} />
    </SideSheet>
  )
}
```

- [ ] **Step 4: `web/src/App.tsx` 用真实页替换 `/users` 占位**

import `Users` 并把 `<Route path="/users" element={<Stub name="用户管理" />} />` 改为 `<Route path="/users" element={<Users />} />`。

- [ ] **Step 5: 类型检查 + 构建**

Run: `cd web && npm run build`
Expected: 通过。

- [ ] **Step 6: 端到端验证**

后端内存模式登录 admin → 用户页：创建用户（校验空标识被拦）→ 列表出现 → 充值 → 生成密钥（明文弹窗仅一次，关闭后列表只见 key_id）→ 禁用（二次确认）。
Expected: 各操作有 Toast，三态正确，明文仅显示一次。

- [ ] **Step 7: Commit**

```bash
git add web/src/pages/admin/Users.tsx web/src/App.tsx
git commit -m "feat(web): 用户管理页（用户+密钥 CRUD，四硬指标样板）"
```

---

### Task 12: 其余管理页（Overview / Channels / Accounts / Settings）

**Files:**
- Create: `web/src/pages/admin/Overview.tsx`、`Channels.tsx`、`Accounts.tsx`、`Settings.tsx`
- Modify: `web/src/App.tsx`（4 个占位替换为真实页）

**Interfaces:**
- Consumes: `api.admin`、`useAsyncData`、`DataTable`/`TableToolbar`、`ConfirmButton`、`text`。
- Produces: `/overview`、`/channels`、`/accounts`、`/settings` 四页。

**均复用 Task 11 建立的模式**（`useAsyncData` + `DataTable` 三态 + Modal 表单 + Toast + ConfirmButton）。以下给每页的完整规格。

- [ ] **Step 1: `Overview.tsx` — 概览卡片**

- 数据：并发拉 `api.admin.listUsers()`、`listChannels()`、`listAccounts()`，用 `Promise.all` 包进一个 `useAsyncData`。启用模型数 = 所有 enabled 渠道的 `models` 键去重计数。
- 渲染：Semi `Card` + `Row/Col` 四张统计卡（用户数 / 渠道数 / 启用模型数 / 账户数），每卡用 `Typography.Title` 显示数值。
- 三态：加载 → 骨架；错误 → 错误提示 + 重试按钮；无需空态（计数恒有值）。
- 不接 `/metrics`（spec §6.1 明确）。

```tsx
import { Card, Row, Col, Typography, Spin, Button, Empty } from '@douyinfe/semi-ui'
import { api } from '../../api/endpoints'
import { useAsyncData } from '../../hooks/useAsyncData'
import { text } from '../../text'

export default function Overview() {
  const { data, loading, error, reload } = useAsyncData(async () => {
    const [users, channels, accounts] = await Promise.all([
      api.admin.listUsers(), api.admin.listChannels(), api.admin.listAccounts(),
    ])
    const models = new Set<string>()
    channels.data.filter((c) => c.enabled).forEach((c) => Object.keys(c.models ?? {}).forEach((m) => models.add(m)))
    return { users: users.data.length, channels: channels.data.length, models: models.size, accounts: accounts.data.length }
  })

  if (loading) return <div style={{ textAlign: 'center', padding: 48 }}><Spin size="large" /></div>
  if (error) return <Empty title={text.common.loadError} description={error}><Button type="primary" onClick={reload}>{text.common.retry}</Button></Empty>

  const cards = [
    { label: '用户数', value: data!.users },
    { label: '渠道数', value: data!.channels },
    { label: '启用模型数', value: data!.models },
    { label: '账户数', value: data!.accounts },
  ]
  return (
    <>
      <Typography.Title heading={5} style={{ marginBottom: 16 }}>{text.nav.overview}</Typography.Title>
      <Row gutter={16}>
        {cards.map((c) => (
          <Col span={6} key={c.label}>
            <Card><Typography.Text type="tertiary">{c.label}</Typography.Text><Typography.Title heading={2}>{c.value}</Typography.Title></Card>
          </Col>
        ))}
      </Row>
    </>
  )
}
```

- [ ] **Step 2: `Channels.tsx` — 渠道 CRUD**

- 列表：`api.admin.listChannels()`；列 = channel_id / name / format(Tag) / base_url / priority / weight / enabled(Tag) / 操作。
- 新建 + 编辑：同一 Modal 表单（编辑时 `initValues` 填充）。字段：`channel_id`（编辑时禁改）、`name`、`format`（Select：openai/anthropic）、`base_url`、`api_key`（password 输入，编辑时占位「留空则不改」——**注意**：后端 PUT 需全量字段，若留空需先 GET 原值或提示必填；本期简单起见编辑时 api_key 必填重填）、`priority`（number）、`weight`（number，≥1）、`models`（键值对编辑器，简化为 JSON textarea：对外名→上游名，空值透传）。
- 校验：channel_id/base_url/api_key/format 必填；weight ≥ 1；models 文本须为合法 JSON 对象（提交前 `JSON.parse` 校验，失败就地红字）。
- 操作：编辑、启停（ConfirmButton 禁用）、删除（ConfirmButton「确认删除渠道？此操作不可恢复」）。删除成功 Toast + reload。
- 渠道 api_key 列表永不显示（后端已脱敏，返回为空）。

- [ ] **Step 3: `Accounts.tsx` — 登录账户管理**

- 列表：`api.admin.listAccounts()`；列 = id / username / role(Tag：admin 蓝 / user 绿) / external_id / enabled(Tag) / 操作。
- 新建 Modal：字段 `username`（必填）、`password`（必填，≥8 前置校验）、`role`（Select：admin/user）、`initial_balance`（InputNumber，仅当 role=user 时显示，min 0）。**表单不含 external_id 输入框**（spec §6.1 硬性：user 账户的 external_id 由后端自动生成/回填）。提交调 `api.admin.createAccount`。
- 改密：行操作「改密」→ Modal 输入新密码（≥8 校验）→ `api.admin.resetPassword(id, pw)` → Toast。
- 启停：ConfirmButton（禁用账户二次确认）→ `api.admin.setAccountEnabled`。
- 响应体不含 password_hash（后端已保证；前端也不展示任何哈希）。

- [ ] **Step 4: `Settings.tsx` — 系统设置**

- 数据：`api.admin.getSettings()`。
- 表单：`Form.Switch field="registration_enabled"`（开放注册开关）+ `Form.InputNumber field="new_user_initial_balance"`（新用户初始额度，min 0）。用 `initValues` 填充当前值。
- 保存：`api.admin.putSettings(values)` → Toast「保存成功」。保存按钮 loading 态。
- 三态：加载中 Spin；错误 + 重试；有数据渲染表单（无空态）。

```tsx
import { Form, Button, Toast, Spin, Empty, Typography } from '@douyinfe/semi-ui'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { Settings as SettingsT } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { text } from '../../text'

export default function Settings() {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.getSettings())
  const [saving, setSaving] = useState(false)

  const save = async (values: SettingsT) => {
    setSaving(true)
    try { await api.admin.putSettings(values); Toast.success(text.common.saved) }
    catch (e) { Toast.error(e instanceof Error ? e.message : '保存失败') }
    finally { setSaving(false) }
  }

  if (loading) return <div style={{ textAlign: 'center', padding: 48 }}><Spin size="large" /></div>
  if (error || !data) return <Empty title={text.common.loadError} description={error ?? ''}><Button type="primary" onClick={reload}>{text.common.retry}</Button></Empty>

  return (
    <>
      <Typography.Title heading={5} style={{ marginBottom: 16 }}>{text.nav.settings}</Typography.Title>
      <Form onSubmit={save} initValues={data} style={{ maxWidth: 480 }}>
        <Form.Switch field="registration_enabled" label="开放注册" />
        <Form.InputNumber field="new_user_initial_balance" label="新用户初始额度" min={0} style={{ width: '100%' }} />
        <Button htmlType="submit" type="primary" theme="solid" loading={saving} style={{ marginTop: 12 }}>{text.common.save}</Button>
      </Form>
    </>
  )
}
```

- [ ] **Step 5: `App.tsx` 替换 4 个占位为真实页**

import `Overview`/`Channels`/`Accounts`/`Settings`，替换对应 `<Stub>`。

- [ ] **Step 6: 类型检查 + 构建**

Run: `cd web && npm run build`
Expected: 通过。

- [ ] **Step 7: 端到端验证**

登录 admin → 逐页验：概览四卡有数；渠道建/编辑/启停/删（删二次确认）；账户建 user（无 external_id 输入框、可填初始余额）+ 改密 + 启停；设置改开关并保存后刷新仍生效。
Expected: 四页四硬指标齐全。

- [ ] **Step 8: Commit**

```bash
git add web/src/pages/admin/ web/src/App.tsx
git commit -m "feat(web): 概览/渠道/账户/系统设置四个管理页"
```

---

### Task 13: 用户门户页（PortalHome / PortalKeys）

**Files:**
- Create: `web/src/pages/portal/PortalHome.tsx`、`PortalKeys.tsx`
- Modify: `web/src/App.tsx`（`/portal`、`/portal/keys` 占位替换）

**Interfaces:**
- Consumes: `api.me`（profile/listKeys/createKey/setKeyEnabled/deleteKey）、共享组件。
- Produces: `/portal`、`/portal/keys` 两页。

**注意**：`/me/keys` 建 key **不传** user_id（后端强制绑会话）；删除走 `api.me.deleteKey`。交互与 admin 侧一致但只操作自己的资源。

- [ ] **Step 1: `PortalHome.tsx` — 我的概览**

- 数据：`api.me.profile()` → `{ external_id, balance }`。
- 渲染：两张卡（账户标识 / 当前余额）。加载 Spin；错误 + 重试。

```tsx
import { Card, Row, Col, Typography, Spin, Empty, Button } from '@douyinfe/semi-ui'
import { api } from '../../api/endpoints'
import { useAsyncData } from '../../hooks/useAsyncData'
import { text } from '../../text'

export default function PortalHome() {
  const { data, loading, error, reload } = useAsyncData(() => api.me.profile())
  if (loading) return <div style={{ textAlign: 'center', padding: 48 }}><Spin size="large" /></div>
  if (error || !data) return <Empty title={text.common.loadError} description={error ?? ''}><Button type="primary" onClick={reload}>{text.common.retry}</Button></Empty>
  return (
    <>
      <Typography.Title heading={5} style={{ marginBottom: 16 }}>{text.nav.portalHome}</Typography.Title>
      <Row gutter={16}>
        <Col span={8}><Card><Typography.Text type="tertiary">账户标识</Typography.Text><Typography.Title heading={4}>{data.external_id}</Typography.Title></Card></Col>
        <Col span={8}><Card><Typography.Text type="tertiary">当前余额</Typography.Text><Typography.Title heading={2}>{data.balance.toLocaleString()}</Typography.Title></Card></Col>
      </Row>
    </>
  )
}
```

- [ ] **Step 2: `PortalKeys.tsx` — 我的密钥（建/启停/删 + 明文一次性）**

- 列表：`api.me.listKeys()`；列 = key_id / rate_limit_per_min / enabled(Tag) / 操作（启停 ConfirmButton + 删除 ConfirmButton）。
- 建 key：按钮 → `api.me.createKey(0, [])` → 明文 PlaintextKeyModal 一次性显示 → reload。
- 删除：ConfirmButton「确认删除此密钥？删除后使用该密钥的请求将立即失败」→ `api.me.deleteKey(key_id)` → Toast + reload。

```tsx
import { Button, Space, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { APIKey } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { DataTable, TableToolbar } from '../../components/DataTable'
import { ConfirmButton } from '../../components/ConfirmButton'
import { PlaintextKeyModal } from '../../components/PlaintextKeyModal'
import { text } from '../../text'

export default function PortalKeys() {
  const { data, loading, error, reload } = useAsyncData(() => api.me.listKeys().then((r) => r.data))
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  const createKey = async () => {
    setCreating(true)
    try { const c = await api.me.createKey(0, []); setPlaintext(c.api_key); reload() }
    catch (e) { Toast.error(e instanceof Error ? e.message : '创建失败') }
    finally { setCreating(false) }
  }

  const columns = [
    { title: 'Key ID', dataIndex: 'key_id' },
    { title: '限流/分', dataIndex: 'rate_limit_per_min' },
    { title: '状态', dataIndex: 'enabled', render: (v: boolean) => <Tag color={v ? 'green' : 'grey'}>{v ? '启用' : '禁用'}</Tag> },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: APIKey) => (
        <Space>
          {r.enabled
            ? <ConfirmButton title="确认禁用此密钥？" buttonText={text.common.disable} successMsg={text.common.saved} type="tertiary"
                onConfirm={async () => { await api.me.setKeyEnabled(r.key_id, false); reload() }} />
            : <Button theme="borderless" type="primary" onClick={async () => {
                try { await api.me.setKeyEnabled(r.key_id, true); Toast.success(text.common.saved); reload() }
                catch (e) { Toast.error(e instanceof Error ? e.message : '操作失败') }
              }}>{text.common.enable}</Button>}
          <ConfirmButton title="确认删除此密钥？" content="删除后使用该密钥的请求将立即失败。" buttonText={text.common.delete} successMsg={text.common.deleted}
            onConfirm={async () => { await api.me.deleteKey(r.key_id); reload() }} />
        </Space>
      ),
    },
  ]

  return (
    <>
      <DataTable<APIKey>
        columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="key_id"
        toolbar={<TableToolbar title={text.nav.portalKeys} actions={<Button type="primary" loading={creating} onClick={createKey}>生成新密钥</Button>} />}
      />
      <Typography.Text type="tertiary" style={{ display: 'block', marginTop: 8 }}>明文仅在创建时显示一次，请及时保存。</Typography.Text>
      <PlaintextKeyModal apiKey={plaintext ?? ''} visible={plaintext !== null} onClose={() => setPlaintext(null)} />
    </>
  )
}
```

- [ ] **Step 3: `App.tsx` 替换门户占位**

import `PortalHome`/`PortalKeys`，替换 `/portal`、`/portal/keys` 的 `<Stub>`。

- [ ] **Step 4: 类型检查 + 构建**

Run: `cd web && npm run build`
Expected: 通过。

- [ ] **Step 5: 端到端验证（含越权）**

用 admin 建一个 user 账户 → 用该 user 登录 → 应跳 `/portal`（而非 overview）；建 key（明文一次）、启停、删；侧边栏只有「我的概览/我的密钥」两项。手动改 URL 到 `/console/users` → 前端守护弹回 `/portal`；即便前端被绕过，后端 `/admin/*` 也会 403。
Expected: 门户可用，角色分流与守护正确。

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/portal/ web/src/App.tsx
git commit -m "feat(web): 用户门户（我的概览 + 我的密钥）"
```

---

### Task 14: 收尾（构建集成验证 + 暗色 + 文档/记忆同步）

**Files:**
- Modify: `docs/progress.md`、`docs/modules.md`、`docs/architecture.md`、`CLAUDE.md`
- Modify: 记忆 `linapi-progress.md` + `MEMORY.md`
- Modify: `README`（若有；补控制台构建与访问说明）

**Interfaces:**
- Consumes: 全部前置任务。
- Produces: 可交付的嵌入式控制台 + 一致文档。

- [ ] **Step 1: 全链路构建验证（前端 → embed → 后端）**

Run: `cd web && npm run build && cd .. && CGO_ENABLED=1 go test -race ./... && go build -o bin/linapi ./cmd/linapi`
Expected: 前端产物进 `internal/server/web_dist/`；Go 测试全绿（含 Plan 1 后端测试）；二进制构建成功（已 embed 前端）。

- [ ] **Step 2: 单二进制端到端冒烟**

用内存模式启 `bin/linapi`（config：`admin.enabled=true` + bootstrap admin），访问 `http://localhost:8080/console`：
- 登录页加载（样式正常，非默认皮肤）。
- admin 登录 → 五页可用。
- 主题切换按钮 → 全站切暗色，刷新后保持（localStorage 持久化）。
- 退出登录 → 回登录页，此后访问 `/console/overview` 被弹回登录。
Expected: 全流程通过，暗色一等公民，会话恢复正确。

- [ ] **Step 3: 更新 `docs/progress.md`**

顶部日期 `2026-07-10`；追加第 ⑮ 步：Web 控制台（React+Vite+TS+Semi，嵌入式单二进制 `/console`，SPA fallback；统一登录按角色分流；admin 五页 + user 两页；四硬指标；暗色持久化）。记明「前后端两份计划均已落地」。

- [ ] **Step 4: 更新 `docs/modules.md` 与 `docs/architecture.md`**

modules.md 加 `web/`（前端工程结构）与 `internal/server/console.go`（embed 伺服）两节。architecture.md 补「控制台部署形态」：开发期 Vite proxy、生产期 embed 单二进制、SPA fallback、前端非安全边界（授权在后端）。

- [ ] **Step 5: 更新 `CLAUDE.md`**

- 「常用命令」加前端构建：`cd web && npm install`、`npm run dev`（开发，proxy 到 8080）、`npm run build`（产物进 `internal/server/web_dist/`，随 `go build` embed）。
- 「目录约定」加 `web/` 与 `internal/server/console.go`。
- 「开发进度」追加第 ⑮ 步。

- [ ] **Step 6: 更新持久记忆**

`linapi-progress.md` 加第 ⑮ 步控制台完成（技术栈、部署形态、构建流程）。`MEMORY.md` 对应 hook 补一句「已含 React 控制台（web/），单二进制 /console」。

- [ ] **Step 7: 最终确认全绿并提交**

Run: `cd web && npm run build && cd .. && CGO_ENABLED=1 go test -race ./... && go build ./...`
Expected: 全绿。

```bash
git add docs/ CLAUDE.md web/ internal/server/web_dist/
git commit -m "docs: 同步 Web 控制台（第 15 步）进度与构建说明"
```

---

## 自查（Self-Review）

**Spec 覆盖核对**（对照 [admin-console.md](../specs/admin-console.md) §6、§8 前端）：
- 嵌入式单二进制 + `/console` + SPA fallback → Task 2 ✅
- Vite + React + TS + Semi + react-router → Task 1 ✅
- 统一登录按角色分流 + `/auth/me` 恢复 + 401 统一踢回 → Task 4/10 ✅
- admin 五页（概览/用户/渠道/账户/设置）→ Task 11/12 ✅
- user 两页（我的概览/我的密钥）→ Task 13 ✅
- 导航按角色渲染 → Task 9 ✅
- 四硬指标（三态/反馈/校验/响应式）→ DataTable(Task 8) + useAsyncData(Task 6) + ConfirmButton(Task 7) 贯穿各页，Task 11 样板 + 各页规格逐条落点 ✅
- 密钥明文仅回显一次 + 掩码 → PlaintextKeyModal(Task 7) + Users(Task 11) + PortalKeys(Task 13) ✅
- 暗色一等公民 + 持久化 → Task 5 ✅
- 主色定制不裸用默认皮肤 → Task 5 tokens.css ✅
- 越权：`/me/keys` 不传 user_id → Task 3 endpoints（me.createKey 无 user 参数）✅
- 前端非安全边界（后端授权）→ Task 7 ProtectedRoute 注释 + Task 13 验证 ✅

**占位符扫描**：脚手架/配置/API/共享组件/登录页/用户页/概览页/设置页/门户页均为完整代码；渠道页与账户页以完整列/字段/校验/端点/硬指标规格给出（复用已给完整代码的共享组件与 Users 样板），无 TBD/TODO。

**类型一致性核对**：`api.*` 方法签名与 `types.ts` 一致；`MeInfo.role` 联合类型 `'admin'|'user'` 在 auth store、ProtectedRoute、Layout 一致；`CreatedKey.api_key` 字段名与 PlaintextKeyModal 用法一致；`DataTable` 泛型约束 `T extends Record<string, unknown>` 与各页 `User`/`APIKey`/`Channel`/`Account` 兼容。

**一处需实现时留意（已在 Task 10 Step 1 注明）**：登录页「是否显示注册入口」本期从简（始终显示，点击后由后端 403 兜底）。若要精确，后端需加一个公开的 `GET /auth/registration-status`——这属 Plan 1 的可选增强，不阻塞本计划；实现者照 Task 10 注释处理即可。

## 执行交接

计划已保存到 `docs/superpowers/plans/2026-07-10-admin-console-frontend.md`。这是第 2 份（前端），依赖第 1 份（后端）先完成。

两种执行方式：

1. **子代理驱动（推荐）** —— 每任务派新子代理实现，任务间复核。前端任务多为「建文件 + 构建验证」，适合子代理并按 commit 边界推进。
2. **内联执行** —— 本会话按批次执行，带检查点复核。

**建议整体推进顺序**：先执行 Plan 1（后端，16 个任务）→ 验证 `-race` 全绿 → 再执行 Plan 2（前端，14 个任务）。因为前端 Task 2 起就需要后端 `/auth`、`/admin`、`/me` 端点做端到端验证。

两份计划都要执行吗？选哪种执行方式？（或先执行 Plan 1，回头再定 Plan 2？）
