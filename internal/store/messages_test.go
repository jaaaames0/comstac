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

func TestGetMessageDetailExtractsNestedMultipartHTML(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "nested.db")
	db, err := store.OpenAndMigrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// multipart/mixed wrapping multipart/alternative, as sent by Steam.
	raw := strings.Join([]string{
		"Subject: Nested Multipart",
		"From: sender@example.com",
		"To: local@example.com",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=MIX-1",
		"",
		"This is a multi-part message in MIME format.",
		"--MIX-1",
		"Content-Type: multipart/alternative; boundary=ALT-1",
		"",
		"--ALT-1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Plain section.",
		"",
		"--ALT-1",
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"<html><body><p style=3D\"color:red\">Nested html</p></body></html>",
		"",
		"--ALT-1--",
		"",
		"--MIX-1",
		"Content-Type: text/html; name=attached.html",
		"Content-Disposition: attachment; filename=attached.html",
		"",
		"<p>attached file</p>",
		"",
		"--MIX-1--",
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
	if !strings.Contains(detail.BodyHTML, `<p style="color:red">Nested html</p>`) {
		t.Fatalf("expected nested html body, got: %q", detail.BodyHTML)
	}
	if !strings.Contains(detail.BodyText, "Plain section.") {
		t.Fatalf("expected plain-text body, got: %q", detail.BodyText)
	}
}
