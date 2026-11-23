//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsOfficialDeepSeekAPIURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://api.deepseek.com", true},
		{"https://api.deepseek.com/v1/chat/completions", true},
		{"https://api.deepseek.com/responses", true},
		{"https://api.deepseek.com/anthropic/v1/messages", true},
		{"https://API.DEEPSEEK.COM:443/responses", true},
		{"https://opencode.ai/zen/go", false},
		{"https://api.deepseek.com.evil.test/v1/chat/completions", false},
		{"https://api.deepseek.com@evil.test/v1/chat/completions", false},
		{"https://ollama.com/v1", false},
		{"", false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, isOfficialDeepSeekAPIURL(tc.url), "url=%q", tc.url)
	}
}

func TestChannel_IsDeepSeekWebSearchEnabled(t *testing.T) {
	require.False(t, (*Channel)(nil).IsDeepSeekWebSearchEnabled(PlatformDeepseek))
	require.False(t, (&Channel{}).IsDeepSeekWebSearchEnabled(PlatformDeepseek))

	chMap := &Channel{
		FeaturesConfig: map[string]any{
			featureKeyDeepSeekWebSearch: map[string]any{
				PlatformDeepseek: true,
				PlatformOpenAI:   false,
			},
		},
	}
	require.True(t, chMap.IsDeepSeekWebSearchEnabled(PlatformDeepseek))
	require.False(t, chMap.IsDeepSeekWebSearchEnabled(PlatformOpenAI))
	require.False(t, chMap.IsDeepSeekWebSearchEnabled(PlatformKimi))

	chBool := &Channel{
		FeaturesConfig: map[string]any{
			featureKeyDeepSeekWebSearch: true,
		},
	}
	require.True(t, chBool.IsDeepSeekWebSearchEnabled(PlatformDeepseek))
}

func TestAccount_GetDeepSeekWebSearchMode(t *testing.T) {
	require.Equal(t, WebSearchModeDefault, (*Account)(nil).GetDeepSeekWebSearchMode())
	require.Equal(t, WebSearchModeDefault, (&Account{
		Platform: PlatformDeepseek,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeEnabled},
	}).GetDeepSeekWebSearchMode())

	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"nil extra", nil, WebSearchModeDefault},
		{"empty extra", map[string]any{}, WebSearchModeDefault},
		{"string enabled", map[string]any{featureKeyDeepSeekWebSearch: "enabled"}, WebSearchModeEnabled},
		{"string disabled", map[string]any{featureKeyDeepSeekWebSearch: "disabled"}, WebSearchModeDisabled},
		{"string default", map[string]any{featureKeyDeepSeekWebSearch: "default"}, WebSearchModeDefault},
		{"string unknown", map[string]any{featureKeyDeepSeekWebSearch: "invalid"}, WebSearchModeDefault},
		{"bool true", map[string]any{featureKeyDeepSeekWebSearch: true}, WebSearchModeEnabled},
		{"bool false", map[string]any{featureKeyDeepSeekWebSearch: false}, WebSearchModeDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Extra: tc.extra}
			require.Equal(t, tc.want, acc.GetDeepSeekWebSearchMode())
		})
	}
}

func newGinContextWithGroup(groupID int64, group *Group) *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if groupID > 0 {
		c.Set("api_key", &APIKey{ID: 1, GroupID: &groupID, Group: group})
	}
	return c
}

func newGinContextWithGroupID(groupID int64) *gin.Context {
	return newGinContextWithGroup(groupID, nil)
}

func newChannelServiceForDeepSeekWebSearchTest(groupID int64, features map[string]any) *ChannelService {
	ch := &Channel{
		ID:             1,
		Name:           "test-deepseek-ch",
		Status:         StatusActive,
		GroupIDs:       []int64{groupID},
		FeaturesConfig: features,
	}
	return newChannelServiceWithCache(groupID, ch)
}

