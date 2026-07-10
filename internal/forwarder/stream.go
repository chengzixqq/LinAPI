package forwarder

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
	"linapi/internal/middleware"
	"linapi/internal/routing"
)

// forwardStream 处理流式转发：读取上游 SSE，逐条经「渠道解码 → 客户端编码」转换后
// 实时写给客户端，同时累计 token 用量供结算。
//
// 同格式直通时（fc.canPassthrough）仍逐条解码以累计 usage，但把上游原始 SSE 记录
// 原样写回客户端，跳过客户端编码，既省编码开销又保真透传。
//
// 关键取舍：收到任何 2xx 就视为上游可能已经消费，之后即使尚未向客户端写出
// 也不再故障转移；committed 仅表示 HTTP 响应是否已经写出、还能否改写错误响应。
func (f *Forwarder) forwardStream(c *gin.Context, cand routing.Candidate, fc *forwardCtx) tryOutcome {
	ch := cand.Channel
	if err := c.Request.Context().Err(); err != nil {
		return tryOutcome{kind: outcomeNeutral, err: err}
	}

	channelAdapter, ok := adapter.Get(string(ch.Format))
	if !ok {
		f.logger.Error("渠道格式无对应适配器", "channel", ch.ID, "format", ch.Format)
		return tryOutcome{kind: outcomeChannelError, err: errUnexpected}
	}

	passthrough := fc.canPassthrough(ch)

	// 构造上游请求体：直通时原样透传（客户端本就是流式请求，stream 字段已为 true）。
	var body []byte
	var err error
	if passthrough {
		body = fc.rawBody
	} else if fc.clientFormat == string(ch.Format) {
		body, err = rewriteNormalizedRequestModel(fc.rawBody, ch.UpstreamModel(fc.clientModel))
		if err != nil {
			return tryOutcome{kind: outcomeChannelError, err: err}
		}
	} else {
		upstreamReq := *fc.req
		upstreamReq.Model = ch.UpstreamModel(fc.clientModel)
		upstreamReq.Stream = true

		built, err := channelAdapter.BuildRequest(&upstreamReq)
		if err != nil {
			f.logger.Error("构造上游流式请求失败", "channel", ch.ID, "err", err)
			return tryOutcome{kind: outcomeChannelError, err: err}
		}
		body = built
	}
	body, err = f.enforceCandidateOutputLimit(ch, fc, body)
	if err != nil {
		f.logger.Error("应用上游流式输出上限策略失败", "channel", ch.ID, "err", err)
		return tryOutcome{kind: outcomeChannelError, err: err}
	}
	if err := c.Request.Context().Err(); err != nil {
		return tryOutcome{kind: outcomeNeutral, err: err}
	}

	// 本地请求构造必须在 reservation 进入 in_flight 前完成；构造失败绝不会
	// 触碰上游，可安全继续其它候选。
	prepared, err := f.upstream.prepare(c.Request.Context(), ch, body, true)
	if err != nil {
		return tryOutcome{kind: outcomeChannelError, err: err}
	}
	if err := c.Request.Context().Err(); err != nil {
		return tryOutcome{kind: outcomeNeutral, err: err}
	}
	if err := f.markInFlight(fc.res, ch.ID); err != nil {
		if releaseErr := f.releaseAttempt(fc.res); releaseErr == nil {
			return tryOutcome{kind: outcomeChannelError, err: err}
		}
		return tryOutcome{kind: outcomeChannelError, consumed: true, err: err}
	}
	resp, err := f.upstream.send(prepared)
	if err != nil {
		if errors.Is(err, ErrUpstreamNotDialed) {
			if releaseErr := f.releaseAttempt(fc.res); releaseErr == nil {
				if ctxErr := c.Request.Context().Err(); ctxErr != nil {
					return tryOutcome{kind: outcomeNeutral, err: ctxErr}
				}
				return tryOutcome{kind: outcomeChannelError, err: err}
			}
		}
		if ctxErr := c.Request.Context().Err(); ctxErr != nil {
			return tryOutcome{kind: outcomeNeutral, consumed: true, err: ctxErr}
		}
		return tryOutcome{kind: outcomeChannelError, consumed: true, err: err}
	}
	defer resp.Stream.Close()

	// 非 2xx 状态尚无成功消费证据，可按既有状态码策略决定退款或故障转移。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Stream, 64*1024))
		if !definitelyUnconsumedStatus(resp.StatusCode) {
			relayUpstreamError(c, resp.StatusCode, errBody, resp.Header, channelAdapter, fc.clientFormat)
			return tryOutcome{kind: outcomeChannelError, consumed: true, committed: true, err: errUnexpected}
		}
		if err := f.releaseAttempt(fc.res); err != nil {
			return tryOutcome{kind: outcomeChannelError, consumed: true, err: err}
		}
		if isChannelError(resp.StatusCode) {
			f.logger.Warn("上游流式渠道错误", "channel", ch.ID, "status", resp.StatusCode)
			return tryOutcome{
				kind: outcomeChannelError,
				err:  errUnexpected,
				upstreamError: &upstreamHTTPError{
					body: errBody, header: resp.Header, adapter: channelAdapter,
				},
			}
		}
		relayUpstreamError(c, resp.StatusCode, errBody, resp.Header, channelAdapter, fc.clientFormat)
		return tryOutcome{kind: outcomeClientError}
	}
	copySafeUpstreamHeaders(c.Writer.Header(), resp.Header)

	// 收到 2xx 即表示上游可能已经产生消费。此后的所有退出路径都必须结算，
	// 即使流被截断或 usage 不完整，也由 Billing 按预授权上限保守收费。
	var usage canonical.Usage
	committed := false
	finishAttempt := func(kind outcomeKind, attemptErr error, exact bool) tryOutcome {
		middleware.SetLogUpstream(c, ch.ID)
		middleware.SetLogUsage(c, usage.InputTokens, usage.OutputTokens)
		settlementUsage := usage
		if !exact {
			// 已观测数值仍写入消费记录，但不再把截断/缺终态的 usage 声明为
			// 可精确采用，迫使 Billing 按预授权上限保守结算。
			settlementUsage.InputTokensKnown = false
			settlementUsage.OutputTokensKnown = false
			settlementUsage.TotalTokensKnown = false
		}
		settled := f.settle(fc.res, ch.ID, settlementUsage)
		if kind == outcomeChannelError && !committed {
			writeError(c, http.StatusBadGateway, "upstream_error", "上游流未完整结束，已按预授权上限计费")
			committed = true
		}
		return tryOutcome{
			kind:      kind,
			settled:   settled,
			consumed:  true,
			committed: committed,
			err:       attemptErr,
		}
	}

	// 拿到 flusher（仅类型断言，不提交响应）。SSE 响应头推迟到首个输出前才写，
	// 保证 committed 与真实响应提交点一致，便于外层判断是否还能返回结构化错误。
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		f.logger.Error("ResponseWriter 不支持 Flush，无法流式响应")
		writeError(c, http.StatusInternalServerError, "internal_error", "服务器不支持流式响应")
		return finishAttempt(outcomeNeutral, nil, false)
	}

	decoder := channelAdapter.NewStreamDecoder()
	// 直通不需要编码器：原样透传上游记录。
	var encoder adapter.StreamEncoder
	if !passthrough {
		encoder = fc.clientAdapter.NewStreamEncoder()
	}
	reader := newSSEReader(resp.Stream)

	protocolCompleted := false
	finalUsageSeen := false
	finalUsageInvalidated := false
	var finalUsage canonical.Usage

	for {
		record, err := reader.Next()
		if err != nil {
			if ctxErr := c.Request.Context().Err(); ctxErr != nil {
				return finishAttempt(outcomeNeutral, ctxErr, false)
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("上游流在协议结束事件前 EOF")
				f.logger.Warn("上游流提前结束", "channel", ch.ID, "err", err)
				return finishAttempt(outcomeChannelError, err, false)
			}
			// 2xx 后的读取失败代表消费状态已确定为“不可退款”；结算使用当前
			// usage，缺失部分由 Billing 按预授权上限保守处理。
			f.logger.Warn("读取上游流失败", "channel", ch.ID, "err", err)
			return finishAttempt(outcomeChannelError, err, false)
		}

		events, err := decoder.Decode(record)
		if err != nil {
			if ctxErr := c.Request.Context().Err(); ctxErr != nil {
				return finishAttempt(outcomeNeutral, ctxErr, false)
			}
			f.logger.Warn("解码上游流事件失败", "channel", ch.ID, "err", err)
			return finishAttempt(outcomeChannelError, err, false)
		}

		// 无论是否直通，都累计用量并跟踪协议终态。EOF 本身不算完成：只有
		// OpenAI [DONE] / Anthropic message_stop 解码出的 message_stop 才算。
		var eventErr error
		if finalUsageSeen && len(events) == 0 {
			// 最终 usage 后出现无法识别的记录，无法证明该 usage 仍是终态。
			finalUsageInvalidated = true
		}
		for _, ev := range events {
			if finalUsageSeen && !allowedAfterFinalUsage(ev) {
				finalUsageInvalidated = true
			}
			accumulateUsage(&usage, ev)
			if ev.UsageFinal {
				finalUsageSeen = true
				accumulateUsage(&finalUsage, ev)
			}
			switch ev.Type {
			case canonical.EventMessageStop:
				protocolCompleted = true
			case canonical.EventError:
				if ev.Err == "" {
					eventErr = errors.New("上游流返回 error 事件")
				} else {
					eventErr = fmt.Errorf("上游流返回 error 事件: %s", ev.Err)
				}
			}
		}

		// 直通：原样透传上游 SSE 记录（补回记录边界空行），跳过编码。
		if passthrough {
			if !committed {
				setSSEHeaders(c)
				committed = true
			}
			setSSEWriteDeadline(c.Writer)
			if werr := writeSSERecord(c.Writer, record); werr != nil {
				f.logger.Debug("向客户端写流失败（客户端可能已断开）", "err", werr)
				if eventErr != nil {
					return finishAttempt(outcomeChannelError, eventErr, false)
				}
				return finishAttempt(outcomeNeutral, werr, false)
			}
			flusher.Flush()
			if eventErr != nil {
				f.logger.Warn("上游流内错误", "channel", ch.ID, "err", eventErr)
				return finishAttempt(outcomeChannelError, eventErr, false)
			}
			if protocolCompleted {
				break
			}
			continue
		}

		for _, ev := range events {
			out, err := encoder.Encode(ev)
			if err != nil {
				f.logger.Warn("编码客户端流事件失败", "err", err)
				continue
			}
			if len(out) == 0 {
				continue // 该事件在目标格式下无需输出。
			}
			// 首个输出前才写 SSE 头，提交响应。此后 committed=true，不再故障转移。
			if !committed {
				setSSEHeaders(c)
				committed = true
			}
			setSSEWriteDeadline(c.Writer)
			if _, werr := c.Writer.Write(out); werr != nil {
				// 客户端断开：已消耗上游用量，仍需结算。
				f.logger.Debug("向客户端写流失败（客户端可能已断开）", "err", werr)
				if eventErr != nil {
					return finishAttempt(outcomeChannelError, eventErr, false)
				}
				return finishAttempt(outcomeNeutral, werr, false)
			}
			flusher.Flush()
		}

		if eventErr != nil {
			f.logger.Warn("上游流内错误", "channel", ch.ID, "err", eventErr)
			return finishAttempt(outcomeChannelError, eventErr, false)
		}
		if protocolCompleted {
			break
		}
	}

	usage = usageWithFinalAuthority(usage, finalUsage)
	if !finalUsageSeen || finalUsageInvalidated || !usageReadyForExactSettlement(usage) {
		err := errors.New("上游流缺少完整的最终 usage")
		f.logger.Warn("上游流无法精确结算，改用预授权上限", "channel", ch.ID,
			"protocol_completed", protocolCompleted, "final_usage_seen", finalUsageSeen,
			"final_usage_invalidated", finalUsageInvalidated, "usage", usage, "err", err)
		return finishAttempt(outcomeChannelError, err, false)
	}

	return finishAttempt(outcomeSuccess, nil, true)
}

