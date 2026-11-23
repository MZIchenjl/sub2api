package service

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const multiProviderModelsDevSample = `{
	"anthropic": {
		"id": "anthropic",
		"name": "Anthropic",
		"models": {
			"claude-opus-5": {
				"id": "claude-opus-5",
				"name": "Claude Opus 5",
				"family": "claude-opus",
				"release_date": "2026-08-01",
				"canonical_model_id": "anthropic/claude-opus-5",
				"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
			},
			"claude-sonnet-4-6": {
				"id": "claude-sonnet-4-6",
				"name": "Claude Sonnet 4.6",
				"family": "claude-sonnet",
				"release_date": "2026-02-17",
				"canonical_model_id": "anthropic/claude-sonnet-4-6",
				"cost": {
					"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75,
					"tiers": [{"input": 6, "output": 22.5, "tier": {"type": "context", "size": 200000}}]
				}
			}
		}
	},
	"openrouter": {
		"id": "openrouter",
		"name": "OpenRouter",
		"models": {
			"anthropic/claude-opus-5": {
				"id": "anthropic/claude-opus-5",
				"name": "Anthropic: Claude Opus 5",
				"release_date": "2026-08-01",
				"canonical_model_id": "anthropic/claude-opus-5",
				"cost": {"input": 5.5, "output": 27.5, "cache_read": 0.55, "cache_write": 6.875}
			}
		}
	},
	"amazon-bedrock": {
		"id": "amazon-bedrock",
		"name": "Amazon Bedrock",
		"models": {
			"global.anthropic.claude-opus-5-v1:0": {
				"id": "global.anthropic.claude-opus-5-v1:0",
				"name": "Claude Opus 5 (Bedrock Global)",
				"release_date": "2026-08-01",
				"canonical_model_id": "anthropic/claude-opus-5",
				"cost": {"input": 6, "output": 30, "cache_read": 0.6, "cache_write": 7.5}
			}
		}
	},
	"google-vertex-anthropic": {
		"id": "google-vertex-anthropic",
		"name": "Vertex AI Anthropic",
		"models": {
			"claude-opus-5@default": {
				"id": "claude-opus-5@default",
				"name": "Claude Opus 5 (Vertex)",
				"release_date": "2026-08-01",
				"canonical_model_id": "anthropic/claude-opus-5",
				"cost": {"input": 5.2, "output": 26, "cache_read": 0.52, "cache_write": 6.5}
			}
		}
	},
	"openai": {
		"id": "openai",
		"name": "OpenAI",
		"models": {
			"gpt-6-astra": {
				"id": "gpt-6-astra",
				"name": "GPT-6 Astra",
				"release_date": "2026-09-01",
				"canonical_model_id": "openai/gpt-6-astra",
				"cost": {
					"input": 10, "output": 50, "cache_read": 1, "cache_write": 12.5,
					"tiers": [{"input": 20, "output": 75, "tier": {"type": "context", "size": 272000}}]
				}
			}
		}
	},
	"google": {
		"id": "google",
		"name": "Google",
		"models": {
			"gemini-4-carbon": {
				"id": "gemini-4-carbon",
				"name": "Gemini 4 Carbon",
				"release_date": "2026-07-01",
				"canonical_model_id": "google/gemini-4-carbon",
				"cost": {"input": 2, "output": 10, "cache_read": 0.1}
			}
		}
	}
}`

type inMemorySettingRepo struct {
	mu     sync.RWMutex
	values map[string]string
}

func newInMemorySettingRepo() *inMemorySettingRepo {
	return &inMemorySettingRepo{values: make(map[string]string)}
}

func (r *inMemorySettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: v}, nil
}

func (r *inMemorySettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	s, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return s.Value, nil
}

func (r *inMemorySettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *inMemorySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r *inMemorySettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range settings {
		r.values[k] = v
	}
	return nil
}

func (r *inMemorySettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

func (r *inMemorySettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func TestNormalizeOriginalModelID(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"claude-opus-5", "claude-opus-5"},
		{"anthropic/claude-opus-5", "claude-opus-5"},
		{"global.anthropic.claude-opus-5-v1:0", "claude-opus-5"},
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", "claude-haiku-4-5-20251001"},
		{"claude-opus-5@default", "claude-opus-5"},
		{"claude-sonnet-4-5@20250929", "claude-sonnet-4-5-20250929"},
		{"deepseek-ai/DeepSeek-V4-Pro", "deepseek-v4-pro"},
		{"~openai/gpt-astra-latest", "gpt-astra-latest"},
		{"deepseek/deepseek-v4-pro:free", "deepseek-v4-pro"},
		{"kimi-k3.1-pro-highspeed", "kimi-k3.1-pro-highspeed"},
		{"claude-sonnet-4-6[1m]", "claude-sonnet-4-6"},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, NormalizeOriginalModelID(tc.raw), tc.raw)
	}
}

