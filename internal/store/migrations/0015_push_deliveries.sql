-- Bounded log of Web Push delivery attempts for diagnosing missed
-- notifications. Only the push service host is stored, never the endpoint
-- capability URL.
CREATE TABLE IF NOT EXISTS push_deliveries (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  kind          TEXT NOT NULL,
  endpoint_host TEXT NOT NULL,
  status        INTEGER NOT NULL,
  error         TEXT NOT NULL DEFAULT ''
);
