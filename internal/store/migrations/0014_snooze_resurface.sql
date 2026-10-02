-- resurfaced_at records when an expired snooze returned a message to the top
-- of the inbox; list order uses it in place of created_at once set.
ALTER TABLE messages ADD COLUMN resurfaced_at TEXT;
CREATE INDEX IF NOT EXISTS idx_messages_list_at ON messages(COALESCE(resurfaced_at, created_at) DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_snooze_until ON messages(snooze_until) WHERE snooze_until IS NOT NULL;
