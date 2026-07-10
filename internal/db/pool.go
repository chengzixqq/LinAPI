package db

import (
	"context"
	"crypto/sha256"
	"embed"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaSQL 是建表语句，编译期嵌入二进制，启动时幂等应用（全部 IF NOT EXISTS）。
// 内容与仓库根 db/schema.sql 一致（sqlc 的源）；改表结构时两处需同步。
//
//go:embed schema.sql
var schemaSQL string

// migrationFS 保存既有数据库的增量升级脚本。schema.sql 只用于全新数据库；一旦
// 某个版本发布，升级内容必须追加到 migrations/，不能依赖修改 CREATE TABLE。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationLockID int64 = 0x4c696e415049 // "LinAPI"

type migration struct {
	Version  int64
	Name     string
	SQL      string
	Checksum string
}

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

// ApplySchema 在同一事务与 advisory lock 下应用版本化迁移。
//
// 全新数据库直接应用当前 schema.sql，再把所有已包含的迁移版本登记为完成；既有
// 数据库只执行尚未登记的 migrations/*.sql。这样 CREATE TABLE IF NOT EXISTS 不会
// 再伪装成升级机制，多实例并发启动也只会有一个实例实际迁移。
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开始数据库迁移事务失败: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // Commit 后回滚为空操作

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("获取数据库迁移锁失败: %w", err)
	}
	if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    BIGINT      PRIMARY KEY,
    name       TEXT        NOT NULL,
    checksum   TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("创建迁移版本表失败: %w", err)
	}

	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}

	var hasCoreSchema bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL`).Scan(&hasCoreSchema); err != nil {
		return fmt.Errorf("探测数据库 schema 失败: %w", err)
	}

	if !hasCoreSchema {
		if len(applied) != 0 {
			return fmt.Errorf("数据库迁移记录存在但核心 users 表缺失，拒绝自动重建")
		}
		if _, err := tx.Exec(ctx, schemaSQL); err != nil {
			return fmt.Errorf("初始化数据库 schema 失败: %w", err)
		}
		for _, m := range migrations {
			if err := recordMigration(ctx, tx, m); err != nil {
				return err
			}
		}
	} else {
		for _, m := range migrations {
			if _, ok := applied[m.Version]; ok {
				continue
			}
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("应用数据库迁移 %04d_%s 失败: %w", m.Version, m.Name, err)
			}
			if err := recordMigration(ctx, tx, m); err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交数据库迁移失败: %w", err)
	}
	return nil
}

// ValidateChannelKeyEnvelopeConstraint 在历史明文迁移后验证 channels 全表约束。
// 仅在 ApplySchema 已确保约束存在后调用。
func ValidateChannelKeyEnvelopeConstraint(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `ALTER TABLE channels VALIDATE CONSTRAINT channels_api_key_envelope_check`); err != nil {
		return fmt.Errorf("验证渠道密钥密文约束失败: %w", err)
	}
	return nil
}

// VerifySchema 在关闭 auto_migrate 时校验数据库已处于当前二进制认识的版本。
// 缺少迁移、迁移文件被改写或数据库版本高于当前二进制都 fail-closed。
func VerifySchema(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("读取 schema_migrations 失败（请先启用 auto_migrate 或执行迁移）: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]migration)
	for rows.Next() {
		var m migration
		if err := rows.Scan(&m.Version, &m.Name, &m.Checksum); err != nil {
			return fmt.Errorf("读取迁移版本失败: %w", err)
		}
		applied[m.Version] = m
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历迁移版本失败: %w", err)
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}
	for _, m := range migrations {
		if _, ok := applied[m.Version]; !ok {
			return fmt.Errorf("数据库缺少迁移版本 %04d_%s", m.Version, m.Name)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内置迁移失败: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	seen := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".sql")
		versionText, name, ok := strings.Cut(stem, "_")
		if !ok || name == "" {
			return nil, fmt.Errorf("迁移文件名 %q 必须为 <version>_<name>.sql", entry.Name())
		}
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("迁移文件 %q 的版本号非法", entry.Name())
		}
		if _, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("迁移版本 %d 重复", version)
		}
		seen[version] = struct{}{}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("读取迁移 %q 失败: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("迁移 %q 为空", entry.Name())
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, migration{
			Version: version, Name: name, SQL: string(body), Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	if len(migrations) == 0 {
		return nil, fmt.Errorf("未找到任何内置数据库迁移")
	}
	return migrations, nil
}

func readAppliedMigrations(ctx context.Context, tx pgx.Tx) (map[int64]migration, error) {
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("读取迁移版本失败: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]migration)
	for rows.Next() {
		var m migration
		if err := rows.Scan(&m.Version, &m.Name, &m.Checksum); err != nil {
			return nil, fmt.Errorf("读取迁移版本失败: %w", err)
		}
		applied[m.Version] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历迁移版本失败: %w", err)
	}
	return applied, nil
}

func validateAppliedMigrations(applied map[int64]migration, known []migration) error {
	knownByVersion := make(map[int64]migration, len(known))
	for _, m := range known {
		knownByVersion[m.Version] = m
	}
	for version, got := range applied {
		want, ok := knownByVersion[version]
		if !ok {
			return fmt.Errorf("数据库包含当前二进制未知的迁移版本 %d，拒绝降级运行", version)
		}
		if got.Name != want.Name || got.Checksum != want.Checksum {
			return fmt.Errorf("迁移版本 %d 的名称或校验和不匹配，迁移文件不可改写", version)
		}
	}
	return nil
}

func recordMigration(ctx context.Context, tx pgx.Tx, m migration) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		m.Version, m.Name, m.Checksum,
	); err != nil {
		return fmt.Errorf("记录数据库迁移 %04d_%s 失败: %w", m.Version, m.Name, err)
	}
	return nil
}
