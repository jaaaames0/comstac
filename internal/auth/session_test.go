package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"comstac/internal/store"
)

func sessionTestManager(t *testing.T, idle, maxAge time.Duration) (*Manager, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := NewManager(db, "", idle, maxAge, "test-csrf-secret")
	if err := mgr.BootstrapUser(ctx, "admin", "initial-password"); err != nil {
		t.Fatal(err)
	}
	return mgr, db
}

func sessionLimits(t *testing.T, db *sql.DB, token string) (expires, maxExpires int64, found bool) {
	t.Helper()
	err := db.QueryRow(`SELECT expires_at_unix, max_expires_at_unix FROM sessions WHERE token = ?`, hashToken(token)).Scan(&expires, &maxExpires)
	if err == sql.ErrNoRows {
		return 0, 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return expires, maxExpires, true
}

func TestLoginStoresOnlyTokenDigestWithIdleAndMaximumExpiry(t *testing.T) {
	mgr, db := sessionTestManager(t, 24*time.Hour, 14*24*time.Hour)
	before := time.Now().Unix()
	token, expires, err := mgr.Login(context.Background(), "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	var raw int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE token = ?`, token).Scan(&raw); err != nil || raw != 0 {
		t.Fatalf("raw token stored: %d %v", raw, err)
	}
	gotExpires, gotMax, found := sessionLimits(t, db, token)
	if !found {
		t.Fatal("session digest not stored")
	}
	if gotExpires != expires.Unix() || gotExpires < before+24*3600 || gotExpires > before+24*3600+5 {
		t.Fatalf("idle expiry = %d (cookie %d), login at %d", gotExpires, expires.Unix(), before)
	}
	if gotMax < before+14*24*3600 || gotMax > before+14*24*3600+5 {
		t.Fatalf("maximum expiry = %d, login at %d", gotMax, before)
	}
}

func TestAuthenticateSlidesIdleExpiryAtMostOncePerStep(t *testing.T) {
	ctx := context.Background()
	mgr, db := sessionTestManager(t, 24*time.Hour, 14*24*time.Hour)
	token, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}

	// Freshly issued: no rewrite and no new cookie.
	session, err := mgr.Authenticate(ctx, token)
	if err != nil || session == nil || session.Refreshed {
		t.Fatalf("fresh session = %+v %v", session, err)
	}

	// Last used two hours ago: expiry moves to a full idle period from now.
	stale := time.Now().Add(22 * time.Hour).Unix()
	if _, err := db.Exec(`UPDATE sessions SET expires_at_unix = ? WHERE token = ?`, stale, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	session, err = mgr.Authenticate(ctx, token)
	if err != nil || session == nil || !session.Refreshed {
		t.Fatalf("stale session = %+v %v", session, err)
	}
	want := time.Now().Add(24 * time.Hour).Unix()
	if got, _, _ := sessionLimits(t, db, token); got != session.Expires.Unix() || got < want-5 || got > want {
		t.Fatalf("refreshed expiry = %d (cookie %d), want about %d", got, session.Expires.Unix(), want)
	}
	if session, err = mgr.Authenticate(ctx, token); err != nil || session == nil || session.Refreshed {
		t.Fatalf("second request refreshed again: %+v %v", session, err)
	}
}

func TestAuthenticateNeverExtendsPastMaximumAge(t *testing.T) {
	ctx := context.Background()
	mgr, db := sessionTestManager(t, 24*time.Hour, 14*24*time.Hour)
	token, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	key := hashToken(token)
	maxExpires := time.Now().Add(3 * time.Hour).Unix()
	if _, err := db.Exec(`UPDATE sessions SET expires_at_unix = ?, max_expires_at_unix = ? WHERE token = ?`,
		time.Now().Add(time.Hour).Unix(), maxExpires, key); err != nil {
		t.Fatal(err)
	}
	session, err := mgr.Authenticate(ctx, token)
	if err != nil || session == nil || !session.Refreshed || session.Expires.Unix() != maxExpires {
		t.Fatalf("capped refresh = %+v %v, want expiry %d", session, err, maxExpires)
	}

	// Daily use cannot keep a session alive past its maximum age.
	if _, err := db.Exec(`UPDATE sessions SET max_expires_at_unix = ? WHERE token = ?`, time.Now().Unix(), key); err != nil {
		t.Fatal(err)
	}
	if session, err = mgr.Authenticate(ctx, token); err != nil || session != nil {
		t.Fatalf("session past maximum age = %+v %v", session, err)
	}
	if _, _, found := sessionLimits(t, db, token); found {
		t.Fatal("session past maximum age was not deleted")
	}
}

func TestAuthenticateRejectsIdleExpiredAndLegacyRawTokens(t *testing.T) {
	ctx := context.Background()
	mgr, db := sessionTestManager(t, 24*time.Hour, 14*24*time.Hour)
	token, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET expires_at_unix = ? WHERE token = ?`, time.Now().Unix(), hashToken(token)); err != nil {
		t.Fatal(err)
	}
	if session, err := mgr.Authenticate(ctx, token); err != nil || session != nil {
		t.Fatalf("idle-expired session = %+v %v", session, err)
	}

	// A row written before digests were stored holds the cookie value itself.
	legacy := "legacy-raw-session-token"
	future := time.Now().Add(time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO sessions(token, user_id, expires_at_unix, max_expires_at_unix) VALUES (?, 1, ?, ?)`, legacy, future, future); err != nil {
		t.Fatal(err)
	}
	if session, err := mgr.Authenticate(ctx, legacy); err != nil || session != nil {
		t.Fatalf("legacy raw token accepted: %+v %v", session, err)
	}
	// Login prunes rows that can no longer be used.
	if _, err := db.Exec(`UPDATE sessions SET max_expires_at_unix = 0 WHERE token = ?`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Login(ctx, "admin", "initial-password"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE token = ?`, legacy).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired legacy rows = %d %v", n, err)
	}
}

func TestLogoutEverywhereRevokesOnlyThatUsersSessions(t *testing.T) {
	ctx := context.Background()
	mgr, db := sessionTestManager(t, 24*time.Hour, 14*24*time.Hour)
	first, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := mgr.Login(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO users(id, username, password_hash) VALUES (2, 'other', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(token, user_id, expires_at_unix, max_expires_at_unix) VALUES (?, 2, ?, ?)`, hashToken("other-user"), future, future); err != nil {
		t.Fatal(err)
	}

	if err := mgr.LogoutEverywhere(ctx, first); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if session, err := mgr.Authenticate(ctx, token); err != nil || session != nil {
			t.Fatalf("session survived logout everywhere: %+v %v", session, err)
		}
	}
	if session, err := mgr.Authenticate(ctx, "other-user"); err != nil || session == nil {
		t.Fatalf("another user's session was revoked: %+v %v", session, err)
	}
}

func TestNewManagerCapsIdleTimeoutAtMaximumAge(t *testing.T) {
	mgr := NewManager(nil, "", 48*time.Hour, 12*time.Hour, "")
	if mgr.idleTTL != 12*time.Hour || mgr.maxAge != 12*time.Hour {
		t.Fatalf("idle %v max %v", mgr.idleTTL, mgr.maxAge)
	}
	// Short idle timeouts refresh often enough not to lapse mid-use.
	if got := NewManager(nil, "", time.Hour, 0, "").refreshStep(); got != 15*time.Minute {
		t.Fatalf("refresh step = %v", got)
	}
}
