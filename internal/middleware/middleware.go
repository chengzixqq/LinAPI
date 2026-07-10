// Package middleware 提供网关的 HTTP 中间件：鉴权、限流、额度检查。
//
// /v1 前置 Auth -> RateLimit；资金预授权必须在解析模型与输出上限后计算，
// 因此由 Forwarder 在上游 I/O 前调用持久账本完成。
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

// abortError 按兼容端点的协议上下文中止请求；普通管理/认证端点未注入协议，
// 继续使用历史 OpenAI 风格错误结构。
func abortError(c *gin.Context, status int, errType, message string) {
	c.Abort()
	WriteError(c, status, errType, message)
}
