-- Suggestions share dedupe keys with confirmed events, so the key is only
-- unique among confirmed events.
DROP INDEX IF EXISTS idx_calendar_events_dedupe;
CREATE UNIQUE INDEX IF NOT EXISTS idx_calendar_events_dedupe_confirmed
  ON calendar_events(dedupe_key) WHERE dedupe_key IS NOT NULL AND status = 'confirmed';
CREATE INDEX IF NOT EXISTS idx_calendar_events_key ON calendar_events(dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_calendar_events_message ON calendar_events(message_id) WHERE message_id IS NOT NULL;

-- Which extractor version last scanned each message.
CREATE TABLE IF NOT EXISTS message_extractions (
  message_id   INTEGER PRIMARY KEY,
  version      INTEGER NOT NULL,
  found        INTEGER NOT NULL DEFAULT 0,
  extracted_at TEXT NOT NULL DEFAULT (datetime('now'))
);
