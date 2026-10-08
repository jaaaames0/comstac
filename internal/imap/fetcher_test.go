package imap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"comstac/internal/ingest"
	"comstac/internal/store"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type fakeMailbox struct {
	order               []imaplib.UID
	messages            map[imaplib.UID]*imapclient.FetchMessageBuffer
	searchErr, fetchErr error
	searchCalls         int
	beforeFetch         func()
}

func (m *fakeMailbox) SearchUIDs(from, to imaplib.UID) ([]imaplib.UID, error) {
	m.searchCalls++
	var uids []imaplib.UID
	for _, uid := range m.order {
		if uid >= from && uid <= to {
			uids = append(uids, uid)
		}
	}
	return uids, m.searchErr
}

func (m *fakeMailbox) FetchMessages(uids []imaplib.UID) ([]*imapclient.FetchMessageBuffer, error) {
	if m.beforeFetch != nil {
		m.beforeFetch()
	}
	wanted := make(map[imaplib.UID]bool)
	for _, uid := range uids {
		wanted[uid] = true
	}
	var messages []*imapclient.FetchMessageBuffer
	for _, uid := range m.order {
		if wanted[uid] && m.messages[uid] != nil {
			messages = append(messages, m.messages[uid])
		}
	}
	return messages, m.fetchErr
}

type mailCounter struct{ count int }

func (n *mailCounter) QueueNewMail(string, string, int64) bool { n.count++; return true }

func testFetcher(t *testing.T) (*Fetcher, int64, *mailCounter) {
	t.Helper()
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "imap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	account, err := store.EnsureIMAPAccount(context.Background(), db, "owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	n := &mailCounter{}
	svc := ingest.NewService(db)
	svc.SetNotifier(n)
	return NewFetcher(FetcherConfig{}, db, svc), account, n
}

func message(uid imaplib.UID) *imapclient.FetchMessageBuffer {
	return &imapclient.FetchMessageBuffer{UID: uid, BodySection: []imapclient.FetchBodySectionBuffer{{
		Bytes: []byte(fmt.Sprintf("Subject: Message %d\r\n\r\nBody %d", uid, uid)),
	}}}
}

