# LinAPI 管理控制台 — 后端认证层实现计划（Plan 1/2）

> **状态（2026-07-11）**：Plan 1 后端已实现；本文件保留为 2026-07-10 的实施计划，未逐项回填复选框或示例代码。当前实现与剩余审查项以 [`../../progress.md`](../../progress.md) 和 [`../../reviews/2026-07-10-comprehensive-readonly-audit.md`](../../reviews/2026-07-10-comprehensive-readonly-audit.md) 为准。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 LinAPI 网关补齐统一账户认证体系（账户/密码/角色/会话），并把 `/admin/*` 从裸 token 鉴权切换为「会话 + 角色」，新增用户自助 `/me/*` 与认证 `/auth/*` 端点。

**Architecture:** 新增 `internal/account` 包（账户 + 系统设置的领域模型与双实现：PG + 内存，沿用现有 `store`/`admin` 的双实现模式）。会话用 Redis 存不透明 token + HttpOnly Cookie。新增 `SessionAuth`/`RequireRole` 中间件替换 `AdminAuth`。建 user 账户时原子地连带创建计费实体（`users` 表）。

**Tech Stack:** Go + Gin、pgx/v5（手写 sqlc 同构产物）、go-redis/v9、golang.org/x/crypto/bcrypt。

## Global Constraints

以下为全计划共同约束，每个任务都隐含遵守（值逐字取自 spec [docs/superpowers/specs/admin-console.md](../specs/admin-console.md)）：

- **中文注释**：所有新增代码沿用项目中文注释风格。
- **schema 双写**：改表结构须同步 `db/schema.sql`（sqlc 源）与 `internal/db/schema.sql`（运行时 embed 迁移副本）两份。
- **密码哈希**：`golang.org/x/crypto/bcrypt`，cost 默认（bcrypt.DefaultCost=10）；绝不存明文、绝不用 MD5/SHA 等快哈希。
- **会话安全**：session ID = 32 字节随机 hex；Redis key `session:<id>`；Cookie 属性 `HttpOnly + Secure + SameSite=Strict`，不放 localStorage；TTL 默认 24h，「记住我」7d（Cookie Max-Age 同步）；Redis 不可用时登录 fail-closed（不降级为无鉴权）。
- **角色**：仅 `admin` 与 `user` 两种（字符串常量）。
- **越权硬约束**：`POST /me/keys` 绑定用户强制取自 session 的 external_id，完全忽略前端传值；`/me/keys/:keyid` 启停/删除先校验 `key.user_id == session.external_id`，不属于返回 404。
- **预留字段（存而不用）**：`accounts.group_name TEXT DEFAULT 'default'`、`users.rate_multiplier INT DEFAULT 100`；本期落库即可，不接入任何计费/分组逻辑。
- **金额单位**：BIGINT 存最小计费单位；倍率用整数百分比（100 = 1.00x）避免浮点。
- **计费实体一致性**：建 user 账户 = 建账户 + 建计费实体 + 回填关联，任一步失败整体失败、不留孤儿（DB 模式用事务；内存模式失败回滚）。
- **验证命令**：`CGO_ENABLED=1 go test -race ./...` 须全绿（cgo 需 C 编译器，gcc 在 `C:\ProgramData\mingw64\mingw64\bin`）。

---

## 文件结构

**新增文件：**
- `internal/account/account.go` — 领域模型（`Account`、`Role` 常量）、`AccountStore` 接口、sentinel error（`ErrNotFound`/`ErrConflict`）、编译期断言。
- `internal/account/settings.go` — `Settings` 领域模型（`RegistrationEnabled`、`NewUserInitialBalance`）、`SettingsStore` 接口、KV 键常量与默认值。
- `internal/account/password.go` — bcrypt 哈希/校验封装（`HashPassword`/`CheckPassword`）。
- `internal/account/memory.go` — `AccountStore` + `SettingsStore` 的内存实现；建 user 账户连带在注入的 `store.MemoryStore` 建计费用户。
- `internal/account/postgres.go` — `AccountStore` + `SettingsStore` 的 PG 实现（依赖扩展后的 `db.Querier`）。
- `internal/account/memory_test.go`、`internal/account/settings_test.go`、`internal/account/password_test.go` — 单测。
- `internal/session/session.go` — Redis 会话管理器（`Create`/`Get`/`Delete`），`SessionData` 结构。
- `internal/session/session_test.go` — 用 miniredis 的单测。
- `internal/middleware/session_auth.go` — `SessionAuth`、`RequireRole` 中间件 + context 取值辅助。
- `internal/middleware/session_auth_test.go` — 单测。
- `internal/server/auth_handlers.go` — `/auth/*`（register/login/logout/me）处理器。
- `internal/server/me_handlers.go` — `/me/*`（profile/keys CRUD）处理器，含越权校验。
- `internal/server/account_handlers.go` — `/admin/accounts`、`/admin/settings` 处理器。
- `internal/server/auth_handlers_test.go`、`internal/server/me_handlers_test.go`、`internal/server/account_handlers_test.go` — HTTP 层端到端测试。
- `internal/db/accounts.sql.go`、`internal/db/settings.sql.go` — 手写 sqlc 同构查询。

**修改文件：**
- `db/schema.sql` + `internal/db/schema.sql` — 加 `accounts`、`settings` 表；`users` 表加 `rate_multiplier` 列。
- `internal/db/models.go` — 加 `Account`、`Setting` 模型；`User` 加 `RateMultiplier` 字段。
- `internal/db/querier.go` — `Querier` 接口加 accounts/settings 方法。
- `internal/config/config.go` — `AdminConfig` 去掉 `Token`/`LoopbackOnly`，加 `Bootstrap`（username/password）；`setDefaults` 同步。
- `internal/server/server.go` — `Deps` 加 `Account`/`Settings`/`Session`；`registerRoutes` 加 `/auth`、`/me`；`registerAdminRoutes` 改 `SessionAuth+RequireRole`。
- `cmd/linapi/main.go` — 装配 account/settings store、session 管理器、bootstrap 管理员、注入新 Deps。
- `internal/middleware/admin_auth.go` + `admin_auth_test.go` — 删除（`AdminAuth` 退役）。
- `config.example.yaml` — admin 段改文档。

---

### Task 1: 数据库 schema（accounts / settings 表 + users.rate_multiplier）

**Files:**
- Modify: `db/schema.sql`（追加两表 + 改 users 表）
- Modify: `internal/db/schema.sql`（同步，运行时 embed 迁移副本）

**Interfaces:**
- Consumes: 无（第一个任务）。
- Produces: 数据库表 `accounts`、`settings`，以及 `users.rate_multiplier` 列，供 Task 2/3 的 sqlc 查询与模型使用。

- [ ] **Step 1: 在 `db/schema.sql` 的 users 表定义中加 rate_multiplier 列**

找到 `db/schema.sql` 中 users 表的 `balance` 行，在 `enabled` 行之前插入预留列：

```sql
    balance     BIGINT      NOT NULL DEFAULT 0,
    -- rate_multiplier 预留：单用户定价倍率覆盖，百分比整数（100=1.00x），本期存而不用。
    rate_multiplier INT     NOT NULL DEFAULT 100,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
```

- [ ] **Step 2: 在 `db/schema.sql` 末尾追加 accounts 与 settings 表**

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
    -- group_name 预留：定价分组名，本期存而不用。
    group_name    TEXT        NOT NULL DEFAULT 'default',
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_accounts_role ON accounts (role);

-- 系统设置：运行时可变的 KV 配置，控制台可改、即时生效。
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- [ ] **Step 3: 把 Step 1 + Step 2 的完全相同改动同步到 `internal/db/schema.sql`**

`internal/db/schema.sql` 是运行时 embed 的迁移副本，内容须与 `db/schema.sql` 一致。对它做与 Step 1、Step 2 逐字相同的改动。

- [ ] **Step 4: 校验两份 schema 一致**

Run: `diff db/schema.sql internal/db/schema.sql`
Expected: 无输出（两份完全一致）。若项目约定两份本就有差异，则至少确认新增的三处改动在两份中逐字相同。

- [ ] **Step 5: 编译验证（schema 是纯 SQL，仅确认未破坏 embed）**

Run: `go build ./internal/db/`
Expected: 编译通过，无输出。

- [ ] **Step 6: Commit**

```bash
git add db/schema.sql internal/db/schema.sql
git commit -m "feat(db): 加 accounts/settings 表与 users.rate_multiplier 预留列"
```

---

### Task 2: db 层 — accounts / settings 模型与查询

**Files:**
- Modify: `internal/db/models.go`（加 `Account`、`Setting`；`User` 加 `RateMultiplier`）
- Create: `internal/db/accounts.sql.go`
- Create: `internal/db/settings.sql.go`
- Modify: `internal/db/querier.go`（`Querier` 接口加新方法）

**Interfaces:**
- Consumes: Task 1 的 `accounts`、`settings` 表。
- Produces（供 Task 5 的 PG 实现使用）：
  - `db.Account` 结构 + `db.Setting` 结构
  - `Queries.CreateAccount(ctx, CreateAccountParams) (Account, error)`
  - `Queries.GetAccountByUsername(ctx, username string) (Account, error)`
  - `Queries.GetAccountByID(ctx, id int64) (Account, error)`
  - `Queries.ListAccounts(ctx, ListAccountsParams) ([]Account, error)`
  - `Queries.CountAccounts(ctx) (int64, error)`
  - `Queries.SetAccountEnabled(ctx, SetAccountEnabledParams) (Account, error)`
  - `Queries.UpdateAccountPassword(ctx, UpdateAccountPasswordParams) error`
  - `Queries.GetSetting(ctx, key string) (Setting, error)`
  - `Queries.UpsertSetting(ctx, UpsertSettingParams) error`
  - `CreateAccountParams{Username, PasswordHash, Role string; ExternalID pgtype.Text}`

- [ ] **Step 1: 在 `internal/db/models.go` 加 Account 与 Setting 模型，并给 User 加预留字段**

在 `User` 结构的 `Balance` 后加一行（现有查询不 select 它，故不影响既有 Scan）：

```go
	Balance    int64              `json:"balance"`
	// RateMultiplier 是预留的单用户定价倍率（百分比，100=1.00x）；本期存而不用，
	// 现有查询不 select 该列，故此字段暂不参与任何 Scan。
	RateMultiplier int32          `json:"rate_multiplier"`
	Enabled    bool               `json:"enabled"`
```

在文件末尾追加：

```go
// Account 对应 accounts 表：控制台登录账户（与计费实体 users 分离）。
// PasswordHash 存 bcrypt 哈希，绝不落明文。ExternalID 对 admin 角色可空。
type Account struct {
	ID           int64              `json:"id"`
	Username     string             `json:"username"`
	PasswordHash string             `json:"password_hash"`
	Role         string             `json:"role"`
	ExternalID   pgtype.Text        `json:"external_id"`
	GroupName    string             `json:"group_name"`
	Enabled      bool               `json:"enabled"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
	UpdatedAt    pgtype.Timestamptz `json:"updated_at"`
}

// Setting 对应 settings 表：运行时可变的 KV 系统设置。
type Setting struct {
	Key       string             `json:"key"`
	Value     string             `json:"value"`
	UpdatedAt pgtype.Timestamptz `json:"updated_at"`
}
```

- [ ] **Step 2: 创建 `internal/db/accounts.sql.go`**

```go
package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const createAccount = `-- name: CreateAccount :one
INSERT INTO accounts (username, password_hash, role, external_id)
VALUES ($1, $2, $3, $4)
RETURNING id, username, password_hash, role, external_id, group_name, enabled, created_at, updated_at
`

// CreateAccountParams 是 CreateAccount 的入参。
type CreateAccountParams struct {
	Username     string      `json:"username"`
	PasswordHash string      `json:"password_hash"`
	Role         string      `json:"role"`
	ExternalID   pgtype.Text `json:"external_id"`
}

