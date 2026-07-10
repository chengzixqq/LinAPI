package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"linapi/internal/account"
)

// accountConsoleHandlers 聚合 /admin/accounts 与 /admin/settings 端点。
type accountConsoleHandlers struct {
	accounts account.AccountStore
	settings account.SettingsStore
}

func newAccountConsoleHandlers(accounts account.AccountStore, settings account.SettingsStore) *accountConsoleHandlers {
	return &accountConsoleHandlers{accounts: accounts, settings: settings}
}

// writeAccountError 把 account 领域错误映射为 HTTP 状态码。
func writeAccountError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, account.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "账户不存在")
	case errors.Is(err, account.ErrConflict):
		writeError(c, http.StatusConflict, "conflict", "用户名已存在")
	case errors.Is(err, account.ErrInvalidRole):
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法角色")
	default:
		writeError(c, http.StatusInternalServerError, "internal_error", "存储操作失败")
	}
}

func (h *accountConsoleHandlers) listAccounts(c *gin.Context) {
	limit, ok := queryInt(c, "limit", 100, 1, 500)
	if !ok {
		return
	}
	offset, ok := queryInt(c, "offset", 0, 0, 1_000_000_000)
	if !ok {
		return
	}
	accs, err := h.accounts.ListAccounts(c.Request.Context(), limit, offset)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accs})
}

type createAccountReq struct {
	Username       string `json:"username" binding:"required"`
	Password       string `json:"password" binding:"required"`
	Role           string `json:"role" binding:"required"`
	InitialBalance int64  `json:"initial_balance"`
}

func (h *accountConsoleHandlers) createAccount(c *gin.Context) {
	var req createAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if !account.ValidRole(req.Role) {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法角色（仅 admin/user）")
		return
	}
	hash, err := account.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) || errors.Is(err, account.ErrPasswordTooLong) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码须至少 8 个字符且不超过 72 字节")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}

	var acc account.Account
	if req.Role == account.RoleUser {
		// user 账户：自动建计费实体，admin 可指定初始余额。
		acc, err = h.accounts.CreateUserAccount(c.Request.Context(), req.Username, hash, req.InitialBalance)
	} else {
		acc, err = h.accounts.CreateAccount(c.Request.Context(), account.CreateAccountInput{
			Username: req.Username, PasswordHash: hash, Role: req.Role,
		})
	}
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusCreated, acc) // acc 无 PasswordHash 字段。
}

func (h *accountConsoleHandlers) setAccountEnabled(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法账户 ID")
		return
	}
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	acc, err := h.accounts.SetEnabled(c.Request.Context(), id, *req.Enabled)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, acc)
}

type resetPasswordReq struct {
	Password string `json:"password" binding:"required"`
}

func (h *accountConsoleHandlers) resetPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "非法账户 ID")
		return
	}
	var req resetPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	hash, err := account.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, account.ErrPasswordTooShort) || errors.Is(err, account.ErrPasswordTooLong) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "密码须至少 8 个字符且不超过 72 字节")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "处理密码失败")
		return
	}
	if err := h.accounts.UpdatePassword(c.Request.Context(), id, hash); err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *accountConsoleHandlers) getSettings(c *gin.Context) {
	s, err := h.settings.Get(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取设置失败")
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *accountConsoleHandlers) putSettings(c *gin.Context) {
	var s account.Settings
	if err := c.ShouldBindJSON(&s); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	// 纵深防御（审查 AUD-P0-07）：自助注册恒不发额度，new_user_initial_balance 已无
	// 正向语义。拒绝正值，杜绝“设了却不生效”的脏配置误导运维以为注册会送额度。
	// 要给用户发额度只能走管理面主动建号 / 充值。
	if s.NewUserInitialBalance != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request_error",
			"new_user_initial_balance 必须为 0：自助注册不发放额度，请通过管理面主动建号或充值")
		return
	}
	if err := h.settings.Put(c.Request.Context(), s); err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "保存设置失败")
		return
	}
	c.JSON(http.StatusOK, s)
}
