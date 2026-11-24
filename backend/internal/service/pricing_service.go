package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"go.uber.org/zap"
)

const (
	DefaultModelsDevURL             = "https://models.dev/api.json"
	DefaultPricingSyncIntervalHours = 24
	MinPricingSyncIntervalHours     = 1
	MaxPricingSyncIntervalHours     = 720
	modelsDevCatalogFileName        = "models_dev_catalog.json"

	SettingKeyModelPricingSyncConfig = "model_pricing_sync_config"
	SettingKeyModelPricingSyncMeta   = "model_pricing_sync_meta"
	SettingKeyModelPricingOverrides  = "model_pricing_overrides"

	PricingSyncStatusNever   = "never"
	PricingSyncStatusSuccess = "success"
	PricingSyncStatusFailed  = "failed"

	PricingSyncOverwriteSkipLocked = "skip_locked"
	PricingSyncOverwriteAll        = "overwrite_all"

	tokensPerMillion = 1_000_000.0
)

var (
	openAIModelDatePattern = regexp.MustCompile(`-\d{8}$`)
	openAIModelBasePattern = regexp.MustCompile(`^(gpt-\d+(?:\.\d+)?)(?:-|$)`)
	bedrockRegionPrefixRe  = regexp.MustCompile(`^(?:us|eu|ap|global|au|ca|jp|us-gov)\.(?:anthropic|amazon|meta|mistral|cohere|deepseek|google|openai|qwen|xai|moonshot|zhipu|minimax|ai21|stability|twelvelabs|writer|nvidia)\.`)
	bedrockVendorPrefixRe  = regexp.MustCompile(`^(?:anthropic|amazon|meta|mistral|cohere|deepseek|google|openai|qwen|xai|moonshot|zhipu|minimax|ai21|stability|twelvelabs|writer|nvidia)\.`)
	bedrockVersionSuffixRe = regexp.MustCompile(`-v\d+(?::\d+)?$`)
	vertexDateSuffixRe     = regexp.MustCompile(`@(\d{8})$`)
)

// tokenPriceOverrides tracks explicit nonnegative rates from custom pricing overrides.
// Keep this provenance with the parsed card so model-specific fallback policies do not clobber explicit edits.
type tokenPriceOverrides struct {
	input, output, cacheRead bool
}

// CatalogModelPricing 模型目录价格数据结构（per-token USD，供 BillingService / 渠道展示使用）
type CatalogModelPricing struct {
	CacheCreationInputTokenCostExplicit bool `json:"-"`
	priceOverrides                      tokenPriceOverrides
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority           float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost         float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextInputCostMultiplier      float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier     float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier                 bool    `json:"supports_service_tier"`
	Provider                            string  `json:"provider"`
	Mode                                string  `json:"mode"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	OutputCostPerImage                  float64 `json:"output_cost_per_image"`
	OutputCostPerImageToken             float64 `json:"output_cost_per_image_token"`
	InputCostPerImageToken              float64 `json:"input_cost_per_image_token"`
	CacheReadInputImageTokenCost        float64 `json:"cache_read_input_image_token_cost"`
	CacheStorageCostPerTokenPerHour     float64 `json:"cache_storage_cost_per_token_per_hour"`
	TokenPricingAbsent                  bool    `json:"-"`
}

// PricingRemoteClient 远程 models.dev 价格数据获取接口
type PricingRemoteClient interface {
	FetchPricingJSON(ctx context.Context, url string) ([]byte, error)
}

// --- Supported system provider specs (only sync models supported by sub2api) ---

type supportedModelsDevProviderSpec struct {
	ProviderID   string
	Platform     string // primary sub2api platform ID
	ChannelLabel string // display name for account-type matched channel
	IsDefault    bool   // first-party default channel for Platform
	IsAggregator bool   // multi-vendor aggregator platform (opencode_go / command_code / cline)
	ModelFilter  func(origID string, raw modelsDevRawModel) bool
}

var openCodeGoKnownModelSet = func() map[string]struct{} {
	ids := DefaultOpenCodeGoModelIDs()
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	return set
}()

func hasPositiveTokenCost(raw modelsDevRawModel) bool {
	if raw.Cost == nil {
		return false
	}
	if raw.Cost.Input != nil && *raw.Cost.Input > 0 {
		return true
	}
	if raw.Cost.Output != nil && *raw.Cost.Output > 0 {
		return true
	}
	return false
}

func isClaudeCatalogModelID(origID string, _ modelsDevRawModel) bool {
	return strings.HasPrefix(origID, "claude-")
}

func isGeminiCatalogModelID(origID string, _ modelsDevRawModel) bool {
	return strings.HasPrefix(origID, "gemini-") ||
		strings.HasPrefix(origID, "imagen-") ||
		strings.HasPrefix(origID, "veo-") ||
		strings.HasPrefix(origID, "deep-research-")
}

func isOpenCodeSupportedModel(origID string, raw modelsDevRawModel) bool {
	if _, ok := openCodeGoKnownModelSet[origID]; ok {
		return true
	}
	return hasPositiveTokenCost(raw) && !strings.HasSuffix(strings.ToLower(strings.TrimSpace(raw.ID)), "-free")
}

func isPaidAggregatorModel(_ string, raw modelsDevRawModel) bool {
	return hasPositiveTokenCost(raw)
}

// supportedModelsDevProviders defines the exact models.dev providers relevant to sub2api's
// registered platforms and account types. All other 300+ unrelated providers are ignored during sync.
var supportedModelsDevProviders = []supportedModelsDevProviderSpec{
	// Anthropic (OAuth / Setup-Token / API Key / Upstream + Bedrock + Vertex AI Anthropic)
	{ProviderID: "anthropic", Platform: PlatformAnthropic, ChannelLabel: "Anthropic", IsDefault: true, ModelFilter: isClaudeCatalogModelID},
	{ProviderID: "amazon-bedrock", Platform: PlatformAnthropic, ChannelLabel: "AWS Bedrock", IsDefault: false, ModelFilter: isClaudeCatalogModelID},
	{ProviderID: "google-vertex-anthropic", Platform: PlatformAnthropic, ChannelLabel: "Vertex AI (Anthropic)", IsDefault: false, ModelFilter: isClaudeCatalogModelID},

	// OpenAI (OAuth / API Key / Upstream)
	{ProviderID: "openai", Platform: PlatformOpenAI, ChannelLabel: "OpenAI", IsDefault: true},

	// Gemini (OAuth / API Key / Upstream + Vertex AI Gemini)
	{ProviderID: "google", Platform: PlatformGemini, ChannelLabel: "Google AI", IsDefault: true, ModelFilter: isGeminiCatalogModelID},
	{ProviderID: "google-vertex", Platform: PlatformGemini, ChannelLabel: "Vertex AI (Gemini)", IsDefault: false, ModelFilter: isGeminiCatalogModelID},

	// Grok (xAI)
	{ProviderID: "xai", Platform: PlatformGrok, ChannelLabel: "xAI", IsDefault: true},

	// Kimi (Moonshot PayG + CN + Coding Plan)
	{ProviderID: "moonshotai", Platform: PlatformKimi, ChannelLabel: "Moonshot", IsDefault: true},
	{ProviderID: "moonshotai-cn", Platform: PlatformKimi, ChannelLabel: "Moonshot CN", IsDefault: false},
	{ProviderID: "kimi-code-plan-global", Platform: PlatformKimi, ChannelLabel: "Kimi Coding Plan", IsDefault: false},
	{ProviderID: "kimi-code-plan-cn", Platform: PlatformKimi, ChannelLabel: "Kimi Coding Plan (CN)", IsDefault: false},

	// Zhipu GLM (Zhipu PayG + Z.AI + Coding Plan)
	{ProviderID: "zhipuai", Platform: PlatformZhipu, ChannelLabel: "Zhipu AI", IsDefault: true},
	{ProviderID: "zai", Platform: PlatformZhipu, ChannelLabel: "Z.AI", IsDefault: false},
	{ProviderID: "zhipuai-coding-plan", Platform: PlatformZhipu, ChannelLabel: "Zhipu Coding Plan", IsDefault: false},
	{ProviderID: "zai-coding-plan", Platform: PlatformZhipu, ChannelLabel: "Z.AI Coding Plan", IsDefault: false},

	// DeepSeek
	{ProviderID: "deepseek", Platform: PlatformDeepseek, ChannelLabel: "DeepSeek", IsDefault: true},

	// MiniMax (PayG + CN + Coding Plan)
	{ProviderID: "minimax", Platform: PlatformMiniMax, ChannelLabel: "MiniMax", IsDefault: true},
	{ProviderID: "minimax-cn", Platform: PlatformMiniMax, ChannelLabel: "MiniMax CN", IsDefault: false},
	{ProviderID: "minimax-coding-plan", Platform: PlatformMiniMax, ChannelLabel: "MiniMax Coding Plan", IsDefault: false},
	{ProviderID: "minimax-cn-coding-plan", Platform: PlatformMiniMax, ChannelLabel: "MiniMax Coding Plan (CN)", IsDefault: false},

	// Multi-model registered sub2api platforms
	{ProviderID: "opencode-go", Platform: PlatformOpenCodeGo, ChannelLabel: "OpenCode Go", IsDefault: true, IsAggregator: true, ModelFilter: isOpenCodeSupportedModel},
	{ProviderID: "commandcode", Platform: PlatformCommandCode, ChannelLabel: "Command Code", IsDefault: true, IsAggregator: true, ModelFilter: isPaidAggregatorModel},
	{ProviderID: "cline-pass", Platform: PlatformCline, ChannelLabel: "Cline", IsDefault: true, IsAggregator: true, ModelFilter: isPaidAggregatorModel},
	{ProviderID: "typesafe", Platform: PlatformTypeSafe, ChannelLabel: "TypeSafe", IsDefault: true},
}

var supportedModelsDevProviderMap = func() map[string]supportedModelsDevProviderSpec {
	m := make(map[string]supportedModelsDevProviderSpec, len(supportedModelsDevProviders))
	for _, spec := range supportedModelsDevProviders {
		m[spec.ProviderID] = spec
	}
	return m
}()

// ResolveAccountPricingProvider automatically maps an Account (platform + account type +
// coding plan / CN host) to its corresponding models.dev channel ID.
func ResolveAccountPricingProvider(account *Account, model string) string {
	if account == nil {
		return ""
	}
	switch account.Platform {
	case PlatformAnthropic:
		if account.IsBedrock() {
			return "amazon-bedrock"
		}
		if account.IsVertexServiceAccount() {
			return "google-vertex-anthropic"
		}
		return "anthropic"
	case PlatformOpenAI:
		return "openai"
	case PlatformGemini:
		if account.IsVertexServiceAccount() {
			return "google-vertex"
		}
		return "google"
	case PlatformAntigravity:
		modelLower := strings.ToLower(strings.TrimSpace(model))
		if strings.Contains(modelLower, "gemini") || strings.Contains(modelLower, "imagen") {
			return "google"
		}
		return "anthropic"
	case PlatformGrok:
		return "xai"
	case PlatformDeepseek:
		return "deepseek"
	case PlatformKimi:
		baseURL := strings.ToLower(strings.TrimSpace(account.GetOpenAIFormatBaseURL()))
		isCN := strings.Contains(baseURL, "moonshot.cn") || strings.Contains(baseURL, "kimi.com")
		if account.IsCodingPlan() {
			if isCN {
				return "kimi-code-plan-cn"
			}
			return "kimi-code-plan-global"
		}
		if isCN {
			return "moonshotai-cn"
		}
		return "moonshotai"
	case PlatformZhipu:
		baseURL := strings.ToLower(strings.TrimSpace(account.GetOpenAIFormatBaseURL()))
		isZAI := strings.Contains(baseURL, "z.ai")
		if account.IsCodingPlan() {
			if isZAI {
				return "zai-coding-plan"
			}
			return "zhipuai-coding-plan"
		}
		if isZAI {
			return "zai"
		}
		return "zhipuai"
	case PlatformMiniMax:
		baseURL := strings.ToLower(strings.TrimSpace(account.GetOpenAIFormatBaseURL()))
		isCN := strings.Contains(baseURL, "minimaxi.com") || strings.Contains(baseURL, "minimax.cn")
		if account.IsCodingPlan() {
			if isCN {
				return "minimax-cn-coding-plan"
			}
			return "minimax-coding-plan"
		}
		if isCN {
			return "minimax-cn"
		}
		return "minimax"
	case PlatformOpenCodeGo:
		return "opencode-go"
	case PlatformCommandCode:
		return "commandcode"
	case PlatformCline:
		return "cline-pass"
	case PlatformTypeSafe:
		return "typesafe"
	default:
		if spec, ok := domain.LookupPlatform(account.Platform); ok && spec.ModelsDevProvider != "" {
			return spec.ModelsDevProvider
		}
		return ""
	}
}

// --- models.dev raw API JSON structures ---

type modelsDevRawProvider struct {
	ID     string                       `json:"id"`
	Name   string                       `json:"name"`
	API    string                       `json:"api,omitempty"`
	Models map[string]modelsDevRawModel `json:"models"`
}

type modelsDevRawModalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type modelsDevRawLimit struct {
	Context int `json:"context,omitempty"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output,omitempty"`
}

