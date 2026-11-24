package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestPruneDoneSyncJobs(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{Source: ingest.SourceSMTP, RawMIME: []byte("Subject: x\r\n\r\nbody")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO sync_jobs (message_id, action, status, updated_at) VALUES
			(1, 'read', 'done',    '2026-01-01 00:00:00'),
			(1, 'read', 'done',    '2026-11-20 00:00:00'),
			(1, 'read', 'failed',  '2026-01-01 00:00:00'),
			(1, 'read', 'pending', '2026-01-01 00:00:00');`); err != nil {
		t.Fatal(err)
	}
	n, err := store.PruneDoneSyncJobs(ctx, db, time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	var left int
	db.QueryRow(`SELECT count(*) FROM sync_jobs`).Scan(&left)
	if left != 3 {
		t.Fatalf("%d jobs left, want recent done + failed + pending", left)
	}
}