func checkpoint(t *testing.T, f *Fetcher, account int64) uint32 {
	t.Helper()
	uid, _, err := store.GetIMAPCheckpoint(context.Background(), f.db, account, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

func setCheckpoint(t *testing.T, f *Fetcher, account int64, uid uint32) {
	t.Helper()
	if err := store.SetIMAPCheckpoint(context.Background(), f.db, account, "INBOX", uid); err != nil {
		t.Fatal(err)
	}
}

func execSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestPartialIngestFailureRemainsRetryable(t *testing.T) {
	ctx := context.Background()
	f, account, n := testFetcher(t)
	setCheckpoint(t, f, account, 100)
	// Higher UID arrives first, succeeds, and must be deduplicated on retry.
	m := &fakeMailbox{order: []imaplib.UID{102, 101}, messages: map[imaplib.UID]*imapclient.FetchMessageBuffer{101: message(101), 102: message(102)}}
	execSQL(t, f.db, `CREATE TRIGGER fail_ingest BEFORE INSERT ON messages_raw WHEN NEW.remote_id = '101' BEGIN SELECT RAISE(FAIL, 'injected persistence failure'); END`)
	if err := f.fetchMailbox(ctx, account, 103, m); err == nil {
		t.Fatal("ingest failure was swallowed")
	}
	if got := checkpoint(t, f, account); got != 100 {
		t.Fatalf("checkpoint advanced past failed UID: %d", got)
	}
	execSQL(t, f.db, `DROP TRIGGER fail_ingest`)
	// Recreate the fetcher to prove this is a durable retry, not local state.
	f = NewFetcher(f.cfg, f.db, f.ingestor)
	if err := f.fetchMailbox(ctx, account, 103, m); err != nil {
		t.Fatal(err)
	}
	if got := checkpoint(t, f, account); got != 102 {
		t.Fatalf("checkpoint = %d", got)
	}
	if n.count != 2 {
		t.Fatalf("notifications = %d, want 2", n.count)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("messages = %d, %v", count, err)
	}
}

func TestInitializedEmptyMailboxFetchesFirstArrival(t *testing.T) {
	f, account, n := testFetcher(t)
	m := &fakeMailbox{}
	if err := f.fetchMailbox(context.Background(), account, 1, m); err != nil {
		t.Fatal(err)
	}
	m.order = []imaplib.UID{1}
	m.messages = map[imaplib.UID]*imapclient.FetchMessageBuffer{1: message(1)}
	f = NewFetcher(f.cfg, f.db, f.ingestor)
	if err := f.fetchMailbox(context.Background(), account, 2, m); err != nil {
		t.Fatal(err)
	}
	if n.count != 1 || checkpoint(t, f, account) != 1 {
		t.Fatalf("first arrival skipped; notifications = %d", n.count)
	}
}

func TestFirstConnectionStillSkipsHistory(t *testing.T) {
	f, account, n := testFetcher(t)
	m := &fakeMailbox{order: []imaplib.UID{100}, messages: map[imaplib.UID]*imapclient.FetchMessageBuffer{100: message(100)}}
	if err := f.fetchMailbox(context.Background(), account, 101, m); err != nil {
		t.Fatal(err)
	}
	if n.count != 0 || m.searchCalls != 0 || checkpoint(t, f, account) != 100 {
		t.Fatal("initial history policy changed")
	}
}

func TestCheckpointSaveFailurePropagatesAndRetryDeduplicates(t *testing.T) {
	f, account, n := testFetcher(t)
	setCheckpoint(t, f, account, 100)
	m := &fakeMailbox{order: []imaplib.UID{101}, messages: map[imaplib.UID]*imapclient.FetchMessageBuffer{101: message(101)}}
	execSQL(t, f.db, `CREATE TRIGGER fail_checkpoint BEFORE UPDATE ON imap_checkpoints BEGIN SELECT RAISE(FAIL, 'injected checkpoint failure'); END`)
	if err := f.fetchMailbox(context.Background(), account, 102, m); err == nil {
		t.Fatal("checkpoint error was swallowed")
	}
	if checkpoint(t, f, account) != 100 {
		t.Fatal("checkpoint changed")
	}
	execSQL(t, f.db, `DROP TRIGGER fail_checkpoint`)
	if err := f.fetchMailbox(context.Background(), account, 102, m); err != nil {
		t.Fatal(err)
	}
	if checkpoint(t, f, account) != 101 || n.count != 1 {
		t.Fatal("retry duplicated persisted mail")
	}
}

func TestUnrelatedUniqueFailureIsNotDuplicateMail(t *testing.T) {
	f, account, _ := testFetcher(t)
	setCheckpoint(t, f, account, 100)
	// A different unique constraint must never be accepted as persisted MIME.
	execSQL(t, f.db, `CREATE TABLE collision (value TEXT UNIQUE); INSERT INTO collision VALUES ('same'); CREATE TRIGGER fail_other_unique BEFORE INSERT ON messages BEGIN INSERT INTO collision VALUES ('same'); END`)
	m := &fakeMailbox{order: []imaplib.UID{101}, messages: map[imaplib.UID]*imapclient.FetchMessageBuffer{101: message(101)}}
	if err := f.fetchMailbox(context.Background(), account, 102, m); err == nil {
		t.Fatal("unrelated UNIQUE failure treated as duplicate")
	}
	if checkpoint(t, f, account) != 100 {
		t.Fatal("checkpoint skipped failed insert")
	}
}

func TestSearchAndFetchErrorsLeaveCheckpoint(t *testing.T) {
	for _, stage := range []string{"search", "fetch"} {
		t.Run(stage, func(t *testing.T) {
			f, account, _ := testFetcher(t)
			setCheckpoint(t, f, account, 100)
			m := &fakeMailbox{order: []imaplib.UID{101}}
			if stage == "search" {
				m.searchErr = errors.New("search failed")
			} else {
				m.fetchErr = errors.New("fetch failed")
			}
			if err := f.fetchMailbox(context.Background(), account, 102, m); err == nil {
				t.Fatal("expected error")
			}
			if checkpoint(t, f, account) != 100 {
				t.Fatal("checkpoint changed")
			}
		})
	}
}

func TestMissingFetchedMessageMustBeConfirmedExpunged(t *testing.T) {
	for _, expunged := range []bool{false, true} {
		t.Run(fmt.Sprintf("expunged=%t", expunged), func(t *testing.T) {
			f, account, n := testFetcher(t)
			setCheckpoint(t, f, account, 100)
			m := &fakeMailbox{order: []imaplib.UID{101, 102}, messages: map[imaplib.UID]*imapclient.FetchMessageBuffer{102: message(102)}}
			if expunged {
				m.beforeFetch = func() { m.order = []imaplib.UID{102} }
			}
			err := f.fetchMailbox(context.Background(), account, 103, m)
			if expunged {
				if err != nil || checkpoint(t, f, account) != 102 || n.count != 1 {
					t.Fatalf("expunged range: %v", err)
				}
			} else {
				if err == nil || checkpoint(t, f, account) != 100 {
					t.Fatal("still-present omitted UID skipped")
				}
				m.messages[101] = message(101)
				if err := f.fetchMailbox(context.Background(), account, 103, m); err != nil {
					t.Fatal(err)
				}
				if n.count != 2 {
					t.Fatalf("retry notifications = %d", n.count)
				}
			}
			if m.searchCalls < 2 {
				t.Fatal("missing UID was not rechecked")
			}
		})
	}
}

func TestMissingOrEmptyMIMEBodyIsRetryable(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			f, account, _ := testFetcher(t)
			setCheckpoint(t, f, account, 100)
			bad := &imapclient.FetchMessageBuffer{UID: 101}
			if empty {
				bad.BodySection = []imapclient.FetchBodySectionBuffer{{Bytes: []byte{}}}
			}
			m := &fakeMailbox{order: []imaplib.UID{102, 101}, messages: map[imaplib.UID]*imapclient.FetchMessageBuffer{101: bad, 102: message(102)}}
			if err := f.fetchMailbox(context.Background(), account, 103, m); err == nil {
				t.Fatal("invalid body accepted")
			}
			if checkpoint(t, f, account) != 100 {
				t.Fatal("invalid body skipped")
			}
		})
	}
}

