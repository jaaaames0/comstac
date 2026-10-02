-- One row per AI date-extraction call, for usage, cost and the daily cap.
CREATE TABLE IF NOT EXISTS ai_extraction_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id    INTEGER NOT NULL,
  model         TEXT NOT NULL,
  trigger       TEXT NOT NULL,              -- manual | sender
  status        TEXT NOT NULL,              -- ok | error | refused | truncated
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_microusd INTEGER NOT NULL DEFAULT 0,
  proposed      INTEGER NOT NULL DEFAULT 0, -- events the model returned
  kept          INTEGER NOT NULL DEFAULT 0, -- events that passed checks
  created       INTEGER NOT NULL DEFAULT 0, -- new suggestions stored
  error         TEXT NOT NULL DEFAULT '',
  request_id    TEXT NOT NULL DEFAULT '',   -- provider request id for billing lookups
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_ai_runs_created ON ai_extraction_runs(created_at);
CREATE INDEX IF NOT EXISTS idx_ai_runs_message ON ai_extraction_runs(message_id);

-- Sender domains whose new inbox mail is sent for AI extraction automatically.
CREATE TABLE IF NOT EXISTS ai_sender_domains (
  domain     TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