// allowedAfterFinalUsage 限定供应商声明最终用量后的合法尾声。允许重复的最终
// usage（部分兼容实现会先随 finish_reason、再发 choices=[] 尾块）、心跳和协议
// message_stop；任何新的内容或非最终 usage 都会使精确结算失效。
func allowedAfterFinalUsage(ev canonical.Event) bool {
	switch ev.Type {
	case canonical.EventPing, canonical.EventMessageStop:
		return true
	case canonical.EventMessageDelta:
		return ev.UsageFinal && ev.Usage != nil
	default:
		return false
	}
}

// usageWithFinalAuthority 防止 message_start 的临时/零值字段补齐一个残缺的最终
// usage。最终事件明确给出的字段覆盖累计值；最终未给 output 时清除早期 output，
// 只有 final total + 已知 input 能重新可靠推导。
func usageWithFinalAuthority(aggregate, final canonical.Usage) canonical.Usage {
	if final.InputTokensKnown {
		aggregate.InputTokens = final.InputTokens
		aggregate.InputTokensKnown = true
	}
	if final.OutputTokensKnown {
		aggregate.OutputTokens = final.OutputTokens
		aggregate.OutputTokensKnown = true
	} else {
		aggregate.OutputTokens = 0
		aggregate.OutputTokensKnown = false
	}
	if final.TotalTokensKnown {
		aggregate.ReportedTotalTokens = final.ReportedTotalTokens
		aggregate.TotalTokensKnown = true
	} else {
		aggregate.ReportedTotalTokens = 0
		aggregate.TotalTokensKnown = false
	}
	return aggregate
}

