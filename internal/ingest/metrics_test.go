package ingest_test

import (
	"context"
	"path/filepath"
	"testing"

	"comstac/internal/ingest"
	"comstac/internal/securitymetrics"
	"comstac/internal/store"
)

type rejectingNotifier struct{}

func (rejectingNotifier) QueueNewMail(string, string, int64) bool { return false }

func TestNotificationQueueDropIncrementsSanitizedCounter(t *testing.T) {
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var counters securitymetrics.Counters
	service := ingest.NewService(db)
	service.SetNotifier(rejectingNotifier{})
	service.SetSecurityCounters(&counters)
	if err := service.IngestRaw(context.Background(), ingest.IngestInput{
		Source: ingest.SourceSMTP,
		RawMIME: []byte("From: Sender <sender@example.test>\r\n" +
			"To: recipient@example.test\r\nSubject: private subject\r\n\r\nprivate body"),
	}); err != nil {
		t.Fatal(err)
	}

	got := counters.Snapshot()
	if got.NotificationDrops != 1 {
		t.Fatalf("notification drops = %d, want 1", got.NotificationDrops)
	}
}
