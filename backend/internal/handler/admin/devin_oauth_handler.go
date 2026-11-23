package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// DevinOAuthHandler handles admin Devin OAuth PKCE and token validation endpoints.
type DevinOAuthHandler struct {
	devinOAuthService *service.DevinOAuthService
}

// NewDevinOAuthHandler creates a new DevinOAuthHandler.
func NewDevinOAuthHandler(devinOAuthService *service.DevinOAuthService) *DevinOAuthHandler {
	return &DevinOAuthHandler{devinOAuthService: devinOAuthService}
}

type DevinGenerateAuthURLRequest struct {
	ProxyID     *int64 `json:"proxy_id"`
	RedirectURI string `json:"redirect_uri"`
}

// GenerateAuthURL generates a Devin PKCE authorization URL.
// POST /api/v1/admin/devin/oauth/auth-url
func (h *DevinOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req DevinGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = DevinGenerateAuthURLRequest{}
	}
	result, err := h.devinOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, req.RedirectURI)
	if err != nil {
		response.InternalError(c, "生成 Devin 授权链接失败: "+err.Error())
		return
	}
	response.Success(c, result)
}

type DevinExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Code      string `json:"code" binding:"required"`
	State     string `json:"state"`
	ProxyID   *int64 `json:"proxy_id"`
}

// ExchangeCode exchanges an authorization code (or callback URL) for a Devin session token.
// POST /api/v1/admin/devin/oauth/exchange-code
func (h *DevinOAuthHandler) ExchangeCode(c *gin.Context) {
	var req DevinExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	tokenInfo, err := h.devinOAuthService.ExchangeCode(c.Request.Context(), &service.DevinExchangeCodeInput{
		SessionID: req.SessionID,
		Code:      req.Code,
		State:     req.State,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.BadRequest(c, "Devin Token 交换失败: "+err.Error())
		return
	}
	response.Success(c, tokenInfo)
}

type DevinValidateTokenRequest struct {
	Token   string `json:"token" binding:"required"`
	BaseURL string `json:"base_url"`
	ProxyID *int64 `json:"proxy_id"`
}

// ValidateToken validates a manually entered Devin session token or cog_... API key.
// POST /api/v1/admin/devin/oauth/validate-token
func (h *DevinOAuthHandler) ValidateToken(c *gin.Context) {
	var req DevinValidateTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	tokenInfo, err := h.devinOAuthService.ValidateToken(c.Request.Context(), req.Token, req.BaseURL, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Devin 凭据校验失败: "+err.Error())
		return
	}
	response.Success(c, tokenInfo)
}
