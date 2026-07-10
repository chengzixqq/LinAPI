// Package middleware 提供网关的 HTTP 中间件：鉴权、限流、额度检查。
//
// 三者按顺序挂在 /v1 分组上：Auth -> RateLimit -> Quota。
// Auth 把调用方身份注入 gin.Context，后续中间件与业务处理器据此工作。
package middleware

import (
	"github.com/gin-gonic/gin"

	"linapi/internal/store"
)

// contextKey 是注入 gin.Context 的键，避免与其它键冲突。
const (
	ctxKeyIdentity = "linapi.identity"
)

// IdentityFrom 从 gin.Context 取出鉴权阶段注入的调用方身份。
// 未鉴权或注入失败时返回 (nil, false)。
func IdentityFrom(c *gin.Context) (*store.Identity, bool) {
	v, ok := c.Get(ctxKeyIdentity)
	if !ok {
		return nil, false
	}
	id, ok := v.(*store.Identity)
	return id, ok
}

// abortError 以统一的 OpenAI 风格错误结构中止请求。
// 网关对外错误格式对齐 OpenAI，便于客户端 SDK 直接消费。
func abortError(c *gin.Context, status int, errType, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}
