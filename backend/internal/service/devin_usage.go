package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/devin"
)

func (s *AccountUsageService) cachedDevinUsage(accountID int64, force bool) (*UsageInfo, bool) {
	if force || s == nil || s.cache == nil {
		return nil, false
	}
	cached, ok := s.cache.devinCache.Load(accountID)
	if !ok {
		return nil, false
	}
	entry, ok := cached.(*devinUsageCache)
	if !ok || entry == nil || entry.usageInfo == nil {
		return nil, false
	}
	ttl := apiCacheTTL
	if entry.usageInfo.Error != "" {
		ttl = apiErrorCacheTTL
	}
	if time.Since(entry.timestamp) >= ttl {
		return nil, false
	}
	return entry.usageInfo, true
}

// getDevinUsage queries Devin GetUserStatus via Connect-RPC, returning daily & weekly quota windows.
func (s *AccountUsageService) getDevinUsage(ctx context.Context, account *Account, force bool) (*UsageInfo, error) {
	if account == nil {
		now := time.Now()
		return &UsageInfo{UpdatedAt: &now}, nil
	}

	if usage, ok := s.cachedDevinUsage(account.ID, force); ok {
		return usage, nil
	}

	token := account.GetDevinToken()
	if token == "" {
		now := time.Now()
		return &UsageInfo{
			UpdatedAt:   &now,
			NeedsReauth: true,
			ErrorCode:   "unauthenticated",
			Error:       "Devin session token is missing",
		}, nil
	}

	flightKey := fmt.Sprintf("devin-usage:%d", account.ID)
	result, flightErr, _ := s.cache.devinFlight.Do(flightKey, func() (any, error) {
		if usage, ok := s.cachedDevinUsage(account.ID, force); ok {
			return usage, nil
		}

		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()

		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		} else if account.ProxyID != nil && s.accountRepo != nil {
			if fresh, err := s.accountRepo.GetByID(fetchCtx, account.ID); err == nil && fresh != nil && fresh.Proxy != nil {
				proxyURL = fresh.Proxy.URL()
			}
		}

		client, err := devin.NewClientWithTimeout(proxyURL, 20*time.Second)
		if err != nil {
			now := time.Now()
			degraded := &UsageInfo{UpdatedAt: &now, ErrorCode: "network_error", Error: err.Error()}
			s.cache.devinCache.Store(account.ID, &devinUsageCache{usageInfo: degraded, timestamp: now})
			return degraded, nil
		}
		client.SetServerBaseURL(account.GetDevinBaseURL())

		status, err := client.FetchUserStatus(fetchCtx, token, fmt.Sprintf("acct-%d", account.ID))
		if err != nil {
			now := time.Now()
			degraded := &UsageInfo{UpdatedAt: &now, Error: err.Error(), ErrorCode: "network_error"}
			var statusErr *devin.UserStatusError
			if errors.As(err, &statusErr) {
				switch statusErr.StatusCode {
				case http.StatusUnauthorized:
					degraded.NeedsReauth = true
					degraded.ErrorCode = "unauthenticated"
				case http.StatusForbidden:
					degraded.IsForbidden = true
					degraded.ErrorCode = "forbidden"
				case http.StatusTooManyRequests:
					degraded.ErrorCode = "rate_limited"
				}
			}
			s.cache.devinCache.Store(account.ID, &devinUsageCache{usageInfo: degraded, timestamp: now})
			return degraded, nil
		}

		now := time.Now()
		dailyUsed := clampDevinUsedPercent(100 - float64(status.DailyQuotaRemainingPercent))
		weeklyUsed := clampDevinUsedPercent(100 - float64(status.WeeklyQuotaRemainingPercent))

		dailyProgress := &UsageProgress{
			Utilization: dailyUsed,
		}
		if !status.DailyQuotaResetAt.IsZero() {
			t := status.DailyQuotaResetAt
			dailyProgress.ResetsAt = &t
			if rem := int(time.Until(t).Seconds()); rem > 0 {
				dailyProgress.RemainingSeconds = rem
			}
		}

		weeklyProgress := &UsageProgress{
			Utilization: weeklyUsed,
		}
		if !status.WeeklyQuotaResetAt.IsZero() {
			t := status.WeeklyQuotaResetAt
			weeklyProgress.ResetsAt = &t
			if rem := int(time.Until(t).Seconds()); rem > 0 {
				weeklyProgress.RemainingSeconds = rem
			}
		}

		usage := &UsageInfo{
			Source:       "active",
			UpdatedAt:    &now,
			DevinDaily:   dailyProgress,
			DevinWeekly:  weeklyProgress,
			DevinPlan:    status.Plan,
			DevinEmail:   status.Email,
			DevinOrgName: status.OrgName,
		}

		// Persist quota reset timestamps and plan metadata to account.Extra for cooldown resolution.
		if s.accountRepo != nil && account.ID > 0 {
			updates := map[string]any{
				"devin_daily_quota_remaining_percent":  status.DailyQuotaRemainingPercent,
				"devin_weekly_quota_remaining_percent": status.WeeklyQuotaRemainingPercent,
				"devin_quota_updated_at":               now.UTC().Format(time.RFC3339),
			}
			if status.Plan != "" {
				updates["devin_plan"] = status.Plan
			}
			if status.Email != "" {
				updates["email"] = status.Email
				updates["devin_email"] = status.Email
			}
			if status.OrgName != "" {
				updates["devin_org_name"] = status.OrgName
			}
			if !status.DailyQuotaResetAt.IsZero() {
				updates["devin_daily_quota_reset_at"] = status.DailyQuotaResetAt.UTC().Format(time.RFC3339)
			}
			if !status.WeeklyQuotaResetAt.IsZero() {
				updates["devin_weekly_quota_reset_at"] = status.WeeklyQuotaResetAt.UTC().Format(time.RFC3339)
			}
			if err := s.accountRepo.UpdateExtra(fetchCtx, account.ID, updates); err != nil {
				slog.Warn("devin_usage_persist_extra_failed", "account_id", account.ID, "error", err)
			}
		}

		s.cache.devinCache.Store(account.ID, &devinUsageCache{usageInfo: usage, timestamp: now})
		return usage, nil
	})
	if flightErr != nil {
		return nil, flightErr
	}
	if usage, ok := result.(*UsageInfo); ok && usage != nil {
		return usage, nil
	}
	now := time.Now()
	return &UsageInfo{UpdatedAt: &now}, nil
}

func clampDevinUsedPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
