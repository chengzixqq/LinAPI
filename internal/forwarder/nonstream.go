package forwarder

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
	"linapi/internal/middleware"
	"linapi/internal/routing"
)

// forwardNonStream 处理非流式转发：BuildRequest → 上游 → ParseResponse → 结算 → BuildResponse。
// 同格式直通时（fc.canPassthrough）跳过请求/响应的 canonical 构造，透传原始字节。
func (f *Forwarder) forwardNonStream(c *gin.Context, cand routing.Candidate, fc *forwardCtx) tryOutcome {
	ch := cand.Channel
	if err := c.Request.Context().Err(); err != nil {
		return tryOutcome{kind: outcomeNeutral, err: err}
	}

	channelAdapter, ok := adapter.Get(string(ch.Format))
	if !ok {
		// 渠道配置了未知格式：视为渠道故障，尝试下一个。
		f.logger.Error("渠道格式无对应适配器", "channel", ch.ID, "format", ch.Format)
		return tryOutcome{kind: outcomeChannelError, err: errUnexpected}
	}

	passthrough := fc.canPassthrough(ch)

	// 构造上游请求体：直通时原样透传客户端字节；否则按渠道格式 BuildRequest。
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
		// 按渠道映射改写上游模型名。复制请求避免污染其它候选的重试。
		upstreamReq := *fc.req
		upstreamReq.Model = ch.UpstreamModel(fc.clientModel)
		upstreamReq.Stream = false

		built, err := channelAdapter.BuildRequest(&upstreamReq)
		if err != nil {
			f.logger.Error("构造上游请求失败", "channel", ch.ID, "err", err)
			return tryOutcome{kind: outcomeChannelError, err: err}
		}
		body = built
	}
	body, err = f.enforceCandidateOutputLimit(ch, fc, body)
	if err != nil {
		f.logger.Error("应用上游输出上限策略失败", "channel", ch.ID, "err", err)
		return tryOutcome{kind: outcomeChannelError, err: err}
	}
	if err := c.Request.Context().Err(); err != nil {
		return tryOutcome{kind: outcomeNeutral, err: err}
	}

	// 先完成全部本地 HTTP 构造校验；这里失败时还没有任何上游 I/O，可安全
	// 尝试其它渠道，最终由外层退款。
	prepared, err := f.upstream.prepare(c.Request.Context(), ch, body, false)
	if err != nil {
		return tryOutcome{kind: outcomeChannelError, err: err}
	}
	if err := c.Request.Context().Err(); err != nil {
		return tryOutcome{kind: outcomeNeutral, err: err}
	}

	// 在真实网络发送前持久标记 in_flight。该提交结果若不确定，本次必须停止，
	// 不能退款或换渠道，否则可能对已送达请求重复消费。
	if err := f.markInFlight(fc.res, ch.ID); err != nil {
		// 尚未执行 send，因此即使 MarkInFlight 的提交结果未知，也可用幂等
		// ReleaseAttempt 把 reserved/in_flight 收敛回可退款状态。
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
			// 客户端取消导致的发送中断不代表渠道故障；请求可能已送达，
			// 因此仍保留 in_flight，等待后续对账。
			return tryOutcome{kind: outcomeNeutral, consumed: true, err: ctxErr}
		}
		// 请求是否已送达无法证明，保留 in_flight 并停止跨渠道重放。
		return tryOutcome{kind: outcomeChannelError, consumed: true, err: err}
	}

	// 上游返回错误状态。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if !definitelyUnconsumedStatus(resp.StatusCode) {
			relayUpstreamError(c, resp.StatusCode, resp.Body, resp.Header, channelAdapter, fc.clientFormat)
			return tryOutcome{kind: outcomeChannelError, consumed: true, committed: true, err: errUnexpected}
		}
		if err := f.releaseAttempt(fc.res); err != nil {
			return tryOutcome{kind: outcomeChannelError, consumed: true, err: err}
		}
		if isChannelError(resp.StatusCode) {
			f.logger.Warn("上游渠道错误", "channel", ch.ID, "status", resp.StatusCode)
			return tryOutcome{
				kind: outcomeChannelError,
				err:  errUnexpected,
				upstreamError: &upstreamHTTPError{
					body: resp.Body, header: resp.Header, adapter: channelAdapter,
				},
			}
		}
		// 4xx（非渠道故障）：请求本身的问题，渠道健康。透传上游错误体，不再重试。
		// 未产生用量，settled=false 由上层 defer 退回押金。
		relayUpstreamError(c, resp.StatusCode, resp.Body, resp.Header, channelAdapter, fc.clientFormat)
		return tryOutcome{kind: outcomeClientError}
	}
	copySafeUpstreamHeaders(c.Writer.Header(), resp.Header)

	// 成功：解析为规范响应（即便直通也需解析以提取 usage 计费）。
	canonResp, err := channelAdapter.ParseResponse(resp.Body)
	if err != nil {
		f.logger.Error("解析上游响应失败", "channel", ch.ID, "err", err)
		// 2xx 已证明请求到达并由上游成功处理，不能故障转移或退款。usage 无法
		// 解析时按 reservation 上限保守结算，并向客户端返回网关错误。
		settled := f.settle(fc.res, ch.ID, canonical.Usage{})
		writeError(c, http.StatusBadGateway, "upstream_error", "上游响应无法安全计费")
		return tryOutcome{kind: outcomeSuccess, consumed: true, settled: settled, committed: true}
	}

	// 先结算：token 已实际消耗，即便后续构造客户端响应失败也应记账。
	settled := f.settle(fc.res, ch.ID, canonResp.Usage)

	// 回填渠道与用量到访问日志。
	middleware.SetLogUpstream(c, ch.ID)
	middleware.SetLogUsage(c, canonResp.Usage.InputTokens, canonResp.Usage.OutputTokens)

	// 直通：响应与客户端同格式，原样透传上游字节，跳过反向转换。
	if passthrough {
		c.Data(http.StatusOK, contentTypeJSON, resp.Body)
		return tryOutcome{kind: outcomeSuccess, consumed: true, settled: settled, committed: true}
	}

	// 反向转换回客户端格式；模型名回填为客户端请求的对外名。
	canonResp.Model = fc.clientModel
	out, err := fc.clientAdapter.BuildResponse(canonResp)
	if err != nil {
		f.logger.Error("构造客户端响应失败", "err", err)
		writeError(c, http.StatusInternalServerError, "internal_error", "构造响应失败")
		// 已成功调用上游并结算，视为成功（不再故障转移）。
		return tryOutcome{kind: outcomeSuccess, consumed: true, settled: settled, committed: true}
	}

	c.Data(http.StatusOK, contentTypeJSON, out)
	return tryOutcome{kind: outcomeSuccess, consumed: true, settled: settled, committed: true}
}

const contentTypeJSON = "application/json; charset=utf-8"
