ALTER TABLE messages ADD COLUMN sent_at TEXT;
CREATE INDEX IF NOT EXISTS idx_messages_sent_at ON messages(COALESCE(sent_at, created_at) DESC);