// CreateAccount 新建登录账户。
func (q *Queries) CreateAccount(ctx context.Context, arg CreateAccountParams) (Account, error) {
	row := q.db.QueryRow(ctx, createAccount, arg.Username, arg.PasswordHash, arg.Role, arg.ExternalID)
	var i Account
	err := row.Scan(&i.ID, &i.Username, &i.PasswordHash, &i.Role, &i.ExternalID, &i.GroupName, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

const getAccountByUsername = `-- name: GetAccountByUsername :one
SELECT id, username, password_hash, role, external_id, group_name, enabled, created_at, updated_at
FROM accounts WHERE username = $1
`

// GetAccountByUsername 按登录名取账户（登录校验用）。
func (q *Queries) GetAccountByUsername(ctx context.Context, username string) (Account, error) {
	row := q.db.QueryRow(ctx, getAccountByUsername, username)
	var i Account
	err := row.Scan(&i.ID, &i.Username, &i.PasswordHash, &i.Role, &i.ExternalID, &i.GroupName, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

const getAccountByID = `-- name: GetAccountByID :one
SELECT id, username, password_hash, role, external_id, group_name, enabled, created_at, updated_at
FROM accounts WHERE id = $1
`

// GetAccountByID 按主键取账户。
func (q *Queries) GetAccountByID(ctx context.Context, id int64) (Account, error) {
	row := q.db.QueryRow(ctx, getAccountByID, id)
	var i Account
	err := row.Scan(&i.ID, &i.Username, &i.PasswordHash, &i.Role, &i.ExternalID, &i.GroupName, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

const listAccounts = `-- name: ListAccounts :many
SELECT id, username, password_hash, role, external_id, group_name, enabled, created_at, updated_at
FROM accounts ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2
`

// ListAccountsParams 是 ListAccounts 的分页入参。
type ListAccountsParams struct {
	Limit  int32 `json:"limit"`
	Offset int32 `json:"offset"`
}

// ListAccounts 分页列出账户。
func (q *Queries) ListAccounts(ctx context.Context, arg ListAccountsParams) ([]Account, error) {
	rows, err := q.db.Query(ctx, listAccounts, arg.Limit, arg.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Account{}
	for rows.Next() {
		var i Account
		if err := rows.Scan(&i.ID, &i.Username, &i.PasswordHash, &i.Role, &i.ExternalID, &i.GroupName, &i.Enabled, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

const countAccounts = `-- name: CountAccounts :one
SELECT count(*) FROM accounts
`

// CountAccounts 统计账户数（概览页与 bootstrap 判断用）。
func (q *Queries) CountAccounts(ctx context.Context) (int64, error) {
	row := q.db.QueryRow(ctx, countAccounts)
	var n int64
	err := row.Scan(&n)
	return n, err
}

const setAccountEnabled = `-- name: SetAccountEnabled :one
UPDATE accounts SET enabled = $2, updated_at = now()
WHERE id = $1
RETURNING id, username, password_hash, role, external_id, group_name, enabled, created_at, updated_at
`

// SetAccountEnabledParams 是 SetAccountEnabled 的入参。
type SetAccountEnabledParams struct {
	ID      int64 `json:"id"`
	Enabled bool  `json:"enabled"`
}

// SetAccountEnabled 启停账户。
func (q *Queries) SetAccountEnabled(ctx context.Context, arg SetAccountEnabledParams) (Account, error) {
	row := q.db.QueryRow(ctx, setAccountEnabled, arg.ID, arg.Enabled)
	var i Account
	err := row.Scan(&i.ID, &i.Username, &i.PasswordHash, &i.Role, &i.ExternalID, &i.GroupName, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

const updateAccountPassword = `-- name: UpdateAccountPassword :exec
UPDATE accounts SET password_hash = $2, updated_at = now() WHERE id = $1
`

// UpdateAccountPasswordParams 是 UpdateAccountPassword 的入参。
type UpdateAccountPasswordParams struct {
	ID           int64  `json:"id"`
	PasswordHash string `json:"password_hash"`
}

// UpdateAccountPassword 改密（存新的 bcrypt 哈希）。
func (q *Queries) UpdateAccountPassword(ctx context.Context, arg UpdateAccountPasswordParams) error {
	_, err := q.db.Exec(ctx, updateAccountPassword, arg.ID, arg.PasswordHash)
	return err
}
```

- [ ] **Step 3: 创建 `internal/db/settings.sql.go`**

```go
package db

import "context"

const getSetting = `-- name: GetSetting :one
SELECT key, value, updated_at FROM settings WHERE key = $1
`

// GetSetting 取单个设置项。
func (q *Queries) GetSetting(ctx context.Context, key string) (Setting, error) {
	row := q.db.QueryRow(ctx, getSetting, key)
	var i Setting
	err := row.Scan(&i.Key, &i.Value, &i.UpdatedAt)
	return i, err
}

const upsertSetting = `-- name: UpsertSetting :exec
INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
`

// UpsertSettingParams 是 UpsertSetting 的入参。
type UpsertSettingParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UpsertSetting 写入/更新设置项（幂等）。
func (q *Queries) UpsertSetting(ctx context.Context, arg UpsertSettingParams) error {
	_, err := q.db.Exec(ctx, upsertSetting, arg.Key, arg.Value)
	return err
}
```

- [ ] **Step 4: 在 `internal/db/querier.go` 的 `Querier` 接口追加方法**

在 `// usage_logs` 段之前插入：

```go
	// accounts
	CreateAccount(ctx context.Context, arg CreateAccountParams) (Account, error)
	GetAccountByUsername(ctx context.Context, username string) (Account, error)
	GetAccountByID(ctx context.Context, id int64) (Account, error)
	ListAccounts(ctx context.Context, arg ListAccountsParams) ([]Account, error)
	CountAccounts(ctx context.Context) (int64, error)
	SetAccountEnabled(ctx context.Context, arg SetAccountEnabledParams) (Account, error)
	UpdateAccountPassword(ctx context.Context, arg UpdateAccountPasswordParams) error
	// settings
	GetSetting(ctx context.Context, key string) (Setting, error)
	UpsertSetting(ctx context.Context, arg UpsertSettingParams) error
```

- [ ] **Step 5: 编译验证**

Run: `go build ./internal/db/`
Expected: 编译通过。`var _ Querier = (*Queries)(nil)` 断言成立（新方法已在 Queries 上实现）。

- [ ] **Step 6: 同步查询到 sqlc 源 `db/query.sql`（保持生成源一致）**

把 Step 2/3 中各查询的 SQL（`-- name:` 注释块 + SQL 语句）追加到 `db/query.sql`，与手写产物对应。这样将来 `sqlc generate` 能原样重生成。

- [ ] **Step 7: Commit**

```bash
git add internal/db/ db/query.sql
git commit -m "feat(db): accounts/settings 查询与模型（手写 sqlc 同构产物）"
```

---

### Task 3: 密码哈希封装

**Files:**
- Create: `internal/account/password.go`
- Create: `internal/account/password_test.go`

**Interfaces:**
- Consumes: `golang.org/x/crypto/bcrypt`（已在 go.mod？若无则 `go get`）。
- Produces（供 Task 4、7、bootstrap 使用）：
  - `account.HashPassword(plain string) (string, error)`
  - `account.CheckPassword(hash, plain string) bool`
  - `account.ErrPasswordTooShort error`（min 8）
  - `account.MinPasswordLen = 8` 常量

- [ ] **Step 1: 写失败测试 `internal/account/password_test.go`**

```go
package account

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	if hash == "s3cret-pw" {
		t.Fatal("哈希不得等于明文")
	}
	if !CheckPassword(hash, "s3cret-pw") {
		t.Error("正确密码应校验通过")
	}
	if CheckPassword(hash, "wrong-pw") {
		t.Error("错误密码不应通过")
	}
}

func TestHashPasswordTooShort(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrPasswordTooShort {
		t.Fatalf("短密码应返回 ErrPasswordTooShort, 得到 %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/account/ -run TestHashAndCheckPassword -v`
Expected: 编译失败（`HashPassword` 未定义）。

- [ ] **Step 3: 实现 `internal/account/password.go`**

```go
package account

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLen 是密码最小长度（注册/改密时后端强校验，前端另有前置校验）。
const MinPasswordLen = 8

// ErrPasswordTooShort 表示密码长度不足。
var ErrPasswordTooShort = errors.New("account: 密码长度不足")

// HashPassword 用 bcrypt（默认 cost）哈希明文密码。绝不存明文、绝不用快哈希。
func HashPassword(plain string) (string, error) {
	if len(plain) < MinPasswordLen {
		return "", ErrPasswordTooShort
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword 校验明文与 bcrypt 哈希是否匹配。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
```

- [ ] **Step 4: 确保依赖就位并运行测试**

Run: `go get golang.org/x/crypto/bcrypt && go test ./internal/account/ -v`
Expected: PASS（两个测试通过）。

- [ ] **Step 5: Commit**

```bash
git add internal/account/password.go internal/account/password_test.go go.mod go.sum
git commit -m "feat(account): bcrypt 密码哈希封装"
```

---

### Task 4: account 领域模型与接口（AccountStore / SettingsStore）

**Files:**
- Create: `internal/account/account.go`
- Create: `internal/account/settings.go`

**Interfaces:**
- Consumes: 无跨任务依赖（纯定义）。
- Produces（Task 5/6 实现、Task 7~9 消费）：
  - 角色常量 `account.RoleAdmin = "admin"`、`account.RoleUser = "user"`
  - sentinel `account.ErrNotFound`、`account.ErrConflict`、`account.ErrInvalidRole`
  - `account.Account` 结构（`ID int64; Username, Role, ExternalID, GroupName string; Enabled bool; CreatedAt time.Time`）—— 注意**不含 PasswordHash**（领域视图不外泄哈希）
  - `account.Credentials` 结构（`Account` + `PasswordHash string`），仅登录校验内部用
  - `account.CreateAccountInput{Username, PasswordHash, Role, ExternalID string}`
  - `AccountStore` 接口（见下）
  - `account.Settings{RegistrationEnabled bool; NewUserInitialBalance int64}`
  - `SettingsStore` 接口
  - KV 键常量 `KeyRegistrationEnabled = "registration_enabled"`、`KeyNewUserInitialBalance = "new_user_initial_balance"`

- [ ] **Step 1: 创建 `internal/account/account.go`**

```go
// Package account 提供控制台的账户与系统设置领域模型及数据访问抽象。
//
// 设计意图：登录账户（accounts 表）与计费实体（users 表）职责分离——
// 账户管鉴权（用户名/密码/角色），计费实体管额度。建 user 账户时连带
// 创建计费实体并回填 external_id 关联。内存与 PostgreSQL 各实现一套接口。
package account

import (
	"context"
	"errors"
	"time"
)

// 角色：仅两种。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// sentinel error。
var (
	ErrNotFound    = errors.New("account: 账户不存在")
	ErrConflict    = errors.New("account: 用户名已存在")
	ErrInvalidRole = errors.New("account: 非法角色")
)

// ValidRole 判断角色是否合法。
func ValidRole(role string) bool {
	return role == RoleAdmin || role == RoleUser
}

// Account 是账户的领域视图（刻意不含 password_hash，避免哈希外泄）。
type Account struct {
	ID         int64     `json:"id"`
	Username   string    `json:"username"`
	Role       string    `json:"role"`
	ExternalID string    `json:"external_id,omitempty"`
	GroupName  string    `json:"group_name"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

// Credentials 是登录校验用的内部结构：账户视图 + 密码哈希。
// 仅 GetCredentials 返回，绝不序列化给客户端。
type Credentials struct {
	Account
	PasswordHash string
}

// CreateAccountInput 是新建账户的入参。PasswordHash 已是 bcrypt 哈希。
// ExternalID 对 user 角色为其计费实体标识；admin 角色可空。
type CreateAccountInput struct {
	Username     string
	PasswordHash string
	Role         string
	ExternalID   string
}

// AccountStore 是账户数据访问接口。实现须并发安全。
//
// 约定：用户名冲突映射为 ErrConflict；目标不存在映射为 ErrNotFound。
type AccountStore interface {
	// CreateUserAccount 原子地建 user 账户 + 计费实体（余额 initialBalance），
	// 回填 external_id 关联。任一步失败整体失败、不留孤儿。返回账户视图。
	CreateUserAccount(ctx context.Context, username, passwordHash string, initialBalance int64) (Account, error)
	// CreateAccount 直接建账户（供 bootstrap 建 admin，不连带计费实体）。
	CreateAccount(ctx context.Context, in CreateAccountInput) (Account, error)
	// GetCredentials 按用户名取账户 + 密码哈希（登录校验）。
	GetCredentials(ctx context.Context, username string) (Credentials, error)
	// GetByID 按主键取账户视图。
	GetByID(ctx context.Context, id int64) (Account, error)
	// GetByUsername 按用户名取账户视图。
	GetByUsername(ctx context.Context, username string) (Account, error)
	// ListAccounts 分页列出账户。
	ListAccounts(ctx context.Context, limit, offset int) ([]Account, error)
	// CountAccounts 统计账户总数。
	CountAccounts(ctx context.Context) (int64, error)
	// SetEnabled 启停账户。
	SetEnabled(ctx context.Context, id int64, enabled bool) (Account, error)
	// UpdatePassword 改密（传入已哈希的新密码）。
	UpdatePassword(ctx context.Context, id int64, passwordHash string) error
}
```

- [ ] **Step 2: 创建 `internal/account/settings.go`**

```go
package account

import (
	"context"
	"strconv"
)

// 系统设置的 KV 键。
const (
	KeyRegistrationEnabled   = "registration_enabled"
	KeyNewUserInitialBalance = "new_user_initial_balance"
)

// 默认值：注册默认关闭（安全默认），初始额度默认 0。
const (
	DefaultRegistrationEnabled   = false
	DefaultNewUserInitialBalance = int64(0)
)

// Settings 是系统设置的领域视图。
type Settings struct {
	RegistrationEnabled   bool  `json:"registration_enabled"`
	NewUserInitialBalance int64 `json:"new_user_initial_balance"`
}

// SettingsStore 是系统设置数据访问接口。实现须并发安全。
type SettingsStore interface {
	// Get 读取全部设置（缺失的键回退默认值）。
	Get(ctx context.Context) (Settings, error)
	// Put 覆盖写入全部设置。
	Put(ctx context.Context, s Settings) error
}

// parseBool / formatBool / parseInt64 是 KV 值（TEXT）与类型化字段的转换辅助。
func parseBool(s string, def bool) bool {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

func formatBool(b bool) string { return strconv.FormatBool(b) }

func parseInt64(s string, def int64) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

func formatInt64(n int64) string { return strconv.FormatInt(n, 10) }
```

- [ ] **Step 3: 编译验证**

Run: `go build ./internal/account/`
Expected: 编译通过（接口尚无实现，但定义本身可编译）。

- [ ] **Step 4: Commit**

```bash
git add internal/account/account.go internal/account/settings.go
git commit -m "feat(account): 账户与系统设置领域模型及接口"
```

---

### Task 5: account 内存实现（AccountStore + SettingsStore）

**Files:**
- Create: `internal/account/memory.go`
- Create: `internal/account/memory_test.go`

**Interfaces:**
- Consumes: Task 4 的接口与类型；`store.MemoryStore`（用其 `AdminCreateUser` 建计费实体）。
- Produces（供 Task 7~9 与 main 装配）：
  - `account.NewMemoryStore(base *store.MemoryStore) *MemoryStore`（实现 `AccountStore` + `SettingsStore`）
  - external_id 生成规则：user 账户的 external_id 直接用 username（内存模式够用、可读）

- [ ] **Step 1: 写失败测试 `internal/account/memory_test.go`**

```go
package account

import (
	"context"
	"errors"
	"testing"

	"linapi/internal/store"
)

func newMemStore() *MemoryStore {
	return NewMemoryStore(store.NewMemoryStore(nil))
}

func TestMemoryCreateUserAccountBindsBilling(t *testing.T) {
	base := store.NewMemoryStore(nil)
	m := NewMemoryStore(base)
	ctx := context.Background()

	acc, err := m.CreateUserAccount(ctx, "alice", "hash1", 500)
	if err != nil {
		t.Fatalf("CreateUserAccount 失败: %v", err)
	}
	if acc.Role != RoleUser || acc.ExternalID == "" {
		t.Fatalf("user 账户应有角色与 external_id: %+v", acc)
	}
	// 计费实体应已建：底层 store 能查到余额 500。
	bal, _ := base.Balance(ctx, acc.ExternalID)
	if bal != 500 {
		t.Errorf("计费实体余额应为 500, 得到 %d", bal)
	}

	// 用户名重复应 ErrConflict。
	if _, err := m.CreateUserAccount(ctx, "alice", "hash2", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复用户名应 ErrConflict, 得到 %v", err)
	}
}

func TestMemoryGetCredentials(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	if _, err := m.CreateUserAccount(ctx, "bob", "bcrypt-hash", 0); err != nil {
		t.Fatalf("建账户失败: %v", err)
	}
	cred, err := m.GetCredentials(ctx, "bob")
	if err != nil {
		t.Fatalf("GetCredentials 失败: %v", err)
	}
	if cred.PasswordHash != "bcrypt-hash" {
		t.Errorf("密码哈希不符: %q", cred.PasswordHash)
	}
	if _, err := m.GetCredentials(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查不存在账户应 ErrNotFound, 得到 %v", err)
	}
}

func TestMemoryCreateAdminAccount(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	acc, err := m.CreateAccount(ctx, CreateAccountInput{
		Username: "root", PasswordHash: "h", Role: RoleAdmin,
	})
	if err != nil {
		t.Fatalf("建 admin 账户失败: %v", err)
	}
	if acc.Role != RoleAdmin || acc.ExternalID != "" {
		t.Fatalf("admin 账户不应有 external_id: %+v", acc)
	}
	if _, err := m.CreateAccount(ctx, CreateAccountInput{Username: "x", Role: "bogus"}); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("非法角色应 ErrInvalidRole, 得到 %v", err)
	}
}

func TestMemorySetEnabledAndPassword(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	acc, _ := m.CreateUserAccount(ctx, "carol", "h", 0)

	off, err := m.SetEnabled(ctx, acc.ID, false)
	if err != nil || off.Enabled {
		t.Fatalf("SetEnabled(false) 失败: %+v err=%v", off, err)
	}
	if err := m.UpdatePassword(ctx, acc.ID, "newhash"); err != nil {
		t.Fatalf("UpdatePassword 失败: %v", err)
	}
	cred, _ := m.GetCredentials(ctx, "carol")
	if cred.PasswordHash != "newhash" {
		t.Errorf("改密未生效: %q", cred.PasswordHash)
	}
	if _, err := m.SetEnabled(ctx, 99999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("启停不存在账户应 ErrNotFound, 得到 %v", err)
	}
}

func TestMemorySettings(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()

	// 默认值：注册关闭、初始额度 0。
	s, err := m.Get(ctx)
	if err != nil {
		t.Fatalf("Get 设置失败: %v", err)
	}
	if s.RegistrationEnabled || s.NewUserInitialBalance != 0 {
		t.Fatalf("默认设置应为 关闭/0, 得到 %+v", s)
	}

	// 写入后读回。
	if err := m.Put(ctx, Settings{RegistrationEnabled: true, NewUserInitialBalance: 1000}); err != nil {
		t.Fatalf("Put 设置失败: %v", err)
	}
	s, _ = m.Get(ctx)
	if !s.RegistrationEnabled || s.NewUserInitialBalance != 1000 {
		t.Fatalf("设置未持久化: %+v", s)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/account/ -run TestMemory -v`
Expected: 编译失败（`NewMemoryStore` 未定义）。

- [ ] **Step 3: 实现 `internal/account/memory.go`**

```go
package account

import (
	"context"
	"sync"
	"time"

	"linapi/internal/store"
)

// MemoryStore 是 AccountStore + SettingsStore 的内存实现（database.enabled=false 时用）。
//
// 计费实体复用注入的 store.MemoryStore（建 user 账户时在其上建计费用户），
// 使账户体系与热路径共享同一份用户/余额数据。账户与设置由本类型自持。
type MemoryStore struct {
	base *store.MemoryStore

	mu       sync.RWMutex
	byID     map[int64]*memAccount
	byName   map[string]*memAccount
	nextID   int64
	settings Settings
}

type memAccount struct {
	acc  Account
	hash string
}

// NewMemoryStore 包装一个 store.MemoryStore。
func NewMemoryStore(base *store.MemoryStore) *MemoryStore {
	return &MemoryStore{
		base:     base,
		byID:     make(map[int64]*memAccount),
		byName:   make(map[string]*memAccount),
		nextID:   1,
		settings: Settings{RegistrationEnabled: DefaultRegistrationEnabled, NewUserInitialBalance: DefaultNewUserInitialBalance},
	}
}

// CreateUserAccount 建 user 账户 + 计费实体（external_id 用 username）。
func (m *MemoryStore) CreateUserAccount(_ context.Context, username, passwordHash string, initialBalance int64) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byName[username]; ok {
		return Account{}, ErrConflict
	}
	// 先建计费实体（external_id = username）；失败则不建账户（不留孤儿）。
	if _, err := m.base.AdminCreateUser(username, initialBalance, true); err != nil {
		// 计费实体已存在等同用户名冲突。
		return Account{}, ErrConflict
	}
	return m.insertLocked(username, passwordHash, RoleUser, username), nil
}

// CreateAccount 直接建账户（bootstrap 建 admin，不连带计费实体）。
func (m *MemoryStore) CreateAccount(_ context.Context, in CreateAccountInput) (Account, error) {
	if !ValidRole(in.Role) {
		return Account{}, ErrInvalidRole
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byName[in.Username]; ok {
		return Account{}, ErrConflict
	}
	return m.insertLocked(in.Username, in.PasswordHash, in.Role, in.ExternalID), nil
}

// insertLocked 在持锁下插入账户，返回视图。调用方须已校验用户名唯一。
func (m *MemoryStore) insertLocked(username, hash, role, externalID string) Account {
	acc := Account{
		ID:         m.nextID,
		Username:   username,
		Role:       role,
		ExternalID: externalID,
		GroupName:  "default",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}
	rec := &memAccount{acc: acc, hash: hash}
	m.byID[acc.ID] = rec
	m.byName[username] = rec
	m.nextID++
	return acc
}

func (m *MemoryStore) GetCredentials(_ context.Context, username string) (Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byName[username]
	if !ok {
		return Credentials{}, ErrNotFound
	}
	return Credentials{Account: rec.acc, PasswordHash: rec.hash}, nil
}

func (m *MemoryStore) GetByID(_ context.Context, id int64) (Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byID[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return rec.acc, nil
}

func (m *MemoryStore) GetByUsername(_ context.Context, username string) (Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byName[username]
	if !ok {
		return Account{}, ErrNotFound
	}
	return rec.acc, nil
}

func (m *MemoryStore) ListAccounts(_ context.Context, limit, offset int) ([]Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := make([]Account, 0, len(m.byID))
	for _, rec := range m.byID {
		all = append(all, rec.acc)
	}
	// 按 ID 倒序（近似创建时间倒序）。
	sortAccountsDesc(all)
	return pageAccounts(all, limit, offset), nil
}

func (m *MemoryStore) CountAccounts(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.byID)), nil
}

func (m *MemoryStore) SetEnabled(_ context.Context, id int64, enabled bool) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.byID[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	rec.acc.Enabled = enabled
	return rec.acc, nil
}

func (m *MemoryStore) UpdatePassword(_ context.Context, id int64, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.byID[id]
	if !ok {
		return ErrNotFound
	}
	rec.hash = passwordHash
	return nil
}

func (m *MemoryStore) Get(_ context.Context) (Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings, nil
}

func (m *MemoryStore) Put(_ context.Context, s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = s
	return nil
}
```

- [ ] **Step 4: 加排序/分页辅助到 `internal/account/memory.go` 末尾**

```go
import "sort" // 加到文件 import 块

// sortAccountsDesc 按 ID 倒序（近似创建时间倒序）。
func sortAccountsDesc(a []Account) {
	sort.Slice(a, func(i, j int) bool { return a[i].ID > a[j].ID })
}

// pageAccounts 应用 limit/offset（limit<=0 不限制）。
func pageAccounts(a []Account, limit, offset int) []Account {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(a) {
		return []Account{}
	}
	a = a[offset:]
	if limit > 0 && limit < len(a) {
		a = a[:limit]
	}
	return a
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/account/ -v`
Expected: PASS（全部内存测试 + 密码测试通过）。

- [ ] **Step 6: Commit**

```bash
git add internal/account/memory.go internal/account/memory_test.go
git commit -m "feat(account): AccountStore/SettingsStore 内存实现"
```

---

### Task 6: account PostgreSQL 实现（含建 user 事务）

**Files:**
- Create: `internal/account/postgres.go`
- Create: `internal/account/postgres_test.go`

**Interfaces:**
- Consumes: Task 2 的 `db` 查询与模型；`*pgxpool.Pool`（事务用）；`db.Querier`（非事务读）。
- Produces（供 main 装配）：
  - `account.NewPGStore(pool *pgxpool.Pool) *PGStore`（实现 `AccountStore` + `SettingsStore`）

**说明**：`CreateUserAccount` 用事务把「建 users 计费实体 + 建 accounts 账户」纳入一个原子单元，任一步失败回滚（满足计费实体一致性硬约束）。其余只读/单写方法走连接池。单元测试用 `fakeQuerier` 覆盖可 fake 的方法（GetCredentials/ListAccounts/Settings 的映射）；`CreateUserAccount` 的事务路径依赖真实 PG（本环境无 DB，故不在单测覆盖，靠内存实现的行为测试 + 编译期接口断言 + 真实环境集成验证保障）。

- [ ] **Step 1: 写失败测试 `internal/account/postgres_test.go`（只测可 fake 的方法）**

```go
package account

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"linapi/internal/db"
)

// fakeQuerier 是 db.Querier 的测试替身，只实现本测试触达的方法。
type fakeQuerier struct {
	db.Querier
	getByUsernameFn func(ctx context.Context, u string) (db.Account, error)
	getSettingFn    func(ctx context.Context, k string) (db.Setting, error)
}

func (f *fakeQuerier) GetAccountByUsername(ctx context.Context, u string) (db.Account, error) {
	return f.getByUsernameFn(ctx, u)
}
func (f *fakeQuerier) GetSetting(ctx context.Context, k string) (db.Setting, error) {
	return f.getSettingFn(ctx, k)
}

func TestPGGetCredentials(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{
		getByUsernameFn: func(_ context.Context, u string) (db.Account, error) {
			if u == "ghost" {
				return db.Account{}, pgx.ErrNoRows
			}
			return db.Account{
				ID: 1, Username: u, PasswordHash: "bh", Role: RoleUser,
				ExternalID: pgtype.Text{String: u, Valid: true}, Enabled: true,
			}, nil
		},
	}
	s := &PGStore{q: q}

	cred, err := s.GetCredentials(ctx, "alice")
	if err != nil {
		t.Fatalf("GetCredentials 失败: %v", err)
	}
	if cred.PasswordHash != "bh" || cred.ExternalID != "alice" {
		t.Fatalf("凭证映射错误: %+v", cred)
	}
	if _, err := s.GetCredentials(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound, 得到 %v", err)
	}
}

func TestPGGetSettingsDefaults(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{
		getSettingFn: func(_ context.Context, _ string) (db.Setting, error) {
			return db.Setting{}, pgx.ErrNoRows // 键缺失 -> 回退默认。
		},
	}
	s := &PGStore{q: q}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get 设置失败: %v", err)
	}
	if got.RegistrationEnabled != DefaultRegistrationEnabled || got.NewUserInitialBalance != DefaultNewUserInitialBalance {
		t.Fatalf("缺失键应回退默认, 得到 %+v", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/account/ -run TestPG -v`
Expected: 编译失败（`PGStore` 未定义）。

- [ ] **Step 3: 实现 `internal/account/postgres.go`**

```go
package account

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"linapi/internal/db"
)

// PGStore 是 AccountStore + SettingsStore 的 PostgreSQL 实现。
// pool 供 CreateUserAccount 开事务；q 是绑定连接池的查询器，供非事务读写。
type PGStore struct {
	pool *pgxpool.Pool
	q    db.Querier
}

// NewPGStore 用连接池构造 PGStore。
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, q: db.New(pool)}
}

// mapErr 归一 pgx 写错误：无行 -> ErrNotFound，唯一冲突 -> ErrConflict。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// CreateUserAccount 在事务内建计费实体 + user 账户（external_id=username），原子提交。
func (s *PGStore) CreateUserAccount(ctx context.Context, username, passwordHash string, initialBalance int64) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 提交后 Rollback 为 no-op。

	qtx := db.New(tx)
	// 1. 建计费实体。
	if _, err := qtx.CreateUser(ctx, db.CreateUserParams{
		ExternalID: username, Balance: initialBalance, Enabled: true,
	}); err != nil {
		return Account{}, mapErr(err)
	}
	// 2. 建账户，external_id 关联计费实体。
	acc, err := qtx.CreateAccount(ctx, db.CreateAccountParams{
		Username: username, PasswordHash: passwordHash, Role: RoleUser,
		ExternalID: pgtype.Text{String: username, Valid: true},
	})
	if err != nil {
		return Account{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return accountFromDB(acc), nil
}

// CreateAccount 直接建账户（bootstrap 建 admin）。
func (s *PGStore) CreateAccount(ctx context.Context, in CreateAccountInput) (Account, error) {
	if !ValidRole(in.Role) {
		return Account{}, ErrInvalidRole
	}
	var ext pgtype.Text
	if in.ExternalID != "" {
		ext = pgtype.Text{String: in.ExternalID, Valid: true}
	}
	acc, err := s.q.CreateAccount(ctx, db.CreateAccountParams{
		Username: in.Username, PasswordHash: in.PasswordHash, Role: in.Role, ExternalID: ext,
	})
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) GetCredentials(ctx context.Context, username string) (Credentials, error) {
	acc, err := s.q.GetAccountByUsername(ctx, username)
	if err != nil {
		return Credentials{}, mapErr(err)
	}
	return Credentials{Account: accountFromDB(acc), PasswordHash: acc.PasswordHash}, nil
}

func (s *PGStore) GetByID(ctx context.Context, id int64) (Account, error) {
	acc, err := s.q.GetAccountByID(ctx, id)
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) GetByUsername(ctx context.Context, username string) (Account, error) {
	acc, err := s.q.GetAccountByUsername(ctx, username)
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) ListAccounts(ctx context.Context, limit, offset int) ([]Account, error) {
	rows, err := s.q.ListAccounts(ctx, db.ListAccountsParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, accountFromDB(r))
	}
	return out, nil
}

func (s *PGStore) CountAccounts(ctx context.Context) (int64, error) {
	return s.q.CountAccounts(ctx)
}

func (s *PGStore) SetEnabled(ctx context.Context, id int64, enabled bool) (Account, error) {
	acc, err := s.q.SetAccountEnabled(ctx, db.SetAccountEnabledParams{ID: id, Enabled: enabled})
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	return mapErr(s.q.UpdateAccountPassword(ctx, db.UpdateAccountPasswordParams{ID: id, PasswordHash: passwordHash}))
}

func (s *PGStore) Get(ctx context.Context) (Settings, error) {
	out := Settings{RegistrationEnabled: DefaultRegistrationEnabled, NewUserInitialBalance: DefaultNewUserInitialBalance}
	if v, err := s.q.GetSetting(ctx, KeyRegistrationEnabled); err == nil {
		out.RegistrationEnabled = parseBool(v.Value, DefaultRegistrationEnabled)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, err
	}
	if v, err := s.q.GetSetting(ctx, KeyNewUserInitialBalance); err == nil {
		out.NewUserInitialBalance = parseInt64(v.Value, DefaultNewUserInitialBalance)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, err
	}
	return out, nil
}

func (s *PGStore) Put(ctx context.Context, st Settings) error {
	if err := s.q.UpsertSetting(ctx, db.UpsertSettingParams{Key: KeyRegistrationEnabled, Value: formatBool(st.RegistrationEnabled)}); err != nil {
		return err
	}
	return s.q.UpsertSetting(ctx, db.UpsertSettingParams{Key: KeyNewUserInitialBalance, Value: formatInt64(st.NewUserInitialBalance)})
}

// accountFromDB 把 db.Account 转为领域视图（丢弃 password_hash）。
func accountFromDB(a db.Account) Account {
	return Account{
		ID:         a.ID,
		Username:   a.Username,
		Role:       a.Role,
		ExternalID: a.ExternalID.String,
		GroupName:  a.GroupName,
		Enabled:    a.Enabled,
		CreatedAt:  a.CreatedAt.Time,
	}
}

// 编译期断言：PGStore 同时实现两个接口。
var (
	_ AccountStore  = (*PGStore)(nil)
	_ SettingsStore = (*PGStore)(nil)
)
```

- [ ] **Step 4: 给内存实现也加编译期断言（放 `internal/account/memory.go` 末尾）**

```go
var (
	_ AccountStore  = (*MemoryStore)(nil)
	_ SettingsStore = (*MemoryStore)(nil)
)
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/account/ -v`
Expected: PASS（含 TestPGGetCredentials / TestPGGetSettingsDefaults）。

- [ ] **Step 6: Commit**

```bash
git add internal/account/postgres.go internal/account/postgres_test.go internal/account/memory.go
git commit -m "feat(account): AccountStore/SettingsStore PostgreSQL 实现（建 user 走事务）"
```

---

### Task 7: 会话管理器（Redis）

**Files:**
- Create: `internal/session/session.go`
- Create: `internal/session/session_test.go`

**Interfaces:**
- Consumes: `*redis.Client`。
- Produces（供 Task 8 中间件与 Task 9 handler）：
  - `session.SessionData{AccountID int64; Username, Role, ExternalID string}`
  - `session.Manager` 结构
  - `session.NewManager(rdb *redis.Client) *Manager`
  - `(m *Manager) Create(ctx, data SessionData, ttl time.Duration) (token string, err error)`
  - `(m *Manager) Get(ctx, token string) (SessionData, error)`（不存在返回 `session.ErrNotFound`）
  - `(m *Manager) Delete(ctx, token string) error`
  - `session.ErrNotFound error`
  - `session.DefaultTTL = 24h`、`session.RememberTTL = 7*24h` 常量

- [ ] **Step 1: 写失败测试 `internal/session/session_test.go`**

```go
package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewManager(rdb), mr
}

func TestSessionCreateGetDelete(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	data := SessionData{AccountID: 1, Username: "alice", Role: "user", ExternalID: "alice"}

	token, err := m.Create(ctx, data, DefaultTTL)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if len(token) != 64 { // 32 字节 hex。
		t.Fatalf("token 应为 64 位 hex, 得到 %d 位", len(token))
	}

	got, err := m.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got != data {
		t.Fatalf("会话数据不符: %+v vs %+v", got, data)
	}

	if err := m.Delete(ctx, token); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, err := m.Get(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 ErrNotFound, 得到 %v", err)
	}
}

func TestSessionGetMissing(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Get(context.Background(), "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在 token 应 ErrNotFound, 得到 %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	m, mr := newTestManager(t)
	ctx := context.Background()
	token, _ := m.Create(ctx, SessionData{AccountID: 1, Username: "a", Role: "user"}, 1*time.Second)

	mr.FastForward(2 * time.Second) // 快进过期。
	if _, err := m.Get(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期后应 ErrNotFound, 得到 %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/session/ -v`
Expected: 编译失败（`NewManager` 未定义）。

- [ ] **Step 3: 实现 `internal/session/session.go`**

```go
// Package session 提供基于 Redis 的会话管理：不透明 token -> 会话数据。
//
// 会话是控制台鉴权的凭据载体：登录成功后 Create 生成随机 token 存入 Redis，
// 通过 HttpOnly Cookie 下发；后续请求由中间件用 token 反查会话数据。
// Redis 不可用时 Create/Get 直接返回错误（fail-closed，绝不降级为无鉴权）。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// TTL 常量。
const (
	DefaultTTL  = 24 * time.Hour
	RememberTTL = 7 * 24 * time.Hour
)

// keyPrefix 是会话在 Redis 的键前缀。
const keyPrefix = "session:"

// ErrNotFound 表示会话不存在或已过期。
var ErrNotFound = errors.New("session: 会话不存在或已过期")

// SessionData 是一份会话承载的身份信息（登录时写入，鉴权时读出）。
type SessionData struct {
	AccountID  int64  `json:"account_id"`
	Username   string `json:"username"`
	Role       string `json:"role"`
	ExternalID string `json:"external_id"`
}

// Manager 管理会话的生命周期。
type Manager struct {
	rdb *redis.Client
}

// NewManager 构造会话管理器。
func NewManager(rdb *redis.Client) *Manager {
	return &Manager{rdb: rdb}
}

// Create 生成一个随机 token，把会话数据以给定 TTL 存入 Redis。
func (m *Manager) Create(ctx context.Context, data SessionData, ttl time.Duration) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话 token 失败: %w", err)
	}
	token := hex.EncodeToString(buf)

	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if err := m.rdb.Set(ctx, keyPrefix+token, payload, ttl).Err(); err != nil {
		return "", fmt.Errorf("写入会话失败: %w", err)
	}
	return token, nil
}

// Get 按 token 反查会话数据；不存在或过期返回 ErrNotFound。
func (m *Manager) Get(ctx context.Context, token string) (SessionData, error) {
	raw, err := m.rdb.Get(ctx, keyPrefix+token).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return SessionData{}, ErrNotFound
		}
		return SessionData{}, err
	}
	var data SessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return SessionData{}, err
	}
	return data, nil
}

// Delete 删除会话（登出）。删除不存在的 token 不视为错误。
func (m *Manager) Delete(ctx context.Context, token string) error {
	return m.rdb.Del(ctx, keyPrefix+token).Err()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/session/ -v`
Expected: PASS（3 个测试通过）。

- [ ] **Step 5: Commit**

```bash
git add internal/session/
git commit -m "feat(session): 基于 Redis 的会话管理器"
```

---

### Task 8: 会话鉴权中间件（SessionAuth + RequireRole）

**Files:**
- Create: `internal/middleware/session_auth.go`
- Create: `internal/middleware/session_auth_test.go`

**Interfaces:**
- Consumes: `session.Manager`（Task 7）、`session.SessionData`。
- Produces（供 Task 9 handler 与 server 装配）：
  - `middleware.CookieName = "linapi_session"` 常量
  - `middleware.SessionAuth(m *session.Manager) gin.HandlerFunc`（校验 Cookie → 注入会话；失败 401）
  - `middleware.RequireRole(role string) gin.HandlerFunc`（校验已注入会话的角色；不符 403）
  - `middleware.SessionFrom(c *gin.Context) (session.SessionData, bool)`

- [ ] **Step 1: 写失败测试 `internal/middleware/session_auth_test.go`**

```go
package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/session"
)

func newSessionManager(t *testing.T) *session.Manager {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return session.NewManager(rdb)
}

func TestSessionAuthRejectsNoCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	r := gin.New()
	r.Use(SessionAuth(m))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 Cookie 应 401, 得到 %d", w.Code)
	}
}

func TestSessionAuthAcceptsValidCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 1, Username: "alice", Role: "user", ExternalID: "alice",
	}, session.DefaultTTL)

	r := gin.New()
	r.Use(SessionAuth(m))
	r.GET("/probe", func(c *gin.Context) {
		s, ok := SessionFrom(c)
		if !ok || s.Username != "alice" {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("有效 Cookie 应 200, 得到 %d", w.Code)
	}
}

func TestRequireRoleForbidsMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := newSessionManager(t)
	token, _ := m.Create(context.Background(), session.SessionData{
		AccountID: 1, Username: "u", Role: "user",
	}, session.DefaultTTL)

	r := gin.New()
	r.Use(SessionAuth(m), RequireRole("admin"))
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("user 访问 admin 路由应 403, 得到 %d", w.Code)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/middleware/ -run TestSessionAuth -v`
Expected: 编译失败（`SessionAuth` 未定义）。

- [ ] **Step 3: 实现 `internal/middleware/session_auth.go`**

```go
package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/session"
)

// CookieName 是会话 Cookie 的名字。
const CookieName = "linapi_session"

// ctxKeySession 是会话数据注入 gin.Context 的键。
const ctxKeySession = "linapi.session"

// SessionAuth 构建会话鉴权中间件：从 Cookie 取 token，反查会话并注入 context。
// 无 Cookie / 会话失效 / Redis 异常都拒绝（fail-closed）。
func SessionAuth(m *session.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(CookieName)
		if err != nil || token == "" {
			abortError(c, http.StatusUnauthorized, "authentication_error", "未登录")
			return
		}
		data, err := m.Get(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				abortError(c, http.StatusUnauthorized, "authentication_error", "会话已失效，请重新登录")
				return
			}
			// Redis 异常等：fail-closed，返回 503。
			abortError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
			return
		}
		c.Set(ctxKeySession, data)
		c.Next()
	}
}

// RequireRole 构建角色校验中间件，须在 SessionAuth 之后挂载。
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, ok := SessionFrom(c)
		if !ok {
			abortError(c, http.StatusUnauthorized, "authentication_error", "未登录")
			return
		}
		if s.Role != role {
			abortError(c, http.StatusForbidden, "permission_error", "权限不足")
			return
		}
		c.Next()
	}
}

// SessionFrom 从 gin.Context 取出会话数据。
func SessionFrom(c *gin.Context) (session.SessionData, bool) {
	v, ok := c.Get(ctxKeySession)
	if !ok {
		return session.SessionData{}, false
	}
	s, ok := v.(session.SessionData)
	return s, ok
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/middleware/ -run "TestSessionAuth|TestRequireRole" -v`
Expected: PASS（3 个测试通过）。

- [ ] **Step 5: Commit**

```bash
git add internal/middleware/session_auth.go internal/middleware/session_auth_test.go
git commit -m "feat(middleware): SessionAuth + RequireRole 会话鉴权中间件"
```

---

### Task 9: config —— admin 段改造（去 token/loopback，加 bootstrap）

**Files:**
- Modify: `internal/config/config.go`（`AdminConfig` 改字段 + `setDefaults`）

**Interfaces:**
- Consumes: 无。
- Produces（供 main 装配 bootstrap 与 server 装配鉴权）：
  - `config.AdminConfig{Enabled bool; Bootstrap BootstrapConfig; ChannelReloadInterval int}`
  - `config.BootstrapConfig{Username, Password string}`
  - 移除 `AdminConfig.Token`、`AdminConfig.LoopbackOnly`

- [ ] **Step 1: 改 `internal/config/config.go` 的 AdminConfig**

把现有 `AdminConfig` 整段替换为：

```go
// AdminConfig 是管理面与控制台的配置。
// 控制台鉴权改为「账号密码 + 会话」，不再用裸 token；本段仅保留挂载开关、
// 首个管理员播种（bootstrap）与渠道定时热重载间隔。
type AdminConfig struct {
	// Enabled 为 true 时挂载控制台（/console）与认证端点（/auth /admin /me）；默认关闭。
	Enabled bool `mapstructure:"enabled"`
	// Bootstrap 是首个管理员账户的播种配置（仅当该用户名不存在时创建）。
	Bootstrap BootstrapConfig `mapstructure:"bootstrap"`
	// ChannelReloadInterval 是渠道定时热重载的间隔（秒）。<=0 关闭。仅 database.enabled=true 生效。
	ChannelReloadInterval int `mapstructure:"channel_reload_interval"`
}

// BootstrapConfig 描述首个管理员账户的播种参数。
type BootstrapConfig struct {
	// Username 为空时不播种。
	Username string `mapstructure:"username"`
	// Password 建议用环境变量注入（LINAPI_ADMIN_BOOTSTRAP_PASSWORD）。为空时不播种并告警。
	Password string `mapstructure:"password"`
}
```

- [ ] **Step 2: 改 `setDefaults` 中的 admin 段**

把现有 admin 默认值三行替换为：

```go
	// 管理面/控制台默认关闭，需显式开启。bootstrap 默认空（不播种）。
	// 渠道定时热重载默认 60s（database.enabled=true 时生效，<=0 关闭）。
	v.SetDefault("admin.enabled", false)
	v.SetDefault("admin.bootstrap.username", "")
	v.SetDefault("admin.bootstrap.password", "")
	v.SetDefault("admin.channel_reload_interval", 60)
```

- [ ] **Step 3: 编译验证（会暴露 server.go/main.go 中对旧字段的引用——预期，后续任务修复）**

Run: `go build ./internal/config/`
Expected: `internal/config/` 单独编译通过。（`go build ./...` 此时会因 server/main 仍引用旧字段而失败，属预期，Task 13/14 修复。）

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "refactor(config): admin 段去 token/loopback，改为 bootstrap 播种"
```

---

### Task 10: 扩展密钥存储 —— DeleteAPIKey（支撑 /me/keys 删除）

**Files:**
- Modify: `internal/admin/store.go`（`AdminStore` 接口加 `DeleteAPIKey`）
- Modify: `internal/store/memory.go`（加 `AdminDeleteKey`）
- Modify: `internal/admin/memory.go`（实现 `DeleteAPIKey`）
- Modify: `internal/admin/postgres.go`（实现 `DeleteAPIKey`）
- Modify: `internal/db/api_keys.sql.go`（加 `DeleteAPIKey` 查询）
- Modify: `internal/db/querier.go`（接口加 `DeleteAPIKey`）
- Modify: `db/query.sql`（同步 SQL 源）
- Create/Modify: `internal/admin/memory_test.go`（补删除用例）

**Interfaces:**
- Consumes: 现有 admin/store/db 结构。
- Produces（供 Task 12 的 /me/keys 与 admin 密钥管理）：
  - `AdminStore.DeleteAPIKey(ctx, keyID string) error`（不存在返回 `admin.ErrNotFound`）
  - `(*store.MemoryStore) AdminDeleteKey(keyID string) error`
  - `(*db.Queries) DeleteAPIKey(ctx, keyID string) (int64, error)`

- [ ] **Step 1: 写失败测试（追加到 `internal/admin/memory_test.go` 的 TestMemoryAPIKeyCRUD 末尾）**

在 `TestMemoryAPIKeyCRUD` 的最后（启停校验之后）追加：

```go
	// 删除密钥。
	if err := m.DeleteAPIKey(ctx, "k1"); err != nil {
		t.Fatalf("DeleteAPIKey 失败: %v", err)
	}
	if keys, _ := m.ListAPIKeysByUser(ctx, "u1"); len(keys) != 0 {
		t.Fatalf("删除后应无密钥, 得到 %d 个", len(keys))
	}
	if err := m.DeleteAPIKey(ctx, "k1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在密钥应 ErrNotFound, 得到 %v", err)
	}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/admin/ -run TestMemoryAPIKeyCRUD -v`
Expected: 编译失败（`DeleteAPIKey` 未定义）。

- [ ] **Step 3: `internal/admin/store.go` 的 AdminStore 接口加方法**

在 `SetAPIKeyEnabled` 行下方加：

```go
	SetAPIKeyEnabled(ctx context.Context, keyID string, enabled bool) (APIKey, error)
	// DeleteAPIKey 物理删除密钥；不存在返回 ErrNotFound。
	DeleteAPIKey(ctx context.Context, keyID string) error
```

- [ ] **Step 4: `internal/store/memory.go` 加 AdminDeleteKey**

在 `AdminSetKeyEnabled` 方法后加：

```go
// AdminDeleteKey 物理删除密钥（按 keyID）；不存在返回 ErrKeyNotFound。
func (s *MemoryStore) AdminDeleteKey(keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keyByID[keyID]
	if !ok {
		return ErrKeyNotFound
	}
	delete(s.keyByID, keyID)
	// 同步删除明文 key 索引（找到指向同一 Identity 的条目）。
	for k, v := range s.keys {
		if v == id {
			delete(s.keys, k)
			break
		}
	}
	return nil
}
```

- [ ] **Step 5: `internal/admin/memory.go` 实现 DeleteAPIKey**

在 `SetAPIKeyEnabled` 方法后加：

```go
func (m *MemoryStore) DeleteAPIKey(_ context.Context, keyID string) error {
	return mapUserErr(m.base.AdminDeleteKey(keyID))
}
```

- [ ] **Step 6: `internal/db/api_keys.sql.go` 加 DeleteAPIKey 查询**

在文件末尾加：

```go
const deleteAPIKey = `-- name: DeleteAPIKey :execrows
DELETE FROM api_keys WHERE key_id = $1
`

// DeleteAPIKey 按 key_id 物理删除密钥，返回受影响行数（0 表示不存在）。
func (q *Queries) DeleteAPIKey(ctx context.Context, keyID string) (int64, error) {
	ct, err := q.db.Exec(ctx, deleteAPIKey, keyID)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
```

- [ ] **Step 7: `internal/db/querier.go` 接口加方法**

在 `SetAPIKeyEnabled` 行下方加：

```go
	DeleteAPIKey(ctx context.Context, keyID string) (int64, error)
```

- [ ] **Step 8: `internal/admin/postgres.go` 实现 DeleteAPIKey**

在 `SetAPIKeyEnabled` 方法后加：

```go
func (s *PGStore) DeleteAPIKey(ctx context.Context, keyID string) error {
	n, err := s.q.DeleteAPIKey(ctx, keyID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 9: 同步 SQL 到 `db/query.sql`**

把 `deleteAPIKey` 的 `-- name: DeleteAPIKey :execrows` 注释块 + SQL 追加到 `db/query.sql`。

- [ ] **Step 10: 运行测试确认通过**

Run: `go test ./internal/admin/ ./internal/store/ -v`
Expected: PASS（含新增删除用例）。

- [ ] **Step 11: Commit**

```bash
git add internal/admin/ internal/store/memory.go internal/db/ db/query.sql
git commit -m "feat(admin): AdminStore 增 DeleteAPIKey（支撑用户自助删 key）"
```

---

### Task 11: /auth 处理器（register / login / logout / me）

**Files:**
- Create: `internal/server/auth_handlers.go`
- Create: `internal/server/auth_handlers_test.go`

**Interfaces:**
- Consumes: `account.AccountStore`、`account.SettingsStore`、`session.Manager`、`middleware.SessionFrom`、`middleware.CookieName`、`account.HashPassword`/`CheckPassword`。
- Produces（供 server 装配）：
  - `authHandlers` 结构，字段 `accounts account.AccountStore`、`settings account.SettingsStore`、`sessions *session.Manager`、`secureCookie bool`
  - 方法 `register`、`login`、`logout`、`me`
  - `newAuthHandlers(...) *authHandlers`

- [ ] **Step 1: 写失败测试 `internal/server/auth_handlers_test.go`**

```go
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/account"
	"linapi/internal/middleware"
	"linapi/internal/session"
	"linapi/internal/store"
)

// newAuthTestEngine 构建挂了 /auth 的 gin 引擎，返回引擎与底层依赖。
func newAuthTestEngine(t *testing.T) (*gin.Engine, account.AccountStore, *session.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sess := session.NewManager(rdb)

	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAuthHandlers(accStore, accStore, sess, false)

	e := gin.New()
	g := e.Group("/auth")
	g.POST("/register", h.register)
	g.POST("/login", h.login)
	g.POST("/logout", middleware.SessionAuth(sess), h.logout)
	g.GET("/me", middleware.SessionAuth(sess), h.me)
	return e, accStore, sess
}

func TestRegisterDisabledByDefault(t *testing.T) {
	e, _, _ := newAuthTestEngine(t)
	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("默认注册关闭应 403, 得到 %d", w.Code)
	}
}