// writeSSERecord 把一条 SSE 记录原样写回客户端，并补回记录边界（空行）。
// sseReader 剥掉了记录间的分隔空行，透传时需补回 "\n\n" 以维持合法 SSE 分帧。
func writeSSERecord(w io.Writer, record []byte) error {
	if _, err := w.Write(record); err != nil {
		return err
	}
	_, err := w.Write([]byte("\n\n"))
	return err
}

// accumulateUsage 从规范事件中提取并更新用量。
// message_start 常带 input tokens，message_delta（结束）带最终 output tokens；
// 取「见过的最大值」以兼容各家在不同事件里下发累计/最终用量的差异。
func accumulateUsage(u *canonical.Usage, ev canonical.Event) {
	if ev.Usage == nil {
		return
	}
	if ev.Usage.InputTokensKnown {
		if !u.InputTokensKnown || ev.Usage.InputTokens > u.InputTokens {
			u.InputTokens = ev.Usage.InputTokens
		}
		u.InputTokensKnown = true
	}
	if ev.Usage.OutputTokensKnown {
		if !u.OutputTokensKnown || ev.Usage.OutputTokens > u.OutputTokens {
			u.OutputTokens = ev.Usage.OutputTokens
		}
		u.OutputTokensKnown = true
	}
	if ev.Usage.TotalTokensKnown {
		if !u.TotalTokensKnown || ev.Usage.ReportedTotalTokens > u.ReportedTotalTokens {
			u.ReportedTotalTokens = ev.Usage.ReportedTotalTokens
		}
		u.TotalTokensKnown = true
	}
	if ev.Usage.CacheCreationInputTokens < 0 {
		u.CacheCreationInputTokens = -1
	} else if u.CacheCreationInputTokens >= 0 && ev.Usage.CacheCreationInputTokens > u.CacheCreationInputTokens {
		u.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
	}
	if ev.Usage.CacheReadInputTokens < 0 {
		u.CacheReadInputTokens = -1
	} else if u.CacheReadInputTokens >= 0 && ev.Usage.CacheReadInputTokens > u.CacheReadInputTokens {
		u.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
	}
}

