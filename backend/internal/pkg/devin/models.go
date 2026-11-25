package devin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// GetCliModelConfigsPath is the unary Connect-RPC endpoint returning account-visible models.
	GetCliModelConfigsPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

	// DefaultTestModel is the lightweight default model for account connection tests.
	DefaultTestModel = "swe-2-flash"

	// FreeCostTier is the model_cost_tier value indicating a model does not consume paid quota.
	FreeCostTier = 4
)

// ModelConfig represents a single model entry returned by GetCliModelConfigs or the fallback catalog.
type ModelConfig struct {
	ID              string   `json:"id"`
	DisplayName     string   `json:"display_name,omitempty"`
	OwnedBy         string   `json:"owned_by,omitempty"`
	FamilyUID       string   `json:"family_uid,omitempty"`
	ContextLength   int      `json:"context_length,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	SupportsImages  bool     `json:"supports_images,omitempty"`
	CostTier        int      `json:"cost_tier,omitempty"`
	ThinkingLevels  []string `json:"thinking_levels,omitempty"`
}

// DefaultModels returns the built-in fallback Devin model catalog.
func DefaultModels() []ModelConfig {
	out := make([]ModelConfig, len(defaultModelCatalog))
	copy(out, defaultModelCatalog)
	return out
}

// DefaultModelIDs returns the model IDs of the built-in fallback Devin catalog.
func DefaultModelIDs() []string {
	ids := make([]string, 0, len(defaultModelCatalog))
	for _, m := range defaultModelCatalog {
		ids = append(ids, m.ID)
	}
	return ids
}

var defaultModelCatalog = []ModelConfig{
	{ID: "swe-2", DisplayName: "SWE-2", OwnedBy: "cognition", ContextLength: 262000, MaxOutputTokens: 64000, SupportsImages: true, CostTier: FreeCostTier, ThinkingLevels: []string{"medium", "high", "max"}},
	{ID: "swe-2-flash", DisplayName: "SWE-2 Flash", OwnedBy: "cognition", ContextLength: 262144, MaxOutputTokens: 64000, SupportsImages: false, CostTier: FreeCostTier, ThinkingLevels: []string{"medium", "high", "max"}},
	{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-fable-5-2", DisplayName: "Claude Fable 5.2", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-fable-5-1", DisplayName: "Claude Fable 5.1", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-5", DisplayName: "Claude Opus 5", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-4-7", DisplayName: "Claude Opus 4.7", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true},
	{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", OwnedBy: "anthropic", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true},
	{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", OwnedBy: "anthropic", ContextLength: 200000, MaxOutputTokens: 64000, SupportsImages: true},
	{ID: "gpt-6-astra", DisplayName: "GPT-6 Astra", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-sol", DisplayName: "GPT-6 Sol", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-luna", DisplayName: "GPT-6 Luna", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-1-sol", DisplayName: "GPT-6.1 Sol", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-1-terra", DisplayName: "GPT-6.1 Terra", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-5-6-sol", DisplayName: "GPT-5.6 Sol", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-5-6-terra", DisplayName: "GPT-5.6 Terra", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-5-6-luna", DisplayName: "GPT-5.6 Luna", OwnedBy: "openai", ContextLength: 1000000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gemini-4-carbon", DisplayName: "Gemini 4 Carbon", OwnedBy: "google", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high"}},
	{ID: "gemini-4-argon", DisplayName: "Gemini 4 Argon", OwnedBy: "google", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high"}},
	{ID: "gemini-3-8-flash", DisplayName: "Gemini 3.8 Flash", OwnedBy: "google", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high"}},
	{ID: "grok-4-7", DisplayName: "Grok 4.7", OwnedBy: "xai", ContextLength: 500000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh"}},
	{ID: "grok-4-6", DisplayName: "Grok 4.6", OwnedBy: "xai", ContextLength: 500000, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh"}},
	{ID: "glm-5-3", DisplayName: "GLM-5.3", OwnedBy: "zhipu", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: false, ThinkingLevels: []string{"low", "high", "max"}},
	{ID: "glm-5-2", DisplayName: "GLM-5.2", OwnedBy: "zhipu", ContextLength: 200000, MaxOutputTokens: 64000, SupportsImages: false, ThinkingLevels: []string{"none", "max"}},
	{ID: "kimi-k3-1-pro", DisplayName: "Kimi K3.1 Pro", OwnedBy: "moonshot", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: true, ThinkingLevels: []string{"low", "medium", "high", "max"}},
	{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", OwnedBy: "deepseek", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: false, ThinkingLevels: []string{"low", "medium", "high"}},
	{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", OwnedBy: "deepseek", ContextLength: 1048576, MaxOutputTokens: 64000, SupportsImages: false, ThinkingLevels: []string{"low", "medium", "high"}},
}

var defaultModelIndex = func() map[string]ModelConfig {
	m := make(map[string]ModelConfig, len(defaultModelCatalog))
	for _, entry := range defaultModelCatalog {
		m[strings.ToLower(entry.ID)] = entry
	}
	return m
}()

// LookupDefaultModel finds a built-in Devin model config by ID (case-insensitive).
func LookupDefaultModel(id string) (ModelConfig, bool) {
	entry, ok := defaultModelIndex[strings.ToLower(strings.TrimSpace(id))]
	return entry, ok
}

// BuildGetCliModelConfigsRequest encodes the unary Connect-RPC GetCliModelConfigs request.
func BuildGetCliModelConfigsRequest(sessionToken, deviceSeed string) []byte {
	if deviceSeed == "" {
		deviceSeed = sessionToken
	}
	meta := BuildClientMetadataBytes(sessionToken, deviceSeed, runtime.GOOS)
	var req []byte
	req = protowire.AppendTag(req, 1, protowire.BytesType)
	req = protowire.AppendBytes(req, meta)
	return req
}

// ParseGetCliModelConfigsResponse decodes ClientModelConfig entries from GetCliModelConfigsResponse.
func ParseGetCliModelConfigsResponse(data []byte) ([]ModelConfig, error) {
	if len(data) == 0 {
		return nil, errors.New("empty GetCliModelConfigs response")
	}
	var models []ModelConfig
	seen := make(map[string]struct{})
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return nil, protowire.ParseError(n)
		}
		rem = rem[n:]
		if num == 1 && wireType == protowire.BytesType {
			itemBytes, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return nil, protowire.ParseError(m)
			}
			rem = rem[m:]
			if cfg, ok := parseClientModelConfig(itemBytes); ok {
				lowFam := strings.ToLower(cfg.FamilyUID)
				lowID := strings.ToLower(cfg.ID)
				if lowFam == "adaptive" || lowFam == "fusion" || lowID == "adaptive" || lowID == "fusion" {
					continue
				}
				if _, dup := seen[cfg.ID]; !dup {
					seen[cfg.ID] = struct{}{}
					models = append(models, cfg)
				}
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return nil, protowire.ParseError(m)
			}
			rem = rem[m:]
		}
	}
	return models, nil
}

func parseClientModelConfig(data []byte) (ModelConfig, bool) {
	var cfg ModelConfig
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return cfg, false
		}
		rem = rem[n:]
		switch wireType {
		case protowire.BytesType:
			val, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return cfg, false
			}
			rem = rem[m:]
			switch num {
			case 1:
				cfg.DisplayName = string(val)
			case 22:
				cfg.ID = string(val)
			case 23:
				parseModelInfoSubfield(val, &cfg)
			}
		case protowire.VarintType:
			val, m := protowire.ConsumeVarint(rem)
			if m <= 0 {
				return cfg, false
			}
			rem = rem[m:]
			switch num {
			case 5:
				cfg.SupportsImages = val != 0
			case 18:
				cfg.ContextLength = int(val)
			case 24:
				cfg.CostTier = int(val)
			}
		default:
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return cfg, false
			}
			rem = rem[m:]
		}
	}
	if strings.TrimSpace(cfg.ID) == "" {
		return cfg, false
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = cfg.ID
	}
	return cfg, true
}

func parseModelInfoSubfield(data []byte, cfg *ModelConfig) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n <= 0 {
			return
		}
		rem = rem[n:]
		switch wireType {
		case protowire.VarintType:
			val, m := protowire.ConsumeVarint(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			if num == 13 {
				cfg.MaxOutputTokens = int(val)
			}
		case protowire.BytesType:
			val, m := protowire.ConsumeBytes(rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
			if num == 23 {
				cfg.FamilyUID = string(val)
			}
		default:
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m <= 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

// FetchCliModelConfigs calls GetCliModelConfigs to retrieve the account's live model catalog.
func (c *Client) FetchCliModelConfigs(ctx context.Context, sessionToken, deviceSeed string) ([]ModelConfig, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("devin client is not initialized")
	}
	token := FormatSessionToken(sessionToken)
	if token == "" {
		return nil, errors.New("devin session token is required")
	}

	reqBody := BuildGetCliModelConfigsRequest(token, deviceSeed)
	endpoint := strings.TrimRight(c.ServerBaseURL(), "/") + GetCliModelConfigsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Basic %s-%s", token, token))
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Accept", "*/*")
	req.Header["User-Agent"] = []string{""}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devin GetCliModelConfigs error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	return ParseGetCliModelConfigsResponse(respBytes)
}

var knownEffortSuffixes = []string{
	"-none", "-low", "-medium", "-high", "-xhigh", "-max", "-fast", "-slow", "-priority",
	"-low-priority", "-medium-priority", "-high-priority", "-xhigh-priority", "-max-priority",
	"-low-fast", "-medium-fast", "-high-fast", "-xhigh-fast", "-max-fast", "-none-fast",
	"-thinking-1m", "-thinking", "-max-1m", "-none-1m",
	"-minimal-lightning", "-low-lightning", "-medium-lightning", "-high-lightning",
	"-xhigh-lightning", "-max-lightning", "-none-lightning",
	"_none", "_minimal", "_low", "_medium", "_high", "_xhigh", "_max", "_thinking",
}

var specialAliases = map[string]string{
	"claude-haiku-4-5": "MODEL_PRIVATE_11",
	"gpt-4-1":          "MODEL_CHAT_GPT_4_1_2025_04_14",
}

// HasEffortSuffix reports whether model already carries an explicit Devin upstream effort suffix.
func HasEffortSuffix(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, s := range knownEffortSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// NormalizeThinkingLevel maps effort strings or Anthropic thinking token budgets to Devin effort levels.
func NormalizeThinkingLevel(level string, budgetTokens int) string {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "minimal", "low", "medium", "high", "xhigh", "max", "fast":
		return normalized
	case "none", "off", "disabled":
		return "none"
	case "auto", "adaptive":
		return "high"
	}
	if budgetTokens > 0 {
		switch {
		case budgetTokens <= 4096:
			return "low"
		case budgetTokens <= 16384:
			return "medium"
		case budgetTokens <= 32768:
			return "high"
		default:
			return "max"
		}
	}
	return ""
}

// ResolveChatModelUID resolves a user/client model name and requested thinking level into a valid
// upstream Devin chat_model_uid.
func ResolveChatModelUID(rawModel, thinkingLevel string, budgetTokens int) string {
	model := strings.TrimSpace(rawModel)
	if model == "" {
		return "swe-2-high"
	}

	cleanModel := model
	if strings.HasPrefix(strings.ToLower(cleanModel), "devin/") {
		cleanModel = cleanModel[6:]
	}

	// Preserve explicit upstream UIDs (e.g. MODEL_PRIVATE_11, swe-2-high, claude-opus-5-5-xhigh).
	if strings.HasPrefix(cleanModel, "MODEL_") || HasEffortSuffix(cleanModel) {
		return cleanModel
	}

	baseModel := cleanModel
	if colonIdx := strings.LastIndex(cleanModel, ":"); colonIdx != -1 {
		baseModel = strings.TrimSpace(cleanModel[:colonIdx])
		thinkingLevel = strings.TrimSpace(cleanModel[colonIdx+1:])
	} else if openIdx := strings.LastIndex(cleanModel, "("); openIdx != -1 && strings.HasSuffix(cleanModel, ")") {
		baseModel = strings.TrimSpace(cleanModel[:openIdx])
		thinkingLevel = strings.TrimSpace(cleanModel[openIdx+1 : len(cleanModel)-1])
	}

	rawLevel := strings.ToLower(strings.TrimSpace(thinkingLevel))
	effort := NormalizeThinkingLevel(thinkingLevel, budgetTokens)
	lowerBase := strings.ToLower(baseModel)
	canonicalBase := strings.ReplaceAll(lowerBase, ".", "-")

	if alias, exists := specialAliases[canonicalBase]; exists {
		return alias
	}
	if canonicalBase == "claude-sonnet-4-5" || strings.Contains(canonicalBase, "sonnet-4-5") {
		if effort != "" && effort != "none" {
			return "MODEL_PRIVATE_3"
		}
		return "MODEL_PRIVATE_2"
	}
	if canonicalBase == "gemini-3-flash" {
		canonicalBase = "gemini-3-8-flash"
	}

	switch canonicalBase {
	case "swe-1-7":
		if effort == "medium" {
			return "swe-1-7-medium"
		}
		return "swe-1-7"
	case "swe-1-6":
		if effort == "fast" {
			return "swe-1-6-fast"
		}
		return "swe-1-6"
	case "glm-5-2":
		if effort == "none" {
			return "glm-5-2-none"
		}
		if effort == "max" {
			return "glm-5-2-max"
		}
		return "glm-5-2"
	case "glm-5-2-1m":
		if effort == "none" {
			return "glm-5-2-none-1m"
		}
		if effort == "max" {
			return "glm-5-2-max-1m"
		}
		return "glm-5-2-1m"
	case "claude-opus-4-6", "claude-sonnet-4-6":
		if effort != "" && effort != "none" {
			return canonicalBase + "-thinking"
		}
		return canonicalBase
	case "claude-opus-4-6-1m":
		if effort != "" && effort != "none" {
			return "claude-opus-4-6-thinking-1m"
		}
		return canonicalBase
	case "claude-sonnet-4-6-1m":
		if effort != "" && effort != "none" {
			return "claude-sonnet-4-6-thinking-1m"
		}
		return canonicalBase
	}

	modelInfo, found := LookupDefaultModel(canonicalBase)
	if !found && canonicalBase != lowerBase {
		modelInfo, found = LookupDefaultModel(lowerBase)
	}
	var allowedLevels []string
	if found && len(modelInfo.ThinkingLevels) > 0 {
		allowedLevels = modelInfo.ThinkingLevels
	}

	if strings.HasSuffix(canonicalBase, "-lightning") {
		stem := strings.TrimSuffix(canonicalBase, "-lightning")
		if HasEffortSuffix(stem) || len(allowedLevels) == 0 {
			return canonicalBase
		}
		def := selectDefaultEffort(stem, allowedLevels)
		return stem + "-" + clampEffort(effort, allowedLevels, def) + "-lightning"
	}

	if len(allowedLevels) == 0 {
		return canonicalBase
	}

	if base, isFast := strings.CutSuffix(rawLevel, "-fast"); isFast && effortLevelIndex(base) != -1 {
		def := selectDefaultEffort(canonicalBase, allowedLevels)
		return canonicalBase + "-" + clampEffort(base, allowedLevels, def) + "-fast"
	}

	def := selectDefaultEffort(canonicalBase, allowedLevels)
	clamped := clampEffort(effort, allowedLevels, def)
	if effort == "fast" {
		return canonicalBase + "-" + clamped + "-fast"
	}
	return canonicalBase + "-" + clamped
}

func selectDefaultEffort(baseModel string, levels []string) string {
	if strings.Contains(baseModel, "swe-2") {
		return "high"
	}
	var hasNone, hasLow, hasMedium, hasHigh bool
	for _, l := range levels {
		switch l {
		case "none":
			hasNone = true
		case "low":
			hasLow = true
		case "medium":
			hasMedium = true
		case "high":
			hasHigh = true
		}
	}
	if hasNone && hasLow && strings.HasPrefix(baseModel, "gpt-5") {
		return "low"
	}
	if hasHigh && (strings.Contains(baseModel, "gemini") ||
		strings.Contains(baseModel, "grok") ||
		strings.Contains(baseModel, "glm") ||
		strings.Contains(baseModel, "deepseek") ||
		strings.Contains(baseModel, "kimi") ||
		strings.Contains(baseModel, "nemotron")) {
		return "high"
	}
	if hasMedium {
		return "medium"
	}
	if hasHigh {
		return "high"
	}
	if hasLow {
		return "low"
	}
	return levels[0]
}

var standardEffortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func effortLevelIndex(level string) int {
	lower := strings.ToLower(strings.TrimSpace(level))
	for i, l := range standardEffortOrder {
		if l == lower {
			return i
		}
	}
	return -1
}

func clampEffort(requested string, allowed []string, defaultEffort string) string {
	if requested == "" {
		return defaultEffort
	}
	reqLower := strings.ToLower(strings.TrimSpace(requested))
	for _, a := range allowed {
		if reqLower == strings.ToLower(strings.TrimSpace(a)) {
			return a
		}
	}
	if reqLower == "none" {
		return defaultEffort
	}
	reqIdx := effortLevelIndex(reqLower)
	if reqIdx == -1 {
		return defaultEffort
	}
	bestMatch := defaultEffort
	bestDist := 999
	bestIdx := -1
	for _, a := range allowed {
		aIdx := effortLevelIndex(a)
		if aIdx == -1 {
			continue
		}
		dist := reqIdx - aIdx
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist || (dist == bestDist && aIdx > bestIdx) {
			bestDist = dist
			bestMatch = a
			bestIdx = aIdx
		}
	}
	return bestMatch
}
