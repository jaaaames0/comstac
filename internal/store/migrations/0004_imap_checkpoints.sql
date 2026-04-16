CREATE TABLE IF NOT EXISTS imap_checkpoints (
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    mailbox    TEXT    NOT NULL,
    last_uid   INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (account_id, mailbox)
);