// usageReadyForExactSettlement 判断当前 usage 能否直接使用，或能否由 total 与
// 单边 token 安全推导另一边。Billing 会再次执行同样的资金边界校验；这里用于
// 区分健康的完整流与需要保守结算的供应商协议异常。
func usageReadyForExactSettlement(u canonical.Usage) bool {
	input, output := u.InputTokens, u.OutputTokens
	inputKnown, outputKnown := u.InputTokensKnown, u.OutputTokensKnown
	cacheTotal, cacheOK := checkedStreamTokenSum(u.CacheCreationInputTokens, u.CacheReadInputTokens)
	if !cacheOK {
		return false
	}

	if u.TotalTokensKnown {
		total := u.ReportedTotalTokens
		if total < 0 {
			return false
		}
		switch {
		case inputKnown && outputKnown:
			calculated, ok := checkedStreamTokenSum(input, cacheTotal, output)
			if !ok || calculated != total {
				return false
			}
		case inputKnown:
			known, ok := checkedStreamTokenSum(input, cacheTotal)
			if !ok || total < known {
				return false
			}
			output, outputKnown = total-known, true
		case outputKnown:
			known, ok := checkedStreamTokenSum(output, cacheTotal)
			if !ok || total < known {
				return false
			}
			input, inputKnown = total-known, true
		default:
			return false
		}
	}

	if !inputKnown || !outputKnown || input < 0 || output < 0 {
		return false
	}
	_, ok := checkedStreamTokenSum(input, cacheTotal, output)
	return ok
}

func checkedStreamTokenSum(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || total > math.MaxInt-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

// setSSEHeaders 设置流式响应所需的 HTTP 头。
func setSSEHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)
}

const downstreamStreamWriteTimeout = 30 * time.Second

// setSSEWriteDeadline 为每次事件写入刷新独立期限。它不会像 http.Server.WriteTimeout
// 那样限制整条长流，但客户端持续不读时会在单次写阻塞 30 秒后释放资源。
func setSSEWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downstreamStreamWriteTimeout))
}
