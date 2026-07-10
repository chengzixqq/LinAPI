package forwarder

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/adapter"
	"linapi/internal/middleware"
	"linapi/internal/routing"
)

// forwardNonStream 处理非流式转发：BuildRequest → 上游 → ParseResponse → 结算 → BuildResponse。
// 同格式直通时（fc.canPassthrough）跳过请求/响应的 canonical 构造，透传原始字节。
func (f *Forwarder) forwardNonStream(c *gin.Context, cand routing.Candidate, fc *forwardCtx) tryOutcome {
	ch := cand.Channel
	channelAdapter, ok := adapter.Get(string(ch.Format))
	if !ok {
		// 渠道配置了未知格式：视为渠道故障，尝试下一个。
		f.logger.Error("渠道格式无对应适配器", "channel", ch.ID, "format", ch.Format)
		return tryOutcome{kind: outcomeChannelError, err: errUnexpected}
	}

	passthrough := fc.canPassthrough(ch)

	// 构造上游请求体：直通时原样透传客户端字节；否则按渠道格式 BuildRequest。
	var body []byte
	if passthrough {
		body = fc.rawBody
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

	resp, err := f.upstream.do(c.Request.Context(), ch, body, false)
	if err != nil {
		// 网络层错误：渠道故障，故障转移。
		return tryOutcome{kind: outcomeChannelError, err: err}
	}

	// 上游返回错误状态。
	if resp.StatusCode >= 400 {
		if isChannelError(resp.StatusCode) {
			f.logger.Warn("上游渠道错误", "channel", ch.ID, "status", resp.StatusCode)
			return tryOutcome{kind: outcomeChannelError, err: errUnexpected}
		}
		// 4xx（非渠道故障）：请求本身的问题，渠道健康。透传上游错误体，不再重试。
		// 未产生用量，settled=false 由上层 defer 退回押金。
		relayUpstreamError(c, resp.StatusCode, resp.Body)
		return tryOutcome{kind: outcomeClientError}
	}

	// 成功：解析为规范响应（即便直通也需解析以提取 usage 计费）。
	canonResp, err := channelAdapter.ParseResponse(resp.Body)
	if err != nil {
		f.logger.Error("解析上游响应失败", "channel", ch.ID, "err", err)
		return tryOutcome{kind: outcomeChannelError, err: err}
	}

	// 先结算：token 已实际消耗，即便后续构造客户端响应失败也应记账。
	settled := f.settle(fc.res, ch.ID, fc.requestID, canonResp.Usage)

	// 回填渠道与用量到访问日志。
	middleware.SetLogUpstream(c, ch.ID)
	middleware.SetLogUsage(c, canonResp.Usage.InputTokens, canonResp.Usage.OutputTokens)

	// 直通：响应与客户端同格式，原样透传上游字节，跳过反向转换。
	if passthrough {
		c.Data(http.StatusOK, contentTypeJSON, resp.Body)
		return tryOutcome{kind: outcomeSuccess, settled: settled, committed: true}
	}

	// 反向转换回客户端格式；模型名回填为客户端请求的对外名。
	canonResp.Model = fc.clientModel
	out, err := fc.clientAdapter.BuildResponse(canonResp)
	if err != nil {
		f.logger.Error("构造客户端响应失败", "err", err)
		writeError(c, http.StatusInternalServerError, "internal_error", "构造响应失败")
		// 已成功调用上游并结算，视为成功（不再故障转移）。
		return tryOutcome{kind: outcomeSuccess, settled: settled, committed: true}
	}

	c.Data(http.StatusOK, contentTypeJSON, out)
	return tryOutcome{kind: outcomeSuccess, settled: settled, committed: true}
}

const contentTypeJSON = "application/json; charset=utf-8"

// relayUpstreamError 把上游的 4xx 错误透传给客户端。
// 上游错误体已是 JSON（各家都用 {"error":{...}} 结构），原样转发最利于调试。
func relayUpstreamError(c *gin.Context, status int, body []byte) {
	if len(body) == 0 {
		writeError(c, status, "upstream_error", "上游返回错误")
		return
	}
	c.Data(status, contentTypeJSON, body)
}
