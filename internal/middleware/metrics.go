package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"linapi/internal/metrics"
)

// Metrics 构建 Prometheus HTTP 指标中间件：记录请求数与处理耗时。
//
// 路由维度用 c.FullPath()（路由模板，如 "/v1/chat/completions"）而非真实 URL，
// 避免把路径参数值放进标签造成高基数。未匹配任何路由时 FullPath 为空，归入 "unmatched"。
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		method := c.Request.Method
		status := strconv.Itoa(c.Writer.Status())

		metrics.HTTPRequestsTotal.WithLabelValues(path, method, status).Inc()
		metrics.HTTPRequestDuration.WithLabelValues(path, method).Observe(time.Since(start).Seconds())
	}
}
