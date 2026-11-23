package service

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const testStartupModelsDevJSON = `{
	"anthropic": {
		"id": "anthropic",
		"name": "Anthropic",
		"models": {
			"claude-opus-5": {
				"id": "claude-opus-5",
				"name": "Claude Opus 5",
				"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
			}
		}
	}
}`

type blockingModelsDevClient struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingModelsDevClient) FetchPricingJSON(ctx context.Context, _ string) ([]byte, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type stubModelsDevClient struct {
	mu    sync.Mutex
	body  []byte
	err   error
	calls int
}

func (c *stubModelsDevClient) FetchPricingJSON(_ context.Context, _ string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.body, nil
}

func (c *stubModelsDevClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// 本地缓存存在且已过期时，启动不得阻塞在远程 models.dev 拉取上，本地缓存启动即可用。
func TestPricingStartupDoesNotBlockOnRemoteModelsDevSync(t *testing.T) {
	remote := &blockingModelsDevClient{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(remote.release) })

	dir := t.TempDir()
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           dir,
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   true,
			SyncIntervalHours: 1,
		},
	}, remote)

	cachePath := svc.getCatalogCachePath()
	require.NoError(t, os.WriteFile(cachePath, []byte(testStartupModelsDevJSON), 0644))
	// Make cache stale so background refresh triggers
	oldTime := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(cachePath, oldTime, oldTime))

	done := make(chan error, 1)
	go func() { done <- svc.Initialize() }()
	t.Cleanup(func() { svc.Stop() })

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("startup must not block on remote models.dev sync")
	}

	p := svc.GetModelPricing("claude-opus-5")
	require.NotNil(t, p)
	require.InDelta(t, 5e-6, p.InputCostPerToken, 1e-12)

	select {
	case <-remote.started:
	case <-time.After(2 * time.Second):
		t.Fatal("stale cache must trigger background models.dev sync")
	}
}

// 后台同步成功后更新内存和本地磁盘缓存。
func TestPricingStartupBackgroundSyncUpdatesCache(t *testing.T) {
	updatedJSON := `{
		"anthropic": {
			"id": "anthropic",
			"name": "Anthropic",
			"models": {
				"claude-opus-5": {
					"id": "claude-opus-5",
					"name": "Claude Opus 5",
					"cost": {"input": 4, "output": 20, "cache_read": 0.4, "cache_write": 5}
				}
			}
		}
	}`
	remote := &stubModelsDevClient{body: []byte(updatedJSON)}
	dir := t.TempDir()
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           dir,
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   true,
			SyncIntervalHours: 24,
		},
	}, remote)

	require.NoError(t, svc.Initialize())
	t.Cleanup(func() { svc.Stop() })

	require.Eventually(t, func() bool {
		p := svc.GetModelPricing("claude-opus-5")
		return p != nil && p.InputCostPerToken == 4e-6
	}, 2*time.Second, 10*time.Millisecond)

	saved, err := os.ReadFile(svc.getCatalogCachePath())
	require.NoError(t, err)
	require.Equal(t, updatedJSON, string(saved))
}

// 同步失败时保留已有本地缓存并记录错误状态。
func TestPricingSyncFailureKeepsExistingCache(t *testing.T) {
	remote := &stubModelsDevClient{err: errors.New("network unreachable")}
	dir := t.TempDir()
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           dir,
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, remote)

	require.NoError(t, os.WriteFile(svc.getCatalogCachePath(), []byte(testStartupModelsDevJSON), 0644))
	require.NoError(t, svc.Initialize())
	t.Cleanup(func() { svc.Stop() })

	status, err := svc.SyncNow(context.Background())
	require.Error(t, err)
	require.NotNil(t, status)
	require.Equal(t, PricingSyncStatusFailed, status.Meta.LastSyncStatus)
	require.Contains(t, status.Meta.LastSyncError, "network unreachable")

	// Cached data remains available
	p := svc.GetModelPricing("claude-opus-5")
	require.NotNil(t, p)
	require.InDelta(t, 5e-6, p.InputCostPerToken, 1e-12)
}
