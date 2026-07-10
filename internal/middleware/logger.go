package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// 请求级上下文键与请求 ID 响应头。
const (
	ctxKeyRequestID = "linapi.request_id"
	ctxKeyAccessLog = "linapi.access_log"
	headerRequestID = "X-Request-Id"
)

// accessLog 汇集一次请求在处理过程中被逐步填充的业务字段（模型 / 渠道 / 用量）。
// 由 RequestLogger 在入口创建并放入 context，转发层在处理中回填，收尾时统一输出。
// 仅在单个请求的 handler 链（同一 goroutine）内顺序读写，无需额外加锁。
type accessLog struct {
	model        string
	channel      string
	inputTokens  int
	outputTokens int
}

// RequestIDFrom 返回本次请求的唯一 ID（RequestLogger 在入口注入）。
// 未挂 RequestLogger 时返回 ("", false)，调用方应自行兜底生成。
func RequestIDFrom(c *gin.Context) (string, bool) {
	v, ok := c.Get(ctxKeyRequestID)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// accessLogFrom 取出请求级日志载体；未挂 RequestLogger 时返回 (nil, false)，
// 使各 SetLog* 在无中间件（如转发层单测）时退化为无操作。
func accessLogFrom(c *gin.Context) (*accessLog, bool) {
	v, ok := c.Get(ctxKeyAccessLog)
	if !ok {
		return nil, false
	}
	al, ok := v.(*accessLog)
	return al, ok
}

// SetLogModel 回填本次请求命中的对外模型名（转发层解析请求后调用）。
func SetLogModel(c *gin.Context, model string) {
	if al, ok := accessLogFrom(c); ok {
		al.model = model
	}
}

// SetLogUpstream 回填实际命中的上游渠道 ID（转发层选定候选后调用）。
func SetLogUpstream(c *gin.Context, channel string) {
	if al, ok := accessLogFrom(c); ok {
		al.channel = channel
	}
}

// SetLogUsage 回填本次请求的 token 用量（转发层结算时调用）。
func SetLogUsage(c *gin.Context, inputTokens, outputTokens int) {
	if al, ok := accessLogFrom(c); ok {
		al.inputTokens = inputTokens
		al.outputTokens = outputTokens
	}
}

// RequestLogger 构建结构化访问日志中间件：
//
// 入口为每个请求分配 request_id（优先复用入站 X-Request-Id 头，便于跨服务串联），
// 注入 context 与响应头；收尾按状态码选级别（5xx→Error，4xx→Warn，其余→Info），
// 输出方法 / 路径 / 状态 / 耗时 / 客户端 IP / 调用方身份，以及转发层回填的
// model / channel / token 用量（缺失字段省略，避免噪声）。
//
// skip 中的路径（如 /healthz、/metrics）不记日志，避免探活与指标抓取淹没业务日志。
func RequestLogger(logger *slog.Logger, skip ...string) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	skipSet := make(map[string]struct{}, len(skip))
	for _, p := range skip {
		skipSet[p] = struct{}{}
	}
	return func(c *gin.Context) {
		rid := c.GetHeader(headerRequestID)
		if rid == "" {
			rid = newRequestID()
		}
		c.Set(ctxKeyRequestID, rid)
		c.Header(headerRequestID, rid)

		al := &accessLog{}
		c.Set(ctxKeyAccessLog, al)

		start := time.Now()
		c.Next()

		// 路由匹配后才有 FullPath；跳过探活 / 指标端点。
		if _, skipped := skipSet[c.FullPath()]; skipped {
			return
		}

		status := c.Writer.Status()
		attrs := []slog.Attr{
			slog.String("request_id", rid),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Float64("latency_ms", float64(time.Since(start).Microseconds())/1000.0),
			slog.String("client_ip", c.ClientIP()),
		}
		if id, ok := IdentityFrom(c); ok {
			attrs = append(attrs,
				slog.String("user_id", id.UserID),
				slog.String("key_id", id.KeyID),
			)
		}
		if al.model != "" {
			attrs = append(attrs, slog.String("model", al.model))
		}
		if al.channel != "" {
			attrs = append(attrs, slog.String("channel", al.channel))
		}
		if al.inputTokens > 0 || al.outputTokens > 0 {
			attrs = append(attrs,
				slog.Int("input_tokens", al.inputTokens),
				slog.Int("output_tokens", al.outputTokens),
			)
		}

		const msg = "http_request"
		ctx := c.Request.Context()
		switch {
		case status >= 500:
			logger.LogAttrs(ctx, slog.LevelError, msg, attrs...)
		case status >= 400:
			logger.LogAttrs(ctx, slog.LevelWarn, msg, attrs...)
		default:
			logger.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
		}
	}
}

// newRequestID 生成随机请求 ID。与转发层同风格（"req_" + hex），
// 便于访问日志与计费用量日志按同一 ID 对账。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极罕见：退化为时间戳，仍保证基本可读性与唯一性。
		return "req_" + time.Now().Format("20060102150405.000000")
	}
	return "req_" + hex.EncodeToString(b[:])
}
