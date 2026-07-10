package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// MetricsAuth 用固定 Bearer token 保护指标端点。token 为空仅供本地 debug；release
// 启动校验会拒绝空值。比较使用常量时间，且日志不会记录 Authorization 头。
func MetricsAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		const prefix = "Bearer "
		auth := c.GetHeader("Authorization")
		provided := ""
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			provided = strings.TrimSpace(auth[len(prefix):])
		}
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}
