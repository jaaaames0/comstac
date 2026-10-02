package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

func seedSnoozeMessages(t *testing.T, n int) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "snooze.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ingestor := ingest.NewService(db)
	for i := 1; i <= n; i++ {
		raw := strings.Join([]string{
			fmt.Sprintf("Subject: message %d", i),
			"From: sender@example.com",
			"To: local@example.com",
			fmt.Sprintf("Message-ID: <m%d@example.com>", i),
			"",
			fmt.Sprintf("body %d", i),
			"",
		}, "\r\n")
		if err := ingestor.IngestRaw(ctx, ingest.IngestInput{
			Source: ingest.SourceSMTP, EnvelopeFrom: "sender@example.com",
			EnvelopeTo: []string{"local@example.com"}, RawMIME: []byte(raw),
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
		// Distinct, older arrival times so resurfacing visibly reorders.
		if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = datetime('now', ?), read = 1 WHERE id = ?`,
			fmt.Sprintf("-%d days", n-i+1), i); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func listIDs(t *testing.T, db *sql.DB, opts store.ListMessageOptions) []int64 {
	t.Helper()
	items, err := store.ListMessages(context.Background(), db, opts)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func TestWakeDueSnoozesResurfacesInboxMessages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := seedSnoozeMessages(t, 3)
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)

	mustSnooze := func(id int64, until time.Time) {
		if _, err := store.SetMessageSnoozeUntil(ctx, db, id, &until); err != nil {
			t.Fatal(err)
		}
	}
	mustSnooze(1, past)   // inbox, due
	mustSnooze(2, future) // inbox, pending
	mustSnooze(3, past)   // trashed, due
	if _, err := store.SetMessageArchived(ctx, db, 3, true); err != nil {
		t.Fatal(err)
	}

	no := false
	if got := listIDs(t, db, store.ListMessageOptions{Limit: 10, Snoozed: true}); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("snoozed view before wake = %v, want soonest first [1 2]", got)
	}

	woken, err := store.WakeDueSnoozes(ctx, db, now)
	if err != nil {
		t.Fatalf("wake: %v", err)
	}
	if len(woken) != 1 || woken[0].ID != 1 || woken[0].Subject != "message 1" {
		t.Fatalf("woken = %+v, want only message 1", woken)
	}

	d1, _ := store.GetMessageDetail(ctx, db, 1)
	d2, _ := store.GetMessageDetail(ctx, db, 2)
	d3, _ := store.GetMessageDetail(ctx, db, 3)
	if d1.SnoozeUntil != "" || d1.Read {
		t.Fatalf("message 1 not resurfaced: %+v", d1)
	}
	if d2.SnoozeUntil == "" {
		t.Fatal("pending snooze was cleared")
	}
	if d3.SnoozeUntil != "" || !d3.Read || !d3.Archived {
		t.Fatalf("trashed due snooze should only be cleared: %+v", d3)
	}

	inbox := store.ListMessageOptions{Limit: 10, Archived: &no, Spam: &no}
	if got := listIDs(t, db, inbox); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("inbox order = %v, want resurfaced message first [1 2]", got)
	}
	items, _ := store.ListMessages(ctx, db, inbox)
	if items[0].ResurfacedAt == "" || items[1].ResurfacedAt != "" {
		t.Fatalf("resurfaced_at not exposed correctly: %+v", items)
	}

	// Keyset pagination follows the same order.
	page := store.ListMessageOptions{Limit: 1, Archived: &no, Spam: &no}
	first := listIDs(t, db, page)
	page.BeforeID = first[0]
	second := listIDs(t, db, page)
	if fmt.Sprint(first, second) != "[1] [2]" {
		t.Fatalf("pagination = %v %v, want [1] [2]", first, second)
	}

	if again, err := store.WakeDueSnoozes(ctx, db, now); err != nil || len(again) != 0 {
		t.Fatalf("second wake = %v %v, want nothing", again, err)
	}
	if got := listIDs(t, db, store.ListMessageOptions{Limit: 10, Snoozed: true}); fmt.Sprint(got) != "[2]" {
		t.Fatalf("snoozed view after wake = %v, want [2]", got)
	}
}

func TestPushDeliveryLogIsBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "pushlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < 205; i++ {
		if err := store.RecordPushDelivery(ctx, db, store.PushDelivery{Kind: fmt.Sprintf("k%d", i), EndpointHost: "h", Status: 201}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ListPushDeliveries(ctx, db, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 200 || all[0].Kind != "k204" {
		t.Fatalf("log has %d rows, newest %q; want 200 newest k204", len(all), all[0].Kind)
	}
}
