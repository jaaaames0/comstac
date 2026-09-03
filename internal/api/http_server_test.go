package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPServerHasDefensiveTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:8080", http.NewServeMux())
	if srv.ReadHeaderTimeout != httpReadHeaderTimeout || srv.ReadTimeout != httpReadTimeout ||
		srv.WriteTimeout != httpWriteTimeout || srv.IdleTimeout != httpIdleTimeout {
		t.Fatalf("unexpected timeouts: %+v", srv)
	}
	if srv.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}

func TestSecurityHeadersUseNonceAndDisableCaching(t *testing.T) {
	w := httptest.NewRecorder()
	setSecurityHeaders(w, "test-nonce")

	h := w.Header()
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self' 'nonce-test-nonce'",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"upgrade-insecure-requests",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %q", want, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP permits arbitrary inline scripts: %q", csp)
	}
	if got := h.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
}

func TestLoginAndVendoredBrowserAssets(t *testing.T) {
	srv := New(":0", nil, nil, nil, nil, nil)
	handler := srv.Handler()

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}
	if got := login.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("login Cache-Control = %q", got)
	}
	if got := login.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self' 'nonce-") {
		t.Fatalf("login CSP missing nonce policy: %q", got)
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/static/htmx-1.9.12.min.js", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("HTMX status = %d", asset.Code)
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("HTMX Cache-Control = %q", got)
	}
	digest := sha256.Sum256(asset.Body.Bytes())
	if got := hex.EncodeToString(digest[:]); got != "449317ade7881e949510db614991e195c3a099c4c791c24dacec55f9f4a2a452" {
		t.Fatalf("HTMX SHA-256 = %s", got)
	}

	app := httptest.NewRecorder()
	handler.ServeHTTP(app, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	if app.Code != http.StatusOK {
		payload, _ := io.ReadAll(app.Result().Body)
		t.Fatalf("app.js status = %d body=%s", app.Code, payload)
	}
	if got := app.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("app.js Cache-Control = %q", got)
	}
}
