package devin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestFormatSessionTokenAndCallbackParsing(t *testing.T) {
	require.Equal(t, "devin-session-token$eyJabc", FormatSessionToken("eyJabc"))
	require.Equal(t, "devin-session-token$eyJabc", FormatSessionToken("devin-session-token$eyJabc"))
	require.Equal(t, "cog_12345", FormatSessionToken("cog_12345"))

	code, state := ParseCallbackInput("http://127.0.0.1:8085/callback?code=abc123&state=xyz789")
	require.Equal(t, "abc123", code)
	require.Equal(t, "xyz789", state)

	code2, state2 := ParseCallbackInput("bare_code_only")
	require.Equal(t, "bare_code_only", code2)
	require.Empty(t, state2)
}

func TestBuildAuthorizationURL(t *testing.T) {
	headless := BuildAuthorizationURL("", "", "challenge123", "state456")
	require.True(t, strings.HasPrefix(headless, "https://app.devin.ai/auth/cli/continue?"))
	require.Contains(t, headless, "cli_pkce_marker=1")
	require.Contains(t, headless, "code_challenge=challenge123")
	require.Contains(t, headless, "state=state456")

	withRedirect := BuildAuthorizationURL("https://app.devin.ai", "http://127.0.0.1:8085/callback", "chal", "st")
	require.Contains(t, withRedirect, "redirect_uri=http%3A%2F%2F127.0.0.1%3A8085%2Fcallback")
	require.NotContains(t, withRedirect, "cli_pkce_marker=1")
}

func TestResolveChatModelUID(t *testing.T) {
	require.Equal(t, "swe-2-high", ResolveChatModelUID("swe-2", "", 0))
	require.Equal(t, "swe-2-max", ResolveChatModelUID("devin/swe-2", "max", 0))
	require.Equal(t, "swe-2-high", ResolveChatModelUID("swe-2-high", "low", 0))
	require.Equal(t, "MODEL_PRIVATE_11", ResolveChatModelUID("claude-haiku-4-5", "", 0))
	require.Equal(t, "claude-opus-5-5-xhigh", ResolveChatModelUID("claude-opus-5.5", "xhigh", 0))
	require.Equal(t, "claude-opus-5-5-high", ResolveChatModelUID("claude-opus-5-5", "", 24000))
	require.Equal(t, "claude-opus-4-6-thinking", ResolveChatModelUID("claude-opus-4-6", "high", 0))
	require.Equal(t, "claude-opus-4-6", ResolveChatModelUID("claude-opus-4-6", "none", 0))
	require.Equal(t, "gemini-4-carbon-high", ResolveChatModelUID("gemini-4-carbon", "", 0))
	require.Equal(t, "gpt-6-astra-medium", ResolveChatModelUID("gpt-6-astra", "", 0))
}

func TestUserStatusRoundTrip(t *testing.T) {
	resetDaily := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	resetWeekly := time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)

	var orgBytes []byte
	orgBytes = protowire.AppendTag(orgBytes, 4, protowire.BytesType)
	orgBytes = protowire.AppendString(orgBytes, "org_123")
	orgBytes = protowire.AppendTag(orgBytes, 8, protowire.BytesType)
	orgBytes = protowire.AppendString(orgBytes, "Acme")

	var planInfo []byte
	planInfo = protowire.AppendTag(planInfo, 2, protowire.BytesType)
	planInfo = protowire.AppendString(planInfo, "Pro")
	planInfo = protowire.AppendTag(planInfo, 33, protowire.BytesType)
	planInfo = protowire.AppendBytes(planInfo, orgBytes)

	var planStatus []byte
	planStatus = protowire.AppendTag(planStatus, 1, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planInfo)
	planStatus = protowire.AppendTag(planStatus, 14, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 72)
	planStatus = protowire.AppendTag(planStatus, 15, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 45)
	planStatus = protowire.AppendTag(planStatus, 17, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, uint64(resetDaily.Unix()))
	planStatus = protowire.AppendTag(planStatus, 18, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, uint64(resetWeekly.Unix()))

	var userStatus []byte
	userStatus = protowire.AppendTag(userStatus, 3, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "alice")
	userStatus = protowire.AppendTag(userStatus, 7, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "alice@example.com")
	userStatus = protowire.AppendTag(userStatus, 13, protowire.BytesType)
	userStatus = protowire.AppendBytes(userStatus, planStatus)
	userStatus = protowire.AppendTag(userStatus, 36, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "uid_1")

	var outer []byte
	outer = protowire.AppendTag(outer, 1, protowire.BytesType)
	outer = protowire.AppendBytes(outer, userStatus)

	parsed, err := ParseGetUserStatusResponse(outer)
	require.NoError(t, err)
	require.Equal(t, "alice", parsed.UserName)
	require.Equal(t, "alice@example.com", parsed.Email)
	require.Equal(t, "uid_1", parsed.UserID)
	require.Equal(t, "org_123", parsed.OrgID)
	require.Equal(t, "Acme", parsed.OrgName)
	require.Equal(t, "Pro", parsed.Plan)
	require.Equal(t, int64(72), parsed.DailyQuotaRemainingPercent)
	require.Equal(t, int64(45), parsed.WeeklyQuotaRemainingPercent)
	require.Equal(t, resetDaily, parsed.DailyQuotaResetAt)
	require.Equal(t, resetWeekly, parsed.WeeklyQuotaResetAt)
}

