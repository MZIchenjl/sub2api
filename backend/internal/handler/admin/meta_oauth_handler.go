package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MetaOAuthHandler exposes admin endpoints for Meta Muse Code RFC 8628 Device Flow OAuth and token validation.
type MetaOAuthHandler struct {
	metaOAuthService *service.MetaOAuthService
}

// NewMetaOAuthHandler constructs a MetaOAuthHandler.
func NewMetaOAuthHandler(metaOAuthService *service.MetaOAuthService) *MetaOAuthHandler {
	return &MetaOAuthHandler{metaOAuthService: metaOAuthService}
}

type MetaStartDeviceFlowRequest struct {
	ProxyID *int64 `json:"proxy_id"`
}

type MetaPollDeviceFlowRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

type MetaValidateTokenRequest struct {
	Token   string `json:"token" binding:"required"`
	BaseURL string `json:"base_url"`
	ProxyID *int64 `json:"proxy_id"`
}

// StartDeviceFlow handles POST /api/v1/admin/meta/oauth/device-code
func (h *MetaOAuthHandler) StartDeviceFlow(c *gin.Context) {
	var req MetaStartDeviceFlowRequest
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	res, err := h.metaOAuthService.StartDeviceFlow(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.InternalError(c, "Failed to start Meta device flow: "+err.Error())
		return
	}
	response.Success(c, res)
}

// PollDeviceFlow handles POST /api/v1/admin/meta/oauth/poll
func (h *MetaOAuthHandler) PollDeviceFlow(c *gin.Context) {
	var req MetaPollDeviceFlowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	res, err := h.metaOAuthService.PollDeviceFlow(c.Request.Context(), req.SessionID, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Failed to poll Meta authorization: "+err.Error())
		return
	}
	response.Success(c, res)
}

// ValidateToken handles POST /api/v1/admin/meta/oauth/validate-token
func (h *MetaOAuthHandler) ValidateToken(c *gin.Context) {
	var req MetaValidateTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	info, err := h.metaOAuthService.ValidateOrMintToken(c.Request.Context(), req.Token, req.BaseURL, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Failed to validate Meta token: "+err.Error())
		return
	}
	response.Success(c, info)
}
