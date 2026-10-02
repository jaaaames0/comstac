-- Calendar events. Times are local wall-clock values in tz: all-day events
-- store dates (end inclusive), timed events store YYYY-MM-DDTHH:MM. Recurring
-- events carry an RRULE subset and comma-separated skipped local dates.
CREATE TABLE IF NOT EXISTS calendar_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  title       TEXT NOT NULL,
  notes       TEXT NOT NULL DEFAULT '',
  location    TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'custom',
  source      TEXT NOT NULL DEFAULT 'manual',
  status      TEXT NOT NULL DEFAULT 'confirmed',
  all_day     INTEGER NOT NULL DEFAULT 0,
  start_local TEXT NOT NULL,
  end_local   TEXT NOT NULL DEFAULT '',
  tz          TEXT NOT NULL DEFAULT 'Australia/Sydney',
  rrule       TEXT NOT NULL DEFAULT '',
  exdates     TEXT NOT NULL DEFAULT '',
  message_id  INTEGER,
  details     TEXT NOT NULL DEFAULT '{}',
  dedupe_key  TEXT,
  created_at  TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_calendar_events_dedupe ON calendar_events(dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_calendar_events_status_start ON calendar_events(status, start_local);

-- Reminders fire offset_minutes before each occurrence start (negative means
-- after; all-day events start at local midnight). created_at stops a newly
-- added reminder from firing retroactively for an occurrence already past it.
CREATE TABLE IF NOT EXISTS calendar_reminders (
  event_id       INTEGER NOT NULL,
  offset_minutes INTEGER NOT NULL,
  created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  PRIMARY KEY (event_id, offset_minutes)
);

-- One row per sent reminder so restarts and overlapping scheduler ticks never
-- send the same reminder twice.
CREATE TABLE IF NOT EXISTS calendar_reminder_fires (
  event_id         INTEGER NOT NULL,
  occurrence_start TEXT NOT NULL,
  offset_minutes   INTEGER NOT NULL,
  fired_at         TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (event_id, occurrence_start, offset_minutes)
);
