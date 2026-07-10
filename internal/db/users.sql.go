package db

import (
	"context"
)

const getUserByExternalID = `-- name: GetUserByExternalID :one
SELECT id, external_id, balance, enabled, created_at, updated_at
FROM users
WHERE external_id = $1
`

// GetUserByExternalID 按对外用户标识取用户。
func (q *Queries) GetUserByExternalID(ctx context.Context, externalID string) (User, error) {
	row := q.db.QueryRow(ctx, getUserByExternalID, externalID)
	var i User
	err := row.Scan(
		&i.ID,
		&i.ExternalID,
		&i.Balance,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const getBalance = `-- name: GetBalance :one
SELECT balance
FROM users
WHERE external_id = $1 AND enabled = TRUE
`

// GetBalance 只取余额，供额度中间件读冷源 seed。
// 禁用或不存在的用户查不到（调用方按 0 余额处理，闸门自然拦截）。
func (q *Queries) GetBalance(ctx context.Context, externalID string) (int64, error) {
	row := q.db.QueryRow(ctx, getBalance, externalID)
	var balance int64
	err := row.Scan(&balance)
	return balance, err
}

const addBalance = `-- name: AddBalance :one
UPDATE users
SET balance = balance + $2,
	 balance_version = balance_version + 1,
    updated_at = now()
WHERE external_id = $1
RETURNING balance
`

// AddBalanceParams 是 AddBalance 的入参。
type AddBalanceParams struct {
	ExternalID string `json:"external_id"`
	Delta      int64  `json:"delta"`
}

// AddBalance 原子增减余额并返回新值，供充值/对账。Delta 为负表示扣费。
func (q *Queries) AddBalance(ctx context.Context, arg AddBalanceParams) (int64, error) {
	row := q.db.QueryRow(ctx, addBalance, arg.ExternalID, arg.Delta)
	var balance int64
	err := row.Scan(&balance)
	return balance, err
}

const createUser = `-- name: CreateUser :one
INSERT INTO users (external_id, balance, enabled)
VALUES ($1, $2, $3)
RETURNING id, external_id, balance, enabled, created_at, updated_at
`

// CreateUserParams 是 CreateUser 的入参。
type CreateUserParams struct {
	ExternalID string `json:"external_id"`
	Balance    int64  `json:"balance"`
	Enabled    bool   `json:"enabled"`
}

// CreateUser 新建用户。
func (q *Queries) CreateUser(ctx context.Context, arg CreateUserParams) (User, error) {
	row := q.db.QueryRow(ctx, createUser, arg.ExternalID, arg.Balance, arg.Enabled)
	var i User
	err := row.Scan(
		&i.ID,
		&i.ExternalID,
		&i.Balance,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

const listUsers = `-- name: ListUsers :many
SELECT id, external_id, balance, enabled, created_at, updated_at
FROM users
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2
`

// ListUsersParams 是 ListUsers 的入参（分页）。
type ListUsersParams struct {
	Limit  int32 `json:"limit"`
	Offset int32 `json:"offset"`
}

// ListUsers 分页列出用户，供管理面展示。
func (q *Queries) ListUsers(ctx context.Context, arg ListUsersParams) ([]User, error) {
	rows, err := q.db.Query(ctx, listUsers, arg.Limit, arg.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []User{}
	for rows.Next() {
		var i User
		if err := rows.Scan(
			&i.ID,
			&i.ExternalID,
			&i.Balance,
			&i.Enabled,
			&i.CreatedAt,
			&i.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const setUserEnabled = `-- name: SetUserEnabled :one
UPDATE users
SET enabled = $2,
    updated_at = now()
WHERE external_id = $1
RETURNING id, external_id, balance, enabled, created_at, updated_at
`

// SetUserEnabledParams 是 SetUserEnabled 的入参。
type SetUserEnabledParams struct {
	ExternalID string `json:"external_id"`
	Enabled    bool   `json:"enabled"`
}

// SetUserEnabled 启用/禁用用户（软删除），返回更新后的行。
func (q *Queries) SetUserEnabled(ctx context.Context, arg SetUserEnabledParams) (User, error) {
	row := q.db.QueryRow(ctx, setUserEnabled, arg.ExternalID, arg.Enabled)
	var i User
	err := row.Scan(
		&i.ID,
		&i.ExternalID,
		&i.Balance,
		&i.Enabled,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}