func TestConnectEnvelopeAndGzip(t *testing.T) {
	small := []byte("hello world")
	framedSmall, compressed := WrapConnectEnvelope(small)
	require.False(t, compressed)
	flag, payload, err := ReadConnectFrame(bytes.NewReader(framedSmall))
	require.NoError(t, err)
	require.Equal(t, ConnectFlagData, flag)
	require.Equal(t, small, payload)

	large := bytes.Repeat([]byte("a"), 16*1024)
	framedLarge, compressed := WrapConnectEnvelope(large)
	require.True(t, compressed)
	flag, payload, err = ReadConnectFrame(bytes.NewReader(framedLarge))
	require.NoError(t, err)
	require.Equal(t, ConnectFlagCompressed, flag)
	require.Equal(t, large, payload)
}

func TestToolCallIDRoundTripAndImageMIMESniff(t *testing.T) {
	orig := "Bash:0#a65b6a5e"
	encoded := EncodeToolCallID(orig)
	require.True(t, strings.HasPrefix(encoded, "dv_"))
	require.Equal(t, orig, DecodeToolCallID(encoded))

	safe := "toolu_01ABC_def-123"
	require.Equal(t, safe, EncodeToolCallID(safe))
	require.Equal(t, safe, DecodeToolCallID(safe))

	pngHeader := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00})
	require.Equal(t, "image/png", DetectImageMIME(pngHeader, "image/jpeg"))
}

func TestAnthropicTranslationAndStreamProcessor(t *testing.T) {
	req := &apicompat.AnthropicRequest{
		Model:     "claude-opus-5-5",
		MaxTokens: 4096,
		System:    json.RawMessage(`"You are a helpful assistant."`),
		Messages: []apicompat.AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`[{"type":"text","text":"Check the file","cache_control":{"type":"ephemeral"}}]`),
			},
		},
		Tools: []apicompat.AnthropicTool{
			{
				Name:        "read_file",
				Description: "Reads a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		},
		OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "high"},
	}

	params, err := AnthropicRequestToDevin(req, req.Model, TranslateOptions{SessionID: "sess-1"})
	require.NoError(t, err)
	require.Equal(t, "claude-opus-5-5-high", params.ModelUID)
	require.Len(t, params.Prompts, 1)
	require.True(t, params.Prompts[0].CacheControl)
	require.Len(t, params.Tools, 1)

	wireBytes := BuildGetChatMessageRequest("devin-session-token$test", "seed", params)
	require.NotEmpty(t, wireBytes)

	proc := NewStreamProcessor("claude-opus-5-5")
	ev1 := proc.ProcessFrame(FrameResult{
		ThinkingText:   "Let me think",
		DeltaSignature: []byte("sig_123"),
	})
	require.NotEmpty(t, ev1)

	ev2 := proc.ProcessFrame(FrameResult{
		ContentText: "Sure, calling tools.",
	})
	require.NotEmpty(t, ev2)

	ev3 := proc.ProcessFrame(FrameResult{
		ToolCallDeltas: []ToolCallDelta{
			{ID: "Bash:0#1", Name: "read_file", Arguments: `{"path":"a.go"}`},
			{ID: "Bash:1#2", Name: "read_file", Arguments: `{"path":"b.go"}`},
		},
		Usage: &Usage{
			PromptTokens:     120,
			CompletionTokens: 45,
			CachedTokens:     80,
			CacheWriteTokens: 10,
		},
		StopReason: 10,
	})
	require.NotEmpty(t, ev3)

	finalEvents, resp, usage := proc.Finalize()
	require.NotEmpty(t, finalEvents)
	require.True(t, proc.HasMeaningfulContent())
	require.Equal(t, "tool_use", apicompat.AnthropicStopReasonString(resp.StopReason))
	require.Len(t, resp.Content, 4) // thinking + text + 2 tool_use blocks
	require.Equal(t, int64(120), usage.PromptTokens)
	require.Equal(t, int64(45), usage.CompletionTokens)
	require.Equal(t, int64(80), usage.CachedTokens)
	require.Equal(t, int64(10), usage.CacheWriteTokens)
}

func TestParseTrailerErrorAndQuotaPeriod(t *testing.T) {
	status, code, msg, err := ParseTrailerError([]byte(`{"error":{"code":"resource_exhausted","message":"Daily usage quota exhausted"}}`))
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "resource_exhausted", code)
	require.Equal(t, QuotaPeriodDaily, QuotaExhaustionPeriod(msg))

	_, _, msgW, _ := ParseTrailerError([]byte(`{"error":{"code":"failed_precondition","message":"Weekly quota exhausted"}}`))
	require.Equal(t, QuotaPeriodWeekly, QuotaExhaustionPeriod(msgW))
}

func TestClientExchangeCodeAndSelfProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"eyJhbGciOiJIUzI1NiJ9.test"}`))
		case "/v3/self":
			require.Equal(t, "Bearer devin-session-token$eyJhbGciOiJIUzI1NiJ9.test", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user_name":"alice","user_id":"u1","org_id":"o1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewClientWithHTTPClient(srv.Client())
	client.SetAPIBaseURL(srv.URL)

	token, err := client.ExchangeCode(context.Background(), "code123", "verifier456")
	require.NoError(t, err)
	require.Equal(t, "devin-session-token$eyJhbGciOiJIUzI1NiJ9.test", token)

	profile, err := client.FetchSelfProfile(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, "alice", profile.UserName)
	require.Equal(t, "u1", profile.UserID)
	require.Equal(t, "o1", profile.OrgID)
}
