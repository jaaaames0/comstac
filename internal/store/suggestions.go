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
	if created, err = replaceSuggestionsTx(ctx, tx, messageID, false, suggestions); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_extractions (message_id, version, found) VALUES (?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET version = excluded.version, found = excluded.found, extracted_at = datetime('now')`,
		messageID, version, created); err != nil {
		return 0, fmt.Errorf("record extraction: %w", err)
	}
	return created, tx.Commit()
}

// SaveAISuggestions replaces a message's pending AI suggestions, applying the
// same per-key rules as SaveExtraction. Rule-based suggestions are kept.
func SaveAISuggestions(ctx context.Context, db *sql.DB, messageID int64, suggestions []CalendarEvent) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	created, err := replaceSuggestionsTx(ctx, tx, messageID, true, suggestions)
	if err != nil {
		return 0, err
	}
	return created, tx.Commit()
}

// aiExtractorPrefix marks suggestions made by a language model in
// details.extractor ("ai:claude-haiku-4-5").
const aiExtractorPrefix = "ai:"

// replaceSuggestionsTx clears the message's pending suggestions from one
// source (AI or rules) and stores the new ones.
func replaceSuggestionsTx(ctx context.Context, tx *sql.Tx, messageID int64, ai bool, suggestions []CalendarEvent) (created int, err error) {
	op := "NOT LIKE"
	if ai {
		op = "LIKE"
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_events WHERE message_id = ? AND status = ?
		AND COALESCE(json_extract(details, '$.extractor'), '') `+op+` ?`, messageID, CalendarSuggested, aiExtractorPrefix+"%"); err != nil {
		return 0, fmt.Errorf("clear suggestions: %w", err)
	}
	aiFlag := 0
	if ai {
		aiFlag = 1
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_event_refs WHERE message_id = ? AND ai = ?`, messageID, aiFlag); err != nil {
		return 0, fmt.Errorf("clear refs: %w", err)
	}
	for _, s := range suggestions {
		if s.DedupeKey != "" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_event_refs (message_id, dedupe_key, ai) VALUES (?, ?, ?)`,
				messageID, s.DedupeKey, aiFlag); err != nil {
				return 0, fmt.Errorf("record ref: %w", err)
			}
		}
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
	return created, nil
}

// ListMessageCalendarEvents returns a message's pending suggestions and the
// events already added from it, plus events other messages own under a key
// this message's extraction also found: their suggestions, confirmed events
// and dismissed suggestions (offered again, since this message names them
// too). A dismissed event is left out when another event shares its key, and
// a confirmed event when an update suggestion for it is listed.
func ListMessageCalendarEvents(ctx context.Context, db *sql.DB, messageID int64) ([]CalendarEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events
		WHERE (message_id = ? AND status IN (?, ?))
		   OR (COALESCE(message_id, 0) <> ? AND status IN (?, ?, ?)
		       AND dedupe_key IN (SELECT dedupe_key FROM message_event_refs WHERE message_id = ?))
		ORDER BY start_local, id`,
		messageID, CalendarSuggested, CalendarConfirmed,
		messageID, CalendarSuggested, CalendarConfirmed, CalendarDismissed, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []CalendarEvent
	live := map[string]bool{}
	updated := map[int64]bool{} // confirmed events a listed suggestion would change
	for rows.Next() {
		e, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		if e.Status != CalendarDismissed && e.DedupeKey != "" {
			live[e.DedupeKey] = true
		}
		if e.Status == CalendarSuggested {
			if u := e.SuggestionDetails().Updates; u != 0 {
				updated[u] = true
			}
		}
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []CalendarEvent
	for _, e := range all {
		if (e.Status == CalendarDismissed && live[e.DedupeKey]) || updated[e.ID] {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// MessageNamesEvent reports whether an event belongs to the message or
// shares a key its extraction found, so the message's strip may act on it.
func MessageNamesEvent(ctx context.Context, db *sql.DB, messageID int64, e *CalendarEvent) bool {
	if e.MessageID == messageID {
		return true
	}
	if e.DedupeKey == "" {
		return false
	}
	var n int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM message_event_refs WHERE message_id = ? AND dedupe_key = ?`, messageID, e.DedupeKey).Scan(&n)
	return n > 0
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
	// A dismissed suggestion can still be added from a message that names it.
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.Status != CalendarSuggested && s.Status != CalendarDismissed) {
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

// GetExtractionTarget loads one message for extraction with its inbox state.
// It returns nil when the message does not exist.
func GetExtractionTarget(ctx context.Context, db *sql.DB, id int64) (t *ExtractionTarget, archived, spam bool, err error) {
	var x ExtractionTarget
	var a, s int
	err = db.QueryRowContext(ctx, `
		SELECT m.id, COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), COALESCE(m.date_hdr, ''), m.created_at, mr.mime, m.archived, m.spam
		FROM messages m JOIN messages_raw mr ON mr.id = m.raw_id WHERE m.id = ?`, id).
		Scan(&x.ID, &x.Subject, &x.FromAddr, &x.DateHdr, &x.CreatedAt, &x.Raw, &a, &s)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	return &x, a != 0, s != 0, nil
}
