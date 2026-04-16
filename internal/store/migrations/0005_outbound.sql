CREATE TABLE IF NOT EXISTS outbound_messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    from_addr   TEXT NOT NULL,
    to_addr     TEXT NOT NULL,
    subject     TEXT NOT NULL DEFAULT '',
    body_text   TEXT NOT NULL DEFAULT '',
    message_id  TEXT NOT NULL DEFAULT '',
    in_reply_to INTEGER REFERENCES messages(id),
    raw_mime    BLOB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'sent' CHECK (status IN ('sent', 'failed')),
    error_msg   TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
