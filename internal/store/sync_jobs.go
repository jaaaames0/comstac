package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func EnqueueIMAPSyncJob(ctx context.Context, db *sql.DB, messageID int64, action string, payload any) (bool, error) {
	return enqueueIMAPSyncJob(ctx, db, messageID, action, payload)
}

type syncJobDB interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueIMAPSyncJob(ctx context.Context, db syncJobDB, messageID int64, action string, payload any) (bool, error) {
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

func messageSource(ctx context.Context, db syncJobDB, messageID int64) (string, error) {
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

// PruneDoneSyncJobs deletes completed jobs last updated before cutoff and
// returns how many were removed. Failed jobs are kept as evidence.
func PruneDoneSyncJobs(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM sync_jobs WHERE status = 'done' AND updated_at < ?`,
		cutoff.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, fmt.Errorf("prune sync jobs: %w", err)
	}
	return res.RowsAffected()
}
