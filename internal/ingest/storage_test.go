package ingest_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"comstac/internal/ingest"
	"comstac/internal/storageguard"
	"comstac/internal/store"
)

func TestIngestRejectsBeforePersistenceAtStorageLimit(t *testing.T) {
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	guard, err := storageguard.New(t.TempDir(), storageguard.Limits{
		MaxStateBytes: 1, MinFreeBytes: 1, WarnFreeBytes: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := ingest.NewService(db)
	service.SetStorageGuard(guard)
	err = service.IngestRaw(context.Background(), ingest.IngestInput{
		Source: ingest.SourceSMTP, RawMIME: []byte("Subject: test\r\n\r\nbody"),
	})
	if !errors.Is(err, storageguard.ErrUnavailable) {
		t.Fatalf("IngestRaw() error = %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages_raw`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("messages_raw count = %d, want 0", count)
	}
}
