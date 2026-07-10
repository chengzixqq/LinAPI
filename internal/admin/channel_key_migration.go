package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"linapi/internal/db"
)

var ErrPlaintextChannelKeys = errors.New("admin: 检测到 PostgreSQL 明文渠道密钥")

type channelKeyMigrationUpdate struct {
	channelID string
	oldValue  string
	newValue  string
}

// MigrateChannelAPIKeys 在单个事务内验证全部现有密文，并按显式开关迁移历史明文。
// allowPlaintext=false 时只要发现一行明文就整体失败，绝不继续以明文启动。
func MigrateChannelAPIKeys(
	ctx context.Context,
	pool *pgxpool.Pool,
	cipher *ChannelKeyCipher,
	allowPlaintext bool,
) (migrated int, err error) {
	if pool == nil || cipher == nil {
		return 0, ErrChannelKeyEncryptionRequired
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("开始渠道密钥迁移事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := db.New(tx)
	rows, err := q.ListChannelKeyMaterialsForUpdate(ctx)
	if err != nil {
		return 0, fmt.Errorf("锁定渠道密钥失败: %w", err)
	}
	updates, err := planChannelKeyMigration(rows, cipher, allowPlaintext)
	if err != nil {
		return 0, err
	}
	for _, update := range updates {
		n, updateErr := q.UpdateChannelKeyMaterial(ctx, db.UpdateChannelKeyMaterialParams{
			ChannelID: update.channelID,
			ApiKey:    update.newValue,
			OldApiKey: update.oldValue,
		})
		if updateErr != nil {
			return 0, fmt.Errorf("迁移渠道密钥失败: %w", updateErr)
		}
		if n != 1 {
			return 0, fmt.Errorf("迁移渠道密钥失败: 数据在事务内发生冲突")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("提交渠道密钥迁移事务失败: %w", err)
	}
	return len(updates), nil
}

func planChannelKeyMigration(
	rows []db.ListChannelKeyMaterialsForUpdateRow,
	cipher *ChannelKeyCipher,
	allowPlaintext bool,
) ([]channelKeyMigrationUpdate, error) {
	if cipher == nil {
		return nil, ErrChannelKeyEncryptionRequired
	}
	plaintextCount := 0
	for _, row := range rows {
		if !isChannelKeyEnvelope(row.ApiKey) {
			plaintextCount++
			continue
		}
		if _, err := cipher.Decrypt(row.ChannelID, row.ApiKey); err != nil {
			return nil, fmt.Errorf("渠道 %q 的密钥密文无法验证: %w", row.ChannelID, ErrInvalidChannelKeyEnvelope)
		}
	}
	if plaintextCount > 0 && !allowPlaintext {
		return nil, fmt.Errorf("%w（%d 行）；请在维护窗口仅一次启用 database.channel_key_encryption.migrate_plaintext，迁移成功后立即关闭", ErrPlaintextChannelKeys, plaintextCount)
	}

	updates := make([]channelKeyMigrationUpdate, 0, plaintextCount)
	for _, row := range rows {
		if isChannelKeyEnvelope(row.ApiKey) {
			continue
		}
		encrypted, err := cipher.Encrypt(row.ChannelID, row.ApiKey)
		if err != nil {
			return nil, fmt.Errorf("加密历史渠道密钥失败: %w", err)
		}
		updates = append(updates, channelKeyMigrationUpdate{
			channelID: row.ChannelID,
			oldValue:  row.ApiKey,
			newValue:  encrypted,
		})
	}
	return updates, nil
}
