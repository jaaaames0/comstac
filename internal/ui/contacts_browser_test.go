package ui

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/relay"
	"comstac/internal/store"
)

type previewSender struct {
	mu      sync.Mutex
	Calls   int
	Message relay.OutboundMessage
}

func (s *previewSender) From() string { return "owner@example.test" }
func (s *previewSender) Send(_ context.Context, m relay.OutboundMessage) (string, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	s.Message = m
	return "fixture@example.test", []byte("Subject: Fixture\r\n\r\nSynthetic send"), nil
}

// Optional loopback fixture for the browser check. It has disposable synthetic
// mail/contacts, a fake sender and no production configuration or credentials.
func TestContactBrowserFixture(t *testing.T) {
	infoPath := os.Getenv("COMSTAC_CONTACT_PREVIEW_INFO")
	if infoPath == "" {
		t.Skip("optional browser fixture")
	}
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range []store.Contact{{Name: "Doe, Jane", Email: "jane@example.test"}, {Name: "Alex Example", Email: "alex@example.test"}, {Name: "<img src=x onerror=alert(1)>", Email: "escaped@example.test"}} {
		if err := store.SaveContact(ctx, db, &c); err != nil {
			t.Fatal(err)
		}
	}
	if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{Source: ingest.SourceSMTP, RawMIME: []byte("Subject: Sender fixture\r\n\r\nHello fixture")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET from_addr='"Saved Sender" <sender@example.test>'`); err != nil {
		t.Fatal(err)
	}
	sender := &previewSender{}
	mux := http.NewServeMux()
	registerRoutes(mux, db, sender, nil)
	static, err := fs.Sub(StaticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /static/htmx-1.9.12.min.js", func(w http.ResponseWriter, r *http.Request) {
		data, _ := StaticFS.ReadFile("static/htmx-1.9.12.min.js")
		if len(data) > 0 && data[len(data)-1] == '\n' {
			data = data[:len(data)-1]
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(data)
	})
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		data, _ := StaticFS.ReadFile("static/sw.js")
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(data)
	})
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		data, _ := StaticFS.ReadFile("static/manifest.json")
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})
	done := make(chan struct{})
	var once sync.Once
	mux.HandleFunc("POST /preview/stop", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(done) }) })
	mux.HandleFunc("GET /preview/sends", func(w http.ResponseWriter, r *http.Request) {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		json.NewEncoder(w).Encode(sender)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path != "/preview/stop" && r.Header.Get("X-CSRF-Token") != "fixture-csrf" {
			http.Error(w, "invalid CSRF", 403)
			return
		}
		mux.ServeHTTP(w, r.WithContext(WithCSRFToken(r.Context(), "fixture-csrf")))
	}))
	defer srv.Close()
	data, _ := json.Marshal(map[string]string{"url": srv.URL})
	if err := os.WriteFile(infoPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(7 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
}
