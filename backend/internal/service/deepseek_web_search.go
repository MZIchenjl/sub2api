package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// featureKeyDeepSeekWebSearch is stored in Channel.FeaturesConfig (per-platform
	// bool map or bool) and Account.Extra (tri-state: "default"/"enabled"/"disabled").
	featureKeyDeepSeekWebSearch = "deepseek_web_search"

	deepSeekToolTypeWebSearch = "deepseek_web_search"
	deepSeekToolTypeReadURL   = "deepseek_read_url"

	officialDeepSeekAPIHost = "api.deepseek.com"
)

var (
	patternWebSearchToken         = []byte("web_search")
	patternDeepSeekWebSearchToken = []byte(deepSeekToolTypeWebSearch)
	patternDeepSeekReadURLToken   = []byte(deepSeekToolTypeReadURL)
)

// IsDeepSeekWebSearchEnabled reports whether this channel enables DeepSeek
// official free server-side web search for the given platform.
// Defaults to false unless explicitly enabled.
func (c *Channel) IsDeepSeekWebSearchEnabled(platform string) bool {
	if c == nil || c.FeaturesConfig == nil {
		return false
	}
	override := platformBoolOverride(c.FeaturesConfig, featureKeyDeepSeekWebSearch, platform)
	return override != nil && *override
}

// GetDeepSeekWebSearchMode returns the account-level override mode for DeepSeek
// official server-side web search:
//   - "enabled": force on (when upstream targets api.deepseek.com)
//   - "disabled": force off (even if channel enables it)
//   - "default": follow channel configuration (default off)
func (a *Account) GetDeepSeekWebSearchMode() string {
	if a == nil || (a.Type != AccountTypeAPIKey && a.Type != AccountTypeUpstream) || a.Extra == nil {
		return WebSearchModeDefault
	}
	raw, ok := a.Extra[featureKeyDeepSeekWebSearch]
	if !ok {
		return WebSearchModeDefault
	}
	if b, ok := raw.(bool); ok {
		if b {
			return WebSearchModeEnabled
		}
		return WebSearchModeDisabled
	}
	mode, ok := raw.(string)
	if !ok {
		return WebSearchModeDefault
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case WebSearchModeEnabled:
		return WebSearchModeEnabled
	case WebSearchModeDisabled:
		return WebSearchModeDisabled
	default:
		return WebSearchModeDefault
	}
}

// isOfficialDeepSeekAPIURL reports whether rawURL points directly at the
// official DeepSeek API host (api.deepseek.com). Third-party relays or
// aggregators (e.g. OpenCode Go, Ollama Cloud) placed under platform=deepseek
// return false so official-only web search adaptations are never applied to them.
func isOfficialDeepSeekAPIURL(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), officialDeepSeekAPIHost)
}

// NormalizeDeepSeekWebSearchMode canonicalizes persisted group/account override
// values: "enabled" / "disabled" are preserved; everything else normalizes to
// "" (meaning "default" / follow parent scope).
func NormalizeDeepSeekWebSearchMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case WebSearchModeEnabled:
		return WebSearchModeEnabled
	case WebSearchModeDisabled:
		return WebSearchModeDisabled
	default:
		return ""
	}
}

// GetDeepSeekWebSearchMode returns the group-level override mode for DeepSeek
// official server-side web search:
//   - "enabled": force on for official api.deepseek.com accounts in this group
//   - "disabled": force off (overrides channel config)
//   - "default": follow channel configuration (default off)
func (g *Group) GetDeepSeekWebSearchMode() string {
	if g == nil {
		return WebSearchModeDefault
	}
	switch NormalizeDeepSeekWebSearchMode(g.DeepSeekWebSearchMode) {
	case WebSearchModeEnabled:
		return WebSearchModeEnabled
	case WebSearchModeDisabled:
		return WebSearchModeDisabled
	default:
		return WebSearchModeDefault
	}
}

func getOpenAIGroupFromContext(c *gin.Context) *Group {
	if c == nil {
		return nil
	}
	value, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, ok := value.(*APIKey)
	if !ok || apiKey == nil {
		return nil
	}
	return apiKey.Group
}

