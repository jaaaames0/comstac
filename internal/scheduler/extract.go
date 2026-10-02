package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/mail"
	"time"

	"comstac/internal/extract"
	"comstac/internal/store"
)

// extractBatch bounds how many messages one tick scans, so a large backfill
// spreads over several ticks.
const extractBatch = 40

// scanForDates turns dates found in unscanned inbox mail into calendar
// suggestions.
func (s *Scheduler) scanForDates(ctx context.Context, now time.Time) error {
	targets, err := store.PendingExtractions(ctx, s.db, extract.Version, extractBatch)
	if err != nil {
		return err
	}
	created := 0
	for _, t := range targets {
		in := extract.Input{
			Subject:  t.Subject,
			From:     t.FromAddr,
			Date:     messageDate(t.DateHdr, t.CreatedAt),
			Text:     store.ExtractBodyText(t.Raw),
			HTML:     store.ExtractBodyHTML(t.Raw),
			Calendar: store.ExtractCalendarParts(t.Raw),
		}
		var suggestions []store.CalendarEvent
		for _, c := range extract.Extract(in, now) {
			fields, _ := json.Marshal(store.SuggestionDetails{Extractor: c.Extractor, Evidence: c.Evidence, Fields: c.Details})
			suggestions = append(suggestions, store.CalendarEvent{
				Title: c.Title, Kind: c.Kind, Location: c.Location, Notes: c.Notes, AllDay: c.AllDay,
				StartLocal: c.StartLocal, EndLocal: c.EndLocal, TZ: c.TZ, DedupeKey: c.Key, Details: string(fields),
			})
		}
		n, err := store.SaveExtraction(ctx, s.db, t.ID, extract.Version, suggestions)
		if err != nil {
			return err
		}
		created += n
	}
	if len(targets) > 0 {
		slog.Info("scanned mail for dates", "component", "scheduler", "messages", len(targets), "suggestions", created)
	}
	return nil
}

// messageDate prefers the Date header and falls back to the receive time.
func messageDate(dateHdr, createdAt string) time.Time {
	if t, err := mail.ParseDate(dateHdr); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		return t
	}
	return time.Now()
}
