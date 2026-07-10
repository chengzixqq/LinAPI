// Package db 是数据库访问层。
//
// 本包的代码按 sqlc（engine=postgresql, sql_package=pgx/v5）的生成约定编写，
// 与 `sqlc generate` 的产物同构：db.go(骨架) / models.go(表模型) / querier.go(接口) /
// *.sql.go(查询实现)。源定义在仓库根的 db/schema.sql 与 db/query.sql，
// 生成配置见 sqlc.yaml。一旦环境可安装 sqlc，`sqlc generate` 可原样覆盖本目录。
//
// 手写而非生成的唯一原因：当前环境无法联网安装 sqlc 二进制（见 sqlc.yaml 注释）。
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX 是查询所需的最小数据库接口，*pgxpool.Pool 与 pgx.Tx 均满足它，
// 因此 Queries 既可直接用连接池，也可在事务中用 WithTx 派生。
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// New 用一个 DBTX（连接池或事务）构造查询器。
func New(db DBTX) *Queries {
	return &Queries{db: db}
}

// Queries 持有数据库句柄，所有查询方法挂在其上。并发安全性取决于底层 DBTX
// （*pgxpool.Pool 并发安全；单个 pgx.Tx 不可并发使用）。
type Queries struct {
	db DBTX
}

// WithTx 返回一个绑定到指定事务的新查询器，用于把多条查询纳入同一事务。
func (q *Queries) WithTx(tx pgx.Tx) *Queries {
	return &Queries{db: tx}
}
