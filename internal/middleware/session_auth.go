package middleware

import (
	"context"
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

// SessionVersionChecker 回查某账户当前的会话代次。由账户库实现（审查 AUD-P1-17）：
// 账户禁用 / 改密时代次递增，鉴权时据此作废所有旧会话。
type SessionVersionChecker interface {
	SessionVersion(ctx context.Context, accountID int64) (int, error)
}

// SessionVersionCheckerFunc 让普通函数满足 SessionVersionChecker 接口。
type SessionVersionCheckerFunc func(ctx context.Context, accountID int64) (int, error)

// SessionVersion 调用底层函数。
func (f SessionVersionCheckerFunc) SessionVersion(ctx context.Context, accountID int64) (int, error) {
	return f(ctx, accountID)
}

// SessionAuthWithVersion 在 SessionAuth 基础上增加会话代次校验（审查 AUD-P1-17）：
// 反查会话后，再用 checker 取账户当前代次，与会话快照 SessionVersion 比对。
//   - 一致：放行；
//   - 不一致（账户已禁用 / 改密使代次递增）：判定为陈旧会话，主动删除该会话并 401，
//     使旧 Cookie 立即失效、不再复用；
//   - checker 出错（账户库异常）：fail-closed 返回 503，绝不因回查失败而跳过校验放行。
//
// checker 为 nil 时退化为普通 SessionAuth（不做代次校验），便于未接账户库的场景。
func SessionAuthWithVersion(m *session.Manager, checker SessionVersionChecker) gin.HandlerFunc {
	if checker == nil {
		return SessionAuth(m)
	}
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
			abortError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
			return
		}
		current, err := checker.SessionVersion(c.Request.Context(), data.AccountID)
		if err != nil {
			// 账户库异常：fail-closed，绝不跳过代次校验。
			abortError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
			return
		}
		if current != data.SessionVersion {
			// 代次已变（账户禁用 / 改密）：主动删除陈旧会话，令旧 Cookie 立即失效。
			// 删除失败不影响判定——本请求一律拒绝，杜绝旧会话被复用。
			_ = m.Delete(c.Request.Context(), token)
			abortError(c, http.StatusUnauthorized, "authentication_error", "会话已失效，请重新登录")
			return
		}
		c.Set(ctxKeySession, data)
		c.Next()
	}
}
