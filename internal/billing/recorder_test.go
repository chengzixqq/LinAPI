package billing

import (
	"context"
	"sync"
	"testing"
	"time"
)

// captureSink 记录收到的所有用量日志，供断言。并发安全。
type captureSink struct {
	mu      sync.Mutex
	records []UsageRecord
	// writeErr 若非 nil，Write 返回它（模拟落库失败）。
	writeErr error
}

func (s *captureSink) Write(_ context.Context, recs []UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.records = append(s.records, recs...)
	return nil
}

func (s *captureSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func TestRecorderFlushOnClose(t *testing.T) {
	sink := &captureSink{}
	// 大批量 + 长间隔：确保是 Close 触发的冲刷，而非攒够或定时。
	r := NewRecorder(sink, RecorderConfig{
		BufferSize:    100,
		BatchSize:     1000,
		FlushInterval: time.Hour,
	}, nil)

	const n = 10
	for i := 0; i < n; i++ {
		r.Record(UsageRecord{RequestID: "req", UserID: "u1", Cost: 1})
	}
	r.Close()

	if got := sink.count(); got != n {
		t.Fatalf("Close 后应冲刷全部 %d 条，实得 %d", n, got)
	}
}

func TestRecorderFlushOnBatchSize(t *testing.T) {
	sink := &captureSink{}
	r := NewRecorder(sink, RecorderConfig{
		BufferSize:    100,
		BatchSize:     5,
		FlushInterval: time.Hour, // 排除定时冲刷干扰
	}, nil)
	defer r.Close()

	for i := 0; i < 5; i++ {
		r.Record(UsageRecord{RequestID: "req"})
	}

	// 攒够 BatchSize 应很快落库；轮询等待避免时序脆弱。
	waitFor(t, func() bool { return sink.count() == 5 }, time.Second)
}

func TestRecorderSyncFallbackWhenFull(t *testing.T) {
	sink := &captureSink{}
	// 缓冲极小且后台来不及消费时，Record 应同步兜底写，不丢日志。
	// 用 BatchSize 大 + 长间隔让后台 goroutine「卡住」不消费，逼出兜底路径。
	r := NewRecorder(sink, RecorderConfig{
		BufferSize:    1,
		BatchSize:     1000,
		FlushInterval: time.Hour,
	}, nil)
	defer r.Close()

	// 填满缓冲后，后续 Record 直接同步落库。
	total := 50
	for i := 0; i < total; i++ {
		r.Record(UsageRecord{RequestID: "req"})
	}

	// 关闭后所有记录都应到达 sink（缓冲内 + 同步兜底的）。
	r.Close()
	if got := sink.count(); got != total {
		t.Fatalf("不应丢日志：期望 %d，实得 %d", total, got)
	}
}

func TestRecorderCloseIdempotent(t *testing.T) {
	r := NewRecorder(&captureSink{}, RecorderConfig{}, nil)
	r.Close()
	r.Close() // 二次关闭不应 panic
}

func TestRecorderSetsCreatedAt(t *testing.T) {
	sink := &captureSink{}
	r := NewRecorder(sink, RecorderConfig{}, nil)
	r.Record(UsageRecord{RequestID: "req"}) // 不设 CreatedAt
	r.Close()

	if sink.count() != 1 {
		t.Fatal("应有 1 条记录")
	}
	if sink.records[0].CreatedAt.IsZero() {
		t.Error("Record 应自动填充 CreatedAt")
	}
}

// waitFor 轮询 cond 直到为真或超时。
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
