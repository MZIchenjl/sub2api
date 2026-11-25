// Package devin provides OAuth, Connect-RPC wire protocol, quota, and
// request/response translation support for Devin (Cognition / Windsurf) accounts.
package devin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// DefaultAppBaseURL is the user-facing web app for Devin CLI PKCE authorization.
	DefaultAppBaseURL = "https://app.devin.ai"
	// DefaultAPIBaseURL is the Devin REST API backend for token exchange and /v3/self.
	DefaultAPIBaseURL = "https://api.devin.ai"
	// DefaultServerURL is the upstream Codeium/Devin Connect-RPC server.
	DefaultServerURL = "https://server.codeium.com"

	// SessionTokenPrefix is the mandatory prefix for Devin CLI/Desktop JWT session tokens.
	SessionTokenPrefix = "devin-session-token$"

	// SessionTTL is the lifetime of an in-flight OAuth PKCE session.
	SessionTTL = 30 * time.Minute
)

// OAuthSession holds transient PKCE state for an in-flight admin OAuth login.
type OAuthSession struct {
	State        string
	CodeVerifier string
	RedirectURI  string
	ProxyURL     string
	CreatedAt    time.Time
}

// SessionStore stores active Devin OAuth PKCE sessions in memory.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*OAuthSession
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewSessionStore creates a SessionStore with periodic expired-session cleanup.
func NewSessionStore() *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*OAuthSession),
		stopCh:   make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// Set stores a session by ID.
func (s *SessionStore) Set(sessionID string, session *OAuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

// Get returns a non-expired session by ID.
func (s *SessionStore) Get(sessionID string) (*OAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok || session == nil {
		return nil, false
	}
	if time.Since(session.CreatedAt) > SessionTTL {
		return nil, false
	}
	return session, true
}

// Delete removes a session by ID.
func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// Stop stops the background cleanup goroutine.
func (s *SessionStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, sess := range s.sessions {
				if sess == nil || time.Since(sess.CreatedAt) > SessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// GenerateState generates a random URL-safe OAuth state string.
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateSessionID generates a random hex session identifier.
func GenerateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateCodeVerifier generates an RFC 7636 PKCE code verifier.
func GenerateCodeVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateCodeChallenge computes the S256 PKCE code challenge for codeVerifier.
func GenerateCodeChallenge(codeVerifier string) string {
	sum := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// BuildAuthorizationURL builds the Devin CLI PKCE authorization URL.
// When redirectURI is empty, it generates the headless manual code URL
// (cli_pkce_marker=1) matching the official Devin CLI query parameter ordering.
func BuildAuthorizationURL(appBaseURL, redirectURI, codeChallenge, state string) string {
	base := strings.TrimRight(strings.TrimSpace(appBaseURL), "/")
	if base == "" {
		base = DefaultAppBaseURL
	}
	trimmedRedirect := strings.TrimSpace(redirectURI)
	var queryParts []string
	if trimmedRedirect != "" {
		queryParts = append(queryParts, "redirect_uri="+url.QueryEscape(trimmedRedirect))
	}
	if strings.TrimSpace(state) != "" {
		queryParts = append(queryParts, "state="+url.QueryEscape(strings.TrimSpace(state)))
	}
	queryParts = append(queryParts,
		"prompt=select_account",
		"code_challenge="+url.QueryEscape(strings.TrimSpace(codeChallenge)),
		"code_challenge_method=S256",
	)
	if trimmedRedirect == "" {
		queryParts = append(queryParts, "cli_pkce_marker=1")
	}
	return fmt.Sprintf("%s/auth/cli/continue?%s", base, strings.Join(queryParts, "&"))
}

// ParseCallbackInput extracts the authorization code and optional state from either
// a full callback URL (e.g. http://127.0.0.1:.../callback?code=...&state=...) or a bare code.
func ParseCallbackInput(raw string) (code, state string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	if strings.Contains(trimmed, "://") || strings.Contains(trimmed, "code=") {
		if parsed, err := url.Parse(trimmed); err == nil {
			q := parsed.Query()
			if c := strings.TrimSpace(q.Get("code")); c != "" {
				return c, strings.TrimSpace(q.Get("state"))
			}
		}
		if idx := strings.Index(trimmed, "?"); idx >= 0 {
			if q, err := url.ParseQuery(trimmed[idx+1:]); err == nil {
				if c := strings.TrimSpace(q.Get("code")); c != "" {
					return c, strings.TrimSpace(q.Get("state"))
				}
			}
		}
	}
	return trimmed, ""
}

// FormatSessionToken normalizes a Devin credential.
// JWTs starting with "eyJ" are automatically prefixed with "devin-session-token$".
// Already-prefixed session tokens and "cog_..." API keys are preserved as-is.
func FormatSessionToken(rawToken string) string {
	t := strings.TrimSpace(rawToken)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, SessionTokenPrefix) {
		return t
	}
	if strings.HasPrefix(t, "eyJ") {
		return SessionTokenPrefix + t
	}
	return t
}

// SelfProfile represents user identity returned by GET https://api.devin.ai/v3/self.
type SelfProfile struct {
	UserName string `json:"user_name"`
	UserID   string `json:"user_id"`
	OrgID    string `json:"org_id"`
}

// Client handles HTTP and Connect-RPC requests to Devin endpoints.
type Client struct {
	httpClient    *http.Client
	appBaseURL    string
	apiBaseURL    string
	serverBaseURL string
}

// NewClient constructs a Devin client with optional HTTP/SOCKS5 proxy support.
func NewClient(proxyURL string) (*Client, error) {
	return NewClientWithTimeout(proxyURL, 30*time.Second)
}

// NewClientWithTimeout constructs a Devin client with the specified HTTP timeout.
func NewClientWithTimeout(proxyURL string, timeout time.Duration) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(proxyURL) != "" {
		_, parsed, err := proxyurl.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url: %w", err)
		}
		if parsed != nil {
			if err := proxyutil.ConfigureTransportProxy(transport, parsed); err != nil {
				return nil, fmt.Errorf("configure proxy transport: %w", err)
			}
		}
	}
	return &Client{
		httpClient:    &http.Client{Transport: transport, Timeout: timeout},
		appBaseURL:    DefaultAppBaseURL,
		apiBaseURL:    DefaultAPIBaseURL,
		serverBaseURL: DefaultServerURL,
	}, nil
}

// NewClientWithHTTPClient constructs a Devin client backed by a caller-supplied http.Client.
func NewClientWithHTTPClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		httpClient:    httpClient,
		appBaseURL:    DefaultAppBaseURL,
		apiBaseURL:    DefaultAPIBaseURL,
		serverBaseURL: DefaultServerURL,
	}
}

