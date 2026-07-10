package billing

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// UsageRecord 是一条计费用量日志，转发完成后生成，异步落库。
type UsageRecord struct {
	// RequestID 关联该请求的追踪 ID（幂等/对账用）。
	RequestID string
	// UserID / KeyID 计费归因维度。
	UserID string
	KeyID  string
	// Model 是对外模型名（计价维度）。
	Model string
	// Channel 是实际命中的上游渠道 ID（成本分析用）。
	Channel string
	// InputTokens / OutputTokens 是真实用量。
	InputTokens  int
	OutputTokens int
	// Cost 是本次实际扣费（最小计费单位）。
	Cost int64
	// CreatedAt 是记账时间。
	CreatedAt time.Time
}

// Sink 是用量日志的落库目的地。第 7 步由 sqlc/PostgreSQL 实现批量 INSERT。
//
// 约定：Write 应尽量幂等（按 RequestID），以容忍进程崩溃后的重放。
type Sink interface {
	// Write 持久化一批用量日志。返回错误由调用方决定重试或丢弃策略。
	Write(ctx context.Context, records []UsageRecord) error
}

// NopSink 丢弃所有日志，用于未接入持久层时（当前阶段）与测试。
type NopSink struct{}

// Write 实现 Sink，不做任何事。
func (NopSink) Write(context.Context, []UsageRecord) error { return nil }

// RecorderConfig 配置异步用量记录器。
type RecorderConfig struct {
	// BufferSize 是内存缓冲队列容量。队列满时 Record 转为同步写，避免丢账单。
	BufferSize int
	// BatchSize 是单次落库的最大条数。
	BatchSize int
	// FlushInterval 是即使未攒够 BatchSize 也强制落库的间隔。
	FlushInterval time.Duration
}

// 默认参数：面向高并发下的批量写入与可接受的落库延迟。
const (
	defaultBufferSize    = 4096
	defaultBatchSize     = 128
	defaultFlushInterval = time.Second
)

func (c RecorderConfig) withDefaults() RecorderConfig {
	if c.BufferSize <= 0 {
		c.BufferSize = defaultBufferSize
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = defaultFlushInterval
	}
	return c
}

// Recorder 把用量日志放入内存队列，由后台 goroutine 批量落库。
//
// 设计取舍：计费结算的关键路径（Reserve/Settle）走 Redis 保证一致；
// 用量日志属「记账凭证」，可容忍毫秒级延迟，故异步批量以降低 DB 压力。
// 队列满时退化为同步写（宁可慢一点也不丢账单）。
type Recorder struct {
	sink   Sink
	cfg    RecorderConfig
	logger *slog.Logger

	ch   chan UsageRecord
	wg   sync.WaitGroup
	once sync.Once
}

// NewRecorder 创建并启动异步用量记录器。调用方须在关闭时调用 Close 以冲刷残留日志。
func NewRecorder(sink Sink, cfg RecorderConfig, logger *slog.Logger) *Recorder {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	r := &Recorder{
		sink:   sink,
		cfg:    cfg,
		logger: logger,
		ch:     make(chan UsageRecord, cfg.BufferSize),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

// Record 提交一条用量日志。非阻塞：队列有空位则入队；
// 队列满则同步落库兜底（保证不丢账单，代价是该次调用变慢）。
func (r *Recorder) Record(rec UsageRecord) {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	select {
	case r.ch <- rec:
	default:
		// 缓冲已满：同步兜底写，避免丢弃账单。
		r.flush([]UsageRecord{rec})
	}
}

// loop 是后台批量落库循环：攒够 BatchSize 或到 FlushInterval 即冲刷。
func (r *Recorder) loop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]UsageRecord, 0, r.cfg.BatchSize)

	for {
		select {
		case rec, ok := <-r.ch:
			if !ok {
				// 通道关闭：冲刷残留并退出。
				if len(batch) > 0 {
					r.flush(batch)
				}
				return
			}
			batch = append(batch, rec)
			if len(batch) >= r.cfg.BatchSize {
				r.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush 将一批日志写入 Sink，失败仅记日志（用量日志不阻断主流程）。
func (r *Recorder) flush(batch []UsageRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.sink.Write(ctx, batch); err != nil {
		r.logger.Error("用量日志落库失败", "count", len(batch), "err", err)
	}
}

// Close 停止后台循环并冲刷残留日志。幂等，可安全并发调用。
func (r *Recorder) Close() {
	r.once.Do(func() {
		close(r.ch)
		r.wg.Wait()
	})
}
