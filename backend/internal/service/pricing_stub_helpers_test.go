package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// openAILadderCatalogJSON models.dev 形态的长上下文阶梯样例（tiers + context_over_200k）。
const openAILadderCatalogJSON = `{
	"openai": {
		"id": "openai",
		"name": "OpenAI",
		"models": {
			"gpt-5.4": {
				"id": "gpt-5.4",
				"name": "GPT-5.4",
				"cost": {
					"input": 2.5,
					"output": 15,
					"cache_read": 0.25,
					"cache_write": 2.5,
					"tiers": [{"input": 5, "output": 22.5, "cache_read": 0.5, "tier": {"type": "context", "size": 272000}}]
				}
			},
			"gpt-5.5-pro": {
				"id": "gpt-5.5-pro",
				"name": "GPT-5.5 Pro",
				"cost": {
					"input": 30,
					"output": 180,
					"tiers": [{"input": 60, "output": 270, "tier": {"type": "context", "size": 272000}}]
				}
			}
		}
	}
}`

// newStubPricingServiceFromJSON 使用 models.dev 解析路径构造 PricingService stub。
func newStubPricingServiceFromJSON(t *testing.T, body string) *PricingService {
	t.Helper()
	s := NewPricingService(nil, nil)
	require.NoError(t, s.LoadModelsDevJSON([]byte(body)))
	return s
}

// newStubPricingServiceFromCatalogMap 从 CatalogModelPricing 映射直接构造 PricingService stub。
func newStubPricingServiceFromCatalogMap(data map[string]*CatalogModelPricing) *PricingService {
	s := NewPricingService(nil, nil)
	if data != nil {
		s.effectiveDefaultData = data
		for modelID, p := range data {
			if p == nil || p.Provider == "" {
				continue
			}
			prov := p.Provider
			s.providerModels[prov] = append(s.providerModels[prov], modelID)
		}
	}
	return s
}
