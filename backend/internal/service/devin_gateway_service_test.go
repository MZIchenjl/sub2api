package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/devin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

type stubDevinUpstream struct {
	handler func(req *http.Request) (*http.Response, error)
}

func (s *stubDevinUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.handler(req)
}

func (s *stubDevinUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.handler(req)
}

type stubDevinAccountRepo struct {
	AccountRepository
	rateLimitedUntil time.Time
	errMessage       string
}

func (r *stubDevinAccountRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitedUntil = resetAt
	return nil
}

func (r *stubDevinAccountRepo) SetError(_ context.Context, _ int64, msg string) error {
	r.errMessage = msg
	return nil
}

func buildDevinTextResponseStream(text string, promptTokens, compTokens uint64) []byte {
	var usageBytes []byte
	usageBytes = protowire.AppendTag(usageBytes, 2, protowire.VarintType)
	usageBytes = protowire.AppendVarint(usageBytes, promptTokens)
	usageBytes = protowire.AppendTag(usageBytes, 3, protowire.VarintType)
	usageBytes = protowire.AppendVarint(usageBytes, compTokens)

	var frame1 []byte
	frame1 = protowire.AppendTag(frame1, 3, protowire.BytesType)
	frame1 = protowire.AppendString(frame1, text)
	frame1 = protowire.AppendTag(frame1, 5, protowire.VarintType)
	frame1 = protowire.AppendVarint(frame1, 2)
	frame1 = protowire.AppendTag(frame1, 7, protowire.BytesType)
	frame1 = protowire.AppendBytes(frame1, usageBytes)

	var out []byte
	out = append(out, devin.WrapConnectEnvelopeWithFlag(devin.ConnectFlagData, frame1)...)
	out = append(out, devin.WrapConnectEnvelopeWithFlag(devin.ConnectFlagEndStream, []byte(`{}`))...)
	return out
}

func TestDevinGatewayForwardAnthropicChatAndResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &stubDevinUpstream{
		handler: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, devin.GetChatMessagePath, req.URL.Path)
			require.Equal(t, "application/connect+proto", req.Header.Get("Content-Type"))
			require.Contains(t, req.Header.Get("Authorization"), "Basic devin-session-token$tok-devin-session-token$tok")
			body := buildDevinTextResponseStream("Hello from Devin!", 42, 11)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		},
	}

	svc := NewDevinGatewayService(&stubDevinAccountRepo{}, nil, upstream, nil)
	account := &Account{
		ID:       101,
		Name:     "devin-1",
		Platform: PlatformDevin,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"session_token": "devin-session-token$tok",
		},
	}

	// 1. Anthropic /v1/messages (non-stream)
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		body := []byte(`{"model":"claude-opus-5-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
		res, err := svc.Forward(context.Background(), c, account, body, false)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 42, res.Usage.InputTokens)
		require.Equal(t, 11, res.Usage.OutputTokens)
		require.Contains(t, w.Body.String(), "Hello from Devin!")
	}

	// 2. OpenAI /v1/chat/completions (non-stream)
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		body := []byte(`{"model":"swe-2","messages":[{"role":"user","content":"hi"}]}`)
		res, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 42, res.Usage.InputTokens)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
		require.Equal(t, "chat.completion", parsed["object"])
		require.Contains(t, w.Body.String(), "Hello from Devin!")
	}

	// 3. OpenAI /v1/responses (streaming)
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		body := []byte(`{"model":"gpt-6-astra","input":"hi","stream":true}`)
		res, err := svc.ForwardAsResponses(context.Background(), c, account, body, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, res.Stream)
		require.Contains(t, w.Body.String(), "Hello from Devin!")
	}
}

func TestDevinGatewayQuotaExhaustionTriggersCooldownAndFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	upstream := &stubDevinUpstream{
		handler: func(_ *http.Request) (*http.Response, error) {
			trailer := devin.WrapConnectEnvelopeWithFlag(devin.ConnectFlagEndStream, []byte(`{"error":{"code":"resource_exhausted","message":"Daily usage quota exhausted"}}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(trailer)),
			}, nil
		},
	}

	repo := &stubDevinAccountRepo{}
	svc := NewDevinGatewayService(repo, nil, upstream, nil)
	account := &Account{
		ID:       202,
		Platform: PlatformDevin,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"session_token": "devin-session-token$tok",
		},
		Extra: map[string]any{
			"devin_daily_quota_reset_at": resetAt.Format(time.RFC3339),
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"swe-2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`), false)
	require.Error(t, err)

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, resetAt, repo.rateLimitedUntil)
}
