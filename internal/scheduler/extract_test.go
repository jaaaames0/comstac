package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"comstac/internal/extract"
	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestScanForDatesCreatesSuggestionsOnce(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	raw := strings.Join([]string{
		"Subject: Your bank connections will be removed on 3 Oct 2026",
		"From: Service <notifications@service.example>",
		"To: me@example.com",
		"Date: Wed, 30 Sep 2026 05:02:52 +0000",
		"",
		"Your trial has ended. Your bank connections are scheduled to be removed on 3 Oct 2026.",
		"",
	}, "\r\n")
	if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{
		Source: ingest.SourceSMTP, EnvelopeFrom: "notifications@service.example",
		EnvelopeTo: []string{"me@example.com"}, RawMIME: []byte(raw),
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, time.Second, nil, sydney)
	now := time.Date(2026, 9, 30, 16, 0, 0, 0, sydney)
	if err := s.scanForDates(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.scanForDates(ctx, now); err != nil {
		t.Fatal(err)
	}
	evs, err := store.ListMessageCalendarEvents(ctx, db, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("suggestions = %+v (%v)", evs, err)
	}
	if evs[0].Status != store.CalendarSuggested || evs[0].Kind != "deadline" || !evs[0].AllDay || evs[0].StartLocal != "2026-10-03" {
		t.Fatalf("suggestion = %+v", evs[0])
	}
	done, pending, err := store.ExtractionProgress(ctx, db, extract.Version)
	if err != nil || done != 1 || pending != 0 {
		t.Fatalf("progress done=%d pending=%d %v", done, pending, err)
	}
}
