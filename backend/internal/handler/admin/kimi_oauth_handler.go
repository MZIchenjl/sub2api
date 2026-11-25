package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// KimiOAuthHandler exposes admin endpoints for Kimi Code RFC 8628 Device Flow OAuth.
type KimiOAuthHandler struct {
	kimiOAuthService *service.KimiOAuthService
}

// NewKimiOAuthHandler constructs a KimiOAuthHandler.
func NewKimiOAuthHandler(kimiOAuthService *service.KimiOAuthService) *KimiOAuthHandler {
	return &KimiOAuthHandler{kimiOAuthService: kimiOAuthService}
}

type KimiStartDeviceFlowRequest struct {
	Domain  string `json:"domain"`
	ProxyID *int64 `json:"proxy_id"`
}

type KimiPollDeviceFlowRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

type KimiRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
	Domain       string `json:"domain"`
	DeviceID     string `json:"device_id"`
	ProxyID      *int64 `json:"proxy_id"`
}

// StartDeviceFlow handles POST /api/v1/admin/kimi/oauth/device-code
func (h *KimiOAuthHandler) StartDeviceFlow(c *gin.Context) {
	var req KimiStartDeviceFlowRequest
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	res, err := h.kimiOAuthService.StartDeviceFlow(c.Request.Context(), req.Domain, req.ProxyID)
	if err != nil {
		response.InternalError(c, "Failed to start Kimi device flow: "+err.Error())
		return
	}
	response.Success(c, res)
}

// PollDeviceFlow handles POST /api/v1/admin/kimi/oauth/poll
func (h *KimiOAuthHandler) PollDeviceFlow(c *gin.Context) {
	var req KimiPollDeviceFlowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	res, err := h.kimiOAuthService.PollDeviceFlow(c.Request.Context(), req.SessionID, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Failed to poll Kimi authorization: "+err.Error())
		return
	}
	response.Success(c, res)
}

// RefreshToken handles POST /api/v1/admin/kimi/oauth/refresh
func (h *KimiOAuthHandler) RefreshToken(c *gin.Context) {
	var req KimiRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	info, err := h.kimiOAuthService.RefreshToken(c.Request.Context(), req.RefreshToken, req.Domain, req.DeviceID, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Failed to refresh Kimi token: "+err.Error())
		return
	}
	response.Success(c, info)
}
