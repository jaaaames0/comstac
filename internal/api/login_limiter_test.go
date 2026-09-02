package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authpkg "comstac/internal/auth"
	"comstac/internal/store"
)

func TestLoginLimiterBlocksAndSuccessClears(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter()
	limiter.now = func() time.Time { return now }

	for i := 0; i < loginFailureLimit-1; i++ {
		blocked, _ := limiter.failure("client")
		if blocked {
			t.Fatalf("failure %d blocked too early", i+1)
		}
	}
	blocked, retryAfter := limiter.failure("client")
	if !blocked || retryAfter != loginBlockDuration {
		t.Fatalf("final failure blocked=%v retryAfter=%v", blocked, retryAfter)
	}
	if allowed, _ := limiter.allow("client"); allowed {
		t.Fatal("blocked client was allowed")
	}

	now = now.Add(loginBlockDuration)
	if allowed, _ := limiter.allow("client"); !allowed {
		t.Fatal("client remained blocked after duration")
	}

	limiter.failure("client")
	limiter.success("client")
	if allowed, _ := limiter.allow("client"); !allowed {
		t.Fatal("successful login did not clear failures")
	}
}

func TestLoginClientKeyTrustsRealIPOnlyFromLoopback(t *testing.T) {
	proxied := httptest.NewRequest("POST", "http://example.test/api/login", nil)
	proxied.RemoteAddr = "127.0.0.1:12345"
	proxied.Header.Set("X-Real-IP", "203.0.113.9")
	if got := loginClientKey(proxied); got != "203.0.113.9" {
		t.Fatalf("proxied key = %q", got)
	}

	direct := httptest.NewRequest("POST", "http://example.test/api/login", nil)
	direct.RemoteAddr = "198.51.100.4:54321"
	direct.Header.Set("X-Real-IP", "203.0.113.9")
	if got := loginClientKey(direct); got != "198.51.100.4" {
		t.Fatalf("direct key = %q", got)
	}
}

func TestLoginLimiterIsSharedAcrossHTMLAndAPIRoutes(t *testing.T) {
	db := openLoginLimiterTestDB(t)
	authMgr := bootstrapLoginLimiterAuth(t, db)
	handler := New("127.0.0.1:8080", db, authMgr, nil, nil, nil).Handler()

	for i := 0; i < loginFailureLimit; i++ {
		path := "/login"
		contentType := "application/x-www-form-urlencoded"
		body := []byte("username=admin&password=wrong-password")
		if i%2 == 1 {
			path = "/api/login"
			contentType = "application/json"
			body = []byte(`{"username":"admin","password":"wrong-password"}`)
		}
		req := httptest.NewRequest(http.MethodPost, "http://example.test"+path, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Real-IP", "203.0.113.10")
		req.Header.Set("Content-Type", contentType)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		want := http.StatusUnauthorized
		if i == loginFailureLimit-1 {
			want = http.StatusTooManyRequests
			if resp.Header().Get("Retry-After") == "" {
				t.Fatal("rate-limited response omitted Retry-After")
			}
		}
		if resp.Code != want {
			t.Fatalf("attempt %d status=%d body=%q want=%d", i+1, resp.Code, resp.Body.String(), want)
		}
	}
}

func TestAPILoginRejectsOversizeBody(t *testing.T) {
	db := openLoginLimiterTestDB(t)
	authMgr := bootstrapLoginLimiterAuth(t, db)
	handler := New("127.0.0.1:8080", db, authMgr, nil, nil, nil).Handler()

	body := `{"username":"admin","password":"` + strings.Repeat("x", maxLoginBodyBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "http://example.test/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
}

func openLoginLimiterTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "login-limiter.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func bootstrapLoginLimiterAuth(t *testing.T, db *sql.DB) *authpkg.Manager {
	t.Helper()
	mgr := authpkg.NewManager(db, "", time.Hour, "test-csrf-secret")
	if err := mgr.BootstrapUser(context.Background(), "admin", "password123"); err != nil {
		t.Fatalf("bootstrap user: %v", err)
	}
	return mgr
}