// isDeepSeekOfficialWebSearchEnabled evaluates whether native DeepSeek official
// web search handling should be activated for this request.
//
// Requires BOTH:
//  1. Actual resolved upstream URL targets official api.deepseek.com.
//  2. Opt-in enabled under 3-tier precedence:
//     Account ("enabled"/"disabled") > Group ("enabled"/"disabled") > Channel > Default (false).
func (s *OpenAIGatewayService) isDeepSeekOfficialWebSearchEnabled(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	targetURL string,
) bool {
	if account == nil || !isOfficialDeepSeekAPIURL(targetURL) {
		return false
	}
	switch account.GetDeepSeekWebSearchMode() {
	case WebSearchModeEnabled:
		return true
	case WebSearchModeDisabled:
		return false
	}

	if group := getOpenAIGroupFromContext(c); group != nil {
		switch group.GetDeepSeekWebSearchMode() {
		case WebSearchModeEnabled:
			return true
		case WebSearchModeDisabled:
			return false
		}
	}

	groupID := getOpenAIGroupIDFromContext(c)
	if groupID <= 0 || s == nil || s.channelService == nil {
		return false
	}
	ch, err := s.channelService.GetChannelForGroup(ctx, groupID)
	if err != nil || ch == nil {
		return false
	}
	return ch.IsDeepSeekWebSearchEnabled(account.Platform)
}

// shouldBypassWebSearchEmulationForDeepSeek reports whether /v1/messages should
// skip third-party web_search emulation and forward directly to DeepSeek's
// official free server-side web search.
//
// Explicit account-level web_search_emulation="enabled" still wins.
func (s *OpenAIGatewayService) shouldBypassWebSearchEmulationForDeepSeek(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) bool {
	if account == nil {
		return false
	}
	if account.GetWebSearchEmulationMode() == WebSearchModeEnabled {
		return false
	}
	routingModel := upstreamRoutingModel(account, body, defaultMappedModel)
	proto := s.resolveUpstreamProtocolFor(ctx, account, APIProtocolAnthropic, routingModel)
	var targetBaseURL string
	switch proto {
	case APIProtocolAnthropic:
		targetBaseURL = account.GetAnthropicProtocolBaseURL()
	case APIProtocolResponses:
		if account.UsesNativeCNResponses() && account.IsAdaptiveAPIProtocol() {
			targetBaseURL = account.GetCNProtocolBaseURL(APIProtocolResponses)
		} else {
			targetBaseURL = account.GetOpenAIBaseURL()
		}
	default:
		targetBaseURL = account.GetOpenAIBaseURL()
	}
	return s.isDeepSeekOfficialWebSearchEnabled(ctx, c, account, targetBaseURL)
}

func isDeepSeekFlashModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "flash")
}

func isStandardWebSearchToolType(toolType string) bool {
	return toolType == "web_search" || strings.HasPrefix(toolType, "web_search_")
}

func isDeepSeekNativeWebToolType(toolType string) bool {
	return toolType == deepSeekToolTypeWebSearch || toolType == deepSeekToolTypeReadURL
}

// detectInboundDeepSeekWebTools inspects an inbound Anthropic or Responses
// request body for web search / URL reading tool declarations before a
// Chat Completions bridge drops non-function server tools.
func detectInboundDeepSeekWebTools(body []byte) (wantSearch bool, wantReadURL bool) {
	if !bytes.Contains(body, patternWebSearchToken) &&
		!bytes.Contains(body, patternDeepSeekReadURLToken) &&
		!bytes.Contains(body, []byte(toolTypeGoogleSearch)) {
		return false, false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false, false
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		t := strings.TrimSpace(tool.Get("type").String())
		switch {
		case t == deepSeekToolTypeWebSearch || isStandardWebSearchToolType(t) || t == toolTypeGoogleSearch:
			wantSearch = true
		case t == deepSeekToolTypeReadURL:
			wantReadURL = true
		}
		return true
	})
	return wantSearch, wantReadURL
}