func TestOpenAIGatewayService_IsDeepSeekOfficialWebSearchEnabled(t *testing.T) {
	ctx := context.Background()
	groupID := int64(42)

	channelOn := newChannelServiceForDeepSeekWebSearchTest(groupID, map[string]any{
		featureKeyDeepSeekWebSearch: map[string]any{PlatformDeepseek: true},
	})
	channelOff := newChannelServiceForDeepSeekWebSearchTest(groupID, map[string]any{
		featureKeyDeepSeekWebSearch: map[string]any{PlatformDeepseek: false},
	})

	svcOn := &OpenAIGatewayService{channelService: channelOn}
	svcOff := &OpenAIGatewayService{channelService: channelOff}
	c := newGinContextWithGroupID(groupID)

	officialURL := "https://api.deepseek.com/anthropic/v1/messages"
	relayURL := "https://opencode.ai/zen/go/v1/messages"

	t.Run("default off when channel off and account default", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
		require.False(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, c, acc, officialURL))
	})

	t.Run("enabled via channel when account default and host official", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
		require.True(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, c, acc, officialURL))
	})

	t.Run("blocked on third-party relay even when channel and account enabled", func(t *testing.T) {
		acc := &Account{
			ID:       1,
			Platform: PlatformDeepseek,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeEnabled},
		}
		require.False(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, c, acc, relayURL))
	})

	t.Run("account disabled overrides channel enabled", func(t *testing.T) {
		acc := &Account{
			ID:       1,
			Platform: PlatformDeepseek,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeDisabled},
		}
		require.False(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, c, acc, officialURL))
	})

	t.Run("account enabled overrides channel disabled", func(t *testing.T) {
		acc := &Account{
			ID:       1,
			Platform: PlatformDeepseek,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeEnabled},
		}
		require.True(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, c, acc, officialURL))
	})

	t.Run("openai platform apikey pointing to official api.deepseek.com honors account enabled", func(t *testing.T) {
		acc := &Account{
			ID:          2,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://api.deepseek.com"},
			Extra:       map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeEnabled},
		}
		require.True(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, c, acc, "https://api.deepseek.com/responses"))
	})

	t.Run("group enabled overrides channel disabled", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformDeepseek, DeepSeekWebSearchMode: WebSearchModeEnabled})
		require.True(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, officialURL))
	})

	t.Run("group disabled overrides channel enabled", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformDeepseek, DeepSeekWebSearchMode: WebSearchModeDisabled})
		require.False(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, officialURL))
	})

	t.Run("group default follows channel enabled", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformDeepseek, DeepSeekWebSearchMode: ""})
		require.True(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, officialURL))
		require.False(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, officialURL))
	})

	t.Run("account disabled overrides group enabled", func(t *testing.T) {
		acc := &Account{
			ID:       1,
			Platform: PlatformDeepseek,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeDisabled},
		}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformDeepseek, DeepSeekWebSearchMode: WebSearchModeEnabled})
		require.False(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, officialURL))
	})

	t.Run("account enabled overrides group disabled", func(t *testing.T) {
		acc := &Account{
			ID:       1,
			Platform: PlatformDeepseek,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeEnabled},
		}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformDeepseek, DeepSeekWebSearchMode: WebSearchModeDisabled})
		require.True(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, officialURL))
	})

	t.Run("openai group enabled activates for openai apikey pointing to api.deepseek.com only", func(t *testing.T) {
		acc := &Account{
			ID:          3,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://api.deepseek.com"},
		}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformOpenAI, DeepSeekWebSearchMode: WebSearchModeEnabled})
		require.True(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, "https://api.deepseek.com/responses"))
		require.False(t, svcOff.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, "https://api.openai.com/v1/responses"))
	})

	t.Run("group enabled still blocked on third-party relay", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
		grpCtx := newGinContextWithGroup(groupID, &Group{ID: groupID, Platform: PlatformDeepseek, DeepSeekWebSearchMode: WebSearchModeEnabled})
		require.False(t, svcOn.isDeepSeekOfficialWebSearchEnabled(ctx, grpCtx, acc, relayURL))
	})
}

func TestSanitizeGroupDeepSeekWebSearch(t *testing.T) {
	for _, platform := range []string{PlatformDeepseek, PlatformComposite, PlatformOpenAI} {
		g := &Group{Platform: platform, DeepSeekWebSearchMode: " ENABLED "}
		sanitizeGroupDeepSeekWebSearch(g)
		require.Equal(t, WebSearchModeEnabled, g.DeepSeekWebSearchMode)

		g2 := &Group{Platform: platform, DeepSeekWebSearchMode: "default"}
		sanitizeGroupDeepSeekWebSearch(g2)
		require.Equal(t, "", g2.DeepSeekWebSearchMode)
	}

	gUnsupported := &Group{Platform: PlatformAnthropic, DeepSeekWebSearchMode: WebSearchModeEnabled}
	sanitizeGroupDeepSeekWebSearch(gUnsupported)
	require.Equal(t, "", gUnsupported.DeepSeekWebSearchMode)
}

