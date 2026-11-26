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

type memoryModelPricingRepo struct {
	mu           sync.Mutex
	catalog      []ModelPricingCatalogRecord
	overrides    map[ModelPricingOverrideKey]ModelPricingOverrideRecord
	replaceErr   error
	upsertErr    error
	deleteErr    error
	replaceCalls int
}

func newMemoryModelPricingRepo() *memoryModelPricingRepo {
	return &memoryModelPricingRepo{
		overrides: make(map[ModelPricingOverrideKey]ModelPricingOverrideRecord),
	}
}

func (r *memoryModelPricingRepo) seedFromJSON(t *testing.T, body []byte, syncedAt time.Time) {
	t.Helper()
	recs, err := extractSupportedModelsDevRecords(body, syncedAt)
	require.NoError(t, err)
	r.mu.Lock()
	r.catalog = recs
	r.mu.Unlock()
}

func (r *memoryModelPricingRepo) ListCatalogEntries(_ context.Context) ([]ModelPricingCatalogRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModelPricingCatalogRecord, len(r.catalog))
	copy(out, r.catalog)
	return out, nil
}

func (r *memoryModelPricingRepo) ListOverrides(_ context.Context) ([]ModelPricingOverrideRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModelPricingOverrideRecord, 0, len(r.overrides))
	for _, rec := range r.overrides {
		out = append(out, rec)
	}
	return out, nil
}

func (r *memoryModelPricingRepo) ReplaceCatalogAndReconcileOverrides(_ context.Context, entries []ModelPricingCatalogRecord, deleteOverrideKeys []ModelPricingOverrideKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replaceCalls++
	if r.replaceErr != nil {
		return r.replaceErr
	}
	copied := make([]ModelPricingCatalogRecord, len(entries))
	copy(copied, entries)
	r.catalog = copied
	for _, k := range deleteOverrideKeys {
		delete(r.overrides, k)
	}
	return nil
}

func (r *memoryModelPricingRepo) UpsertOverride(_ context.Context, rec ModelPricingOverrideRecord, deleteKeys ...ModelPricingOverrideKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.upsertErr != nil {
		return r.upsertErr
	}
	for _, k := range deleteKeys {
		delete(r.overrides, k)
	}
	r.overrides[ModelPricingOverrideKey{
		Scope:   rec.Scope,
		ScopeID: rec.ScopeID,
		ModelID: rec.ModelID,
	}] = rec
	return nil
}

func (r *memoryModelPricingRepo) DeleteOverrides(_ context.Context, keys ...ModelPricingOverrideKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	for _, k := range keys {
		delete(r.overrides, k)
	}
	return nil
}

// 数据库缓存存在且已过期时，启动不得阻塞在远程 models.dev 拉取上，DB 中的定价启动即可用。
func TestPricingStartupDoesNotBlockOnRemoteModelsDevSync(t *testing.T) {
	remote := &blockingModelsDevClient{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(remote.release) })

	repo := newMemoryModelPricingRepo()
	repo.seedFromJSON(t, []byte(testStartupModelsDevJSON), time.Now().Add(-2*time.Hour))

	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   true,
			SyncIntervalHours: 1,
		},
	}, remote)
	svc.SetModelPricingRepository(repo)

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
		t.Fatal("stale DB catalog must trigger background models.dev sync")
	}
}

