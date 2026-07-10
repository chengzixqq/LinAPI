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
	router       *routing.Router
	billing      *billing.Billing
	outputLimits *OpenAIOutputLimitResolver
	targetPolicy *UpstreamTargetPolicy
	upstream     *upstreamClient
	logger       *slog.Logger
}

// New 构建转发器。
func New(router *routing.Router, bill *billing.Billing, logger *slog.Logger) *Forwarder {
	resolver, err := NewOpenAIOutputLimitResolver(nil, false)
	if err != nil {
		panic(err)
	}
	return NewWithPolicies(router, bill, resolver, newDevelopmentTargetPolicy(), logger)
}

// NewWithOutputLimitResolver 构建带上游 OpenAI 输出上限字段策略的转发器。
func NewWithOutputLimitResolver(router *routing.Router, bill *billing.Billing, resolver *OpenAIOutputLimitResolver, logger *slog.Logger) *Forwarder {
	return NewWithPolicies(router, bill, resolver, newDevelopmentTargetPolicy(), logger)
}

// NewWithPolicies 构建同时带输出上限字段与上游网络目标策略的转发器。
func NewWithPolicies(router *routing.Router, bill *billing.Billing, resolver *OpenAIOutputLimitResolver, targetPolicy *UpstreamTargetPolicy, logger *slog.Logger) *Forwarder {
	if logger == nil {
		logger = slog.Default()
	}
	if resolver == nil {
		resolver, _ = NewOpenAIOutputLimitResolver(nil, false)
	}
	if targetPolicy == nil {
		targetPolicy = newDevelopmentTargetPolicy()
	}
	return &Forwarder{
		router:       router,
		billing:      bill,
		outputLimits: resolver,
		targetPolicy: targetPolicy,
		upstream:     newUpstreamClientWithPolicy(targetPolicy),
		logger:       logger,
	}
}

// Handler 返回一个 gin.HandlerFunc，clientFormat 指定客户端所用的线格式
// （"openai" / "anthropic"），决定入向 ParseRequest 与出向 BuildResponse 用哪个适配器。
func (f *Forwarder) Handler(clientFormat string) gin.HandlerFunc {
	return func(c *gin.Context) {
		middleware.SetProtocol(c, clientFormat)
		f.forward(c, clientFormat)
	}
}

