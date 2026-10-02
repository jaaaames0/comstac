package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CalendarEvent is a stored calendar event. Times are local wall-clock values
// in TZ; see migration 0016 for formats.
type CalendarEvent struct {
	ID         int64
	Title      string
	Notes      string
	Location   string
	Kind       string
	Source     string
	Status     string
	AllDay     bool
	StartLocal string
	EndLocal   string
	TZ         string
	RRule      string
	ExDates    string
	MessageID  int64 // 0 when not linked to a message
	Details    string
	DedupeKey  string
	Reminders  []CalendarReminder
	CreatedAt  string
	UpdatedAt  string
}

// CalendarReminder is one reminder offset on an event.
type CalendarReminder struct {
	OffsetMinutes int
	CreatedAt     time.Time
}

// Calendar event statuses.
const (
	CalendarConfirmed = "confirmed"
	CalendarSuggested = "suggested"
	CalendarDismissed = "dismissed"
)

const calendarEventColumns = `id, title, notes, location, kind, source, status, all_day, start_local,
	end_local, tz, rrule, exdates, COALESCE(message_id, 0), details, COALESCE(dedupe_key, ''), created_at, updated_at`

func scanCalendarEvent(row interface{ Scan(...any) error }) (CalendarEvent, error) {
	var e CalendarEvent
	var allDay int
	err := row.Scan(&e.ID, &e.Title, &e.Notes, &e.Location, &e.Kind, &e.Source, &e.Status, &allDay,
		&e.StartLocal, &e.EndLocal, &e.TZ, &e.RRule, &e.ExDates, &e.MessageID, &e.Details, &e.DedupeKey,
		&e.CreatedAt, &e.UpdatedAt)
	e.AllDay = allDay != 0
	return e, err
}

// ListCalendarEvents returns all events with the given status, with reminders.
func ListCalendarEvents(ctx context.Context, db *sql.DB, status string) ([]CalendarEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events WHERE status = ? ORDER BY start_local, id`, status)
	if err != nil {
		return nil, fmt.Errorf("list calendar events: %w", err)
	}
	var events []CalendarEvent
	byID := map[int64]int{}
	for rows.Next() {
		e, err := scanCalendarEvent(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan calendar event: %w", err)
		}
		byID[e.ID] = len(events)
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rem, err := db.QueryContext(ctx, `
		SELECT r.event_id, r.offset_minutes, r.created_at
		FROM calendar_reminders r JOIN calendar_events e ON e.id = r.event_id
		WHERE e.status = ? ORDER BY r.event_id, r.offset_minutes DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("list calendar reminders: %w", err)
	}
	defer rem.Close()
	for rem.Next() {
		var id int64
		var r CalendarReminder
		var created string
		if err := rem.Scan(&id, &r.OffsetMinutes, &created); err != nil {
			return nil, fmt.Errorf("scan calendar reminder: %w", err)
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		if i, ok := byID[id]; ok {
			events[i].Reminders = append(events[i].Reminders, r)
		}
	}
	return events, rem.Err()
}

