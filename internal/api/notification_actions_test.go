package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"comstac/internal/api"
	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestNotificationActionsRequireSessionAndCSRF(t *testing.T) {
	db := openTestDB(t)
	srv := httptest.NewTLSServer(api.New(":0", db, bootstrapAuth(t, db), nil, nil, nil).Handler())
	defer srv.Close()
	client := srv.Client()
	client.Jar = mustCookieJar(t)
	request := func(method, path, body, token string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, data)
		}
		if path == "/api/push/action-token" && resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("CSRF response must not be cached")
		}
		return data
	}
	request("GET", "/api/push/action-token", "", "", 401)
	request("POST", "/api/push/action", `{"action":"trash","message_id":1}`, "", 401)
	request("POST", "/api/login", `{"username":"admin","password":"password123"}`, "", 200)
	var token struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(request("GET", "/api/push/action-token", "", "", 200), &token); err != nil || token.CSRFToken == "" {
		t.Fatalf("CSRF token = %+v (%v)", token, err)
	}
	request("POST", "/api/push/action", `{"action":"trash","message_id":1}`, "", 403)
	request("POST", "/api/push/action", `{"action":"trash","message_id":1}`, "wrong", 403)
	request("GET", "/api/push/action", "", "", 405)
	request("POST", "/api/push/action", `{"action":"delete_everything"}`, token.CSRFToken, 400)
	request("POST", "/api/push/action", `{"action":"read","message_id":9999}`, token.CSRFToken, 404)

	ctx := context.Background()
	if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{
		Source: ingest.SourceIMAP, EnvelopeFrom: "a@example.com", EnvelopeTo: []string{"b@example.com"},
		RawMIME: []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: Notification\r\n\r\nHello"),
	}); err != nil {
		t.Fatal(err)
	}
	var id int64
	_ = db.QueryRow(`SELECT id FROM messages`).Scan(&id)
	action := func(name string) {
		t.Helper()
		request("POST", "/api/push/action", fmt.Sprintf(`{"action":%q,"message_id":%d}`, name, id), token.CSRFToken, 204)
	}
	action("read")
	action("read")
	action("trash")
	_, _ = db.Exec(`UPDATE messages SET archived_at = '2020-01-01 00:00:00' WHERE id = ?`, id)
	action("trash")
	var archivedAt string
	_ = db.QueryRow(`SELECT archived_at FROM messages WHERE id = ?`, id).Scan(&archivedAt)
	if archivedAt != "2020-01-01 00:00:00" {
		t.Fatalf("retry reset trash retention: %s", archivedAt)
	}
	_, _ = store.SetMessageArchived(ctx, db, id, false)
	action("snooze_mail")
	detail, _ := store.GetMessageDetail(ctx, db, id)
	snooze := detail.SnoozeUntil
	action("snooze_mail")
	detail, _ = store.GetMessageDetail(ctx, db, id)
	until, _ := time.Parse(time.RFC3339, detail.SnoozeUntil)
	if !detail.Read || detail.SnoozeUntil != snooze || time.Until(until) < 59*time.Minute || time.Until(until) > time.Hour {
		t.Fatalf("message actions = %+v", detail)
	}
	var jobs int
	_ = db.QueryRow(`SELECT count(*) FROM sync_jobs WHERE message_id = ?`, id).Scan(&jobs)
	if jobs != 3 {
		t.Fatalf("sync jobs = %d, want read/trash/snooze exactly once", jobs)
	}
	// A failed sync enqueue rolls local state back too.
	_, _ = db.Exec(`UPDATE messages SET read = 0 WHERE id = ?`, id)
	_, _ = db.Exec(`CREATE TRIGGER fail_notification_sync BEFORE INSERT ON sync_jobs BEGIN SELECT RAISE(ABORT, 'sync unavailable'); END`)
	request("POST", "/api/push/action", fmt.Sprintf(`{"action":"read","message_id":%d}`, id), token.CSRFToken, 500)
	detail, _ = store.GetMessageDetail(ctx, db, id)
	if detail.Read {
		t.Fatal("failed action changed local state")
	}
	_, _ = db.Exec(`UPDATE sessions SET expires_at_unix = 0`)
	request("GET", "/api/push/action-token", "", "", 401)
	request("POST", "/api/push/action", fmt.Sprintf(`{"action":"trash","message_id":%d}`, id), token.CSRFToken, 401)
}

func TestCalendarNotificationSnoozeAction(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	srv := httptest.NewTLSServer(api.New(":0", db, bootstrapAuth(t, db), nil, nil, nil).Handler())
	defer srv.Close()
	client := srv.Client()
	client.Jar = mustCookieJar(t)
	resp, err := client.Post(srv.URL+"/api/login", "application/json", bytes.NewBufferString(`{"username":"admin","password":"password123"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	start := time.Now().Add(time.Hour).UTC().Truncate(time.Minute)
	ev := &store.CalendarEvent{Title: "Meeting", StartLocal: start.Format("2006-01-02T15:04"), TZ: "UTC", RRule: "FREQ=WEEKLY"}
	id, err := store.SaveCalendarEvent(ctx, db, ev, []int{60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCalendarReminder(ctx, db, id, start, 60); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"action":"snooze_event","event_id":%d,"occurrence":%q,"source_key":"offset:60"}`, id, start.Format(time.RFC3339))
	postJSONExpect(t, client, srv.URL+"/api/push/action", body, 204)
	postJSONExpect(t, client, srv.URL+"/api/push/action", body, 204)
	var n int
	var due int64
	if err := db.QueryRow(`SELECT count(*), max(due_at_unix) FROM calendar_notification_snoozes`).Scan(&n, &due); err != nil {
		t.Fatal(err)
	}
	if n != 1 || time.Until(time.Unix(due, 0)) < 9*time.Minute || time.Until(time.Unix(due, 0)) > 10*time.Minute {
		t.Fatalf("snooze count=%d due=%d", n, due)
	}
	got, _ := store.GetCalendarEvent(ctx, db, id)
	if got.StartLocal != ev.StartLocal || got.RRule != ev.RRule || len(got.Reminders) != 1 {
		t.Fatalf("snoozing changed event: %+v", got)
	}
	postJSONExpect(t, client, srv.URL+"/api/push/action", fmt.Sprintf(`{"action":"snooze_event","event_id":%d,"occurrence":%q,"source_key":"offset:15"}`, id, start.Format(time.RFC3339)), 404)
	postJSONExpect(t, client, srv.URL+"/api/push/action", fmt.Sprintf(`{"action":"snooze_event","event_id":%d,"occurrence":%q,"source_key":"offset:60"}`, id, start.Add(time.Minute).Format(time.RFC3339)), 404)
	if err := store.AddCalendarExDate(ctx, db, id, start.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	postJSONExpect(t, client, srv.URL+"/api/push/action", body, 404)
}
