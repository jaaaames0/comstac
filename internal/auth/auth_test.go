package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCookieAttributesMatchWhenSetAndCleared(t *testing.T) {
	mgr := NewManager(nil, "", time.Hour, "test-secret")

	setRecorder := httptest.NewRecorder()
	mgr.SetSessionCookie(setRecorder, "token", time.Now().Add(time.Hour))
	setCookie := onlyCookie(t, setRecorder)
	assertSessionCookieBoundary(t, setCookie)
	if setCookie.MaxAge < 0 || setCookie.Value != "token" {
		t.Fatalf("unexpected set cookie: %+v", setCookie)
	}

	clearRecorder := httptest.NewRecorder()
	mgr.ClearSessionCookie(clearRecorder)
	clearCookie := onlyCookie(t, clearRecorder)
	assertSessionCookieBoundary(t, clearCookie)
	if clearCookie.MaxAge != -1 || clearCookie.Value != "" {
		t.Fatalf("unexpected clear cookie: %+v", clearCookie)
	}
}

func onlyCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	return cookies[0]
}

func assertSessionCookieBoundary(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("weak cookie boundary: %+v", cookie)
	}
}
