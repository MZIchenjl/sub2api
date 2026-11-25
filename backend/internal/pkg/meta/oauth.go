// Package meta provides authentication, key minting, request preparation, and
// protocol utilities for Meta Muse Code (https://api.meta.ai/v1).
package meta

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
)

const (
	// DefaultAPIBaseURL is the default official Meta Muse API base URL.
	DefaultAPIBaseURL = "https://api.meta.ai/v1"
	// AuthHost is the Meta account OAuth2 authorization host.
	AuthHost = "https://auth.meta.com"
	// DeviceAuthorizationEndpoint is Meta's RFC 8628 device authorization endpoint.
	DeviceAuthorizationEndpoint = AuthHost + "/oidc/device/authorization/"
	// TokenEndpoint is Meta's RFC 8628 token polling endpoint.
	TokenEndpoint = AuthHost + "/oidc/device/token/"
	// DefaultMintURL exchanges a Device Client Access (dca:...) token for a Muse API key.
	DefaultMintURL = "https://api.meta.ai/muse-code/key"
	// ClientID is Meta Muse CLI's official OAuth client ID.
	ClientID = "1031625952748946"
	// DeviceGrantType is the RFC 8628 grant type parameter.
	DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// AuthUserAgent is the User-Agent used for OAuth device flow and key minting.
	AuthUserAgent = "muse-code/1.0.2"
	// DefaultPollIntervalSeconds is the fallback polling interval.
	DefaultPollIntervalSeconds = 5
	// DefaultSessionTTL bounds in-memory device flow sessions.
	DefaultSessionTTL = 15 * time.Minute
)

// DeviceCodeResponse represents Meta's device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenData represents the token payload from Meta's device token endpoint.
type TokenData struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// MintedKeyResponse represents the API key minted from https://api.meta.ai/muse-code/key.
type MintedKeyResponse struct {
	APIKey           string `json:"api_key"`
	BaseURL          string `json:"base_url"`
	UserEmail        string `json:"user_email"`
	UserFullName     string `json:"user_full_name"`
	SubsTierName     string `json:"subs_tier_name"`
	SubsTierID       string `json:"subs_tier_id"`
	IsSubsActive     bool   `json:"is_subs_active"`
	HasPaymentMethod bool   `json:"has_payment_method"`
	RequirePayment   bool   `json:"require_payment"`
	CanSubscribe     bool   `json:"can_subscribe"`
}

// PollStatus represents a single-step device authorization poll result.
type PollStatus string

const (
	PollStatusPending    PollStatus = "pending"
	PollStatusSlowDown   PollStatus = "slow_down"
	PollStatusAuthorized PollStatus = "authorized"
	PollStatusExpired    PollStatus = "expired"
	PollStatusDenied     PollStatus = "denied"
)

// PollResult holds the outcome of a single poll attempt.
type PollResult struct {
	Status   PollStatus
	Token    *TokenData
	Minted   *MintedKeyResponse
	Interval int
}

// DeviceSession stores an active Meta device flow session.
type DeviceSession struct {
	SessionID               string
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                int
	ProxyURL                string
	CreatedAt               time.Time
	ExpiresAt               time.Time
}

// SessionStore manages in-flight Meta device authorization sessions.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*DeviceSession
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewSessionStore constructs a SessionStore with periodic expiration cleanup.
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

// Stop halts background cleanup.
func (s *SessionStore) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// Set stores a session by ID.
func (s *SessionStore) Set(id string, session *DeviceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = session
}

// Get retrieves an unexpired session by ID.
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

// GenerateSessionID produces a random 16-byte hex ID.
func GenerateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IsDCAToken reports whether raw looks like a Meta Device Client Access token.
func IsDCAToken(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "dca:")
}

// Client handles Meta Muse OAuth device flow and API key minting.
type Client struct {
	deviceEndpoint string
	tokenEndpoint  string
	mintURL        string
	timeout        time.Duration
}

// NewClient constructs a Meta OAuth/Mint HTTP client.
func NewClient() *Client {
	return &Client{
		deviceEndpoint: DeviceAuthorizationEndpoint,
		tokenEndpoint:  TokenEndpoint,
		mintURL:        DefaultMintURL,
		timeout:        30 * time.Second,
	}
}

