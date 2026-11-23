package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGeminiCarbonPricingAliases(t *testing.T) {
	base := &CatalogModelPricing{InputCostPerToken: 2e-6, OutputCostPerToken: 10e-6, CacheReadInputTokenCost: 0.1e-6}
	svc := &PricingService{effectiveDefaultData: map[string]*CatalogModelPricing{"gemini-4-carbon": base}}
	for _, model := range []string{
		"gemini-4-carbon", "gemini-4-carbon-high", "gemini-4-carbon-medium", "gemini-4-carbon-low", "gemini-4-carbon-tiered",
		"models/gemini-4-carbon", "projects/p/locations/global/publishers/google/models/gemini-4-carbon-high",
	} {
		t.Run(model, func(t *testing.T) {
			require.Same(t, base, svc.GetIdentifiedModelPricing(model))
		})
	}
	// A separately published tier price must take precedence over base normalization.
	tier := &CatalogModelPricing{InputCostPerToken: 3e-6}
	svc.effectiveDefaultData["gemini-4-carbon-high"] = tier
	require.Same(t, tier, svc.GetIdentifiedModelPricing("gemini-4-carbon-high"))
	require.Equal(t, "gemini-4-carbon-cyber", normalizeGeminiThinkingTierAlias("gemini-4-carbon-cyber"))
}

func TestGeminiCarbonFallbackBilling(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	for _, model := range []string{"gemini-4-carbon", "gemini-4-carbon-low", "gemini-4-carbon-medium", "gemini-4-carbon-high", "models/gemini-4-carbon", "projects/p/locations/global/publishers/google/models/gemini-4-carbon"} {
		t.Run(model, func(t *testing.T) {
			cost, err := svc.CalculateCost(model, tokens, 1)
			require.NoError(t, err)
			require.InDelta(t, 2, cost.InputCost, 1e-12)
			require.InDelta(t, 10, cost.OutputCost, 1e-12)
			require.InDelta(t, 0.1, cost.CacheReadCost, 1e-12)
			require.InDelta(t, 12.1, cost.TotalCost, 1e-12)
			priority, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "priority")
			require.NoError(t, err)
			require.InDelta(t, 12.1*1.8, priority.TotalCost, 1e-12)
			require.Zero(t, svc.CacheStoragePricePerTokenHour(model))
		})
	}
}

func TestModelsDevCatalogIncludesGeminiCarbon(t *testing.T) {
	svc := newStubPricingServiceFromJSON(t, `{
		"google": {
			"id": "google",
			"name": "Google",
			"models": {
				"gemini-4-carbon": {
					"id": "gemini-4-carbon",
					"name": "Gemini 4 Carbon",
					"cost": {"input": 2, "output": 10, "cache_read": 0.1}
				}
			}
		}
	}`)
	pricing := svc.GetIdentifiedModelPricing("gemini-4-carbon")
	require.NotNil(t, pricing)
	require.Equal(t, "google", pricing.Provider)
	require.InDelta(t, 2e-6, pricing.InputCostPerToken, 1e-15)
	require.InDelta(t, 10e-6, pricing.OutputCostPerToken, 1e-15)
	require.InDelta(t, 0.1e-6, pricing.CacheReadInputTokenCost, 1e-15)
	require.Zero(t, pricing.LongContextInputTokenThreshold)
	require.Zero(t, pricing.CacheStorageCostPerTokenPerHour)
	require.Contains(t, svc.ListModelNamesByProvider("google"), "gemini-4-carbon")
}
