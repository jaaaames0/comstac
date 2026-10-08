package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/ingest"
	"comstac/internal/relay"
	"comstac/internal/store"
)

func TestContactFormsPreserveInputEscapeNamesAndSaveExplicitly(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{Source: ingest.SourceSMTP, RawMIME: []byte("Subject: Contact fixture\r\n\r\nBody")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET from_addr='"Doe, José" <jose@example.test>'`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerRoutes(mux, db, &fakeSender{}, nil)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(w, r)
		return w
	}
	w := get("/ui/contacts/edit?message=1")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Doe, José") || !strings.Contains(w.Body.String(), "jose@example.test") {
		t.Fatal("sender prefill", w.Body.String())
	}
	items, _ := store.SearchContacts(ctx, db, "", 10, 0)
	if len(items) != 0 {
		t.Fatal("sender auto-saved")
	}
	form := url.Values{"id": {"0"}, "name": {"<script>alert(1)</script>"}, "email": {"bad"}}
	w = post("/ui/contacts/save", form)
	if !strings.Contains(w.Body.String(), "&lt;script&gt;") || strings.Contains(w.Body.String(), "<script>alert") || !strings.Contains(w.Body.String(), `value="bad"`) {
		t.Fatal("validation input lost or unsafe", w.Body.String())
	}
	form.Set("email", "jose@example.test")
	form.Set("name", "Doe, José")
	w = post("/ui/contacts/save", form)
	if !strings.Contains(w.Body.String(), "Contact saved.") {
		t.Fatal(w.Body.String())
	}
	w = post("/ui/contacts/save", form)
	if !strings.Contains(w.Body.String(), "already saved") || !strings.Contains(w.Body.String(), "Doe, José") {
		t.Fatal("duplicate input lost", w.Body.String())
	}
	w = get("/ui/contacts/search?q=José")
	var suggestions []struct{ Recipient string }
	if err := json.Unmarshal(w.Body.Bytes(), &suggestions); err != nil || len(suggestions) != 1 {
		t.Fatalf("suggestions: %s %v", w.Body.String(), err)
	}
	if recipients, err := relay.ParseRecipients(suggestions[0].Recipient); err != nil || len(recipients) != 1 {
		t.Fatalf("quoted autocomplete broke: %v %v", recipients, err)
	}
	form.Set("id", "1")
	form.Set("name", "Updated")
	post("/ui/contacts/save", form)
	c, err := store.GetContact(ctx, db, 1)
	if err != nil || c.Name != "Updated" {
		t.Fatal("edit failed", err)
	}
	w = post("/ui/contacts/delete", url.Values{"id": {"1"}})
	if !strings.Contains(w.Body.String(), "Contact deleted.") {
		t.Fatal(w.Body.String())
	}
	if w := get("/ui/contacts?page=-1"); w.Code != 400 {
		t.Fatal("bad page")
	}
	if w := get("/ui/contacts/search?q=" + strings.Repeat("x", 129)); w.Code != 400 {
		t.Fatal("bad query")
	}
}
