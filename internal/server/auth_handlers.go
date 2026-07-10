package server

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
	"linapi/internal/middleware"
	"linapi/internal/session"
)

// dummyPasswordHash 是一枚固定的 bcrypt 哈希，用于账户不存在时仍执行一次等价的密码比较，
// 抹平「账户存在」与「不存在」之间的响应耗时差异，堵住基于计时的用户名枚举侧信道。
var dummyPasswordHash, _ = account.HashPassword("linapi-timing-guard-placeholder")

// authHandlers 聚合 /auth 端点的处理器。
type authHandlers struct {
	accounts     account.AccountStore
	settings     account.SettingsStore
	sessions     *session.Manager
	secureCookie bool // 生产置 true（HTTPS）；本地/测试为 false 以便非 HTTPS 下 Cookie 可用。
}

func newAuthHandlers(accounts account.AccountStore, settings account.SettingsStore, sessions *session.Manager, secureCookie bool) *authHandlers {
	return &authHandlers{accounts: accounts, settings: settings, sessions: sessions, secureCookie: secureCookie}
}

type credentialsReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Remember bool   `json:"remember"`
}

// setSessionCookie 下发 HttpOnly + SameSite=Strict 会话 Cookie。
func (h *authHandlers) setSessionCookie(c *gin.Context, token string, maxAgeSeconds int) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(middleware.CookieName, token, maxAgeSeconds, "/", "", h.secureCookie, true)
}

// register 自助注册：受 registration_enabled 开关控制。
func (h *authHandlers) register(c *gin.Context) {
	var req credentialsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	settings, err := h.settings.Get(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取系统设置失败")
		return
	}
	if !settings.RegistrationEnabled {
		writeError(c, http.StatusForbidden, "permission_error", "当前未开放注册")
		return
	}
	hash, err := account.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码长度不足（至少 8 位）")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}
	acc, err := h.accounts.CreateUserAccount(c.Request.Context(), req.Username, hash, settings.NewUserInitialBalance)
	if err != nil {
		if errors.Is(err, account.ErrConflict) {
			writeError(c, http.StatusConflict, "conflict", "用户名已存在")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "创建账户失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"username": acc.Username, "role": acc.Role})
}

// login 校验账密，建会话，下发 Cookie。
func (h *authHandlers) login(c *gin.Context) {
	var req credentialsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	cred, err := h.accounts.GetCredentials(c.Request.Context(), req.Username)
	// 恒定工作量：账户不存在时也拿 dummy hash 比一次，消除计时侧信道（防用户名枚举）。
	hashToCheck := dummyPasswordHash
	if err == nil {
		hashToCheck = cred.PasswordHash
	}
	passOK := account.CheckPassword(hashToCheck, req.Password)
	if err != nil || !cred.Enabled || !passOK {
		// 统一错误，不区分「用户不存在」「账户禁用」「密码错误」，避免用户名枚举。
		writeError(c, http.StatusUnauthorized, "authentication_error", "用户名或密码错误")
		return
	}
	ttl := session.DefaultTTL
	if req.Remember {
		ttl = session.RememberTTL
	}
	token, err := h.sessions.Create(c.Request.Context(), session.SessionData{
		AccountID: cred.ID, Username: cred.Username, Role: cred.Role, ExternalID: cred.ExternalID,
	}, ttl)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	c.JSON(http.StatusOK, gin.H{"username": cred.Username, "role": cred.Role})
}

// logout 删会话 + 清 Cookie。
func (h *authHandlers) logout(c *gin.Context) {
	if token, err := c.Cookie(middleware.CookieName); err == nil {
		_ = h.sessions.Delete(c.Request.Context(), token)
	}
	h.setSessionCookie(c, "", -1) // maxAge<0 立即失效。
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// me 返回当前会话身份（前端恢复登录态用）。
func (h *authHandlers) me(c *gin.Context) {
	s, ok := middleware.SessionFrom(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "authentication_error", "未登录")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"username": s.Username, "role": s.Role, "external_id": s.ExternalID,
	})
}
