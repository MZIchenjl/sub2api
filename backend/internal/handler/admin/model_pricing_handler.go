package admin

import (
	"errors"
	"io"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ModelPricingHandler manages models.dev pricing sync for system-supported models
// and default/per-group pricing overrides.
type ModelPricingHandler struct {
	pricingService *service.PricingService
	groupRepo      service.GroupRepository
}

// NewModelPricingHandler creates a new admin model pricing handler.
func NewModelPricingHandler(pricingService *service.PricingService, groupRepo service.GroupRepository) *ModelPricingHandler {
	return &ModelPricingHandler{
		pricingService: pricingService,
		groupRepo:      groupRepo,
	}
}

type updatePricingSyncConfigRequest struct {
	AutoSyncEnabled   bool   `json:"auto_sync_enabled"`
	SyncIntervalHours int    `json:"sync_interval_hours"`
	ModelsDevURL      string `json:"models_dev_url"`
}

type syncModelPricingRequest struct {
	OverwriteMode string `json:"overwrite_mode"` // "skip_locked" (default) | "overwrite_all"
}

type upsertModelPricingOverrideRequest struct {
	GroupID                *int64   `json:"group_id"`
	ChannelID              string   `json:"channel_id"`
	ModelID                string   `json:"model_id" binding:"required"`
	Platform               string   `json:"platform"`
	DisplayName            string   `json:"display_name"`
	Locked                 *bool    `json:"locked"`
	InputPerMillion        float64  `json:"input_per_million"`
	OutputPerMillion       float64  `json:"output_per_million"`
	CacheReadPerMillion    float64  `json:"cache_read_per_million"`
	CacheWritePerMillion   float64  `json:"cache_write_per_million"`
	CacheWrite1hPerMillion *float64 `json:"cache_write_1h_per_million"`
	LongContextThreshold   int      `json:"long_context_threshold"`
	LongContextInputMult   float64  `json:"long_context_input_multiplier"`
	LongContextOutputMult  float64  `json:"long_context_output_multiplier"`
}

type setChannelPriceLockRequest struct {
	ModelID   string `json:"model_id" binding:"required"`
	ChannelID string `json:"channel_id"`
	Locked    bool   `json:"locked"`
}

// GetStatus returns models.dev sync configuration, metadata, and per-group override summaries.
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

// SyncNow triggers an immediate manual sync from models.dev (filtered to system-supported models),
// applying the user-confirmed overwrite_mode ("skip_locked" or "overwrite_all").
// POST /api/v1/admin/model-pricing/sync
func (h *ModelPricingHandler) SyncNow(c *gin.Context) {
	var req syncModelPricingRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", err.Error()))
		return
	}
	mode := strings.TrimSpace(req.OverwriteMode)
	if mode == "" {
		mode = service.PricingSyncOverwriteSkipLocked
	}
	status, err := h.pricingService.SyncNowWithMode(c.Request.Context(), mode)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("MODELS_DEV_SYNC_FAILED", err.Error()))
		return
	}
	response.Success(c, status)
}

// ListModels lists system-supported models from the models.dev cache, optionally scoped
// to a specific system Group (automatically filtered to that group's platform).
// GET /api/v1/admin/model-pricing/models
func (h *ModelPricingHandler) ListModels(c *gin.Context) {
	var groupID int64
	var groupPlatform string
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be a non-negative integer"))
			return
		}
		groupID = parsed
		if groupID > 0 && h.groupRepo != nil {
			if grp, err := h.groupRepo.GetByIDLite(c.Request.Context(), groupID); err == nil && grp != nil {
				groupPlatform = grp.Platform
			}
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	result := h.pricingService.ListModelPricingCatalog(service.ModelPricingListQuery{
		GroupID:       groupID,
		GroupPlatform: groupPlatform,
		Platform:      c.Query("platform"),
		Search:        c.Query("search"),
		Filter:        c.Query("filter"),
		Page:          page,
		PageSize:      pageSize,
	})
	response.Success(c, result)
}

// UpsertOverride creates or updates custom model pricing for Default, an Account-Type Channel, or a System Group.
// PUT /api/v1/admin/model-pricing/override
func (h *ModelPricingHandler) UpsertOverride(c *gin.Context) {
	var req upsertModelPricingOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", err.Error()))
		return
	}
	platform := strings.TrimSpace(req.Platform)
	if platform == "" && req.GroupID != nil && *req.GroupID > 0 && h.groupRepo != nil {
		if grp, err := h.groupRepo.GetByIDLite(c.Request.Context(), *req.GroupID); err == nil && grp != nil && grp.Platform != service.PlatformComposite {
			platform = grp.Platform
		}
	}
	err := h.pricingService.UpsertCustomPricingWithOptions(c.Request.Context(), service.UpsertPricingOverrideInput{
		GroupID:   req.GroupID,
		ChannelID: req.ChannelID,
		ModelID:   req.ModelID,
		Platform:  platform,
		Locked:    req.Locked,
		Custom: service.TokenPricingPerMillion{
			DisplayName:            req.DisplayName,
			InputPerMillion:        req.InputPerMillion,
			OutputPerMillion:       req.OutputPerMillion,
			CacheReadPerMillion:    req.CacheReadPerMillion,
			CacheWritePerMillion:   req.CacheWritePerMillion,
			CacheWrite1hPerMillion: req.CacheWrite1hPerMillion,
			LongContextThreshold:   req.LongContextThreshold,
			LongContextInputMult:   req.LongContextInputMult,
			LongContextOutputMult:  req.LongContextOutputMult,
		},
	})
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PRICING_OVERRIDE", err.Error()))
		return
	}
	response.Success(c, gin.H{"message": "Model pricing saved successfully"})
}

// ResetOverride removes a custom override / lock (for Default, Account-Type Channel, or System Group).
// DELETE /api/v1/admin/model-pricing/override?model_id=...&group_id=...&channel_id=...
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
	channelID := strings.TrimSpace(c.Query("channel_id"))

	if err := h.pricingService.ResetModelPricingWithScope(c.Request.Context(), groupID, channelID, modelID); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("RESET_PRICING_FAILED", err.Error()))
		return
	}
	response.Success(c, gin.H{"message": "Model pricing reset successfully"})
}

// SetChannelLock locks or unlocks a specific (model_id, channel_id) price so automatic sync
// skips locked channels on subsequent updates.
// PUT /api/v1/admin/model-pricing/lock
func (h *ModelPricingHandler) SetChannelLock(c *gin.Context) {
	var req setChannelPriceLockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", err.Error()))
		return
	}
	if err := h.pricingService.SetChannelPriceLock(c.Request.Context(), req.ModelID, req.ChannelID, req.Locked); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_LOCK_REQUEST", err.Error()))
		return
	}
	response.Success(c, gin.H{"message": "Channel price lock updated"})
}