func TestRegisterWhenEnabledThenLogin(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	// 打开注册开关。
	_ = accStore.(*account.MemoryStore).Put(context.Background(), account.Settings{RegistrationEnabled: true})

	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("开启后注册应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 登录应下发 Cookie。
	req = httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("登录应下发会话 Cookie")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "bob", PasswordHash: hash, Role: account.RoleAdmin,
	})

	body, _ := json.Marshal(gin.H{"username": "bob", "password": "wrongpass"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401, 得到 %d", w.Code)
	}
}

func TestMeReturnsIdentity(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "carol", PasswordHash: hash, Role: account.RoleAdmin,
	})
	login, _ := json.Marshal(gin.H{"username": "carol", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	cookies := w.Result().Cookies()

	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/auth/me 应 200, 得到 %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["username"] != "carol" || got["role"] != "admin" {
		t.Fatalf("me 身份不符: %+v", got)
	}
}
```

- [ ] **Step 2: 加测试辅助 `bytesReader`（放 `internal/server/auth_handlers_test.go` 顶部 import 之后）**

```go
import "bytes"

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/server/ -run "TestRegister|TestLogin|TestMe" -v`
Expected: 编译失败（`newAuthHandlers` 未定义）。

- [ ] **Step 4: 实现 `internal/server/auth_handlers.go`**

```go
package server

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
	"linapi/internal/middleware"
	"linapi/internal/session"
)

