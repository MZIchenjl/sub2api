package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubAdminModelsDevClient struct {
	body []byte
}

func (c *stubAdminModelsDevClient) FetchPricingJSON(_ context.Context, _ string) ([]byte, error) {
	return c.body, nil
}

const adminModelsDevSample = `{
	"anthropic": {
		"id": "anthropic",
		"name": "Anthropic",
		"models": {
			"claude-opus-5": {
				"id": "claude-opus-5",
				"name": "Claude Opus 5",
				"cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
			}
		}
	},
	"amazon-bedrock": {
		"id": "amazon-bedrock",
		"name": "Amazon Bedrock",
		"models": {
			"us.anthropic.claude-opus-5-v1:0": {
				"id": "us.anthropic.claude-opus-5-v1:0",
				"name": "Claude Opus 5 (Bedrock)",
				"cost": {"input": 5.5, "output": 27.5, "cache_read": 0.55, "cache_write": 6.875}
			}
		}
	},
	"openai": {
		"id": "openai",
		"name": "OpenAI",
		"models": {
			"gpt-5.4": {
				"id": "gpt-5.4",
				"name": "GPT-5.4",
				"cost": {"input": 2.5, "output": 15}
			}
		}
	},
	"openrouter": {
		"id": "openrouter",
		"name": "OpenRouter",
		"models": {
			"random/junk": {
				"id": "random/junk",
				"name": "Junk Model",
				"cost": {"input": 1, "output": 2}
			}
		}
	}
}`

func setupModelPricingTestRouter(t *testing.T) (*gin.Engine, *service.PricingService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := service.NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:           t.TempDir(),
			ModelsDevURL:      service.DefaultModelsDevURL,
			AutoSyncEnabled:   false,
			SyncIntervalHours: 24,
		},
	}, &stubAdminModelsDevClient{body: []byte(adminModelsDevSample)})

	h := NewModelPricingHandler(svc, nil)
	r := gin.New()
	g := r.Group("/admin/model-pricing")
	g.GET("/status", h.GetStatus)
	g.PUT("/sync-config", h.UpdateSyncConfig)
	g.POST("/sync", h.SyncNow)
	g.GET("/models", h.ListModels)
	g.PUT("/override", h.UpsertOverride)
	g.DELETE("/override", h.ResetOverride)
	g.PUT("/freeze", h.SetChannelFreeze)
	return r, svc
}

func TestModelPricingHandler_EndToEndFlow(t *testing.T) {
	r, _ := setupModelPricingTestRouter(t)

	// 1. Manual sync (must filter out openrouter random/junk and keep only 2 supported models)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/model-pricing/sync", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"model_count":2`)

	// 2. List supported models and verify account-type channels are populated
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/model-pricing/models?search=claude-opus-5", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var listResp struct {
		Data service.ModelPricingListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Equal(t, 1, listResp.Data.Total)
	require.Equal(t, "anthropic", listResp.Data.Items[0].Platform)
	require.Equal(t, "anthropic", listResp.Data.Items[0].DefaultChannelID)
	require.Len(t, listResp.Data.Items[0].ChannelPrices, 2) // Anthropic + AWS Bedrock

	// 3. Edit default price for claude-opus-5
	defBody := `{"model_id":"claude-opus-5","input_per_million":4.5,"output_per_million":22.5}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/admin/model-pricing/override", bytes.NewBufferString(defBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// 4. Edit custom price for group 42
	overrideBody := `{"group_id":42,"model_id":"claude-opus-5","input_per_million":3,"output_per_million":15,"cache_read_per_million":0.3,"cache_write_per_million":3.75}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/admin/model-pricing/override", bytes.NewBufferString(overrideBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// 5. Query group 42 vs group 99 (unset group uses default)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/model-pricing/models?group_id=42&search=claude-opus-5", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Equal(t, "group_custom", listResp.Data.Items[0].Source)
	require.InDelta(t, 3.0, listResp.Data.Items[0].EffectivePricing.InputPerMillion, 1e-9)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/model-pricing/models?group_id=99&search=claude-opus-5", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Equal(t, "inherited_default", listResp.Data.Items[0].Source)
	require.InDelta(t, 4.5, listResp.Data.Items[0].EffectivePricing.InputPerMillion, 1e-9)

	// 6. Reset group 42 -> back to inherited_default
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/admin/model-pricing/override?model_id=claude-opus-5&group_id=42", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/model-pricing/models?group_id=42&search=claude-opus-5", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Equal(t, "inherited_default", listResp.Data.Items[0].Source)

	// 7. Edit amazon-bedrock channel price & freeze status
	chBody := `{"channel_id":"amazon-bedrock","model_id":"claude-opus-5","input_per_million":4.9,"output_per_million":24.5,"frozen":true}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/admin/model-pricing/override", bytes.NewBufferString(chBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Verify channel price & freeze state are returned
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/model-pricing/models?search=claude-opus-5", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	var bedrockFound bool
	for _, ch := range listResp.Data.Items[0].ChannelPrices {
		if ch.ChannelID == "amazon-bedrock" {
			bedrockFound = true
			require.True(t, ch.IsOverridden)
			require.True(t, ch.IsFrozen)
			require.InDelta(t, 4.9, ch.Pricing.InputPerMillion, 1e-9)
		}
	}
	require.True(t, bedrockFound)

	// 8. Unfreeze via PUT /freeze and test manual sync overwrite_all
	freezeBody := `{"model_id":"claude-opus-5","channel_id":"amazon-bedrock","frozen":false}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/admin/model-pricing/freeze", bytes.NewBufferString(freezeBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	syncAllBody := `{"overwrite_mode":"overwrite_all"}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/model-pricing/sync", bytes.NewBufferString(syncAllBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"frozen_channel_count":0`)
}
