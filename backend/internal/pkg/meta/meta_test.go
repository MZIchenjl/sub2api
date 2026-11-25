package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestDeviceFlowAndMintAPIKey(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oidc/device/authorization/":
			_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
				DeviceCode:              "meta-dc-1",
				UserCode:                "MUSE-9999",
				VerificationURI:         "https://auth.meta.com/device",
				VerificationURIComplete: "https://auth.meta.com/device?user_code=MUSE-9999",
				ExpiresIn:               600,
				Interval:                5,
			})
		case "/oidc/device/token/":
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(TokenData{
				AccessToken: "dca:token-123",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
		case "/muse-code/key":
			if r.Header.Get("Authorization") != "Bearer dca:token-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(MintedKeyResponse{
				APIKey:       "meta_live_key_abc",
				BaseURL:      DefaultAPIBaseURL,
				UserEmail:    "dev@example.com",
				UserFullName: "Meta Dev",
				SubsTierName: "Pro",
				IsSubsActive: true,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewClient()
	client.SetEndpoints(srv.URL+"/oidc/device/authorization/", srv.URL+"/oidc/device/token/", srv.URL+"/muse-code/key")

	dcr, err := client.RequestDeviceCode(context.Background(), "")
	if err != nil {
		t.Fatalf("RequestDeviceCode failed: %v", err)
	}
	if dcr.UserCode != "MUSE-9999" {
		t.Fatalf("unexpected user_code: %s", dcr.UserCode)
	}

	p1, err := client.PollDeviceTokenOnce(context.Background(), dcr.DeviceCode, "")
	if err != nil || p1.Status != PollStatusPending {
		t.Fatalf("expected pending, got %+v err=%v", p1, err)
	}

	p2, err := client.PollDeviceTokenOnce(context.Background(), dcr.DeviceCode, "")
	if err != nil || p2.Status != PollStatusAuthorized || p2.Minted == nil || p2.Minted.APIKey != "meta_live_key_abc" {
		t.Fatalf("expected authorized+minted, got %+v err=%v", p2, err)
	}
}

func TestPrepareResponsesPayloadAndMuseToolNamespace(t *testing.T) {
	raw := []byte(`{
		"model":"old",
		"stream":false,
		"generate":true,
		"safety_identifier":"abc",
		"prompt_cache_retention":null,
		"input":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]
	}`)
	prepared, err := PrepareResponsesPayload(raw, "muse-spark-1.3")
	if err != nil {
		t.Fatalf("PrepareResponsesPayload failed: %v", err)
	}
	if gjson.GetBytes(prepared, "model").String() != "muse-spark-1.3" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(prepared, "model").String())
	}
	if !gjson.GetBytes(prepared, "stream").Bool() {
		t.Fatalf("expected stream=true")
	}
	if gjson.GetBytes(prepared, "generate").Exists() || gjson.GetBytes(prepared, "prompt_cache_retention").Exists() {
		t.Fatalf("unsupported fields should be stripped: %s", string(prepared))
	}
	tools := gjson.GetBytes(prepared, "tools").Array()
	if len(tools) != 2 || tools[0].Get("name").String() != "get_weather" || tools[1].Get("type").String() != "namespace" || tools[1].Get("name").String() != "muse" {
		t.Fatalf("expected canonical muse namespace appended, got: %s", gjson.GetBytes(prepared, "tools").Raw)
	}
	// Idempotency check.
	again := EnsureMuseToolNamespace(prepared)
	if len(gjson.GetBytes(again, "tools").Array()) != 2 {
		t.Fatalf("EnsureMuseToolNamespace must be idempotent")
	}
}

func TestHeadersAndRateLimitParsing(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, ResponsesEndpointURL(""), nil)
	ApplyMetaAPIHeaders(req, "meta_key_1", true, "muse-spark-1.3-contributor")
	if req.Header.Get("X-Tbh-Contributor-Mode") != "1" {
		t.Fatalf("expected contributor header")
	}
	if req.Header.Get("X-Client-Id") != ClientSurface || !strings.HasPrefix(req.Header.Get("User-Agent"), "muse-build/") {
		t.Fatalf("unexpected identity headers: %+v", req.Header)
	}
	if req.Header.Get("X-Tbh-Session-Id") == "" {
		t.Fatalf("expected generated session ID")
	}

	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"error":{"code":"rate_limit_exceeded","message":"subscription quota exhausted","resets_at":1700003600}}`)
	resetAt, isQuota := ParseRateLimitResetAt(http.StatusTooManyRequests, body, now)
	if !isQuota || resetAt.Unix() != 1_700_003_600 {
		t.Fatalf("expected quota resetAt=1700003600, got isQuota=%v resetAt=%v", isQuota, resetAt)
	}
	if !IsSubscriptionUsageEvent("response.subscription_usage", nil) {
		t.Fatalf("expected subscription_usage detection")
	}

	if got := NormalizeModelID("meta/muse-spark-1.3-contributor"); got != "muse-spark-1.3-contributor" {
		t.Fatalf("unexpected normalized model: %s", got)
	}
	if !IsContributorModel("metamuse/muse-spark-1.3-contributor") {
		t.Fatalf("expected prefixed contributor model to be detected")
	}
	preparedPrefixed, err := PrepareResponsesPayload([]byte(`{"model":"old","input":"hi"}`), "meta-muse/muse-ember-1.0")
	if err != nil {
		t.Fatalf("PrepareResponsesPayload prefixed failed: %v", err)
	}
	if got := gjson.GetBytes(preparedPrefixed, "model").String(); got != "muse-ember-1.0" {
		t.Fatalf("expected provider prefix stripped, got %s", got)
	}
}
