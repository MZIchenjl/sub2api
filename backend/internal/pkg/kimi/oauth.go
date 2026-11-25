// Package kimi provides OAuth2 Device Authorization Grant (RFC 8628) authentication,
// token refresh, and client header helpers for Kimi Code (kimi.com & kimi.ai).
package kimi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/google/uuid"
)

const (
	// ClientID is Kimi Code CLI's official OAuth client ID.
	ClientID = "17e5f671-d194-4dfb-9706-5516cb48c098"

	// DefaultDomain is Kimi's primary (.com) domain.
	DefaultDomain = "kimi.com"
	// AIDomain is Kimi's international (.ai) domain.
	AIDomain = "kimi.ai"

	// OAuthHostCom is the OAuth server host for kimi.com.
	OAuthHostCom = "https://auth.kimi.com"
	// OAuthHostAI is the OAuth server host for kimi.ai.
	OAuthHostAI = "https://auth.kimi.ai"

	// APIBaseURLCom is the coding API base URL for kimi.com.
	APIBaseURLCom = "https://api.kimi.com/coding"
	// APIBaseURLAI is the coding API base URL for kimi.ai.
	APIBaseURLAI = "https://api.kimi.ai/coding"

	// DeviceAuthorizationPath is the RFC 8628 device authorization endpoint path.
	DeviceAuthorizationPath = "/api/oauth/device_authorization"
	// TokenPath is the OAuth2 token & refresh endpoint path.
	TokenPath = "/api/oauth/token"

	// DeviceGrantType is the RFC 8628 grant type.
	DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// DefaultPollIntervalSeconds is the fallback polling interval.
	DefaultPollIntervalSeconds = 5

	// DefaultSessionTTL is how long an initiated device-code session lives in memory.
	DefaultSessionTTL = 15 * time.Minute

	// DefaultUserAgent is the CLI user agent used for Kimi OAuth requests.
	DefaultUserAgent = "KimiCLI/1.0"
	// ClientPlatformHeader identifies the client in X-Msh-Platform.
	ClientPlatformHeader = "kimi_cli"
	// ClientVersionHeader identifies the client version in X-Msh-Version.
	ClientVersionHeader = "1.0.0"
)

// IsAIDomain reports whether domain refers to kimi.ai.
func IsAIDomain(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	return d == "kimi.ai" || d == "ai" || d == "kimi-ai" || strings.HasSuffix(d, ".kimi.ai")
}

// NormalizeDomain canonicalizes a domain string to "kimi.com" or "kimi.ai".
func NormalizeDomain(domain string) string {
	if IsAIDomain(domain) {
		return AIDomain
	}
	return DefaultDomain
}

// ResolveOAuthHost returns the OAuth host URL for the given domain.
func ResolveOAuthHost(domain string) string {
	if IsAIDomain(domain) {
		return OAuthHostAI
	}
	return OAuthHostCom
}

// ResolveAPIBaseURL returns the Kimi For Coding base URL (without /v1) for the given domain.
func ResolveAPIBaseURL(domain string) string {
	if IsAIDomain(domain) {
		return APIBaseURLAI
	}
	return APIBaseURLCom
}

