package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type modelPricingRepository struct {
	db *sql.DB
}

// NewModelPricingRepository creates a PostgreSQL repository for model pricing catalog and overrides.
func NewModelPricingRepository(db *sql.DB) service.ModelPricingRepository {
	return &modelPricingRepository{db: db}
}

func nullableFloat64Arg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func scanNullableFloat64(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func (r *modelPricingRepository) ListCatalogEntries(ctx context.Context) ([]service.ModelPricingCatalogRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil model pricing repository")
	}

	const query = `
		SELECT
			channel_id, raw_model_id, model_id, platform,
			display_name, family, release_date, status, mode, pricing_missing,
			input_per_million, output_per_million,
			cache_read_per_million, cache_write_per_million, cache_write_explicit,
			cache_write_1h_per_million, cache_storage_per_million_per_hour,
			long_context_threshold, long_context_input_multiplier, long_context_output_multiplier,
			score, synced_at
		FROM model_pricing_catalog_entries
		ORDER BY channel_id ASC, raw_model_id ASC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query model_pricing_catalog_entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []service.ModelPricingCatalogRecord
	for rows.Next() {
		var rec service.ModelPricingCatalogRecord
		var inPM, outPM, cacheReadPM, cacheWritePM, cacheWrite1h sql.NullFloat64
		if err := rows.Scan(
			&rec.ChannelID, &rec.RawModelID, &rec.ModelID, &rec.Platform,
			&rec.DisplayName, &rec.Family, &rec.ReleaseDate, &rec.Status, &rec.Mode, &rec.PricingMissing,
			&inPM, &outPM,
			&cacheReadPM, &cacheWritePM, &rec.CacheWriteExplicit,
			&cacheWrite1h, &rec.CacheStoragePerMillionPerHour,
			&rec.LongContextThreshold, &rec.LongContextInputMult, &rec.LongContextOutputMult,
			&rec.Score, &rec.SyncedAt,
		); err != nil {
			return nil, fmt.Errorf("scan model_pricing_catalog_entries: %w", err)
		}
		rec.InputPerMillion = scanNullableFloat64(inPM)
		rec.OutputPerMillion = scanNullableFloat64(outPM)
		rec.CacheReadPerMillion = scanNullableFloat64(cacheReadPM)
		rec.CacheWritePerMillion = scanNullableFloat64(cacheWritePM)
		rec.CacheWrite1hPerMillion = scanNullableFloat64(cacheWrite1h)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model_pricing_catalog_entries: %w", err)
	}
	return out, nil
}

func (r *modelPricingRepository) ListOverrides(ctx context.Context) ([]service.ModelPricingOverrideRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil model pricing repository")
	}

	const query = `
		SELECT
			scope, scope_id, model_id, platform, display_name,
			input_per_million, output_per_million,
			cache_read_per_million, cache_write_per_million,
			cache_write_1h_per_million, cache_storage_per_million_per_hour,
			long_context_threshold, long_context_input_multiplier, long_context_output_multiplier,
			is_locked, is_custom, updated_at
		FROM model_pricing_overrides
		ORDER BY scope ASC, scope_id ASC, model_id ASC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query model_pricing_overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []service.ModelPricingOverrideRecord
	for rows.Next() {
		var rec service.ModelPricingOverrideRecord
		var cacheWrite1h sql.NullFloat64
		if err := rows.Scan(
			&rec.Scope, &rec.ScopeID, &rec.ModelID, &rec.Platform, &rec.DisplayName,
			&rec.InputPerMillion, &rec.OutputPerMillion,
			&rec.CacheReadPerMillion, &rec.CacheWritePerMillion,
			&cacheWrite1h, &rec.CacheStoragePerMillionPerHour,
			&rec.LongContextThreshold, &rec.LongContextInputMult, &rec.LongContextOutputMult,
			&rec.Locked, &rec.IsCustom, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan model_pricing_overrides: %w", err)
		}
		rec.CacheWrite1hPerMillion = scanNullableFloat64(cacheWrite1h)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model_pricing_overrides: %w", err)
	}
	return out, nil
}

