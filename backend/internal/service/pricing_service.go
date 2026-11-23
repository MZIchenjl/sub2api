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

	PricingSelectionModeProvider = "provider"
	PricingSelectionModeCustom   = "custom"

	PricingSyncStatusNever   = "never"
	PricingSyncStatusSuccess = "success"
	PricingSyncStatusFailed  = "failed"

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

// --- models.dev raw API JSON structures ---

type modelsDevRawProvider struct {
	ID     string                       `json:"id"`
	Name   string                       `json:"name"`
	API    string                       `json:"api"`
	Models map[string]modelsDevRawModel `json:"models"`
}

type modelsDevRawModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type modelsDevRawLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

type modelsDevRawCostTierSpec struct {
	Type string `json:"type"`
	Size int    `json:"size"`
}

type modelsDevRawCostTier struct {
	Input      *float64                  `json:"input"`
	Output     *float64                  `json:"output"`
	CacheRead  *float64                  `json:"cache_read"`
	CacheWrite *float64                  `json:"cache_write"`
	Tier       *modelsDevRawCostTierSpec `json:"tier"`
}

type modelsDevRawContextOver200k struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type modelsDevRawCost struct {
	Input           *float64                     `json:"input"`
	Output          *float64                     `json:"output"`
	CacheRead       *float64                     `json:"cache_read"`
	CacheWrite      *float64                     `json:"cache_write"`
	Reasoning       *float64                     `json:"reasoning"`
	InputAudio      *float64                     `json:"input_audio"`
	OutputAudio     *float64                     `json:"output_audio"`
	Tiers           []modelsDevRawCostTier       `json:"tiers"`
	ContextOver200k *modelsDevRawContextOver200k `json:"context_over_200k"`
}

type modelsDevRawModel struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Family           string                  `json:"family"`
	ReleaseDate      string                  `json:"release_date"`
	Status           string                  `json:"status"`
	CanonicalModelID string                  `json:"canonical_model_id"`
	Modalities       *modelsDevRawModalities `json:"modalities"`
	Limit            *modelsDevRawLimit      `json:"limit"`
	Cost             *modelsDevRawCost       `json:"cost"`
}

// --- Public DTOs & Store structures ---

// PricingSyncConfig controls models.dev synchronization.
type PricingSyncConfig struct {
	AutoSyncEnabled   bool   `json:"auto_sync_enabled"`
	SyncIntervalHours int    `json:"sync_interval_hours"`
	ModelsDevURL      string `json:"models_dev_url"`
}

// PricingSyncMetadata records the latest models.dev sync result and cache stats.
type PricingSyncMetadata struct {
	LastSyncAt      *time.Time `json:"last_sync_at,omitempty"`
	LastSyncStatus  string     `json:"last_sync_status"`
	LastSyncError   string     `json:"last_sync_error,omitempty"`
	CacheHash       string     `json:"cache_hash,omitempty"`
	ProviderCount   int        `json:"provider_count"`
	ModelGroupCount int        `json:"model_group_count"`
	RawEntryCount   int        `json:"raw_entry_count"`
}

// PricingSyncStatus aggregates sync config, metadata, and override summaries for the admin UI.
type PricingSyncStatus struct {
	Config               PricingSyncConfig   `json:"config"`
	Meta                 PricingSyncMetadata `json:"meta"`
	DefaultOverrideCount int                 `json:"default_override_count"`
	GroupOverrideCounts  map[string]int      `json:"group_override_counts"`
	CacheLoaded          bool                `json:"cache_loaded"`
}

// TokenPricingPerMillion represents human-friendly USD per 1M token prices (aligned with models.dev & cc-switch).
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

