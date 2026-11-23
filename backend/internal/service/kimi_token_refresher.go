package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

const kimiTokenRefreshMinSkew = 10 * time.Minute

// KimiTokenCacheKey returns the distributed lock/cache key for a Kimi OAuth account.
func KimiTokenCacheKey(account *Account) string {
	return "kimi:account:" + strconv.FormatInt(account.ID, 10)
}

// KimiTokenRefresher implements TokenRefresher for Kimi Code OAuth accounts.
type KimiTokenRefresher struct {
	kimiOAuthService *KimiOAuthService
}

// NewKimiTokenRefresher constructs a KimiTokenRefresher.
func NewKimiTokenRefresher(kimiOAuthService *KimiOAuthService) *KimiTokenRefresher {
	return &KimiTokenRefresher{kimiOAuthService: kimiOAuthService}
}

// CacheKey returns the lock key for distributed refresh deduplication.
func (r *KimiTokenRefresher) CacheKey(account *Account) string {
	return KimiTokenCacheKey(account)
}

// CanRefresh reports whether account is a refreshable Kimi OAuth account.
func (r *KimiTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformKimi &&
		account.Type == AccountTypeOAuth &&
		strings.TrimSpace(account.GetKimiRefreshToken()) != ""
}

// NeedsRefresh checks whether the Kimi access token is missing or near expiry.
func (r *KimiTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if account == nil || strings.TrimSpace(account.GetKimiRefreshToken()) == "" {
		return false
	}
	if strings.TrimSpace(account.GetKimiAccessToken()) == "" {
		return true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return true
	}
	if refreshWindow < kimiTokenRefreshMinSkew {
		refreshWindow = kimiTokenRefreshMinSkew
	}
	return time.Until(*expiresAt) < refreshWindow
}

// Refresh exchanges the Kimi refresh_token and preserves non-token credentials.
func (r *KimiTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.kimiOAuthService == nil {
		return nil, errors.New("kimi oauth service is not configured")
	}
	tokenInfo, err := r.kimiOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return nil, err
	}
	newCreds := r.kimiOAuthService.BuildAccountCredentials(tokenInfo)
	return MergeCredentials(account.Credentials, newCreds), nil
}
