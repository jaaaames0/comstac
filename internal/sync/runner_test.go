package sync

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comstac/internal/imap"
	"comstac/internal/ingest"
	"comstac/internal/store"
)

type adapterFunc func(context.Context, imap.StateAction) error

func (f adapterFunc) ApplyStateAction(ctx context.Context, action imap.StateAction) error {
	return f(ctx, action)
}

func TestRunnerMarksDoneOnSuccess(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	messageID := seedIMAPMessage(t, db)
	jobID := insertSyncJob(t, db, messageID, "archive", `{"archived":true}`)

	r := NewRunner(db, time.Second, adapterFunc(func(_ context.Context, action imap.StateAction) error {
		if action.MessageID != messageID {
			t.Fatalf("unexpected message id: got=%d want=%d", action.MessageID, messageID)
		}
		return nil
	}))

	if err := r.processOne(context.Background()); err != nil {
		t.Fatalf("process one: %v", err)
	}

	got := readSyncJobState(t, db, jobID)
	if got.status != "done" || got.attempts != 1 {
		t.Fatalf("unexpected state: %+v", got)
	}
	if got.lastError != "" {
		t.Fatalf("expected empty error, got %q", got.lastError)
	}
}

func TestRunnerRetriesThenFailsAtMaxAttempts(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	messageID := seedIMAPMessage(t, db)
	jobID := insertSyncJob(t, db, messageID, "read", `{"read":true}`)
	now := time.Unix(1_900_000_000, 0).UTC()

	r := NewRunner(db, time.Second, adapterFunc(func(_ context.Context, _ imap.StateAction) error {
		return errors.New("temporary upstream failure")
	}))
	r.maxAttempts = 2
	r.baseBackoff = 5 * time.Second
	r.maxBackoff = 5 * time.Second
	r.nowFn = func() time.Time { return now }

	if err := r.processOne(context.Background()); err != nil {
		t.Fatalf("first process: %v", err)
	}

	first := readSyncJobState(t, db, jobID)
	if first.status != "retrying" || first.attempts != 1 {
		t.Fatalf("unexpected first state: %+v", first)
	}
	if first.nextAttemptUnix != now.Add(5*time.Second).Unix() {
		t.Fatalf("unexpected retry time: got=%d want=%d", first.nextAttemptUnix, now.Add(5*time.Second).Unix())
	}

	if _, err := db.Exec(`UPDATE sync_jobs SET next_attempt_at_unix = strftime('%s', 'now') WHERE id = ?`, jobID); err != nil {
		t.Fatalf("reset next attempt: %v", err)
	}
	if err := r.processOne(context.Background()); err != nil {
		t.Fatalf("second process: %v", err)
	}

	second := readSyncJobState(t, db, jobID)
	if second.status != "failed" || second.attempts != 2 {
		t.Fatalf("unexpected second state: %+v", second)
	}
	if second.nextAttemptUnix != 0 {
		t.Fatalf("expected no next attempt for failed job, got %d", second.nextAttemptUnix)
	}
	if !strings.Contains(second.lastError, "temporary upstream failure") {
		t.Fatalf("expected stored error, got %q", second.lastError)
	}
}

func TestRunnerMarksPermanentErrorsFailedImmediately(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	messageID := seedIMAPMessage(t, db)
	jobID := insertSyncJob(t, db, messageID, "snooze", `{"until":"2030-01-01T00:00:00Z"}`)

	r := NewRunner(db, time.Second, adapterFunc(func(_ context.Context, _ imap.StateAction) error {
		return imap.PermanentError(errors.New("unsupported action"))
	}))
	r.maxAttempts = 5

	if err := r.processOne(context.Background()); err != nil {
		t.Fatalf("process one: %v", err)
	}

	got := readSyncJobState(t, db, jobID)
	if got.status != "failed" || got.attempts != 1 {
		t.Fatalf("unexpected state: %+v", got)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sync.db")
	db, err := store.OpenAndMigrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedIMAPMessage(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO accounts(name, kind, email_address) VALUES ('imap-main', 'imap', 'imap@example.com')`)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accountID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("account id: %v", err)
	}

	ingestor := ingest.NewService(db)
	err = ingestor.IngestRaw(context.Background(), ingest.IngestInput{
		Source:       ingest.SourceIMAP,
		AccountID:    sql.NullInt64{Int64: accountID, Valid: true},
		EnvelopeFrom: "sender@example.com",
		EnvelopeTo:   []string{"local@example.com"},
		RemoteID:     "remote-123",
		RawMIME:      []byte("Subject: Sync Test\r\nFrom: sender@example.com\r\nTo: local@example.com\r\n\r\nbody\r\n"),
	})
	if err != nil {
		t.Fatalf("ingest imap: %v", err)
	}

	var messageID int64
	if err := db.QueryRow(`SELECT m.id FROM messages m ORDER BY m.id DESC LIMIT 1`).Scan(&messageID); err != nil {
		t.Fatalf("select message id: %v", err)
	}
	return messageID
}

func insertSyncJob(t *testing.T, db *sql.DB, messageID int64, action string, payload string) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO sync_jobs(message_id, action, payload_json, status, next_attempt_at_unix)
		VALUES (?, ?, ?, 'pending', strftime('%s', 'now'))
	`, messageID, action, payload)
	if err != nil {
		t.Fatalf("insert sync job: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("job id: %v", err)
	}
	return id
}

type syncJobState struct {
	status          string
	attempts        int
	nextAttemptUnix int64
	lastError       string
}

func readSyncJobState(t *testing.T, db *sql.DB, jobID int64) syncJobState {
	t.Helper()
	var out syncJobState
	if err := db.QueryRow(`
		SELECT status, attempts, next_attempt_at_unix, COALESCE(last_error, '')
		FROM sync_jobs
		WHERE id = ?
	`, jobID).Scan(&out.status, &out.attempts, &out.nextAttemptUnix, &out.lastError); err != nil {
		t.Fatalf("read sync job: %v", err)
	}
	return out
}
