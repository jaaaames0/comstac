package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"testing"
	"time"

	"comstac/internal/api"
	authpkg "comstac/internal/auth"
	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	authMgr := bootstrapAuth(t, db)
	srv := api.New(":0", db, authMgr, nil, nil)
	baseURL, stop := runAPIServer(t, srv)
	defer stop()

	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(body))
	}
	if string(body) != "{\"ok\":true}" {
		t.Fatalf("unexpected body: %q", string(body))
	}
}

func TestProtectedAPIRequiresLogin(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	authMgr := bootstrapAuth(t, db)
	srv := api.New(":0", db, authMgr, nil, nil)
	baseURL, stop := runAPIServer(t, srv)
	defer stop()

	resp, err := http.Get(baseURL + "/api/messages")
	if err != nil {
		t.Fatalf("get api/messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 got %d body=%s", resp.StatusCode, string(body))
	}
}

func TestListMessagesFiltersPaginationAndDetail(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	seedMessages(t, db)
	authMgr := bootstrapAuth(t, db)

	srv := api.New(":0", db, authMgr, nil, nil)
	baseURL, stop := runAPIServer(t, srv)
	defer stop()

	client := authedClient(t, baseURL)

	page1 := fetchMessages(t, client, baseURL+"/api/messages?limit=1")
	if len(page1.Items) != 1 {
		t.Fatalf("expected 1 item got %d", len(page1.Items))
	}
	if page1.NextBeforeID == 0 {
		t.Fatalf("expected next_before_id")
	}

	page2 := fetchMessages(t, client, baseURL+"/api/messages?limit=2&before_id="+itoa(page1.NextBeforeID))
	if len(page2.Items) == 0 {
		t.Fatalf("expected older messages with before_id")
	}
	if page2.Items[0].ID >= page1.Items[0].ID {
		t.Fatalf("expected older IDs, got %d then %d", page1.Items[0].ID, page2.Items[0].ID)
	}

	unread := fetchMessages(t, client, baseURL+"/api/messages?limit=10&unread=1")
	for _, m := range unread.Items {
		if m.Read {
			t.Fatalf("found read message in unread filter")
		}
	}

	archived := fetchMessages(t, client, baseURL+"/api/messages?limit=10&archived=1")
	if len(archived.Items) == 0 {
		t.Fatalf("expected archived messages")
	}
	for _, m := range archived.Items {
		if !m.Archived {
			t.Fatalf("found active message in archived filter")
		}
	}

	smtpOnly := fetchMessages(t, client, baseURL+"/api/messages?limit=10&source=smtp")
	if len(smtpOnly.Items) == 0 {
		t.Fatalf("expected smtp messages")
	}
	for _, m := range smtpOnly.Items {
		if m.Source != "smtp" {
			t.Fatalf("unexpected source %q", m.Source)
		}
	}

	messageID := page1.Items[0].ID
	resp, err := client.Get(baseURL + "/api/messages/" + itoa(messageID))
	if err != nil {
		t.Fatalf("get message detail: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("detail status=%d body=%s", resp.StatusCode, string(body))
	}
	var detail struct {
		ID       int64  `json:"id"`
		Subject  string `json:"subject"`
		BodyText string `json:"body_text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != messageID {
		t.Fatalf("unexpected detail id %d", detail.ID)
	}
	if detail.Subject == "" || detail.BodyText == "" {
		t.Fatalf("expected subject and body in detail")
	}

	postJSON(t, client, baseURL+"/api/messages/"+itoa(messageID)+"/archive", `{"archived":true}`)
	archivedCheck := fetchMessages(t, client, baseURL+"/api/messages?limit=10&archived=1")
	foundArchived := false
	for _, m := range archivedCheck.Items {
		if m.ID == messageID {
			foundArchived = true
			break
		}
	}
	if !foundArchived {
		t.Fatalf("expected message %d in archived list after action", messageID)
	}

	postJSON(t, client, baseURL+"/api/messages/"+itoa(messageID)+"/read", `{"read":false}`)
	unreadCheck := fetchMessages(t, client, baseURL+"/api/messages?limit=10&unread=1")
	foundUnread := false
	for _, m := range unreadCheck.Items {
		if m.ID == messageID {
			foundUnread = true
			break
		}
	}
	if !foundUnread {
		t.Fatalf("expected message %d in unread list after action", messageID)
	}

	postJSON(t, client, baseURL+"/api/messages/"+itoa(messageID)+"/snooze", `{"until":"2030-01-01T00:00:00Z"}`)

	imapMessages := fetchMessages(t, client, baseURL+"/api/messages?limit=1&source=imap")
	if len(imapMessages.Items) == 0 {
		t.Fatalf("expected at least one imap message")
	}
	imapID := imapMessages.Items[0].ID
	postJSON(t, client, baseURL+"/api/messages/"+itoa(imapID)+"/archive", `{"archived":true}`)

	var syncCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_jobs WHERE message_id = ? AND action = 'archive'`, imapID).Scan(&syncCount); err != nil {
		t.Fatalf("query sync jobs: %v", err)
	}
	if syncCount == 0 {
		t.Fatalf("expected sync job for imap archive action")
	}
}

func TestAccountsListAndCreateLocal(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	authMgr := bootstrapAuth(t, db)
	srv := api.New(":0", db, authMgr, nil, nil)
	baseURL, stop := runAPIServer(t, srv)
	defer stop()

	client := authedClient(t, baseURL)

	resp, err := client.Get(baseURL + "/api/accounts?kind=local")
	if err != nil {
		t.Fatalf("get accounts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("accounts list status=%d body=%s", resp.StatusCode, string(body))
	}

	postJSONExpect(t, client, baseURL+"/api/accounts", `{"kind":"local","email_address":"local2@example.com","name":"Local Two"}`, http.StatusCreated)
	postJSONExpect(t, client, baseURL+"/api/accounts", `{"kind":"local","email_address":"local2@example.com"}`, http.StatusConflict)

	resp2, err := client.Get(baseURL + "/api/accounts?kind=local")
	if err != nil {
		t.Fatalf("get accounts after create: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("accounts list status=%d body=%s", resp2.StatusCode, string(body))
	}
	var out struct {
		Items []struct {
			EmailAddress string `json:"email_address"`
			Kind         string `json:"kind"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode accounts list: %v", err)
	}
	found := false
	for _, item := range out.Items {
		if item.EmailAddress == "local2@example.com" && item.Kind == "local" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected created local account in list")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "api.db")
	db, err := store.OpenAndMigrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func bootstrapAuth(t *testing.T, db *sql.DB) *authpkg.Manager {
	t.Helper()
	mgr := authpkg.NewManager(db, "", time.Hour, "")
	if err := mgr.BootstrapUser(context.Background(), "admin", "password123"); err != nil {
		t.Fatalf("bootstrap auth: %v", err)
	}
	return mgr
}

func seedMessages(t *testing.T, db *sql.DB) {
	t.Helper()
	ingestor := ingest.NewService(db)

	payloads := []struct {
		subject string
		source  ingest.Source
		body    string
	}{
		{subject: "Newest", source: ingest.SourceSMTP, body: "new body"},
		{subject: "Middle", source: ingest.SourceIMAP, body: "middle body"},
		{subject: "Oldest", source: ingest.SourceSMTP, body: "old body"},
	}

	for _, p := range payloads {
		raw := []byte("Subject: " + p.subject + "\r\nFrom: sender@example.com\r\nTo: local@example.com\r\n\r\n" + p.body + "\r\n")
		if err := ingestor.IngestRaw(context.Background(), ingest.IngestInput{
			Source:       p.source,
			EnvelopeFrom: "sender@example.com",
			EnvelopeTo:   []string{"local@example.com"},
			RawMIME:      raw,
		}); err != nil {
			t.Fatalf("ingest %s: %v", p.subject, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := db.Exec(`UPDATE messages SET read=1 WHERE subject='Middle'`); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if _, err := db.Exec(`UPDATE messages SET archived=1 WHERE subject='Oldest'`); err != nil {
		t.Fatalf("mark archived: %v", err)
	}
}

func runAPIServer(t *testing.T, srv *api.Server) (baseURL string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.RunWithListener(ctx, ln) }()

	waitHTTPReady(t, "http://"+ln.Addr().String()+"/healthz")

	return "http://" + ln.Addr().String(), func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				t.Fatalf("api shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("api did not stop")
		}
		_ = ln.Close()
	}
}

func authedClient(t *testing.T, baseURL string) *http.Client {
	t.Helper()
	jar := mustCookieJar(t)
	client := &http.Client{Jar: jar}

	payload := []byte(`{"username":"admin","password":"password123"}`)
	resp, err := client.Post(baseURL+"/api/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status=%d body=%s", resp.StatusCode, string(body))
	}
	return client
}

func mustCookieJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return jar
}

func waitHTTPReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("endpoint not ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func fetchMessages(t *testing.T, client *http.Client, url string) messagesResponse {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var out messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func postJSON(t *testing.T, client *http.Client, url string, body string) {
	t.Helper()
	postJSONExpect(t, client, url, body, http.StatusOK)
}

func postJSONExpect(t *testing.T, client *http.Client, url string, body string, status int) {
	t.Helper()
	resp, err := client.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("post %s status=%d want=%d body=%s", url, resp.StatusCode, status, string(payload))
	}
}

func itoa(v int64) string { return fmt.Sprintf("%d", v) }

type messagesResponse struct {
	Items []struct {
		ID       int64  `json:"id"`
		Source   string `json:"source"`
		Subject  string `json:"subject"`
		Read     bool   `json:"read"`
		Archived bool   `json:"archived"`
	} `json:"items"`
	NextBeforeID int64 `json:"next_before_id"`
}