func TestOpenAIGatewayService_ShouldBypassWebSearchEmulationForDeepSeek(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7)
	c := newGinContextWithGroupID(groupID)
	body := []byte(`{"model":"deepseek-flash","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"latest news"}]}`)

	channelOn := newChannelServiceForDeepSeekWebSearchTest(groupID, map[string]any{
		featureKeyDeepSeekWebSearch: map[string]any{PlatformDeepseek: true},
	})
	svc := &OpenAIGatewayService{channelService: channelOn}

	t.Run("bypasses emulation for official DeepSeek adaptive account when channel enables it", func(t *testing.T) {
		acc := &Account{
			ID:          10,
			Platform:    PlatformDeepseek,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"api_protocol": APIProtocolAdaptive},
		}
		require.True(t, svc.shouldBypassWebSearchEmulationForDeepSeek(ctx, c, acc, body, ""))
	})

	t.Run("does not bypass when account explicitly forces web_search_emulation enabled", func(t *testing.T) {
		acc := &Account{
			ID:          11,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://api.deepseek.com"},
			Extra: map[string]any{
				featureKeyDeepSeekWebSearch:  WebSearchModeEnabled,
				featureKeyWebSearchEmulation: WebSearchModeEnabled,
			},
		}
		require.False(t, svc.shouldBypassWebSearchEmulationForDeepSeek(ctx, c, acc, body, ""))
	})

	t.Run("does not bypass when base_url points to third-party relay", func(t *testing.T) {
		acc := &Account{
			ID:       12,
			Platform: PlatformDeepseek,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_protocol": APIProtocolAnthropic,
				"base_url":     "https://relay.example.com/anthropic",
			},
			Extra: map[string]any{featureKeyDeepSeekWebSearch: WebSearchModeEnabled},
		}
		require.False(t, svc.shouldBypassWebSearchEmulationForDeepSeek(ctx, c, acc, body, ""))
	})
}

func TestFilterEmulatedWebSearchHistoryBlocks(t *testing.T) {
	t.Run("preserves genuine blocks while stripping emulated blocks", func(t *testing.T) {
		mixed := []byte(`{"model":"deepseek-flash","messages":[{"role":"assistant","content":[` +
			`{"type":"server_tool_use","id":"srvtoolu_01Real","name":"deepseek_web_search","input":{"action":"search","queries":["go"]}},` +
			`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01Real","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"}]},` +
			`{"type":"server_tool_use","id":"srvtoolu_ws_fake1234","name":"web_search","input":{"query":"old"}},` +
			`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_ws_fake1234","content":[]},` +
			`{"type":"text","text":"answer"}` +
			`]}]}`)

		out := FilterEmulatedWebSearchHistoryBlocks(mixed)
		content := gjson.GetBytes(out, "messages.0.content").Array()
		require.Len(t, content, 3)
		require.Equal(t, "srvtoolu_01Real", content[0].Get("id").String())
		require.Equal(t, "web_search_tool_result", content[1].Get("type").String())
		require.Equal(t, "text", content[2].Get("type").String())
	})
}

func TestAdaptDeepSeekAnthropicWebSearchTools(t *testing.T) {
	t.Run("keeps standard Anthropic web_search_20250305 untouched when no native conflict", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-flash","tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
		out := adaptDeepSeekAnthropicWebSearchTools(in)
		require.Equal(t, string(in), string(out))
	})

	t.Run("drops standard web_search when deepseek_web_search is also declared (avoid 400 mutual exclusion)", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-flash","tools":[{"type":"web_search_20250305","name":"web_search"},{"type":"deepseek_web_search","name":"deepseek_web_search"},{"type":"deepseek_web_search","name":"deepseek_web_search"},{"name":"bash","input_schema":{"type":"object"}}]}`)
		out := adaptDeepSeekAnthropicWebSearchTools(in)
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 2)
		require.Equal(t, "deepseek_web_search", tools[0].Get("type").String())
		require.Equal(t, "bash", tools[1].Get("name").String())
	})
}

