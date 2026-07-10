package db

import (
	"strings"
	"testing"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("至少应内置一个数据库迁移")
	}
	for i, m := range migrations {
		if m.Version <= 0 || m.Name == "" || m.SQL == "" || len(m.Checksum) != 64 {
			t.Fatalf("迁移元数据非法: %+v", m)
		}
		if i > 0 && migrations[i-1].Version >= m.Version {
			t.Fatalf("迁移未严格递增: %d >= %d", migrations[i-1].Version, m.Version)
		}
	}
}

func TestLegacyMigrationContainsKnownUpgrades(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	sql := migrations[0].SQL
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS rate_multiplier",
		"CREATE TABLE IF NOT EXISTS billing_reservations",
		"ADD COLUMN IF NOT EXISTS session_version",
		"accounts_role_external_id_check",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("首个迁移缺少已知升级语句 %q", required)
		}
	}
}
