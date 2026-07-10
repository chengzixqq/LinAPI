package billing

import (
	"context"
	"testing"
	"time"

	"linapi/internal/db"
)

// fakeInsertQuerier 是 db.Querier 的测试替身，只捕获 InsertUsageLog 的入参。
type fakeInsertQuerier struct {
	db.Querier // 未实现的方法会 panic（本测试不触达）。
	calls      []db.InsertUsageLogParams
	err        error
}

func (f *fakeInsertQuerier) InsertUsageLog(_ context.Context, arg db.InsertUsageLogParams) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, arg)
	return nil
}

func TestPGSinkWriteMapsFields(t *testing.T) {
	q := &fakeInsertQuerier{}
	sink := NewPGSink(q)

	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	records := []UsageRecord{
		{
			RequestID: "req-1", UserID: "u1", KeyID: "k1",
			Model: "gpt-4o", Channel: "ch-a",
			InputTokens: 100, OutputTokens: 50, Cost: 1234, CreatedAt: ts,
		},
		{
			RequestID: "req-2", UserID: "u2", KeyID: "k2",
			Model: "claude-3", Channel: "ch-b",
			InputTokens: 7, OutputTokens: 9, Cost: 55, CreatedAt: ts,
		},
	}

	if err := sink.Write(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 2 {
		t.Fatalf("应写入 2 条，实际 %d", len(q.calls))
	}

	got := q.calls[0]
	if got.RequestID != "req-1" || got.UserID != "u1" || got.KeyID != "k1" {
		t.Fatalf("归因字段映射错误: %+v", got)
	}
	if got.Model != "gpt-4o" || got.Channel != "ch-a" {
		t.Fatalf("模型/渠道映射错误: %+v", got)
	}
	if got.InputTokens != 100 || got.OutputTokens != 50 || got.Cost != 1234 {
		t.Fatalf("用量/成本映射错误: %+v", got)
	}
	if !got.CreatedAt.Valid || !got.CreatedAt.Time.Equal(ts) {
		t.Fatalf("时间戳映射错误: %+v", got.CreatedAt)
	}
}

func TestPGSinkWritePropagatesError(t *testing.T) {
	// 底层写库出错应向上抛出（交由 Recorder 记日志，不阻断主流程）。
	q := &fakeInsertQuerier{err: context.DeadlineExceeded}
	sink := NewPGSink(q)

	err := sink.Write(context.Background(), []UsageRecord{{RequestID: "x"}})
	if err != context.DeadlineExceeded {
		t.Fatalf("期望透传底层错误, 实际 %v", err)
	}
}

func TestPGSinkWriteEmpty(t *testing.T) {
	// 空批次不应触发任何写库。
	q := &fakeInsertQuerier{}
	if err := NewPGSink(q).Write(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 0 {
		t.Fatalf("空批次不应写库, 实际 %d 次", len(q.calls))
	}
}