// adaptDeepSeekAnthropicWebSearchTools ensures tools sent to DeepSeek's
// official Anthropic endpoint (/anthropic/v1/messages) do not violate DeepSeek's
// mutual-exclusion rule between standard web_search* tools and native
// deepseek_web_search / deepseek_read_url tools, and deduplicates native web tools.
func adaptDeepSeekAnthropicWebSearchTools(body []byte) []byte {
	if !bytes.Contains(body, patternDeepSeekWebSearchToken) && !bytes.Contains(body, patternDeepSeekReadURLToken) {
		return body
	}
	toolsRes := gjson.GetBytes(body, "tools")
	if !toolsRes.Exists() || !toolsRes.IsArray() {
		return body
	}

	var tools []any
	if err := json.Unmarshal([]byte(toolsRes.Raw), &tools); err != nil {
		return body
	}

	hasNative := false
	for _, raw := range tools {
		if m, ok := raw.(map[string]any); ok {
			t, _ := m["type"].(string)
			if isDeepSeekNativeWebToolType(strings.TrimSpace(t)) {
				hasNative = true
				break
			}
		}
	}
	if !hasNative {
		return body
	}

	out := make([]any, 0, len(tools))
	seenNative := make(map[string]bool, 2)
	changed := false
	for _, raw := range tools {
		m, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		t := strings.TrimSpace(stringField(m, "type"))
		if isStandardWebSearchToolType(t) {
			// DeepSeek rejects web_search* alongside deepseek_web_search / deepseek_read_url.
			changed = true
			continue
		}
		if isDeepSeekNativeWebToolType(t) {
			if seenNative[t] {
				changed = true
				continue
			}
			seenNative[t] = true
		}
		out = append(out, raw)
	}
	if !changed {
		return body
	}

	toolsBytes, err := json.Marshal(out)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tools", toolsBytes)
	if err != nil {
		return body
	}
	return updated
}

// adaptDeepSeekResponsesWebSearchTools adapts web_search tools for DeepSeek's
// official Responses endpoint (/responses):
//   - If deepseek_web_search / deepseek_read_url is already declared, drops
//     standard web_search* entries to avoid DeepSeek's 400 mutual-exclusion error.
//   - For deepseek-flash models, rewrites standard web_search / web_search_preview*
//     to {"type":"deepseek_web_search"} because DeepSeek Responses ignores the
//     generic "web_search" type on deepseek-flash.
//   - For non-flash models (e.g. deepseek-v4-pro), normalizes web_search_preview*
//     to {"type":"web_search"} while keeping native "web_search" intact.
func adaptDeepSeekResponsesWebSearchTools(body []byte, upstreamModel string) []byte {
	if !bytes.Contains(body, patternWebSearchToken) && !bytes.Contains(body, patternDeepSeekReadURLToken) {
		return body
	}
	toolsRes := gjson.GetBytes(body, "tools")
	if !toolsRes.Exists() || !toolsRes.IsArray() {
		return body
	}

	var tools []any
	if err := json.Unmarshal([]byte(toolsRes.Raw), &tools); err != nil {
		return body
	}

	hasNative := false
	for _, raw := range tools {
		if m, ok := raw.(map[string]any); ok {
			if isDeepSeekNativeWebToolType(strings.TrimSpace(stringField(m, "type"))) {
				hasNative = true
				break
			}
		}
	}

	isFlash := isDeepSeekFlashModel(upstreamModel)
	out := make([]any, 0, len(tools))
	seenSearch := false
	seenReadURL := false
	seenStdWebSearch := false
	changed := false
	standardSearchRewrittenOrDropped := false

	for _, raw := range tools {
		m, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		t := strings.TrimSpace(stringField(m, "type"))
		switch {
		case t == deepSeekToolTypeWebSearch:
			if seenSearch {
				changed = true
				continue
			}
			seenSearch = true
			out = append(out, raw)
		case t == deepSeekToolTypeReadURL:
			if seenReadURL {
				changed = true
				continue
			}
			seenReadURL = true
			out = append(out, raw)
		case isStandardWebSearchToolType(t):
			if hasNative {
				changed = true
				standardSearchRewrittenOrDropped = true
				continue
			}
			if isFlash {
				changed = true
				standardSearchRewrittenOrDropped = true
				if !seenSearch {
					seenSearch = true
					out = append(out, map[string]any{"type": deepSeekToolTypeWebSearch})
				}
				continue
			}
			// deepseek-v4-pro natively accepts {"type":"web_search"}.
			if seenStdWebSearch {
				changed = true
				continue
			}
			seenStdWebSearch = true
			if t != "web_search" {
				changed = true
				standardSearchRewrittenOrDropped = true
				out = append(out, map[string]any{"type": "web_search"})
			} else {
				out = append(out, raw)
			}
		default:
			out = append(out, raw)
		}
	}

	if !changed {
		return body
	}

	toolsBytes, err := json.Marshal(out)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tools", toolsBytes)
	if err != nil {
		return body
	}
	if standardSearchRewrittenOrDropped {
		updated = relaxStandardWebSearchToolChoice(updated, `"auto"`)
	}
	return updated
}