// GetCalendarEvent returns one event with its reminders, or nil if absent.
func GetCalendarEvent(ctx context.Context, db *sql.DB, id int64) (*CalendarEvent, error) {
	e, err := scanCalendarEvent(db.QueryRowContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get calendar event: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT offset_minutes, created_at FROM calendar_reminders WHERE event_id = ? ORDER BY offset_minutes DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("get calendar reminders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r CalendarReminder
		var created string
		if err := rows.Scan(&r.OffsetMinutes, &created); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		e.Reminders = append(e.Reminders, r)
	}
	return &e, rows.Err()
}

// SaveCalendarEvent inserts (ID == 0) or updates an event and sets its
// reminder offsets. Unchanged offsets keep their creation time; new ones only
// apply to occurrences whose reminder time is still ahead.
func SaveCalendarEvent(ctx context.Context, db *sql.DB, e *CalendarEvent, offsets []int) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := saveCalendarEventTx(ctx, tx, e, offsets)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func saveCalendarEventTx(ctx context.Context, tx *sql.Tx, e *CalendarEvent, offsets []int) (int64, error) {
	if e.Kind == "" {
		e.Kind = "custom"
	}
	if e.Source == "" {
		e.Source = "manual"
	}
	if e.Status == "" {
		e.Status = CalendarConfirmed
	}
	if e.TZ == "" {
		e.TZ = "Australia/Sydney"
	}
	if e.Details == "" {
		e.Details = "{}"
	}
	allDay := 0
	if e.AllDay {
		allDay = 1
	}
	var msgID, dedupe any
	if e.MessageID > 0 {
		msgID = e.MessageID
	}
	if e.DedupeKey != "" {
		dedupe = e.DedupeKey
	}
	id := e.ID
	if id == 0 {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO calendar_events (title, notes, location, kind, source, status, all_day, start_local,
				end_local, tz, rrule, exdates, message_id, details, dedupe_key)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Title, e.Notes, e.Location, e.Kind, e.Source, e.Status, allDay, e.StartLocal,
			e.EndLocal, e.TZ, e.RRule, e.ExDates, msgID, e.Details, dedupe)
		if err != nil {
			return 0, fmt.Errorf("insert calendar event: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `
			UPDATE calendar_events SET title = ?, notes = ?, location = ?, kind = ?, source = ?, status = ?,
				all_day = ?, start_local = ?, end_local = ?, tz = ?, rrule = ?, exdates = ?, message_id = ?,
				details = ?, dedupe_key = ?, updated_at = datetime('now')
			WHERE id = ?`,
			e.Title, e.Notes, e.Location, e.Kind, e.Source, e.Status, allDay, e.StartLocal,
			e.EndLocal, e.TZ, e.RRule, e.ExDates, msgID, e.Details, dedupe, id)
		if err != nil {
			return 0, fmt.Errorf("update calendar event: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, fmt.Errorf("calendar event %d not found", id)
		}
	}

	keep := map[int]bool{}
	for _, o := range offsets {
		keep[o] = true
	}
	list := make([]any, 0, len(keep)+1)
	list = append(list, id)
	marks := make([]string, 0, len(keep))
	for o := range keep {
		list = append(list, o)
		marks = append(marks, "?")
	}
	q := `DELETE FROM calendar_reminders WHERE event_id = ?`
	if len(marks) > 0 {
		q += ` AND offset_minutes NOT IN (` + strings.Join(marks, ",") + `)`
	}
	if _, err := tx.ExecContext(ctx, q, list...); err != nil {
		return 0, fmt.Errorf("prune calendar reminders: %w", err)
	}
	sorted := make([]int, 0, len(keep))
	for o := range keep {
		sorted = append(sorted, o)
	}
	sort.Ints(sorted)
	for _, o := range sorted {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO calendar_reminders (event_id, offset_minutes) VALUES (?, ?)`, id, o); err != nil {
			return 0, fmt.Errorf("insert calendar reminder: %w", err)
		}
	}
	e.ID = id
	return id, nil
}

// DeleteCalendarEvent removes an event with its reminders and fire records.
func DeleteCalendarEvent(ctx context.Context, db *sql.DB, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteCalendarEventTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteCalendarEventTx(ctx context.Context, tx *sql.Tx, id int64) error {
	for _, q := range []string{
		`DELETE FROM calendar_reminder_fires WHERE event_id = ?`,
		`DELETE FROM calendar_reminders WHERE event_id = ?`,
		`DELETE FROM calendar_events WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("delete calendar event: %w", err)
		}
	}
	return nil
}

// AddCalendarExDate skips one local date of a recurring event.
func AddCalendarExDate(ctx context.Context, db *sql.DB, id int64, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid date %q", date)
	}
	_, err := db.ExecContext(ctx, `
		UPDATE calendar_events
		SET exdates = CASE WHEN exdates = '' THEN ? ELSE exdates || ',' || ? END, updated_at = datetime('now')
		WHERE id = ? AND (',' || exdates || ',') NOT LIKE ('%,' || ? || ',%')`, date, date, id, date)
	return err
}

// ReplaceRosterShifts deletes roster events starting within [fromDate,
// toDate] (inclusive local dates) and inserts the given events, atomically.
func ReplaceRosterShifts(ctx context.Context, db *sql.DB, fromDate, toDate string, events []CalendarEvent, offsets []int) (removed int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM calendar_events
		WHERE source = 'roster' AND substr(start_local, 1, 10) BETWEEN ? AND ?`, fromDate, toDate)
	if err != nil {
		return 0, fmt.Errorf("find roster shifts: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := deleteCalendarEventTx(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	for i := range events {
		events[i].Source = "roster"
		if _, err := saveCalendarEventTx(ctx, tx, &events[i], offsets); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}

// ClaimCalendarReminder records that a reminder for one occurrence is being
// sent. It returns false if it was already claimed.
func ClaimCalendarReminder(ctx context.Context, db *sql.DB, eventID int64, occurrenceStart time.Time, offset int) (bool, error) {
	res, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO calendar_reminder_fires (event_id, occurrence_start, offset_minutes)
		VALUES (?, ?, ?)`, eventID, occurrenceStart.UTC().Format(time.RFC3339), offset)
	if err != nil {
		return false, fmt.Errorf("claim calendar reminder: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountRosterShifts counts roster events starting within [fromDate, toDate].
func CountRosterShifts(ctx context.Context, db *sql.DB, fromDate, toDate string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM calendar_events
		WHERE source = 'roster' AND substr(start_local, 1, 10) BETWEEN ? AND ?`, fromDate, toDate).Scan(&n)
	return n, err
}
