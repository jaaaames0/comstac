package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserNotFound       = errors.New("user not found")
)

const (
	MinimumPasswordBytes = 12
	MaximumPasswordBytes = 72
)

const (
	DefaultSessionIdleTTL = 24 * time.Hour
	DefaultSessionMaxAge  = 14 * 24 * time.Hour
)

type Manager struct {
	db         *sql.DB
	cookieName string
	idleTTL    time.Duration
	maxAge     time.Duration
	csrfSecret []byte
}

// Session is a valid session after Authenticate has applied idle renewal.
type Session struct {
	Expires time.Time
	// Refreshed reports that the expiry moved and the cookie should be re-issued.
	Refreshed bool
}

// NewManager configures sessions that expire after idleTTL without use and
// never outlive maxAge from login, however often they are used.
func NewManager(db *sql.DB, cookieName string, idleTTL, maxAge time.Duration, csrfSecret string) *Manager {
	if cookieName == "" {
		cookieName = "comstac_session"
	}
	if idleTTL <= 0 {
		idleTTL = DefaultSessionIdleTTL
	}
	if maxAge <= 0 {
		maxAge = DefaultSessionMaxAge
	}
	if idleTTL > maxAge {
		idleTTL = maxAge
	}
	secret := []byte(csrfSecret)
	if len(secret) == 0 {
		secret = []byte("comstac-csrf-default-key")
	}
	return &Manager{db: db, cookieName: cookieName, idleTTL: idleTTL, maxAge: maxAge, csrfSecret: secret}
}

// CSRFToken returns a per-session CSRF token derived from the session token via HMAC-SHA256.
func (m *Manager) CSRFToken(sessionToken string) string {
	mac := hmac.New(sha256.New, m.csrfSecret)
	mac.Write([]byte(sessionToken))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// ValidateCSRF checks the X-CSRF-Token header or _csrf form field against the expected token.
func (m *Manager) ValidateCSRF(r *http.Request, sessionToken string) bool {
	expected := m.CSRFToken(sessionToken)
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.FormValue("_csrf")
	}
	return hmac.Equal([]byte(got), []byte(expected))
}

func (m *Manager) BootstrapUser(ctx context.Context, username, password string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("auth manager not configured")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	var userCount int
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if userCount > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	if _, err := m.db.ExecContext(ctx, `INSERT INTO users(username, password_hash) VALUES (?, ?)`, username, string(hash)); err != nil {
		return fmt.Errorf("insert bootstrap user: %w", err)
	}
	return nil
}

func (m *Manager) Login(ctx context.Context, username, password string) (string, time.Time, error) {
	if m == nil || m.db == nil {
		return "", time.Time{}, fmt.Errorf("auth manager not configured")
	}

	var userID int64
	var hash string
	err := m.db.QueryRowContext(ctx, `SELECT id, password_hash FROM users WHERE username = ?`, strings.TrimSpace(username)).Scan(&userID, &hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, ErrInvalidCredentials
		}
		return "", time.Time{}, fmt.Errorf("select user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", time.Time{}, ErrInvalidCredentials
	}

	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate token: %w", err)
	}
	now := time.Now()
	maxExpires := now.Add(m.maxAge)
	expires := now.Add(m.idleTTL)
	if expires.After(maxExpires) {
		expires = maxExpires
	}

	// Expired rows are otherwise only removed when their token is presented.
	if _, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at_unix <= ? OR max_expires_at_unix <= ?`, now.Unix(), now.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("prune sessions: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `INSERT INTO sessions(token, user_id, expires_at_unix, max_expires_at_unix) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, expires.Unix(), maxExpires.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("insert session: %w", err)
	}
	return token, expires, nil
}

// RotatePassword atomically replaces one user's bcrypt hash and revokes every
// session belonging to that user. The plaintext password is never persisted.
func (m *Manager) RotatePassword(ctx context.Context, username string, password []byte) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("auth manager not configured")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}
	if len(password) < MinimumPasswordBytes || len(password) > MaximumPasswordBytes {
		return fmt.Errorf("password must be between %d and %d bytes", MinimumPasswordBytes, MaximumPasswordBytes)
	}

	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password rotation: %w", err)
	}
	defer tx.Rollback()

	var userID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return fmt.Errorf("select user for password rotation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), userID); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit password rotation: %w", err)
	}
	return nil
}

func (m *Manager) Logout(ctx context.Context, token string) error {
	if m == nil || m.db == nil || token == "" {
		return nil
	}
	_, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, hashToken(token))
	return err
}

// LogoutEverywhere revokes every session belonging to the token's user,
// including the presented one.
func (m *Manager) LogoutEverywhere(ctx context.Context, token string) error {
	if m == nil || m.db == nil || token == "" {
		return nil
	}
	_, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = (SELECT user_id FROM sessions WHERE token = ?)`, hashToken(token))
	return err
}

// Authenticate returns nil when the token is not a live session. A live
// session's idle expiry slides forward with use, capped at its maximum age;
// the row is rewritten at most once per refresh step to avoid a write per request.
func (m *Manager) Authenticate(ctx context.Context, token string) (*Session, error) {
	if m == nil || m.db == nil || token == "" {
		return nil, nil
	}
	key := hashToken(token)
	var expiresAt, maxExpiresAt int64
	err := m.db.QueryRowContext(ctx, `SELECT expires_at_unix, max_expires_at_unix FROM sessions WHERE token = ?`, key).Scan(&expiresAt, &maxExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("select session: %w", err)
	}
	now := time.Now()
	if now.Unix() >= expiresAt || now.Unix() >= maxExpiresAt {
		_, _ = m.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, key)
		return nil, nil
	}

	next := now.Add(m.idleTTL).Unix()
	if next > maxExpiresAt {
		next = maxExpiresAt
	}
	if next-expiresAt < int64(m.refreshStep()/time.Second) {
		return &Session{Expires: time.Unix(expiresAt, 0)}, nil
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE sessions SET expires_at_unix = ? WHERE token = ? AND expires_at_unix < ?`, next, key, next); err != nil {
		return nil, fmt.Errorf("refresh session: %w", err)
	}
	return &Session{Expires: time.Unix(next, 0), Refreshed: true}, nil
}

func (m *Manager) refreshStep() time.Duration {
	if step := m.idleTTL / 4; step < time.Hour {
		return step
	}
	return time.Hour
}

func (m *Manager) ReadToken(r *http.Request) string {
	if m == nil {
		return ""
	}
	cookie, err := r.Cookie(m.cookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (m *Manager) SetSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	if m == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

func (m *Manager) ClearSessionCookie(w http.ResponseWriter) {
	if m == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// hashToken is the stored form of a session token, so a copy of the database
// does not contain usable cookies.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
