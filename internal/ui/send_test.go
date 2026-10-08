package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/relay"
	"comstac/internal/store"
)

type fakeSender struct {
	calls     int
	afterSend func()
	err       error
	message   relay.OutboundMessage
}

func TestUnknownDeliveryDoesNotRenderSendFailureOrRetry(t *testing.T) {
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "uncertain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sender := &fakeSender{err: fmt.Errorf("%w: disconnected", relay.ErrDeliveryUnknown)}
	mux := http.NewServeMux()
	registerRoutes(mux, db, sender, nil)
	req := httptest.NewRequest(http.MethodPost, "/ui/compose", strings.NewReader("to=recipient%40example.test&body=Body"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if sender.calls != 1 || !strings.Contains(response.Body.String(), "delivery unconfirmed") || strings.Contains(response.Body.String(), "send failed") {
		t.Fatalf("ambiguous response=%s calls=%d", response.Body.String(), sender.calls)
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ui/sent", nil))
	if !strings.Contains(response.Body.String(), ">unconfirmed<") || strings.Contains(response.Body.String(), ">failed<") {
		t.Fatalf("sent history hides uncertainty: %s", response.Body.String())
	}
}

func (s *fakeSender) From() string { return "owner@example.test" }
func (s *fakeSender) Send(_ context.Context, message relay.OutboundMessage) (string, []byte, error) {
	s.calls++
	s.message = message
	if s.afterSend != nil {
		s.afterSend()
	}
	return "fixture@example.test", []byte("Subject: fixture\r\n\r\nbody"), s.err
}

func TestAcceptedSendSurvivesRequestCancellation(t *testing.T) {
	for _, route := range []string{"/ui/compose", "/ui/reply"} {
		t.Run(route, func(t *testing.T) {
			db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "send.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender := &fakeSender{afterSend: cancel}
			mux := http.NewServeMux()
			registerRoutes(mux, db, sender, nil)
			form := url.Values{"to": {"recipient@example.test"}, "subject": {"Fixture"}, "body": {"Body"}}
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(form.Encode())).WithContext(ctx)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM outbound_messages WHERE status='sent'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("known acceptance missing from history: %d %v", count, err)
			}
			if sender.calls != 1 || !strings.Contains(response.Body.String(), "message sent") {
				t.Fatalf("calls=%d response=%s", sender.calls, response.Body.String())
			}
		})
	}
}

func TestAcceptedSendHistoryFailureDoesNotInviteResend(t *testing.T) {
	for _, route := range []string{"/ui/compose", "/ui/reply"} {
		t.Run(route, func(t *testing.T) {
			db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "send.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`CREATE TRIGGER fail_history BEFORE INSERT ON outbound_messages BEGIN SELECT RAISE(FAIL,'injected failure'); END`); err != nil {
				t.Fatal(err)
			}
			sender := &fakeSender{}
			mux := http.NewServeMux()
			registerRoutes(mux, db, sender, nil)
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader("to=recipient%40example.test&subject=Fixture&body=Body"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			body := response.Body.String()
			if sender.calls != 1 || !strings.Contains(body, "message sent") || !strings.Contains(body, "history") || strings.Contains(body, "send failed") {
				t.Fatalf("calls=%d response=%s", sender.calls, body)
			}
		})
	}
}