func TestModelsDevGroupingAndProviderSwitchingAndGroupOverrides(t *testing.T) {
	ctx := context.Background()
	repo := newInMemorySettingRepo()
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           t.TempDir(),
			ModelsDevURL:      DefaultModelsDevURL,
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, nil)
	svc.SetSettingRepository(repo)
	require.NoError(t, svc.LoadModelsDevJSON([]byte(multiProviderModelsDevSample)))

	// 1. All 4 providers for claude-opus-5 must group under original model ID "claude-opus-5",
	// with first-party "anthropic" auto-selected as default.
	list := svc.ListModelPricingCatalog(ModelPricingListQuery{Search: "claude-opus-5", Page: 1, PageSize: 20})
	require.Equal(t, 1, list.Total)
	item := list.Items[0]
	require.Equal(t, "claude-opus-5", item.ModelID)
	require.Equal(t, 4, item.CandidateCount)
	require.Equal(t, "anthropic", item.ActiveProviderID)
	require.Equal(t, "default_auto", item.Source)
	require.InDelta(t, 5.0, item.EffectivePricing.InputPerMillion, 1e-9)

	// 2. Default lookup uses Anthropic ($5/$25) and extracts long-context ladder for sonnet 4.6
	opus := svc.GetModelPricing("claude-opus-5")
	require.NotNil(t, opus)
	require.InDelta(t, 5e-6, opus.InputCostPerToken, 1e-12)
	sonnet := svc.GetModelPricing("claude-sonnet-4-6")
	require.NotNil(t, sonnet)
	require.Equal(t, 200000, sonnet.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, sonnet.LongContextInputCostMultiplier, 1e-9)
	require.InDelta(t, 1.5, sonnet.LongContextOutputCostMultiplier, 1e-9)

	// 3. Switch cached provider in Default scope to OpenRouter ($5.5/$27.5)
	orKey := makeCandidateKey("openrouter", "anthropic/claude-opus-5")
	require.NoError(t, svc.SelectCachedProvider(ctx, nil, "claude-opus-5", orKey))
	opus = svc.GetModelPricing("claude-opus-5")
	require.InDelta(t, 5.5e-6, opus.InputCostPerToken, 1e-12)
	require.InDelta(t, 27.5e-6, opus.OutputCostPerToken, 1e-12)

	// 4. Per-group pricing:
	//    - Group 10 switches cached provider to Amazon Bedrock ($6/$30)
	//    - Group 20 edits custom price ($2/$10)
	//    - Group 30 has NO override -> MUST fall back to Default price ($5.5/$27.5)!
	g10 := int64(10)
	g20 := int64(20)
	g30 := int64(30)

	bedrockKey := makeCandidateKey("amazon-bedrock", "global.anthropic.claude-opus-5-v1:0")
	require.NoError(t, svc.SelectCachedProvider(ctx, &g10, "claude-opus-5", bedrockKey))
	require.NoError(t, svc.UpsertCustomPricing(ctx, &g20, "claude-opus-5", TokenPricingPerMillion{
		InputPerMillion:      2,
		OutputPerMillion:     10,
		CacheReadPerMillion:  0.2,
		CacheWritePerMillion: 2.5,
	}))

	// Verify BillingService + ModelPricingResolver end-to-end resolution
	billingSvc := NewBillingService(&config.Config{}, svc)
	resolver := NewModelPricingResolver(nil, billingSvc)

	res10 := resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", GroupID: &g10})
	require.Equal(t, PricingSourceGroup, res10.Source)
	require.InDelta(t, 6e-6, res10.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 30e-6, res10.BasePricing.OutputPricePerToken, 1e-12)

	res20 := resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", GroupID: &g20})
	require.Equal(t, PricingSourceGroup, res20.Source)
	require.InDelta(t, 2e-6, res20.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 10e-6, res20.BasePricing.OutputPricePerToken, 1e-12)

	res30 := resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", GroupID: &g30})
	require.Equal(t, PricingSourceModelsDev, res30.Source)
	require.InDelta(t, 5.5e-6, res30.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 27.5e-6, res30.BasePricing.OutputPricePerToken, 1e-12)

	// Verify UI list view for Group 30 shows inherited_default, while Group 20 shows group_custom
	listG30 := svc.ListModelPricingCatalog(ModelPricingListQuery{GroupID: g30, Search: "claude-opus-5"})
	require.Equal(t, "inherited_default", listG30.Items[0].Source)
	require.False(t, listG30.Items[0].IsGroupOverridden)
	listG20 := svc.ListModelPricingCatalog(ModelPricingListQuery{GroupID: g20, Search: "claude-opus-5"})
	require.Equal(t, "group_custom", listG20.Items[0].Source)
	require.True(t, listG20.Items[0].IsGroupOverridden)
	require.InDelta(t, 2.0, listG20.Items[0].EffectivePricing.InputPerMillion, 1e-9)
	require.NotNil(t, listG20.Items[0].DefaultPricing)
	require.InDelta(t, 5.5, listG20.Items[0].DefaultPricing.InputPerMillion, 1e-9)

	// 5. Reset Group 20 override -> immediately falls back to Default price ($5.5/$27.5)
	require.NoError(t, svc.ResetModelPricing(ctx, &g20, "claude-opus-5"))
	res20After := resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", GroupID: &g20})
	require.Equal(t, PricingSourceModelsDev, res20After.Source)
	require.InDelta(t, 5.5e-6, res20After.BasePricing.InputPricePerToken, 1e-12)

	// 6. Add custom-only private model (e.g. Devin swe-2)
	require.NoError(t, svc.UpsertCustomPricing(ctx, nil, "swe-2", TokenPricingPerMillion{
		DisplayName:          "Devin SWE-2",
		InputPerMillion:      1.5,
		OutputPerMillion:     6,
		CacheReadPerMillion:  0.15,
		CacheWritePerMillion: 1.875,
	}))
	swe := svc.GetModelPricing("swe-2")
	require.NotNil(t, swe)
	require.InDelta(t, 1.5e-6, swe.InputCostPerToken, 1e-12)

	// 7. Reload persisted state into a fresh PricingService instance -> overrides & selections survive restart
	svc2 := NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	svc2.SetSettingRepository(repo)
	svc2.loadPersistedState(ctx)
	require.NoError(t, svc2.LoadModelsDevJSON([]byte(multiProviderModelsDevSample)))
	require.InDelta(t, 5.5e-6, svc2.GetModelPricing("claude-opus-5").InputCostPerToken, 1e-12)
	g10Price, ok := svc2.GetModelPricingForGroup(g10, "claude-opus-5")
	require.True(t, ok)
	require.InDelta(t, 6e-6, g10Price.InputCostPerToken, 1e-12)
	require.NotNil(t, svc2.GetModelPricing("swe-2"))
}

