-- 254_model_pricing_tables.sql
-- Store synced models.dev pricing catalog entries and scoped custom overrides / price locks
-- directly in PostgreSQL tables instead of local disk JSON cache + monolithic settings blob.

CREATE TABLE IF NOT EXISTS model_pricing_catalog_entries (
    channel_id VARCHAR(64) NOT NULL,
    raw_model_id VARCHAR(200) NOT NULL,
    model_id VARCHAR(200) NOT NULL,
    platform VARCHAR(64) NOT NULL,
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    family VARCHAR(128) NOT NULL DEFAULT '',
    release_date VARCHAR(32) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT '',
    mode VARCHAR(32) NOT NULL DEFAULT 'chat',
    input_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    output_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    cache_read_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    cache_write_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    cache_write_explicit BOOLEAN NOT NULL DEFAULT FALSE,
    cache_write_1h_per_million NUMERIC(18, 8),
    cache_storage_per_million_per_hour NUMERIC(18, 8) NOT NULL DEFAULT 0,
    long_context_threshold INTEGER NOT NULL DEFAULT 0,
    long_context_input_multiplier NUMERIC(12, 6) NOT NULL DEFAULT 0,
    long_context_output_multiplier NUMERIC(12, 6) NOT NULL DEFAULT 0,
    score INTEGER NOT NULL DEFAULT 0,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel_id, raw_model_id)
);

CREATE INDEX IF NOT EXISTS idx_model_pricing_catalog_entries_model_id
    ON model_pricing_catalog_entries (model_id);

CREATE INDEX IF NOT EXISTS idx_model_pricing_catalog_entries_platform
    ON model_pricing_catalog_entries (platform);

CREATE TABLE IF NOT EXISTS model_pricing_overrides (
    scope VARCHAR(16) NOT NULL CHECK (scope IN ('default', 'channel', 'group')),
    scope_id VARCHAR(64) NOT NULL DEFAULT '',
    model_id VARCHAR(200) NOT NULL,
    platform VARCHAR(64) NOT NULL DEFAULT '',
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    input_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    output_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    cache_read_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    cache_write_per_million NUMERIC(18, 8) NOT NULL DEFAULT 0,
    cache_write_1h_per_million NUMERIC(18, 8),
    cache_storage_per_million_per_hour NUMERIC(18, 8) NOT NULL DEFAULT 0,
    long_context_threshold INTEGER NOT NULL DEFAULT 0,
    long_context_input_multiplier NUMERIC(12, 6) NOT NULL DEFAULT 0,
    long_context_output_multiplier NUMERIC(12, 6) NOT NULL DEFAULT 0,
    is_locked BOOLEAN NOT NULL DEFAULT FALSE,
    is_custom BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (scope, scope_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_model_id
    ON model_pricing_overrides (model_id);