type modelsDevRawCostTierSpec struct {
	Type string `json:"type"`
	Size int    `json:"size"`
}

type modelsDevRawCostTier struct {
	Input      *float64                  `json:"input,omitempty"`
	Output     *float64                  `json:"output,omitempty"`
	CacheRead  *float64                  `json:"cache_read,omitempty"`
	CacheWrite *float64                  `json:"cache_write,omitempty"`
	Tier       *modelsDevRawCostTierSpec `json:"tier,omitempty"`
}

type modelsDevRawContextOver200k struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

type modelsDevRawCost struct {
	Input           *float64                     `json:"input,omitempty"`
	Output          *float64                     `json:"output,omitempty"`
	CacheRead       *float64                     `json:"cache_read,omitempty"`
	CacheWrite      *float64                     `json:"cache_write,omitempty"`
	Reasoning       *float64                     `json:"reasoning,omitempty"`
	InputAudio      *float64                     `json:"input_audio,omitempty"`
	OutputAudio     *float64                     `json:"output_audio,omitempty"`
	Tiers           []modelsDevRawCostTier       `json:"tiers,omitempty"`
	ContextOver200k *modelsDevRawContextOver200k `json:"context_over_200k,omitempty"`
}

type modelsDevRawModel struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name,omitempty"`
	Family           string                  `json:"family,omitempty"`
	ReleaseDate      string                  `json:"release_date,omitempty"`
	Status           string                  `json:"status,omitempty"`
	CanonicalModelID string                  `json:"canonical_model_id,omitempty"`
	Modalities       *modelsDevRawModalities `json:"modalities,omitempty"`
	Limit            *modelsDevRawLimit      `json:"limit,omitempty"`
	Cost             *modelsDevRawCost       `json:"cost,omitempty"`
}

// --- Public DTOs & Store structures ---

// PricingSyncConfig controls models.dev synchronization.
type PricingSyncConfig struct {
	AutoSyncEnabled   bool   `json:"auto_sync_enabled"`
	SyncIntervalHours int    `json:"sync_interval_hours"`
	ModelsDevURL      string `json:"models_dev_url"`
}

// PricingSyncMetadata records the latest models.dev sync result and filtered cache stats.
type PricingSyncMetadata struct {
	LastSyncAt           *time.Time `json:"last_sync_at,omitempty"`
	LastSyncStatus       string     `json:"last_sync_status"`
	LastSyncError        string     `json:"last_sync_error,omitempty"`
	CacheHash            string     `json:"cache_hash,omitempty"`
	ChannelCount         int        `json:"channel_count"`
	ModelCount           int        `json:"model_count"`
	RawEntryCount        int        `json:"raw_entry_count"`
	LastSkippedLocked    int        `json:"last_skipped_locked,omitempty"`
	LastOverwrittenCount int        `json:"last_overwritten_count,omitempty"`
}

// PricingSyncStatus aggregates sync config, metadata, and override/lock counts for the admin UI.
type PricingSyncStatus struct {
	Config               PricingSyncConfig   `json:"config"`
	Meta                 PricingSyncMetadata `json:"meta"`
	DefaultOverrideCount int                 `json:"default_override_count"`
	ChannelOverrideCount int                 `json:"channel_override_count"`
	LockedChannelCount   int                 `json:"locked_channel_count"`
	UnlockedCustomCount  int                 `json:"unlocked_custom_count"`
	GroupOverrideCounts  map[string]int      `json:"group_override_counts"`
	CacheLoaded          bool                `json:"cache_loaded"`
}

// TokenPricingPerMillion represents human-friendly USD per 1M token prices.
type TokenPricingPerMillion struct {
	DisplayName                   string   `json:"display_name,omitempty"`
	InputPerMillion               float64  `json:"input_per_million"`
	OutputPerMillion              float64  `json:"output_per_million"`
	CacheReadPerMillion           float64  `json:"cache_read_per_million"`
	CacheWritePerMillion          float64  `json:"cache_write_per_million"`
	CacheWrite1hPerMillion        *float64 `json:"cache_write_1h_per_million,omitempty"`
	CacheStoragePerMillionPerHour float64  `json:"cache_storage_per_million_per_hour,omitempty"`
	LongContextThreshold          int      `json:"long_context_threshold,omitempty"`
	LongContextInputMult          float64  `json:"long_context_input_multiplier,omitempty"`
	LongContextOutputMult         float64  `json:"long_context_output_multiplier,omitempty"`
}

