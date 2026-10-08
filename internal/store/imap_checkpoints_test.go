package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"comstac/internal/store"
)

func TestIMAPCheckpointDistinguishesAbsentAndSavedZeroAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	db, err := store.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	account, err := store.EnsureIMAPAccount(ctx, db, "owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if uid, found, err := store.GetIMAPCheckpoint(ctx, db, account, "INBOX"); err != nil || uid != 0 || found {
		t.Fatalf("absent checkpoint = %d, %t, %v", uid, found, err)
	}
	if err := store.SetIMAPCheckpoint(ctx, db, account, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if uid, found, err := store.GetIMAPCheckpoint(ctx, db, account, "INBOX"); err != nil || uid != 0 || !found {
		t.Fatalf("saved zero after reopen = %d, %t, %v", uid, found, err)
	}
}
