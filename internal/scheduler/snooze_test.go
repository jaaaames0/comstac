package scheduler

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

type recordingNotifier struct {
	mailIDs []int64
	events  []string // "title|body|url|tag"
}

func (r *recordingNotifier) QueueReminder(_, _ string, id int64) bool {
	r.mailIDs = append(r.mailIDs, id)
	return true
}

func (r *recordingNotifier) QueueEventReminder(title, body, url, tag string) bool {
	r.events = append(r.events, strings.Join([]string{title, body, url, tag}, "|"))
	return true
}

var sydney = func() *time.Location {
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		panic(err)
	}
	return loc
}()

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestWakeSnoozesNotifiesResurfacedMessages(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	raw := strings.Join([]string{"Subject: hi", "From: a@example.com", "To: b@example.com", "", "body", ""}, "\r\n")
	if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{
		Source: ingest.SourceSMTP, EnvelopeFrom: "a@example.com",
		EnvelopeTo: []string{"b@example.com"}, RawMIME: []byte(raw),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	until := now.Add(time.Minute)
	if _, err := store.SetMessageSnoozeUntil(ctx, db, 1, &until); err != nil {
		t.Fatal(err)
	}

	rec := &recordingNotifier{}
	s := New(db, time.Second, rec, sydney)
	if err := s.wakeSnoozes(ctx, now); err != nil || len(rec.mailIDs) != 0 {
		t.Fatalf("woke early: %v %v", rec.mailIDs, err)
	}
	if err := s.wakeSnoozes(ctx, now.Add(2*time.Minute)); err != nil || len(rec.mailIDs) != 1 || rec.mailIDs[0] != 1 {
		t.Fatalf("expected one reminder for message 1, got %v %v", rec.mailIDs, err)
	}

	// No notifier configured (push disabled) still resurfaces.
	if err := New(db, time.Second, nil, sydney).wakeSnoozes(ctx, now); err != nil {
		t.Fatal(err)
	}
}
