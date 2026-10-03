-- Dedupe keys each message's extraction found, including ones whose event
-- belongs to another message (an itinerary and its booking confirmation both
-- name the same flight). ai separates AI runs from rule scans, which replace
-- their own refs independently.
CREATE TABLE IF NOT EXISTS message_event_refs (
  message_id INTEGER NOT NULL,
  dedupe_key TEXT    NOT NULL,
  ai         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (message_id, ai, dedupe_key)
);
CREATE INDEX IF NOT EXISTS idx_message_event_refs_key ON message_event_refs(dedupe_key);
