package admin

import (
	"errors"
	"testing"

	"linapi/internal/db"
)

func TestPlanChannelKeyMigrationRequiresExplicitOptIn(t *testing.T) {
	c := testChannelKeyCipher(t, "primary", 0x23)
	rows := []db.ListChannelKeyMaterialsForUpdateRow{
		{ChannelID: "c1", ApiKey: "plaintext-one"},
		{ChannelID: "c2", ApiKey: "plaintext-two"},
	}
	if _, err := planChannelKeyMigration(rows, c, false); !errors.Is(err, ErrPlaintextChannelKeys) {
		t.Fatalf("未显式允许时必须拒绝历史明文: %v", err)
	}
	updates, err := planChannelKeyMigration(rows, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 {
		t.Fatalf("迁移数=%d, want 2", len(updates))
	}
	for i, update := range updates {
		got, err := c.Decrypt(update.channelID, update.newValue)
		if err != nil || got != rows[i].ApiKey {
			t.Fatalf("迁移结果无法还原: got=%q err=%v", got, err)
		}
	}
}

func TestPlanChannelKeyMigrationValidatesExistingEnvelopes(t *testing.T) {
	c := testChannelKeyCipher(t, "primary", 0x33)
	envelope, err := c.Encrypt("c1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	updates, err := planChannelKeyMigration([]db.ListChannelKeyMaterialsForUpdateRow{
		{ChannelID: "c1", ApiKey: envelope},
	}, c, false)
	if err != nil || len(updates) != 0 {
		t.Fatalf("合法密文不应重复迁移: updates=%d err=%v", len(updates), err)
	}
	if _, err := planChannelKeyMigration([]db.ListChannelKeyMaterialsForUpdateRow{
		{ChannelID: "other-channel", ApiKey: envelope},
	}, c, true); !errors.Is(err, ErrInvalidChannelKeyEnvelope) {
		t.Fatalf("AAD 不匹配的已有密文必须阻止启动: %v", err)
	}
}
