package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func EnqueueIMAPSyncJob(ctx context.Context, db *sql.DB, messageID int64, action string, payload any) (bool, error) {
	source, err := messageSource(ctx, db, messageID)
	if err != nil {
		return false, err
	}
	if source != "imap" {
		return false, nil
	}

	blob, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal sync payload: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO sync_jobs(message_id, action, payload_json, status)
		VALUES (?, ?, ?, 'pending')
	`, messageID, action, string(blob)); err != nil {
		return false, fmt.Errorf("insert sync job: %w", err)
	}
	return true, nil
}

func messageSource(ctx context.Context, db *sql.DB, messageID int64) (string, error) {
	var source string
	err := db.QueryRowContext(ctx, `
		SELECT mr.source
		FROM messages m
		JOIN messages_raw mr ON mr.id = m.raw_id
		WHERE m.id = ?
	`, messageID).Scan(&source)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("query message source: %w", err)
	}
	return source, nil
}
