package smtpserver_test

import (
	"context"
	"database/sql"
	"net"
	"net/smtp"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/smtpserver"
	"comstac/internal/store"
)

func TestSMTPToSQLiteIngestSmoke(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	ingestor := ingest.NewService(db)
	srv := smtpserver.New(":0", ingestor, smtpserver.Options{
		LocalDomains:    []string{"example.com"},
		LocalRecipients: []string{"local@example.com"},
	})

	ln, done, cancel := runSMTPServer(t, srv)
	defer cancel()

	msg := []byte("Subject: Smoke Test\r\nFrom: sender@example.com\r\nTo: local@example.com\r\n\r\nhello from test\r\n")
	if err := smtp.SendMail(ln.Addr().String(), nil, "sender@example.com", []string{"local@example.com"}, msg); err != nil {
		t.Fatalf("send mail: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		count, qErr := rawCount(db)
		if qErr != nil {
			t.Fatalf("query raw count: %v", qErr)
		}
		if count >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("message not ingested before timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	waitSMTPStop(t, done)
}

func TestSMTPRejectsUnknownRecipient(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	ingestor := ingest.NewService(db)
	srv := smtpserver.New(":0", ingestor, smtpserver.Options{
		LocalDomains:    []string{"example.com"},
		LocalRecipients: []string{"local@example.com"},
	})

	ln, done, cancel := runSMTPServer(t, srv)
	defer cancel()

	msg := []byte("Subject: Reject\r\nFrom: sender@example.com\r\nTo: unknown@example.com\r\n\r\nblocked\r\n")
	err := smtp.SendMail(ln.Addr().String(), nil, "sender@example.com", []string{"unknown@example.com"}, msg)
	if err == nil {
		t.Fatalf("expected recipient rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "550") {
		t.Fatalf("expected 550 rejection, got: %v", err)
	}

	cancel()
	waitSMTPStop(t, done)
}

func TestSMTPRoutesToLocalAccountID(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	_, err := db.Exec(`INSERT INTO accounts(name, kind, email_address) VALUES ('local@example.com', 'local', 'local@example.com')`)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}

	ingestor := ingest.NewService(db)
	srv := smtpserver.New(":0", ingestor, smtpserver.Options{
		LocalDomains:    []string{"example.com"},
		LocalRecipients: []string{"local@example.com"},
		ResolveAccountID: func(ctx context.Context, recipient string) (sql.NullInt64, error) {
			return store.ResolveLocalAccountID(ctx, db, recipient)
		},
	})

	ln, done, cancel := runSMTPServer(t, srv)
	defer cancel()

	msg := []byte("Subject: Routed\r\nFrom: sender@example.com\r\nTo: local@example.com\r\n\r\naccount routing\r\n")
	if err := smtp.SendMail(ln.Addr().String(), nil, "sender@example.com", []string{"local@example.com"}, msg); err != nil {
		t.Fatalf("send mail: %v", err)
	}

	var accountID sql.NullInt64
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = db.QueryRow(`SELECT account_id FROM messages ORDER BY id DESC LIMIT 1`).Scan(&accountID)
		if err == nil && accountID.Valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected routed account_id, err=%v val=%+v", err, accountID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	waitSMTPStop(t, done)
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.OpenAndMigrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func runSMTPServer(t *testing.T, srv *smtpserver.Server) (net.Listener, chan error, context.CancelFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.RunWithListener(runCtx, ln)
	}()
	waitForReady(t, ln.Addr().String())
	return ln, done, func() {
		cancel()
		_ = ln.Close()
	}
}

func waitSMTPStop(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not stop")
	}
}

func waitForReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("smtp server not ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func rawCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages_raw WHERE source = 'smtp'`).Scan(&n)
	return n, err
}
