package middleware

import (
	"crypto/subtle"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

// AdminAuth 构建管理面鉴权中间件，与业务 /v1 鉴权完全隔离。
//
// 双重防线：
//   - token：Authorization: Bearer <token> 常量时间比对，不匹配即 401。
//   - loopbackOnly：为 true 时只接受来自回环地址（127.0.0.1 / ::1）的请求，非回环 403。
//
// token 为空时中间件拒绝一切请求（防止误配出无鉴权的管理面）——调用方
// （server 装配）应在 admin.enabled=true 时校验 token 非空并拒绝启动。
func AdminAuth(token string, loopbackOnly bool) gin.HandlerFunc {
	tokenBytes := []byte(token)
	return func(c *gin.Context) {
		if loopbackOnly && !isLoopback(c.RemoteIP()) {
			abortError(c, http.StatusForbidden, "permission_error",
				"管理接口仅允许回环地址访问")
			return
		}

		if len(tokenBytes) == 0 {
			abortError(c, http.StatusUnauthorized, "authentication_error",
				"管理接口未配置访问令牌")
			return
		}

		provided := extractBearerToken(c)
		// 常量时间比较，避免通过响应时延侧信道猜测 token。
		if subtle.ConstantTimeCompare([]byte(provided), tokenBytes) != 1 {
			abortError(c, http.StatusUnauthorized, "authentication_error",
				"无效的管理令牌")
			return
		}

		c.Next()
	}
}

// extractBearerToken 从 Authorization: Bearer 头取令牌（管理面不接受 x-api-key）。
func extractBearerToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
		return auth[len(prefix):]
	}
	return ""
}

// isLoopback 判断远端地址是否为回环。ip 为 gin 解析出的客户端 IP 字符串。
func isLoopback(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}
