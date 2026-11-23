package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupDeepSeekWebSearchModeMigration(t *testing.T) {
	content, err := FS.ReadFile("253_group_deepseek_web_search_mode.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"ADD COLUMN IF NOT EXISTS deepseek_web_search_mode VARCHAR(20) NOT NULL DEFAULT ''")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.deepseek_web_search_mode")
}
