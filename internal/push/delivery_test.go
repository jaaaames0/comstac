package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"comstac/internal/store"
)

type pushRecorder struct {
	mu      sync.Mutex
	headers []http.Header
	status  int
}

func (p *pushRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.headers = append(p.headers, r.Header.Clone())
	w.WriteHeader(p.status)
}

func newTestNotifier(t *testing.T) *Notifier {
	t.Helper()
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(db, pub, priv, "mailto:test@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func addSubscription(t *testing.T, n *Notifier, endpoint string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	if err := store.SavePushSubscription(context.Background(), n.db, endpoint,
		base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(auth)); err != nil {
		t.Fatal(err)
	}
}

func TestSendSetsDurableTTLAndHighUrgencyAndLogsDelivery(t *testing.T) {
	rec := &pushRecorder{status: http.StatusCreated}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	n := newTestNotifier(t)
	addSubscription(t, n, srv.URL+"/push/secret-capability-token")

	n.SendNewMail(context.Background(), "hello", "a@example.com", 7)
	n.SendReminder(context.Background(), "later", "a@example.com", 7)
	if accepted, total := n.SendTest(context.Background()); accepted != 1 || total != 1 {
		t.Fatalf("test send = %d/%d, want 1/1", accepted, total)
	}

	if len(rec.headers) != 3 {
		t.Fatalf("push service saw %d requests, want 3", len(rec.headers))
	}
	for i, wantTTL := range []string{"86400", "86400", "300"} {
		if got := rec.headers[i].Get("TTL"); got != wantTTL {
			t.Fatalf("request %d TTL = %q, want %q", i, got, wantTTL)
		}
		if got := rec.headers[i].Get("Urgency"); got != "high" {
			t.Fatalf("request %d Urgency = %q, want high", i, got)
		}
	}

	log, err := store.ListPushDeliveries(context.Background(), n.db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 || log[0].Kind != "test" || log[1].Kind != "reminder" || log[2].Kind != "mail" || log[0].Status != 201 {
		t.Fatalf("delivery log = %+v", log)
	}
	if strings.Contains(log[0].EndpointHost, "secret") || !strings.HasPrefix(log[0].EndpointHost, "127.0.0.1:") {
		t.Fatalf("endpoint host = %q, want host only", log[0].EndpointHost)
	}
}

func TestSendRemovesNotFoundSubscription(t *testing.T) {
	srv := httptest.NewServer(&pushRecorder{status: http.StatusNotFound})
	defer srv.Close()
	n := newTestNotifier(t)
	addSubscription(t, n, srv.URL+"/push/gone")

	if accepted, total := n.SendTest(context.Background()); accepted != 0 || total != 1 {
		t.Fatalf("send = %d/%d, want 0/1", accepted, total)
	}
	if count, _ := store.CountPushSubscriptions(context.Background(), n.db); count != 0 {
		t.Fatalf("404 subscription kept (%d)", count)
	}
}

func TestSendFailureDoesNotRecordCapabilityURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL + "/push/secret-capability-token"
	srv.Close() // connection refused
	n := newTestNotifier(t)
	addSubscription(t, n, endpoint)

	n.SendTest(context.Background())
	log, _ := store.ListPushDeliveries(context.Background(), n.db, 1)
	if len(log) != 1 || log[0].Status != 0 || log[0].Error == "" {
		t.Fatalf("failure not recorded: %+v", log)
	}
	if strings.Contains(log[0].Error, "secret-capability-token") {
		t.Fatalf("capability URL leaked into delivery log: %q", log[0].Error)
	}
}