// forward 执行一次完整转发。请求解析并确定模型计费边界后，先持久预授权，
// 再发上游；只有明确未产生上游消费的路径才允许退款。
func (f *Forwarder) forward(c *gin.Context, clientFormat string) {
	clientAdapter, ok := adapter.Get(clientFormat)
	if !ok {
		writeError(c, http.StatusInternalServerError, "internal_error", "客户端格式适配器未注册")
		return
	}

	// 读取并解析客户端请求。
	body, err := c.GetRawData()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "请求体过大")
			return
		}
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
	id, ok := middleware.IdentityFrom(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "authentication_error", "未鉴权")
		return
	}
	if !id.Allows(req.Model) {
		writeError(c, http.StatusForbidden, "permission_error", "当前密钥无权访问模型 "+req.Model)
		return
	}

	// 服务端强制输出上限。缺失时注入模型上限，超限/非正数请求在打上游前拒绝。
	maxOutput, err := f.billing.NormalizeMaxOutput(req.Model, req.MaxTokens)
	if err != nil {
		if errors.Is(err, billing.ErrInvalidTokenLimit) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "max_tokens 超出服务端模型上限")
		} else {
			writeError(c, http.StatusInternalServerError, "internal_error", "读取计费策略失败")
		}
		return
	}
	req.MaxTokens = &maxOutput

	// 同格式直通也必须把服务端上限（以及 OpenAI stream usage 开关）写进真实上游
	// 请求；最小 JSON 合并保留未建模字段。
	normalizedBody, err := normalizePassthroughRequest(body, clientFormat, maxOutput, req.Stream)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "规范化请求失败: "+err.Error())
		return
	}

	// 回填对外模型名到访问日志。
	middleware.SetLogModel(c, req.Model)

	// 路由选出候选渠道（已按优先级/权重排序、过滤熔断）。
	candidates, err := f.router.Select(req.Model)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "no_channel_error",
			"没有可服务该模型的可用渠道")
		return
	}

	// 复用访问日志中间件注入的 request_id 作为 trace_id；资金与 usage_logs 使用
	// 独立的服务端 reservation ID，通过 billing_reservations 关联，避免外部 ID 冲突。
	// 未挂中间件时（如单测）兜底自生成 trace_id。
	requestID, ok := middleware.RequestIDFrom(c)
	if !ok {
		requestID = newRequestID()
	}
	clientModel := req.Model

	// 按最大可计费输入和强制输出上限冻结最坏成本。余额不足时在任何上游 I/O
	// 发生之前拒绝，阻止并发请求以固定小押金超卖。
	res, reserved, err := f.billing.Reserve(c.Request.Context(), billing.ReserveRequest{
		TraceID: requestID, UserID: id.UserID, KeyID: id.KeyID, Model: req.Model,
		MaxOutputTokens: maxOutput,
	})
	if err != nil {
		f.logger.Error("计费预授权失败", "request_id", requestID, "err", err)
		writeError(c, http.StatusInternalServerError, "internal_error", "计费服务暂时不可用")
		return
	}
	if !reserved {
		writeError(c, http.StatusPaymentRequired, "insufficient_quota", "额度不足，请充值后重试")
		return
	}

	// refundable 只表示“尚无上游消费证据”。收到任何 2xx 后 consumed=true；
	// 此后即便 usage 缺失或本地结算失败也绝不退款。
	consumed := false
	settled := false
	defer func() {
		if !consumed && !settled {
			f.refund(res)
		}
	}()

	fc := &forwardCtx{
		clientFormat:  clientFormat,
		clientAdapter: clientAdapter,
		rawBody:       normalizedBody,
		req:           req,
		clientModel:   clientModel,
		res:           res,
		requestID:     requestID,
	}

	// 依次尝试候选，直到某个成功或全部失败。
	var lastErr error
	var lastUpstreamError *upstreamHTTPError
	for _, cand := range candidates {
		// 客户端已取消时立即停止，既不占用熔断探测名额，也不尝试后备渠道。
		if c.Request.Context().Err() != nil {
			return
		}

		// 带副作用的准入：未获准（熔断中/半开额度耗尽）则跳过，不记结果。
		permit, allowed := cand.Breaker.Allow()
		if !allowed {
			continue
		}

		attemptStart := time.Now()
		outcome := f.tryCandidate(c, cand, fc)

		switch outcome.kind {
		case outcomeSuccess:
			metrics.ObserveUpstream(
				cand.Channel.ID, string(cand.Channel.Format), true, time.Since(attemptStart).Seconds(),
			)
			permit.RecordSuccess()
			metrics.SetBreakerState(cand.Channel.ID, cand.Breaker.StateCode())
			consumed = outcome.consumed
			settled = outcome.settled
			return
		case outcomeClientError:
			// 上游明确拒绝请求本身（非渠道故障）：渠道是健康的，不再故障转移。
			metrics.ObserveUpstream(
				cand.Channel.ID, string(cand.Channel.Format), true, time.Since(attemptStart).Seconds(),
			)
			permit.RecordSuccess()
			metrics.SetBreakerState(cand.Channel.ID, cand.Breaker.StateCode())
			consumed = outcome.consumed
			settled = outcome.settled
			return
		case outcomeChannelError:
			// 渠道故障（网络错误 / 5xx / 429）：记失败并尝试下一个候选。
			metrics.ObserveUpstream(
				cand.Channel.ID, string(cand.Channel.Format), false, time.Since(attemptStart).Seconds(),
			)
			permit.RecordFailure()
			metrics.SetBreakerState(cand.Channel.ID, cand.Breaker.StateCode())
			lastErr = outcome.err
			if outcome.upstreamError != nil {
				lastUpstreamError = outcome.upstreamError
			}
			// 流式一旦已向客户端写出数据则无法故障转移。
			if outcome.committed || outcome.consumed {
				if outcome.consumed && !outcome.committed {
					writeError(c, http.StatusBadGateway, "upstream_error",
						"上游请求结果不确定，预授权已保留等待对账")
				}
				consumed = outcome.consumed
				settled = outcome.settled
				return
			}
		case outcomeNeutral:
			// 客户端取消或下游写失败不代表渠道健康度；释放许可后立即停止，
			// 且不记录上游成功/失败指标。
			permit.RecordNeutral()
			consumed = outcome.consumed
			settled = outcome.settled
			return
		}
	}

	// 所有候选均失败（且未向客户端提交）。
	f.logger.Warn("全部候选渠道失败", "model", clientModel, "err", lastErr)
	if lastUpstreamError != nil {
		relayUpstreamError(c, http.StatusBadGateway, lastUpstreamError.body,
			lastUpstreamError.header, lastUpstreamError.adapter, clientFormat)
		return
	}
	writeError(c, http.StatusBadGateway, "upstream_error", "所有上游渠道均不可用")
}

