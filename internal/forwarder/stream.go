package forwarder

import (
	"errors"
	"io"
	"net/http"

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
// 关键取舍：一旦已向客户端写出任何字节（committed），就无法再故障转移——
// HTTP 响应头与部分响应体已提交。因此上游连接建立、拿到成功状态码之前的失败
// 可以换渠道；之后的失败只能中断该次响应。
func (f *Forwarder) forwardStream(c *gin.Context, cand routing.Candidate, fc *forwardCtx) tryOutcome {
	ch := cand.Channel
	channelAdapter, ok := adapter.Get(string(ch.Format))
	if !ok {
		f.logger.Error("渠道格式无对应适配器", "channel", ch.ID, "format", ch.Format)
		return tryOutcome{kind: outcomeChannelError, err: errUnexpected}
	}

	passthrough := fc.canPassthrough(ch)

	// 构造上游请求体：直通时原样透传（客户端本就是流式请求，stream 字段已为 true）。
	var body []byte
	if passthrough {
		body = fc.rawBody
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

	resp, err := f.upstream.do(c.Request.Context(), ch, body, true)
	if err != nil {
		return tryOutcome{kind: outcomeChannelError, err: err}
	}
	defer resp.Stream.Close()

	// 上游错误状态：此刻尚未向客户端写出，可故障转移。
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Stream, 64*1024))
		if isChannelError(resp.StatusCode) {
			f.logger.Warn("上游流式渠道错误", "channel", ch.ID, "status", resp.StatusCode)
			return tryOutcome{kind: outcomeChannelError, err: errUnexpected}
		}
		relayUpstreamError(c, resp.StatusCode, errBody)
		return tryOutcome{kind: outcomeClientError}
	}

	// 拿到 flusher（仅类型断言，不提交响应）。SSE 响应头推迟到首个输出前才写，
	// 从而保证「响应提交点」与 committed 标志一致：首块之前的失败仍可故障转移。
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		f.logger.Error("ResponseWriter 不支持 Flush，无法流式响应")
		writeError(c, http.StatusInternalServerError, "internal_error", "服务器不支持流式响应")
		return tryOutcome{kind: outcomeSuccess, committed: false}
	}

	decoder := channelAdapter.NewStreamDecoder()
	// 直通不需要编码器：原样透传上游记录。
	var encoder adapter.StreamEncoder
	if !passthrough {
		encoder = fc.clientAdapter.NewStreamEncoder()
	}
	reader := newSSEReader(resp.Stream)

	var usage canonical.Usage
	committed := false

	for {
		record, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// 流中途断开。若已提交响应则无法故障转移，只能结束。
			f.logger.Warn("读取上游流失败", "channel", ch.ID, "err", err)
			if committed {
				break
			}
			return tryOutcome{kind: outcomeChannelError, err: err}
		}

		events, err := decoder.Decode(record)
		if err != nil {
			f.logger.Warn("解码上游流事件失败", "channel", ch.ID, "err", err)
			if committed {
				break
			}
			return tryOutcome{kind: outcomeChannelError, err: err}
		}

		// 无论是否直通，都遍历事件累计用量（计费所需）。
		for _, ev := range events {
			accumulateUsage(&usage, ev)
		}

		// 直通：原样透传上游 SSE 记录（补回记录边界空行），跳过编码。
		if passthrough {
			if !committed {
				setSSEHeaders(c)
				committed = true
			}
			if werr := writeSSERecord(c.Writer, record); werr != nil {
				f.logger.Debug("向客户端写流失败（客户端可能已断开）", "err", werr)
				middleware.SetLogUpstream(c, ch.ID)
				middleware.SetLogUsage(c, usage.InputTokens, usage.OutputTokens)
				settled := f.settle(fc.res, ch.ID, fc.requestID, usage)
				return tryOutcome{kind: outcomeSuccess, settled: settled, committed: true}
			}
			flusher.Flush()
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
			if _, werr := c.Writer.Write(out); werr != nil {
				// 客户端断开：已消耗上游用量，仍需结算。
				f.logger.Debug("向客户端写流失败（客户端可能已断开）", "err", werr)
				middleware.SetLogUpstream(c, ch.ID)
				middleware.SetLogUsage(c, usage.InputTokens, usage.OutputTokens)
				settled := f.settle(fc.res, ch.ID, fc.requestID, usage)
				return tryOutcome{kind: outcomeSuccess, settled: settled, committed: true}
			}
			flusher.Flush()
		}
	}

	// 流正常结束：按累计用量结算。
	middleware.SetLogUpstream(c, ch.ID)
	middleware.SetLogUsage(c, usage.InputTokens, usage.OutputTokens)
	settled := f.settle(fc.res, ch.ID, fc.requestID, usage)
	return tryOutcome{kind: outcomeSuccess, settled: settled, committed: committed}
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
	if ev.Usage.InputTokens > u.InputTokens {
		u.InputTokens = ev.Usage.InputTokens
	}
	if ev.Usage.OutputTokens > u.OutputTokens {
		u.OutputTokens = ev.Usage.OutputTokens
	}
	if ev.Usage.CacheCreationInputTokens > u.CacheCreationInputTokens {
		u.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
	}
	if ev.Usage.CacheReadInputTokens > u.CacheReadInputTokens {
		u.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
	}
}

// setSSEHeaders 设置流式响应所需的 HTTP 头。
func setSSEHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)
}
