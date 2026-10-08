-- One ten-minute follow-up per delivered calendar notification. Persisted
-- snoozes survive restarts; source_key makes retries of an action idempotent.
CREATE TABLE calendar_notification_snoozes (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id         INTEGER NOT NULL,
  occurrence_start TEXT NOT NULL,
  source_key       TEXT NOT NULL,
  due_at_unix      INTEGER NOT NULL,
  fired_at         TEXT,
  UNIQUE (event_id, occurrence_start, source_key)
);
CREATE INDEX idx_notification_snoozes_due ON calendar_notification_snoozes(due_at_unix) WHERE fired_at IS NULL;
