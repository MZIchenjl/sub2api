package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type metaTestUpstream struct{}

func (m *metaTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

func (m *metaTestUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

func newMetaTestSSEServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		handler(w, r, body)
	}))
}

func TestMetaGatewayService_AllProtocolsAndSubscriptionFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var lastReqHeaders http.Header
	var lastReqBody []byte
	srv := newMetaTestSSEServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		lastReqHeaders = r.Header.Clone()
		lastReqBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"muse-spark-1.3-contributor"}}`,
			"",
			"event: response.subscription_usage",
			`data: {"type":"response.subscription_usage","usage":{"remaining":99}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello from Muse"}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"muse-spark-1.3-contributor","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello from Muse"}]}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}`,
			"",
		}, "\n"))
	})
	defer srv.Close()

	svc := NewMetaGatewayService(nil, nil, &metaTestUpstream{}, nil, nil)
	account := &Account{
		ID:       42,
		Platform: PlatformMeta,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "meta_test_key",
			"base_url": srv.URL,
		},
	}

	// 1. /v1/responses streaming (filters response.subscription_usage + injects contributor & muse namespace)
	t.Run("responses stream", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		reqBody := []byte(`{"model":"muse-spark-1.3-contributor","stream":true,"input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(reqBody)))

		res, err := svc.ForwardAsResponses(context.Background(), c, account, reqBody, nil)
		if err != nil {
			t.Fatalf("ForwardAsResponses failed: %v", err)
		}
		if res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 4 {
			t.Fatalf("unexpected usage: %+v", res.Usage)
		}
		if lastReqHeaders.Get("X-Tbh-Contributor-Mode") != "1" {
			t.Fatalf("expected X-Tbh-Contributor-Mode header")
		}
		if len(gjson.GetBytes(lastReqBody, "tools").Array()) != 2 {
			t.Fatalf("expected muse namespace appended to tools: %s", string(lastReqBody))
		}
		if strings.Contains(rec.Body.String(), "subscription_usage") {
			t.Fatalf("subscription_usage event must be filtered out: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Hello from Muse") {
			t.Fatalf("expected text delta in stream: %s", rec.Body.String())
		}
	})

	// 2. /v1/chat/completions non-streaming
	t.Run("chat completions non-stream", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		reqBody := []byte(`{"model":"muse-ember-1.0","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))

		_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, reqBody, nil)
		if err != nil {
			t.Fatalf("ForwardAsChatCompletions failed: %v", err)
		}
		if got := gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String(); got != "Hello from Muse" {
			t.Fatalf("unexpected chat completion content: %q (%s)", got, rec.Body.String())
		}
	})

	// 3. /v1/messages Anthropic non-streaming
	t.Run("anthropic messages non-stream", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		reqBody := []byte(`{"model":"muse-ember-1.0","max_tokens":128,"stream":false,"messages":[{"role":"user","content":"hi"}]}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(reqBody)))

		_, err := svc.Forward(context.Background(), c, account, reqBody, false)
		if err != nil {
			t.Fatalf("Forward failed: %v", err)
		}
		if got := gjson.GetBytes(rec.Body.Bytes(), "content.0.text").String(); got != "Hello from Muse" {
			t.Fatalf("unexpected anthropic text: %q (%s)", got, rec.Body.String())
		}
	})
}

func TestMetaGatewayService_RateLimitFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newMetaTestSSEServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":      "rate_limit_exceeded",
				"message":   "subscription quota exhausted",
				"resets_at": 1999999999,
			},
		})
	})
	defer srv.Close()

	svc := NewMetaGatewayService(nil, nil, &metaTestUpstream{}, nil, nil)
	account := &Account{
		ID:       99,
		Platform: PlatformMeta,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "meta_test_key",
			"base_url": srv.URL,
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	reqBody := []byte(`{"model":"muse-ember-1.0","input":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(reqBody)))

	_, err := svc.ForwardAsResponses(context.Background(), c, account, reqBody, nil)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) || failoverErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 UpstreamFailoverError, got %v", err)
	}
}