func (r *modelPricingRepository) ReplaceCatalogAndReconcileOverrides(
	ctx context.Context,
	entries []service.ModelPricingCatalogRecord,
	deleteOverrideKeys []service.ModelPricingOverrideKey,
) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil model pricing repository")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM model_pricing_catalog_entries`); err != nil {
		return rollback(fmt.Errorf("clear model_pricing_catalog_entries: %w", err))
	}

	if len(entries) > 0 {
		stmt, err := tx.PrepareContext(ctx, pq.CopyIn(
			"model_pricing_catalog_entries",
			"channel_id", "raw_model_id", "model_id", "platform",
			"display_name", "family", "release_date", "status", "mode", "pricing_missing",
			"input_per_million", "output_per_million",
			"cache_read_per_million", "cache_write_per_million", "cache_write_explicit",
			"cache_write_1h_per_million", "cache_storage_per_million_per_hour",
			"long_context_threshold", "long_context_input_multiplier", "long_context_output_multiplier",
			"score", "synced_at",
		))
		if err != nil {
			return rollback(fmt.Errorf("prepare copy model_pricing_catalog_entries: %w", err))
		}

		for _, rec := range entries {
			syncedAt := rec.SyncedAt
			if syncedAt.IsZero() {
				syncedAt = time.Now().UTC()
			}
			if _, err := stmt.ExecContext(
				ctx,
				rec.ChannelID, rec.RawModelID, rec.ModelID, rec.Platform,
				rec.DisplayName, rec.Family, rec.ReleaseDate, rec.Status, rec.Mode, rec.PricingMissing,
				nullableFloat64Arg(rec.InputPerMillion), nullableFloat64Arg(rec.OutputPerMillion),
				nullableFloat64Arg(rec.CacheReadPerMillion), nullableFloat64Arg(rec.CacheWritePerMillion), rec.CacheWriteExplicit,
				nullableFloat64Arg(rec.CacheWrite1hPerMillion), rec.CacheStoragePerMillionPerHour,
				rec.LongContextThreshold, rec.LongContextInputMult, rec.LongContextOutputMult,
				rec.Score, syncedAt.UTC(),
			); err != nil {
				_ = stmt.Close()
				return rollback(fmt.Errorf("copy model_pricing_catalog_entries (%s/%s): %w", rec.ChannelID, rec.RawModelID, err))
			}
		}
		if _, err := stmt.ExecContext(ctx); err != nil {
			_ = stmt.Close()
			return rollback(fmt.Errorf("flush copy model_pricing_catalog_entries: %w", err))
		}
		if err := stmt.Close(); err != nil {
			return rollback(fmt.Errorf("close copy model_pricing_catalog_entries: %w", err))
		}
	}

	if err := deleteOverrideKeysTx(ctx, tx, deleteOverrideKeys); err != nil {
		return rollback(err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model pricing sync tx: %w", err)
	}
	return nil
}

func (r *modelPricingRepository) UpsertOverride(
	ctx context.Context,
	rec service.ModelPricingOverrideRecord,
	deleteKeys ...service.ModelPricingOverrideKey,
) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil model pricing repository")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert override tx: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}

	updatedAt := rec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	const upsertQuery = `
		INSERT INTO model_pricing_overrides (
			scope, scope_id, model_id, platform, display_name,
			input_per_million, output_per_million,
			cache_read_per_million, cache_write_per_million,
			cache_write_1h_per_million, cache_storage_per_million_per_hour,
			long_context_threshold, long_context_input_multiplier, long_context_output_multiplier,
			is_locked, is_custom, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9,
			$10, $11,
			$12, $13, $14,
			$15, $16, $17
		)
		ON CONFLICT (scope, scope_id, model_id) DO UPDATE SET
			platform = EXCLUDED.platform,
			display_name = EXCLUDED.display_name,
			input_per_million = EXCLUDED.input_per_million,
			output_per_million = EXCLUDED.output_per_million,
			cache_read_per_million = EXCLUDED.cache_read_per_million,
			cache_write_per_million = EXCLUDED.cache_write_per_million,
			cache_write_1h_per_million = EXCLUDED.cache_write_1h_per_million,
			cache_storage_per_million_per_hour = EXCLUDED.cache_storage_per_million_per_hour,
			long_context_threshold = EXCLUDED.long_context_threshold,
			long_context_input_multiplier = EXCLUDED.long_context_input_multiplier,
			long_context_output_multiplier = EXCLUDED.long_context_output_multiplier,
			is_locked = EXCLUDED.is_locked,
			is_custom = EXCLUDED.is_custom,
			updated_at = EXCLUDED.updated_at
	`

	if _, err := tx.ExecContext(
		ctx, upsertQuery,
		rec.Scope, rec.ScopeID, rec.ModelID, rec.Platform, rec.DisplayName,
		rec.InputPerMillion, rec.OutputPerMillion,
		rec.CacheReadPerMillion, rec.CacheWritePerMillion,
		nullableFloat64Arg(rec.CacheWrite1hPerMillion), rec.CacheStoragePerMillionPerHour,
		rec.LongContextThreshold, rec.LongContextInputMult, rec.LongContextOutputMult,
		rec.Locked, rec.IsCustom, updatedAt.UTC(),
	); err != nil {
		return rollback(fmt.Errorf("upsert model_pricing_overrides: %w", err))
	}

	if err := deleteOverrideKeysTx(ctx, tx, deleteKeys); err != nil {
		return rollback(err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upsert override tx: %w", err)
	}
	return nil
}

func (r *modelPricingRepository) DeleteOverrides(ctx context.Context, keys ...service.ModelPricingOverrideKey) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil model pricing repository")
	}
	if len(keys) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete overrides tx: %w", err)
	}
	if err := deleteOverrideKeysTx(ctx, tx, keys); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete overrides tx: %w", err)
	}
	return nil
}

func deleteOverrideKeysTx(ctx context.Context, tx *sql.Tx, keys []service.ModelPricingOverrideKey) error {
	if len(keys) == 0 {
		return nil
	}
	const delQuery = `DELETE FROM model_pricing_overrides WHERE scope = $1 AND scope_id = $2 AND model_id = $3`
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, delQuery, k.Scope, k.ScopeID, k.ModelID); err != nil {
			return fmt.Errorf("delete model_pricing_overrides (%s/%s/%s): %w", k.Scope, k.ScopeID, k.ModelID, err)
		}
	}
	return nil
}
