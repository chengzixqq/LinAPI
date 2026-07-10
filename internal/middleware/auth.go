package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"linapi/internal/store"
)

// Auth 构建鉴权中间件：从 Authorization / x-api-key 头提取 API Key，
// 用 Store 解析为调用方身份并注入 gin.Context。
//
// 兼容两种客户端习惯：
//   - OpenAI 风格：Authorization: Bearer sk-xxx
//   - Anthropic 风格：x-api-key: sk-xxx
func Auth(s store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := extractAPIKey(c)
		if apiKey == "" {
			abortError(c, http.StatusUnauthorized, "authentication_error",
				"缺少 API Key：请通过 Authorization: Bearer <key> 或 x-api-key 头提供")
			return
		}

		id, err := s.ResolveKey(c.Request.Context(), apiKey)
		if err != nil {
			if errors.Is(err, store.ErrKeyNotFound) {
				abortError(c, http.StatusUnauthorized, "authentication_error",
					"无效的 API Key")
				return
			}
			// 存储层异常（如 DB 不可用），返回 500 而非 401，便于区分。
			abortError(c, http.StatusInternalServerError, "internal_error",
				"鉴权服务暂时不可用")
			return
		}

		c.Set(ctxKeyIdentity, id)
		c.Next()
	}
}

// extractAPIKey 依次尝试 Authorization: Bearer 与 x-api-key 头。
func extractAPIKey(c *gin.Context) string {
	if auth := c.GetHeader("Authorization"); auth != "" {
		const prefix = "Bearer "
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			return strings.TrimSpace(auth[len(prefix):])
		}
	}
	if key := c.GetHeader("x-api-key"); key != "" {
		return strings.TrimSpace(key)
	}
	return ""
}