func TestListModelNamesByProvider_ModelsDev(t *testing.T) {
	svc := newStubPricingServiceFromJSON(t, multiProviderModelsDevSample)
	require.Equal(t, []string{"claude-opus-5", "claude-sonnet-4-6"}, svc.ListModelNamesByProvider("anthropic"))
	require.Equal(t, []string{"gpt-6-astra"}, svc.ListModelNamesByProvider("OPENAI"))
	require.Equal(t, []string{"gemini-4-carbon"}, svc.ListModelNamesByProvider("google"))
	require.Equal(t, []string{"gemini-4-carbon"}, svc.ListModelNamesByProvider("gemini"))
	require.Empty(t, svc.ListModelNamesByProvider("nonexistent"))
}

func TestPricingServiceURLValidation(t *testing.T) {
	svc := NewPricingService(&config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:      true,
				PricingHosts: []string{"models.dev"},
			},
		},
	}, nil)

	_, err := svc.UpdateSyncConfig(context.Background(), PricingSyncConfig{
		AutoSyncEnabled:   true,
		SyncIntervalHours: 24,
		ModelsDevURL:      "https://evil.example.com/api.json",
	})
	require.Error(t, err)

	status, err := svc.UpdateSyncConfig(context.Background(), PricingSyncConfig{
		AutoSyncEnabled:   true,
		SyncIntervalHours: 12,
		ModelsDevURL:      "https://models.dev/api.json",
	})
	require.NoError(t, err)
	require.Equal(t, 12, status.Config.SyncIntervalHours)
}
