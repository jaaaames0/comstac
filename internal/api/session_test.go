package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"comstac/internal/api"
)

func TestSessionCookieRenewsWithUseAndLogoutEverywhereRevokesAll(t *testing.T) {
	db := openTestDB(t)
	srv := httptest.NewTLSServer(api.New(":0", db, bootstrapAuth(t, db), nil, nil, nil).Handler())
	defer srv.Close()

	login := func() *http.Client {
		client := srv.Client()
		client = &http.Client{Transport: client.Transport, Jar: mustCookieJar(t),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Post(srv.URL+"/api/login", "application/json", bytes.NewBufferString(`{"username":"admin","password":"password123"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("login = %d", resp.StatusCode)
		}
		return client
	}
	do := func(client *http.Client, method, path, csrf string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
			req.Header.Set("HX-Request", "true")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	sessionCookie := func(resp *http.Response) *http.Cookie {
		for _, c := range resp.Cookies() {
			if c.Name == "comstac_session" {
				return c
			}
		}
		return nil
	}

	phone, laptop := login(), login()
	if resp := do(phone, "GET", "/api/messages", ""); resp.StatusCode != http.StatusOK || sessionCookie(resp) != nil {
		t.Fatalf("fresh session request = %d, cookie %v", resp.StatusCode, sessionCookie(resp))
	}

	// Simulate both sessions being idle for half the (one hour) idle timeout.
	if _, err := db.Exec(`UPDATE sessions SET expires_at_unix = ?`, time.Now().Add(30*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	resp := do(phone, "GET", "/api/messages", "")
	renewed := sessionCookie(resp)
	if resp.StatusCode != http.StatusOK || renewed == nil || !renewed.HttpOnly || !renewed.Secure {
		t.Fatalf("idle session request = %d, cookie %+v", resp.StatusCode, renewed)
	}
	if until := time.Until(renewed.Expires); until < 59*time.Minute || until > time.Hour {
		t.Fatalf("renewed cookie expires in %v", until)
	}

	var token struct {
		CSRFToken string `json:"csrf_token"`
	}
	tokenResp, err := phone.Get(srv.URL + "/api/push/action-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&token); err != nil || token.CSRFToken == "" {
		t.Fatalf("csrf token: %v", err)
	}
	tokenResp.Body.Close()

	if resp := do(phone, "POST", "/logout/all", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("logout everywhere without CSRF = %d", resp.StatusCode)
	}
	if resp := do(laptop, "GET", "/api/messages", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("CSRF-less request revoked sessions: %d", resp.StatusCode)
	}
	resp = do(phone, "POST", "/logout/all", token.CSRFToken)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != "/login" {
		t.Fatalf("logout everywhere = %d redirect %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	for name, client := range map[string]*http.Client{"phone": phone, "laptop": laptop} {
		if resp := do(client, "GET", "/api/messages", ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s session after logout everywhere = %d", name, resp.StatusCode)
		}
	}
}
