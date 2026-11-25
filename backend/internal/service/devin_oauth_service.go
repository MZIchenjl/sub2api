package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/devin"
)

// DevinOAuthService coordinates Devin PKCE login and credential validation for admin accounts.
type DevinOAuthService struct {
	sessionStore *devin.SessionStore
	proxyRepo    ProxyRepository
}

// NewDevinOAuthService creates a new DevinOAuthService instance.
func NewDevinOAuthService(proxyRepo ProxyRepository) *DevinOAuthService {
	return &DevinOAuthService{
		sessionStore: devin.NewSessionStore(),
		proxyRepo:    proxyRepo,
	}
}

// Stop releases the OAuth session store background cleanup goroutine.
func (s *DevinOAuthService) Stop() {
	if s != nil && s.sessionStore != nil {
		s.sessionStore.Stop()
	}
}

// DevinAuthURLResult is returned when initiating a Devin PKCE OAuth flow.
type DevinAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// DevinExchangeCodeInput carries the parameters to exchange a Devin authorization code.
type DevinExchangeCodeInput struct {
	SessionID string
	Code      string
	State     string
	ProxyID   *int64
}

// DevinTokenInfo holds the normalized Devin session token plus profile/quota metadata.
type DevinTokenInfo struct {
	SessionToken                string `json:"session_token"`
	Email                       string `json:"email,omitempty"`
	UserName                    string `json:"user_name,omitempty"`
	UserID                      string `json:"user_id,omitempty"`
	OrgID                       string `json:"org_id,omitempty"`
	OrgName                     string `json:"org_name,omitempty"`
	TeamID                      string `json:"team_id,omitempty"`
	Plan                        string `json:"plan,omitempty"`
	BaseURL                     string `json:"base_url,omitempty"`
	DailyQuotaRemainingPercent  *int64 `json:"daily_quota_remaining_percent,omitempty"`
	WeeklyQuotaRemainingPercent *int64 `json:"weekly_quota_remaining_percent,omitempty"`
	DailyQuotaResetAt           string `json:"daily_quota_reset_at,omitempty"`
	WeeklyQuotaResetAt          string `json:"weekly_quota_reset_at,omitempty"`
}

// GenerateAuthURL creates a Devin PKCE authorization URL and stores the transient session.
func (s *DevinOAuthService) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI string) (*DevinAuthURLResult, error) {
	state, err := devin.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}
	codeVerifier, err := devin.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code_verifier: %w", err)
	}
	sessionID, err := devin.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session_id: %w", err)
	}

	proxyURL := s.resolveProxyURL(ctx, proxyID)
	s.sessionStore.Set(sessionID, &devin.OAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		RedirectURI:  strings.TrimSpace(redirectURI),
		ProxyURL:     proxyURL,
		CreatedAt:    time.Now(),
	})

	codeChallenge := devin.GenerateCodeChallenge(codeVerifier)
	authURL := devin.BuildAuthorizationURL(devin.DefaultAppBaseURL, redirectURI, codeChallenge, state)

	return &DevinAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// ExchangeCode exchanges a PKCE authorization code (or pasted callback URL) for a Devin session token.
func (s *DevinOAuthService) ExchangeCode(ctx context.Context, input *DevinExchangeCodeInput) (*DevinTokenInfo, error) {
	if input == nil {
		return nil, fmt.Errorf("invalid exchange code input")
	}
	session, ok := s.sessionStore.Get(strings.TrimSpace(input.SessionID))
	if !ok {
		return nil, fmt.Errorf("oauth session not found or expired")
	}

	parsedCode, parsedState := devin.ParseCallbackInput(input.Code)
	if parsedCode == "" {
		return nil, fmt.Errorf("authorization code is required")
	}
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = parsedState
	}
	if state != "" && state != session.State {
		return nil, fmt.Errorf("oauth state mismatch")
	}

	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		if resolved := s.resolveProxyURL(ctx, input.ProxyID); resolved != "" {
			proxyURL = resolved
		}
	}

	client, err := devin.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create devin client failed: %w", err)
	}

	token, err := client.ExchangeCode(ctx, parsedCode, session.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("exchange devin code failed: %w", err)
	}
	s.sessionStore.Delete(strings.TrimSpace(input.SessionID))

	return s.enrichTokenInfo(ctx, client, token, devin.DefaultServerURL), nil
}

// ValidateToken validates a manually pasted Devin session token or cog_... API key
// against GetUserStatus (and /v3/self fallback) and returns account metadata.
func (s *DevinOAuthService) ValidateToken(ctx context.Context, rawToken, baseURL string, proxyID *int64) (*DevinTokenInfo, error) {
	token := devin.FormatSessionToken(rawToken)
	if token == "" {
		return nil, fmt.Errorf("devin session token or API key is required")
	}

	proxyURL := s.resolveProxyURL(ctx, proxyID)
	client, err := devin.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create devin client failed: %w", err)
	}
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBase != "" {
		client.SetServerBaseURL(trimmedBase)
	} else {
		trimmedBase = devin.DefaultServerURL
	}

	status, statusErr := client.FetchUserStatus(ctx, token, "")
	profile, profileErr := client.FetchSelfProfile(ctx, token)
	if statusErr != nil && profileErr != nil {
		return nil, fmt.Errorf("devin token validation failed: %w", statusErr)
	}

	info := &DevinTokenInfo{
		SessionToken: token,
		BaseURL:      trimmedBase,
	}
	applyDevinMetadata(info, status, profile)
	return info, nil
}

func (s *DevinOAuthService) enrichTokenInfo(ctx context.Context, client *devin.Client, token, baseURL string) *DevinTokenInfo {
	info := &DevinTokenInfo{
		SessionToken: token,
		BaseURL:      baseURL,
	}
	status, _ := client.FetchUserStatus(ctx, token, "")
	profile, _ := client.FetchSelfProfile(ctx, token)
	applyDevinMetadata(info, status, profile)
	return info
}

func applyDevinMetadata(info *DevinTokenInfo, status *devin.UserStatus, profile *devin.SelfProfile) {
	if profile != nil {
		info.UserName = profile.UserName
		info.UserID = profile.UserID
		info.OrgID = profile.OrgID
	}
	if status != nil {
		if status.Email != "" {
			info.Email = status.Email
		}
		if info.UserName == "" && status.UserName != "" {
			info.UserName = status.UserName
		}
		if info.UserID == "" && status.UserID != "" {
			info.UserID = status.UserID
		}
		if info.OrgID == "" && status.OrgID != "" {
			info.OrgID = status.OrgID
		}
		if status.OrgName != "" {
			info.OrgName = status.OrgName
		}
		if status.TeamID != "" {
			info.TeamID = status.TeamID
		}
		if status.Plan != "" {
			info.Plan = status.Plan
		}
		daily := status.DailyQuotaRemainingPercent
		weekly := status.WeeklyQuotaRemainingPercent
		info.DailyQuotaRemainingPercent = &daily
		info.WeeklyQuotaRemainingPercent = &weekly
		if !status.DailyQuotaResetAt.IsZero() {
			info.DailyQuotaResetAt = status.DailyQuotaResetAt.UTC().Format(time.RFC3339)
		}
		if !status.WeeklyQuotaResetAt.IsZero() {
			info.WeeklyQuotaResetAt = status.WeeklyQuotaResetAt.UTC().Format(time.RFC3339)
		}
	}
}

func (s *DevinOAuthService) resolveProxyURL(ctx context.Context, proxyID *int64) string {
	if s == nil || s.proxyRepo == nil || proxyID == nil {
		return ""
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}