// ModelPricingSelection represents an admin's active cached provider selection or custom override for a model.
type ModelPricingSelection struct {
	Mode         string                  `json:"mode"` // "provider" | "custom"
	CandidateKey string                  `json:"candidate_key,omitempty"`
	ProviderID   string                  `json:"provider_id,omitempty"`
	RawModelID   string                  `json:"raw_model_id,omitempty"`
	Custom       *TokenPricingPerMillion `json:"custom,omitempty"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

// PricingOverridesState persists default and per-group selections/overrides.
type PricingOverridesState struct {
	Default map[string]ModelPricingSelection            `json:"default"`
	Groups  map[string]map[string]ModelPricingSelection `json:"groups"`
}

// ModelsDevCandidate represents one cached provider pricing entry under an original model ID.
type ModelsDevCandidate struct {
	CandidateKey  string                 `json:"candidate_key"`
	ProviderID    string                 `json:"provider_id"`
	ProviderName  string                 `json:"provider_name"`
	RawModelID    string                 `json:"raw_model_id"`
	DisplayName   string                 `json:"display_name"`
	Family        string                 `json:"family,omitempty"`
	ReleaseDate   string                 `json:"release_date,omitempty"`
	Status        string                 `json:"status,omitempty"`
	ContextLimit  int                    `json:"context_limit,omitempty"`
	OutputLimit   int                    `json:"output_limit,omitempty"`
	IsRecommended bool                   `json:"is_recommended"`
	Pricing       TokenPricingPerMillion `json:"pricing"`
	catalog       *CatalogModelPricing
	score         int
}

// ModelsDevModelGroup groups all cached models.dev entries sharing the same original model ID.
type ModelsDevModelGroup struct {
	ModelID             string
	DisplayName         string
	Family              string
	ReleaseDate         string
	DefaultCandidateKey string
	Candidates          []ModelsDevCandidate
	candidatesByKey     map[string]*ModelsDevCandidate
}

// ModelsDevProviderSummary summarizes a provider available in the models.dev cache.
type ModelsDevProviderSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ModelCount int    `json:"model_count"`
}

// ModelPricingListQuery defines query filters for listing grouped model pricing.
type ModelPricingListQuery struct {
	GroupID  int64
	Search   string
	Provider string
	Filter   string // "all" | "overridden" | "custom" | "multi_provider"
	Page     int
	PageSize int
}

// ModelPricingListItem is one row in the admin Model Pricing table (grouped by original model ID).
type ModelPricingListItem struct {
	ModelID            string                  `json:"model_id"`
	DisplayName        string                  `json:"display_name"`
	Family             string                  `json:"family,omitempty"`
	ReleaseDate        string                  `json:"release_date,omitempty"`
	Source             string                  `json:"source"` // "default_auto" | "default_provider" | "default_custom" | "inherited_default" | "group_provider" | "group_custom"
	IsGroupOverridden  bool                    `json:"is_group_overridden"`
	IsDefaultModified  bool                    `json:"is_default_modified"`
	IsCustomOnly       bool                    `json:"is_custom_only"`
	ActiveProviderID   string                  `json:"active_provider_id,omitempty"`
	ActiveProviderName string                  `json:"active_provider_name,omitempty"`
	ActiveRawModelID   string                  `json:"active_raw_model_id,omitempty"`
	ActiveCandidateKey string                  `json:"active_candidate_key,omitempty"`
	EffectivePricing   TokenPricingPerMillion  `json:"effective_pricing"`
	DefaultPricing     *TokenPricingPerMillion `json:"default_pricing,omitempty"`
	Candidates         []ModelsDevCandidate    `json:"candidates"`
	CandidateCount     int                     `json:"candidate_count"`
	GroupOverrideCount int                     `json:"group_override_count"`
	UpdatedAt          *time.Time              `json:"updated_at,omitempty"`
}

// ModelPricingListResult is the paginated response for listing model pricing.
type ModelPricingListResult struct {
	Items     []ModelPricingListItem     `json:"items"`
	Total     int                        `json:"total"`
	Page      int                        `json:"page"`
	PageSize  int                        `json:"page_size"`
	Providers []ModelsDevProviderSummary `json:"providers"`
}

// PricingService 动态价格服务（基于 models.dev 缓存 + 原始模型 ID 分组 + 分组独立覆盖）
type PricingService struct {
	cfg          *config.Config
	remoteClient PricingRemoteClient
	settingRepo  SettingRepository

	mu                    sync.RWMutex
	syncMu                sync.Mutex
	syncConfig            PricingSyncConfig
	syncMeta              PricingSyncMetadata
	overrides             PricingOverridesState
	modelGroups           map[string]*ModelsDevModelGroup // normalized original model ID -> grouped candidates
	providerModels        map[string][]string             // provider_id -> sorted original model IDs
	providerSummaries     []ModelsDevProviderSummary
	effectiveDefaultData  map[string]*CatalogModelPricing // materialized default effective catalog for hot path
	lastUpdated           time.Time
	localHash             string

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
			Default: make(map[string]ModelPricingSelection),
			Groups:  make(map[string]map[string]ModelPricingSelection),
		},
		modelGroups:          make(map[string]*ModelsDevModelGroup),
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

// Initialize 初始化价格服务：加载数据库配置与覆盖层 -> 加载本地 models.dev 缓存 -> 启动定时同步器。
func (s *PricingService) Initialize() error {
	if s.cfg != nil && strings.TrimSpace(s.cfg.Pricing.DataDir) != "" {
		if err := os.MkdirAll(s.cfg.Pricing.DataDir, 0755); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to create data directory: %v", err)
		}
	}

	// 1. 从数据库加载同步配置、元数据和覆盖层（若有 settingRepo）
	s.loadPersistedState(context.Background())

	// 2. 优先加载本地 models.dev 缓存，保证启动快速可用
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

	// 3. 即使本地缓存不存在，也要先根据数据库里的自定义覆盖构建默认索引
	s.mu.Lock()
	s.rebuildEffectiveDefaultLocked()
	modelCount := len(s.effectiveDefaultData)
	autoSync := s.syncConfig.AutoSyncEnabled && strings.TrimSpace(s.syncConfig.ModelsDevURL) != ""
	s.mu.Unlock()

	// 4. 启动定时同步调度器
	s.startUpdateScheduler()

	// 5. 若启用自动同步且本地无缓存或缓存已过期，则在后台异步触发一次同步（不阻塞服务启动）
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
				ov.Default = make(map[string]ModelPricingSelection)
			}
			if ov.Groups == nil {
				ov.Groups = make(map[string]map[string]ModelPricingSelection)
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

// --- Sync & Disk Cache ---

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
	return s.applyModelsDevCatalogBytes(body, false)
}

// LoadModelsDevJSON loads raw models.dev api.json bytes into memory (useful for tests & imports).
func (s *PricingService) LoadModelsDevJSON(body []byte) error {
	return s.applyModelsDevCatalogBytes(body, false)
}

func (s *PricingService) applyModelsDevCatalogBytes(body []byte, updateSyncMeta bool) error {
	groups, providerModels, providerSummaries, rawCount, err := parseModelsDevCatalog(body)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(body)
	hashStr := hex.EncodeToString(sum[:])
	now := time.Now()

	s.mu.Lock()
	s.modelGroups = groups
	s.providerModels = providerModels
	s.providerSummaries = providerSummaries
	s.localHash = hashStr
	s.lastUpdated = now
	s.syncMeta.CacheHash = hashStr[:min(16, len(hashStr))]
	s.syncMeta.ProviderCount = len(providerSummaries)
	s.syncMeta.ModelGroupCount = len(groups)
	s.syncMeta.RawEntryCount = rawCount
	if updateSyncMeta {
		t := now
		s.syncMeta.LastSyncAt = &t
		s.syncMeta.LastSyncStatus = PricingSyncStatusSuccess
		s.syncMeta.LastSyncError = ""
	}
	s.rebuildEffectiveDefaultLocked()
	s.mu.Unlock()
	return nil
}

// SyncNow immediately fetches models.dev/api.json, updates local disk cache, memory catalog, and metadata.
func (s *PricingService) SyncNow(ctx context.Context) (*PricingSyncStatus, error) {
	if s == nil {
		return nil, errors.New("pricing service is nil")
	}
	if s.remoteClient == nil {
		return nil, errors.New("pricing remote client is not configured")
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

	logger.LegacyPrintf("service.pricing", "[Pricing] Syncing models.dev catalog from %s", validatedURL)
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := s.remoteClient.FetchPricingJSON(ctx, validatedURL)
	if err != nil {
		err = fmt.Errorf("fetch models.dev catalog: %w", err)
		s.recordSyncError(ctx, err)
		return s.GetSyncStatus(), err
	}

	if err := s.applyModelsDevCatalogBytes(body, true); err != nil {
		err = fmt.Errorf("parse models.dev catalog: %w", err)
		s.recordSyncError(ctx, err)
		return s.GetSyncStatus(), err
	}

	// Atomically write cache to disk
	if cachePath := s.getCatalogCachePath(); cachePath != "" {
		if mkErr := os.MkdirAll(filepath.Dir(cachePath), 0755); mkErr == nil {
			tmpPath := cachePath + ".tmp"
			if writeErr := os.WriteFile(tmpPath, body, 0644); writeErr == nil {
				if renameErr := os.Rename(tmpPath, cachePath); renameErr != nil {
					_ = os.WriteFile(cachePath, body, 0644)
					_ = os.Remove(tmpPath)
				}
			} else {
				logger.LegacyPrintf("service.pricing", "[Pricing] Failed to write models.dev cache file: %v", writeErr)
			}
		}
	}

	s.mu.RLock()
	metaCopy := s.syncMeta
	s.mu.RUnlock()
	if err := s.persistSettingJSON(ctx, SettingKeyModelPricingSyncMeta, metaCopy); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to persist sync metadata: %v", err)
	}

	logger.LegacyPrintf("service.pricing", "[Pricing] Synced models.dev successfully (%d providers, %d model groups, %d entries)",
		metaCopy.ProviderCount, metaCopy.ModelGroupCount, metaCopy.RawEntryCount)
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

// GetSyncStatus returns the current sync config, metadata, and override summary.
func (s *PricingService) GetSyncStatus() *PricingSyncStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	groupCounts := make(map[string]int, len(s.overrides.Groups))
	for gid, models := range s.overrides.Groups {
		if len(models) > 0 {
			groupCounts[gid] = len(models)
		}
	}

	return &PricingSyncStatus{
		Config:               s.syncConfig,
		Meta:                 s.syncMeta,
		DefaultOverrideCount: len(s.overrides.Default),
		GroupOverrideCounts:  groupCounts,
		CacheLoaded:          len(s.modelGroups) > 0,
	}
}

// GetStatus 获取服务状态（兼容旧状态接口）
func (s *PricingService) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"source":            "models.dev",
		"model_count":       len(s.effectiveDefaultData),
		"model_group_count": len(s.modelGroups),
		"provider_count":    len(s.providerSummaries),
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

// --- models.dev parsing & original model ID grouping ---

// NormalizeOriginalModelID extracts the canonical original model ID so the same model
// across different upstream channels/providers (e.g., "anthropic/claude-opus-5",
// "global.anthropic.claude-opus-5-v1:0", "claude-opus-5@default") groups together cleanly.
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

	// Bedrock geo+vendor prefixes (e.g. global.anthropic.claude-opus-4-6-v1)
	id = bedrockRegionPrefixRe.ReplaceAllString(id, "")
	id = bedrockVendorPrefixRe.ReplaceAllString(id, "")
	id = bedrockVersionSuffixRe.ReplaceAllString(id, "")

	// Vertex AI @default and @YYYYMMDD forms
	id = strings.TrimSuffix(id, "@default")
	id = vertexDateSuffixRe.ReplaceAllString(id, "-$1")
	id = strings.ReplaceAll(id, "@", "-")

	// Strip non-numeric variant suffix like :free, :exacto, :beta, :extended
	if idx := strings.Index(id, ":"); idx != -1 {
		id = id[:idx]
	}

	return strings.TrimSpace(id)
}

func makeCandidateKey(providerID, rawModelID string) string {
	return strings.ToLower(strings.TrimSpace(providerID)) + "::" + strings.TrimSpace(rawModelID)
}

func parseModelsDevCatalog(body []byte) (
	map[string]*ModelsDevModelGroup,
	map[string][]string,
	[]ModelsDevProviderSummary,
	int,
	error,
) {
	var rawProviders map[string]modelsDevRawProvider
	if err := json.Unmarshal(body, &rawProviders); err != nil {
		return nil, nil, nil, 0, fmt.Errorf("unmarshal models.dev JSON: %w", err)
	}

	groups := make(map[string]*ModelsDevModelGroup)
	providerModelSets := make(map[string]map[string]struct{})
	providerNames := make(map[string]string)
	rawEntryCount := 0

	for provKey, prov := range rawProviders {
		providerID := strings.ToLower(strings.TrimSpace(prov.ID))
		if providerID == "" {
			providerID = strings.ToLower(strings.TrimSpace(provKey))
		}
		if providerID == "" || len(prov.Models) == 0 {
			continue
		}
		providerName := strings.TrimSpace(prov.Name)
		if providerName == "" {
			providerName = providerID
		}
		providerNames[providerID] = providerName

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

			pricingPM, catalogPricing := convertModelsDevCost(providerID, rawModel)
			displayName := strings.TrimSpace(rawModel.Name)
			if displayName == "" {
				displayName = origID
			}
			var ctxLimit, outLimit int
			if rawModel.Limit != nil {
				ctxLimit = rawModel.Limit.Context
				outLimit = rawModel.Limit.Output
			}

			candKey := makeCandidateKey(providerID, rawModelID)
			cand := ModelsDevCandidate{
				CandidateKey: candKey,
				ProviderID:   providerID,
				ProviderName: providerName,
				RawModelID:   rawModelID,
				DisplayName:  displayName,
				Family:       strings.TrimSpace(rawModel.Family),
				ReleaseDate:  strings.TrimSpace(rawModel.ReleaseDate),
				Status:       strings.TrimSpace(rawModel.Status),
				ContextLimit: ctxLimit,
				OutputLimit:  outLimit,
				Pricing:      pricingPM,
				catalog:      catalogPricing,
				score:        scoreModelsDevCandidate(providerID, rawModelID, origID, rawModel),
			}

			grp := groups[origID]
			if grp == nil {
				grp = &ModelsDevModelGroup{
					ModelID:         origID,
					DisplayName:     displayName,
					Family:          cand.Family,
					ReleaseDate:     cand.ReleaseDate,
					candidatesByKey: make(map[string]*ModelsDevCandidate),
				}
				groups[origID] = grp
			}
			grp.Candidates = append(grp.Candidates, cand)

			if providerModelSets[providerID] == nil {
				providerModelSets[providerID] = make(map[string]struct{})
			}
			providerModelSets[providerID][origID] = struct{}{}
			rawEntryCount++
		}
	}

	if len(groups) == 0 {
		return nil, nil, nil, 0, fmt.Errorf("no valid pricing entries found in models.dev catalog")
	}

	// Sort candidates within each original model ID group and mark the best default candidate.
	for _, grp := range groups {
		sort.SliceStable(grp.Candidates, func(i, j int) bool {
			if grp.Candidates[i].score != grp.Candidates[j].score {
				return grp.Candidates[i].score > grp.Candidates[j].score
			}
			if grp.Candidates[i].ProviderID != grp.Candidates[j].ProviderID {
				return grp.Candidates[i].ProviderID < grp.Candidates[j].ProviderID
			}
			return grp.Candidates[i].RawModelID < grp.Candidates[j].RawModelID
		})
		grp.Candidates[0].IsRecommended = true
		grp.DefaultCandidateKey = grp.Candidates[0].CandidateKey
		grp.DisplayName = grp.Candidates[0].DisplayName
		if grp.Candidates[0].Family != "" {
			grp.Family = grp.Candidates[0].Family
		}
		if grp.Candidates[0].ReleaseDate != "" {
			grp.ReleaseDate = grp.Candidates[0].ReleaseDate
		}
		grp.candidatesByKey = make(map[string]*ModelsDevCandidate, len(grp.Candidates))
		for i := range grp.Candidates {
			grp.candidatesByKey[grp.Candidates[i].CandidateKey] = &grp.Candidates[i]
		}
	}

	providerModels := make(map[string][]string, len(providerModelSets))
	providerSummaries := make([]ModelsDevProviderSummary, 0, len(providerModelSets))
	for pid, set := range providerModelSets {
		models := make([]string, 0, len(set))
		for m := range set {
			models = append(models, m)
		}
		sort.Strings(models)
		providerModels[pid] = models
		providerSummaries = append(providerSummaries, ModelsDevProviderSummary{
			ID:         pid,
			Name:       providerNames[pid],
			ModelCount: len(models),
		})
	}
	sort.Slice(providerSummaries, func(i, j int) bool {
		if providerSummaries[i].ModelCount != providerSummaries[j].ModelCount {
			return providerSummaries[i].ModelCount > providerSummaries[j].ModelCount
		}
		return strings.ToLower(providerSummaries[i].Name) < strings.ToLower(providerSummaries[j].Name)
	})

	return groups, providerModels, providerSummaries, rawEntryCount, nil
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

func scoreModelsDevCandidate(providerID, rawModelID, origID string, raw modelsDevRawModel) int {
	score := 0

	// 1. First-party official provider match (highest priority)
	if canon := strings.ToLower(strings.TrimSpace(raw.CanonicalModelID)); canon != "" {
		if strings.HasPrefix(canon, providerID+"/") {
			score += 1000
		}
	}
	if expected := expectedFirstPartyProvider(origID); expected != "" && providerID == expected {
		score += 1200
	}

	// 2. Primary cloud / reputable catalog providers
	switch providerID {
	case "anthropic", "openai", "google", "deepseek", "xai", "zhipuai", "moonshotai", "minimax", "mistral", "cohere":
		score += 300
	case "openrouter", "google-vertex", "google-vertex-anthropic", "amazon-bedrock", "azure", "azure-cognitive-services":
		score += 180
	case "groq", "togetherai", "fireworks-ai", "perplexity", "alibaba", "dashscope":
		score += 120
	}

	// 3. Exact raw ID match (no extra prefix/variant)
	if strings.EqualFold(rawModelID, origID) {
		score += 40
	}

	// 4. Richer cost metadata
	if raw.Cost != nil {
		if raw.Cost.CacheRead != nil && *raw.Cost.CacheRead > 0 {
			score += 20
		}
		if raw.Cost.CacheWrite != nil && *raw.Cost.CacheWrite > 0 {
			score += 20
		}
		if len(raw.Cost.Tiers) > 0 || raw.Cost.ContextOver200k != nil {
			score += 15
		}
		if raw.Cost.Input != nil && *raw.Cost.Input > 0 {
			score += 10
		}
	}

	// 5. Penalize deprecated / :free variants as default
	if strings.EqualFold(raw.Status, "deprecated") {
		score -= 400
	}
	if strings.Contains(strings.ToLower(rawModelID), ":free") {
		score -= 250
	}
	return score
}

func expectedFirstPartyProvider(origID string) string {
	switch {
	case strings.HasPrefix(origID, "claude-"):
		return "anthropic"
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
		return "openai"
	case strings.HasPrefix(origID, "gemini-"),
		strings.HasPrefix(origID, "gemma-"),
		strings.HasPrefix(origID, "imagen-"),
		strings.HasPrefix(origID, "veo-"):
		return "google"
	case strings.HasPrefix(origID, "deepseek-"):
		return "deepseek"
	case strings.HasPrefix(origID, "grok-"):
		return "xai"
	case strings.HasPrefix(origID, "glm-"):
		return "zhipuai"
	case strings.HasPrefix(origID, "kimi-"),
		strings.HasPrefix(origID, "moonshot-"):
		return "moonshotai"
	case strings.HasPrefix(origID, "minimax-"):
		return "minimax"
	case strings.HasPrefix(origID, "mistral"),
		strings.HasPrefix(origID, "codestral"),
		strings.HasPrefix(origID, "pixtral"),
		strings.HasPrefix(origID, "ministral"),
		strings.HasPrefix(origID, "magistral"):
		return "mistral"
	default:
		return ""
	}
}

// --- Materializing effective default pricing & resolving selections ---

func (s *PricingService) resolveSelectionToCatalogLocked(modelID string, sel ModelPricingSelection) *CatalogModelPricing {
	switch sel.Mode {
	case PricingSelectionModeCustom:
		if sel.Custom == nil {
			return nil
		}
		cat := tokenPricingPerMillionToCatalog(*sel.Custom, "custom", "chat", true)
		cat.priceOverrides = tokenPriceOverrides{
			input:     true,
			output:    true,
			cacheRead: true,
		}
		return cat
	case PricingSelectionModeProvider:
		if grp := s.modelGroups[modelID]; grp != nil {
			if cand, ok := grp.candidatesByKey[sel.CandidateKey]; ok && cand != nil {
				return cand.catalog
			}
			// Fallback by provider_id if raw_model_id changed slightly across syncs
			if sel.ProviderID != "" {
				for i := range grp.Candidates {
					if strings.EqualFold(grp.Candidates[i].ProviderID, sel.ProviderID) {
						return grp.Candidates[i].catalog
					}
				}
			}
		}
		return nil
	default:
		return nil
	}
}

// rebuildEffectiveDefaultLocked rebuilds s.effectiveDefaultData from models.dev groups + Default overrides.
// Caller must hold s.mu write lock.
func (s *PricingService) rebuildEffectiveDefaultLocked() {
	next := make(map[string]*CatalogModelPricing, len(s.modelGroups)+len(s.overrides.Default))

	// 1. Start with auto-selected default candidate for each models.dev model group
	for modelID, grp := range s.modelGroups {
		if len(grp.Candidates) > 0 && grp.Candidates[0].catalog != nil {
			next[modelID] = grp.Candidates[0].catalog
		}
	}

	// 2. Overlay global default selections (switched cached provider or custom prices, including custom-only models)
	for modelID, sel := range s.overrides.Default {
		normID := NormalizeOriginalModelID(modelID)
		if normID == "" {
			continue
		}
		if cat := s.resolveSelectionToCatalogLocked(normID, sel); cat != nil {
			next[normID] = cat
		}
	}

	s.effectiveDefaultData = next
}

// --- Admin Mutations (Switch cached price, Edit custom price, Reset) ---

// SelectCachedProvider switches the active models.dev cached provider price for a model
// (in default scope when groupID == nil or <= 0, or in a specific group scope when groupID > 0).
func (s *PricingService) SelectCachedProvider(ctx context.Context, groupID *int64, modelID, candidateKey string) error {
	normID := NormalizeOriginalModelID(modelID)
	if normID == "" {
		return errors.New("model_id is required")
	}
	candidateKey = strings.TrimSpace(candidateKey)
	if candidateKey == "" {
		return errors.New("candidate_key is required")
	}

	s.mu.Lock()
	grp := s.modelGroups[normID]
	if grp == nil {
		s.mu.Unlock()
		return fmt.Errorf("model %q not found in models.dev cache", normID)
	}
	cand, ok := grp.candidatesByKey[candidateKey]
	if !ok || cand == nil {
		s.mu.Unlock()
		return fmt.Errorf("cached provider candidate %q not found for model %q", candidateKey, normID)
	}

	sel := ModelPricingSelection{
		Mode:         PricingSelectionModeProvider,
		CandidateKey: cand.CandidateKey,
		ProviderID:   cand.ProviderID,
		RawModelID:   cand.RawModelID,
		UpdatedAt:    time.Now(),
	}

	if groupID != nil && *groupID > 0 {
		gidKey := strconv.FormatInt(*groupID, 10)
		if s.overrides.Groups == nil {
			s.overrides.Groups = make(map[string]map[string]ModelPricingSelection)
		}
		if s.overrides.Groups[gidKey] == nil {
			s.overrides.Groups[gidKey] = make(map[string]ModelPricingSelection)
		}
		s.overrides.Groups[gidKey][normID] = sel
	} else {
		if s.overrides.Default == nil {
			s.overrides.Default = make(map[string]ModelPricingSelection)
		}
		// Selecting the auto-recommended candidate clears redundant default override
		if cand.CandidateKey == grp.DefaultCandidateKey {
			delete(s.overrides.Default, normID)
		} else {
			s.overrides.Default[normID] = sel
		}
		s.rebuildEffectiveDefaultLocked()
	}

	overridesCopy := clonePricingOverridesState(s.overrides)
	s.mu.Unlock()

	return s.persistSettingJSON(ctx, SettingKeyModelPricingOverrides, overridesCopy)
}

// UpsertCustomPricing saves custom per-million-token prices for a model
// (either in default scope or for a specific group).
func (s *PricingService) UpsertCustomPricing(ctx context.Context, groupID *int64, modelID string, custom TokenPricingPerMillion) error {
	normID := NormalizeOriginalModelID(modelID)
	if normID == "" {
		return errors.New("model_id is required")
	}
	if err := validateCustomTokenPricing(custom); err != nil {
		return err
	}
	custom.DisplayName = strings.TrimSpace(custom.DisplayName)

	customCopy := custom
	sel := ModelPricingSelection{
		Mode:      PricingSelectionModeCustom,
		Custom:    &customCopy,
		UpdatedAt: time.Now(),
	}

	s.mu.Lock()
	if groupID != nil && *groupID > 0 {
		gidKey := strconv.FormatInt(*groupID, 10)
		if s.overrides.Groups == nil {
			s.overrides.Groups = make(map[string]map[string]ModelPricingSelection)
		}
		if s.overrides.Groups[gidKey] == nil {
			s.overrides.Groups[gidKey] = make(map[string]ModelPricingSelection)
		}
		s.overrides.Groups[gidKey][normID] = sel
	} else {
		if s.overrides.Default == nil {
			s.overrides.Default = make(map[string]ModelPricingSelection)
		}
		s.overrides.Default[normID] = sel
		s.rebuildEffectiveDefaultLocked()
	}
	overridesCopy := clonePricingOverridesState(s.overrides)
	s.mu.Unlock()

	return s.persistSettingJSON(ctx, SettingKeyModelPricingOverrides, overridesCopy)
}

// ResetModelPricing resets a model's override:
//   - If groupID > 0: removes the group override so the group falls back to the default price.
//   - If groupID == nil or <= 0: removes the default override (restores models.dev default, or deletes custom-only model).
func (s *PricingService) ResetModelPricing(ctx context.Context, groupID *int64, modelID string) error {
	normID := NormalizeOriginalModelID(modelID)
	if normID == "" {
		return errors.New("model_id is required")
	}

	s.mu.Lock()
	if groupID != nil && *groupID > 0 {
		gidKey := strconv.FormatInt(*groupID, 10)
		if gm := s.overrides.Groups[gidKey]; gm != nil {
			delete(gm, normID)
			if len(gm) == 0 {
				delete(s.overrides.Groups, gidKey)
			}
		}
	} else {
		delete(s.overrides.Default, normID)
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
		Default: make(map[string]ModelPricingSelection, len(in.Default)),
		Groups:  make(map[string]map[string]ModelPricingSelection, len(in.Groups)),
	}
	for k, v := range in.Default {
		out.Default[k] = cloneModelPricingSelection(v)
	}
	for gid, gm := range in.Groups {
		if len(gm) == 0 {
			continue
		}
		m := make(map[string]ModelPricingSelection, len(gm))
		for k, v := range gm {
			m[k] = cloneModelPricingSelection(v)
		}
		out.Groups[gid] = m
	}
	return out
}

func cloneModelPricingSelection(in ModelPricingSelection) ModelPricingSelection {
	cp := in
	if in.Custom != nil {
		c := *in.Custom
		if in.Custom.CacheWrite1hPerMillion != nil {
			v := *in.Custom.CacheWrite1hPerMillion
			c.CacheWrite1hPerMillion = &v
		}
		cp.Custom = &c
	}
	return cp
}

// --- Admin Catalog Listing & Filtering (cc-switch inspired) ---

// ListModelPricingCatalog returns the paginated, grouped model pricing catalog for the admin UI.
func (s *PricingService) ListModelPricingCatalog(query ModelPricingListQuery) *ModelPricingListResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize <= 0 || query.PageSize > 200 {
		query.PageSize = 50
	}

	// Count how many groups override each model
	groupOverrideCounts := make(map[string]int)
	for _, gm := range s.overrides.Groups {
		for m := range gm {
			groupOverrideCounts[m]++
		}
	}

	var activeGroupOverrides map[string]ModelPricingSelection
	if query.GroupID > 0 {
		activeGroupOverrides = s.overrides.Groups[strconv.FormatInt(query.GroupID, 10)]
	}

	// Union of all model IDs: models.dev groups + default overrides + active group overrides
	allIDs := make(map[string]struct{}, len(s.modelGroups)+len(s.overrides.Default)+len(activeGroupOverrides))
	for id := range s.modelGroups {
		allIDs[id] = struct{}{}
	}
	for id := range s.overrides.Default {
		allIDs[id] = struct{}{}
	}
	for id := range activeGroupOverrides {
		allIDs[id] = struct{}{}
	}

	needle := strings.ToLower(strings.TrimSpace(query.Search))
	providerFilter := strings.ToLower(strings.TrimSpace(query.Provider))
	filterMode := strings.ToLower(strings.TrimSpace(query.Filter))

	items := make([]ModelPricingListItem, 0, len(allIDs))
	for modelID := range allIDs {
		item := s.buildListItemLocked(modelID, query.GroupID, activeGroupOverrides, groupOverrideCounts[modelID])

		// Provider filter
		if providerFilter != "" && providerFilter != "all" {
			if providerFilter == "custom" {
				if !item.IsCustomOnly && item.Source != "default_custom" && item.Source != "group_custom" {
					continue
				}
			} else {
				matchedProvider := false
				for _, c := range item.Candidates {
					if c.ProviderID == providerFilter {
						matchedProvider = true
						break
					}
				}
				if !matchedProvider {
					continue
				}
			}
		}

		// Status filter
		switch filterMode {
		case "overridden":
			if query.GroupID > 0 {
				if !item.IsGroupOverridden {
					continue
				}
			} else {
				if !item.IsDefaultModified && !item.IsCustomOnly {
					continue
				}
			}
		case "custom":
			if !item.IsCustomOnly && item.Source != "default_custom" && item.Source != "group_custom" {
				continue
			}
		case "multi_provider":
			if item.CandidateCount < 2 {
				continue
			}
		}

		// Search filter
		if needle != "" && !matchesPricingSearch(item, needle) {
			continue
		}

		items = append(items, item)
	}

	// Ranking: exact model ID match first -> overridden/custom priority -> release date desc -> model ID asc
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

	providers := make([]ModelsDevProviderSummary, len(s.providerSummaries))
	copy(providers, s.providerSummaries)

	return &ModelPricingListResult{
		Items:     items[start:end],
		Total:     total,
		Page:      query.Page,
		PageSize:  query.PageSize,
		Providers: providers,
	}
}

func (s *PricingService) buildListItemLocked(
	modelID string,
	groupID int64,
	activeGroupOverrides map[string]ModelPricingSelection,
	groupOverrideCount int,
) ModelPricingListItem {
	grp := s.modelGroups[modelID]
	defaultSel, hasDefaultSel := s.overrides.Default[modelID]

	item := ModelPricingListItem{
		ModelID:            modelID,
		DisplayName:        modelID,
		IsCustomOnly:       grp == nil,
		IsDefaultModified:  hasDefaultSel,
		GroupOverrideCount: groupOverrideCount,
	}

	if grp != nil {
		item.DisplayName = grp.DisplayName
		item.Family = grp.Family
		item.ReleaseDate = grp.ReleaseDate
		item.Candidates = make([]ModelsDevCandidate, len(grp.Candidates))
		copy(item.Candidates, grp.Candidates)
		item.CandidateCount = len(grp.Candidates)
	} else {
		item.Candidates = []ModelsDevCandidate{}
	}

	// Resolve default pricing first
	defaultPricing, defaultSource, defProvID, defProvName, defRawID, defCandKey, defUpdatedAt := s.resolveScopeItemPricingLocked(grp, defaultSel, hasDefaultSel, false)

	if groupID > 0 {
		grpSel, hasGrpSel := activeGroupOverrides[modelID]
		if hasGrpSel {
			effPricing, effSource, provID, provName, rawID, candKey, updatedAt := s.resolveScopeItemPricingLocked(grp, grpSel, true, true)
			item.EffectivePricing = effPricing
			item.DefaultPricing = &defaultPricing
			item.Source = effSource
			item.IsGroupOverridden = true
			item.ActiveProviderID = provID
			item.ActiveProviderName = provName
			item.ActiveRawModelID = rawID
			item.ActiveCandidateKey = candKey
			item.UpdatedAt = updatedAt
			if effPricing.DisplayName != "" {
				item.DisplayName = effPricing.DisplayName
			}
			return item
		}
		// Inherit default price when group has no custom setting
		item.EffectivePricing = defaultPricing
		item.DefaultPricing = &defaultPricing
		item.Source = "inherited_default"
		item.IsGroupOverridden = false
		item.ActiveProviderID = defProvID
		item.ActiveProviderName = defProvName
		item.ActiveRawModelID = defRawID
		item.ActiveCandidateKey = defCandKey
		item.UpdatedAt = defUpdatedAt
		if defaultPricing.DisplayName != "" {
			item.DisplayName = defaultPricing.DisplayName
		}
		return item
	}

	item.EffectivePricing = defaultPricing
	item.Source = defaultSource
	item.ActiveProviderID = defProvID
	item.ActiveProviderName = defProvName
	item.ActiveRawModelID = defRawID
	item.ActiveCandidateKey = defCandKey
	item.UpdatedAt = defUpdatedAt
	if defaultPricing.DisplayName != "" {
		item.DisplayName = defaultPricing.DisplayName
	}
	return item
}

func (s *PricingService) resolveScopeItemPricingLocked(
	grp *ModelsDevModelGroup,
	sel ModelPricingSelection,
	hasSel bool,
	isGroup bool,
) (TokenPricingPerMillion, string, string, string, string, string, *time.Time) {
	if hasSel {
		updated := sel.UpdatedAt
		if sel.Mode == PricingSelectionModeCustom && sel.Custom != nil {
			source := "default_custom"
			if isGroup {
				source = "group_custom"
			}
			return *sel.Custom, source, "custom", "Custom", "", "", &updated
		}
		if sel.Mode == PricingSelectionModeProvider && grp != nil {
			cand := grp.candidatesByKey[sel.CandidateKey]
			if cand == nil && sel.ProviderID != "" {
				for i := range grp.Candidates {
					if strings.EqualFold(grp.Candidates[i].ProviderID, sel.ProviderID) {
						cand = &grp.Candidates[i]
						break
					}
				}
			}
			if cand != nil {
				source := "default_provider"
				if isGroup {
					source = "group_provider"
				}
				return cand.Pricing, source, cand.ProviderID, cand.ProviderName, cand.RawModelID, cand.CandidateKey, &updated
			}
		}
	}
	if grp != nil && len(grp.Candidates) > 0 {
		c := grp.Candidates[0]
		return c.Pricing, "default_auto", c.ProviderID, c.ProviderName, c.RawModelID, c.CandidateKey, nil
	}
	return TokenPricingPerMillion{}, "default_auto", "", "", "", "", nil
}

func matchesPricingSearch(item ModelPricingListItem, needle string) bool {
	if strings.Contains(item.ModelID, needle) ||
		strings.Contains(strings.ToLower(item.DisplayName), needle) ||
		strings.Contains(strings.ToLower(item.Family), needle) ||
		strings.Contains(strings.ToLower(item.ActiveProviderID), needle) ||
		strings.Contains(strings.ToLower(item.ActiveProviderName), needle) {
		return true
	}
	for _, c := range item.Candidates {
		if strings.Contains(strings.ToLower(c.RawModelID), needle) ||
			strings.Contains(strings.ToLower(c.ProviderID), needle) ||
			strings.Contains(strings.ToLower(c.ProviderName), needle) {
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

// --- Runtime Billing Lookups ---

// GetModelPricingForGroup returns a group-specific pricing override/selection if configured.
// Returns (nil, false) when the group has no separate override (caller should fall back to default pricing).
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
		if sel, ok := gm[norm]; ok {
			if cat := s.resolveSelectionToCatalogLocked(norm, sel); cat != nil {
				return cat, true
			}
		}
		if sel, ok := gm[cand]; ok {
			if cat := s.resolveSelectionToCatalogLocked(cand, sel); cat != nil {
				return cat, true
			}
		}
	}
	return nil, false
}

// GetModelPricing 获取模型默认价格（走 models.dev 缓存 + 默认切换/自定义覆盖）
func (s *PricingService) GetModelPricing(modelName string) *CatalogModelPricing {
	if s == nil || strings.TrimSpace(modelName) == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	// 1~3. 确定性识别（精确名 / 规范化原始 ID / 已知拼写变体 / 去掉日期版本后缀）
	if pricing := s.lookupIdentifiedModelPricingLocked(lookupCandidates); pricing != nil {
		return pricing
	}

	// 4. 基于模型系列在缓存目录中匹配（Claude）
	if pricing := s.matchByModelFamilyLocked(lookupCandidates[0]); pricing != nil {
		return pricing
	}

	// 5. OpenAI 模型在缓存目录中的变体回退
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

	// 1. 精确匹配（包含原始模型 ID 规范化结果）
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

	// 2. 常见点号/连字符拼写变体
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

	// 3. 去掉日期/版本后缀后匹配（排序键保证确定性）
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

// ListModelNamesByProvider returns all normalized original model IDs in the models.dev cache
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

	// Also support provider aliases if needed (e.g. "gemini" -> "google", "moonshot" -> "moonshotai", "zhipu" -> "zhipuai")
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