// adaptDeepSeekChatWebSearchTools rewrites OpenAI-style web_search /
// web_search_preview* tools to DeepSeek's native {"type":"deepseek_web_search"}
// on the official Chat Completions endpoint (/v1/chat/completions).
func adaptDeepSeekChatWebSearchTools(body []byte) []byte {
	if !bytes.Contains(body, patternWebSearchToken) && !bytes.Contains(body, patternDeepSeekReadURLToken) {
		return body
	}
	toolsRes := gjson.GetBytes(body, "tools")
	if !toolsRes.Exists() || !toolsRes.IsArray() {
		return body
	}

	var tools []any
	if err := json.Unmarshal([]byte(toolsRes.Raw), &tools); err != nil {
		return body
	}

	out := make([]any, 0, len(tools))
	seenSearch := false
	seenReadURL := false
	changed := false
	standardRewritten := false

	for _, raw := range tools {
		m, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		t := strings.TrimSpace(stringField(m, "type"))
		switch {
		case t == deepSeekToolTypeWebSearch:
			if seenSearch {
				changed = true
				continue
			}
			seenSearch = true
			out = append(out, raw)
		case t == deepSeekToolTypeReadURL:
			if seenReadURL {
				changed = true
				continue
			}
			seenReadURL = true
			out = append(out, raw)
		case isStandardWebSearchToolType(t):
			changed = true
			standardRewritten = true
			if !seenSearch {
				seenSearch = true
				out = append(out, map[string]any{"type": deepSeekToolTypeWebSearch})
			}
		default:
			out = append(out, raw)
		}
	}

	if !changed {
		return body
	}
	toolsBytes, err := json.Marshal(out)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tools", toolsBytes)
	if err != nil {
		return body
	}
	if standardRewritten {
		updated = relaxStandardWebSearchToolChoice(updated, `"auto"`)
	}
	return updated
}

// injectDeepSeekChatWebTools appends DeepSeek native web tools to a bridged
// Chat Completions body when the inbound Anthropic or Responses request
// declared web search / URL reading tools that the generic CC bridge dropped.
func injectDeepSeekChatWebTools(chatBody []byte, wantSearch bool, wantReadURL bool) []byte {
	if !wantSearch && !wantReadURL {
		return chatBody
	}
	var tools []any
	toolsRes := gjson.GetBytes(chatBody, "tools")
	if toolsRes.Exists() && toolsRes.IsArray() {
		if err := json.Unmarshal([]byte(toolsRes.Raw), &tools); err != nil {
			return chatBody
		}
	}

	hasSearch := false
	hasReadURL := false
	for _, raw := range tools {
		if m, ok := raw.(map[string]any); ok {
			switch strings.TrimSpace(stringField(m, "type")) {
			case deepSeekToolTypeWebSearch:
				hasSearch = true
			case deepSeekToolTypeReadURL:
				hasReadURL = true
			}
		}
	}

	changed := false
	if wantSearch && !hasSearch {
		tools = append(tools, map[string]any{"type": deepSeekToolTypeWebSearch})
		changed = true
	}
	if wantReadURL && !hasReadURL {
		tools = append(tools, map[string]any{"type": deepSeekToolTypeReadURL})
		changed = true
	}
	if !changed {
		return chatBody
	}
	toolsBytes, err := json.Marshal(tools)
	if err != nil {
		return chatBody
	}
	updated, err := sjson.SetRawBytes(chatBody, "tools", toolsBytes)
	if err != nil {
		return chatBody
	}
	return updated
}

// relaxStandardWebSearchToolChoice replaces an object tool_choice targeting a
// standard web_search* type (e.g. {"type":"web_search"}) with replacementRaw
// when that tool was rewritten or dropped.
func relaxStandardWebSearchToolChoice(body []byte, replacementRaw string) []byte {
	tc := gjson.GetBytes(body, "tool_choice")
	if !tc.Exists() || !tc.IsObject() {
		return body
	}
	if !isStandardWebSearchToolType(strings.TrimSpace(tc.Get("type").String())) {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tool_choice", []byte(replacementRaw))
	if err != nil {
		return body
	}
	return updated
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