// authHandlers 聚合 /auth 端点的处理器。
type authHandlers struct {
	accounts     account.AccountStore
	settings     account.SettingsStore
	sessions     *session.Manager
	secureCookie bool // 生产置 true（HTTPS）；本地/测试为 false 以便非 HTTPS 下 Cookie 可用。
}

func newAuthHandlers(accounts account.AccountStore, settings account.SettingsStore, sessions *session.Manager, secureCookie bool) *authHandlers {
	return &authHandlers{accounts: accounts, settings: settings, sessions: sessions, secureCookie: secureCookie}
}

type credentialsReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Remember bool   `json:"remember"`
}

// setSessionCookie 下发 HttpOnly + SameSite=Strict 会话 Cookie。
func (h *authHandlers) setSessionCookie(c *gin.Context, token string, maxAgeSeconds int) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(middleware.CookieName, token, maxAgeSeconds, "/", "", h.secureCookie, true)
}

// register 自助注册：受 registration_enabled 开关控制。
func (h *authHandlers) register(c *gin.Context) {
	var req credentialsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	settings, err := h.settings.Get(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取系统设置失败")
		return
	}
	if !settings.RegistrationEnabled {
		writeError(c, http.StatusForbidden, "permission_error", "当前未开放注册")
		return
	}
	hash, err := account.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码长度不足（至少 8 位）")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}
	acc, err := h.accounts.CreateUserAccount(c.Request.Context(), req.Username, hash, settings.NewUserInitialBalance)
	if err != nil {
		if errors.Is(err, account.ErrConflict) {
			writeError(c, http.StatusConflict, "conflict", "用户名已存在")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "创建账户失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"username": acc.Username, "role": acc.Role})
}

// login 校验账密，建会话，下发 Cookie。
func (h *authHandlers) login(c *gin.Context) {
	var req credentialsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	cred, err := h.accounts.GetCredentials(c.Request.Context(), req.Username)
	if err != nil || !cred.Enabled || !account.CheckPassword(cred.PasswordHash, req.Password) {
		// 统一错误，不区分「用户不存在」与「密码错误」，避免用户名枚举。
		writeError(c, http.StatusUnauthorized, "authentication_error", "用户名或密码错误")
		return
	}
	ttl := session.DefaultTTL
	if req.Remember {
		ttl = session.RememberTTL
	}
	token, err := h.sessions.Create(c.Request.Context(), session.SessionData{
		AccountID: cred.ID, Username: cred.Username, Role: cred.Role, ExternalID: cred.ExternalID,
	}, ttl)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	c.JSON(http.StatusOK, gin.H{"username": cred.Username, "role": cred.Role})
}

// logout 删会话 + 清 Cookie。
func (h *authHandlers) logout(c *gin.Context) {
	if token, err := c.Cookie(middleware.CookieName); err == nil {
		_ = h.sessions.Delete(c.Request.Context(), token)
	}
	h.setSessionCookie(c, "", -1) // maxAge<0 立即失效。
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// me 返回当前会话身份（前端恢复登录态用）。
func (h *authHandlers) me(c *gin.Context) {
	s, ok := middleware.SessionFrom(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "authentication_error", "未登录")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"username": s.Username, "role": s.Role, "external_id": s.ExternalID,
	})
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/server/ -run "TestRegister|TestLogin|TestMe" -v`
Expected: PASS（4 个测试通过）。

- [ ] **Step 6: Commit**

```bash
git add internal/server/auth_handlers.go internal/server/auth_handlers_test.go
git commit -m "feat(server): /auth 端点（注册/登录/登出/me）"
```

---

### Task 12: /me 处理器（用户自助 profile + 密钥 CRUD，含越权硬约束）

**Files:**
- Create: `internal/server/me_handlers.go`
- Create: `internal/server/me_handlers_test.go`

**Interfaces:**
- Consumes: `admin.Service`（复用其 `Store()` 的密钥 CRUD 与 `DeleteAPIKey`）、`store.Store`（读余额）、`admin.GenerateKey`、`middleware.SessionFrom`。
- Produces（供 server 装配）：
  - `meHandlers` 结构，字段 `svc *admin.Service`、`store store.Store`
  - 方法 `profile`、`listKeys`、`createKey`、`setKeyEnabled`、`deleteKey`
  - `newMeHandlers(svc *admin.Service, st store.Store) *meHandlers`
  - 私有辅助 `ownedKey(c, keyID) (admin.APIKey, bool)`：校验 keyID 属于当前会话 external_id

**越权硬约束（本任务核心，务必测试覆盖）**：
- `createKey` 绑定的 UserID **只取自 `middleware.SessionFrom(c).ExternalID`**，不读请求体任何 user_id/external_id。
- `setKeyEnabled` / `deleteKey` 先用 `ownedKey` 校验归属，非本人返回 404。

- [ ] **Step 1: 写失败测试 `internal/server/me_handlers_test.go`**

```go
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
	"linapi/internal/session"
	"linapi/internal/store"
)

// meTestCtx 造一个带会话身份的 /me 引擎；sessionExt 是当前登录用户的 external_id。
func newMeTestEngine(t *testing.T, sessionExt string) (*gin.Engine, *admin.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	base := store.NewMemoryStore(nil)
	as := admin.NewMemoryStore(base, nil)
	svc := admin.NewService(as, nil, nil)
	// 预置两个用户（当前登录者 + 他人），供越权测试。
	_, _ = as.CreateUser(context.Background(), admin.CreateUserInput{ExternalID: sessionExt, Enabled: true})
	_, _ = as.CreateUser(context.Background(), admin.CreateUserInput{ExternalID: "other", Enabled: true})

	h := newMeHandlers(svc, base)
	e := gin.New()
	// 测试用中间件：直接注入固定会话身份（跳过真实 SessionAuth）。
	inject := func(c *gin.Context) {
		c.Set("linapi.session", session.SessionData{
			AccountID: 1, Username: "me", Role: "user", ExternalID: sessionExt,
		})
		c.Next()
	}
	g := e.Group("/me", inject)
	g.GET("/profile", h.profile)
	g.GET("/keys", h.listKeys)
	g.POST("/keys", h.createKey)
	g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)
	g.DELETE("/keys/:keyid", h.deleteKey)
	return e, svc
}

