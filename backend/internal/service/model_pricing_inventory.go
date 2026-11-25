package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// ActiveModelInventory describes the exact models and account-matched channels currently
// present across all non-deleted accounts in the system.
type ActiveModelInventory struct {
	// Models maps normalized model_id -> set of system platforms exposing that model.
	Models map[string]map[string]struct{}
	// ModelChannels maps normalized model_id -> set of models.dev channel_ids matched by accounts exposing it.
	ModelChannels map[string]map[string]struct{}
	// Channels is the union of all matched models.dev channel_ids across non-deleted accounts.
	Channels map[string]struct{}
	// AccountCount is the total number of non-deleted accounts inspected.
	AccountCount int
}

// PricingInventoryProvider collects the system's current active model inventory.
type PricingInventoryProvider interface {
	CollectActiveModelInventory(ctx context.Context) (*ActiveModelInventory, error)
}

type accountPricingInventoryProvider struct {
	accountRepo AccountRepository
}

// NewAccountPricingInventoryProvider builds a PricingInventoryProvider backed by all non-deleted accounts.
func NewAccountPricingInventoryProvider(accountRepo AccountRepository) PricingInventoryProvider {
	if accountRepo == nil {
		return nil
	}
	return &accountPricingInventoryProvider{accountRepo: accountRepo}
}

func (p *accountPricingInventoryProvider) CollectActiveModelInventory(ctx context.Context) (*ActiveModelInventory, error) {
	accounts, err := p.accountRepo.ListAllWithFilters(ctx, "", "", "", "", 0, "")
	if err != nil {
		return nil, err
	}
	return BuildActiveModelInventoryFromAccounts(accounts), nil
}

// BuildActiveModelInventoryFromAccounts derives the active model × channel inventory
// from a slice of non-deleted accounts.
func BuildActiveModelInventoryFromAccounts(accounts []Account) *ActiveModelInventory {
	inv := &ActiveModelInventory{
		Models:        make(map[string]map[string]struct{}),
		ModelChannels: make(map[string]map[string]struct{}),
		Channels:      make(map[string]struct{}),
		AccountCount:  len(accounts),
	}

	addModel := func(acc *Account, rawModel string) {
		rawModel = strings.TrimSpace(rawModel)
		if rawModel == "" || strings.Contains(rawModel, "*") {
			return
		}
		normID := NormalizeOriginalModelID(rawModel)
		if normID == "" {
			return
		}
		if inv.Models[normID] == nil {
			inv.Models[normID] = make(map[string]struct{})
		}
		if acc.Platform != "" {
			inv.Models[normID][acc.Platform] = struct{}{}
		}
		if chID := ResolveAccountPricingProvider(acc, normID); chID != "" {
			inv.Channels[chID] = struct{}{}
			if inv.ModelChannels[normID] == nil {
				inv.ModelChannels[normID] = make(map[string]struct{})
			}
			inv.ModelChannels[normID][chID] = struct{}{}
		}
	}

	for i := range accounts {
		acc := &accounts[i]
		models := collectAccountModelsForPricingInventory(acc)
		for _, m := range models {
			addModel(acc, m)
		}
	}
	return inv
}

func collectAccountModelsForPricingInventory(acc *Account) []string {
	if acc == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	appendUnique := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, "*") {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	// 1. Explicit configured model_mapping keys & non-wildcard target values.
	mapping := acc.GetModelMapping()
	useDefaultFallback := len(mapping) == 0
	if acc.Platform == PlatformOpenAI && acc.IsOpenAIPassthroughEnabled() && !acc.IsOpenAIPassthroughModelFilterEnabled() {
		useDefaultFallback = true
	}
	for reqModel, targetModel := range mapping {
		appendUnique(reqModel)
		appendUnique(targetModel)
	}

	// 2. Observed/upstream cached models when no explicit mapping narrows the account.
	if useDefaultFallback {
		if acc.Extra != nil {
			if rawMeta, ok := acc.Extra["upstream_model_metadata"].(map[string]any); ok {
				for modelID := range rawMeta {
					appendUnique(modelID)
				}
			}
			if rawGrok, ok := acc.Extra["grok_observed_models"].(map[string]any); ok {
				if models, ok := rawGrok["models"].([]any); ok {
					for _, item := range models {
						if s, ok := item.(string); ok {
							appendUnique(s)
						} else if m, ok := item.(map[string]any); ok {
							if id, ok := m["id"].(string); ok {
								appendUnique(id)
							}
						}
					}
				}
			}
		}
		// 3. Platform built-in default models for unmapped accounts.
		for _, id := range defaultPlatformModelsForPricingInventory(acc.Platform) {
			appendUnique(id)
		}
	}

	return out
}

