package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/session"
)

// CookieName 是会话 Cookie 的名字。
const CookieName = "linapi_session"

// ctxKeySession 是会话数据注入 gin.Context 的键。
const ctxKeySession = "linapi.session"

// SessionAuth 构建会话鉴权中间件：从 Cookie 取 token，反查会话并注入 context。
// 无 Cookie / 会话失效 / Redis 异常都拒绝（fail-closed）。
func SessionAuth(m *session.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(CookieName)
		if err != nil || token == "" {
			abortError(c, http.StatusUnauthorized, "authentication_error", "未登录")
			return
		}
		data, err := m.Get(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				abortError(c, http.StatusUnauthorized, "authentication_error", "会话已失效，请重新登录")
				return
			}
			// Redis 异常等：fail-closed，返回 503。
			abortError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
			return
		}
		c.Set(ctxKeySession, data)
		c.Next()
	}
}

// RequireRole 构建角色校验中间件，须在 SessionAuth 之后挂载。
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, ok := SessionFrom(c)
		if !ok {
			abortError(c, http.StatusUnauthorized, "authentication_error", "未登录")
			return
		}
		if s.Role != role {
			abortError(c, http.StatusForbidden, "permission_error", "权限不足")
			return
		}
		c.Next()
	}
}

// SessionFrom 从 gin.Context 取出会话数据。
func SessionFrom(c *gin.Context) (session.SessionData, bool) {
	v, ok := c.Get(ctxKeySession)
	if !ok {
		return session.SessionData{}, false
	}
	s, ok := v.(session.SessionData)
	return s, ok
}
