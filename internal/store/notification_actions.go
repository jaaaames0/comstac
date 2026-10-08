package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ApplyNotificationMessageAction changes local state and queues IMAP sync in
// one transaction. Repeated actions do not reset trash retention or snooze time.
func ApplyNotificationMessageAction(ctx context.Context, db *sql.DB, id int64, action string, now time.Time) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	source, err := messageSource(ctx, tx, id)
	if err != nil || source == "" {
		return false, err
	}
	var query, syncAction string
	var payload any
	args := []any{id}
	switch action {
	case "trash":
		query = `UPDATE messages SET archived = 1, archived_at = datetime('now') WHERE id = ? AND archived = 0`
		syncAction, payload = "archive", map[string]any{"archived": true}
	case "read":
		query = `UPDATE messages SET read = 1 WHERE id = ? AND read = 0`
		syncAction, payload = "read", map[string]any{"read": true}
	case "snooze_mail":
		until := now.Add(time.Hour).UTC().Format(time.RFC3339)
		query = `UPDATE messages SET snooze_until = ? WHERE id = ? AND archived = 0 AND spam = 0 AND snooze_until IS NULL`
		args = []any{until, id}
		syncAction, payload = "snooze", map[string]any{"until": until}
	default:
		return false, fmt.Errorf("invalid notification action")
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed > 0 {
		if _, err := enqueueIMAPSyncJob(ctx, tx, id, syncAction, payload); err != nil {
			return false, err
		}
	} else if action == "snooze_mail" {
		var inactive bool
		if err := tx.QueryRowContext(ctx, `SELECT archived = 1 OR spam = 1 FROM messages WHERE id = ?`, id).Scan(&inactive); err != nil {
			return false, err
		}
		if inactive {
			return false, nil
		}
	}
	return true, tx.Commit()
}
