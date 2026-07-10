package server

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
	"linapi/internal/middleware"
	"linapi/internal/store"
)

// meHandlers 聚合 /me 用户自助端点。绑定用户一律取自会话，杜绝越权。
type meHandlers struct {
	svc   *admin.Service
	store store.Store
}

func newMeHandlers(svc *admin.Service, st store.Store) *meHandlers {
	return &meHandlers{svc: svc, store: st}
}

// sessionExternalID 取当前会话的计费实体标识；无会话时返回 ""。
func (h *meHandlers) sessionExternalID(c *gin.Context) string {
	s, ok := middleware.SessionFrom(c)
	if !ok {
		return ""
	}
	return s.ExternalID
}

// ownedKey 校验 keyID 属于当前会话用户；不属于/不存在返回 (,false)。
func (h *meHandlers) ownedKey(c *gin.Context, keyID string) (admin.APIKey, bool) {
	ext := h.sessionExternalID(c)
	keys, err := h.svc.Store().ListAPIKeysByUser(c.Request.Context(), ext)
	if err != nil {
		return admin.APIKey{}, false
	}
	for _, k := range keys {
		if k.KeyID == keyID {
			return k, true
		}
	}
	return admin.APIKey{}, false
}

// requireExternalID 取当前会话的计费实体标识；无会话（ext 为空）时写 401 并返回 false。
// 所有自助端点都应经此闸——fail-closed：宁可拒绝，也不以空身份返回默认数据。
func (h *meHandlers) requireExternalID(c *gin.Context) (string, bool) {
	ext := h.sessionExternalID(c)
	if ext == "" {
		writeError(c, http.StatusUnauthorized, "authentication_error", "未登录")
		return "", false
	}
	return ext, true
}

// profile 返回当前用户账户信息 + 余额。
func (h *meHandlers) profile(c *gin.Context) {
	ext, ok := h.requireExternalID(c)
	if !ok {
		return
	}
	bal, err := h.store.Balance(c.Request.Context(), ext)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取余额失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"external_id": ext, "balance": bal})
}

// listKeys 列出当前用户的密钥（脱敏，不含明文）。
func (h *meHandlers) listKeys(c *gin.Context) {
	ext, ok := h.requireExternalID(c)
	if !ok {
		return
	}
	keys, err := h.svc.Store().ListAPIKeysByUser(c.Request.Context(), ext)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "读取密钥失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": keys})
}

type meCreateKeyReq struct {
	RateLimitPerMin int      `json:"rate_limit_per_min"`
	AllowedModels   []string `json:"allowed_models"`
	// 注意：刻意不接收任何 user_id/external_id——绑定用户强制取自会话。
}

// createKey 自助建 key，绑定用户强制取自会话，明文仅回显一次。
func (h *meHandlers) createKey(c *gin.Context) {
	ext, ok := h.requireExternalID(c)
	if !ok {
		return
	}
	var req meCreateKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	gen, err := admin.GenerateKey()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "生成密钥失败")
		return
	}
	k, err := h.svc.Store().CreateAPIKey(c.Request.Context(), admin.CreateAPIKeyInput{
		APIKey:          gen.APIKey,
		KeyID:           gen.KeyID,
		UserID:          ext, // 强制绑定会话用户。
		RateLimitPerMin: req.RateLimitPerMin,
		AllowedModels:   req.AllowedModels,
		Enabled:         true,
	})
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"api_key":            gen.APIKey, // 仅此一次。
		"key_id":             k.KeyID,
		"rate_limit_per_min": k.RateLimitPerMin,
		"allowed_models":     k.AllowedModels,
		"enabled":            k.Enabled,
		"created_at":         k.CreatedAt,
	})
}

// setKeyEnabled 启停自己的 key；非本人 404。
func (h *meHandlers) setKeyEnabled(c *gin.Context) {
	keyID := c.Param("keyid")
	if _, ok := h.ownedKey(c, keyID); !ok {
		writeError(c, http.StatusNotFound, "not_found", "密钥不存在")
		return
	}
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	k, err := h.svc.Store().SetAPIKeyEnabled(c.Request.Context(), keyID, req.Enabled)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, k)
}

// deleteKey 删除自己的 key；非本人 404。
func (h *meHandlers) deleteKey(c *gin.Context) {
	keyID := c.Param("keyid")
	if _, ok := h.ownedKey(c, keyID); !ok {
		writeError(c, http.StatusNotFound, "not_found", "密钥不存在")
		return
	}
	if err := h.svc.Store().DeleteAPIKey(c.Request.Context(), keyID); err != nil {
		if errors.Is(err, admin.ErrNotFound) {
			writeError(c, http.StatusNotFound, "not_found", "密钥不存在")
			return
		}
		writeError(c, http.StatusInternalServerError, "internal_error", "删除密钥失败")
		return
	}
	c.Status(http.StatusNoContent)
}
