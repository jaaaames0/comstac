package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"comstac/internal/mailcontent"
	"comstac/internal/store"
)

func TestPendingExtractionsDoesNotBufferBodies(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "bounds.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i := 1; i <= 4; i++ {
		if _, err := db.Exec(`INSERT INTO messages_raw(id,source,mime,mime_sha256) VALUES (?,'imap',zeroblob(1048576),?)`, i, i); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages(raw_id,subject) VALUES (?,'fixture')`, i); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := store.PendingExtractions(ctx, db, 2, 40)
	if err != nil || len(targets) != 4 {
		t.Fatalf("targets = %d, %v", len(targets), err)
	}
	for _, target := range targets {
		if len(target.Raw) != 0 {
			t.Fatal("candidate selection retained a MIME body")
		}
	}
}

func TestOversizedExtractionDoesNotStarveSupportedMail(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "oversized.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i, size := range []int64{mailcontent.MaxMessageBytes + 1, 32} {
		if _, err := db.Exec(`INSERT INTO messages_raw(id,source,mime,mime_sha256) VALUES (?,'imap',zeroblob(?),?)`, i+1, size, i+1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages(raw_id) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := store.PendingExtractions(ctx, db, 2, 1)
	if err != nil || len(targets) != 1 || targets[0].ID != 2 || targets[0].RawBytes != 32 {
		t.Fatalf("targets = %+v %v", targets, err)
	}
	if raw, err := store.GetBoundedRawMIMEByMessageID(ctx, db, 1); !errors.Is(err, mailcontent.ErrTooLarge) || raw != nil {
		t.Fatalf("oversized read: %d %v", len(raw), err)
	}
	if raw, err := store.GetBoundedRawMIMEByMessageID(ctx, db, 2); err != nil || len(raw) != 32 {
		t.Fatalf("normal read: %d %v", len(raw), err)
	}
	if n, err := store.OversizedPendingExtractions(ctx, db, 2); err != nil || n != 1 {
		t.Fatalf("oversized count: %d %v", n, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM message_extractions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("oversized mail was marked scanned")
	}
}
