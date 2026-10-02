package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ExtractionTarget is a message waiting to be scanned for dates.
type ExtractionTarget struct {
	ID        int64
	Subject   string
	FromAddr  string
	DateHdr   string
	CreatedAt string
	Raw       []byte
}

// PendingExtractions returns up to limit inbox messages (not trashed, not
// spam) that have not been scanned by the given extractor version, oldest
// first.
func PendingExtractions(ctx context.Context, db *sql.DB, version, limit int) ([]ExtractionTarget, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), COALESCE(m.date_hdr, ''), m.created_at, mr.mime
		FROM messages m
		JOIN messages_raw mr ON mr.id = m.raw_id
		LEFT JOIN message_extractions x ON x.message_id = m.id
		WHERE m.archived = 0 AND m.spam = 0 AND (x.message_id IS NULL OR x.version < ?)
		ORDER BY m.id
		LIMIT ?`, version, limit)
	if err != nil {
		return nil, fmt.Errorf("pending extractions: %w", err)
	}
	defer rows.Close()
	var out []ExtractionTarget
	for rows.Next() {
		var t ExtractionTarget
		if err := rows.Scan(&t.ID, &t.Subject, &t.FromAddr, &t.DateHdr, &t.CreatedAt, &t.Raw); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SuggestionDetails is the JSON stored in a suggestion's details column.
type SuggestionDetails struct {
	Extractor string            `json:"extractor,omitempty"`
	Evidence  string            `json:"evidence,omitempty"`
	Updates   int64             `json:"updates,omitempty"` // confirmed event this suggestion would change
	Fields    map[string]string `json:"fields,omitempty"`
}

// SuggestionDetails decodes a stored event's details JSON.
func (e CalendarEvent) SuggestionDetails() SuggestionDetails {
	var d SuggestionDetails
	_ = json.Unmarshal([]byte(e.Details), &d)
	return d
}

func sameTimes(a, b CalendarEvent) bool {
	return a.AllDay == b.AllDay && a.StartLocal == b.StartLocal && a.EndLocal == b.EndLocal && a.TZ == b.TZ
}

// SaveExtraction replaces a message's pending suggestions with new ones and
// records the scan. Per dedupe key: an identical confirmed, suggested or
// dismissed event suppresses the suggestion; a confirmed event with other
// times turns it into an update of that event; an older message's differing
// suggestion is superseded; a newer message's suggestion wins.
func SaveExtraction(ctx context.Context, db *sql.DB, messageID int64, version int, suggestions []CalendarEvent) (created int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_events WHERE message_id = ? AND status = ?`, messageID, CalendarSuggested); err != nil {
		return 0, fmt.Errorf("clear suggestions: %w", err)
	}
	for _, s := range suggestions {
		if s.TZ == "" {
			s.TZ = "Australia/Sydney" // the stored default, so comparisons match
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events WHERE dedupe_key = ?`, s.DedupeKey)
		if err != nil {
			return 0, err
		}
		var existing []CalendarEvent
		for rows.Next() {
			e, err := scanCalendarEvent(rows)
			if err != nil {
				rows.Close()
				return 0, err
			}
			existing = append(existing, e)
		}
		rows.Close()

		skip := false
		var updates int64
		var supersede []int64
		for _, e := range existing {
			switch e.Status {
			case CalendarConfirmed:
				if sameTimes(e, s) {
					skip = true
				} else {
					updates = e.ID
				}
			case CalendarDismissed:
				if sameTimes(e, s) {
					skip = true
				}
			case CalendarSuggested:
				switch {
				case sameTimes(e, s):
					skip = true
				case e.MessageID < messageID:
					supersede = append(supersede, e.ID)
				default:
					skip = true // a newer message already suggests other times
				}
			}
		}
		if skip {
			continue
		}
		for _, id := range supersede {
			if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_events WHERE id = ?`, id); err != nil {
				return 0, err
			}
		}
		d := s.SuggestionDetails()
		d.Updates = updates
		raw, _ := json.Marshal(d)
		s.ID, s.Status, s.Source, s.MessageID, s.Details = 0, CalendarSuggested, "email", messageID, string(raw)
		if _, err := saveCalendarEventTx(ctx, tx, &s, nil); err != nil {
			return 0, err
		}
		created++
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_extractions (message_id, version, found) VALUES (?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET version = excluded.version, found = excluded.found, extracted_at = datetime('now')`,
		messageID, version, created); err != nil {
		return 0, fmt.Errorf("record extraction: %w", err)
	}
	return created, tx.Commit()
}

// ListMessageCalendarEvents returns a message's pending suggestions and the
// events already added from it.
func ListMessageCalendarEvents(ctx context.Context, db *sql.DB, messageID int64) ([]CalendarEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events
		WHERE message_id = ? AND status IN (?, ?) ORDER BY start_local, id`, messageID, CalendarSuggested, CalendarConfirmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarEvent
	for rows.Next() {
		e, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrNotSuggestion is returned when acting on an event that is not a pending
// suggestion.
var ErrNotSuggestion = errors.New("not a pending suggestion")

// AcceptSuggestion confirms a suggestion with the given reminder offsets and
// returns the confirmed event's id. An update suggestion changes the times of
// the event it updates (keeping the user's title, notes and reminders) and is
// removed.
func AcceptSuggestion(ctx context.Context, db *sql.DB, id int64, offsets []int) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	s, err := scanCalendarEvent(tx.QueryRowContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.Status != CalendarSuggested) {
		return 0, ErrNotSuggestion
	}
	if err != nil {
		return 0, err
	}
	target := s.SuggestionDetails().Updates
	if target == 0 && s.DedupeKey != "" {
		// A confirmed event may have appeared since the scan.
		var existing int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM calendar_events WHERE dedupe_key = ? AND status = ?`, s.DedupeKey, CalendarConfirmed).Scan(&existing)
		if err == nil {
			target = existing
		}
	}
	if target != 0 {
		allDay := 0
		if s.AllDay {
			allDay = 1
		}
		res, err := tx.ExecContext(ctx, `UPDATE calendar_events SET all_day = ?, start_local = ?, end_local = ?, tz = ?,
			updated_at = datetime('now') WHERE id = ? AND status = ?`, allDay, s.StartLocal, s.EndLocal, s.TZ, target, CalendarConfirmed)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_events WHERE id = ?`, id); err != nil {
				return 0, err
			}
			return target, tx.Commit()
		}
	}
	d := s.SuggestionDetails()
	d.Updates = 0
	raw, _ := json.Marshal(d)
	s.Status, s.Details = CalendarConfirmed, string(raw)
	if _, err := saveCalendarEventTx(ctx, tx, &s, offsets); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DismissSuggestion hides a suggestion; identical future suggestions stay
// hidden.
func DismissSuggestion(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, `UPDATE calendar_events SET status = ?, updated_at = datetime('now') WHERE id = ? AND status = ?`,
		CalendarDismissed, id, CalendarSuggested)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotSuggestion
	}
	return nil
}

// ExtractionProgress reports how many inbox messages are scanned by the
// given version and how many remain.
func ExtractionProgress(ctx context.Context, db *sql.DB, version int) (done, pending int, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN x.version >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN x.message_id IS NULL OR x.version < ? THEN 1 ELSE 0 END), 0)
		FROM messages m LEFT JOIN message_extractions x ON x.message_id = m.id
		WHERE m.archived = 0 AND m.spam = 0`, version, version).Scan(&done, &pending)
	return done, pending, err
}
