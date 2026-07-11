package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
	"linapi/internal/middleware"
	"linapi/internal/session"
)

// dummyPasswordHash 是一枚固定的 bcrypt 哈希，用于账户不存在时仍执行一次等价的密码比较，
// 抹平「账户存在」与「不存在」之间的响应耗时差异，堵住基于计时的用户名枚举侧信道。
// 占位串远超最小长度，HashPassword 不应失败；万一失败则在启动期 panic 而非让登录路径
// 静默拿到空哈希（那会使侧信道防护失效），fail-fast 优于静默降级。
var dummyPasswordHash = mustHashPassword("linapi-timing-guard-placeholder")

func mustHashPassword(plain string) string {
	h, err := account.HashPassword(plain)
	if err != nil {
		panic("server: 生成 dummy 密码哈希失败: " + err.Error())
	}
	return h
}

// authHandlers 聚合 /auth 端点的处理器。
type authHandlers struct {
	accounts     account.AccountStore
	settings     account.SettingsStore
	sessions     *session.Manager
	secureCookie bool // 生产置 true（HTTPS）；本地/测试为 false 以便非 HTTPS 下 Cookie 可用。
	// bcryptSem 是 bcrypt 并发信号量（审查 AUD-P1-27）：login/register 在做密码哈希/比较
	// 前须先取一个槽，把在途 bcrypt goroutine 数卡在上限内，防匿名并发登录耗尽 CPU。
	// 为 nil 时不限制并发（测试便利）。
	bcryptSem       *middleware.Semaphore
	credentialLimit *middleware.IdentifierRateLimiter
}

func newAuthHandlers(accounts account.AccountStore, settings account.SettingsStore, sessions *session.Manager, secureCookie bool, bcryptSem *middleware.Semaphore, credentialLimit *middleware.IdentifierRateLimiter) *authHandlers {
	return &authHandlers{
		accounts: accounts, settings: settings, sessions: sessions, secureCookie: secureCookie,
		bcryptSem: bcryptSem, credentialLimit: credentialLimit,
	}
}

// acquireBcrypt 非阻塞获取一枚 bcrypt 并发槽；成功返回释放函数。信号量为 nil 时
// 直接放行。容量已满立即返回 false，由调用方回 503，不留下等待中的 handler。
func (h *authHandlers) acquireBcrypt() (release func(), ok bool) {
	if h.bcryptSem == nil {
		return func() {}, true
	}
	if !h.bcryptSem.TryAcquire() {
		return nil, false
	}
	return h.bcryptSem.Release, true
}

func (h *authHandlers) allowCredential(c *gin.Context, scope, username string) bool {
	if h.credentialLimit == nil {
		return true
	}
	allowed, retryAfter, err := h.credentialLimit.Allow(c.Request.Context(), scope, username)
	if err != nil {
		// 与来源 IP 限流一致：Redis 短暂故障时 fail-open，bcrypt 全局并发槽仍兜底。
		return true
	}
	if allowed {
		return true
	}
	c.Header("Retry-After", strconv.Itoa(retryAfter))
	writeError(c, http.StatusTooManyRequests, "rate_limit_error", "认证请求过于频繁，请稍后重试")
	return false
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

// setCSRFCookie 下发 CSRF token Cookie。与会话 Cookie 不同，此 Cookie 刻意 **非 HttpOnly**：
// 前端 JS 需读取它并在写请求头 X-CSRF-Token 回传（双重提交，审查 AUD-P1-26）。仍设
// SameSite=Strict 收窄暴露面。token 本身不是机密——真正的防护在于攻击者跨站读不到它、
// 也设不了自定义请求头。
func (h *authHandlers) setCSRFCookie(c *gin.Context, token string, maxAgeSeconds int) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(middleware.CSRFCookieName, token, maxAgeSeconds, "/", "", h.secureCookie, false)
}

