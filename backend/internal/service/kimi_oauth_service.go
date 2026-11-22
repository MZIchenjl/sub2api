package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kimi"
)

// applyKimiOAuthHeaders injects Kimi CLI X-Msh-* headers when account is a Kimi OAuth account.
func applyKimiOAuthHeaders(req *http.Request, account *Account) {
	if req == nil || account == nil || !account.IsKimiOAuth() {
		return
	}
	kimi.ApplyOAuthHeaders(req, account.GetKimiDeviceID())
}

// KimiDeviceFlowStartResult is returned when initiating Kimi Code OAuth device authorization.
type KimiDeviceFlowStartResult struct {
	SessionID               string `json:"session_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Domain                  string `json:"domain"`
}

// KimiTokenInfo holds normalized OAuth tokens and metadata for a Kimi Code account.
type KimiTokenInfo struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	Scope        string `json:"scope,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	Domain       string `json:"domain"`
	BaseURL      string `json:"base_url"`
}

// KimiDeviceFlowPollResult is returned on each poll step of Kimi device authorization.
type KimiDeviceFlowPollResult struct {
	Status    string         `json:"status"`
	Interval  int            `json:"interval,omitempty"`
	TokenInfo *KimiTokenInfo `json:"token_info,omitempty"`
}

// KimiOAuthService manages Kimi Code RFC 8628 Device Flow OAuth and token refresh.
type KimiOAuthService struct {
	client       *kimi.Client
	sessionStore *kimi.SessionStore
	proxyRepo    ProxyRepository
}

// NewKimiOAuthService constructs a KimiOAuthService.
func NewKimiOAuthService(proxyRepo ProxyRepository) *KimiOAuthService {
	return &KimiOAuthService{
		client:       kimi.NewClient(),
		sessionStore: kimi.NewSessionStore(),
		proxyRepo:    proxyRepo,
	}
}

// Stop releases background resources held by the session store.
func (s *KimiOAuthService) Stop() {
	if s != nil && s.sessionStore != nil {
		s.sessionStore.Stop()
	}
}

