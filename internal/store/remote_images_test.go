package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"comstac/internal/store"
)

func TestRemoteImageSenderMigrationNormalizationAndMatching(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "allowlist.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var table string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='remote_image_senders'`).Scan(&table); err != nil {
		t.Fatalf("migration did not create table: %v", err)
	}

	got, err := store.AddRemoteImageSender(ctx, db, `Trusted Sender <Mixed.Case@Example.COM>`)
	if err != nil {
		t.Fatalf("add sender: %v", err)
	}
	if got != "mixed.case@example.com" {
		t.Fatalf("normalized address=%q", got)
	}
	if _, err := store.AddRemoteImageSender(ctx, db, "MIXED.CASE@EXAMPLE.COM"); err != nil {
		t.Fatalf("idempotent add: %v", err)
	}

	allowed, err := store.RemoteImageSenderAllowed(ctx, db, `Another Name <mixed.case@example.com>`)
	if err != nil || !allowed {
		t.Fatalf("approved mailbox did not match: allowed=%v err=%v", allowed, err)
	}
	allowed, err = store.RemoteImageSenderAllowed(ctx, db, `"mixed.case@example.com" <attacker@example.net>`)
	if err != nil {
		t.Fatalf("forged display-name lookup: %v", err)
	}
	if allowed {
		t.Fatal("forged display name matched approved mailbox")
	}
	if _, err := store.AddRemoteImageSender(ctx, db, "*@example.net"); err != nil {
		t.Fatalf("add literal wildcard-shaped mailbox: %v", err)
	}
	allowed, err = store.RemoteImageSenderAllowed(ctx, db, "someone@example.net")
	if err != nil {
		t.Fatalf("exact-address lookup: %v", err)
	}
	if allowed {
		t.Fatal("wildcard-shaped mailbox was treated as a domain wildcard")
	}

	senders, err := store.ListRemoteImageSenders(ctx, db)
	if err != nil {
		t.Fatalf("list senders: %v", err)
	}
	if len(senders) != 2 || senders[0].EmailAddress != "*@example.net" || senders[1].EmailAddress != "mixed.case@example.com" {
		t.Fatalf("senders=%+v", senders)
	}

	if err := store.RemoveRemoteImageSender(ctx, db, "Mixed.Case@Example.com"); err != nil {
		t.Fatalf("remove sender: %v", err)
	}
	allowed, err = store.RemoteImageSenderAllowed(ctx, db, "mixed.case@example.com")
	if err != nil || allowed {
		t.Fatalf("removed sender still matched: allowed=%v err=%v", allowed, err)
	}
}

func TestNormalizeMailboxAddressRejectsAmbiguousInput(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "not-an-address", "one@example.com, two@example.com", "display only <missing>"} {
		if _, err := store.NormalizeMailboxAddress(raw); err == nil {
			t.Errorf("NormalizeMailboxAddress(%q) succeeded", raw)
		}
	}
}
