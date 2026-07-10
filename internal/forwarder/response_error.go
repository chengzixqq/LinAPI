package forwarder

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
	"linapi/internal/middleware"
)

type upstreamHTTPError struct {
	body    []byte
	header  http.Header
	adapter adapter.Adapter
}

// relayUpstreamError 把任意上游错误解析成规范模型，再按客户端协议编码。
// 非 JSON 或结构异常的上游错误不会被伪装成原协议 JSON，而是成为明确的
// upstream_error；原始文本只作为经过长度/控制字符约束的 message。
func relayUpstreamError(
	c *gin.Context,
	status int,
	body []byte,
	header http.Header,
	upstreamAdapter adapter.Adapter,
	clientFormat string,
) {
	errResp := parseCanonicalUpstreamError(upstreamAdapter, body)
	if errResp.RequestID == "" {
		errResp.RequestID = upstreamRequestID(header)
	}
	copySafeUpstreamHeaders(c.Writer.Header(), header)
	middleware.WriteProtocolError(c, status, clientFormat, errResp)
}

func parseCanonicalUpstreamError(upstreamAdapter adapter.Adapter, body []byte) *canonical.ErrorResponse {
	if codec, ok := upstreamAdapter.(adapter.ErrorCodec); ok && len(body) > 0 {
		if parsed, err := codec.ParseError(body); err == nil && parsed != nil {
			return parsed
		}
	}
	message := safeUpstreamErrorText(body)
	if message == "" {
		message = "上游返回错误"
	}
	return &canonical.ErrorResponse{Type: "upstream_error", Message: message}
}

func safeUpstreamErrorText(body []byte) string {
	const maxErrorMessageBytes = 4096
	if len(body) > maxErrorMessageBytes {
		body = body[:maxErrorMessageBytes]
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(body), "�"))
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, text)
	if !utf8.ValidString(text) {
		return ""
	}
	return text
}

func upstreamRequestID(header http.Header) string {
	for _, name := range []string{
		"Request-Id", "X-Request-Id", "Openai-Request-Id", "Anthropic-Request-Id",
	} {
		if value := safeHeaderValue(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// copySafeUpstreamHeaders 只转发客户端可安全消费、不会改变 HTTP 连接语义的头。
// 网关自己的 X-Request-Id 保持权威；上游请求 ID 另存为 X-Upstream-Request-Id。
func copySafeUpstreamHeaders(dst, src http.Header) {
	if requestID := upstreamRequestID(src); requestID != "" {
		dst.Set("X-Upstream-Request-Id", requestID)
	}
	for name, values := range src {
		lower := strings.ToLower(name)
		if !safeUpstreamHeaderName(lower) {
			continue
		}
		canonicalName := http.CanonicalHeaderKey(name)
		dst.Del(canonicalName)
		for _, value := range values {
			if value = safeHeaderValue(value); value != "" {
				dst.Add(canonicalName, value)
			}
		}
	}
}

func safeUpstreamHeaderName(lower string) bool {
	switch lower {
	case "retry-after", "ratelimit-limit", "ratelimit-remaining", "ratelimit-reset":
		return true
	default:
		return strings.HasPrefix(lower, "x-ratelimit-") ||
			strings.HasPrefix(lower, "anthropic-ratelimit-")
	}
}

func safeHeaderValue(value string) string {
	const maxHeaderValueBytes = 4096
	if value == "" || len(value) > maxHeaderValueBytes {
		return ""
	}
	for _, r := range value {
		if r == '\t' || r >= 0x20 && r != 0x7f {
			continue
		}
		return ""
	}
	return value
}
