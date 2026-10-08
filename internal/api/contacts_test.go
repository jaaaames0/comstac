package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"comstac/internal/api"
	"comstac/internal/store"
)

func TestContactsRequireSessionAndCSRFAfterLogin(t *testing.T) {
	db := openTestDB(t)
	mgr := bootstrapAuth(t, db)
	c := store.Contact{Name: "Saved private contact", Email: "private@example.test"}
	if err := store.SaveContact(context.Background(), db, &c); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(":0", db, mgr, nil, nil, nil).Handler())
	defer srv.Close()
	anonymous := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/ui/contacts", "/ui/contacts/search?q=private", "/ui/contacts/edit?id=1"} {
		resp, err := anonymous.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), c.Email) {
			t.Fatalf("unauthorized contacts leaked: %d %s", resp.StatusCode, body)
		}
	}
	client := authedClient(t, srv.URL)
	for _, path := range []string{"/ui/contacts/save", "/ui/contacts/delete"} {
		resp, err := client.PostForm(srv.URL+path, url.Values{"id": {"1"}, "name": {"Changed"}, "email": {"changed@example.test"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("CSRF not required: %s %d", path, resp.StatusCode)
		}
	}
	base, _ := url.Parse(srv.URL)
	var csrf string
	for _, cookie := range client.Jar.Cookies(base) {
		if cookie.Name == "comstac_session" {
			csrf = mgr.CSRFToken(cookie.Value)
		}
	}
	resp, err := client.PostForm(srv.URL+"/ui/contacts/save", url.Values{"id": {"0"}, "name": {"New saved contact"}, "email": {"new@example.test"}, "_csrf": {csrf}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("authorized save", resp.StatusCode)
	}
	resp, err = client.Get(srv.URL + "/ui/contacts/search?q=new")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(body), "new@example.test") || strings.Contains(string(body), "private@example.test") {
		t.Fatalf("bounded private search: %d %s", resp.StatusCode, body)
	}
	resp, err = anonymous.Get(srv.URL + "/static/contacts.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatal("script unavailable")
	}
}
