package db

import (
	"strings"
	"testing"
)

// TestBillingStateTransitionSQL 防止手写 sqlc 同构产物落后于 db/query.sql。
// RecordConsumption 必须接在 MarkInFlight 之后，否则生产 PG 路径永远更新 0 行。
func TestBillingStateTransitionSQL(t *testing.T) {
	if !strings.Contains(recordBillingConsumption, "status = 'consumed_unsettled'") ||
		!strings.Contains(recordBillingConsumption, "status = 'in_flight'") {
		t.Fatalf("RecordBillingConsumption 状态迁移 SQL 错误:\n%s", recordBillingConsumption)
	}
	if strings.Contains(recordBillingConsumption, "AND status = 'reserved'") {
		t.Fatalf("RecordBillingConsumption 不得从 reserved 直接消费:\n%s", recordBillingConsumption)
	}
}
