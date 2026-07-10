package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"linapi/internal/admin"
)

// channelReq 是渠道创建/更新的请求体（创建与全量更新共用）。
type channelReq struct {
	ChannelID string            `json:"channel_id" binding:"required"`
	Name      string            `json:"name"`
	Format    string            `json:"format" binding:"required"`
	BaseURL   string            `json:"base_url" binding:"required"`
	APIKey    string            `json:"api_key"`
	Models    map[string]string `json:"models"`
	Priority  int               `json:"priority"`
	Weight    int               `json:"weight"`
	Enabled   *bool             `json:"enabled"`
}

// toInput 把请求体转为领域入参；channelID 以路径参数为准（更新时）。
func (r channelReq) toInput(channelID string) admin.ChannelInput {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	weight := r.Weight
	if weight <= 0 {
		weight = 1 // 权重下限保护，避免加权随机时权重为 0 的渠道永不被选
	}
	return admin.ChannelInput{
		ChannelID: channelID,
		Name:      r.Name,
		Format:    r.Format,
		BaseURL:   r.BaseURL,
		APIKey:    r.APIKey,
		Models:    r.Models,
		Priority:  r.Priority,
		Weight:    weight,
		Enabled:   enabled,
	}
}

func (h *adminHandlers) createChannel(c *gin.Context) {
	var req channelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if !validChannelFormat(req.Format) {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "format 必须为 openai 或 anthropic")
		return
	}
	ch, err := h.svc.CreateChannel(c.Request.Context(), req.toInput(req.ChannelID))
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, sanitizeChannel(ch))
}

func (h *adminHandlers) listChannels(c *gin.Context) {
	channels, err := h.svc.Store().ListChannels(c.Request.Context())
	if err != nil {
		writeAdminError(c, err)
		return
	}
	out := make([]admin.Channel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, sanitizeChannel(ch))
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *adminHandlers) getChannel(c *gin.Context) {
	ch, err := h.svc.Store().GetChannel(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, sanitizeChannel(ch))
}

func (h *adminHandlers) updateChannel(c *gin.Context) {
	var req channelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	if !validChannelFormat(req.Format) {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "format 必须为 openai 或 anthropic")
		return
	}
	ch, err := h.svc.UpdateChannel(c.Request.Context(), req.toInput(c.Param("id")))
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, sanitizeChannel(ch))
}

func (h *adminHandlers) setChannelEnabled(c *gin.Context) {
	var req setEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "请求体无效: "+err.Error())
		return
	}
	ch, err := h.svc.SetChannelEnabled(c.Request.Context(), c.Param("id"), req.Enabled)
	if err != nil {
		writeAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, sanitizeChannel(ch))
}

func (h *adminHandlers) deleteChannel(c *gin.Context) {
	if err := h.svc.DeleteChannel(c.Request.Context(), c.Param("id")); err != nil {
		writeAdminError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// sanitizeChannel 清除渠道的上游密钥，避免管理面读取时回显敏感凭证。
func sanitizeChannel(ch admin.Channel) admin.Channel {
	ch.APIKey = ""
	return ch
}

// validChannelFormat 校验渠道线格式是否受支持。
func validChannelFormat(format string) bool {
	return format == "openai" || format == "anthropic"
}

// queryInt 读取查询参数并解析为 int，缺失或非法时返回默认值。
func queryInt(c *gin.Context, key string, def int) int {
	s := c.Query(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
