package middleware

import (
	"github.com/gin-gonic/gin"

	"linapi/internal/adapter"
	"linapi/internal/canonical"
)

const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"

	ctxKeyProtocol = "linapi.protocol"
)

// ProtocolRoute 把一个精确 HTTP endpoint 绑定到客户端线协议。精确匹配避免
// `/admin` 等普通 JSON API 被兼容端点的错误 schema 污染。
type ProtocolRoute struct {
	Method   string
	Path     string
	Protocol string
}

// ProtocolContext 在后续全局/分组中间件执行前按 endpoint 注入客户端协议。
// 应注册在 BodyLimit、Recovery、Auth 和限流器之前。
func ProtocolContext(routes ...ProtocolRoute) gin.HandlerFunc {
	index := make(map[string]string, len(routes))
	for _, route := range routes {
		if route.Method == "" || route.Path == "" || route.Protocol == "" {
			continue
		}
		index[route.Method+" "+route.Path] = route.Protocol
	}
	return func(c *gin.Context) {
		if protocol := index[c.Request.Method+" "+c.Request.URL.Path]; protocol != "" {
			SetProtocol(c, protocol)
		}
		c.Next()
	}
}

// Protocol 返回一个可用于单独 route/group 的协议注入中间件。
func Protocol(protocol string) gin.HandlerFunc {
	return func(c *gin.Context) {
		SetProtocol(c, protocol)
		c.Next()
	}
}

func SetProtocol(c *gin.Context, protocol string) {
	if protocol != "" {
		c.Set(ctxKeyProtocol, protocol)
	}
}

func ProtocolFrom(c *gin.Context) (string, bool) {
	value, ok := c.Get(ctxKeyProtocol)
	if !ok {
		return "", false
	}
	protocol, ok := value.(string)
	return protocol, ok && protocol != ""
}

// WriteError 按当前请求协议输出错误；没有协议上下文时维持历史 OpenAI schema。
func WriteError(c *gin.Context, status int, errType, message string) {
	protocol, _ := ProtocolFrom(c)
	WriteProtocolError(c, status, protocol, &canonical.ErrorResponse{
		Type: errType, Message: message,
	})
}

// WriteProtocolError 按显式协议编码规范错误，不调用 Abort。
func WriteProtocolError(c *gin.Context, status int, protocol string, errResp *canonical.ErrorResponse) {
	if clientAdapter, ok := adapter.Get(protocol); ok {
		if codec, ok := clientAdapter.(adapter.ErrorCodec); ok {
			if body, err := codec.BuildError(errResp); err == nil {
				c.Data(status, "application/json; charset=utf-8", body)
				return
			}
		}
	}

	if protocol == ProtocolAnthropic {
		c.JSON(status, gin.H{
			"type":  "error",
			"error": gin.H{"type": errResp.Type, "message": errResp.Message},
		})
		return
	}
	c.JSON(status, gin.H{
		"error": gin.H{"type": errResp.Type, "message": errResp.Message},
	})
}
