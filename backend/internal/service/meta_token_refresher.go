package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// MetaTokenCacheKey returns the distributed lock/cache key for a Meta Muse OAuth account.
func MetaTokenCacheKey(account *Account) string {
	return "meta:account:" + strconv.FormatInt(account.ID, 10)
}

// MetaTokenRefresher implements TokenRefresher for Meta Muse OAuth accounts.
// Minted Muse API keys do not expire on the short DCA timer, so background refresh
// only runs when a DCA token is present without a minted API key; manual refresh
// can always remint from the stored DCA token.
type MetaTokenRefresher struct {
	metaOAuthService *MetaOAuthService
}

// NewMetaTokenRefresher constructs a MetaTokenRefresher.
func NewMetaTokenRefresher(metaOAuthService *MetaOAuthService) *MetaTokenRefresher {
	return &MetaTokenRefresher{metaOAuthService: metaOAuthService}
}

// CacheKey returns the lock key for distributed refresh deduplication.
func (r *MetaTokenRefresher) CacheKey(account *Account) string {
	return MetaTokenCacheKey(account)
}

// CanRefresh reports whether account is a Meta OAuth account with a DCA token.
func (r *MetaTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformMeta &&
		account.Type == AccountTypeOAuth &&
		strings.TrimSpace(account.GetMetaDCAToken()) != ""
}

// NeedsRefresh returns true only when a Meta OAuth account has a DCA token but no minted API key yet.
func (r *MetaTokenRefresher) NeedsRefresh(account *Account, _ time.Duration) bool {
	if !r.CanRefresh(account) {
		return false
	}
	return strings.TrimSpace(account.GetMetaAPIKey()) == ""
}

// Refresh mints a fresh API key from the account's DCA token and merges credentials.
func (r *MetaTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.metaOAuthService == nil {
		return nil, errors.New("meta oauth service is not configured")
	}
	info, err := r.metaOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return nil, err
	}
	newCreds := r.metaOAuthService.BuildAccountCredentials(info)
	merged := MergeCredentials(account.Credentials, newCreds)
	delete(merged, "expired")
	return merged, nil
}