func TestSearchOrderingCannotCommitHigherBatchBeforeFailure(t *testing.T) {
	f, account, _ := testFetcher(t)
	setCheckpoint(t, f, account, 100)
	m := &fakeMailbox{messages: make(map[imaplib.UID]*imapclient.FetchMessageBuffer)}
	for uid := imaplib.UID(151); uid >= 101; uid-- {
		m.order = append(m.order, uid)
		m.messages[uid] = message(uid)
	}
	execSQL(t, f.db, `CREATE TRIGGER fail_ingest BEFORE INSERT ON messages_raw WHEN NEW.remote_id = '101' BEGIN SELECT RAISE(FAIL, 'injected persistence failure'); END`)
	if err := f.fetchMailbox(context.Background(), account, 152, m); err == nil {
		t.Fatal("expected ingest failure")
	}
	if checkpoint(t, f, account) != 100 {
		t.Fatal("higher batch committed before lower failure")
	}
	execSQL(t, f.db, `DROP TRIGGER fail_ingest`)
	if err := f.fetchMailbox(context.Background(), account, 152, m); err != nil {
		t.Fatal(err)
	}
	if checkpoint(t, f, account) != 151 {
		t.Fatal("retry did not complete all batches")
	}
}

func TestEmptySearchAdvancesOnlyKnownAbsentRange(t *testing.T) {
	f, account, n := testFetcher(t)
	setCheckpoint(t, f, account, 100)
	m := &fakeMailbox{}
	if err := f.fetchMailbox(context.Background(), account, 104, m); err != nil {
		t.Fatal(err)
	}
	if checkpoint(t, f, account) != 103 || n.count != 0 {
		t.Fatal("absent range was not checkpointed")
	}
	m.order = []imaplib.UID{104}
	m.messages = map[imaplib.UID]*imapclient.FetchMessageBuffer{104: message(104)}
	if err := f.fetchMailbox(context.Background(), account, 105, m); err != nil {
		t.Fatal(err)
	}
	if checkpoint(t, f, account) != 104 || n.count != 1 {
		t.Fatal("later arrival skipped")
	}
}