// DeviceCodeResponse represents Kimi's RFC 8628 device authorization response.
type DeviceCodeResponse struct {
	UserCode                string `json:"user_code"`
	DeviceCode              string `json:"device_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenData represents tokens returned from Kimi's OAuth token/refresh endpoint.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	Scope        string `json:"scope"`
}

// PollStatus describes the single-step poll outcome of a device flow session.
type PollStatus string

const (
	PollStatusPending    PollStatus = "pending"
	PollStatusSlowDown   PollStatus = "slow_down"
	PollStatusAuthorized PollStatus = "authorized"
	PollStatusExpired    PollStatus = "expired"
	PollStatusDenied     PollStatus = "denied"
)

// PollResult is the outcome of a single non-blocking poll against Kimi's token endpoint.
type PollResult struct {
	Status   PollStatus
	Token    *TokenData
	Interval int
}

// DeviceSession holds in-flight RFC 8628 device flow state.
type DeviceSession struct {
	SessionID               string
	Domain                  string
	DeviceID                string
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                int
	ProxyURL                string
	CreatedAt               time.Time
	ExpiresAt               time.Time
}

// SessionStore stores active Kimi device authorization sessions in memory.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*DeviceSession
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewSessionStore constructs a SessionStore with periodic cleanup.
func NewSessionStore() *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*DeviceSession),
		stopCh:   make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for id, sess := range s.sessions {
				if now.After(sess.ExpiresAt) {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

// Stop terminates the background cleanup goroutine.
func (s *SessionStore) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// Set saves a device session.
func (s *SessionStore) Set(id string, session *DeviceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = session
}

// Get returns a non-expired session if present.
func (s *SessionStore) Get(id string) (*DeviceSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok || sess == nil || time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

// Delete removes a session.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// GenerateSessionID returns a random 16-byte hex session ID.
func GenerateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateDeviceID returns a fresh UUIDv4 device identifier.
func GenerateDeviceID() string {
	return uuid.New().String()
}

// DeviceModel returns a human-readable OS/arch device model string.
func DeviceModel() string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("macOS %s", runtime.GOARCH)
	case "windows":
		return fmt.Sprintf("Windows %s", runtime.GOARCH)
	case "linux":
		return fmt.Sprintf("Linux %s", runtime.GOARCH)
	default:
		return fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
	}
}

// Hostname returns the host machine name with a safe fallback.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "sub2api"
	}
	return h
}

// ApplyOAuthHeaders injects Kimi CLI X-Msh-* device headers onto req.
func ApplyOAuthHeaders(req *http.Request, deviceID string) {
	if req == nil {
		return
	}
	if strings.TrimSpace(req.Header.Get("User-Agent")) == "" {
		req.Header.Set("User-Agent", DefaultUserAgent)
	}
	req.Header.Set("X-Msh-Platform", ClientPlatformHeader)
	req.Header.Set("X-Msh-Version", ClientVersionHeader)
	req.Header.Set("X-Msh-Device-Name", Hostname())
	req.Header.Set("X-Msh-Device-Model", DeviceModel())
	if trimmed := strings.TrimSpace(deviceID); trimmed != "" {
		req.Header.Set("X-Msh-Device-Id", trimmed)
	}
}

// Client performs HTTP calls for Kimi OAuth device authorization and token refresh.
type Client struct {
	oauthHostOverride string
	timeout           time.Duration
}

// NewClient constructs a Kimi OAuth HTTP client.
func NewClient() *Client {
	return &Client{timeout: 30 * time.Second}
}

// SetOAuthHostOverride overrides the OAuth host URL (used in tests).
func (c *Client) SetOAuthHostOverride(raw string) {
	c.oauthHostOverride = strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (c *Client) oauthHost(domain string) string {
	if c != nil && c.oauthHostOverride != "" {
		return c.oauthHostOverride
	}
	return ResolveOAuthHost(domain)
}

func (c *Client) httpClient(proxyURL string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if trimmed := strings.TrimSpace(proxyURL); trimmed != "" {
		_, parsed, err := proxyurl.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		if err := proxyutil.ConfigureTransportProxy(transport, parsed); err != nil {
			return nil, fmt.Errorf("configure proxy transport: %w", err)
		}
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// RequestDeviceCode starts an RFC 8628 device authorization flow.
func (c *Client) RequestDeviceCode(ctx context.Context, domain, deviceID, proxyURL string) (*DeviceCodeResponse, error) {
	httpClient, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("client_id", ClientID)

	endpoint := c.oauthHost(domain) + DeviceAuthorizationPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kimi: build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	ApplyOAuthHeaders(req, deviceID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimi: device code request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kimi: read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kimi: device code request failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var dcr DeviceCodeResponse
	if err := json.Unmarshal(body, &dcr); err != nil {
		return nil, fmt.Errorf("kimi: parse device code response: %w", err)
	}
	if strings.TrimSpace(dcr.DeviceCode) == "" || strings.TrimSpace(dcr.UserCode) == "" {
		return nil, fmt.Errorf("kimi: device code response missing device_code or user_code")
	}
	if dcr.Interval < DefaultPollIntervalSeconds {
		dcr.Interval = DefaultPollIntervalSeconds
	}
	return &dcr, nil
}

// PollDeviceTokenOnce checks the token endpoint once for a pending device flow.
func (c *Client) PollDeviceTokenOnce(ctx context.Context, domain, deviceCode, deviceID, proxyURL string) (*PollResult, error) {
	httpClient, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("device_code", strings.TrimSpace(deviceCode))
	form.Set("grant_type", DeviceGrantType)

	endpoint := c.oauthHost(domain) + TokenPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kimi: build token poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	ApplyOAuthHeaders(req, deviceID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimi: token poll request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kimi: read token poll response: %w", err)
	}

	// Kimi may return HTTP 200 or 400 with OAuth error fields.
	var oauthResp struct {
		Error            string  `json:"error"`
		ErrorDescription string  `json:"error_description"`
		AccessToken      string  `json:"access_token"`
		RefreshToken     string  `json:"refresh_token"`
		TokenType        string  `json:"token_type"`
		ExpiresIn        float64 `json:"expires_in"`
		Scope            string  `json:"scope"`
	}
	if err := json.Unmarshal(body, &oauthResp); err != nil {
		return nil, fmt.Errorf("kimi: parse token poll response (HTTP %d): %w", resp.StatusCode, err)
	}

	if oauthResp.Error != "" {
		switch oauthResp.Error {
		case "authorization_pending":
			return &PollResult{Status: PollStatusPending}, nil
		case "slow_down":
			return &PollResult{Status: PollStatusSlowDown, Interval: DefaultPollIntervalSeconds + 5}, nil
		case "expired_token":
			return &PollResult{Status: PollStatusExpired}, nil
		case "access_denied":
			return &PollResult{Status: PollStatusDenied}, nil
		default:
			return nil, fmt.Errorf("kimi: oauth error: %s (%s)", oauthResp.Error, oauthResp.ErrorDescription)
		}
	}

	if strings.TrimSpace(oauthResp.AccessToken) == "" {
		return nil, fmt.Errorf("kimi: token response missing access_token (HTTP %d)", resp.StatusCode)
	}

	expiresIn := int64(oauthResp.ExpiresIn)
	var expiresAt int64
	if expiresIn > 0 {
		expiresAt = time.Now().Unix() + expiresIn
	}
	return &PollResult{
		Status: PollStatusAuthorized,
		Token: &TokenData{
			AccessToken:  oauthResp.AccessToken,
			RefreshToken: oauthResp.RefreshToken,
			TokenType:    oauthResp.TokenType,
			ExpiresIn:    expiresIn,
			ExpiresAt:    expiresAt,
			Scope:        oauthResp.Scope,
		},
	}, nil
}

// RefreshToken exchanges a Kimi refresh token for a fresh access token.
func (c *Client) RefreshToken(ctx context.Context, domain, refreshToken, deviceID, proxyURL string) (*TokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("kimi: refresh token is required")
	}
	httpClient, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	endpoint := c.oauthHost(domain) + TokenPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kimi: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	ApplyOAuthHeaders(req, deviceID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimi: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kimi: read refresh response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("kimi: refresh token rejected (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kimi: refresh failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		TokenType    string  `json:"token_type"`
		ExpiresIn    float64 `json:"expires_in"`
		Scope        string  `json:"scope"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("kimi: parse refresh response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("kimi: refresh response missing access_token")
	}

	expiresIn := int64(tokenResp.ExpiresIn)
	var expiresAt int64
	if expiresIn > 0 {
		expiresAt = time.Now().Unix() + expiresIn
	}
	return &TokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresIn:    expiresIn,
		ExpiresAt:    expiresAt,
		Scope:        tokenResp.Scope,
	}, nil
}