// 后台同步成功后更新内存和数据库表，且不再在 DataDir 写入磁盘文件。
func TestPricingStartupBackgroundSyncUpdatesDatabaseWithoutDiskFiles(t *testing.T) {
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
	repo := newMemoryModelPricingRepo()
	dir := t.TempDir()

	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           dir,
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   true,
			SyncIntervalHours: 24,
		},
	}, remote)
	svc.SetModelPricingRepository(repo)

	require.NoError(t, svc.Initialize())
	t.Cleanup(func() { svc.Stop() })

	require.Eventually(t, func() bool {
		p := svc.GetModelPricing("claude-opus-5")
		return p != nil && p.InputCostPerToken == 4e-6
	}, 2*time.Second, 10*time.Millisecond)

	// Persisted into DB repository
	rows, err := repo.ListCatalogEntries(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "anthropic", rows[0].ChannelID)
	require.Equal(t, "claude-opus-5", rows[0].ModelID)
	require.NotNil(t, rows[0].InputPerMillion)
	require.InDelta(t, 4.0, *rows[0].InputPerMillion, 1e-9)

	// No disk cache files created
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// 同步失败时保留已有数据库目录并记录错误状态。
func TestPricingSyncFailureKeepsExistingDatabaseCatalog(t *testing.T) {
	remote := &stubModelsDevClient{err: errors.New("network unreachable")}
	repo := newMemoryModelPricingRepo()
	repo.seedFromJSON(t, []byte(testStartupModelsDevJSON), time.Now())

	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, remote)
	svc.SetModelPricingRepository(repo)

	require.NoError(t, svc.Initialize())
	t.Cleanup(func() { svc.Stop() })

	status, err := svc.SyncNow(context.Background())
	require.Error(t, err)
	require.NotNil(t, status)
	require.Equal(t, PricingSyncStatusFailed, status.Meta.LastSyncStatus)
	require.Contains(t, status.Meta.LastSyncError, "network unreachable")

	p := svc.GetModelPricing("claude-opus-5")
	require.NotNil(t, p)
	require.InDelta(t, 5e-6, p.InputCostPerToken, 1e-12)
}