func TestMeCreateKeyBindsToSession(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	// 即便请求体塞 user_id=other，也必须绑定到会话的 "me"。
	body, _ := json.Marshal(gin.H{"user_id": "other", "external_id": "other", "rate_limit_per_min": 60})
	req := httptest.NewRequest(http.MethodPost, "/me/keys", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("建 key 应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	// 断言 key 落在 "me" 名下，而非 "other"。
	meKeys, _ := svc.Store().ListAPIKeysByUser(context.Background(), "me")
	otherKeys, _ := svc.Store().ListAPIKeysByUser(context.Background(), "other")
	if len(meKeys) != 1 || len(otherKeys) != 0 {
		t.Fatalf("key 必须绑定会话用户 me: me=%d other=%d", len(meKeys), len(otherKeys))
	}
}

func TestMeCannotTouchOthersKey(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	// 直接给 "other" 建一把 key。
	gen, _ := admin.GenerateKey()
	_, _ = svc.Store().CreateAPIKey(context.Background(), admin.CreateAPIKeyInput{
		APIKey: gen.APIKey, KeyID: "other-key", UserID: "other", Enabled: true,
	})

	// 会话是 "me"，尝试禁用他人 key -> 404。
	body, _ := json.Marshal(gin.H{"enabled": false})
	req := httptest.NewRequest(http.MethodPatch, "/me/keys/other-key/enabled", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("操作他人 key 应 404, 得到 %d", w.Code)
	}

	// 尝试删他人 key -> 404。
	req = httptest.NewRequest(http.MethodDelete, "/me/keys/other-key", nil)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("删他人 key 应 404, 得到 %d", w.Code)
	}
}

func TestMeProfileReturnsBalance(t *testing.T) {
	e, svc := newMeTestEngine(t, "me")
	_, _ = svc.Store().AddBalance(context.Background(), "me", 888)

	req := httptest.NewRequest(http.MethodGet, "/me/profile", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("profile 应 200, 得到 %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["external_id"] != "me" {
		t.Fatalf("profile external_id 不符: %+v", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run TestMe -v`
Expected: 编译失败（`newMeHandlers` 未定义）。

- [ ] **Step 3: 实现 `internal/server/me_handlers.go`**

```go
package server

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
	"linapi/internal/middleware"
	"linapi/internal/store"
)

// meHandlers 聚合 /me 用户自助端点。绑定用户一律取自会话，杜绝越权。
type meHandlers struct {
	svc   *admin.Service
	store store.Store
}

func newMeHandlers(svc *admin.Service, st store.Store) *meHandlers {
	return &meHandlers{svc: svc, store: st}
}

// sessionExternalID 取当前会话的计费实体标识；无会话时返回 ""。
func (h *meHandlers) sessionExternalID(c *gin.Context) string {
	s, ok := middleware.SessionFrom(c)
	if !ok {
		return ""
	}
	return s.ExternalID
}

// ownedKey 校验 keyID 属于当前会话用户；不属于/不存在返回 (,false)。
func (h *meHandlers) ownedKey(c *gin.Context, keyID string) (admin.APIKey, bool) {
	ext := h.sessionExternalID(c)
	keys, err := h.svc.Store().ListAPIKeysByUser(c.Request.Context(), ext)
	if err != nil {
		return admin.APIKey{}, false
	}
	for _, k := range keys {
		if k.KeyID == keyID {
			return k, true
		}
	}
	return admin.APIKey{}, false
}

// profile 返回当前用户账户信息 + 余额。
func (h *meHandlers) profile(c *gin.Context) {
	ext := h.sessionExternalID(c)
	bal, err := h.store.Balance(c.Request.Context(), ext)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取余额失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"external_id": ext, "balance": bal})
}

// listKeys 列出当前用户的密钥（脱敏，不含明文）。
func (h *meHandlers) listKeys(c *gin.Context) {
	ext := h.sessionExternalID(c)
	keys, err := h.svc.Store().ListAPIKeysByUser(c.Request.Context(), ext)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取密钥失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": keys})
}

type meCreateKeyReq struct {
	RateLimitPerMin int      `json:"rate_limit_per_min"`
	AllowedModels   []string `json:"allowed_models"`
	// 注意：刻意不接收任何 user_id/external_id——绑定用户强制取自会话。
}

// createKey 自助建 key，绑定用户强制取自会话，明文仅回显一次。
func (h *meHandlers) createKey(c *gin.Context) {
	ext := h.sessionExternalID(c)
	if ext == "" {
		writeError(c, http.StatusUnauthorized, "authentication_error", "未登录")
		return
	}
	var req meCreateKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	gen, err := admin.GenerateKey()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "生成密钥失败")
		return
	}
	k, err := h.svc.Store().CreateAPIKey(c.Request.Context(), admin.CreateAPIKeyInput{
		APIKey:          gen.APIKey,
		KeyID:           gen.KeyID,
		UserID:          ext, // 强制绑定会话用户。
		RateLimitPerMin: req.RateLimitPerMin,
		AllowedModels:   req.AllowedModels,
		Enabled:         true,
	})
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"api_key":            gen.APIKey, // 仅此一次。
		"key_id":             k.KeyID,
		"rate_limit_per_min": k.RateLimitPerMin,
		"allowed_models":     k.AllowedModels,
		"enabled":            k.Enabled,
		"created_at":         k.CreatedAt,
	})
}