// SetEndpoints overrides OAuth and mint URLs (primarily for tests).
func (c *Client) SetEndpoints(deviceEndpoint, tokenEndpoint, mintURL string) {
	if strings.TrimSpace(deviceEndpoint) != "" {
		c.deviceEndpoint = strings.TrimSpace(deviceEndpoint)
	}
	if strings.TrimSpace(tokenEndpoint) != "" {
		c.tokenEndpoint = strings.TrimSpace(tokenEndpoint)
	}
	if strings.TrimSpace(mintURL) != "" {
		c.mintURL = strings.TrimSpace(mintURL)
	}
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

// RequestDeviceCode initiates the RFC 8628 device flow with Meta.
func (c *Client) RequestDeviceCode(ctx context.Context, proxyURL string) (*DeviceCodeResponse, error) {
	httpClient, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{"client_id": {ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.deviceEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("meta: build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", AuthUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta: device code request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("meta: read device code response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("meta: device code failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var dcr DeviceCodeResponse
	if err := json.Unmarshal(body, &dcr); err != nil {
		return nil, fmt.Errorf("meta: parse device code response: %w", err)
	}
	if strings.TrimSpace(dcr.DeviceCode) == "" || strings.TrimSpace(dcr.UserCode) == "" {
		return nil, fmt.Errorf("meta: device code response missing device_code or user_code")
	}
	if dcr.Interval < DefaultPollIntervalSeconds {
		dcr.Interval = DefaultPollIntervalSeconds
	}
	return &dcr, nil
}

// PollDeviceTokenOnce performs a single poll against Meta's token endpoint and,
// once authorized, attempts to mint an API key via /muse-code/key.
func (c *Client) PollDeviceTokenOnce(ctx context.Context, deviceCode, proxyURL string) (*PollResult, error) {
	httpClient, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":  {DeviceGrantType},
		"device_code": {strings.TrimSpace(deviceCode)},
		"client_id":   {ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("meta: build token poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", AuthUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta: token poll request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("meta: read token poll response: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		var tokenData TokenData
		if err := json.Unmarshal(body, &tokenData); err != nil {
			return nil, fmt.Errorf("meta: parse token response: %w", err)
		}
		if strings.TrimSpace(tokenData.AccessToken) == "" {
			return nil, fmt.Errorf("meta: token response missing access_token")
		}
		if tokenData.ExpiresIn > 0 {
			tokenData.ExpiresAt = time.Now().Add(time.Duration(tokenData.ExpiresIn) * time.Second).Unix()
		}
		result := &PollResult{
			Status: PollStatusAuthorized,
			Token:  &tokenData,
		}
		if minted, errMint := c.MintAPIKey(ctx, tokenData.AccessToken, proxyURL); errMint == nil && minted != nil {
			result.Minted = minted
		}
		return result, nil
	}

	var errResp TokenData
	_ = json.Unmarshal(body, &errResp)
	switch errResp.Error {
	case "authorization_pending":
		return &PollResult{Status: PollStatusPending}, nil
	case "slow_down":
		return &PollResult{Status: PollStatusSlowDown, Interval: DefaultPollIntervalSeconds + 5}, nil
	case "access_denied":
		return &PollResult{Status: PollStatusDenied}, nil
	case "expired_token":
		return &PollResult{Status: PollStatusExpired}, nil
	default:
		if errResp.Error != "" {
			return nil, fmt.Errorf("meta: oauth error: %s (%s)", errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("meta: unexpected token response (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// MintAPIKey exchanges a Device Client Access token (dca:...) for a Muse API key.
func (c *Client) MintAPIKey(ctx context.Context, dcaToken, proxyURL string) (*MintedKeyResponse, error) {
	dcaToken = strings.TrimSpace(dcaToken)
	if dcaToken == "" {
		return nil, fmt.Errorf("meta: missing dca token")
	}
	httpClient, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]string{"dca_token": dcaToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mintURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("meta: build mint request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+dcaToken)
	req.Header.Set("User-Agent", AuthUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta: mint request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("meta: read mint response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("meta: mint key failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var minted MintedKeyResponse
	if err := json.Unmarshal(body, &minted); err != nil {
		return nil, fmt.Errorf("meta: parse mint response: %w", err)
	}
	if strings.TrimSpace(minted.APIKey) == "" {
		return nil, fmt.Errorf("meta: mint response missing api_key")
	}
	return &minted, nil
}
