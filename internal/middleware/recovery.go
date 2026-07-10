package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Recovery 捕获 handler panic，但不转储请求头或 panic 值，避免 Cookie、x-api-key
// 等凭证进入日志。它应放在访问日志和指标中间件内侧，使恢复后的 500 被正常观测。
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		defer func() {
			if recover() == nil {
				return
			}
			rid, _ := RequestIDFrom(c)
			logger.ErrorContext(c.Request.Context(), "request_panic",
				"request_id", rid,
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
			)
			c.Abort()
			if !c.Writer.Written() {
				abortError(c, http.StatusInternalServerError, "internal_error", "服务器内部错误")
			}
		}()
		c.Next()
	}
}