// setKeyEnabled 启停自己的 key；非本人 404。
func (h *meHandlers) setKeyEnabled(c *gin.Context) {
	keyID := c.Param("keyid")
	if _, ok := h.ownedKey(c, keyID); !ok {
		writeError(c, http.StatusNotFound, "not_found", "密钥不存在")
		return
	}
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	k, err := h.svc.Store().SetAPIKeyEnabled(c.Request.Context(), keyID, req.Enabled)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, k)
}

// deleteKey 删除自己的 key；非本人 404。
func (h *meHandlers) deleteKey(c *gin.Context) {
	keyID := c.Param("keyid")
	if _, ok := h.ownedKey(c, keyID); !ok {
		writeError(c, http.StatusNotFound, "not_found", "密钥不存在")
		return
	}
	if err := h.svc.Store().DeleteAPIKey(c.Request.Context(), keyID); err != nil {
		if errors.Is(err, admin.ErrNotFound) {
			writeError(c, http.StatusNotFound, "not_found", "密钥不存在")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "删除密钥失败")
		return
	}
	c.Status(http.StatusNoContent)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/server/ -run TestMe -v`
Expected: PASS（越权约束 + profile 通过）。

- [ ] **Step 5: Commit**

```bash
git add internal/server/me_handlers.go internal/server/me_handlers_test.go
git commit -m "feat(server): /me 用户自助端点（密钥绑定会话，杜绝越权）"
```

---

### Task 13: /admin/accounts + /admin/settings 处理器

**Files:**
- Create: `internal/server/account_handlers.go`
- Create: `internal/server/account_handlers_test.go`

**Interfaces:**
- Consumes: `account.AccountStore`、`account.SettingsStore`、`account.HashPassword`、`account.ValidRole`。
- Produces（供 server 装配）：
  - `accountConsoleHandlers` 结构，字段 `accounts account.AccountStore`、`settings account.SettingsStore`
  - 方法 `listAccounts`、`createAccount`、`setAccountEnabled`、`resetPassword`、`getSettings`、`putSettings`
  - `newAccountConsoleHandlers(accounts account.AccountStore, settings account.SettingsStore) *accountConsoleHandlers`

- [ ] **Step 1: 写失败测试 `internal/server/account_handlers_test.go`**

```go
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
	"linapi/internal/store"
)