func TestAdaptDeepSeekResponsesWebSearchTools(t *testing.T) {
	t.Run("rewrites standard web_search to deepseek_web_search for deepseek-flash", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-flash","tools":[{"type":"web_search","search_context_size":"medium"},{"type":"function","name":"get_weather"}],"tool_choice":{"type":"web_search"}}`)
		out := adaptDeepSeekResponsesWebSearchTools(in, "deepseek-flash")
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 2)
		require.Equal(t, "deepseek_web_search", tools[0].Get("type").String())
		require.False(t, tools[0].Get("search_context_size").Exists())
		require.Equal(t, "function", tools[1].Get("type").String())
		require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String())
	})

	t.Run("preserves standard web_search for deepseek-v4-pro", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-v4-pro","tools":[{"type":"web_search"}]}`)
		out := adaptDeepSeekResponsesWebSearchTools(in, "deepseek-v4-pro")
		require.Equal(t, string(in), string(out))
	})

	t.Run("normalizes web_search_preview to web_search for deepseek-v4-pro", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-v4-pro","tools":[{"type":"web_search_preview"}]}`)
		out := adaptDeepSeekResponsesWebSearchTools(in, "deepseek-v4-pro")
		require.Equal(t, "web_search", gjson.GetBytes(out, "tools.0.type").String())
	})

	t.Run("drops standard web_search when deepseek_read_url already present", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-v4-pro","tools":[{"type":"web_search"},{"type":"deepseek_read_url"}]}`)
		out := adaptDeepSeekResponsesWebSearchTools(in, "deepseek-v4-pro")
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 1)
		require.Equal(t, "deepseek_read_url", tools[0].Get("type").String())
	})
}

func TestAdaptDeepSeekChatWebSearchToolsAndBridgeInjection(t *testing.T) {
	t.Run("rewrites web_search and web_search_preview on Chat Completions", func(t *testing.T) {
		in := []byte(`{"model":"deepseek-flash","tools":[{"type":"web_search"},{"type":"web_search_preview"},{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"web_search"}}`)
		out := adaptDeepSeekChatWebSearchTools(in)
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 2)
		require.Equal(t, "deepseek_web_search", tools[0].Get("type").String())
		require.Equal(t, "function", tools[1].Get("type").String())
		require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String())
	})

	t.Run("detects inbound web search tools and injects into bridged CC body", func(t *testing.T) {
		inbound := []byte(`{"model":"deepseek-flash","tools":[{"type":"web_search_20250305","name":"web_search"},{"type":"deepseek_read_url","name":"deepseek_read_url"}]}`)
		wantSearch, wantReadURL := detectInboundDeepSeekWebTools(inbound)
		require.True(t, wantSearch)
		require.True(t, wantReadURL)

		bridged := []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hi"}]}`)
		out := injectDeepSeekChatWebTools(bridged, wantSearch, wantReadURL)
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 2)
		require.Equal(t, "deepseek_web_search", tools[0].Get("type").String())
		require.Equal(t, "deepseek_read_url", tools[1].Get("type").String())

		// Idempotent: second call does not duplicate
		out2 := injectDeepSeekChatWebTools(out, wantSearch, wantReadURL)
		require.Len(t, gjson.GetBytes(out2, "tools").Array(), 2)
	})
}

func TestBuildUpstreamRequest_DeepSeekResponsesWebSearchOptIn(t *testing.T) {
	ctx := context.Background()
	groupID := int64(99)
	c := newGinContextWithGroupID(groupID)
	body := []byte(`{"model":"deepseek-flash","input":"latest news","tools":[{"type":"web_search"}]}`)

	t.Run("does NOT rewrite when both channel and account are off", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		acc := &Account{
			ID:          1,
			Platform:    PlatformDeepseek,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"api_protocol": APIProtocolResponses, "api_key": "sk-test"},
		}
		req, err := svc.buildUpstreamRequest(ctx, c, acc, body, "sk-test", false, "", false)
		require.NoError(t, err)
		sent, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, "web_search", gjson.GetBytes(sent, "tools.0.type").String())
	})

	t.Run("rewrites to deepseek_web_search when channel enables it on official api.deepseek.com", func(t *testing.T) {
		chSvc := newChannelServiceForDeepSeekWebSearchTest(groupID, map[string]any{
			featureKeyDeepSeekWebSearch: map[string]any{PlatformDeepseek: true},
		})
		svc := &OpenAIGatewayService{channelService: chSvc}
		acc := &Account{
			ID:          2,
			Platform:    PlatformDeepseek,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"api_protocol": APIProtocolResponses, "api_key": "sk-test"},
		}
		req, err := svc.buildUpstreamRequest(ctx, c, acc, body, "sk-test", false, "", false)
		require.NoError(t, err)
		require.Equal(t, "https://api.deepseek.com/responses", req.URL.String())
		sent, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(sent, &parsed))
		require.Equal(t, "deepseek_web_search", gjson.GetBytes(sent, "tools.0.type").String())
		require.False(t, gjson.GetBytes(sent, "store").Bool())
	})
}
