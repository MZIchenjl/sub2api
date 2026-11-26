package service

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type stubPricingInventoryProvider struct {
	mu       sync.Mutex
	accounts []Account
}

func (p *stubPricingInventoryProvider) setAccounts(accounts ...Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := make([]Account, len(accounts))
	copy(copied, accounts)
	p.accounts = copied
}

func (p *stubPricingInventoryProvider) CollectActiveModelInventory(_ context.Context) (*ActiveModelInventory, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return BuildActiveModelInventoryFromAccounts(p.accounts), nil
}

const inventoryModelsDevInitial = `{
	"anthropic": {
		"id": "anthropic",
		"name": "Anthropic",
		"models": {
			"claude-opus-5": {
				"id": "claude-opus-5",
				"name": "Claude Opus 5",
				"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
			},
			"claude-free-beta": {
				"id": "claude-free-beta",
				"name": "Claude Free Beta",
				"cost": {"input": 0, "output": 0, "cache_read": 0, "cache_write": 0}
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
				"cost": {"input": 5.5, "output": 27.5}
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
				"cost": {"input": 10, "output": 50}
			}
		}
	}
}`

const inventoryModelsDevBackfilled = `{
	"anthropic": {
		"id": "anthropic",
		"name": "Anthropic",
		"models": {
			"claude-opus-5": {
				"id": "claude-opus-5",
				"name": "Claude Opus 5",
				"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
			},
			"claude-free-beta": {
				"id": "claude-free-beta",
				"name": "Claude Free Beta",
				"cost": {"input": 0, "output": 0, "cache_read": 0, "cache_write": 0}
			},
			"claude-future-beta": {
				"id": "claude-future-beta",
				"name": "Claude Future Beta",
				"cost": {"input": 1.2, "output": 4.8}
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
				"cost": {"input": 5.5, "output": 27.5}
			}
		}
	}
}`