func newAccountConsoleEngine(t *testing.T) (*gin.Engine, account.AccountStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAccountConsoleHandlers(accStore, accStore)
	e := gin.New()
	g := e.Group("/admin")
	g.GET("/accounts", h.listAccounts)
	g.POST("/accounts", h.createAccount)
	g.PATCH("/accounts/:id/enabled", h.setAccountEnabled)
	g.POST("/accounts/:id/password", h.resetPassword)
	g.GET("/settings", h.getSettings)
	g.PUT("/settings", h.putSettings)
	return e, accStore
}

func TestAdminCreateUserAccountWithInitialBalance(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"username": "u1", "password": "password123", "role": "user", "initial_balance": 300})
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("建账户应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["role"] != "user" || got["external_id"] != "u1" {
		t.Fatalf("user 账户应有 external_id: %+v", got)
	}
}

func TestAdminCreateAccountRejectsBadRole(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"username": "x", "password": "password123", "role": "superuser"})
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法角色应 400, 得到 %d", w.Code)
	}
}

func TestAdminAccountResponseHasNoPasswordHash(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	body, _ := json.Marshal(gin.H{"username": "u2", "password": "password123", "role": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if bytesContainsAny(w.Body.Bytes(), "password_hash", "PasswordHash") {
		t.Error("账户响应不得含 password_hash")
	}
}

func TestAdminSettingsRoundTrip(t *testing.T) {
	e, _ := newAccountConsoleEngine(t)
	// 改设置。
	body, _ := json.Marshal(gin.H{"registration_enabled": true, "new_user_initial_balance": 5000})
	req := httptest.NewRequest(http.MethodPut, "/admin/settings", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("改设置应 200, 得到 %d", w.Code)
	}
	// 读回。
	req = httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var got account.Settings
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.RegistrationEnabled || got.NewUserInitialBalance != 5000 {
		t.Fatalf("设置未持久化: %+v", got)
	}
}

// bytesContainsAny 报告 body 是否含任一子串。
func bytesContainsAny(b []byte, subs ...string) bool {
	s := string(b)
	for _, sub := range subs {
		if len(sub) > 0 && contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run TestAdmin -v`
Expected: 编译失败（`newAccountConsoleHandlers` 未定义）。

- [ ] **Step 3: 实现 `internal/server/account_handlers.go`**

```go
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
)

// accountConsoleHandlers 聚合 /admin/accounts 与 /admin/settings 端点。
type accountConsoleHandlers struct {
	accounts account.AccountStore
	settings account.SettingsStore
}

func newAccountConsoleHandlers(accounts account.AccountStore, settings account.SettingsStore) *accountConsoleHandlers {
	return &accountConsoleHandlers{accounts: accounts, settings: settings}
}

// writeAccountError 把 account 领域错误映射为 HTTP 状态码。
func writeAccountError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, account.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "账户不存在")
	case errors.Is(err, account.ErrConflict):
		writeError(c, http.StatusConflict, "conflict", "用户名已存在")
	case errors.Is(err, account.ErrInvalidRole):
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法角色")
	default:
		writeError(c, http.StatusInternalServerError, "internal_error", "存储操作失败")
	}
}

func (h *accountConsoleHandlers) listAccounts(c *gin.Context) {
	limit := queryInt(c, "limit", 100)
	offset := queryInt(c, "offset", 0)
	accs, err := h.accounts.ListAccounts(c.Request.Context(), limit, offset)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accs})
}

type createAccountReq struct {
	Username       string `json:"username" binding:"required"`
	Password       string `json:"password" binding:"required"`
	Role           string `json:"role" binding:"required"`
	InitialBalance int64  `json:"initial_balance"`
}

func (h *accountConsoleHandlers) createAccount(c *gin.Context) {
	var req createAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if !account.ValidRole(req.Role) {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法角色（仅 admin/user）")
		return
	}
	hash, err := account.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码长度不足（至少 8 位）")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}

	var acc account.Account
	if req.Role == account.RoleUser {
		// user 账户：自动建计费实体，admin 可指定初始余额。
		acc, err = h.accounts.CreateUserAccount(c.Request.Context(), req.Username, hash, req.InitialBalance)
	} else {
		acc, err = h.accounts.CreateAccount(c.Request.Context(), account.CreateAccountInput{
			Username: req.Username, PasswordHash: hash, Role: req.Role,
		})
	}
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusCreated, acc) // acc 无 PasswordHash 字段。
}

func (h *accountConsoleHandlers) setAccountEnabled(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法账户 ID")
		return
	}
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	acc, err := h.accounts.SetEnabled(c.Request.Context(), id, req.Enabled)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, acc)
}

type resetPasswordReq struct {
	Password string `json:"password" binding:"required"`
}

func (h *accountConsoleHandlers) resetPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法账户 ID")
		return
	}
	var req resetPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	hash, err := account.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码长度不足（至少 8 位）")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}
	if err := h.accounts.UpdatePassword(c.Request.Context(), id, hash); err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *accountConsoleHandlers) getSettings(c *gin.Context) {
	s, err := h.settings.Get(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取设置失败")
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *accountConsoleHandlers) putSettings(c *gin.Context) {
	var s account.Settings
	if err := c.ShouldBindJSON(&s); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if err := h.settings.Put(c.Request.Context(), s); err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "保存设置失败")
		return
	}
	c.JSON(http.StatusOK, s)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/server/ -run TestAdmin -v`
Expected: PASS（4 个测试通过）。

- [ ] **Step 5: Commit**

```bash
git add internal/server/account_handlers.go internal/server/account_handlers_test.go
git commit -m "feat(server): /admin/accounts 与 /admin/settings 端点"
```

---

### Task 14: 装配 server 路由（Deps 扩展 + /auth /me /admin 接线 + 删 AdminAuth）

**Files:**
- Modify: `internal/server/server.go`（`Deps` 加字段；`registerRoutes` 加 /auth /me；`registerAdminRoutes` 改会话鉴权）
- Delete: `internal/middleware/admin_auth.go`、`internal/middleware/admin_auth_test.go`

**Interfaces:**
- Consumes: Task 6/7/11/12/13 的类型与 handler 构造器。
- Produces（供 Task 15 的 main 装配）：
  - `Deps` 新增字段 `Account account.AccountStore`、`Settings account.SettingsStore`、`Session *session.Manager`、`SecureCookie bool`

- [ ] **Step 1: `internal/server/server.go` 的 import 加新包**

在 import 块加：

```go
	"linapi/internal/account"
	"linapi/internal/session"
```

- [ ] **Step 2: 扩展 `Deps` 结构**

在 `Logger` 字段前插入：

```go
	Account   account.AccountStore  // 控制台账户数据访问；nil 表示不挂控制台端点
	Settings  account.SettingsStore // 系统设置数据访问
	Session   *session.Manager      // 会话管理器（Redis）
	SecureCookie bool               // 会话 Cookie 是否加 Secure 属性（生产 HTTPS 置 true）
	Logger    *slog.Logger          // 结构化日志器；nil 时 RequestLogger 退化为 slog.Default()
```

- [ ] **Step 3: `registerRoutes` 末尾（`s.registerAdminRoutes()` 之前）加认证与自助路由**

```go
	s.registerAuthRoutes()
	s.registerMeRoutes()
	s.registerAdminRoutes()
