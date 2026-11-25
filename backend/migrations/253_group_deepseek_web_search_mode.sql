ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS deepseek_web_search_mode VARCHAR(20) NOT NULL DEFAULT '';

COMMENT ON COLUMN groups.deepseek_web_search_mode IS
    'Group-level override for DeepSeek official free server-side web search ("" / "default" = follow channel, "enabled" = force on, "disabled" = force off)';
