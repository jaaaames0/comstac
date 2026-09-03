CREATE TABLE IF NOT EXISTS remote_image_senders (
  email_address TEXT PRIMARY KEY,
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  CHECK (length(email_address) BETWEEN 3 AND 320),
  CHECK (instr(email_address, '@') > 1)
);
