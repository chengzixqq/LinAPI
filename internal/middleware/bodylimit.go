package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimit 为所有入站请求体设置硬上限。Content-Length 已知且超限时立即返回 413；
// chunked 请求由 MaxBytesReader 在 handler 读取时强制截断。
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes <= 0 || c.Request.Body == nil {
			c.Next()
			return
		}
		if c.Request.ContentLength > maxBytes {
			abortError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "请求体过大")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
