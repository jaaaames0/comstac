package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"comstac/internal/store"
)

func TestRotatePasswordReplacesHashAndRevokesAllSessions(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "rotation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := NewManager(db, "", time.Hour, "test-csrf-secret")
	if err := mgr.BootstrapUser(ctx, "admin", "initial-password"); err != nil {
		t.Fatal(err)
	}
	first, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}

	newPassword := []byte("replacement-password")
	if err := mgr.RotatePassword(ctx, " admin ", newPassword); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Login(ctx, "admin", "initial-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password error = %v", err)
	}
	if _, _, err := mgr.Login(ctx, "admin", string(newPassword)); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	for _, token := range []string{first, second} {
		ok, err := mgr.IsAuthenticated(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("pre-rotation session remained valid")
		}
	}
}

func TestRotatePasswordFailurePreservesExistingSession(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "rotation-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := NewManager(db, "", time.Hour, "test-csrf-secret")
	if err := mgr.BootstrapUser(ctx, "admin", "initial-password"); err != nil {
		t.Fatal(err)
	}
	token, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.RotatePassword(ctx, "missing", []byte("replacement-password")); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user error = %v", err)
	}
	if err := mgr.RotatePassword(ctx, "admin", []byte("too-short")); err == nil {
		t.Fatal("short password was accepted")
	}
	ok, err := mgr.IsAuthenticated(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("failed rotation revoked the existing session")
	}
}
