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
