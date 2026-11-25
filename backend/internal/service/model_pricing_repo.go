package service

import (
	"context"
	"time"
)

const (
	PricingOverrideScopeDefault = "default"
	PricingOverrideScopeChannel = "channel"
	PricingOverrideScopeGroup   = "group"

	// PricingMissingChannelID is the sentinel channel_id persisted for active system models
	// that currently have no pricing entry in models.dev (pricing_missing = true, prices = NULL).
	PricingMissingChannelID = "__missing__"
)

// ModelPricingCatalogRecord represents one active-model pricing row in PostgreSQL.
// Nullable token prices distinguish NULL (missing/unset) from explicit 0 (free).
type ModelPricingCatalogRecord struct {
	ChannelID                     string
	RawModelID                    string
	ModelID                       string
	Platform                      string
	DisplayName                   string
	Family                        string
	ReleaseDate                   string
	Status                        string
	Mode                          string
	PricingMissing                bool
	InputPerMillion               *float64
	OutputPerMillion              *float64
	CacheReadPerMillion           *float64
	CacheWritePerMillion          *float64
	CacheWriteExplicit            bool
	CacheWrite1hPerMillion        *float64
	CacheStoragePerMillionPerHour float64
	LongContextThreshold          int
	LongContextInputMult          float64
	LongContextOutputMult         float64
	Score                         int
	SyncedAt                      time.Time
}

// ModelPricingOverrideRecord represents one scoped custom price override or lock row in PostgreSQL.
type ModelPricingOverrideRecord struct {
	Scope                         string // "default" | "channel" | "group"
	ScopeID                       string // "" for default, channel_id for channel, group_id string for group
	ModelID                       string
	Platform                      string
	DisplayName                   string
	InputPerMillion               float64
	OutputPerMillion              float64
	CacheReadPerMillion           float64
	CacheWritePerMillion          float64
	CacheWrite1hPerMillion        *float64
	CacheStoragePerMillionPerHour float64
	LongContextThreshold          int
	LongContextInputMult          float64
	LongContextOutputMult         float64
	Locked                        bool
	IsCustom                      bool
	UpdatedAt                     time.Time
}

// ModelPricingOverrideKey uniquely identifies one row in model_pricing_overrides.
type ModelPricingOverrideKey struct {
	Scope   string
	ScopeID string
	ModelID string
}

// ModelPricingRepository persists the active models.dev catalog and scoped overrides/locks in PostgreSQL.
type ModelPricingRepository interface {
	ListCatalogEntries(ctx context.Context) ([]ModelPricingCatalogRecord, error)
	ListOverrides(ctx context.Context) ([]ModelPricingOverrideRecord, error)
	ReplaceCatalogAndReconcileOverrides(ctx context.Context, entries []ModelPricingCatalogRecord, deleteOverrideKeys []ModelPricingOverrideKey) error
	UpsertOverride(ctx context.Context, rec ModelPricingOverrideRecord, deleteKeys ...ModelPricingOverrideKey) error
	DeleteOverrides(ctx context.Context, keys ...ModelPricingOverrideKey) error
}
