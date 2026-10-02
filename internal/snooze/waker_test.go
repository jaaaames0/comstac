package snooze

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

type recordingNotifier struct{ ids []int64 }

func (r *recordingNotifier) QueueReminder(_, _ string, id int64) bool {
	r.ids = append(r.ids, id)
	return true
}

func TestWakeOnceNotifiesResurfacedMessages(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "waker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	w := NewWaker(db, time.Second, rec)
	w.nowFn = func() time.Time { return now }
	if err := w.wakeOnce(ctx); err != nil || len(rec.ids) != 0 {
		t.Fatalf("woke early: %v %v", rec.ids, err)
	}
	w.nowFn = func() time.Time { return now.Add(2 * time.Minute) }
	if err := w.wakeOnce(ctx); err != nil || len(rec.ids) != 1 || rec.ids[0] != 1 {
		t.Fatalf("expected one reminder for message 1, got %v %v", rec.ids, err)
	}

	// No notifier configured (push disabled) still resurfaces.
	if err := NewWaker(db, time.Second, nil).wakeOnce(ctx); err != nil {
		t.Fatal(err)
	}
}
