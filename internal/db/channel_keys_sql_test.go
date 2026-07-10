package db

import (
	"strings"
	"testing"
)

func TestChannelKeySQLLocksMigratesAndPreservesSecret(t *testing.T) {
	if !strings.Contains(listChannelKeyMaterialsForUpdate, "FOR UPDATE") {
		t.Fatal("启动迁移必须锁定渠道行")
	}
	if !strings.Contains(updateChannelKeyMaterial, "api_key = $3") {
		t.Fatal("迁移更新必须比较锁定时旧密文")
	}
	if !strings.Contains(updateChannel, "CASE WHEN $10 THEN $5 ELSE api_key END") {
		t.Fatal("渠道更新省略 api_key 时必须由 SQL 原子保留旧值")
	}
	if !strings.Contains(schemaSQL, "channels_api_key_envelope_check") ||
		!strings.Contains(schemaSQL, "NOT VALID") {
		t.Fatal("schema 必须先以 NOT VALID 约束阻止新增明文并允许旧数据迁移")
	}
}
