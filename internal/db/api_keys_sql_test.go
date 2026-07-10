package db

import (
	"strings"
	"testing"
)

func TestCreateAPIKeyLimitedSerializesCountAndInsert(t *testing.T) {
	for _, fragment := range []string{
		"pg_advisory_xact_lock",
		"hashtextextended($3, 0)",
		"count(*) FROM api_keys WHERE user_external_id = $3",
		"< $7",
	} {
		if !strings.Contains(createAPIKeyLimited, fragment) {
			t.Fatalf("限量建 Key SQL 缺少 %q", fragment)
		}
	}
}
