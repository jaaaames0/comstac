package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/api"
)

func TestACMEChallengeServesOnlyBoundedRegularTokenFiles(t *testing.T) {
	dir := t.TempDir()
	token := "abc_DEF-123"
	payload := "challenge-token.thumbprint"
	if err := os.WriteFile(filepath.Join(dir, token), []byte(payload), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oversized"), []byte(strings.Repeat("x", 4097)), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, token), filepath.Join(dir, "symlink")); err != nil {
		t.Fatal(err)
	}

	srv := api.New(":0", nil, nil, nil, nil, nil)
	srv.SetACMEChallengeDir(dir)
	handler := srv.Handler()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "http://mail.example.com/.well-known/acme-challenge/"+token, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d", method, res.Code)
		}
		if got := res.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := res.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if method == http.MethodGet && res.Body.String() != payload {
			t.Fatalf("GET body = %q", res.Body.String())
		}
		if method == http.MethodHead && res.Body.Len() != 0 {
			t.Fatalf("HEAD body length = %d", res.Body.Len())
		}
	}

	for _, name := range []string{"missing", "symlink", "oversized", "../escape", "bad.token"} {
		req := httptest.NewRequest(http.MethodGet, "http://mail.example.com/", nil)
		req.URL.Path = "/.well-known/acme-challenge/" + name
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code == http.StatusOK {
			t.Fatalf("invalid token %q returned 200", name)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "http://mail.example.com/.well-known/acme-challenge/"+token, nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", res.Code)
	}
}

func TestValidateACMEChallengeDirDoesNotLeakPath(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "do-not-log-acme-path")
	err := api.ValidateACMEChallengeDir(secretPath)
	if err == nil {
		t.Fatal("ValidateACMEChallengeDir accepted missing directory")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Fatalf("error leaked configured path: %v", err)
	}
}

func TestACMEChallengeRejectsTraversalWithoutCanonicalRedirect(t *testing.T) {
	dir := t.TempDir()
	srv := api.New(":0", nil, nil, nil, nil, nil)
	srv.SetACMEChallengeDir(dir)
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(httpServer.URL + "/.well-known/acme-challenge/../secret")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal status = %d, want 404 without redirect", response.StatusCode)
	}
}
