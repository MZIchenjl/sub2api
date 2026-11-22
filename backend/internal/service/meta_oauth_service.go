package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/meta"
)

// MetaDeviceFlowStartResult is returned when initiating Meta Muse RFC 8628 device authorization.
type MetaDeviceFlowStartResult struct {
	SessionID               string `json:"session_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// MetaTokenInfo represents normalized credentials & profile returned by Meta OAuth/Mint.
type MetaTokenInfo struct {
	APIKey           string `json:"api_key"`
	DCAToken         string `json:"dca_token,omitempty"`
	DCAExpiresAt     int64  `json:"dca_expires_at,omitempty"`
	BaseURL          string `json:"base_url"`
	Email            string `json:"email,omitempty"`
	Name             string `json:"name,omitempty"`
	SubsTierName     string `json:"subs_tier_name,omitempty"`
	SubsTierID       string `json:"subs_tier_id,omitempty"`
	IsSubsActive     bool   `json:"is_subs_active,omitempty"`
	HasPaymentMethod bool   `json:"has_payment_method,omitempty"`
}

// MetaDeviceFlowPollResult is returned on each poll step of Meta device authorization.
type MetaDeviceFlowPollResult struct {
	Status    string         `json:"status"`
	Interval  int            `json:"interval,omitempty"`
	TokenInfo *MetaTokenInfo `json:"token_info,omitempty"`
}

// MetaOAuthService manages Meta Muse RFC 8628 device authorization and DCA->API key minting.
type MetaOAuthService struct {
	client       *meta.Client
	sessionStore *meta.SessionStore
	proxyRepo    ProxyRepository
}

// NewMetaOAuthService constructs a MetaOAuthService.
func NewMetaOAuthService(proxyRepo ProxyRepository) *MetaOAuthService {
	return &MetaOAuthService{
		client:       meta.NewClient(),
		sessionStore: meta.NewSessionStore(),
		proxyRepo:    proxyRepo,
	}
}

// Stop releases background resources held by the session store.
func (s *MetaOAuthService) Stop() {
	if s != nil && s.sessionStore != nil {
		s.sessionStore.Stop()
	}
}

func (s *MetaOAuthService) resolveProxyURL(ctx context.Context, proxyID *int64) string {
	if proxyID == nil || s == nil || s.proxyRepo == nil {
		return ""
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}

// StartDeviceFlow initiates Meta Muse RFC 8628 device authorization.
func (s *MetaOAuthService) StartDeviceFlow(ctx context.Context, proxyID *int64) (*MetaDeviceFlowStartResult, error) {
	proxyURL := s.resolveProxyURL(ctx, proxyID)
	dcr, err := s.client.RequestDeviceCode(ctx, proxyURL)
	if err != nil {
		return nil, err
	}
	sessionID, err := meta.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	ttl := meta.DefaultSessionTTL
	if dcr.ExpiresIn > 0 {
		if d := time.Duration(dcr.ExpiresIn) * time.Second; d < ttl {
			ttl = d
		}
	}
	now := time.Now()
	s.sessionStore.Set(sessionID, &meta.DeviceSession{
		SessionID:               sessionID,
		DeviceCode:              dcr.DeviceCode,
		UserCode:                dcr.UserCode,
		VerificationURI:         dcr.VerificationURI,
		VerificationURIComplete: dcr.VerificationURIComplete,
		Interval:                dcr.Interval,
		ProxyURL:                proxyURL,
		CreatedAt:               now,
		ExpiresAt:               now.Add(ttl),
	})

	return &MetaDeviceFlowStartResult{
		SessionID:               sessionID,
		UserCode:                dcr.UserCode,
		VerificationURI:         dcr.VerificationURI,
		VerificationURIComplete: dcr.VerificationURIComplete,
		ExpiresIn:               dcr.ExpiresIn,
		Interval:                dcr.Interval,
	}, nil
}

// PollDeviceFlow checks the Meta device authorization session once and mints an API key when approved.
func (s *MetaOAuthService) PollDeviceFlow(ctx context.Context, sessionID string, proxyID *int64) (*MetaDeviceFlowPollResult, error) {
	sess, ok := s.sessionStore.Get(strings.TrimSpace(sessionID))
	if !ok {
		return nil, fmt.Errorf("device authorization session not found or expired")
	}
	proxyURL := sess.ProxyURL
	if proxyURL == "" {
		proxyURL = s.resolveProxyURL(ctx, proxyID)
	}
	res, err := s.client.PollDeviceTokenOnce(ctx, sess.DeviceCode, proxyURL)
	if err != nil {
		return nil, err
	}
	switch res.Status {
	case meta.PollStatusPending:
		return &MetaDeviceFlowPollResult{Status: string(res.Status), Interval: sess.Interval}, nil
	case meta.PollStatusSlowDown:
		nextInterval := sess.Interval + 5
		if res.Interval > nextInterval {
			nextInterval = res.Interval
		}
		sess.Interval = nextInterval
		return &MetaDeviceFlowPollResult{Status: string(res.Status), Interval: nextInterval}, nil
	case meta.PollStatusExpired, meta.PollStatusDenied:
		s.sessionStore.Delete(sess.SessionID)
		return &MetaDeviceFlowPollResult{Status: string(res.Status)}, nil
	case meta.PollStatusAuthorized:
		s.sessionStore.Delete(sess.SessionID)
		info := &MetaTokenInfo{
			DCAToken:     res.Token.AccessToken,
			DCAExpiresAt: res.Token.ExpiresAt,
			BaseURL:      meta.DefaultAPIBaseURL,
		}
		if res.Minted != nil {
			applyMintedKeyToInfo(info, res.Minted)
		}
		return &MetaDeviceFlowPollResult{
			Status:    string(res.Status),
			TokenInfo: info,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected poll status: %s", res.Status)
	}
}

// ValidateOrMintToken accepts either a dca:... token (exchanging it via /muse-code/key)
// or a direct Meta Muse API key.
func (s *MetaOAuthService) ValidateOrMintToken(ctx context.Context, token, baseURL string, proxyID *int64) (*MetaTokenInfo, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return nil, fmt.Errorf("meta token or api_key is required")
	}
	resolvedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if resolvedBase == "" {
		resolvedBase = meta.DefaultAPIBaseURL
	}
	if meta.IsDCAToken(trimmed) {
		proxyURL := s.resolveProxyURL(ctx, proxyID)
		minted, err := s.client.MintAPIKey(ctx, trimmed, proxyURL)
		if err != nil {
			return nil, err
		}
		info := &MetaTokenInfo{
			DCAToken: trimmed,
			BaseURL:  resolvedBase,
		}
		applyMintedKeyToInfo(info, minted)
		if strings.TrimSpace(baseURL) != "" {
			info.BaseURL = resolvedBase
		}
		return info, nil
	}
	return &MetaTokenInfo{
		APIKey:  trimmed,
		BaseURL: resolvedBase,
	}, nil
}

// RefreshAccountToken mints a fresh Muse API key from an account's stored DCA token.
func (s *MetaOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*MetaTokenInfo, error) {
	if account == nil {
		return nil, fmt.Errorf("account is nil")
	}
	dcaToken := account.GetMetaDCAToken()
	if dcaToken == "" {
		return nil, fmt.Errorf("meta account %d missing dca_token", account.ID)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	} else if account.ProxyID != nil {
		proxyURL = s.resolveProxyURL(ctx, account.ProxyID)
	}
	minted, err := s.client.MintAPIKey(ctx, dcaToken, proxyURL)
	if err != nil {
		return nil, err
	}
	info := &MetaTokenInfo{
		DCAToken: dcaToken,
		BaseURL:  account.GetMetaBaseURL(),
	}
	applyMintedKeyToInfo(info, minted)
	return info, nil
}

func applyMintedKeyToInfo(info *MetaTokenInfo, minted *meta.MintedKeyResponse) {
	if info == nil || minted == nil {
		return
	}
	info.APIKey = strings.TrimSpace(minted.APIKey)
	if b := strings.TrimRight(strings.TrimSpace(minted.BaseURL), "/"); b != "" {
		info.BaseURL = b
	}
	info.Email = strings.TrimSpace(minted.UserEmail)
	info.Name = strings.TrimSpace(minted.UserFullName)
	info.SubsTierName = strings.TrimSpace(minted.SubsTierName)
	info.SubsTierID = strings.TrimSpace(minted.SubsTierID)
	info.IsSubsActive = minted.IsSubsActive
	info.HasPaymentMethod = minted.HasPaymentMethod
}

// BuildAccountCredentials builds the standard credentials map for a Meta Muse account.
func (s *MetaOAuthService) BuildAccountCredentials(info *MetaTokenInfo) map[string]any {
	if info == nil {
		return map[string]any{}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(info.BaseURL), "/")
	if baseURL == "" {
		baseURL = meta.DefaultAPIBaseURL
	}
	creds := map[string]any{
		"base_url": baseURL,
	}
	if info.APIKey != "" {
		creds["api_key"] = info.APIKey
		creds["access_token"] = info.APIKey
	} else if info.DCAToken != "" {
		creds["access_token"] = info.DCAToken
	}
	if info.DCAToken != "" {
		creds["dca_token"] = info.DCAToken
	}
	if info.DCAExpiresAt > 0 {
		creds["dca_expires_at"] = info.DCAExpiresAt
	}
	if info.Email != "" {
		creds["email"] = info.Email
	}
	if info.Name != "" {
		creds["user_name"] = info.Name
	}
	if info.SubsTierName != "" {
		creds["subs_tier_name"] = info.SubsTierName
	}
	if info.SubsTierID != "" {
		creds["subs_tier_id"] = info.SubsTierID
	}
	return creds
}

// BuildAccountExtra builds extra metadata persisted on the Meta Muse account.
func (s *MetaOAuthService) BuildAccountExtra(info *MetaTokenInfo) map[string]any {
	if info == nil {
		return nil
	}
	extra := map[string]any{}
	if info.Email != "" {
		extra["email"] = info.Email
		extra["meta_email"] = info.Email
	}
	if info.SubsTierName != "" {
		extra["meta_plan"] = info.SubsTierName
	}
	if info.SubsTierID != "" {
		extra["meta_subs_tier_id"] = info.SubsTierID
	}
	extra["meta_is_subs_active"] = info.IsSubsActive
	if len(extra) == 0 {
		return nil
	}
	return extra
}
