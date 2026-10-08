package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CalendarNotificationSnooze struct {
	ID, EventID int64
	Occurrence  time.Time
}

// SnoozeCalendarNotification creates a ten-minute follow-up for an actual
// delivered reminder. Retrying the same action neither duplicates nor delays it.
func SnoozeCalendarNotification(ctx context.Context, db *sql.DB, eventID int64, occurrence time.Time, sourceKey string, now time.Time) (bool, error) {
	kind, value, ok := strings.Cut(sourceKey, ":")
	number, err := strconv.ParseInt(value, 10, 64)
	if !ok || err != nil || (kind != "offset" && kind != "snooze") {
		return false, nil
	}
	start := occurrence.UTC().Format(time.RFC3339)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var confirmed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM calendar_events WHERE id = ? AND status = ?)`, eventID, CalendarConfirmed).Scan(&confirmed); err != nil {
		return false, err
	}
	if !confirmed {
		return false, nil
	}
	query := `SELECT EXISTS(SELECT 1 FROM calendar_reminder_fires WHERE event_id = ? AND occurrence_start = ? AND offset_minutes = ?)`
	if kind == "snooze" {
		query = `SELECT EXISTS(SELECT 1 FROM calendar_notification_snoozes WHERE event_id = ? AND occurrence_start = ? AND id = ? AND fired_at IS NOT NULL)`
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, query, eventID, start, number).Scan(&valid); err != nil {
		return false, err
	}
	if !valid {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO calendar_notification_snoozes (event_id, occurrence_start, source_key, due_at_unix)
		VALUES (?, ?, ?, ?)`, eventID, start, kind+":"+strconv.FormatInt(number, 10), now.Add(10*time.Minute).Unix()); err != nil {
		return false, fmt.Errorf("snooze calendar notification: %w", err)
	}
	return true, tx.Commit()
}

// DueCalendarNotificationSnoozes discards stale follow-ups using the same grace
// window as ordinary reminders, and returns pending work in due-time order.
func DueCalendarNotificationSnoozes(ctx context.Context, db *sql.DB, now time.Time, grace time.Duration) ([]CalendarNotificationSnooze, error) {
	if _, err := db.ExecContext(ctx, `DELETE FROM calendar_notification_snoozes WHERE due_at_unix <= ?`, now.Add(-grace).Unix()); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, event_id, occurrence_start FROM calendar_notification_snoozes
		WHERE fired_at IS NULL AND due_at_unix <= ? ORDER BY due_at_unix, id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarNotificationSnooze
	for rows.Next() {
		var s CalendarNotificationSnooze
		var start string
		if err := rows.Scan(&s.ID, &s.EventID, &start); err != nil {
			return nil, err
		}
		if s.Occurrence, err = time.Parse(time.RFC3339, start); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func ClaimCalendarNotificationSnooze(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE calendar_notification_snoozes SET fired_at = datetime('now') WHERE id = ? AND fired_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReleaseCalendarNotificationSnooze lets a later tick retry a full push queue.
func ReleaseCalendarNotificationSnooze(ctx context.Context, db *sql.DB, id int64) error {
	_, err := db.ExecContext(ctx, `UPDATE calendar_notification_snoozes SET fired_at = NULL WHERE id = ?`, id)
	return err
}
