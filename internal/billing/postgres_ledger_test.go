package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"linapi/internal/db"
)

func TestSamePostgresReservationChecksIdempotencyFields(t *testing.T) {
	r := Reservation{
		ID: "res-1", TraceID: "trace-1", UserID: "u1", KeyID: "k1", Model: "m1",
		Amount: 100, MaxInputTokens: 1000, MaxOutputTokens: 200,
	}
	row := db.BillingReservation{
		ReservationID: "res-1", TraceID: "trace-1", UserID: "u1", KeyID: "k1", Model: "m1",
		Amount: 100, MaxInputTokens: 1000, MaxOutputTokens: 200,
	}
	if !samePostgresReservation(row, r) {
		t.Fatal("相同 reservation 应被视为幂等重放")
	}
	r.Amount++
	if samePostgresReservation(row, r) {
		t.Fatal("金额变化必须判定为幂等冲突")
	}
}

func TestSamePostgresConsumptionIgnoresRecordingTimestamp(t *testing.T) {
	c := Consumption{
		ReservationID: "res-1", Channel: "ch1", InputTokens: 10, OutputTokens: 5,
		CacheCreationInputTokens: 2, CacheReadInputTokens: 3, ReportedTotalTokens: 20,
		Cost: 99, UsageComplete: true, RecordedAt: time.Now(),
	}
	row := db.BillingReservation{
		ReservationID: "res-1", Channel: "ch1", InputTokens: 10, OutputTokens: 5,
		CacheCreationInputTokens: 2, CacheReadInputTokens: 3, ReportedTotalTokens: 20,
		Cost: 99, UsageComplete: true,
	}
	if !samePostgresConsumption(row, c) {
		t.Fatal("相同消费事实应允许使用不同重试时间戳")
	}
	c.OutputTokens++
	if samePostgresConsumption(row, c) {
		t.Fatal("token 用量变化必须判定为幂等冲突")
	}
}