// outcomeKind 标识一次候选尝试的结果类型。
type outcomeKind int

const (
	outcomeSuccess      outcomeKind = iota // 成功完成并已响应客户端
	outcomeClientError                     // 上游拒绝请求本身（4xx，非渠道故障）
	outcomeChannelError                    // 渠道故障，可尝试下一候选
	outcomeNeutral                         // 客户端取消/下游中断，不影响渠道健康度
)

// tryOutcome 是一次候选尝试的结果。
type tryOutcome struct {
	kind outcomeKind
	// settled 表示是否已完成计费结算（成功 Settle）。
	settled bool
	// consumed 表示上游已返回成功响应，资金不再允许自动退款；它与本地结算
	// 是否成功是两个独立事实（审查 AUD-P0-03）。
	consumed bool
	// committed 表示是否已向客户端写出数据（流式）——此后无法故障转移。
	committed bool
	err       error
	// upstreamError 暂存可安全重试的渠道 HTTP 错误；若所有候选均失败，外层
	// 用最后一份错误体和安全响应头构造客户端协议错误，而不是静默丢弃。
	upstreamError *upstreamHTTPError
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
	requestID     string              // 本次请求追踪 ID
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
func (f *Forwarder) settle(res billing.Reservation, channelID string, usage canonical.Usage) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.billing.Settle(ctx, res, channelID, usage); err != nil {
		f.logger.Error("计费结算失败", "reservation_id", res.ID, "trace_id", res.TraceID, "err", err)
		return false
	}
	return true
}

func (f *Forwarder) markInFlight(res billing.Reservation, channel string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.billing.MarkInFlight(ctx, res, channel)
}

func (f *Forwarder) releaseAttempt(res billing.Reservation) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.billing.ReleaseAttempt(ctx, res)
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
	return status >= 300 && status < 400 || status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout ||
		status == http.StatusUnauthorized || status == http.StatusForbidden
}

// definitelyUnconsumedStatus 仅包含可证明上游未执行模型生成的明确 4xx 拒绝。
// 3xx 可能是 POST 已处理后的 Post/Redirect/Get，408/5xx 也可能在请求送达或
// 生成后产生；三者都必须保留 in_flight，不能退款或重放。
func definitelyUnconsumedStatus(status int) bool {
	switch status {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity,
		http.StatusTooManyRequests:
		return true
	default:
		// 408、499 以及供应商/代理自定义 4xx 不能证明 POST 未被处理。
		return false
	}
}

// newRequestID 生成一次请求的链路 ID；账单幂等另用内部 reservation ID。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极罕见：退化为时间戳，仍保证基本可读性。
		return "req_" + time.Now().Format("20060102150405.000000")
	}
	return "req_" + hex.EncodeToString(b[:])
}

// writeError 按当前客户端协议编码错误；Handler 会在进入转发逻辑前记录格式。
func writeError(c *gin.Context, status int, errType, message string) {
	middleware.WriteError(c, status, errType, message)
}

// errUnexpected 兜底错误。
var errUnexpected = errors.New("forwarder: 意外错误")