func defaultPlatformModelsForPricingInventory(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformAnthropic:
		return claude.DefaultModelIDs()
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, m := range geminicli.DefaultModels {
			ids = append(ids, m.ID)
		}
		return ids
	case PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		return ids
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformOpenCodeGo:
		return DefaultOpenCodeGoModelIDs()
	case PlatformTypeSafe:
		return []string{"jev-latest"}
	default:
		return nil
	}
}

// filterCatalogRecordsForInventory narrows the full supported models.dev snapshot down to
// ONLY the models currently present in inv.
//   - For each active model found in upstream: retains records for every matched account channel
//     PLUS the model's canonical default channel (so models like LongCat-2.5 hosted on an
//     anthropic/openai account still resolve their models.dev default price).
//   - For each active model NOT found in upstream: emits one pricing_missing=true placeholder
//     record with NULL token prices so the UI surfaces it as "Missing Pricing" instead of $0.
func filterCatalogRecordsForInventory(
	upstream []ModelPricingCatalogRecord,
	inv *ActiveModelInventory,
	syncedAt time.Time,
) []ModelPricingCatalogRecord {
	if inv == nil {
		return upstream
	}
	if len(inv.Models) == 0 {
		return nil
	}
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}

	// Group upstream records by normalized model_id and find each model's canonical default channel.
	byModel := make(map[string][]ModelPricingCatalogRecord)
	bestChannelByModel := make(map[string]string)
	bestScoreByModel := make(map[string]int)
	for _, rec := range upstream {
		byModel[rec.ModelID] = append(byModel[rec.ModelID], rec)
		if curCh, ok := bestChannelByModel[rec.ModelID]; !ok ||
			rec.Score > bestScoreByModel[rec.ModelID] ||
			(rec.Score == bestScoreByModel[rec.ModelID] && rec.ChannelID < curCh) {
			bestChannelByModel[rec.ModelID] = rec.ChannelID
			bestScoreByModel[rec.ModelID] = rec.Score
		}
	}

	activeModelIDs := make([]string, 0, len(inv.Models))
	for modelID := range inv.Models {
		activeModelIDs = append(activeModelIDs, modelID)
	}
	sort.Strings(activeModelIDs)

	result := make([]ModelPricingCatalogRecord, 0, len(activeModelIDs))
	for _, modelID := range activeModelIDs {
		candidates := byModel[modelID]
		matchedChannels := inv.ModelChannels[modelID]
		defaultCh := bestChannelByModel[modelID]

		if len(candidates) == 0 {
			primaryPlatform := pickPrimaryActivePlatform(modelID, inv.Models[modelID])
			result = append(result, ModelPricingCatalogRecord{
				ChannelID:      PricingMissingChannelID,
				RawModelID:     modelID,
				ModelID:        modelID,
				Platform:       primaryPlatform,
				DisplayName:    modelID,
				Mode:           "chat",
				PricingMissing: true,
				SyncedAt:       syncedAt.UTC(),
			})
			continue
		}

		keptAny := false
		for _, rec := range candidates {
			_, inAccountChannels := matchedChannels[rec.ChannelID]
			if inAccountChannels || rec.ChannelID == defaultCh {
				result = append(result, rec)
				keptAny = true
			}
		}
		if !keptAny {
			result = append(result, candidates...)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].ChannelID != result[j].ChannelID {
			return result[i].ChannelID < result[j].ChannelID
		}
		return result[i].RawModelID < result[j].RawModelID
	})
	return result
}

func pickPrimaryActivePlatform(modelID string, platforms map[string]struct{}) string {
	if len(platforms) == 0 {
		return inferSystemPlatformForModel(modelID, "")
	}
	inferred := inferSystemPlatformForModel(modelID, "")
	if _, ok := platforms[inferred]; ok {
		return inferred
	}
	for _, spec := range domain.Platforms() {
		if _, ok := platforms[spec.ID]; ok {
			return spec.ID
		}
	}
	for p := range platforms {
		return p
	}
	return inferred
}
