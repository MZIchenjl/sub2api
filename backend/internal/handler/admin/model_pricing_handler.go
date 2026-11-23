package admin

import (
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ModelPricingHandler manages models.dev pricing sync, cached provider switching,
// and default/per-group pricing overrides.
type ModelPricingHandler struct {
	pricingService *service.PricingService
}

// NewModelPricingHandler creates a new admin model pricing handler.
func NewModelPricingHandler(pricingService *service.PricingService) *ModelPricingHandler {
	return &ModelPricingHandler{pricingService: pricingService}
}

type updatePricingSyncConfigRequest struct {
	AutoSyncEnabled   bool   `json:"auto_sync_enabled"`
	SyncIntervalHours int    `json:"sync_interval_hours"`
	ModelsDevURL      string `json:"models_dev_url"`
}

type selectCachedProviderRequest struct {
	GroupID      *int64 `json:"group_id"`
	ModelID      string `json:"model_id" binding:"required"`
	CandidateKey string `json:"candidate_key" binding:"required"`
}

type upsertModelPricingOverrideRequest struct {
	GroupID                *int64   `json:"group_id"`
	ModelID                string   `json:"model_id" binding:"required"`
	DisplayName            string   `json:"display_name"`
	InputPerMillion        float64  `json:"input_per_million"`
	OutputPerMillion       float64  `json:"output_per_million"`
	CacheReadPerMillion    float64  `json:"cache_read_per_million"`
	CacheWritePerMillion   float64  `json:"cache_write_per_million"`
	CacheWrite1hPerMillion *float64 `json:"cache_write_1h_per_million"`
	LongContextThreshold   int      `json:"long_context_threshold"`
	LongContextInputMult   float64  `json:"long_context_input_multiplier"`
	LongContextOutputMult  float64  `json:"long_context_output_multiplier"`
}

// GetStatus returns models.dev sync configuration, metadata, and override summaries.
// GET /api/v1/admin/model-pricing/status
func (h *ModelPricingHandler) GetStatus(c *gin.Context) {
	response.Success(c, h.pricingService.GetSyncStatus())
}

// UpdateSyncConfig updates automatic sync configuration and reschedules background sync.
// PUT /api/v1/admin/model-pricing/sync-config
func (h *ModelPricingHandler) UpdateSyncConfig(c *gin.Context) {
	var req updatePricingSyncConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", err.Error()))
		return
	}

	status, err := h.pricingService.UpdateSyncConfig(c.Request.Context(), service.PricingSyncConfig{
		AutoSyncEnabled:   req.AutoSyncEnabled,
		SyncIntervalHours: req.SyncIntervalHours,
		ModelsDevURL:      req.ModelsDevURL,
	})
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_SYNC_CONFIG", err.Error()))
		return
	}
	response.Success(c, status)
}

// SyncNow triggers an immediate manual sync from models.dev.
// POST /api/v1/admin/model-pricing/sync
func (h *ModelPricingHandler) SyncNow(c *gin.Context) {
	status, err := h.pricingService.SyncNow(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("MODELS_DEV_SYNC_FAILED", err.Error()))
		return
	}
	response.Success(c, status)
}

// ListModels lists cached models.dev pricing grouped by original model ID,
// optionally scoped to a specific group (falling back to default pricing when unset).
// GET /api/v1/admin/model-pricing/models
func (h *ModelPricingHandler) ListModels(c *gin.Context) {
	var groupID int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be a non-negative integer"))
			return
		}
		groupID = parsed
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	result := h.pricingService.ListModelPricingCatalog(service.ModelPricingListQuery{
		GroupID:  groupID,
		Search:   c.Query("search"),
		Provider: c.Query("provider"),
		Filter:   c.Query("filter"),
		Page:     page,
		PageSize: pageSize,
	})
	response.Success(c, result)
}

// SelectProvider switches the active models.dev cached provider price for a model (default or per-group).
// PUT /api/v1/admin/model-pricing/select-provider
func (h *ModelPricingHandler) SelectProvider(c *gin.Context) {
	var req selectCachedProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", err.Error()))
		return
	}
	if err := h.pricingService.SelectCachedProvider(c.Request.Context(), req.GroupID, req.ModelID, req.CandidateKey); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("SELECT_PROVIDER_FAILED", err.Error()))
		return
	}
	response.Success(c, gin.H{"message": "Cached provider pricing switched successfully"})
}

// UpsertOverride creates or updates custom model pricing (for default scope or a specific group).
// PUT /api/v1/admin/model-pricing/override
func (h *ModelPricingHandler) UpsertOverride(c *gin.Context) {
	var req upsertModelPricingOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", err.Error()))
		return
	}
	err := h.pricingService.UpsertCustomPricing(c.Request.Context(), req.GroupID, req.ModelID, service.TokenPricingPerMillion{
		DisplayName:            req.DisplayName,
		InputPerMillion:        req.InputPerMillion,
		OutputPerMillion:       req.OutputPerMillion,
		CacheReadPerMillion:    req.CacheReadPerMillion,
		CacheWritePerMillion:   req.CacheWritePerMillion,
		CacheWrite1hPerMillion: req.CacheWrite1hPerMillion,
		LongContextThreshold:   req.LongContextThreshold,
		LongContextInputMult:   req.LongContextInputMult,
		LongContextOutputMult:  req.LongContextOutputMult,
	})
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PRICING_OVERRIDE", err.Error()))
		return
	}
	response.Success(c, gin.H{"message": "Model pricing saved successfully"})
}

// ResetOverride removes a custom override / provider switch (restores default price for a group,
// or restores models.dev auto price / removes custom-only model in default scope).
// DELETE /api/v1/admin/model-pricing/override?model_id=...&group_id=...
func (h *ModelPricingHandler) ResetOverride(c *gin.Context) {
	modelID := strings.TrimSpace(c.Query("model_id"))
	if modelID == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("MISSING_PARAMETER", "model_id is required"))
		return
	}
	var groupID *int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be a non-negative integer"))
			return
		}
		if parsed > 0 {
			groupID = &parsed
		}
	}

	if err := h.pricingService.ResetModelPricing(c.Request.Context(), groupID, modelID); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("RESET_PRICING_FAILED", err.Error()))
		return
	}
	response.Success(c, gin.H{"message": "Model pricing reset successfully"})
}