// TestPostgresLedgerContract 需要真实 PostgreSQL，以验证事务、行锁和恢复路径。
// 本地/CI 设置 LINAPI_TEST_DATABASE_DSN 后执行；未配置时普通单测保持自包含。
func TestPostgresLedgerContract(t *testing.T) {
	dsn := os.Getenv("LINAPI_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("未设置 LINAPI_TEST_DATABASE_DSN，跳过 PostgreSQL 契约测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := "ledger-user-" + suffix
	reservationIDs := []string{"res-settle-" + suffix, "res-refund-" + suffix, "res-recover-" + suffix, "res-poor-" + suffix}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM billing_ledger WHERE reservation_id = ANY($1)", reservationIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM usage_logs WHERE request_id = ANY($1)", reservationIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM billing_reservations WHERE reservation_id = ANY($1)", reservationIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE external_id = $1", userID)
	})

	if _, err := pool.Exec(ctx, "INSERT INTO users (external_id, balance, enabled) VALUES ($1, 1000, TRUE)", userID); err != nil {
		t.Fatal(err)
	}
	ledger := NewPostgresLedger(pool)
	now := time.Now().UTC()

	settleReservation := Reservation{
		ID: reservationIDs[0], TraceID: "trace", UserID: userID, KeyID: "key", Model: "model",
		Amount: 600, MaxInputTokens: 1000, MaxOutputTokens: 100, CreatedAt: now,
	}
	ok, err := ledger.Reserve(ctx, settleReservation)
	if err != nil || !ok {
		t.Fatalf("Reserve 应成功: ok=%v err=%v", ok, err)
	}
	if ok, err := ledger.Reserve(ctx, settleReservation); err != nil || !ok {
		t.Fatalf("Reserve 重放应幂等成功: ok=%v err=%v", ok, err)
	}
	conflict := settleReservation
	conflict.Amount++
	if _, err := ledger.Reserve(ctx, conflict); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Reserve 参数变化应冲突，得到 %v", err)
	}
	if err := ledger.MarkInFlight(ctx, settleReservation.ID, "ch"); err != nil {
		t.Fatal(err)
	}

	consumption := Consumption{
		ReservationID: settleReservation.ID, Channel: "ch", InputTokens: 100, OutputTokens: 50,
		CacheCreationInputTokens: 10, CacheReadInputTokens: 20, ReportedTotalTokens: 180,
		Cost: 250, UsageComplete: true, RecordedAt: now,
	}
	if err := ledger.RecordConsumption(ctx, consumption); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Finalize(ctx, settleReservation.ID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordConsumption(ctx, consumption); err != nil {
		t.Fatalf("已结算 consumption 重放应幂等: %v", err)
	}
	if err := ledger.Finalize(ctx, settleReservation.ID); err != nil {
		t.Fatalf("Finalize 重放应幂等: %v", err)
	}
	if err := ledger.Refund(ctx, settleReservation.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("settled reservation 不得退款，得到 %v", err)
	}

	refundReservation := Reservation{
		ID: reservationIDs[1], TraceID: "trace", UserID: userID, KeyID: "key", Model: "model",
		Amount: 300, MaxInputTokens: 1000, MaxOutputTokens: 100, CreatedAt: now,
	}
	if ok, err := ledger.Reserve(ctx, refundReservation); err != nil || !ok {
		t.Fatalf("退款用 Reserve 应成功: ok=%v err=%v", ok, err)
	}
	if err := ledger.Refund(ctx, refundReservation.ID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Refund(ctx, refundReservation.ID); err != nil {
		t.Fatalf("Refund 重放应幂等: %v", err)
	}

	recoverReservation := Reservation{
		ID: reservationIDs[2], TraceID: "trace", UserID: userID, KeyID: "key", Model: "model",
		Amount: 100, MaxInputTokens: 1000, MaxOutputTokens: 100, CreatedAt: now,
	}
	if ok, err := ledger.Reserve(ctx, recoverReservation); err != nil || !ok {
		t.Fatalf("恢复用 Reserve 应成功: ok=%v err=%v", ok, err)
	}
	if err := ledger.MarkInFlight(ctx, recoverReservation.ID, "ch"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordConsumption(ctx, Consumption{
		ReservationID: recoverReservation.ID, Channel: "ch", InputTokens: 10, OutputTokens: 5,
		Cost: 40, UsageComplete: true, RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Refund(ctx, recoverReservation.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("consumed_unsettled reservation 不得退款，得到 %v", err)
	}
	if err := ledger.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Recover(ctx); err != nil {
		t.Fatalf("Recover 重放应幂等: %v", err)
	}

	poorReservation := Reservation{
		ID: reservationIDs[3], TraceID: "trace", UserID: userID, KeyID: "key", Model: "model",
		Amount: 800, MaxInputTokens: 1000, MaxOutputTokens: 100, CreatedAt: now,
	}
	if ok, err := ledger.Reserve(ctx, poorReservation); err != nil || ok {
		t.Fatalf("余额不足应拒绝且不报错: ok=%v err=%v", ok, err)
	}

	var balance, version int64
	if err := pool.QueryRow(ctx, "SELECT balance, balance_version FROM users WHERE external_id = $1", userID).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	if balance != 710 || version != 6 {
		t.Fatalf("最终余额/版本错误: balance=%d version=%d，期望 710/6", balance, version)
	}
	var ledgerCount, usageCount, poorCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM billing_ledger WHERE reservation_id = ANY($1)", reservationIDs).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM usage_logs WHERE request_id = ANY($1)", reservationIDs).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM billing_reservations WHERE reservation_id = $1", poorReservation.ID).Scan(&poorCount); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 6 || usageCount != 2 || poorCount != 0 {
		t.Fatalf("持久记录数量错误: ledger=%d usage=%d poor=%d", ledgerCount, usageCount, poorCount)
	}
}
