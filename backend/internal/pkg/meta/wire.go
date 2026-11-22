package meta

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Client identity captured from Muse Code 1.4.14-R7309.1 (aligned with CLIProxyAPI).
const (
	ClientVersion = "1.4.14"
	ClientBuild   = "dec7d7497fecdad135ec5835a8161dbc92e55b58"
	ClientSurface = "tbh:exec"

	// SubscriptionUsageEventType is Meta's internal SSE event that must not be forwarded to clients.
	SubscriptionUsageEventType = "response.subscription_usage"
)

// canonicalMuseNamespace is appended to tool-bearing Responses requests so
// Meta Muse subscription-key attestation accepts caller tools.
const canonicalMuseNamespace = `{"type":"namespace","name":"muse","description":"Muse Code tool set.","tools":[{"type":"function","name":"submit_reminder_decision","description":"Submit this reminder child's explicit decision.","parameters":{"additionalProperties":false,"properties":{"advisory_text":{"description":"Short text to deliver to the main agent when reminding.","type":["null","string"]},"confidence":{"description":"Confidence in the decision.","enum":["low","medium","high"],"type":["null","string"]},"decision":{"enum":["remind","none"],"type":"string"},"priority":{"description":"Requested reminder priority.","enum":["low","normal","high"],"type":["null","string"]},"reason":{"description":"Brief reason for reminding or staying silent.","type":"string"},"skill_id":{"description":"The listed skill id to remind when needs_reminder is true.","type":["null","string"]},"visible_for_steps":{"description":"Requested ephemeral visibility length in main model steps.","maximum":8,"minimum":1,"type":["null","integer"]}},"required":["advisory_text","confidence","decision","priority","reason","skill_id","visible_for_steps"],"type":"object"},"strict":true}]}`

// MuseUserAgent returns the canonical Muse Code CLI User-Agent string.
func MuseUserAgent() string {
	goos := runtime.GOOS
	if goos == "darwin" {
		goos = "macos"
	}
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return fmt.Sprintf("muse-build/%s (non-interactive; %s-%s; build %s)", ClientVersion, goos, arch, ClientBuild)
}

// ApplyMetaClientIdentity injects Muse Code client identity and session headers.
func ApplyMetaClientIdentity(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", MuseUserAgent())
	req.Header.Set("X-Client-Id", ClientSurface)
	if strings.TrimSpace(req.Header.Get("X-Tbh-Session-Id")) != "" {
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	sessionID := id.String()
	req.Header.Set("X-Tbh-Session-Id", sessionID)
	req.Header.Set("X-Meta-Ai-Gateway-Session-Id", sessionID)
	req.Header.Set("X-Tbh-Thread-Id", sessionID)
	req.Header.Set("X-Tbh-Root-Session-Id", sessionID)
	req.Header.Set("X-Tbh-Agent-Role", "main")
}

// ApplyMetaAPIHeaders sets all required HTTP headers for a Meta Muse /responses call.
func ApplyMetaAPIHeaders(req *http.Request, token string, stream bool, model string) {
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	} else {
		req.Header.Del("Authorization")
	}
	ApplyMetaClientIdentity(req)
	if IsContributorModel(model) {
		req.Header.Set("X-Tbh-Contributor-Mode", "1")
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
	} else {
		req.Header.Set("Accept", "application/json")
	}
}

// EnsureMuseToolNamespace appends the canonical Muse namespace to tool-bearing
// Responses requests while preserving all caller tools.
func EnsureMuseToolNamespace(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	items := tools.Array()
	if len(items) == 0 {
		return body
	}
	for _, item := range items {
		if item.Get("type").String() == "namespace" && item.Get("name").String() == "muse" {
			return body
		}
	}
	var buf []byte
	buf = append(buf, '[')
	for i, item := range items {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, item.Raw...)
	}
	buf = append(buf, ',')
	buf = append(buf, canonicalMuseNamespace...)
	buf = append(buf, ']')
	updated, err := sjson.SetRawBytes(body, "tools", buf)
	if err != nil {
		return body
	}
	return updated
}

// PrepareResponsesPayload normalizes an OpenAI Responses request body for Meta Muse:
// sets model, enforces stream=true (required by api.meta.ai/v1/responses), removes
// unsupported fields, validates prompt_cache_retention, and ensures Muse tool namespace.
func PrepareResponsesPayload(raw []byte, model string) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty request payload")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON request payload")
	}
	out := append([]byte(nil), raw...)
	if strings.TrimSpace(model) != "" {
		var err error
		out, err = sjson.SetBytes(out, "model", strings.TrimSpace(model))
		if err != nil {
			return nil, err
		}
	}
	var err error
	out, err = sjson.SetBytes(out, "stream", true)
	if err != nil {
		return nil, err
	}
	// Strip fields unsupported by Meta's Responses endpoint.
	for _, field := range []string{"generate", "safety_identifier", "stream_options", "client_metadata"} {
		out, _ = sjson.DeleteBytes(out, field)
	}
	// Preserve only explicit non-empty string prompt_cache_retention.
	pcr := gjson.GetBytes(out, "prompt_cache_retention")
	if pcr.Exists() && (pcr.Type != gjson.String || strings.TrimSpace(pcr.String()) == "") {
		out, _ = sjson.DeleteBytes(out, "prompt_cache_retention")
	}
	// Ensure store=false unless explicitly provided? Meta accepts store omitted/false; keep caller's value.
	out = EnsureMuseToolNamespace(out)
	return out, nil
}

// IsSubscriptionUsageEvent reports whether an SSE event is Meta's internal subscription_usage event.
func IsSubscriptionUsageEvent(eventType string, eventData []byte) bool {
	if strings.TrimSpace(eventType) == SubscriptionUsageEventType {
		return true
	}
	if len(eventData) > 0 && gjson.GetBytes(eventData, "type").String() == SubscriptionUsageEventType {
		return true
	}
	return false
}

// ParseRateLimitResetAt inspects a Meta 429/404 error body for error.resets_at
// and reports whether it represents a subscription quota exhaustion.
func ParseRateLimitResetAt(statusCode int, body []byte, now time.Time) (resetAt time.Time, isQuota bool) {
	if (statusCode != http.StatusTooManyRequests && statusCode != http.StatusNotFound) || len(body) == 0 {
		return time.Time{}, false
	}
	if ts := gjson.GetBytes(body, "error.resets_at").Int(); ts > 0 {
		t := time.Unix(ts, 0)
		if t.After(now) {
			resetAt = t
		}
	}
	if statusCode == http.StatusTooManyRequests {
		msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
		code := strings.ToLower(gjson.GetBytes(body, "error.code").String())
		if strings.Contains(msg, "subscription quota") ||
			strings.Contains(msg, "quota exhausted") ||
			((code == "rate_limit_exceeded" || strings.Contains(code, "quota")) && gjson.GetBytes(body, "error.resets_at").Exists()) {
			isQuota = true
		}
	}
	return resetAt, isQuota
}

// ResponsesEndpointURL joins baseURL with /responses cleanly.
func ResponsesEndpointURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultAPIBaseURL
	}
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	return base + "/responses"
}
