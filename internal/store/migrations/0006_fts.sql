ALTER TABLE messages ADD COLUMN body_text TEXT NOT NULL DEFAULT '';

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    subject,
    from_addr,
    body_text,
    content='messages',
    content_rowid='id'
);

-- Seed FTS from existing rows (body_text will be empty for historical messages).
INSERT INTO messages_fts(rowid, subject, from_addr, body_text)
    SELECT id, COALESCE(subject,''), COALESCE(from_addr,''), '' FROM messages;

CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, subject, from_addr, body_text)
    VALUES (new.id, COALESCE(new.subject,''), COALESCE(new.from_addr,''), COALESCE(new.body_text,''));
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, subject, from_addr, body_text)
    VALUES ('delete', old.id, COALESCE(old.subject,''), COALESCE(old.from_addr,''), COALESCE(old.body_text,''));
    INSERT INTO messages_fts(rowid, subject, from_addr, body_text)
    VALUES (new.id, COALESCE(new.subject,''), COALESCE(new.from_addr,''), COALESCE(new.body_text,''));
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, subject, from_addr, body_text)
    VALUES ('delete', old.id, COALESCE(old.subject,''), COALESCE(old.from_addr,''), COALESCE(old.body_text,''));
END;