func TestInventoryDrivenPricingLifecycleAndNullVsZero(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryModelPricingRepo()
	remote := &stubModelsDevClient{body: []byte(inventoryModelsDevInitial)}
	invProvider := &stubPricingInventoryProvider{}

	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			ModelsDevURL:      DefaultModelsDevURL,
			AutoSyncEnabled:   true,
			SyncIntervalHours: 24,
		},
	}, remote)
	svc.SetModelPricingRepository(repo)
	svc.SetInventoryProvider(invProvider)

	// 1. Empty initial state: 0 accounts -> 0 catalog rows and 0 remote HTTP fetches
	require.NoError(t, svc.Initialize())
	t.Cleanup(func() { svc.Stop() })

	status := svc.GetSyncStatus()
	require.Equal(t, 0, status.Meta.ModelCount)
	require.Equal(t, 0, status.MissingPricingCount)
	require.Equal(t, 0, remote.callCount())

	rows, err := repo.ListCatalogEntries(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)

	// 2. Add an Anthropic OAuth account using:
	//    - claude-opus-5 (priced in models.dev)
	//    - claude-free-beta (explicit $0 in models.dev)
	//    - claude-future-beta (initially absent from models.dev -> pricing_missing = true, NULL prices)
	//    Note: gpt-6-astra and amazon-bedrock MUST NOT be stored because no OpenAI or Bedrock account exists.
	invProvider.setAccounts(Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-5":      "claude-opus-5",
				"claude-free-beta":   "claude-free-beta",
				"claude-future-beta": "claude-future-beta",
			},
		},
	})

	require.NoError(t, svc.ReconcileWithInventory(ctx))
	require.Equal(t, 1, remote.callCount())

	status = svc.GetSyncStatus()
	require.Equal(t, 3, status.Meta.ModelCount)
	require.Equal(t, 1, status.MissingPricingCount)

	// Unused OpenAI model and unused Bedrock channel must not be persisted
	require.Nil(t, svc.GetModelPricing("gpt-6-astra"))
	_, hasBedrock := svc.GetModelPricingForProvider("amazon-bedrock", "claude-opus-5", "")
	require.False(t, hasBedrock)

	// Inspect persisted DB records: strict NULL vs explicit 0
	rows, err = repo.ListCatalogEntries(ctx)
	require.NoError(t, err)
	byModel := make(map[string]ModelPricingCatalogRecord, len(rows))
	for _, r := range rows {
		byModel[r.ModelID] = r
	}
	require.Len(t, byModel, 3)

	freeRec := byModel["claude-free-beta"]
	require.False(t, freeRec.PricingMissing)
	require.NotNil(t, freeRec.InputPerMillion)
	require.InDelta(t, 0.0, *freeRec.InputPerMillion, 1e-12)
	require.NotNil(t, freeRec.OutputPerMillion)
	require.InDelta(t, 0.0, *freeRec.OutputPerMillion, 1e-12)

	missingRec := byModel["claude-future-beta"]
	require.True(t, missingRec.PricingMissing)
	require.Equal(t, PricingMissingChannelID, missingRec.ChannelID)
	require.Nil(t, missingRec.InputPerMillion)
	require.Nil(t, missingRec.OutputPerMillion)
	require.Nil(t, missingRec.CacheReadPerMillion)
	require.Nil(t, missingRec.CacheWritePerMillion)

	// Runtime pricing lookup: explicit $0 returns non-nil pricing; missing (NULL) returns nil!
	freePricing := svc.GetModelPricing("claude-free-beta")
	require.NotNil(t, freePricing)
	require.InDelta(t, 0.0, freePricing.InputCostPerToken, 1e-12)
	require.Nil(t, svc.GetModelPricing("claude-future-beta"))

	// UI listing distinguishes missing_pricing from explicit $0
	missingList := svc.ListModelPricingCatalog(ModelPricingListQuery{Filter: "missing"})
	require.Equal(t, 1, missingList.Total)
	require.Equal(t, "claude-future-beta", missingList.Items[0].ModelID)
	require.Equal(t, "missing_pricing", missingList.Items[0].Source)
	require.True(t, missingList.Items[0].IsPricingMissing)

	freeList := svc.ListModelPricingCatalog(ModelPricingListQuery{Search: "claude-free-beta"})
	require.Equal(t, 1, freeList.Total)
	require.Equal(t, "default_auto", freeList.Items[0].Source)
	require.False(t, freeList.Items[0].IsPricingMissing)

	// Cannot freeze a missing_pricing model before setting a price
	require.Error(t, svc.SetChannelPriceFreeze(ctx, "claude-future-beta", "", true))

	// 3. Admin explicitly sets $0 free override on claude-future-beta (unfrozen) -> clears missing_pricing!
	unfrozen := false
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ModelID: "claude-future-beta",
		Frozen:  &unfrozen,
		Custom:  TokenPricingPerMillion{InputPerMillion: 0, OutputPerMillion: 0},
	}))
	require.Equal(t, 0, svc.GetSyncStatus().MissingPricingCount)
	explicitZero := svc.GetModelPricing("claude-future-beta")
	require.NotNil(t, explicitZero)
	require.InDelta(t, 0.0, explicitZero.InputCostPerToken, 1e-12)

	futureList := svc.ListModelPricingCatalog(ModelPricingListQuery{Search: "claude-future-beta"})
	require.Equal(t, 1, futureList.Total)
	require.False(t, futureList.Items[0].IsPricingMissing)
	require.Equal(t, "default_custom", futureList.Items[0].Source)

	// Resetting the override returns it to missing_pricing (NULL)
	require.NoError(t, svc.ResetModelPricing(ctx, nil, "claude-future-beta"))
	require.Nil(t, svc.GetModelPricing("claude-future-beta"))
	require.Equal(t, 1, svc.GetSyncStatus().MissingPricingCount)

	// 4. Upstream models.dev later adds pricing for claude-future-beta -> SyncNow auto-backfills it!
	remote.body = []byte(inventoryModelsDevBackfilled)
	syncStatus, err := svc.SyncNowWithMode(ctx, PricingSyncOverwriteSkipFrozen)
	require.NoError(t, err)
	require.Equal(t, 0, syncStatus.MissingPricingCount)
	backfilled := svc.GetModelPricing("claude-future-beta")
	require.NotNil(t, backfilled)
	require.InDelta(t, 1.2e-6, backfilled.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.8e-6, backfilled.OutputCostPerToken, 1e-12)

	// 5. Option A cleanup: set Default + Group overrides on claude-opus-5, then remove claude-opus-5 from accounts.
	//    Both catalog rows AND all overrides for claude-opus-5 must be deleted without extra remote calls.
	gid := int64(42)
	frozenTrue := true
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		ModelID: "claude-opus-5",
		Frozen:  &frozenTrue,
		Custom:  TokenPricingPerMillion{InputPerMillion: 3.0, OutputPerMillion: 15.0},
	}))
	require.NoError(t, svc.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		GroupID: &gid,
		ModelID: "claude-opus-5",
		Custom:  TokenPricingPerMillion{InputPerMillion: 2.0, OutputPerMillion: 10.0},
	}))

	callsBeforePrune := remote.callCount()
	invProvider.setAccounts(Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-future-beta": "claude-future-beta",
			},
		},
	})
	require.NoError(t, svc.ReconcileWithInventory(ctx))
	require.Equal(t, callsBeforePrune, remote.callCount(), "pruning unused models must not fetch remote")

	require.Nil(t, svc.GetModelPricing("claude-opus-5"))
	require.Nil(t, svc.GetModelPricing("claude-free-beta"))
	_, hasGroupAfterPrune := svc.GetModelPricingForGroup(gid, "claude-opus-5")
	require.False(t, hasGroupAfterPrune)

	overridesLeft, err := repo.ListOverrides(ctx)
	require.NoError(t, err)
	require.Empty(t, overridesLeft, "Option A: unused model overrides must be deleted from DB")

	// 6. Removing the last account clears the entire catalog back to empty state
	invProvider.setAccounts()
	require.NoError(t, svc.ReconcileWithInventory(ctx))
	require.Equal(t, 0, svc.GetSyncStatus().Meta.ModelCount)
	rows, err = repo.ListCatalogEntries(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
}
