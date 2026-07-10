package db

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaSQL 是建表语句，编译期嵌入二进制，启动时幂等应用（全部 IF NOT EXISTS）。
// 内容与仓库根 db/schema.sql 一致（sqlc 的源）；改表结构时两处需同步。
//
//go:embed schema.sql
var schemaSQL string

// PoolConfig 是连接池参数（从 config.DatabaseConfig 映射而来，避免 db 包反向依赖 config）。
type PoolConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	ConnMaxLifetime time.Duration
}

// NewPool 按配置建立 pgxpool 连接池，并做一次 Ping 连通性探测。
// 连不上返回错误（调用方决定是否回退到内存实现）。
func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("解析数据库 DSN 失败: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pc.MinConns = cfg.MinConns
	}
	if cfg.ConnMaxLifetime > 0 {
		pc.MaxConnLifetime = cfg.ConnMaxLifetime
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}

	// 启动期连通性探测：连不上尽早暴露。
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库连通性探测失败: %w", err)
	}

	return pool, nil
}

// ApplySchema 幂等地应用建表语句（首次启动自动建表）。
// schema.sql 全部用 IF NOT EXISTS，重复执行安全。
// 生产环境更推荐独立迁移工具（如 golang-migrate），此处为开箱即用而内置。
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("应用数据库 schema 失败: %w", err)
	}
	return nil
}