// 完整验证 DB 持久化闭环：多渠道/Bedrock 区域价、默认/渠道/分组改价与锁、重启恢复与同步对账。
func TestPricingDatabasePersistenceAcrossRestartAndReconcile(t *testing.T) {
	ctx := context.Background()
	initialJSON := `{
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
		},
		"amazon-bedrock": {
			"id": "amazon-bedrock",
			"name": "AWS Bedrock",
			"models": {
				"global.anthropic.claude-opus-5-v1:0": {
					"id": "global.anthropic.claude-opus-5-v1:0",
					"name": "Claude Opus 5 Global",
					"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
				},
				"us.anthropic.claude-opus-5-v1:0": {
					"id": "us.anthropic.claude-opus-5-v1:0",
					"name": "Claude Opus 5 US",
					"cost": {"input": 5.5, "output": 27.5, "cache_read": 0.55, "cache_write": 6.875}
				}
			}
		}
	}`

	remote := &stubModelsDevClient{body: []byte(initialJSON)}
	repo := newMemoryModelPricingRepo()

	svc1 := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, remote)
	svc1.SetModelPricingRepository(repo)
	require.NoError(t, svc1.Initialize())
	t.Cleanup(func() { svc1.Stop() })

	_, err := svc1.SyncNow(ctx)
	require.NoError(t, err)

	// Edit & freeze Bedrock channel, edit unfrozen Default, edit Group 7 override
	freezeTrue := true
	freezeFalse := false
	gid := int64(7)
	require.NoError(t, svc1.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ChannelID: "amazon-bedrock",
		ModelID:   "claude-opus-5",
		Custom:    TokenPricingPerMillion{InputPerMillion: 4.8, OutputPerMillion: 24},
		Frozen:    &freezeTrue,
	}))
	require.NoError(t, svc1.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ModelID: "claude-opus-5",
		Custom:  TokenPricingPerMillion{InputPerMillion: 4.2, OutputPerMillion: 21},
		Frozen:  &freezeFalse,
	}))
	require.NoError(t, svc1.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		GroupID: &gid,
		ModelID: "claude-opus-5",
		Custom:  TokenPricingPerMillion{InputPerMillion: 2.5, OutputPerMillion: 12.5},
	}))

	// Simulate fresh process restart backed only by the DB repository
	svc2 := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			ModelsDevURL:      "https://models.dev/api.json",
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, remote)
	svc2.SetModelPricingRepository(repo)
	require.NoError(t, svc2.Initialize())
	t.Cleanup(func() { svc2.Stop() })

	// Default custom, Bedrock frozen custom, and Group 7 custom all restored cleanly
	pDef := svc2.GetModelPricing("claude-opus-5")
	require.NotNil(t, pDef)
	require.InDelta(t, 4.2e-6, pDef.InputCostPerToken, 1e-12)

	pBedrock, ok := svc2.GetModelPricingForProvider("amazon-bedrock", "claude-opus-5", "us.anthropic.claude-opus-5-v1:0")
	require.True(t, ok)
	require.InDelta(t, 4.8e-6, pBedrock.InputCostPerToken, 1e-12)

	pGrp, ok := svc2.GetModelPricingForGroup(gid, "claude-opus-5")
	require.True(t, ok)
	require.InDelta(t, 2.5e-6, pGrp.InputCostPerToken, 1e-12)

	// Re-sync with skip_frozen: unfrozen default cleared, frozen Bedrock & Group 7 kept
	updatedJSON := `{
		"anthropic": {
			"id": "anthropic",
			"name": "Anthropic",
			"models": {
				"claude-opus-5": {
					"id": "claude-opus-5",
					"name": "Claude Opus 5",
					"cost": {"input": 6, "output": 30, "cache_read": 0.6, "cache_write": 7.5}
				}
			}
		},
		"amazon-bedrock": {
			"id": "amazon-bedrock",
			"name": "AWS Bedrock",
			"models": {
				"global.anthropic.claude-opus-5-v1:0": {
					"id": "global.anthropic.claude-opus-5-v1:0",
					"name": "Claude Opus 5 Global",
					"cost": {"input": 6, "output": 30, "cache_read": 0.6, "cache_write": 7.5}
				},
				"us.anthropic.claude-opus-5-v1:0": {
					"id": "us.anthropic.claude-opus-5-v1:0",
					"name": "Claude Opus 5 US",
					"cost": {"input": 6.6, "output": 33, "cache_read": 0.66, "cache_write": 8.25}
				}
			}
		}
	}`
	remote.body = []byte(updatedJSON)

	status, err := svc2.SyncNowWithMode(ctx, PricingSyncOverwriteSkipFrozen)
	require.NoError(t, err)
	require.Equal(t, 1, status.Meta.LastSkippedFrozen)
	require.Equal(t, 1, status.Meta.LastOverwrittenCount)

	// Default reverted to freshly synced 6.0; Bedrock stayed frozen at 4.8; Group 7 stayed at 2.5
	require.InDelta(t, 6.0e-6, svc2.GetModelPricing("claude-opus-5").InputCostPerToken, 1e-12)
	pBedrockAfter, ok := svc2.GetModelPricingForProvider("amazon-bedrock", "claude-opus-5", "us.anthropic.claude-opus-5-v1:0")
	require.True(t, ok)
	require.InDelta(t, 4.8e-6, pBedrockAfter.InputCostPerToken, 1e-12)
	pGrpAfter, ok := svc2.GetModelPricingForGroup(gid, "claude-opus-5")
	require.True(t, ok)
	require.InDelta(t, 2.5e-6, pGrpAfter.InputCostPerToken, 1e-12)

	// Unfreeze & reset Bedrock -> falls back to exact Bedrock regional US price (6.6)
	require.NoError(t, svc2.ResetModelPricingWithScope(ctx, nil, "amazon-bedrock", "claude-opus-5"))
	pBedrockUS, ok := svc2.GetModelPricingForProvider("amazon-bedrock", "claude-opus-5", "us.anthropic.claude-opus-5-v1:0")
	require.True(t, ok)
	require.InDelta(t, 6.6e-6, pBedrockUS.InputCostPerToken, 1e-12)

	// DB persistence error on sync must not corrupt in-memory catalog
	repo.replaceErr = errors.New("db write failed")
	_, err = svc2.SyncNow(ctx)
	require.Error(t, err)
	require.InDelta(t, 6.0e-6, svc2.GetModelPricing("claude-opus-5").InputCostPerToken, 1e-12)
}
