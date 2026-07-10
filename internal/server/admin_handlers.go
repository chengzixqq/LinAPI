package server

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
)

// adminHandlers 持有管理面服务，聚合用户/密钥/渠道的 CRUD 处理器。
type adminHandlers struct {
	svc *admin.Service
}

// writeAdminError 把 admin 领域错误映射为 HTTP 状态码，沿用统一错误结构。
func writeAdminError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, admin.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, admin.ErrConflict):
		writeError(c, http.StatusConflict, "conflict", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "internal_error", "存储操作失败")
	}
}

// ---- 用户 ----

type createUserReq struct {
	ExternalID string `json:"external_id" binding:"required"`
	Balance    int64  `json:"balance"`
	Enabled    *bool  `json:"enabled"` // 指针以区分"未提供"与"显式 false"，默认 true
}

func (h *adminHandlers) createUser(c *gin.Context) {
	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	u, err := h.svc.Store().CreateUser(c.Request.Context(), admin.CreateUserInput{
		ExternalID: req.ExternalID,
		Balance:    req.Balance,
		Enabled:    enabled,
	})
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, u)
}

func (h *adminHandlers) listUsers(c *gin.Context) {
	limit := queryInt(c, "limit", 100)
	offset := queryInt(c, "offset", 0)
	users, err := h.svc.Store().ListUsers(c.Request.Context(), limit, offset)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": users})
}

func (h *adminHandlers) getUser(c *gin.Context) {
	u, err := h.svc.Store().GetUser(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

type setEnabledReq struct {
	Enabled bool `json:"enabled"`
}

func (h *adminHandlers) setUserEnabled(c *gin.Context) {
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	u, err := h.svc.Store().SetUserEnabled(c.Request.Context(), c.Param("id"), req.Enabled)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

type addBalanceReq struct {
	Delta int64 `json:"delta" binding:"required"`
}

func (h *adminHandlers) addBalance(c *gin.Context) {
	var req addBalanceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	bal, err := h.svc.Store().AddBalance(c.Request.Context(), c.Param("id"), req.Delta)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"external_id": c.Param("id"), "balance": bal})
}

// ---- 密钥 ----

type createKeyReq struct {
	RateLimitPerMin int      `json:"rate_limit_per_min"`
	AllowedModels   []string `json:"allowed_models"`
	Enabled         *bool    `json:"enabled"`
}

func (h *adminHandlers) createKey(c *gin.Context) {
	var req createKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	gen, err := admin.GenerateKey()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "生成密钥失败")
		return
	}
	k, err := h.svc.Store().CreateAPIKey(c.Request.Context(), admin.CreateAPIKeyInput{
		APIKey:          gen.APIKey,
		KeyID:           gen.KeyID,
		UserID:          c.Param("id"),
		RateLimitPerMin: req.RateLimitPerMin,
		AllowedModels:   req.AllowedModels,
		Enabled:         enabled,
	})
	if err != nil {
		writeAdminError(c, err)
		return
	}
	// 明文 api_key 仅在创建响应里回显一次，库中只存其摘要。
	c.JSON(http.StatusCreated, gin.H{
		"api_key":            gen.APIKey,
		"key_id":             k.KeyID,
		"user_id":            k.UserID,
		"rate_limit_per_min": k.RateLimitPerMin,
		"allowed_models":     k.AllowedModels,
		"enabled":            k.Enabled,
		"created_at":         k.CreatedAt,
	})
}

func (h *adminHandlers) listKeys(c *gin.Context) {
	keys, err := h.svc.Store().ListAPIKeysByUser(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": keys})
}

func (h *adminHandlers) setKeyEnabled(c *gin.Context) {
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	k, err := h.svc.Store().SetAPIKeyEnabled(c.Request.Context(), c.Param("keyid"), req.Enabled)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, k)
}
