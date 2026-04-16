package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestGetMessageDetailExtractsMultipartPlainText(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "detail.db")
	db, err := store.OpenAndMigrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	raw := strings.Join([]string{
		"Subject: Multipart Test",
		"From: sender@example.com",
		"To: local@example.com",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=ALT-123",
		"",
		"--ALT-123",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"This is the plain text section.",
		"",
		"--ALT-123",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><p>This is html</p></body></html>",
		"",
		"--ALT-123--",
		"",
	}, "\r\n")

	ingestor := ingest.NewService(db)
	if err := ingestor.IngestRaw(context.Background(), ingest.IngestInput{
		Source:       ingest.SourceSMTP,
		EnvelopeFrom: "sender@example.com",
		EnvelopeTo:   []string{"local@example.com"},
		RawMIME:      []byte(raw),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	detail, err := store.GetMessageDetail(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail == nil {
		t.Fatalf("expected detail")
	}
	if !strings.Contains(detail.BodyText, "plain text section") {
		t.Fatalf("expected plain-text body extraction, got: %q", detail.BodyText)
	}
}
