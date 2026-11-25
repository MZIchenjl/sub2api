package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const supportedModelsDevSample = `{
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
				"cost": {"input": 5.5, "output": 27.5}
			},
			"random/unrelated-model": {
				"id": "random/unrelated-model",
				"name": "Unrelated Third-Party Model",
				"cost": {"input": 1, "output": 2}
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
				"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
			},
			"us.anthropic.claude-opus-5-v1:0": {
				"id": "us.anthropic.claude-opus-5-v1:0",
				"name": "Claude Opus 5 (Bedrock US Regional)",
				"cost": {"input": 5.5, "output": 27.5, "cache_read": 0.55, "cache_write": 6.875}
			},
			"amazon.nova-pro-v1:0": {
				"id": "amazon.nova-pro-v1:0",
				"name": "Amazon Nova Pro (Non-Claude, should be filtered out)",
				"cost": {"input": 0.8, "output": 3.2}
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
				"cost": {"input": 2, "output": 10, "cache_read": 0.1}
			}
		}
	},
	"google-vertex": {
		"id": "google-vertex",
		"name": "Vertex AI",
		"models": {
			"gemini-4-carbon": {
				"id": "gemini-4-carbon",
				"name": "Gemini 4 Carbon (Vertex)",
				"cost": {"input": 2.2, "output": 11, "cache_read": 0.11}
			},
			"meta/llama-4-scout": {
				"id": "meta/llama-4-scout",
				"name": "Llama 4 (Non-Gemini, should be filtered out)",
				"cost": {"input": 0.2, "output": 0.4}
			}
		}
	},
	"deepseek": {
		"id": "deepseek",
		"name": "DeepSeek",
		"models": {
			"deepseek-v4-pro": {
				"id": "deepseek-v4-pro",
				"name": "DeepSeek V4 Pro",
				"cost": {"input": 0.66, "output": 1.98, "cache_read": 0.022}
			}
		}
	},
	"opencode-go": {
		"id": "opencode-go",
		"name": "OpenCode Go",
		"models": {
			"deepseek-v4-pro": {
				"id": "deepseek-v4-pro",
				"name": "DeepSeek V4 Pro (OpenCode Go)",
				"cost": {"input": 0.9, "output": 2.7, "cache_read": 0.03}
			},
			"beam-free": {
				"id": "beam-free",
				"name": "Beam Free (should be filtered out)",
				"cost": {"input": 0, "output": 0}
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

type stubSyncClient struct {
	body []byte
}

func (c *stubSyncClient) FetchPricingJSON(_ context.Context, _ string) ([]byte, error) {
	return c.body, nil
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

func TestModelsDevSyncFiltersOnlySupportedModelsAndSavesSlimCache(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           dataDir,
			ModelsDevURL:      DefaultModelsDevURL,
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, &stubSyncClient{body: []byte(supportedModelsDevSample)})

	status, err := svc.SyncNow(ctx)
	require.NoError(t, err)
	require.True(t, status.CacheLoaded)
	// Supported models: claude-opus-5, claude-sonnet-4-6, gpt-6-astra, gemini-4-carbon, deepseek-v4-pro = 5 models.
	// Unrelated openrouter/random, bedrock amazon.nova-pro, vertex llama-4, opencode beam-free MUST be filtered out!
	require.Equal(t, 5, status.Meta.ModelCount)
	require.Nil(t, svc.GetModelPricing("unrelated-model"))
	require.Nil(t, svc.GetModelPricing("nova-pro"))
	require.Nil(t, svc.GetModelPricing("llama-4-scout"))
	require.Nil(t, svc.GetModelPricing("beam-free"))

	// Slim disk cache must NOT contain unrelated providers/models
	diskBytes, err := os.ReadFile(filepath.Join(dataDir, modelsDevCatalogFileName))
	require.NoError(t, err)
	require.NotContains(t, string(diskBytes), "openrouter")
	require.NotContains(t, string(diskBytes), "nova-pro")
	require.NotContains(t, string(diskBytes), "llama-4-scout")
	require.Contains(t, string(diskBytes), "amazon-bedrock")
	require.Contains(t, string(diskBytes), "google-vertex-anthropic")
}

func TestAccountTypeMatchingAndThreeTierPriority(t *testing.T) {
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
	require.NoError(t, svc.LoadModelsDevJSON([]byte(supportedModelsDevSample)))

	billingSvc := NewBillingService(&config.Config{}, svc)
	resolver := NewModelPricingResolver(nil, billingSvc)

	bedrockAcc := &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}
	vertexClaudeAcc := &Account{Platform: PlatformAnthropic, Type: AccountTypeServiceAccount}
	anthropicOAuthAcc := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	vertexGeminiAcc := &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount}
	googleAPIKeyAcc := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}
	openCodeAcc := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}

	// Verify ResolveAccountPricingProvider mappings
	require.Equal(t, "amazon-bedrock", ResolveAccountPricingProvider(bedrockAcc, "claude-opus-5"))
	require.Equal(t, "google-vertex-anthropic", ResolveAccountPricingProvider(vertexClaudeAcc, "claude-opus-5"))
	require.Equal(t, "anthropic", ResolveAccountPricingProvider(anthropicOAuthAcc, "claude-opus-5"))
	require.Equal(t, "google-vertex", ResolveAccountPricingProvider(vertexGeminiAcc, "gemini-4-carbon"))
	require.Equal(t, "google", ResolveAccountPricingProvider(googleAPIKeyAcc, "gemini-4-carbon"))
	require.Equal(t, "opencode-go", ResolveAccountPricingProvider(openCodeAcc, "deepseek-v4-pro"))

	// Priority 2 (Account-type matched channel) > Priority 3 (Default channel):
	// 1. Bedrock US regional raw model ID -> $5.5/$27.5
	resBedrockUS := resolver.Resolve(ctx, PricingInput{
		Model:         "claude-opus-5",
		Account:       bedrockAcc,
		UpstreamModel: "us.anthropic.claude-opus-5-v1:0",
	})
	require.Equal(t, PricingSourceAccountChannel, resBedrockUS.Source)
	require.Equal(t, "amazon-bedrock", resBedrockUS.MatchedChannelProvider)
	require.InDelta(t, 5.5e-6, resBedrockUS.BasePricing.InputPricePerToken, 1e-12)

	// 2. Bedrock global raw model ID -> $5/$25
	resBedrockGlobal := resolver.Resolve(ctx, PricingInput{
		Model:         "claude-opus-5",
		Account:       bedrockAcc,
		UpstreamModel: "global.anthropic.claude-opus-5-v1:0",
	})
	require.Equal(t, PricingSourceAccountChannel, resBedrockGlobal.Source)
	require.InDelta(t, 5e-6, resBedrockGlobal.BasePricing.InputPricePerToken, 1e-12)

	// 3. Vertex AI Anthropic -> $5.2/$26
	resVertexClaude := resolver.Resolve(ctx, PricingInput{
		Model:   "claude-opus-5",
		Account: vertexClaudeAcc,
	})
	require.Equal(t, PricingSourceAccountChannel, resVertexClaude.Source)
	require.Equal(t, "google-vertex-anthropic", resVertexClaude.MatchedChannelProvider)
	require.InDelta(t, 5.2e-6, resVertexClaude.BasePricing.InputPricePerToken, 1e-12)

	// 4. Vertex AI Gemini -> $2.2/$11 vs Google AI -> $2/$10
	resVertexGem := resolver.Resolve(ctx, PricingInput{Model: "gemini-4-carbon", Account: vertexGeminiAcc})
	require.Equal(t, PricingSourceAccountChannel, resVertexGem.Source)
	require.InDelta(t, 2.2e-6, resVertexGem.BasePricing.InputPricePerToken, 1e-12)

	resGoogleGem := resolver.Resolve(ctx, PricingInput{Model: "gemini-4-carbon", Account: googleAPIKeyAcc})
	require.InDelta(t, 2e-6, resGoogleGem.BasePricing.InputPricePerToken, 1e-12)

	// 5. OpenCode Go running deepseek-v4-pro uses opencode-go price ($0.9/$2.7), NOT clobbered by DeepSeek hardcoded policy!
	resOpenCodeDS := resolver.Resolve(ctx, PricingInput{Model: "deepseek-v4-pro", Account: openCodeAcc})
	require.Equal(t, PricingSourceAccountChannel, resOpenCodeDS.Source)
	require.Equal(t, "opencode-go", resOpenCodeDS.MatchedChannelProvider)
	require.InDelta(t, 0.9e-6, resOpenCodeDS.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 2.7e-6, resOpenCodeDS.BasePricing.OutputPricePerToken, 1e-12)

	// Priority 1 (Group custom price) > Priority 2 (Bedrock / Vertex / Channel) > Priority 3 (Default):
	g10 := int64(10)
	g20 := int64(20)
	require.NoError(t, svc.UpsertCustomPricing(ctx, &g10, "claude-opus-5", PlatformAnthropic, TokenPricingPerMillion{
		InputPerMillion:      2.5,
		OutputPerMillion:     12.5,
		CacheReadPerMillion:  0.25,
		CacheWritePerMillion: 3.125,
	}))
	// Even on Bedrock US account, Group 10 custom price ($2.5/$12.5) MUST win!
	resG10Bedrock := resolver.Resolve(ctx, PricingInput{
		Model:         "claude-opus-5",
		GroupID:       &g10,
		Account:       bedrockAcc,
		UpstreamModel: "us.anthropic.claude-opus-5-v1:0",
	})
	require.Equal(t, PricingSourceGroup, resG10Bedrock.Source)
	require.InDelta(t, 2.5e-6, resG10Bedrock.BasePricing.InputPricePerToken, 1e-12)

	// Group 20 has NO custom price -> falls back to Bedrock US channel ($5.5/$27.5)
	resG20Bedrock := resolver.Resolve(ctx, PricingInput{
		Model:         "claude-opus-5",
		GroupID:       &g20,
		Account:       bedrockAcc,
		UpstreamModel: "us.anthropic.claude-opus-5-v1:0",
	})
	require.Equal(t, PricingSourceAccountChannel, resG20Bedrock.Source)
	require.InDelta(t, 5.5e-6, resG20Bedrock.BasePricing.InputPricePerToken, 1e-12)

	// Default custom price applies when using the default channel (Anthropic OAuth),
	// while Bedrock still uses Bedrock channel price ($5.5) unless overridden at Group level!
	require.NoError(t, svc.UpsertCustomPricing(ctx, nil, "claude-opus-5", PlatformAnthropic, TokenPricingPerMillion{
		InputPerMillion:  4.0,
		OutputPerMillion: 20.0,
	}))
	resAnthropicDefaultCustom := resolver.Resolve(ctx, PricingInput{
		Model:   "claude-opus-5",
		GroupID: &g20,
		Account: anthropicOAuthAcc,
	})
	require.Equal(t, PricingSourceModelsDev, resAnthropicDefaultCustom.Source)
	require.InDelta(t, 4.0e-6, resAnthropicDefaultCustom.BasePricing.InputPricePerToken, 1e-12)

	// System Group platform scoping in ListModelPricingCatalog:
	// An Anthropic group sees only Anthropic models (claude-opus-5, claude-sonnet-4-6), not GPT/Gemini/DeepSeek!
	anthList := svc.ListModelPricingCatalog(ModelPricingListQuery{
		GroupID:       g10,
		GroupPlatform: PlatformAnthropic,
	})
	require.Equal(t, 2, anthList.Total)
	for _, it := range anthList.Items {
		require.Equal(t, PlatformAnthropic, it.Platform)
	}

	// Reset Group 10 override -> falls back to inherited_default
	require.NoError(t, svc.ResetModelPricing(ctx, &g10, "claude-opus-5"))
	resG10AfterReset := resolver.Resolve(ctx, PricingInput{
		Model:   "claude-opus-5",
		GroupID: &g10,
		Account: anthropicOAuthAcc,
	})
	require.Equal(t, PricingSourceModelsDev, resG10AfterReset.Source)
	require.InDelta(t, 4.0e-6, resG10AfterReset.BasePricing.InputPricePerToken, 1e-12)

	// Reload persisted state into a fresh PricingService instance -> overrides survive restart
	svc2 := NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	svc2.SetSettingRepository(repo)
	svc2.loadPersistedState(ctx)
	require.NoError(t, svc2.LoadModelsDevJSON([]byte(supportedModelsDevSample)))
	require.InDelta(t, 4.0e-6, svc2.GetModelPricing("claude-opus-5").InputCostPerToken, 1e-12)
}

func TestChannelEditingLockAndSyncOverwriteModes(t *testing.T) {
	ctx := context.Background()
	repo := newInMemorySettingRepo()
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
			"name": "Amazon Bedrock",
			"models": {
				"global.anthropic.claude-opus-5-v1:0": {
					"id": "global.anthropic.claude-opus-5-v1:0",
					"name": "Claude Opus 5 (Bedrock)",
					"cost": {"input": 5.5, "output": 27.5, "cache_read": 0.55, "cache_write": 6.875}
				}
			}
		},
		"google-vertex-anthropic": {
			"id": "google-vertex-anthropic",
			"name": "Vertex AI",
			"models": {
				"claude-opus-5@default": {
					"id": "claude-opus-5@default",
					"name": "Claude Opus 5 (Vertex)",
					"cost": {"input": 5.2, "output": 26, "cache_read": 0.52, "cache_write": 6.5}
				}
			}
		}
	}`
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
			"name": "Amazon Bedrock",
			"models": {
				"global.anthropic.claude-opus-5-v1:0": {
					"id": "global.anthropic.claude-opus-5-v1:0",
					"name": "Claude Opus 5 (Bedrock)",
					"cost": {"input": 7, "output": 35, "cache_read": 0.7, "cache_write": 8.75}
				}
			}
		},
		"google-vertex-anthropic": {
			"id": "google-vertex-anthropic",
			"name": "Vertex AI",
			"models": {
				"claude-opus-5@default": {
					"id": "claude-opus-5@default",
					"name": "Claude Opus 5 (Vertex)",
					"cost": {"input": 6.5, "output": 32.5, "cache_read": 0.65, "cache_write": 8.125}
				}
			}
		}
	}`

	client := &stubSyncClient{body: []byte(initialJSON)}
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           t.TempDir(),
			ModelsDevURL:      DefaultModelsDevURL,
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, client)
	svc.SetSettingRepository(repo)

	_, err := svc.SyncNow(ctx)
	require.NoError(t, err)

	billingSvc := NewBillingService(&config.Config{}, svc)
	resolver := NewModelPricingResolver(nil, billingSvc)
	bedrockAcc := &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}
	vertexAcc := &Account{Platform: PlatformAnthropic, Type: AccountTypeServiceAccount}
	anthAcc := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	// 1. Edit amazon-bedrock channel price (locked by default = true)
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ChannelID: "amazon-bedrock",
		ModelID:   "claude-opus-5",
		Custom:    TokenPricingPerMillion{InputPerMillion: 4.8, OutputPerMillion: 24.0},
	}))

	// 2. Lock google-vertex-anthropic without editing numbers (snapshot lock at $5.2/$26)
	require.NoError(t, svc.SetChannelPriceLock(ctx, "claude-opus-5", "google-vertex-anthropic", true))

	// 3. Edit anthropic default price with locked=false (unlocked custom -> will update on next sync)
	unlocked := false
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ChannelID: "anthropic",
		ModelID:   "claude-opus-5",
		Locked:    &unlocked,
		Custom:    TokenPricingPerMillion{InputPerMillion: 4.1, OutputPerMillion: 20.5},
	}))

	// 4. Add a system group override (group 77) -> must always survive sync and beat channel prices
	g77 := int64(77)
	require.NoError(t, svc.UpsertCustomPricing(ctx, &g77, "claude-opus-5", PlatformAnthropic, TokenPricingPerMillion{
		InputPerMillion: 2.0, OutputPerMillion: 10.0,
	}))

	// Check runtime priority before sync:
	// Group 77 on Bedrock -> $2.0 (Group wins)
	resG77 := resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", GroupID: &g77, Account: bedrockAcc})
	require.Equal(t, PricingSourceGroup, resG77.Source)
	require.InDelta(t, 2.0e-6, resG77.BasePricing.InputPricePerToken, 1e-12)

	// No group on Bedrock -> $4.8 (Custom Channel wins)
	resBedrock := resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", Account: bedrockAcc})
	require.Equal(t, PricingSourceAccountChannel, resBedrock.Source)
	require.InDelta(t, 4.8e-6, resBedrock.BasePricing.InputPricePerToken, 1e-12)

	// 5. Simulate auto-sync (skip_locked) with updated models.dev prices
	client.body = []byte(updatedJSON)
	status, err := svc.SyncNowWithMode(ctx, PricingSyncOverwriteSkipLocked)
	require.NoError(t, err)
	require.Equal(t, 2, status.Meta.LastSkippedLocked) // bedrock + vertex locked
	require.Equal(t, 1, status.Meta.LastOverwrittenCount) // unlocked anthropic custom updated

	// Bedrock stays $4.8, Vertex stays $5.2, Anthropic updates to $6.0!
	require.InDelta(t, 4.8e-6, resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", Account: bedrockAcc}).BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 5.2e-6, resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", Account: vertexAcc}).BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 6.0e-6, resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", Account: anthAcc}).BasePricing.InputPricePerToken, 1e-12)

	// 6. Unlock Vertex (snapshot lock) -> immediately reverts to latest synced $6.5!
	require.NoError(t, svc.SetChannelPriceLock(ctx, "claude-opus-5", "google-vertex-anthropic", false))
	require.InDelta(t, 6.5e-6, resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", Account: vertexAcc}).BasePricing.InputPricePerToken, 1e-12)

	// 7. Manual sync with overwrite_all -> overwrites even locked Bedrock back to $7.0, while Group 77 stays $2.0!
	status, err = svc.SyncNowWithMode(ctx, PricingSyncOverwriteAll)
	require.NoError(t, err)
	require.Equal(t, 1, status.Meta.LastOverwrittenCount)
	require.Equal(t, 0, status.LockedChannelCount)
	require.InDelta(t, 7.0e-6, resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", Account: bedrockAcc}).BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 2.0e-6, resolver.Resolve(ctx, PricingInput{Model: "claude-opus-5", GroupID: &g77, Account: bedrockAcc}).BasePricing.InputPricePerToken, 1e-12)

	// 8. Lock default channel directly with empty channelID -> freezes current default price ($6.0) across next sync
	require.NoError(t, svc.SetChannelPriceLock(ctx, "claude-opus-5", "", true))
	listAfterDefaultLock := svc.ListModelPricingCatalog(ModelPricingListQuery{Search: "claude-opus-5"})
	require.Len(t, listAfterDefaultLock.Items, 1)
	require.True(t, listAfterDefaultLock.Items[0].IsDefaultLocked)
	require.Equal(t, 1, listAfterDefaultLock.Items[0].LockedChannelCount)
	require.True(t, listAfterDefaultLock.Items[0].ChannelPrices[0].IsDefault)
	require.True(t, listAfterDefaultLock.Items[0].ChannelPrices[0].IsLocked)

	// Edit default channel with display_name, then edit via default channelID without display_name -> preserves display_name
	lockedTrue := true
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ModelID: "claude-opus-5",
		Locked:  &lockedTrue,
		Custom: TokenPricingPerMillion{
			DisplayName:      "Claude Opus 5 Custom",
			InputPerMillion:  6.2,
			OutputPerMillion: 31.0,
		},
	}))
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ChannelID: "anthropic",
		ModelID:   "claude-opus-5",
		Locked:    &lockedTrue,
		Custom: TokenPricingPerMillion{
			InputPerMillion:  6.4,
			OutputPerMillion: 32.0,
		},
	}))
	listAfterDefaultEdit := svc.ListModelPricingCatalog(ModelPricingListQuery{Search: "claude-opus-5"})
	require.Equal(t, "Claude Opus 5 Custom", listAfterDefaultEdit.Items[0].DisplayName)
	require.InDelta(t, 6.4, listAfterDefaultEdit.Items[0].EffectivePricing.InputPerMillion, 1e-9)

	// Unlock default channel via explicit default channelID -> keeps custom price ($6.4) and marks unlocked
	require.NoError(t, svc.SetChannelPriceLock(ctx, "claude-opus-5", "anthropic", false))
	listAfterDefaultUnlock := svc.ListModelPricingCatalog(ModelPricingListQuery{Search: "claude-opus-5"})
	require.False(t, listAfterDefaultUnlock.Items[0].IsDefaultLocked)
	require.True(t, listAfterDefaultUnlock.Items[0].IsDefaultModified)
	require.InDelta(t, 6.4, listAfterDefaultUnlock.Items[0].EffectivePricing.InputPerMillion, 1e-9)
}

func TestListModelNamesByProvider_ModelsDev(t *testing.T) {
	svc := newStubPricingServiceFromJSON(t, supportedModelsDevSample)
	require.Equal(t, []string{"claude-opus-5", "claude-sonnet-4-6"}, svc.ListModelNamesByProvider("anthropic"))
	require.Equal(t, []string{"gpt-6-astra"}, svc.ListModelNamesByProvider("OPENAI"))
	require.Equal(t, []string{"gemini-4-carbon"}, svc.ListModelNamesByProvider("google"))
	require.Equal(t, []string{"gemini-4-carbon"}, svc.ListModelNamesByProvider("gemini"))
	require.Empty(t, svc.ListModelNamesByProvider("openrouter"))
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