// register 自助注册：受 registration_enabled 开关控制。
func (h *authHandlers) register(c *gin.Context) {
	var req credentialsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if !h.allowCredential(c, "register", req.Username) {
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
	// bcrypt 前取并发槽（审查 AUD-P1-27）：排不上队即回 503，避免 goroutine 无界堆积。
	release, ok := h.acquireBcrypt()
	if !ok {
		writeError(c, http.StatusServiceUnavailable, "internal_error", "服务繁忙，请稍后重试")
		return
	}
	hash, err := account.HashPassword(req.Password)
	release()
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) || errors.Is(err, account.ErrPasswordTooLong) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码须至少 8 个字符且不超过 72 字节")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}
	// 自助注册恒不发放额度（审查 AUD-P0-07）：绑定初始余额固定为 0，忽略
	// settings.NewUserInitialBalance。否则任何人注册一个账号即克隆一笔免费额度，
	// 注册开关一开即被薅穿。要给新用户发额度只能走管理面主动建号 / 充值（可信操作）。
	_, err = h.accounts.CreateUserAccount(c.Request.Context(), req.Username, hash, 0)
	if err != nil {
		if errors.Is(err, account.ErrConflict) {
			// 与成功使用完全相同的状态和响应，避免注册端点旁路登录的统一错误，
			// 泄露用户名是否存在（审查 AUD-P2-21）。
			c.JSON(http.StatusCreated, gin.H{"ok": true})
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "创建账户失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"ok": true})
}

// login 校验账密，建会话，下发 Cookie。
func (h *authHandlers) login(c *gin.Context) {
	var req credentialsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if !h.allowCredential(c, "login", req.Username) {
		return
	}
	cred, err := h.accounts.GetCredentials(c.Request.Context(), req.Username)
	// 恒定工作量：账户不存在时也拿 dummy hash 比一次，消除计时侧信道（防用户名枚举）。
	hashToCheck := dummyPasswordHash
	if err == nil {
		hashToCheck = cred.PasswordHash
	}
	// bcrypt 前取并发槽（审查 AUD-P1-27）：无论账户是否存在都同样 acquire+compare，
	// 保持恒定工作量侧信道防护；排不上队即回 503，避免并发登录耗尽 CPU。
	release, ok := h.acquireBcrypt()
	if !ok {
		writeError(c, http.StatusServiceUnavailable, "internal_error", "服务繁忙，请稍后重试")
		return
	}
	passOK := account.CheckPassword(hashToCheck, req.Password)
	release()
	if err != nil || !cred.Enabled || !passOK {
		// 统一错误，不区分「用户不存在」「账户禁用」「密码错误」，避免用户名枚举。
		writeError(c, http.StatusUnauthorized, "authentication_error", "用户名或密码错误")
		return
	}
	ttl := session.DefaultTTL
	if req.Remember {
		ttl = session.RememberTTL
	}
	// 生成与会话绑定的 CSRF token（审查 AUD-P1-26）：存入会话数据，登出删会话即失效。
	csrfToken, err := session.NewCSRFToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "生成会话失败")
		return
	}
	token, err := h.sessions.Create(c.Request.Context(), session.SessionData{
		AccountID: cred.ID, Username: cred.Username, Role: cred.Role, ExternalID: cred.ExternalID,
		CSRFToken: csrfToken,
		// 快照登录时刻的账户会话代次（审查 AUD-P1-17）：账户被禁用/改密后代次递增，
		// 鉴权时比对不一致即判定为已撤销的旧会话并拒绝。
		SessionVersion: cred.SessionVersion,
	}, ttl)
	if err != nil {
		if errors.Is(err, session.ErrTooManyActiveSessions) {
			writeError(c, http.StatusTooManyRequests, "rate_limit_error", "活跃会话数已达上限，请先退出其它设备")
			return
		}
		writeError(c, http.StatusServiceUnavailable, "internal_error", "会话服务暂时不可用")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	h.setCSRFCookie(c, csrfToken, int(ttl.Seconds()))
	c.JSON(http.StatusOK, gin.H{"username": cred.Username, "role": cred.Role, "csrf_token": csrfToken})
}

// logout 删会话 + 清 Cookie。
//
// 撤销可靠性（审查 AUD-P1-29）：用独立短超时 context 执行删除，不复用请求 context
// ——否则客户端在收到响应前断开会取消删除，留下可被盗用的活会话。删除失败时返回 503
// 且不清 Cookie：绝不能让用户误以为已安全登出，而服务端 token 仍在 Redis 里有效
// （最长 7 天）。
func (h *authHandlers) logout(c *gin.Context) {
	token, err := c.Cookie(middleware.CookieName)
	if err != nil || token == "" {
		// 本就无会话 Cookie：视作已登出，清一次 Cookie 保证幂等。
		h.setSessionCookie(c, "", -1)
		h.setCSRFCookie(c, "", -1)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.sessions.Delete(ctx, token); err != nil {
		// 撤销失败：不清 Cookie、不报成功，让客户端明确知道登出未完成。
		writeError(c, http.StatusServiceUnavailable, "internal_error", "登出失败，请重试")
		return
	}

	h.setSessionCookie(c, "", -1) // maxAge<0 立即失效。
	h.setCSRFCookie(c, "", -1)
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
		"csrf_token": s.CSRFToken, // 前端刷新后恢复 CSRF token（审查 AUD-P1-26）。
	})
}

// registrationStatus 是公开只读端点：匿名可达（未登录也能查），供登录页决定是否显示注册入口。
// 只暴露 registration_enabled 一个布尔位——注册入口本就是公开功能，不算敏感信息；仍与 login/register
// 同挂来源 IP 限流兜底滥用。读设置失败时 fail-closed 返回 false（宁可少显示入口，也不谎报开放）。
func (h *authHandlers) registrationStatus(c *gin.Context) {
	settings, err := h.settings.Get(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"registration_enabled": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"registration_enabled": settings.RegistrationEnabled})
}
