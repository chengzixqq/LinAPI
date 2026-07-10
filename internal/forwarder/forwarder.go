package forwarder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"linapi/internal/adapter"
	"linapi/internal/billing"
	"linapi/internal/canonical"
	"linapi/internal/metrics"
	"linapi/internal/middleware"
	"linapi/internal/routing"
)

// Forwarder 是转发层的核心，持有路由与计费门面，向上游发起请求并结算。
// 并发安全：所有字段构建后只读，请求态放在方法局部。
type Forwarder struct {
	router   *routing.Router
	billing  *billing.Billing
	upstream *upstreamClient
	logger   *slog.Logger
}

// New 构建转发器。
func New(router *routing.Router, bill *billing.Billing, logger *slog.Logger) *Forwarder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Forwarder{
		router:   router,
		billing:  bill,
		upstream: newUpstreamClient(),
		logger:   logger,
	}
}

// Handler 返回一个 gin.HandlerFunc，clientFormat 指定客户端所用的线格式
// （"openai" / "anthropic"），决定入向 ParseRequest 与出向 BuildResponse 用哪个适配器。
func (f *Forwarder) Handler(clientFormat string) gin.HandlerFunc {
	return func(c *gin.Context) {
		f.forward(c, clientFormat)
	}
}

// forward 执行一次完整转发。计费押金已在 Quota 中间件预扣，这里负责结算：
// 成功则 Settle（按真实用量退差），否则通过 refund guard 全额退回押金。
func (f *Forwarder) forward(c *gin.Context, clientFormat string) {
	clientAdapter, ok := adapter.Get(clientFormat)
	if !ok {
		writeError(c, http.StatusInternalServerError, "internal_error", "客户端格式适配器未注册")
		return
	}

	// 退款兜底：预扣已在 Quota 中间件完成，只要本次没有成功 Settle，
	// 退出时就全额退回押金，覆盖解析失败、无渠道、全部候选失败等所有早退路径。
	res, hasRes := middleware.ReservationFrom(c)
	settled := false
	defer func() {
		if hasRes && !settled {
			f.refund(res)
		}
	}()

	// 读取并解析客户端请求。
	body, err := c.GetRawData()
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}
	req, err := clientAdapter.ParseRequest(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "解析请求失败: "+err.Error())
		return
	}
	if req.Model == "" {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "缺少 model 字段")
		return
	}

	// 模型级鉴权：密钥可能限定可访问的模型。
	if id, ok := middleware.IdentityFrom(c); ok && !id.Allows(req.Model) {
		writeError(c, http.StatusForbidden, "permission_error", "当前密钥无权访问模型 "+req.Model)
		return
	}

	// 回填模型名到预扣句柄，供 Settle 计价；并取回更新后的句柄。
	middleware.SetReservationModel(c, req.Model)
	res, hasRes = middleware.ReservationFrom(c)

	// 回填对外模型名到访问日志。
	middleware.SetLogModel(c, req.Model)

	// 路由选出候选渠道（已按优先级/权重排序、过滤熔断）。
	candidates, err := f.router.Select(req.Model)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "no_channel_error",
			"没有可服务该模型的可用渠道")
		return
	}

	// 复用访问日志中间件注入的 request_id（使访问日志与用量日志共享同一 ID 对账）；
	// 未挂中间件时（如单测）兜底自生成。
	requestID, ok := middleware.RequestIDFrom(c)
	if !ok {
		requestID = newRequestID()
	}
	clientModel := req.Model

	fc := &forwardCtx{
		clientFormat:  clientFormat,
		clientAdapter: clientAdapter,
		rawBody:       body,
		req:           req,
		clientModel:   clientModel,
		res:           res,
		requestID:     requestID,
	}

	// 依次尝试候选，直到某个成功或全部失败。
	var lastErr error
	for _, cand := range candidates {
		// 带副作用的准入：未获准（熔断中/半开额度耗尽）则跳过，不记结果。
		if !cand.Breaker.Allow() {
			continue
		}

		attemptStart := time.Now()
		outcome := f.tryCandidate(c, cand, fc)
		// 上游指标埋点：outcomeChannelError 记为失败，其余（成功 / 上游拒绝请求本身）
		// 视为渠道健康。耗时覆盖整次尝试（含响应转换）。
		metrics.ObserveUpstream(
			cand.Channel.ID, string(cand.Channel.Format),
			outcome.kind != outcomeChannelError, time.Since(attemptStart).Seconds(),
		)

		switch outcome.kind {
		case outcomeSuccess:
			cand.Breaker.RecordSuccess()
			metrics.SetBreakerState(cand.Channel.ID, cand.Breaker.StateCode())
			settled = outcome.settled
			return
		case outcomeClientError:
			// 上游明确拒绝请求本身（非渠道故障）：渠道是健康的，不再故障转移。
			cand.Breaker.RecordSuccess()
			metrics.SetBreakerState(cand.Channel.ID, cand.Breaker.StateCode())
			settled = outcome.settled
			return
		case outcomeChannelError:
			// 渠道故障（网络错误 / 5xx / 429）：记失败并尝试下一个候选。
			cand.Breaker.RecordFailure()
			metrics.SetBreakerState(cand.Channel.ID, cand.Breaker.StateCode())
			lastErr = outcome.err
			// 流式一旦已向客户端写出数据则无法故障转移。
			if outcome.committed {
				settled = outcome.settled
				return
			}
		}
	}

	// 所有候选均失败（且未向客户端提交）。
	f.logger.Warn("全部候选渠道失败", "model", clientModel, "err", lastErr)
	writeError(c, http.StatusBadGateway, "upstream_error", "所有上游渠道均不可用")
}

