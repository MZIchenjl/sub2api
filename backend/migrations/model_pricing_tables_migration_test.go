package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelPricingTablesMigration(t *testing.T) {
	content, err := FS.ReadFile("254_model_pricing_tables.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS model_pricing_catalog_entries")
	require.Contains(t, sql, "pricing_missing BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "input_per_million NUMERIC(18, 8),")
	require.Contains(t, sql, "output_per_million NUMERIC(18, 8),")
	require.Contains(t, sql, "PRIMARY KEY (channel_id, raw_model_id)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_model_pricing_catalog_entries_model_id")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_model_pricing_catalog_entries_platform")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS model_pricing_overrides")
	require.Contains(t, sql, "CHECK (scope IN ('default', 'channel', 'group'))")
	require.Contains(t, sql, "PRIMARY KEY (scope, scope_id, model_id)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_model_id")
}
