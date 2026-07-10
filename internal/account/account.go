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
	// SessionVersion 是会话代次（审查 AUD-P1-17）：登录时快照进会话 token，鉴权时比对。
	// 禁用账户、重置密码时递增，使一切旧会话立即失效（被盗 token、被禁管理员的旧 Cookie）。
	SessionVersion int `json:"session_version"`
}

// Credentials 是登录校验用的内部结构：账户视图 + 密码哈希。
// 仅 GetCredentials 返回，绝不序列化给客户端。
type Credentials struct {
	Account
	PasswordHash string
}

// CreateAccountInput 是直接新建管理员账户的入参。PasswordHash 已是 bcrypt 哈希。
// user 必须走 CreateUserAccount，保证账户与计费实体原子创建。
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
	// CreateAccount 直接建 admin 账户（供 bootstrap；拒绝 user 绕过计费实体创建）。
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
