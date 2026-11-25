package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestClaude52Pricing(t *testing.T) {
	for _, family := range []string{"fable", "mythos"} {
		model := "claude-" + family + "-5-2"
		for _, source := range []string{"no_catalog", "older_catalog", "models_dev_catalog"} {
			t.Run(family+"/"+source, func(t *testing.T) {
				var catalog *PricingService
				switch source {
				case "older_catalog":
					catalog = newStubPricingServiceFromJSON(t, `{
						"anthropic": {"id": "anthropic", "name": "Anthropic", "models": {
							"claude-fable-5-1": {"id": "claude-fable-5-1", "name": "Claude Fable 5.1", "cost": {"input": 10, "output": 50}},
							"claude-sonnet-4-5": {"id": "claude-sonnet-4-5", "name": "Claude Sonnet 4.5", "cost": {"input": 3, "output": 15}}
						}}
					}`)
				case "models_dev_catalog":
					catalog = newStubPricingServiceFromCatalogMap(map[string]*CatalogModelPricing{
						"claude-fable-5-2": {
							Provider: "anthropic", InputCostPerToken: 8e-6, OutputCostPerToken: 40e-6,
							CacheCreationInputTokenCost: 10e-6, CacheCreationInputTokenCostAbove1hr: 16e-6,
							CacheReadInputTokenCost: 0.2e-6,
						},
						"claude-mythos-5-2": {
							Provider: "anthropic", InputCostPerToken: 8e-6, OutputCostPerToken: 40e-6,
							CacheCreationInputTokenCost: 10e-6, CacheCreationInputTokenCostAbove1hr: 16e-6,
							CacheReadInputTokenCost: 0.2e-6,
						},
					})
				}
				svc := NewBillingService(&config.Config{}, catalog)
				for _, name := range []string{model, "models/" + model, "anthropic/" + model, "anthropic." + model, "global.anthropic." + model} {
					t.Run(name, func(t *testing.T) {
						pricing, err := svc.GetModelPricing(name)
						require.NoError(t, err)
						require.InDelta(t, 8e-6, pricing.InputPricePerToken, 1e-12)
						require.InDelta(t, 40e-6, pricing.OutputPricePerToken, 1e-12)
						require.InDelta(t, 10e-6, pricing.CacheCreationPricePerToken, 1e-12)
						require.InDelta(t, 10e-6, pricing.CacheCreation5mPrice, 1e-12)
						require.InDelta(t, 16e-6, pricing.CacheCreation1hPrice, 1e-12)
						require.InDelta(t, 0.2e-6, pricing.CacheReadPricePerToken, 1e-12)
						require.True(t, pricing.SupportsCacheBreakdown)
					})
				}

				tokens := UsageTokens{InputTokens: 250000, OutputTokens: 200, CacheCreationTokens: 1000,
					CacheCreation5mTokens: 600, CacheCreation1hTokens: 400, CacheReadTokens: 3000}
				for _, tier := range []string{"", "fast", "priority"} {
					t.Run("cost/"+tier, func(t *testing.T) {
						multiplier := 1.0
						if tier != "" {
							multiplier = 2
						}
						cost, err := svc.CalculateCostWithServiceTier(model, tokens, 1.5, tier)
						require.NoError(t, err)
						require.InDelta(t, 2*multiplier, cost.InputCost, 1e-12)
						require.InDelta(t, 0.008*multiplier, cost.OutputCost, 1e-12)
						require.InDelta(t, 0.0124*multiplier, cost.CacheCreationCost, 1e-12)
						require.InDelta(t, 0.0006*multiplier, cost.CacheReadCost, 1e-12)
						require.InDelta(t, 2.021*multiplier, cost.TotalCost, 1e-12)
						require.InDelta(t, 3.0315*multiplier, cost.ActualCost, 1e-12)
						require.False(t, cost.LongContextBillingApplied)

						unified, err := svc.CalculateCostUnified(CostInput{
							Ctx: context.Background(), Model: model, Tokens: tokens, RateMultiplier: 1.5,
							ServiceTier: tier, Resolver: NewModelPricingResolver(nil, svc),
						})
						require.NoError(t, err)
						cost.BillingMode = string(BillingModeToken)
						require.Equal(t, cost, unified)
					})
				}
			})
		}
	}
}

func TestClaude52CatalogOverridesAndVersionBoundaries(t *testing.T) {
	for _, family := range []string{"fable", "mythos"} {
		model := "claude-" + family + "-5-2"
		t.Run(family+"/catalog_override", func(t *testing.T) {
			catalog := newStubPricingServiceFromJSON(t, `{"anthropic":{"id":"anthropic","name":"Anthropic","models":{"`+model+`":{
				"id":"`+model+`","name":"`+model+`","cost":{"input":0,"output":7,"cache_write":0,"cache_read":0}
			}}}}`)
			svc := NewBillingService(&config.Config{}, catalog)
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.Zero(t, pricing.InputPricePerToken)
			require.InDelta(t, 7e-6, pricing.OutputPricePerToken, 1e-12)
			require.Zero(t, pricing.CacheCreationPricePerToken)
			require.Zero(t, pricing.CacheReadPricePerToken)
			for _, name := range []string{model, "anthropic/" + model, "global.anthropic." + model} {
				require.Same(t, catalog.effectiveDefaultData[model], catalog.GetModelPricing(name))
			}
		})
	}

	svc := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"claude-fable-5", "claude-fable-5-1", "claude-fable-5-20", "claude-fable-5-2-preview", "claude-mythos-5-20"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.NotEqual(t, 8e-6, pricing.InputPricePerToken)
		})
	}
}