func (s *KimiOAuthService) resolveProxyURL(ctx context.Context, proxyID *int64) string {
	if proxyID == nil || s == nil || s.proxyRepo == nil {
		return ""
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}

// StartDeviceFlow initiates Kimi Code RFC 8628 device authorization for kimi.com or kimi.ai.
func (s *KimiOAuthService) StartDeviceFlow(ctx context.Context, domain string, proxyID *int64) (*KimiDeviceFlowStartResult, error) {
	normDomain := kimi.NormalizeDomain(domain)
	deviceID := kimi.GenerateDeviceID()
	proxyURL := s.resolveProxyURL(ctx, proxyID)

	dcr, err := s.client.RequestDeviceCode(ctx, normDomain, deviceID, proxyURL)
	if err != nil {
		return nil, err
	}
	sessionID, err := kimi.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	ttl := kimi.DefaultSessionTTL
	if dcr.ExpiresIn > 0 {
		if d := time.Duration(dcr.ExpiresIn) * time.Second; d < ttl {
			ttl = d
		}
	}
	now := time.Now()
	s.sessionStore.Set(sessionID, &kimi.DeviceSession{
		SessionID:               sessionID,
		Domain:                  normDomain,
		DeviceID:                deviceID,
		DeviceCode:              dcr.DeviceCode,
		UserCode:                dcr.UserCode,
		VerificationURI:         dcr.VerificationURI,
		VerificationURIComplete: dcr.VerificationURIComplete,
		Interval:                dcr.Interval,
		ProxyURL:                proxyURL,
		CreatedAt:               now,
		ExpiresAt:               now.Add(ttl),
	})

	return &KimiDeviceFlowStartResult{
		SessionID:               sessionID,
		UserCode:                dcr.UserCode,
		VerificationURI:         dcr.VerificationURI,
		VerificationURIComplete: dcr.VerificationURIComplete,
		ExpiresIn:               dcr.ExpiresIn,
		Interval:                dcr.Interval,
		Domain:                  normDomain,
	}, nil
}

// PollDeviceFlow checks the current status of a Kimi device flow session once.
func (s *KimiOAuthService) PollDeviceFlow(ctx context.Context, sessionID string, proxyID *int64) (*KimiDeviceFlowPollResult, error) {
	sess, ok := s.sessionStore.Get(strings.TrimSpace(sessionID))
	if !ok {
		return nil, fmt.Errorf("device authorization session not found or expired")
	}
	proxyURL := sess.ProxyURL
	if proxyURL == "" {
		proxyURL = s.resolveProxyURL(ctx, proxyID)
	}
	res, err := s.client.PollDeviceTokenOnce(ctx, sess.Domain, sess.DeviceCode, sess.DeviceID, proxyURL)
	if err != nil {
		return nil, err
	}
	switch res.Status {
	case kimi.PollStatusPending:
		return &KimiDeviceFlowPollResult{Status: string(res.Status), Interval: sess.Interval}, nil
	case kimi.PollStatusSlowDown:
		nextInterval := sess.Interval + 5
		if res.Interval > nextInterval {
			nextInterval = res.Interval
		}
		sess.Interval = nextInterval
		return &KimiDeviceFlowPollResult{Status: string(res.Status), Interval: nextInterval}, nil
	case kimi.PollStatusExpired, kimi.PollStatusDenied:
		s.sessionStore.Delete(sess.SessionID)
		return &KimiDeviceFlowPollResult{Status: string(res.Status)}, nil
	case kimi.PollStatusAuthorized:
		s.sessionStore.Delete(sess.SessionID)
		return &KimiDeviceFlowPollResult{
			Status:    string(res.Status),
			TokenInfo: s.tokenDataToInfo(res.Token, sess.Domain, sess.DeviceID),
		}, nil
	default:
		return nil, fmt.Errorf("unexpected poll status: %s", res.Status)
	}
}

// RefreshToken refreshes a Kimi OAuth refresh token directly.
func (s *KimiOAuthService) RefreshToken(ctx context.Context, refreshToken, domain, deviceID string, proxyID *int64) (*KimiTokenInfo, error) {
	normDomain := kimi.NormalizeDomain(domain)
	if strings.TrimSpace(deviceID) == "" {
		deviceID = kimi.GenerateDeviceID()
	}
	proxyURL := s.resolveProxyURL(ctx, proxyID)
	td, err := s.client.RefreshToken(ctx, normDomain, refreshToken, deviceID, proxyURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(td.RefreshToken) == "" {
		td.RefreshToken = strings.TrimSpace(refreshToken)
	}
	return s.tokenDataToInfo(td, normDomain, deviceID), nil
}

// RefreshAccountToken refreshes OAuth tokens for an existing Kimi OAuth account.
func (s *KimiOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*KimiTokenInfo, error) {
	if account == nil {
		return nil, fmt.Errorf("account is nil")
	}
	rt := account.GetKimiRefreshToken()
	if rt == "" {
		return nil, fmt.Errorf("kimi account %d missing refresh_token", account.ID)
	}
	domain := account.GetKimiDomain()
	deviceID := account.GetKimiDeviceID()
	if deviceID == "" {
		deviceID = kimi.GenerateDeviceID()
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	} else if account.ProxyID != nil {
		proxyURL = s.resolveProxyURL(ctx, account.ProxyID)
	}
	td, err := s.client.RefreshToken(ctx, domain, rt, deviceID, proxyURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(td.RefreshToken) == "" {
		td.RefreshToken = rt
	}
	return s.tokenDataToInfo(td, domain, deviceID), nil
}

func (s *KimiOAuthService) tokenDataToInfo(td *kimi.TokenData, domain, deviceID string) *KimiTokenInfo {
	normDomain := kimi.NormalizeDomain(domain)
	apiBase := kimi.ResolveAPIBaseURL(normDomain)
	return &KimiTokenInfo{
		AccessToken:  td.AccessToken,
		RefreshToken: td.RefreshToken,
		TokenType:    td.TokenType,
		ExpiresIn:    td.ExpiresIn,
		ExpiresAt:    td.ExpiresAt,
		Scope:        td.Scope,
		DeviceID:     deviceID,
		Domain:       normDomain,
		BaseURL:      apiBase + "/v1",
	}
}

// BuildAccountCredentials builds the standard sub2api credentials map for a Kimi OAuth account.
func (s *KimiOAuthService) BuildAccountCredentials(info *KimiTokenInfo) map[string]any {
	if info == nil {
		return map[string]any{}
	}
	normDomain := kimi.NormalizeDomain(info.Domain)
	apiBase := kimi.ResolveAPIBaseURL(normDomain)
	creds := map[string]any{
		"access_token":  info.AccessToken,
		"refresh_token": info.RefreshToken,
		"domain":        normDomain,
		"account_mode":  AccountModeCoding,
		"api_protocol":  APIProtocolAdaptive,
		"base_url":      apiBase + "/v1",
		"api_base_urls": map[string]any{
			APIProtocolChatCompletions: apiBase + "/v1",
			APIProtocolAnthropic:       apiBase,
			APIProtocolResponses:       apiBase + "/v1",
		},
	}
	if info.TokenType != "" {
		creds["token_type"] = info.TokenType
	}
	if info.Scope != "" {
		creds["scope"] = info.Scope
	}
	if info.DeviceID != "" {
		creds["device_id"] = info.DeviceID
	}
	if info.ExpiresAt > 0 {
		creds["expires_at"] = time.Unix(info.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	return creds
}