```

- [ ] **Step 4: 加 `registerAuthRoutes` 方法**

```go
// registerAuthRoutes 挂载 /auth 分组（注册/登录/登出/me）。
// 仅当 admin.enabled=true 且注入了账户体系依赖时生效。
func (s *Server) registerAuthRoutes() {
	if !s.cfg.Admin.Enabled || s.deps.Account == nil || s.deps.Session == nil {
		return
	}
	h := newAuthHandlers(s.deps.Account, s.deps.Settings, s.deps.Session, s.deps.SecureCookie)
	g := s.engine.Group("/auth")
	g.POST("/register", h.register)
	g.POST("/login", h.login)
	g.POST("/logout", middleware.SessionAuth(s.deps.Session), h.logout)
	g.GET("/me", middleware.SessionAuth(s.deps.Session), h.me)
}
```

- [ ] **Step 5: 加 `registerMeRoutes` 方法**

```go
// registerMeRoutes 挂载 /me 分组（用户自助）。需登录（任意角色）。
func (s *Server) registerMeRoutes() {
	if !s.cfg.Admin.Enabled || s.deps.Account == nil || s.deps.Session == nil || s.deps.Admin == nil {
		return
	}
	h := newMeHandlers(s.deps.Admin, s.deps.Store)
	g := s.engine.Group("/me", middleware.SessionAuth(s.deps.Session))
	g.GET("/profile", h.profile)
	g.GET("/keys", h.listKeys)
	g.POST("/keys", h.createKey)
	g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)
	g.DELETE("/keys/:keyid", h.deleteKey)
}
```

- [ ] **Step 6: 替换 `registerAdminRoutes` 的鉴权与账户/设置路由**

把 `registerAdminRoutes` 整个方法体替换为：

```go
func (s *Server) registerAdminRoutes() {
	if !s.cfg.Admin.Enabled || s.deps.Admin == nil || s.deps.Session == nil {
		return
	}

	h := &adminHandlers{svc: s.deps.Admin}
	ac := newAccountConsoleHandlers(s.deps.Account, s.deps.Settings)
	// 管理面改「会话 + admin 角色」鉴权（替换裸 token）。
	g := s.engine.Group("/admin", middleware.SessionAuth(s.deps.Session), middleware.RequireRole(account.RoleAdmin))
	{
		// 账户与系统设置
		g.GET("/accounts", ac.listAccounts)
		g.POST("/accounts", ac.createAccount)
		g.PATCH("/accounts/:id/enabled", ac.setAccountEnabled)
		g.POST("/accounts/:id/password", ac.resetPassword)
		g.GET("/settings", ac.getSettings)
		g.PUT("/settings", ac.putSettings)

		// 计费用户
		g.POST("/users", h.createUser)
		g.GET("/users", h.listUsers)
		g.GET("/users/:id", h.getUser)
		g.PATCH("/users/:id/enabled", h.setUserEnabled)
		g.POST("/users/:id/balance", h.addBalance)

		// 密钥（挂在用户下）
		g.POST("/users/:id/keys", h.createKey)
		g.GET("/users/:id/keys", h.listKeys)
		g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)

		// 渠道
		g.POST("/channels", h.createChannel)
		g.GET("/channels", h.listChannels)
		g.GET("/channels/:id", h.getChannel)
		g.PUT("/channels/:id", h.updateChannel)
		g.PATCH("/channels/:id/enabled", h.setChannelEnabled)
		g.DELETE("/channels/:id", h.deleteChannel)
	}
}
```

- [ ] **Step 7: 删除退役的 AdminAuth 中间件与其测试**

```bash
rm internal/middleware/admin_auth.go internal/middleware/admin_auth_test.go
```

- [ ] **Step 8: 更新 `internal/server/admin_handlers_test.go` 以适配新鉴权**

原测试用 `middleware.AdminAuth(testAdminToken, false)` 挂路由，现已删除。把该测试文件的 `newAdminTestEngine` 改为不挂鉴权中间件（直接测 handler 逻辑），删除 `TestAdminAuthGuardsRoutes`（鉴权已由 `session_auth_test.go` 覆盖）。具体：删掉 import 中的 `middleware`、常量 `testAdminToken`、`g.Use(middleware.AdminAuth(...))` 一行，以及 `doAdmin` 里 `req.Header.Set("Authorization", ...)` 一行和 `TestAdminAuthGuardsRoutes` 整个函数。

- [ ] **Step 9: 编译验证**

Run: `go build ./internal/server/ ./internal/middleware/`
Expected: 编译通过（AdminAuth 引用已清除）。

- [ ] **Step 10: Commit**

```bash
git add internal/server/server.go internal/server/admin_handlers_test.go
git rm internal/middleware/admin_auth.go internal/middleware/admin_auth_test.go
git commit -m "feat(server): 控制台路由接线，/admin 改会话+角色鉴权，退役 AdminAuth"
```

---

### Task 15: main 装配（account/settings store + session + bootstrap 管理员）

**Files:**
- Modify: `cmd/linapi/main.go`（`dataLayer` 加 account/settings；装配 session；bootstrap；注入 Deps）

**Interfaces:**
- Consumes: 前述所有 Produces。
- Produces: 可启动的完整服务（`go build ./...` 通过）。

- [ ] **Step 1: `dataLayer` 结构加两字段**

在 `dataLayer` 结构（`pool` 字段前）加：

```go
	account    account.AccountStore  // 控制台账户数据访问
	settings   account.SettingsStore // 系统设置数据访问
```

- [ ] **Step 2: import 加 account、session 包**

```go
	"linapi/internal/account"
	"linapi/internal/session"
```

- [ ] **Step 3: 内存分支装配 account/settings（`buildDataLayer` 的 `!cfg.Database.Enabled` 分支内）**

把内存分支的 `return dataLayer{...}` 改为先建 account store 再返回：

```go
		mem := store.NewMemoryStore(buildKeySeeds(cfg.Auth))
		adminChannels := configToAdminChannels(cfg.Channels)
		accStore := account.NewMemoryStore(mem)
		return dataLayer{
			store:      mem,
			adminStore: admin.NewMemoryStore(mem, adminChannels),
			account:    accStore,
			settings:   accStore,
			sink:       billing.NopSink{},
			channels:   forwarder.ChannelsFromConfig(cfg.Channels),
			pool:       nil,
		}
```

- [ ] **Step 4: PG 分支装配 account/settings（`buildDataLayer` 末尾 `return dataLayer{...}`）**

把 PG 分支的 return 改为：

```go
	accStore := account.NewPGStore(pool)
	return dataLayer{
		store:      store.NewPGStore(q),
		adminStore: admin.NewPGStore(q),
		account:    accStore,
		settings:   accStore,
		sink:       billing.NewPGSink(q),
		channels:   channels,
		pool:       pool,
	}
```

- [ ] **Step 5: 在 `main()` 中装配 session 并注入 Deps**

在 `adminSvc := admin.NewService(...)` 之后、`srv := server.New(...)` 之前加：

```go
	// 会话管理器：控制台登录态载体（Redis）。
	sessions := session.NewManager(rdb)

	// 播种首个管理员账户（仅当配置了 bootstrap 且该用户名不存在时）。
	bootstrapAdmin(cfg, dl.account, logger)
```

把 `server.New` 的 `Deps` 补上新字段：

```go
	srv := server.New(cfg, server.Deps{
		Store:        dl.store,
		Redis:        rdb,
		Billing:      bill,
		Forwarder:    fwd,
		Admin:        adminSvc,
		Account:      dl.account,
		Settings:     dl.settings,
		Session:      sessions,
		SecureCookie: cfg.Server.Mode == "release", // 生产（release）走 HTTPS，加 Secure。
		Logger:       logger,
	})
```

- [ ] **Step 6: 加 `bootstrapAdmin` 函数（放 main.go 末尾）**

```go
// bootstrapAdmin 在配置了 admin.bootstrap 且该用户名尚不存在时，播种首个管理员账户。
// 幂等：已存在同名账户则跳过。密码为空时告警并跳过（绝不建空密码账户）。
func bootstrapAdmin(cfg *config.Config, accounts account.AccountStore, logger *slog.Logger) {
	bs := cfg.Admin.Bootstrap
	if !cfg.Admin.Enabled || bs.Username == "" {
		return
	}
	if bs.Password == "" {
		logger.Warn("跳过管理员播种：admin.bootstrap.username 已设但密码为空",
			"username", bs.Username)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := accounts.GetByUsername(ctx, bs.Username); err == nil {
		logger.Info("管理员账户已存在，跳过播种", "username", bs.Username)
		return
	}
	hash, err := account.HashPassword(bs.Password)
	if err != nil {
		logger.Error("管理员播种失败：密码不合规", "err", err)
		return
	}
	if _, err := accounts.CreateAccount(ctx, account.CreateAccountInput{
		Username: bs.Username, PasswordHash: hash, Role: account.RoleAdmin,
	}); err != nil {
		logger.Error("管理员播种失败", "username", bs.Username, "err", err)
		return
	}
	logger.Info("已播种首个管理员账户", "username", bs.Username)
}
```

- [ ] **Step 7: 全量编译验证**

Run: `go build ./...`
Expected: 编译通过（所有旧字段引用已清除，新依赖已接线）。

- [ ] **Step 8: Commit**

```bash
git add cmd/linapi/main.go
git commit -m "feat(main): 装配账户体系/会话，启动时播种首个管理员"
```

---

### Task 16: 全量验证 + config.example + 文档/记忆同步

**Files:**
- Modify: `config.example.yaml`（admin 段文档）
- Modify: `docs/progress.md`、`docs/modules.md`、`docs/architecture.md`、`CLAUDE.md`
- Modify: 记忆 `C:\Users\15879\.claude\projects\d--project-LinAPI\memory\linapi-progress.md` + `MEMORY.md`

**Interfaces:**
- Consumes: 全部前置任务。
- Produces: 绿色测试 + 一致文档，后端计划完成。

- [ ] **Step 1: gofmt + vet**

Run: `gofmt -l internal/ cmd/ && go vet ./...`
Expected: `gofmt -l` 无输出（无格式问题）；`go vet` 无告警。

- [ ] **Step 2: 全量测试 + 竞态检测**

Run: `CGO_ENABLED=1 go test -race ./...`
Expected: 全部包 ok，无 race 报告。

- [ ] **Step 3: 改 `config.example.yaml` 的 admin 段**

把现有 admin 段替换为：

```yaml
# 管理面与控制台。改为「账号密码 + 会话」鉴权，不再用裸 token。
admin:
  enabled: false                 # true 时挂载控制台与 /auth /admin /me 端点
  # 首个管理员播种：仅当该用户名不存在时创建。密码建议用环境变量注入：
  #   LINAPI_ADMIN_BOOTSTRAP_PASSWORD=xxxx
  bootstrap:
    username: ""                 # 为空则不播种
    password: ""                 # 为空则不播种并告警
  channel_reload_interval: 60    # 渠道定时热重载间隔（秒），<=0 关闭；仅 database.enabled=true 生效
```

- [ ] **Step 4: 更新 `docs/progress.md`**

顶部日期改为 `2026-07-10`；在运维增强段后追加一条：⑭ 统一账户认证体系（`internal/account` 账户/角色/系统设置双实现 + `internal/session` Redis 会话 + `SessionAuth`/`RequireRole` 中间件；`/auth` 注册登录登出、`/me` 用户自助、`/admin/accounts` 与 `/admin/settings`；`/admin` 由裸 token 改为会话+admin 角色；建 user 账户原子连带计费实体；启动播种首个管理员）。记录测试新增：account/session/server/middleware 各包新测试全过 -race。

- [ ] **Step 5: 更新 `docs/modules.md` 与 `docs/architecture.md`**

modules.md 加 `internal/account`、`internal/session` 两节（职责、双实现、接口）。architecture.md 补「控制台认证架构」小节：登录账户（accounts）与计费实体（users）分离、会话流程、越权硬约束、bootstrap 幂等播种。

- [ ] **Step 6: 更新 `CLAUDE.md`**

在「目录约定」加 `internal/account`、`internal/session` 两条；「架构总览」的请求生命周期不变，但补一句控制台端点鉴权走会话；「开发进度」追加第 ⑭ 步。

- [ ] **Step 7: 更新持久记忆**

更新 `linapi-progress.md`：加第 ⑭ 步账户认证体系完成、`/admin` 鉴权变更（token→会话）、新增包 account/session。`MEMORY.md` 对应条目 hook 补一句。

- [ ] **Step 8: 最终确认全绿并提交文档**

Run: `CGO_ENABLED=1 go test -race ./... && go build ./...`
Expected: 全绿。

```bash
git add config.example.yaml docs/ CLAUDE.md
git commit -m "docs: 同步账户认证体系（第 14 步）进度与配置文档"
```

---

## 自查（Self-Review）

**Spec 覆盖核对**（对照 [admin-console.md](../specs/admin-console.md)）：
- 账户/密码/角色/会话模型 → Task 1/4/5/6/7 ✅
- 注册开关 + 初始额度设置 → Task 4（Settings）+ Task 11（register 校验开关）+ Task 13（settings 端点）✅
- 登录/登出/会话 Cookie（HttpOnly+Secure+SameSite=Strict、TTL、记住我）→ Task 7/8/11 ✅
- `/admin` 改会话+admin 角色鉴权、退役裸 token → Task 9/14 ✅
- `/me` 用户自助 + 越权硬约束（key 绑定会话、他人 key 返回 404）→ Task 12 ✅
- 建 user 账户原子连带计费实体 → Task 5（内存）/Task 6（PG 事务）✅
- 预留字段 group_name / rate_multiplier 存而不用 → Task 1/2 ✅
- bootstrap 首个管理员 → Task 9/15 ✅
- 密码 bcrypt、金额 BIGINT、倍率整数百分比 → Task 3/1 ✅
- schema 双写 → Task 1 Step 3 ✅

**前端相关需求**（登录页/控制台 UI/用户面板）→ 不在本计划，见 Plan 2（前端）。

**占位符扫描**：无 TBD/TODO；每个代码步含完整代码。

**类型一致性核对**：`SessionData` 字段（AccountID/Username/Role/ExternalID）在 session/middleware/handlers 中一致；`account.Account` 无 PasswordHash（Task 4 定义、Task 13 测试断言）；`writeError`/`writeAdminError`/`queryInt`/`setEnabledReq` 均为 server 包既有符号（已核对 [admin_handlers.go](../../../internal/server/admin_handlers.go) 与 [server.go](../../../internal/server/server.go)）。

## 执行交接

计划已保存到 `docs/superpowers/plans/2026-07-10-admin-console-backend.md`。这是两份计划的第 1 份（后端）；前端计划（Plan 2）将在后端确认后另写。

两种执行方式：

1. **子代理驱动（推荐）** —— 每个任务派新子代理实现，任务间我来复核，迭代快。
2. **内联执行** —— 在本会话按批次执行，带检查点复核。

选哪种？（或先只看 Plan 1、暂不执行，等我把 Plan 2 前端计划也写出来一起定？）