// ModelPricingOverride stores a custom price override or locked price snapshot
// in Default, Account-Type Channel, or System Group scope.
type ModelPricingOverride struct {
	Platform  string                 `json:"platform,omitempty"`
	Custom    TokenPricingPerMillion `json:"custom"`
	Locked    bool                   `json:"locked,omitempty"`
	IsCustom  bool                   `json:"is_custom,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// PricingOverridesState persists global default edits/locks, per-channel edits/locks,
// and per-system-group independent pricing overrides.
type PricingOverridesState struct {
	Default  map[string]ModelPricingOverride            `json:"default"`
	Channels map[string]map[string]ModelPricingOverride `json:"channels,omitempty"`
	Groups   map[string]map[string]ModelPricingOverride `json:"groups"`
}

// UpsertPricingOverrideInput defines parameters for editing pricing at Default, Channel, or Group scope.
type UpsertPricingOverrideInput struct {
	GroupID   *int64
	ChannelID string
	ModelID   string
	Platform  string
	Custom    TokenPricingPerMillion
	Locked    *bool
}

// AccountChannelPriceEntry represents one account-type matched channel price cached from models.dev
// (with any custom edit and/or sync lock applied).
type AccountChannelPriceEntry struct {
	ChannelID     string                  `json:"channel_id"`
	ChannelName   string                  `json:"channel_name"`
	Platform      string                  `json:"platform"`
	RawModelID    string                  `json:"raw_model_id"`
	IsDefault     bool                    `json:"is_default"`
	IsOverridden  bool                    `json:"is_overridden"`
	IsLocked      bool                    `json:"is_locked"`
	Pricing       TokenPricingPerMillion  `json:"pricing"`
	SyncedPricing *TokenPricingPerMillion `json:"synced_pricing,omitempty"`
	UpdatedAt     *time.Time              `json:"updated_at,omitempty"`
	catalog       *CatalogModelPricing
	score         int
}

// SupportedModelEntry holds all cached channel prices for one system-supported model ID.
type SupportedModelEntry struct {
	ModelID            string
	DisplayName        string
	Family             string
	ReleaseDate        string
	Platform           string   // primary system platform (anthropic / openai / gemini / grok / kimi / zhipu / deepseek / minimax / opencode_go / command_code / cline / typesafe)
	Platforms          []string // all system platforms that support this model (including antigravity for claude/gemini)
	DefaultChannelID   string
	DefaultChannelName string
	DefaultPricing     TokenPricingPerMillion
	defaultCatalog     *CatalogModelPricing
	Channels           []AccountChannelPriceEntry
	channelByID        map[string]*AccountChannelPriceEntry
	rawByChannel       map[string]map[string]*CatalogModelPricing // channelID -> lower(rawModelID) -> catalog
}

// SupportedPlatformSummary summarizes available system platforms in the pricing catalog.
type SupportedPlatformSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ModelCount int    `json:"model_count"`
}

// ModelPricingListQuery defines query filters for listing system-supported model pricing.
type ModelPricingListQuery struct {
	GroupID       int64
	GroupPlatform string // resolved from system GroupRepository when GroupID > 0
	Platform      string // optional platform filter (used when GroupID == 0 or composite group)
	Search        string
	Filter        string // "all" | "overridden" | "custom"
	Page          int
	PageSize      int
}

// ModelPricingListItem is one top-level model row in the admin Model Pricing multi-level table.
type ModelPricingListItem struct {
	ModelID              string                     `json:"model_id"`
	DisplayName          string                     `json:"display_name"`
	Platform             string                     `json:"platform"`
	Platforms            []string                   `json:"platforms"`
	Family               string                     `json:"family,omitempty"`
	ReleaseDate          string                     `json:"release_date,omitempty"`
	Source               string                     `json:"source"` // "default_auto" | "default_custom" | "inherited_default" | "group_custom"
	IsGroupOverridden    bool                       `json:"is_group_overridden"`
	IsDefaultModified    bool                       `json:"is_default_modified"`
	IsDefaultLocked      bool                       `json:"is_default_locked"`
	IsCustomOnly         bool                       `json:"is_custom_only"`
	DefaultChannelID     string                     `json:"default_channel_id,omitempty"`
	DefaultChannelName   string                     `json:"default_channel_name,omitempty"`
	ChannelPrices        []AccountChannelPriceEntry `json:"channel_prices"`
	EffectivePricing     TokenPricingPerMillion     `json:"effective_pricing"`
	DefaultPricing       *TokenPricingPerMillion    `json:"default_pricing,omitempty"`
	GroupOverrideCount   int                        `json:"group_override_count"`
	ChannelOverrideCount int                        `json:"channel_override_count"`
	LockedChannelCount   int                        `json:"locked_channel_count"`
	UpdatedAt            *time.Time                 `json:"updated_at,omitempty"`
}

// ModelPricingListResult is the paginated response for listing model pricing.
type ModelPricingListResult struct {
	Items         []ModelPricingListItem     `json:"items"`
	Total         int                        `json:"total"`
	Page          int                        `json:"page"`
	PageSize      int                        `json:"page_size"`
	GroupPlatform string                     `json:"group_platform,omitempty"`
	Platforms     []SupportedPlatformSummary `json:"platforms"`
}

// PricingService 动态价格服务（基于 models.dev 过滤缓存 + 账户类型自动匹配渠道 + 系统分组独立定价）
type PricingService struct {
	cfg          *config.Config
	remoteClient PricingRemoteClient
	settingRepo  SettingRepository

	mu                   sync.RWMutex
	syncMu               sync.Mutex
	syncConfig           PricingSyncConfig
	syncMeta             PricingSyncMetadata
	overrides            PricingOverridesState
	models               map[string]*SupportedModelEntry // normalized original model ID -> system-supported model entry
	providerModels       map[string][]string             // models.dev provider_id -> sorted original model IDs
	platformSummaries    []SupportedPlatformSummary
	effectiveDefaultData map[string]*CatalogModelPricing // materialized default effective catalog for hot path
	lastUpdated          time.Time
	localHash            string

	stopCh           chan struct{}
	schedulerResetCh chan struct{}
	stopOnce         sync.Once
	wg               sync.WaitGroup
}

// NewPricingService 创建价格服务
func NewPricingService(cfg *config.Config, remoteClient PricingRemoteClient) *PricingService {
	syncCfg := defaultSyncConfigFromAppConfig(cfg)
	return &PricingService{
		cfg:          cfg,
		remoteClient: remoteClient,
		syncConfig:   syncCfg,
		syncMeta: PricingSyncMetadata{
			LastSyncStatus: PricingSyncStatusNever,
		},
		overrides: PricingOverridesState{
			Default:  make(map[string]ModelPricingOverride),
			Channels: make(map[string]map[string]ModelPricingOverride),
			Groups:   make(map[string]map[string]ModelPricingOverride),
		},
		models:               make(map[string]*SupportedModelEntry),
		providerModels:       make(map[string][]string),
		effectiveDefaultData: make(map[string]*CatalogModelPricing),
		stopCh:               make(chan struct{}),
		schedulerResetCh:     make(chan struct{}, 1),
	}
}

// SetSettingRepository attaches optional DB persistence for sync config, metadata, and overrides.
func (s *PricingService) SetSettingRepository(repo SettingRepository) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.settingRepo = repo
	s.mu.Unlock()
}

func defaultSyncConfigFromAppConfig(cfg *config.Config) PricingSyncConfig {
	out := PricingSyncConfig{
		AutoSyncEnabled:   true,
		SyncIntervalHours: DefaultPricingSyncIntervalHours,
		ModelsDevURL:      DefaultModelsDevURL,
	}
	if cfg == nil {
		return out
	}
	out.AutoSyncEnabled = cfg.Pricing.AutoSyncEnabled
	if cfg.Pricing.SyncIntervalHours >= MinPricingSyncIntervalHours && cfg.Pricing.SyncIntervalHours <= MaxPricingSyncIntervalHours {
		out.SyncIntervalHours = cfg.Pricing.SyncIntervalHours
	}
	if u := strings.TrimSpace(cfg.Pricing.ModelsDevURL); u != "" {
		out.ModelsDevURL = u
	}
	return out
}

// Initialize 初始化价格服务：加载数据库配置与分组覆盖 -> 加载本地缓存 -> 启动定时同步器。
func (s *PricingService) Initialize() error {
	if s.cfg != nil && strings.TrimSpace(s.cfg.Pricing.DataDir) != "" {
		if err := os.MkdirAll(s.cfg.Pricing.DataDir, 0755); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to create data directory: %v", err)
		}
	}

	s.loadPersistedState(context.Background())

	cachePath := s.getCatalogCachePath()
	cacheLoaded := false
	cacheStale := true
	if cachePath != "" {
		if err := s.loadCatalogFromDisk(cachePath); err == nil {
			cacheLoaded = true
			if info, statErr := os.Stat(cachePath); statErr == nil {
				s.mu.RLock()
				interval := time.Duration(s.normalizedSyncIntervalHoursLocked()) * time.Hour
				s.mu.RUnlock()
				cacheStale = time.Since(info.ModTime()) >= interval
			}
		} else if !os.IsNotExist(err) {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load cached models.dev catalog: %v", err)
		}
	}

	s.mu.Lock()
	s.rebuildEffectiveDefaultLocked()
	modelCount := len(s.effectiveDefaultData)
	autoSync := s.syncConfig.AutoSyncEnabled && strings.TrimSpace(s.syncConfig.ModelsDevURL) != ""
	s.mu.Unlock()

	s.startUpdateScheduler()

	if autoSync && s.remoteClient != nil && (!cacheLoaded || cacheStale) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ctx, cancel := s.backgroundSyncContext(60 * time.Second)
			defer cancel()
			if _, err := s.SyncNow(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.LegacyPrintf("service.pricing", "[Pricing] Initial models.dev sync failed: %v", err)
			}
		}()
	}

	logger.LegacyPrintf("service.pricing", "[Pricing] Service initialized (models.dev cache_loaded=%t, effective_models=%d)", cacheLoaded, modelCount)
	return nil
}

func (s *PricingService) backgroundSyncContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	done := make(chan struct{})
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		close(done)
		cancel()
	}
}

// Stop 停止价格服务
func (s *PricingService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
	logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Service stopped")
}

func (s *PricingService) startUpdateScheduler() {
	if s == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var timer *time.Timer
		resetTimer := func() <-chan time.Time {
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer = nil
			}
			s.mu.RLock()
			enabled := s.syncConfig.AutoSyncEnabled && strings.TrimSpace(s.syncConfig.ModelsDevURL) != "" && s.remoteClient != nil
			hours := s.normalizedSyncIntervalHoursLocked()
			s.mu.RUnlock()
			if !enabled {
				return nil
			}
			timer = time.NewTimer(time.Duration(hours) * time.Hour)
			return timer.C
		}

		tickCh := resetTimer()
		for {
			select {
			case <-tickCh:
				ctx, cancel := s.backgroundSyncContext(60 * time.Second)
				if _, err := s.SyncNow(ctx); err != nil && !errors.Is(err, context.Canceled) {
					logger.LegacyPrintf("service.pricing", "[Pricing] Scheduled models.dev sync failed: %v", err)
				}
				cancel()
				tickCh = resetTimer()
			case <-s.schedulerResetCh:
				tickCh = resetTimer()
			case <-s.stopCh:
				if timer != nil {
					timer.Stop()
				}
				return
			}
		}
	}()
}

func (s *PricingService) signalSchedulerReset() {
	if s == nil || s.schedulerResetCh == nil {
		return
	}
	select {
	case s.schedulerResetCh <- struct{}{}:
	default:
	}
}

func (s *PricingService) normalizedSyncIntervalHoursLocked() int {
	h := s.syncConfig.SyncIntervalHours
	if h < MinPricingSyncIntervalHours {
		return DefaultPricingSyncIntervalHours
	}
	if h > MaxPricingSyncIntervalHours {
		return MaxPricingSyncIntervalHours
	}
	return h
}

// --- Persistence helpers ---

func (s *PricingService) loadPersistedState(ctx context.Context) {
	s.mu.RLock()
	repo := s.settingRepo
	s.mu.RUnlock()
	if repo == nil {
		return
	}

	values, err := repo.GetMultiple(ctx, []string{
		SettingKeyModelPricingSyncConfig,
		SettingKeyModelPricingSyncMeta,
		SettingKeyModelPricingOverrides,
	})
	if err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load persisted state: %v", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if raw, ok := values[SettingKeyModelPricingSyncConfig]; ok && strings.TrimSpace(raw) != "" {
		var cfg PricingSyncConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
			if strings.TrimSpace(cfg.ModelsDevURL) == "" {
				cfg.ModelsDevURL = DefaultModelsDevURL
			}
			if cfg.SyncIntervalHours < MinPricingSyncIntervalHours || cfg.SyncIntervalHours > MaxPricingSyncIntervalHours {
				cfg.SyncIntervalHours = DefaultPricingSyncIntervalHours
			}
			s.syncConfig = cfg
		}
	}

	if raw, ok := values[SettingKeyModelPricingSyncMeta]; ok && strings.TrimSpace(raw) != "" {
		var meta PricingSyncMetadata
		if err := json.Unmarshal([]byte(raw), &meta); err == nil {
			s.syncMeta = meta
		}
	}

	if raw, ok := values[SettingKeyModelPricingOverrides]; ok && strings.TrimSpace(raw) != "" {
		var ov PricingOverridesState
		if err := json.Unmarshal([]byte(raw), &ov); err == nil {
			if ov.Default == nil {
				ov.Default = make(map[string]ModelPricingOverride)
			}
			for id, item := range ov.Default {
				if !item.IsCustom && !item.Locked {
					item.IsCustom = true
					ov.Default[id] = item
				}
			}
			if ov.Channels == nil {
				ov.Channels = make(map[string]map[string]ModelPricingOverride)
			}
			for chID, chMap := range ov.Channels {
				for id, item := range chMap {
					if !item.IsCustom && !item.Locked {
						item.IsCustom = true
						chMap[id] = item
					}
				}
				ov.Channels[chID] = chMap
			}
			if ov.Groups == nil {
				ov.Groups = make(map[string]map[string]ModelPricingOverride)
			}
			s.overrides = ov
		}
	}
}

func (s *PricingService) persistSettingJSON(ctx context.Context, key string, value any) error {
	s.mu.RLock()
	repo := s.settingRepo
	s.mu.RUnlock()
	if repo == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return repo.Set(ctx, key, string(encoded))
}

// --- Sync & Filtered Disk Cache ---

func (s *PricingService) getCatalogCachePath() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.DataDir) == "" {
		return ""
	}
	return filepath.Join(s.cfg.Pricing.DataDir, modelsDevCatalogFileName)
}

func (s *PricingService) loadCatalogFromDisk(filePath string) error {
	body, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	_, err = s.applyModelsDevCatalogBytes(body, false)
	return err
}

// LoadModelsDevJSON loads models.dev JSON bytes into memory (filtered to system-supported platforms/models).
func (s *PricingService) LoadModelsDevJSON(body []byte) error {
	_, err := s.applyModelsDevCatalogBytes(body, false)
	return err
}

func (s *PricingService) applyModelsDevCatalogBytes(body []byte, updateSyncMeta bool) ([]byte, error) {
	models, providerModels, platformSummaries, filteredRaw, channelCount, rawCount, err := parseSupportedModelsDevCatalog(body)
	if err != nil {
		return nil, err
	}

	filteredBytes, err := json.Marshal(filteredRaw)
	if err != nil {
		filteredBytes = body
	}

	sum := sha256.Sum256(filteredBytes)
	hashStr := hex.EncodeToString(sum[:])
	now := time.Now()

	s.mu.Lock()
	s.models = models
	s.providerModels = providerModels
	s.platformSummaries = platformSummaries
	s.localHash = hashStr
	s.lastUpdated = now
	s.syncMeta.CacheHash = hashStr[:min(16, len(hashStr))]
	s.syncMeta.ChannelCount = channelCount
	s.syncMeta.ModelCount = len(models)
	s.syncMeta.RawEntryCount = rawCount
	if updateSyncMeta {
		t := now
		s.syncMeta.LastSyncAt = &t
		s.syncMeta.LastSyncStatus = PricingSyncStatusSuccess
		s.syncMeta.LastSyncError = ""
	}
	s.rebuildEffectiveDefaultLocked()
	s.mu.Unlock()
	return filteredBytes, nil
}

// SyncNow fetches models.dev/api.json, filters to system-supported models & channels,
// skips locked channel prices, updates unlocked entries, and persists the slim filtered cache.
func (s *PricingService) SyncNow(ctx context.Context) (*PricingSyncStatus, error) {
	return s.SyncNowWithMode(ctx, PricingSyncOverwriteSkipLocked)
}

// SyncNowWithMode performs a models.dev sync with explicit overwrite semantics:
//   - "skip_locked" (default / auto-sync): keeps all locked channel prices untouched; updates unlocked entries.
//   - "overwrite_all" (manual confirmed overwrite): overwrites all synced channel prices (including locked ones),
//     while preserving custom-only models and system group overrides.
func (s *PricingService) SyncNowWithMode(ctx context.Context, overwriteMode string) (*PricingSyncStatus, error) {
	if s == nil {
		return nil, errors.New("pricing service is nil")
	}
	if s.remoteClient == nil {
		return nil, errors.New("pricing remote client is not configured")
	}

	mode := strings.ToLower(strings.TrimSpace(overwriteMode))
	if mode == "" {
		mode = PricingSyncOverwriteSkipLocked
	}
	if mode != PricingSyncOverwriteSkipLocked && mode != PricingSyncOverwriteAll {
		return nil, fmt.Errorf("invalid overwrite_mode %q (expected %q or %q)", overwriteMode, PricingSyncOverwriteSkipLocked, PricingSyncOverwriteAll)
	}

	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	s.mu.RLock()
	rawURL := strings.TrimSpace(s.syncConfig.ModelsDevURL)
	s.mu.RUnlock()
	if rawURL == "" {
		rawURL = DefaultModelsDevURL
	}

	validatedURL, err := s.validatePricingURL(rawURL)
	if err != nil {
		s.recordSyncError(ctx, err)
		return s.GetSyncStatus(), err
	}

	logger.LegacyPrintf("service.pricing", "[Pricing] Syncing supported models from %s (mode=%s)", validatedURL, mode)
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := s.remoteClient.FetchPricingJSON(ctx, validatedURL)
	if err != nil {
		err = fmt.Errorf("fetch models.dev catalog: %w", err)
		s.recordSyncError(ctx, err)
		return s.GetSyncStatus(), err
	}

	filteredBytes, err := s.applyModelsDevCatalogBytes(body, true)
	if err != nil {
		err = fmt.Errorf("parse models.dev catalog: %w", err)
		s.recordSyncError(ctx, err)
		return s.GetSyncStatus(), err
	}

	// Reconcile Default & Channel overrides against the freshly synced catalog according to overwrite_mode.
	// System Group overrides (s.overrides.Groups) and custom-only models are always preserved.
	var overridesChanged bool
	var overridesCopy PricingOverridesState
	s.mu.Lock()
	skippedLocked := 0
	overwrittenCount := 0

	for modelID, ov := range s.overrides.Default {
		if s.models[modelID] == nil {
			// Custom-only model not in models.dev: always keep.
			continue
		}
		if mode == PricingSyncOverwriteAll {
			delete(s.overrides.Default, modelID)
			overwrittenCount++
			overridesChanged = true
		} else {
			if ov.Locked {
				skippedLocked++
			} else {
				delete(s.overrides.Default, modelID)
				overwrittenCount++
				overridesChanged = true
			}
		}
	}

	for chID, chModels := range s.overrides.Channels {
		for modelID, ov := range chModels {
			entry := s.models[modelID]
			if entry == nil || entry.channelByID[chID] == nil {
				continue
			}
			if mode == PricingSyncOverwriteAll {
				delete(chModels, modelID)
				overwrittenCount++
				overridesChanged = true
			} else {
				if ov.Locked {
					skippedLocked++
				} else {
					delete(chModels, modelID)
					overwrittenCount++
					overridesChanged = true
				}
			}
		}
		if len(chModels) == 0 {
			delete(s.overrides.Channels, chID)
		}
	}

	s.syncMeta.LastSkippedLocked = skippedLocked
	s.syncMeta.LastOverwrittenCount = overwrittenCount
	if overridesChanged {
		s.rebuildEffectiveDefaultLocked()
		overridesCopy = clonePricingOverridesState(s.overrides)
	}
	metaCopy := s.syncMeta
	s.mu.Unlock()

	if overridesChanged {
		if err := s.persistSettingJSON(ctx, SettingKeyModelPricingOverrides, overridesCopy); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to persist reconciled overrides: %v", err)
		}
	}

	// Atomically write slim filtered cache to disk
	if cachePath := s.getCatalogCachePath(); cachePath != "" {
		if mkErr := os.MkdirAll(filepath.Dir(cachePath), 0755); mkErr == nil {
			tmpPath := cachePath + ".tmp"
			if writeErr := os.WriteFile(tmpPath, filteredBytes, 0644); writeErr == nil {
				if renameErr := os.Rename(tmpPath, cachePath); renameErr != nil {
					_ = os.WriteFile(cachePath, filteredBytes, 0644)
					_ = os.Remove(tmpPath)
				}
			} else {
				logger.LegacyPrintf("service.pricing", "[Pricing] Failed to write models.dev cache file: %v", writeErr)
			}
		}
	}

	if err := s.persistSettingJSON(ctx, SettingKeyModelPricingSyncMeta, metaCopy); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to persist sync metadata: %v", err)
	}

	logger.LegacyPrintf("service.pricing", "[Pricing] Synced supported models.dev pricing (%d channels, %d models, %d entries, skipped_locked=%d, overwritten=%d)",
		metaCopy.ChannelCount, metaCopy.ModelCount, metaCopy.RawEntryCount, skippedLocked, overwrittenCount)
	return s.GetSyncStatus(), nil
}

func (s *PricingService) recordSyncError(ctx context.Context, syncErr error) {
	now := time.Now()
	s.mu.Lock()
	s.syncMeta.LastSyncAt = &now
	s.syncMeta.LastSyncStatus = PricingSyncStatusFailed
	s.syncMeta.LastSyncError = syncErr.Error()
	metaCopy := s.syncMeta
	s.mu.Unlock()
	_ = s.persistSettingJSON(ctx, SettingKeyModelPricingSyncMeta, metaCopy)
}

// ForceUpdate 强制立即同步 models.dev 数据
func (s *PricingService) ForceUpdate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := s.SyncNow(ctx)
	return err
}

// UpdateSyncConfig updates sync configuration, persists it, and reschedules background sync.
func (s *PricingService) UpdateSyncConfig(ctx context.Context, next PricingSyncConfig) (*PricingSyncStatus, error) {
	next.ModelsDevURL = strings.TrimSpace(next.ModelsDevURL)
	if next.ModelsDevURL == "" {
		next.ModelsDevURL = DefaultModelsDevURL
	}
	if _, err := s.validatePricingURL(next.ModelsDevURL); err != nil {
		return nil, err
	}
	if next.SyncIntervalHours < MinPricingSyncIntervalHours || next.SyncIntervalHours > MaxPricingSyncIntervalHours {
		return nil, fmt.Errorf("sync_interval_hours must be between %d and %d", MinPricingSyncIntervalHours, MaxPricingSyncIntervalHours)
	}

	s.mu.Lock()
	s.syncConfig = next
	s.mu.Unlock()

	if err := s.persistSettingJSON(ctx, SettingKeyModelPricingSyncConfig, next); err != nil {
		return nil, fmt.Errorf("persist sync config: %w", err)
	}
	s.signalSchedulerReset()
	return s.GetSyncStatus(), nil
}

// GetSyncStatus returns the current sync config, metadata, and override/lock summary.
func (s *PricingService) GetSyncStatus() *PricingSyncStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	groupCounts := make(map[string]int, len(s.overrides.Groups))
	for gid, models := range s.overrides.Groups {
		if len(models) > 0 {
			groupCounts[gid] = len(models)
		}
	}

	defaultOverrideCount := 0
	channelOverrideCount := 0
	lockedChannelCount := 0
	unlockedCustomCount := 0

	for modelID, ov := range s.overrides.Default {
		isCustom := ov.IsCustom || !ov.Locked
		if isCustom {
			defaultOverrideCount++
		}
		if s.models[modelID] != nil {
			if ov.Locked {
				lockedChannelCount++
			} else if isCustom {
				unlockedCustomCount++
			}
		}
	}
	for chID, chModels := range s.overrides.Channels {
		for modelID, ov := range chModels {
			isCustom := ov.IsCustom || !ov.Locked
			if isCustom {
				channelOverrideCount++
			}
			if ov.Locked {
				lockedChannelCount++
			} else if isCustom {
				if entry := s.models[modelID]; entry != nil && entry.channelByID[chID] != nil {
					unlockedCustomCount++
				}
			}
		}
	}

	return &PricingSyncStatus{
		Config:               s.syncConfig,
		Meta:                 s.syncMeta,
		DefaultOverrideCount: defaultOverrideCount,
		ChannelOverrideCount: channelOverrideCount,
		LockedChannelCount:   lockedChannelCount,
		UnlockedCustomCount:  unlockedCustomCount,
		GroupOverrideCounts:  groupCounts,
		CacheLoaded:          len(s.models) > 0,
	}
}

// GetStatus 获取服务状态
func (s *PricingService) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"source":            "models.dev",
		"model_count":       len(s.effectiveDefaultData),
		"channel_count":     s.syncMeta.ChannelCount,
		"last_updated":      s.lastUpdated,
		"local_hash":        s.localHash[:min(8, len(s.localHash))],
		"auto_sync_enabled": s.syncConfig.AutoSyncEnabled,
	}
}

func (s *PricingService) validatePricingURL(raw string) (string, error) {
	if s.cfg == nil || !s.cfg.Security.URLAllowlist.Enabled {
		allowHTTP := s.cfg == nil || s.cfg.Security.URLAllowlist.AllowInsecureHTTP
		normalized, err := urlvalidator.ValidateURLFormat(raw, allowHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid pricing url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.PricingHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid pricing url: %w", err)
	}
	return normalized, nil
}

// --- models.dev parsing & system-supported filtering ---

// NormalizeOriginalModelID extracts the canonical original model ID so the same model
// across different account-type channels (e.g., "anthropic/claude-opus-5",
// "global.anthropic.claude-opus-5-v1:0", "claude-opus-5@default") aligns cleanly.
func NormalizeOriginalModelID(rawModelID string) string {
	id := strings.TrimSpace(rawModelID)
	if id == "" {
		return ""
	}
	if idx := strings.LastIndex(id, "/"); idx != -1 {
		id = id[idx+1:]
	}
	id = strings.TrimPrefix(id, "~")
	id = strings.TrimSpace(strings.ToLower(id))

	if strings.HasSuffix(id, "[1m]") {
		id = strings.TrimSpace(strings.TrimSuffix(id, "[1m]"))
	}

	id = bedrockRegionPrefixRe.ReplaceAllString(id, "")
	id = bedrockVendorPrefixRe.ReplaceAllString(id, "")
	id = bedrockVersionSuffixRe.ReplaceAllString(id, "")

	id = strings.TrimSuffix(id, "@default")
	id = vertexDateSuffixRe.ReplaceAllString(id, "-$1")
	id = strings.ReplaceAll(id, "@", "-")

	if idx := strings.Index(id, ":"); idx != -1 {
		id = id[:idx]
	}

	return strings.TrimSpace(id)
}

func inferSystemPlatformForModel(origID, fallbackPlatform string) string {
	switch {
	case strings.HasPrefix(origID, "claude-"):
		return PlatformAnthropic
	case strings.HasPrefix(origID, "gpt-"),
		strings.HasPrefix(origID, "o1"),
		strings.HasPrefix(origID, "o3"),
		strings.HasPrefix(origID, "o4"),
		strings.HasPrefix(origID, "chatgpt-"),
		strings.HasPrefix(origID, "codex-"),
		strings.HasPrefix(origID, "text-embedding-"),
		strings.HasPrefix(origID, "dall-e"),
		strings.HasPrefix(origID, "whisper"),
		strings.HasPrefix(origID, "tts-"):
		return PlatformOpenAI
	case strings.HasPrefix(origID, "gemini-"),
		strings.HasPrefix(origID, "gemma-"),
		strings.HasPrefix(origID, "imagen-"),
		strings.HasPrefix(origID, "veo-"),
		strings.HasPrefix(origID, "deep-research-"):
		return PlatformGemini
	case strings.HasPrefix(origID, "grok-"),
		strings.HasPrefix(origID, "composer-"):
		return PlatformGrok
	case strings.HasPrefix(origID, "deepseek-"):
		return PlatformDeepseek
	case strings.HasPrefix(origID, "glm-"):
		return PlatformZhipu
	case strings.HasPrefix(origID, "kimi-"),
		strings.HasPrefix(origID, "moonshot-"):
		return PlatformKimi
	case strings.HasPrefix(origID, "minimax-"):
		return PlatformMiniMax
	case strings.HasPrefix(origID, "jev-"):
		return PlatformTypeSafe
	default:
		if fallbackPlatform != "" {
			return fallbackPlatform
		}
		return PlatformOpenAI
	}
}

func defaultProviderForSystemPlatform(platform string) string {
	if spec, ok := domain.LookupPlatform(platform); ok && spec.ModelsDevProvider != "" {
		return spec.ModelsDevProvider
	}
	return ""
}

func scoreChannelEntry(spec supportedModelsDevProviderSpec, origID, rawModelID string, raw modelsDevRawModel) int {
	score := 0
	primaryPlatform := inferSystemPlatformForModel(origID, spec.Platform)
	if spec.Platform == primaryPlatform && spec.IsDefault && !spec.IsAggregator {
		score += 2000
	} else if spec.Platform == primaryPlatform && !spec.IsAggregator {
		score += 1000
	} else if spec.IsDefault {
		score += 400
	}
	// Prefer exact non-regional canonical ID within the same provider (e.g. global.anthropic.* or bare id)
	lowerRaw := strings.ToLower(strings.TrimSpace(rawModelID))
	if lowerRaw == origID {
		score += 100
	} else if strings.HasPrefix(lowerRaw, "global.") {
		score += 60
	} else if strings.HasPrefix(lowerRaw, "anthropic.") || strings.HasSuffix(lowerRaw, "@default") {
		score += 40
	}
	if raw.Cost != nil {
		if raw.Cost.CacheRead != nil && *raw.Cost.CacheRead > 0 {
			score += 15
		}
		if raw.Cost.CacheWrite != nil && *raw.Cost.CacheWrite > 0 {
			score += 15
		}
		if len(raw.Cost.Tiers) > 0 || raw.Cost.ContextOver200k != nil {
			score += 10
		}
	}
	if strings.EqualFold(raw.Status, "deprecated") {
		score -= 400
	}
	return score
}

func parseSupportedModelsDevCatalog(body []byte) (
	map[string]*SupportedModelEntry,
	map[string][]string,
	[]SupportedPlatformSummary,
	map[string]modelsDevRawProvider,
	int,
	int,
	error,
) {
	var rawProviders map[string]modelsDevRawProvider
	if err := json.Unmarshal(body, &rawProviders); err != nil {
		return nil, nil, nil, nil, 0, 0, fmt.Errorf("unmarshal models.dev JSON: %w", err)
	}

	models := make(map[string]*SupportedModelEntry)
	platformSetsByModel := make(map[string]map[string]struct{})
	providerModelSets := make(map[string]map[string]struct{})
	filteredRaw := make(map[string]modelsDevRawProvider)
	activeChannels := make(map[string]struct{})
	rawEntryCount := 0

	for provKey, prov := range rawProviders {
		providerID := strings.ToLower(strings.TrimSpace(prov.ID))
		if providerID == "" {
			providerID = strings.ToLower(strings.TrimSpace(provKey))
		}
		spec, supported := supportedModelsDevProviderMap[providerID]
		if !supported || len(prov.Models) == 0 {
			continue
		}

		keptRawModels := make(map[string]modelsDevRawModel)
		for modelKey, rawModel := range prov.Models {
			if rawModel.Cost == nil {
				continue
			}
			if rawModel.Cost.Input == nil && rawModel.Cost.Output == nil {
				continue
			}

			rawModelID := strings.TrimSpace(rawModel.ID)
			if rawModelID == "" {
				rawModelID = strings.TrimSpace(modelKey)
			}
			origID := NormalizeOriginalModelID(rawModelID)
			if origID == "" {
				continue
			}
			if spec.ModelFilter != nil && !spec.ModelFilter(origID, rawModel) {
				continue
			}

			keptRawModels[modelKey] = rawModel
			pricingPM, catalogPricing := convertModelsDevCost(providerID, rawModel)
			displayName := strings.TrimSpace(rawModel.Name)
			if displayName == "" {
				displayName = origID
			}

			entry := models[origID]
			if entry == nil {
				primaryPlatform := inferSystemPlatformForModel(origID, spec.Platform)
				entry = &SupportedModelEntry{
					ModelID:      origID,
					DisplayName:  displayName,
					Family:       strings.TrimSpace(rawModel.Family),
					ReleaseDate:  strings.TrimSpace(rawModel.ReleaseDate),
					Platform:     primaryPlatform,
					channelByID:  make(map[string]*AccountChannelPriceEntry),
					rawByChannel: make(map[string]map[string]*CatalogModelPricing),
				}
				models[origID] = entry
				platformSetsByModel[origID] = make(map[string]struct{})
				platformSetsByModel[origID][primaryPlatform] = struct{}{}
				if primaryPlatform == PlatformAnthropic || primaryPlatform == PlatformGemini {
					platformSetsByModel[origID][PlatformAntigravity] = struct{}{}
				}
			}
			platformSetsByModel[origID][spec.Platform] = struct{}{}

			// Preserve exact raw model ID entry per channel (crucial for Bedrock global vs us/eu regional pricing!)
			if entry.rawByChannel[providerID] == nil {
				entry.rawByChannel[providerID] = make(map[string]*CatalogModelPricing)
			}
			entry.rawByChannel[providerID][strings.ToLower(rawModelID)] = catalogPricing

			candScore := scoreChannelEntry(spec, origID, rawModelID, rawModel)
			existingCh := entry.channelByID[providerID]
			if existingCh == nil || candScore > existingCh.score {
				chEntry := &AccountChannelPriceEntry{
					ChannelID:   providerID,
					ChannelName: spec.ChannelLabel,
					Platform:    spec.Platform,
					RawModelID:  rawModelID,
					Pricing:     pricingPM,
					catalog:     catalogPricing,
					score:       candScore,
				}
				entry.channelByID[providerID] = chEntry
			}

			if providerModelSets[providerID] == nil {
				providerModelSets[providerID] = make(map[string]struct{})
			}
			providerModelSets[providerID][origID] = struct{}{}
			activeChannels[providerID] = struct{}{}
			rawEntryCount++
		}

		if len(keptRawModels) > 0 {
			filteredRaw[providerID] = modelsDevRawProvider{
				ID:     providerID,
				Name:   spec.ChannelLabel,
				API:    prov.API,
				Models: keptRawModels,
			}
		}
	}

	if len(models) == 0 {
		return nil, nil, nil, nil, 0, 0, fmt.Errorf("no valid supported pricing entries found in models.dev catalog")
	}

	platformModelCounts := make(map[string]int)
	for origID, entry := range models {
		channels := make([]AccountChannelPriceEntry, 0, len(entry.channelByID))
		for _, ch := range entry.channelByID {
			channels = append(channels, *ch)
		}
		sort.SliceStable(channels, func(i, j int) bool {
			if channels[i].score != channels[j].score {
				return channels[i].score > channels[j].score
			}
			return channels[i].ChannelID < channels[j].ChannelID
		})
		channels[0].IsDefault = true
		entry.Channels = channels
		entry.DefaultChannelID = channels[0].ChannelID
		entry.DefaultChannelName = channels[0].ChannelName
		entry.DefaultPricing = channels[0].Pricing
		entry.defaultCatalog = channels[0].catalog
		if channels[0].Pricing.DisplayName != "" {
			entry.DisplayName = channels[0].Pricing.DisplayName
		}

		// Rebuild channelByID pointers against sorted slice
		entry.channelByID = make(map[string]*AccountChannelPriceEntry, len(channels))
		for i := range entry.Channels {
			entry.channelByID[entry.Channels[i].ChannelID] = &entry.Channels[i]
		}

		// Collect platforms in canonical domain.Platforms() display order
		pSet := platformSetsByModel[origID]
		orderedPlatforms := make([]string, 0, len(pSet))
		for _, pSpec := range domain.Platforms() {
			if _, ok := pSet[pSpec.ID]; ok {
				orderedPlatforms = append(orderedPlatforms, pSpec.ID)
				platformModelCounts[pSpec.ID]++
			}
		}
		entry.Platforms = orderedPlatforms
	}

	providerModels := make(map[string][]string, len(providerModelSets))
	for pid, set := range providerModelSets {
		list := make([]string, 0, len(set))
		for m := range set {
			list = append(list, m)
		}
		sort.Strings(list)
		providerModels[pid] = list
	}

	platformSummaries := make([]SupportedPlatformSummary, 0, len(domain.Platforms()))
	for _, pSpec := range domain.Platforms() {
		platformSummaries = append(platformSummaries, SupportedPlatformSummary{
			ID:         pSpec.ID,
			Name:       pSpec.DisplayName,
			ModelCount: platformModelCounts[pSpec.ID],
		})
	}

	return models, providerModels, platformSummaries, filteredRaw, len(activeChannels), rawEntryCount, nil
}

func convertModelsDevCost(providerID string, raw modelsDevRawModel) (TokenPricingPerMillion, *CatalogModelPricing) {
	c := raw.Cost
	var inPM, outPM, cacheReadPM, cacheWritePM float64
	if c.Input != nil && *c.Input >= 0 {
		inPM = *c.Input
	}
	if c.Output != nil && *c.Output >= 0 {
		outPM = *c.Output
	}
	if c.CacheRead != nil && *c.CacheRead >= 0 {
		cacheReadPM = *c.CacheRead
	}
	if c.CacheWrite != nil && *c.CacheWrite >= 0 {
		cacheWritePM = *c.CacheWrite
	}

	threshold, inMult, outMult := extractLongContextFromModelsDevCost(c, inPM, outPM)

	pm := TokenPricingPerMillion{
		DisplayName:           strings.TrimSpace(raw.Name),
		InputPerMillion:       inPM,
		OutputPerMillion:      outPM,
		CacheReadPerMillion:   cacheReadPM,
		CacheWritePerMillion:  cacheWritePM,
		LongContextThreshold:  threshold,
		LongContextInputMult:  inMult,
		LongContextOutputMult: outMult,
	}

	mode := "chat"
	if raw.Modalities != nil && len(raw.Modalities.Output) > 0 {
		hasText := false
		hasImage := false
		for _, m := range raw.Modalities.Output {
			switch strings.ToLower(m) {
			case "text":
				hasText = true
			case "image":
				hasImage = true
			}
		}
		if hasImage && !hasText {
			mode = "image_generation"
		}
	}

	cat := tokenPricingPerMillionToCatalog(pm, providerID, mode, c.CacheWrite != nil)
	return pm, cat
}

func extractLongContextFromModelsDevCost(c *modelsDevRawCost, baseInPM, baseOutPM float64) (int, float64, float64) {
	if c == nil {
		return 0, 0, 0
	}
	bestThreshold := 0
	var tierIn, tierOut float64

	for _, t := range c.Tiers {
		if t.Tier == nil || !strings.EqualFold(t.Tier.Type, "context") || t.Tier.Size <= 0 {
			continue
		}
		if bestThreshold == 0 || t.Tier.Size < bestThreshold {
			bestThreshold = t.Tier.Size
			tierIn, tierOut = 0, 0
			if t.Input != nil && *t.Input > 0 {
				tierIn = *t.Input
			}
			if t.Output != nil && *t.Output > 0 {
				tierOut = *t.Output
			}
		}
	}
	if bestThreshold == 0 && c.ContextOver200k != nil {
		bestThreshold = 200_000
		if c.ContextOver200k.Input != nil && *c.ContextOver200k.Input > 0 {
			tierIn = *c.ContextOver200k.Input
		}
		if c.ContextOver200k.Output != nil && *c.ContextOver200k.Output > 0 {
			tierOut = *c.ContextOver200k.Output
		}
	}
	if bestThreshold <= 0 {
		return 0, 0, 0
	}

	inMult, outMult := 1.0, 1.0
	if tierIn > 0 && baseInPM > 0 {
		inMult = tierIn / baseInPM
	}
	if tierOut > 0 && baseOutPM > 0 {
		outMult = tierOut / baseOutPM
	}
	if inMult <= 1 && outMult <= 1 {
		return 0, 0, 0
	}
	return bestThreshold, roundMultiplier(inMult), roundMultiplier(outMult)
}

func roundMultiplier(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func tokenPricingPerMillionToCatalog(pm TokenPricingPerMillion, provider, mode string, explicitCacheWrite bool) *CatalogModelPricing {
	if mode == "" {
		mode = "chat"
	}
	cat := &CatalogModelPricing{
		InputCostPerToken:                   pm.InputPerMillion / tokensPerMillion,
		OutputCostPerToken:                  pm.OutputPerMillion / tokensPerMillion,
		CacheReadInputTokenCost:             pm.CacheReadPerMillion / tokensPerMillion,
		CacheCreationInputTokenCost:         pm.CacheWritePerMillion / tokensPerMillion,
		CacheCreationInputTokenCostExplicit: explicitCacheWrite || pm.CacheWritePerMillion > 0,
		LongContextInputTokenThreshold:      pm.LongContextThreshold,
		LongContextInputCostMultiplier:      pm.LongContextInputMult,
		LongContextOutputCostMultiplier:     pm.LongContextOutputMult,
		CacheStorageCostPerTokenPerHour:     pm.CacheStoragePerMillionPerHour / tokensPerMillion,
		Provider:                            provider,
		Mode:                                mode,
		SupportsPromptCaching:               pm.CacheReadPerMillion > 0 || pm.CacheWritePerMillion > 0,
	}
	if pm.CacheWrite1hPerMillion != nil && *pm.CacheWrite1hPerMillion >= 0 {
		cat.CacheCreationInputTokenCostAbove1hr = *pm.CacheWrite1hPerMillion / tokensPerMillion
		cat.SupportsPromptCaching = true
	}
	return cat
}

func overrideToCatalog(ov ModelPricingOverride) *CatalogModelPricing {
	cat := tokenPricingPerMillionToCatalog(ov.Custom, "custom", "chat", true)
	cat.priceOverrides = tokenPriceOverrides{
		input:     true,
		output:    true,
		cacheRead: true,
	}
	return cat
}

// rebuildEffectiveDefaultLocked rebuilds s.effectiveDefaultData from supported models + Default overrides.
func (s *PricingService) rebuildEffectiveDefaultLocked() {
	next := make(map[string]*CatalogModelPricing, len(s.models)+len(s.overrides.Default))

	for modelID, entry := range s.models {
		if entry.defaultCatalog != nil {
			next[modelID] = entry.defaultCatalog
		}
	}

	for modelID, ov := range s.overrides.Default {
		normID := NormalizeOriginalModelID(modelID)
		if normID == "" {
			continue
		}
		next[normID] = overrideToCatalog(ov)
	}

	s.effectiveDefaultData = next
}

// --- Admin Mutations (Edit custom price for Default, Account-Type Channel, or System Group; Lock; Reset) ---

// UpsertCustomPricing saves custom per-million-token prices for a model
// (either in default scope when groupID == nil/<=0, or for a specific system group).
func (s *PricingService) UpsertCustomPricing(ctx context.Context, groupID *int64, modelID, platform string, custom TokenPricingPerMillion) error {
	return s.UpsertCustomPricingWithOptions(ctx, UpsertPricingOverrideInput{
		GroupID:  groupID,
		ModelID:  modelID,
		Platform: platform,
		Custom:   custom,
	})
}

// UpsertCustomPricingWithOptions saves custom prices at one of three scopes:
//   - GroupID > 0: System Group override (highest priority, never overwritten by sync).
//   - ChannelID != "": Account-Type Channel override (if equal to model's default channel, unified with Default).
//   - Otherwise: Global Default override.
func (s *PricingService) UpsertCustomPricingWithOptions(ctx context.Context, opts UpsertPricingOverrideInput) error {
	normID := NormalizeOriginalModelID(opts.ModelID)
	if normID == "" {
		return errors.New("model_id is required")
	}
	channelID := strings.ToLower(strings.TrimSpace(opts.ChannelID))
	hasGroup := opts.GroupID != nil && *opts.GroupID > 0
	if hasGroup && channelID != "" {
		return errors.New("group_id and channel_id cannot both be specified")
	}
	if err := validateCustomTokenPricing(opts.Custom); err != nil {
		return err
	}

	custom := opts.Custom
	custom.DisplayName = strings.TrimSpace(custom.DisplayName)
	platform := strings.ToLower(strings.TrimSpace(opts.Platform))

	locked := true
	if opts.Locked != nil {
		locked = *opts.Locked
	}

	ov := ModelPricingOverride{
		Platform:  platform,
		Custom:    custom,
		Locked:    locked,
		IsCustom:  true,
		UpdatedAt: time.Now(),
	}

	s.mu.Lock()
	entry := s.models[normID]
	if ov.Platform == "" {
		if entry != nil {
			ov.Platform = entry.Platform
		} else {
			ov.Platform = inferSystemPlatformForModel(normID, "")
		}
	}

	switch {
	case hasGroup:
		ov.Locked = false // system group overrides are always independent of models.dev sync
		gidKey := strconv.FormatInt(*opts.GroupID, 10)
		if s.overrides.Groups == nil {
			s.overrides.Groups = make(map[string]map[string]ModelPricingOverride)
		}
		if s.overrides.Groups[gidKey] == nil {
			s.overrides.Groups[gidKey] = make(map[string]ModelPricingOverride)
		}
		s.overrides.Groups[gidKey][normID] = ov

	case channelID != "" && (entry == nil || channelID != entry.DefaultChannelID):
		if _, knownSpec := supportedModelsDevProviderMap[channelID]; !knownSpec && (entry == nil || entry.channelByID[channelID] == nil) {
			s.mu.Unlock()
			return fmt.Errorf("unsupported channel_id: %s", channelID)
		}
		if s.overrides.Channels == nil {
			s.overrides.Channels = make(map[string]map[string]ModelPricingOverride)
		}
		if s.overrides.Channels[channelID] == nil {
			s.overrides.Channels[channelID] = make(map[string]ModelPricingOverride)
		}
		s.overrides.Channels[channelID][normID] = ov

	default:
		if s.overrides.Default == nil {
			s.overrides.Default = make(map[string]ModelPricingOverride)
		}
		if channelID != "" && ov.Custom.DisplayName == "" {
			if existing, ok := s.overrides.Default[normID]; ok && existing.Custom.DisplayName != "" {
				ov.Custom.DisplayName = existing.Custom.DisplayName
			}
		}
		s.overrides.Default[normID] = ov
		// Keep default channel unified in Default (remove any stale duplicate under Channels[DefaultChannelID])
		if entry != nil && entry.DefaultChannelID != "" {
			if cm := s.overrides.Channels[entry.DefaultChannelID]; cm != nil {
				delete(cm, normID)
				if len(cm) == 0 {
					delete(s.overrides.Channels, entry.DefaultChannelID)
				}
			}
		}
		s.rebuildEffectiveDefaultLocked()
	}

	overridesCopy := clonePricingOverridesState(s.overrides)
	s.mu.Unlock()

	return s.persistSettingJSON(ctx, SettingKeyModelPricingOverrides, overridesCopy)
}

// SetChannelPriceLock locks or unlocks a specific (model_id, channel_id) channel price so automatic
// sync skips locked channels on subsequent updates.
func (s *PricingService) SetChannelPriceLock(ctx context.Context, modelID, channelID string, locked bool) error {
	normID := NormalizeOriginalModelID(modelID)
	if normID == "" {
		return errors.New("model_id is required")
	}
	channelID = strings.ToLower(strings.TrimSpace(channelID))

	s.mu.Lock()
	entry := s.models[normID]
	if channelID == "" && entry != nil {
		channelID = entry.DefaultChannelID
	}

	isDefaultChannel := entry == nil || channelID == "" || channelID == entry.DefaultChannelID
	now := time.Now()

	if isDefaultChannel {
		existing, hasExisting := s.overrides.Default[normID]
		if locked {
			if hasExisting {
				if !existing.Locked {
					existing.IsCustom = true
				}
				existing.Locked = true
				existing.UpdatedAt = now
				s.overrides.Default[normID] = existing
			} else {
				if entry == nil {
					s.mu.Unlock()
					return fmt.Errorf("model %q not found", normID)
				}
				if s.overrides.Default == nil {
					s.overrides.Default = make(map[string]ModelPricingOverride)
				}
				s.overrides.Default[normID] = ModelPricingOverride{
					Platform:  entry.Platform,
					Custom:    entry.DefaultPricing,
					Locked:    true,
					IsCustom:  false,
					UpdatedAt: now,
				}
			}
		} else if hasExisting {
			if existing.IsCustom {
				existing.Locked = false
				existing.UpdatedAt = now
				s.overrides.Default[normID] = existing
			} else {
				delete(s.overrides.Default, normID)
			}
		}
		s.rebuildEffectiveDefaultLocked()
	} else {
		var chEntry *AccountChannelPriceEntry
		if entry != nil {
			chEntry = entry.channelByID[channelID]
		}
		chMap := s.overrides.Channels[channelID]
		existing, hasExisting := ModelPricingOverride{}, false
		if chMap != nil {
			existing, hasExisting = chMap[normID]
		}
		if locked {
			if hasExisting {
				if !existing.Locked {
					existing.IsCustom = true
				}
				existing.Locked = true
				existing.UpdatedAt = now
				chMap[normID] = existing
			} else {
				if chEntry == nil {
					s.mu.Unlock()
					return fmt.Errorf("channel %q not found for model %q", channelID, normID)
				}
				if s.overrides.Channels == nil {
					s.overrides.Channels = make(map[string]map[string]ModelPricingOverride)
				}
				if s.overrides.Channels[channelID] == nil {
					s.overrides.Channels[channelID] = make(map[string]ModelPricingOverride)
				}
				s.overrides.Channels[channelID][normID] = ModelPricingOverride{
					Platform:  entry.Platform,
					Custom:    chEntry.Pricing,
					Locked:    true,
					IsCustom:  false,
					UpdatedAt: now,
				}
			}
		} else if hasExisting {
			if existing.IsCustom {
				existing.Locked = false
				existing.UpdatedAt = now
				chMap[normID] = existing
			} else {
				delete(chMap, normID)
				if len(chMap) == 0 {
					delete(s.overrides.Channels, channelID)
				}
			}
		}
	}

	overridesCopy := clonePricingOverridesState(s.overrides)
	s.mu.Unlock()

	return s.persistSettingJSON(ctx, SettingKeyModelPricingOverrides, overridesCopy)
}

// ResetModelPricing resets a model's override in default or group scope.
func (s *PricingService) ResetModelPricing(ctx context.Context, groupID *int64, modelID string) error {
	return s.ResetModelPricingWithScope(ctx, groupID, "", modelID)
}

// ResetModelPricingWithScope resets a model's override/lock at Default, Channel, or Group scope:
//   - If groupID > 0: removes the group override so the group falls back to matched channel / default price.
//   - If channelID != "": removes the channel override/lock (unified with Default if default channel).
//   - Otherwise: removes the default override/lock (restores models.dev default, or deletes custom-only model).
func (s *PricingService) ResetModelPricingWithScope(ctx context.Context, groupID *int64, channelID, modelID string) error {
	normID := NormalizeOriginalModelID(modelID)
	if normID == "" {
		return errors.New("model_id is required")
	}
	channelID = strings.ToLower(strings.TrimSpace(channelID))
	hasGroup := groupID != nil && *groupID > 0
	if hasGroup && channelID != "" {
		return errors.New("group_id and channel_id cannot both be specified")
	}

	s.mu.Lock()
	entry := s.models[normID]
	switch {
	case hasGroup:
		gidKey := strconv.FormatInt(*groupID, 10)
		if gm := s.overrides.Groups[gidKey]; gm != nil {
			delete(gm, normID)
			if len(gm) == 0 {
				delete(s.overrides.Groups, gidKey)
			}
		}
	case channelID != "" && (entry == nil || channelID != entry.DefaultChannelID):
		if cm := s.overrides.Channels[channelID]; cm != nil {
			delete(cm, normID)
			if len(cm) == 0 {
				delete(s.overrides.Channels, channelID)
			}
		}
	default:
		delete(s.overrides.Default, normID)
		if entry != nil && entry.DefaultChannelID != "" {
			if cm := s.overrides.Channels[entry.DefaultChannelID]; cm != nil {
				delete(cm, normID)
				if len(cm) == 0 {
					delete(s.overrides.Channels, entry.DefaultChannelID)
				}
			}
		}
		s.rebuildEffectiveDefaultLocked()
	}
	overridesCopy := clonePricingOverridesState(s.overrides)
	s.mu.Unlock()

	return s.persistSettingJSON(ctx, SettingKeyModelPricingOverrides, overridesCopy)
}

func validateCustomTokenPricing(p TokenPricingPerMillion) error {
	checkNonNeg := func(name string, v float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("%s must be a non-negative finite number", name)
		}
		return nil
	}
	if err := checkNonNeg("input_per_million", p.InputPerMillion); err != nil {
		return err
	}
	if err := checkNonNeg("output_per_million", p.OutputPerMillion); err != nil {
		return err
	}
	if err := checkNonNeg("cache_read_per_million", p.CacheReadPerMillion); err != nil {
		return err
	}
	if err := checkNonNeg("cache_write_per_million", p.CacheWritePerMillion); err != nil {
		return err
	}
	if p.CacheWrite1hPerMillion != nil {
		if err := checkNonNeg("cache_write_1h_per_million", *p.CacheWrite1hPerMillion); err != nil {
			return err
		}
	}
	if p.LongContextThreshold < 0 {
		return errors.New("long_context_threshold must be non-negative")
	}
	if err := checkNonNeg("long_context_input_multiplier", p.LongContextInputMult); err != nil {
		return err
	}
	if err := checkNonNeg("long_context_output_multiplier", p.LongContextOutputMult); err != nil {
		return err
	}
	return nil
}

func clonePricingOverridesState(in PricingOverridesState) PricingOverridesState {
	out := PricingOverridesState{
		Default:  make(map[string]ModelPricingOverride, len(in.Default)),
		Channels: make(map[string]map[string]ModelPricingOverride, len(in.Channels)),
		Groups:   make(map[string]map[string]ModelPricingOverride, len(in.Groups)),
	}
	for k, v := range in.Default {
		out.Default[k] = cloneModelPricingOverride(v)
	}
	for chID, cm := range in.Channels {
		if len(cm) == 0 {
			continue
		}
		m := make(map[string]ModelPricingOverride, len(cm))
		for k, v := range cm {
			m[k] = cloneModelPricingOverride(v)
		}
		out.Channels[chID] = m
	}
	for gid, gm := range in.Groups {
		if len(gm) == 0 {
			continue
		}
		m := make(map[string]ModelPricingOverride, len(gm))
		for k, v := range gm {
			m[k] = cloneModelPricingOverride(v)
		}
		out.Groups[gid] = m
	}
	return out
}

func cloneModelPricingOverride(in ModelPricingOverride) ModelPricingOverride {
	cp := in
	if in.Custom.CacheWrite1hPerMillion != nil {
		v := *in.Custom.CacheWrite1hPerMillion
		cp.Custom.CacheWrite1hPerMillion = &v
	}
	return cp
}

// --- Admin Catalog Listing & Filtering (Scoped to System Groups & Supported Platforms) ---

// ListModelPricingCatalog returns the paginated list of system-supported models,
// scoped to either Global Default or a specific System Group (filtered by that group's platform).
func (s *PricingService) ListModelPricingCatalog(query ModelPricingListQuery) *ModelPricingListResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize <= 0 || query.PageSize > 200 {
		query.PageSize = 50
	}

	groupOverrideCounts := make(map[string]int)
	for _, gm := range s.overrides.Groups {
		for m := range gm {
			groupOverrideCounts[m]++
		}
	}

	var activeGroupOverrides map[string]ModelPricingOverride
	if query.GroupID > 0 {
		activeGroupOverrides = s.overrides.Groups[strconv.FormatInt(query.GroupID, 10)]
	}

	allIDs := make(map[string]struct{}, len(s.models)+len(s.overrides.Default)+len(activeGroupOverrides))
	for id := range s.models {
		allIDs[id] = struct{}{}
	}
	for id := range s.overrides.Default {
		allIDs[id] = struct{}{}
	}
	for id := range activeGroupOverrides {
		allIDs[id] = struct{}{}
	}

	needle := strings.ToLower(strings.TrimSpace(query.Search))
	filterMode := strings.ToLower(strings.TrimSpace(query.Filter))
	groupPlatform := strings.ToLower(strings.TrimSpace(query.GroupPlatform))
	platformFilter := strings.ToLower(strings.TrimSpace(query.Platform))
	if groupPlatform != "" && groupPlatform != PlatformComposite {
		platformFilter = groupPlatform
	}

	items := make([]ModelPricingListItem, 0, len(allIDs))
	for modelID := range allIDs {
		item := s.buildListItemLocked(modelID, query.GroupID, activeGroupOverrides, groupOverrideCounts[modelID])

		// Platform filter (automatically applied for single-platform system groups, or manual filter)
		if platformFilter != "" && platformFilter != "all" {
			// Always show models explicitly overridden in the currently selected group
			if !(query.GroupID > 0 && item.IsGroupOverridden) {
				if !modelSupportsPlatform(item, platformFilter) {
					continue
				}
			}
		}

		// Status filter
		switch filterMode {
		case "overridden":
			if query.GroupID > 0 {
				if !item.IsGroupOverridden && item.ChannelOverrideCount == 0 {
					continue
				}
			} else {
				if !item.IsDefaultModified && !item.IsCustomOnly && item.ChannelOverrideCount == 0 {
					continue
				}
			}
		case "custom":
			if !item.IsCustomOnly && item.Source != "default_custom" && item.Source != "group_custom" && item.ChannelOverrideCount == 0 {
				continue
			}
		case "locked":
			if item.LockedChannelCount == 0 {
				continue
			}
		}

		if needle != "" && !matchesPricingSearch(item, needle) {
			continue
		}

		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if needle != "" {
			iExact := items[i].ModelID == needle
			jExact := items[j].ModelID == needle
			if iExact != jExact {
				return iExact
			}
			iPrefix := strings.HasPrefix(items[i].ModelID, needle) || startsAtWordBoundary(items[i].ModelID, needle)
			jPrefix := strings.HasPrefix(items[j].ModelID, needle) || startsAtWordBoundary(items[j].ModelID, needle)
			if iPrefix != jPrefix {
				return iPrefix
			}
		}
		if query.GroupID > 0 && items[i].IsGroupOverridden != items[j].IsGroupOverridden {
			return items[i].IsGroupOverridden
		}
		if items[i].IsCustomOnly != items[j].IsCustomOnly {
			return items[i].IsCustomOnly
		}
		if items[i].ReleaseDate != items[j].ReleaseDate {
			return items[i].ReleaseDate > items[j].ReleaseDate
		}
		return items[i].ModelID < items[j].ModelID
	})

	total := len(items)
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}

	platforms := make([]SupportedPlatformSummary, len(s.platformSummaries))
	copy(platforms, s.platformSummaries)

	return &ModelPricingListResult{
		Items:         items[start:end],
		Total:         total,
		Page:          query.Page,
		PageSize:      query.PageSize,
		GroupPlatform: groupPlatform,
		Platforms:     platforms,
	}
}

func modelSupportsPlatform(item ModelPricingListItem, platform string) bool {
	if strings.EqualFold(item.Platform, platform) {
		return true
	}
	for _, p := range item.Platforms {
		if strings.EqualFold(p, platform) {
			return true
		}
	}
	return false
}

func (s *PricingService) buildListItemLocked(
	modelID string,
	groupID int64,
	activeGroupOverrides map[string]ModelPricingOverride,
	groupOverrideCount int,
) ModelPricingListItem {
	entry := s.models[modelID]
	defaultOv, hasDefaultOv := s.overrides.Default[modelID]
	defaultIsCustom := hasDefaultOv && (defaultOv.IsCustom || !defaultOv.Locked)

	platform := inferSystemPlatformForModel(modelID, "")
	if hasDefaultOv && defaultOv.Platform != "" {
		platform = defaultOv.Platform
	}

	item := ModelPricingListItem{
		ModelID:            modelID,
		DisplayName:        modelID,
		Platform:           platform,
		Platforms:          []string{platform},
		IsCustomOnly:       entry == nil,
		IsDefaultModified:  defaultIsCustom,
		IsDefaultLocked:    hasDefaultOv && defaultOv.Locked,
		GroupOverrideCount: groupOverrideCount,
		ChannelPrices:      []AccountChannelPriceEntry{},
	}

	var defaultPricing TokenPricingPerMillion
	defaultSource := "default_auto"
	var defaultUpdatedAt *time.Time

	if entry != nil {
		item.DisplayName = entry.DisplayName
		item.Platform = entry.Platform
		item.Platforms = append([]string(nil), entry.Platforms...)
		item.Family = entry.Family
		item.ReleaseDate = entry.ReleaseDate
		item.DefaultChannelID = entry.DefaultChannelID
		item.DefaultChannelName = entry.DefaultChannelName
		item.ChannelPrices = make([]AccountChannelPriceEntry, len(entry.Channels))
		for i, ch := range entry.Channels {
			origPricing := ch.Pricing
			chCopy := ch
			if chCopy.IsDefault {
				if hasDefaultOv {
					chCopy.Pricing = defaultOv.Custom
					chCopy.IsLocked = defaultOv.Locked
					chCopy.IsOverridden = defaultIsCustom
					origCopy := origPricing
					chCopy.SyncedPricing = &origCopy
					t := defaultOv.UpdatedAt
					chCopy.UpdatedAt = &t
				}
			} else if cm := s.overrides.Channels[chCopy.ChannelID]; cm != nil {
				if chOv, ok := cm[modelID]; ok {
					chIsCustom := chOv.IsCustom || !chOv.Locked
					chCopy.Pricing = chOv.Custom
					chCopy.IsLocked = chOv.Locked
					chCopy.IsOverridden = chIsCustom
					origCopy := origPricing
					chCopy.SyncedPricing = &origCopy
					t := chOv.UpdatedAt
					chCopy.UpdatedAt = &t
				}
			}
			if chCopy.IsOverridden {
				item.ChannelOverrideCount++
			}
			if chCopy.IsLocked {
				item.LockedChannelCount++
			}
			item.ChannelPrices[i] = chCopy
		}
		defaultPricing = entry.DefaultPricing
	}

	if hasDefaultOv {
		defaultPricing = defaultOv.Custom
		if defaultIsCustom {
			defaultSource = "default_custom"
		}
		t := defaultOv.UpdatedAt
		defaultUpdatedAt = &t
		if defaultOv.Custom.DisplayName != "" {
			item.DisplayName = defaultOv.Custom.DisplayName
		}
	}

	if groupID > 0 {
		if grpOv, ok := activeGroupOverrides[modelID]; ok {
			t := grpOv.UpdatedAt
			item.EffectivePricing = grpOv.Custom
			item.DefaultPricing = &defaultPricing
			item.Source = "group_custom"
			item.IsGroupOverridden = true
			item.UpdatedAt = &t
			if grpOv.Platform != "" && entry == nil {
				item.Platform = grpOv.Platform
				item.Platforms = []string{grpOv.Platform}
			}
			if grpOv.Custom.DisplayName != "" {
				item.DisplayName = grpOv.Custom.DisplayName
			}
			return item
		}
		item.EffectivePricing = defaultPricing
		item.DefaultPricing = &defaultPricing
		item.Source = "inherited_default"
		item.IsGroupOverridden = false
		item.UpdatedAt = defaultUpdatedAt
		return item
	}

	item.EffectivePricing = defaultPricing
	item.Source = defaultSource
	item.UpdatedAt = defaultUpdatedAt
	return item
}

func matchesPricingSearch(item ModelPricingListItem, needle string) bool {
	if strings.Contains(item.ModelID, needle) ||
		strings.Contains(strings.ToLower(item.DisplayName), needle) ||
		strings.Contains(strings.ToLower(item.Family), needle) ||
		strings.Contains(strings.ToLower(item.Platform), needle) ||
		strings.Contains(strings.ToLower(item.DefaultChannelName), needle) {
		return true
	}
	for _, ch := range item.ChannelPrices {
		if strings.Contains(strings.ToLower(ch.ChannelID), needle) ||
			strings.Contains(strings.ToLower(ch.ChannelName), needle) ||
			strings.Contains(strings.ToLower(ch.RawModelID), needle) {
			return true
		}
	}
	return false
}

func startsAtWordBoundary(text, needle string) bool {
	value := strings.ToLower(text)
	if len(needle) == 0 || len(needle) > len(value) {
		return false
	}
	for i := 0; i <= len(value)-len(needle); i++ {
		if i > 0 {
			prev := value[i-1]
			if prev != ' ' && prev != '-' && prev != '.' && prev != '_' && prev != '/' {
				continue
			}
		}
		if strings.HasPrefix(value[i:], needle) {
			return true
		}
	}
	return false
}

// --- Runtime Billing Lookups (Group > Matched Account Channel > Default Channel) ---

// GetModelPricingForGroup returns a system group's custom pricing override if configured.
// Returns (nil, false) when the group has no separate override.
func (s *PricingService) GetModelPricingForGroup(groupID int64, modelName string) (*CatalogModelPricing, bool) {
	if s == nil || groupID <= 0 || strings.TrimSpace(modelName) == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	gm := s.overrides.Groups[strconv.FormatInt(groupID, 10)]
	if len(gm) == 0 {
		return nil, false
	}

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	candidates := s.buildModelLookupCandidates(modelLower)
	for _, cand := range candidates {
		norm := NormalizeOriginalModelID(cand)
		if ov, ok := gm[norm]; ok {
			return overrideToCatalog(ov), true
		}
		if ov, ok := gm[cand]; ok {
			return overrideToCatalog(ov), true
		}
	}
	return nil, false
}

// GetModelPricingForProvider looks up the pricing for a specific account-type matched
// models.dev channel (e.g. amazon-bedrock, google-vertex-anthropic, google-vertex,
// opencode-go, cline-pass, zhipuai-coding-plan, etc.).
//
// If providerID matches the model's default channel AND the admin has customized the global
// default price for that model, this returns (nil, false) so the default custom price applies.
// Distinct account channels (e.g. amazon-bedrock vs default anthropic) always return their
// channel-specific price (Priority: Group > Matched Channel > Default Channel).
func (s *PricingService) GetModelPricingForProvider(providerID, modelName, rawUpstreamModel string) (*CatalogModelPricing, bool) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if s == nil || providerID == "" || strings.TrimSpace(modelName) == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawLower := strings.ToLower(strings.TrimSpace(rawUpstreamModel))
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	findEntry := func() *SupportedModelEntry {
		for _, cand := range lookupCandidates {
			if e := s.models[cand]; e != nil {
				return e
			}
			if norm := NormalizeOriginalModelID(cand); norm != "" {
				if e := s.models[norm]; e != nil {
					return e
				}
			}
		}
		baseName := s.extractBaseName(NormalizeOriginalModelID(lookupCandidates[0]))
		if baseName != "" {
			if e := s.models[baseName]; e != nil {
				return e
			}
		}
		return nil
	}

	entry := findEntry()

	// 1. Explicit per-channel custom override or locked price snapshot always takes precedence for this channel.
	if chMap := s.overrides.Channels[providerID]; len(chMap) > 0 {
		if entry != nil {
			if ov, ok := chMap[entry.ModelID]; ok {
				return overrideToCatalog(ov), true
			}
		}
		for _, cand := range lookupCandidates {
			if norm := NormalizeOriginalModelID(cand); norm != "" {
				if ov, ok := chMap[norm]; ok {
					return overrideToCatalog(ov), true
				}
			}
			if ov, ok := chMap[cand]; ok {
				return overrideToCatalog(ov), true
			}
		}
	}

	if entry == nil {
		return nil, false
	}

	// 2. If this account uses the model's default channel and the admin customized or locked the default price,
	// let default custom/locked price take precedence cleanly.
	if providerID == entry.DefaultChannelID {
		if _, hasDefaultOverride := s.overrides.Default[entry.ModelID]; hasDefaultOverride {
			return nil, false
		}
	}

	// 3. Exact raw upstream model ID match inside this channel (e.g. Bedrock regional vs global!)
	if rawLower != "" {
		if rawMap := entry.rawByChannel[providerID]; rawMap != nil {
			if cat, ok := rawMap[rawLower]; ok && cat != nil {
				return cat, true
			}
		}
	}
	if rawMap := entry.rawByChannel[providerID]; rawMap != nil {
		if cat, ok := rawMap[modelLower]; ok && cat != nil {
			return cat, true
		}
	}

	// 4. Canonical channel price entry for this provider
	if ch := entry.channelByID[providerID]; ch != nil && ch.catalog != nil {
		return ch.catalog, true
	}

	return nil, false
}

// GetModelPricing 获取模型默认渠道价格（全局自定义覆盖 > models.dev 默认渠道价）
func (s *PricingService) GetModelPricing(modelName string) *CatalogModelPricing {
	if s == nil || strings.TrimSpace(modelName) == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	if pricing := s.lookupIdentifiedModelPricingLocked(lookupCandidates); pricing != nil {
		return pricing
	}
	if pricing := s.matchByModelFamilyLocked(lookupCandidates[0]); pricing != nil {
		return pricing
	}
	if strings.HasPrefix(lookupCandidates[0], "gpt-") {
		return s.matchOpenAIModelLocked(lookupCandidates[0])
	}
	return nil
}

// GetIdentifiedModelPricing 在价格表中确定性地识别模型，识别不到时返回 nil。
func (s *PricingService) GetIdentifiedModelPricing(modelName string) *CatalogModelPricing {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	if modelLower == "" {
		return nil
	}
	return s.lookupIdentifiedModelPricingLocked(s.buildModelLookupCandidates(modelLower))
}

func (s *PricingService) lookupIdentifiedModelPricingLocked(lookupCandidates []string) *CatalogModelPricing {
	if len(lookupCandidates) == 0 {
		return nil
	}

	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := s.effectiveDefaultData[candidate]; ok {
			return pricing
		}
		if norm := NormalizeOriginalModelID(candidate); norm != "" && norm != candidate {
			if pricing, ok := s.effectiveDefaultData[norm]; ok {
				return pricing
			}
		}
	}

	for _, candidate := range lookupCandidates {
		for _, variant := range []string{
			strings.ReplaceAll(candidate, "-4-5-", "-4.5-"),
			strings.ReplaceAll(candidate, "-4.5-", "-4-5-"),
			strings.ReplaceAll(candidate, "-4-6", "-4.6"),
			strings.ReplaceAll(candidate, "-4.6", "-4-6"),
		} {
			if pricing, ok := s.effectiveDefaultData[variant]; ok {
				return pricing
			}
		}
	}

	baseName := s.extractBaseName(NormalizeOriginalModelID(lookupCandidates[0]))
	if baseName != "" {
		if pricing, ok := s.effectiveDefaultData[baseName]; ok {
			return pricing
		}
		var matchedKey string
		var matchedPricing *CatalogModelPricing
		for key, pricing := range s.effectiveDefaultData {
			if s.extractBaseName(strings.ToLower(key)) == baseName {
				if matchedKey == "" || key < matchedKey {
					matchedKey = key
					matchedPricing = pricing
				}
			}
		}
		if matchedPricing != nil {
			return matchedPricing
		}
	}

	return nil
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
	origID := NormalizeOriginalModelID(modelLower)
	rawCandidates := []string{
		modelLower,
		origID,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	}
	normalized := normalizeModelNameForPricing(modelLower)

	candidates := rawCandidates
	if isAntigravityThinkingTierAlias(lastSegment(modelLower)) {
		candidates = append(candidates, normalized)
	} else {
		candidates = append([]string{normalized}, candidates...)
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

func normalizeModelNameForPricing(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	if canonical := canonicalizeOpenAIModelAliasSpelling(model); canonical != "" {
		if canonical == "gpt-6" {
			return "gpt-6-astra"
		}
		if canonical == "gpt-5.6" {
			return "gpt-5.6-sol"
		}
		if suffix, ok := strings.CutPrefix(canonical, "gpt-5.6-"); ok && (suffix == "max" || isKnownCodexModelSuffix(suffix)) {
			return "gpt-5.6-sol"
		}
		return canonical
	}
	return normalizeAntigravityClaudeTierAlias(normalizeGeminiThinkingTierAlias(model))
}

func normalizeAntigravityClaudeTierAlias(model string) string {
	for _, baseModel := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		for _, tier := range []string{"-high", "-low", "-medium"} {
			if model == baseModel+tier {
				return baseModel
			}
		}
	}
	return model
}

func isAntigravityThinkingTierAlias(model string) bool {
	return normalizeGeminiThinkingTierAlias(model) != model || normalizeAntigravityClaudeTierAlias(model) != model
}

func normalizeGeminiThinkingTierAlias(model string) string {
	for _, baseModel := range []string{"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash", "gemini-4-argon", "gemini-4-carbon"} {
		for _, tier := range []string{"-high", "-low", "-medium", "-tiered"} {
			if model == baseModel+tier {
				return baseModel
			}
		}
	}
	return model
}

func lastSegment(model string) string {
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

func (s *PricingService) extractBaseName(model string) string {
	parts := strings.Split(model, "-")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 8 && isNumeric(part) {
			continue
		}
		if strings.Contains(part, ":") {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "-")
}

func (s *PricingService) matchByModelFamilyLocked(model string) *CatalogModelPricing {
	model = normalizeAntigravityClaudeTierAlias(model)
	switch {
	case claude.IsFable52(model):
		if pricing, ok := s.effectiveDefaultData["claude-fable-5-2"]; ok {
			return pricing
		}
	case claude.IsMythos52(model):
		if pricing, ok := s.effectiveDefaultData["claude-mythos-5-2"]; ok {
			return pricing
		}
	case claude.IsOpus55(model):
		if pricing, ok := s.effectiveDefaultData["claude-opus-5-5"]; ok {
			return pricing
		}
	case claude.IsSonnet55(model):
		if pricing, ok := s.effectiveDefaultData["claude-sonnet-5-5"]; ok {
			return pricing
		}
	case claude.IsHaiku55(model):
		if pricing, ok := s.effectiveDefaultData["claude-haiku-5-5"]; ok {
			return pricing
		}
	}

	type modelFamily struct {
		name    string
		match   []string
		pricing []string
	}
	families := []modelFamily{
		{name: "opus-5", match: []string{"claude-opus-5"}, pricing: []string{"claude-opus-5", "claude-opus-4-8"}},
		{name: "opus-4.8", match: []string{"claude-opus-4-8", "claude-opus-4.8"}, pricing: []string{"claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7"}},
		{name: "opus-4.7", match: []string{"claude-opus-4-7", "claude-opus-4.7"}, pricing: []string{"claude-opus-4-7", "claude-opus-4.7", "claude-opus-4-6"}},
		{name: "opus-4.6", match: []string{"claude-opus-4-6", "claude-opus-4.6"}},
		{name: "opus-4.5", match: []string{"claude-opus-4-5", "claude-opus-4.5"}},
		{name: "opus-4", match: []string{"claude-opus-4", "claude-3-opus"}},
		{name: "sonnet-4.6", match: []string{"claude-sonnet-4-6", "claude-sonnet-4.6"}},
		{name: "sonnet-4.5", match: []string{"claude-sonnet-4-5", "claude-sonnet-4.5"}},
		{name: "sonnet-4", match: []string{"claude-sonnet-4", "claude-3-5-sonnet"}},
		{name: "sonnet-3.5", match: []string{"claude-3-5-sonnet", "claude-3.5-sonnet"}},
		{name: "sonnet-3", match: []string{"claude-3-sonnet"}},
		{name: "haiku-4.5", match: []string{"claude-haiku-4-5", "claude-haiku-4.5"}},
		{name: "haiku-3.5", match: []string{"claude-3-5-haiku", "claude-3.5-haiku"}},
		{name: "haiku-3", match: []string{"claude-3-haiku"}},
	}

	var matched *modelFamily
	for i := range families {
		for _, pattern := range families[i].match {
			if strings.Contains(model, pattern) || strings.Contains(model, strings.ReplaceAll(pattern, "-", "")) {
				matched = &families[i]
				break
			}
		}
		if matched != nil {
			break
		}
	}
	if matched == nil {
		return nil
	}

	lookups := matched.pricing
	if lookups == nil {
		lookups = matched.match
	}
	keys := make([]string, 0, len(s.effectiveDefaultData))
	for k := range s.effectiveDefaultData {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, pattern := range lookups {
		for _, key := range keys {
			keyLower := strings.ToLower(key)
			if matched.name == "opus-5" && claude.IsOpus55(keyLower) {
				continue
			}
			if strings.Contains(keyLower, pattern) {
				return s.effectiveDefaultData[key]
			}
		}
	}
	return nil
}

func (s *PricingService) matchOpenAIModelLocked(model string) *CatalogModelPricing {
	if strings.HasPrefix(model, "gpt-5.3-codex-spark") {
		if pricing, ok := s.effectiveDefaultData["gpt-5.1-codex"]; ok {
			return pricing
		}
	}
	if openai.IsGPT61TerraModelSpelling(model) {
		if pricing, ok := s.effectiveDefaultData["gpt-6.1-terra"]; ok {
			return pricing
		}
	}
	if base := openai.GPT62ModelID(model); base != "" {
		if pricing, ok := s.effectiveDefaultData[base]; ok {
			return pricing
		}
	}
	if openai.IsGPT61SolModelSpelling(model) {
		if pricing, ok := s.effectiveDefaultData["gpt-6.1-sol"]; ok {
			return pricing
		}
	}
	if openai.IsGPT6SolOrLunaModelSpelling(model) {
		if pricing, ok := s.effectiveDefaultData[normalizeKnownOpenAICodexModel(model)]; ok {
			return pricing
		}
	}

	variants := s.generateOpenAIModelVariants(model, openAIModelDatePattern)
	for _, variant := range variants {
		if pricing, ok := s.effectiveDefaultData[variant]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, variant))
			return pricing
		}
	}

	if strings.HasPrefix(model, "gpt-5.3-codex") {
		if pricing, ok := s.effectiveDefaultData["gpt-5.2-codex"]; ok {
			return pricing
		}
	}

	if isOpenAIImageGenerationModel(model) {
		for _, candidate := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1"} {
			if pricing, ok := s.effectiveDefaultData[candidate]; ok {
				return pricing
			}
		}
		return nil
	}

	defaultModel := strings.ToLower(openai.DefaultTestModel)
	if pricing, ok := s.effectiveDefaultData[defaultModel]; ok {
		return pricing
	}
	return nil
}

func (s *PricingService) generateOpenAIModelVariants(model string, datePattern *regexp.Regexp) []string {
	seen := make(map[string]bool)
	var variants []string

	addVariant := func(v string) {
		if v != model && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	withoutDate := datePattern.ReplaceAllString(model, "")
	if withoutDate != model {
		addVariant(withoutDate)
	}
	if matches := openAIModelBasePattern.FindStringSubmatch(model); len(matches) > 1 {
		addVariant(matches[1])
	}
	if withoutDate != model {
		if matches := openAIModelBasePattern.FindStringSubmatch(withoutDate); len(matches) > 1 {
			addVariant(matches[1])
		}
	}
	return variants
}

// ListModelNamesByProvider returns all normalized original model IDs in the supported models.dev cache
// belonging to the given provider ID (case-insensitive, sorted alphabetically).
func (s *PricingService) ListModelNamesByProvider(provider string) []string {
	if s == nil {
		return []string{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	if models, ok := s.providerModels[provider]; ok {
		out := make([]string, len(models))
		copy(out, models)
		return out
	}

	aliasMap := map[string]string{
		"gemini":   "google",
		"moonshot": "moonshotai",
		"zhipu":    "zhipuai",
	}
	if alias, ok := aliasMap[provider]; ok {
		if models, exists := s.providerModels[alias]; exists {
			out := make([]string, len(models))
			copy(out, models)
			return out
		}
	}

	return []string{}
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
