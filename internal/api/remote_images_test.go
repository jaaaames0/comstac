package api_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"comstac/internal/api"
	authpkg "comstac/internal/auth"
	"comstac/internal/ingest"
)

func TestRemoteImagePreferencesRequireAuthAndCSRFAndMatchMailboxOnly(t *testing.T) {
	db := openTestDB(t)
	authMgr := bootstrapAuth(t, db)
	forgedID := ingestHTMLMessage(t, db, `"trusted@example.com" <attacker@example.net>`)
	trustedID := ingestHTMLMessage(t, db, `Trusted Sender <TRUSTED@EXAMPLE.COM>`)

	srv := api.New(":0", db, authMgr, nil, nil, nil)
	baseURL, stop := runAPIServer(t, srv)
	defer stop()

	unauthenticated := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := unauthenticated.Get(baseURL + "/ui/accounts")
	if err != nil {
		t.Fatalf("unauthenticated accounts request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("unauthenticated status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	unauthForm := url.Values{"action": {"remote_image_add"}, "email": {"trusted@example.com"}}
	resp, err = unauthenticated.Post(baseURL+"/ui/accounts", "application/x-www-form-urlencoded", strings.NewReader(unauthForm.Encode()))
	if err != nil {
		t.Fatalf("unauthenticated preference request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("unauthenticated preference status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	client := authedClient(t, baseURL)
	status := postRemoteImagePreference(t, client, baseURL, "remote_image_add", "trusted@example.com", false)
	if status != http.StatusForbidden {
		t.Fatalf("missing-CSRF status=%d", status)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM remote_image_senders`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("CSRF-rejected request changed the allowlist")
	}

	status = postRemoteImagePreference(t, client, baseURL, "remote_image_add", "Trusted@Example.COM", true)
	if status != http.StatusOK {
		t.Fatalf("add status=%d", status)
	}

	forged := getResponseBody(t, client, baseURL+"/ui/message?id="+itoa(forgedID))
	if !strings.Contains(forged, ">load images</button>") || strings.Contains(forged, "img-src data: https:;") {
		t.Fatal("forged display name caused automatic image loading")
	}

	trusted := getResponseBody(t, client, baseURL+"/ui/message?id="+itoa(trustedID))
	if strings.Contains(trusted, ">load images</button>") || !strings.Contains(trusted, "img-src data: https:;") {
		t.Fatal("approved exact mailbox did not load HTTPS images")
	}
	for _, policy := range []string{"script-src &#39;none&#39;", "form-action &#39;none&#39;", "connect-src &#39;none&#39;", "frame-src &#39;none&#39;"} {
		if !strings.Contains(trusted, policy) {
			t.Fatalf("allowlisted rendering lost policy %q", policy)
		}
	}
	for _, forbidden := range []string{"tracker.invalid/refresh", "allow-scripts", "allow-forms"} {
		if strings.Contains(trusted, forbidden) {
			t.Fatalf("allowlisted rendering retained forbidden capability %q", forbidden)
		}
	}
	if !strings.Contains(trusted, "tracker.invalid/pixel") || !strings.Contains(trusted, `sandbox="allow-popups allow-popups-to-escape-sandbox"`) {
		t.Fatal("allowlisted rendering lost the HTTPS image or iframe sandbox")
	}

	actionBody := postMessageAction(t, client, baseURL, trustedID)
	if strings.Contains(actionBody, ">load images</button>") || !strings.Contains(actionBody, "img-src data: https:;") {
		t.Fatal("message-action rendering bypassed the sender preference")
	}

	status = postRemoteImagePreference(t, client, baseURL, "remote_image_remove", "trusted@example.com", true)
	if status != http.StatusOK {
		t.Fatalf("remove status=%d", status)
	}
	afterRemove := getResponseBody(t, client, baseURL+"/ui/message?id="+itoa(trustedID))
	if !strings.Contains(afterRemove, ">load images</button>") || strings.Contains(afterRemove, "img-src data: https:;") {
		t.Fatal("removed mailbox still loaded images")
	}
}

func TestLoadImagesActionAllowlistsExactSenderMailbox(t *testing.T) {
	db := openTestDB(t)
	authMgr := bootstrapAuth(t, db)
	forgedID := ingestHTMLMessage(t, db, `"trusted@example.com" <Attacker@Example.NET>`)
	laterID := ingestHTMLMessage(t, db, `attacker@example.net`)
	otherID := ingestHTMLMessage(t, db, `trusted@example.com`)
	invalidID := ingestHTMLMessage(t, db, `not a mailbox`)

	srv := api.New(":0", db, authMgr, nil, nil, nil)
	baseURL, stop := runAPIServer(t, srv)
	defer stop()
	client := authedClient(t, baseURL)

	form := url.Values{"id": {itoa(forgedID)}, "action": {"allow_images"}}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/ui/message/actions", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing-CSRF status=%d", resp.StatusCode)
	}
	if senders := remoteImageSenders(t, db); len(senders) != 0 {
		t.Fatalf("CSRF-rejected action changed the allowlist: %v", senders)
	}

	body := postMessageActionValues(t, client, baseURL, forgedID, "allow_images", "")
	if strings.Contains(body, ">load images</button>") || !strings.Contains(body, "img-src data: https:;") {
		t.Fatal("load images action did not render remote images")
	}
	if senders := remoteImageSenders(t, db); len(senders) != 1 || senders[0] != "attacker@example.net" {
		t.Fatalf("allowlist = %v, want only the exact From mailbox", senders)
	}
	later := getResponseBody(t, client, baseURL+"/ui/message?id="+itoa(laterID))
	if strings.Contains(later, ">load images</button>") || !strings.Contains(later, "img-src data: https:;") {
		t.Fatal("later message from the allowlisted mailbox did not load images")
	}
	other := getResponseBody(t, client, baseURL+"/ui/message?id="+itoa(otherID))
	if !strings.Contains(other, ">load images</button>") || strings.Contains(other, "img-src data: https:;") {
		t.Fatal("display-name address was allowlisted")
	}

	body = postMessageActionValues(t, client, baseURL, invalidID, "allow_images", "")
	if !strings.Contains(body, "img-src data: https:;") {
		t.Fatal("unparseable From did not load images once")
	}
	if senders := remoteImageSenders(t, db); len(senders) != 1 {
		t.Fatalf("unparseable From changed the allowlist: %v", senders)
	}
}

func remoteImageSenders(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT email_address FROM remote_image_senders ORDER BY email_address`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func postMessageAction(t *testing.T, client *http.Client, baseURL string, id int64) string {
	t.Helper()
	return postMessageActionValues(t, client, baseURL, id, "read", "1")
}

func postMessageActionValues(t *testing.T, client *http.Client, baseURL string, id int64, action, value string) string {
	t.Helper()
	form := url.Values{"id": {itoa(id)}, "action": {action}, "value": {value}}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/ui/message/actions", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setCSRFHeader(client, req)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message action status=%d", resp.StatusCode)
	}
	return string(body)
}

func ingestHTMLMessage(t *testing.T, db *sql.DB, from string) int64 {
	t.Helper()
	raw := strings.Join([]string{
		"From: " + from,
		"To: local@example.com",
		"Subject: remote image preference test",
		"MIME-Version: 1.0",
		`Content-Type: text/html; charset="utf-8"`,
		"",
		`<meta http-equiv="refresh" content="0;url=https://tracker.invalid/refresh"><form action="https://tracker.invalid/form"><img src="https://tracker.invalid/pixel"><script src="https://tracker.invalid/script.js"></script></form>`,
	}, "\r\n")
	if err := ingest.NewService(db).IngestRaw(context.Background(), ingest.IngestInput{
		Source:       ingest.SourceSMTP,
		EnvelopeFrom: "sender@shadow.invalid",
		EnvelopeTo:   []string{"local@example.com"},
		RawMIME:      []byte(raw),
	}); err != nil {
		t.Fatalf("ingest HTML message: %v", err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM messages ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("query ingested message: %v", err)
	}
	return id
}

func postRemoteImagePreference(t *testing.T, client *http.Client, baseURL, action, address string, withCSRF bool) int {
	t.Helper()
	form := url.Values{"action": {action}, "email": {address}}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/ui/accounts", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withCSRF {
		setCSRFHeader(client, req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func setCSRFHeader(client *http.Client, req *http.Request) {
	for _, cookie := range client.Jar.Cookies(req.URL) {
		if cookie.Name == "comstac_session" {
			mgr := authpkg.NewManager(nil, "", time.Hour, "")
			req.Header.Set("X-CSRF-Token", mgr.CSRFToken(cookie.Value))
			return
		}
	}
}

func getResponseBody(t *testing.T, client *http.Client, endpoint string) string {
	t.Helper()
	resp, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", endpoint, resp.StatusCode)
	}
	return string(body)
}