// outcomeKind 标识一次候选尝试的结果类型。
type outcomeKind int

const (
	outcomeSuccess      outcomeKind = iota // 成功完成并已响应客户端
	outcomeClientError                     // 上游拒绝请求本身（4xx，非渠道故障）
	outcomeChannelError                    // 渠道故障，可尝试下一候选
)

// tryOutcome 是一次候选尝试的结果。
type tryOutcome struct {
	kind outcomeKind
	// settled 表示是否已完成计费结算（成功 Settle）。
	settled bool
	// committed 表示是否已向客户端写出数据（流式）——此后无法故障转移。
	committed bool
	err       error
}

// forwardCtx 聚合一次转发在所有候选间共享的不变量，避免逐候选透传大量参数。
// 构建后只读。
type forwardCtx struct {
	clientFormat  string              // 客户端线格式（"openai"/"anthropic"）
	clientAdapter adapter.Adapter     // 客户端格式适配器（入向解析 / 出向构造）
	rawBody       []byte              // 客户端原始请求字节（同格式直通时透传）
	req           *canonical.Request  // 解析后的规范请求
	clientModel   string              // 客户端请求的对外模型名
	res           billing.Reservation // 计费预扣句柄
	requestID     string              // 本次请求唯一 ID
}

// canPassthrough 判断某候选渠道是否可走同格式直通：
// 客户端格式 == 渠道格式，且该渠道对当前模型无重命名（上游模型名与对外名一致）。
// 满足时请求体可原样透传上游、响应可原样透传客户端，既省一次 canonical 往返
// 的编解码开销，又彻底避免超集模型未覆盖字段的丢失。
func (fc *forwardCtx) canPassthrough(ch *routing.Channel) bool {
	return fc.clientFormat == string(ch.Format) &&
		ch.UpstreamModel(fc.clientModel) == fc.clientModel
}

// tryCandidate 对单个候选渠道发起一次尝试（流式与非流式分派）。
func (f *Forwarder) tryCandidate(c *gin.Context, cand routing.Candidate, fc *forwardCtx) tryOutcome {
	if fc.req.Stream {
		return f.forwardStream(c, cand, fc)
	}
	return f.forwardNonStream(c, cand, fc)
}

// settle 按真实用量结算（退差 + 记用量日志）。用独立 context，
// 避免客户端断开导致 Settle 被取消而漏记账。
func (f *Forwarder) settle(res billing.Reservation, channelID, requestID string, usage canonical.Usage) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.billing.Settle(ctx, res, channelID, requestID, usage.InputTokens, usage.OutputTokens); err != nil {
		f.logger.Error("计费结算失败", "request_id", requestID, "err", err)
		return false
	}
	return true
}

// refund 全额退回押金（转发彻底失败、无计费用量时）。
func (f *Forwarder) refund(res billing.Reservation) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.billing.Refund(ctx, res); err != nil {
		f.logger.Error("退款失败", "user_id", res.UserID, "err", err)
	}
}

// isChannelError 判断上游状态码是否属于「渠道故障」（应故障转移）。
// 5xx / 429（限流）/ 408（超时）视为渠道问题；其余 4xx 视为请求本身的问题。
func isChannelError(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout
}

// newRequestID 生成一次请求的唯一 ID（用量日志幂等 / 对账）。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极罕见：退化为时间戳，仍保证基本可读性。
		return "req_" + time.Now().Format("20060102150405.000000")
	}
	return "req_" + hex.EncodeToString(b[:])
}

// writeError 以 OpenAI 风格错误结构响应（与中间件对外格式一致）。
func writeError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

// errUnexpected 兜底错误。
var errUnexpected = errors.New("forwarder: 意外错误")