// SetServerBaseURL overrides the upstream Connect-RPC server URL.
func (c *Client) SetServerBaseURL(raw string) {
	if c != nil && strings.TrimSpace(raw) != "" {
		c.serverBaseURL = strings.TrimRight(strings.TrimSpace(raw), "/")
	}
}

// SetAPIBaseURL overrides the Devin REST API URL (primarily for tests).
func (c *Client) SetAPIBaseURL(raw string) {
	if c != nil && strings.TrimSpace(raw) != "" {
		c.apiBaseURL = strings.TrimRight(strings.TrimSpace(raw), "/")
	}
}

// SetAppBaseURL overrides the Devin web app URL (primarily for tests).
func (c *Client) SetAppBaseURL(raw string) {
	if c != nil && strings.TrimSpace(raw) != "" {
		c.appBaseURL = strings.TrimRight(strings.TrimSpace(raw), "/")
	}
}

// ServerBaseURL returns the configured upstream Connect-RPC server URL.
func (c *Client) ServerBaseURL() string {
	if c == nil || strings.TrimSpace(c.serverBaseURL) == "" {
		return DefaultServerURL
	}
	return c.serverBaseURL
}

// ExchangeCode exchanges a PKCE authorization code for a Devin session token.
func (c *Client) ExchangeCode(ctx context.Context, code, codeVerifier string) (string, error) {
	if c == nil || c.httpClient == nil {
		return "", errors.New("devin client is not initialized")
	}
	trimmedCode := strings.TrimSpace(code)
	trimmedVerifier := strings.TrimSpace(codeVerifier)
	if trimmedCode == "" {
		return "", errors.New("authorization code is required")
	}
	if trimmedVerifier == "" {
		return "", errors.New("code_verifier is required")
	}

	payload, err := json.Marshal(map[string]string{
		"code":          trimmedCode,
		"code_verifier": trimmedVerifier,
	})
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("%s/auth/cli/token", strings.TrimRight(c.apiBaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("devin token exchange request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read devin token exchange response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("devin token exchange failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", fmt.Errorf("parse devin token exchange response: %w", err)
	}
	token := FormatSessionToken(parsed.Token)
	if token == "" {
		return "", errors.New("devin token exchange response missing token")
	}
	return token, nil
}

// FetchSelfProfile queries GET /v3/self for user identity metadata.
func (c *Client) FetchSelfProfile(ctx context.Context, sessionToken string) (*SelfProfile, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("devin client is not initialized")
	}
	token := FormatSessionToken(sessionToken)
	if token == "" {
		return nil, errors.New("session token is required")
	}

	endpoint := fmt.Sprintf("%s/v3/self", strings.TrimRight(c.apiBaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devin /v3/self returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var profile SelfProfile
	if err := json.Unmarshal(respBytes, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}
