// Package push sends Web Push notifications to browser PWA clients
// and maintains live SSE connections for the agent (ghost-mail integration).
package push

import (
	"context"
	"crypto/elliptic"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"comstac/internal/store"
)

// Notifier delivers Web Push notifications to all registered subscriptions,
// and emits new_mail events to connected agent SSE clients.
type Notifier struct {
	db           *sql.DB
	vapidPublic  string // base64url-encoded public key, passed directly to webpush
	vapidPrivate string
	vapidSubject string
	agentClients *AgentClients // nil if agent SSE not configured
	queue        chan newMailNotification
	workers      int
}

const (
	defaultNotificationQueueSize = 128
	defaultNotificationWorkers   = 2
	notificationTimeout          = 15 * time.Second
)

type newMailNotification struct {
	subject   string
	from      string
	messageID int64
	reminder  bool
	// event is set for calendar reminders, which carry a prepared payload.
	event *payload
}

func New(db *sql.DB, vapidPublic, vapidPrivate, vapidSubject string, ac *AgentClients) (*Notifier, error) {
	// The webpush library prepends "mailto:" if the subject doesn't start with "https:".
	// Strip any existing "mailto:" prefix to avoid doubling it.
	subject := strings.TrimPrefix(vapidSubject, "mailto:")

	// Verify that the public key corresponds to the private key. Mismatch means
	// the keys were generated separately or swapped in the environment file.
	if err := validateVAPIDKeyPair(vapidPublic, vapidPrivate); err != nil {
		return nil, fmt.Errorf("validate VAPID key pair: %w", err)
	}

	return &Notifier{
		db:           db,
		vapidPublic:  vapidPublic,
		vapidPrivate: vapidPrivate,
		vapidSubject: subject,
		agentClients: ac,
		queue:        make(chan newMailNotification, defaultNotificationQueueSize),
		workers:      defaultNotificationWorkers,
	}, nil
}

// QueueNewMail queues a notification without delaying SMTP or IMAP ingest.
// A full queue drops only the notification; the persisted message remains
// accepted and visible in the mailbox.
func (n *Notifier) QueueNewMail(subject, from string, messageID int64) bool {
	select {
	case n.queue <- newMailNotification{subject: subject, from: from, messageID: messageID}:
		return true
	default:
		return false
	}
}

// QueueReminder queues a notification for a message whose snooze expired.
// Reminders go to Web Push subscriptions only, not agent new_mail events.
func (n *Notifier) QueueReminder(subject, from string, messageID int64) bool {
	select {
	case n.queue <- newMailNotification{subject: subject, from: from, messageID: messageID, reminder: true}:
		return true
	default:
		return false
	}
}

// QueueEventReminder queues a calendar event reminder. url is the in-app page
// opened when the notification is clicked; tag identifies the occurrence.
func (n *Notifier) QueueEventReminder(title, body, url, tag string) bool {
	select {
	case n.queue <- newMailNotification{event: &payload{Title: title, Body: body, URL: url, Tag: tag}}:
		return true
	default:
		return false
	}
}

// Run processes the bounded notification queue with a fixed worker count.
func (n *Notifier) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := 0; i < n.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-n.queue:
					deliveryCtx, cancel := context.WithTimeout(ctx, notificationTimeout)
					if event.event != nil {
						n.sendPush(deliveryCtx, "event", reminderTTL, *event.event)
					} else if event.reminder {
						n.SendReminder(deliveryCtx, event.subject, event.from, event.messageID)
					} else {
						n.SendNewMail(deliveryCtx, event.subject, event.from, event.messageID)
					}
					cancel()
				}
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

// validateVAPIDKeyPair checks that the private key produces the given public key.
func validateVAPIDKeyPair(vapidPublic, vapidPrivate string) error {
	pubBytes, err := base64.RawURLEncoding.DecodeString(vapidPublic)
	if err != nil {
		// Try with padding
		pubBytes, err = base64.URLEncoding.DecodeString(vapidPublic)
		if err != nil {
			return fmt.Errorf("decode public key: %w", err)
		}
	}
	privBytes, err := base64.RawURLEncoding.DecodeString(vapidPrivate)
	if err != nil {
		privBytes, err = base64.URLEncoding.DecodeString(vapidPrivate)
		if err != nil {
			return fmt.Errorf("decode private key: %w", err)
		}
	}

	curve := elliptic.P256()

	// Derive public key from private scalar.
	x, y := curve.ScalarBaseMult(privBytes)

	// Decode stored public key.
	storedX, storedY := elliptic.Unmarshal(curve, pubBytes)
	if storedX == nil {
		return fmt.Errorf("COMSTAC_VAPID_PUBLIC is not a valid P-256 uncompressed point (%d bytes)", len(pubBytes))
	}

	if storedX.Cmp(x) != 0 || storedY.Cmp(y) != 0 {
		return fmt.Errorf("public and private VAPID keys do not match")
	}
	return nil
}

type payload struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	MessageID int64  `json:"message_id,omitempty"`
	// Tag identifies the notification on the device. Distinct tags keep a
	// burst of mail from collapsing into a single notification.
	Tag string `json:"tag,omitempty"`
	// URL is the in-app path opened on click; defaults to the message.
	URL string `json:"url,omitempty"`
}

// Delivery settings. A short TTL makes the push service discard a message
// whenever the device is briefly unreachable (Doze, network change), and
// normal urgency lets Android defer it; both caused missed notifications.
const (
	mailTTL      = 24 * 60 * 60 // seconds
	reminderTTL  = 24 * 60 * 60
	testTTL      = 5 * 60
	pushUrgency  = webpush.UrgencyHigh
	maxErrorText = 200
)

// SendNewMail dispatches a "new mail" push notification to all subscriptions
// and emits a new_mail event to all connected agent SSE clients.
// Delivery errors per-subscription are logged but do not fail the call.
func (n *Notifier) SendNewMail(ctx context.Context, subject, from string, messageID int64) {
	// Emit to agent SSE clients.
	if n.agentClients != nil {
		n.agentClients.EmitNewMail(messageID, from, subject)
	}
	n.sendPush(ctx, "mail", mailTTL, payload{
		Title: "comstac", Body: notificationBody(subject, from),
		MessageID: messageID, Tag: fmt.Sprintf("mail-%d", messageID),
	})
}

// SendReminder dispatches a "snooze expired" push notification to all
// subscriptions.
func (n *Notifier) SendReminder(ctx context.Context, subject, from string, messageID int64) {
	n.sendPush(ctx, "reminder", reminderTTL, payload{
		Title: "comstac reminder", Body: notificationBody(subject, from),
		MessageID: messageID, Tag: fmt.Sprintf("reminder-%d", messageID),
	})
}

// SendTest sends a test notification to every subscription and reports how
// many the push services accepted. Results are also recorded in the delivery
// log.
func (n *Notifier) SendTest(ctx context.Context) (accepted, total int) {
	return n.sendPush(ctx, "test", testTTL, payload{
		Title: "comstac", Body: "test notification", Tag: "test",
	})
}

func notificationBody(subject, from string) string {
	if from != "" {
		return fmt.Sprintf("%s — %s", from, subject)
	}
	return subject
}

func (n *Notifier) sendPush(ctx context.Context, kind string, ttl int, p payload) (accepted, total int) {
	subs, err := store.ListPushSubscriptions(ctx, n.db)
	if err != nil {
		slog.Error("push: list subscriptions", "err", err)
		return 0, 0
	}
	if len(subs) == 0 {
		return 0, 0
	}

	msg, _ := json.Marshal(p)
	opts := &webpush.Options{
		Subscriber:      n.vapidSubject,
		VAPIDPublicKey:  n.vapidPublic,
		VAPIDPrivateKey: n.vapidPrivate,
		TTL:             ttl,
		Urgency:         pushUrgency,
	}

	for _, sub := range subs {
		host := endpointHost(sub.Endpoint)
		ws := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.P256dh,
				Auth:   sub.Auth,
			},
		}
		rec := store.PushDelivery{Kind: kind, EndpointHost: host}
		resp, err := webpush.SendNotificationWithContext(ctx, msg, ws, opts)
		if err != nil {
			// net/http errors embed the request URL, which is the
			// subscription capability; keep only the host.
			errText := strings.ReplaceAll(err.Error(), sub.Endpoint, "https://"+host+"/…")
			rec.Error = truncate(errText, maxErrorText)
			slog.Warn("push: send failed", "kind", kind, "host", host, "err", errText)
		} else {
			rec.Status = resp.StatusCode
			var bodyBytes [512]byte
			n2, _ := resp.Body.Read(bodyBytes[:])
			resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
				// The subscription no longer exists at the push service.
				rec.Error = "subscription expired; removed"
				slog.Info("push: subscription expired, removing", "host", host, "status", resp.StatusCode)
				_ = store.DeletePushSubscription(ctx, n.db, sub.Endpoint)
			case resp.StatusCode >= 400:
				rec.Error = truncate(strings.ReplaceAll(string(bodyBytes[:n2]), sub.Endpoint, "https://"+host+"/…"), maxErrorText)
				slog.Warn("push: unexpected status", "kind", kind, "host", host, "status", resp.StatusCode, "body", rec.Error)
			default:
				accepted++
				slog.Info("push: delivered to push service", "kind", kind, "host", host, "status", resp.StatusCode)
			}
		}
		total++
		// Record with a fresh context so a timed-out send is still logged.
		recCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := store.RecordPushDelivery(recCtx, n.db, rec); err != nil {
			slog.Error("push: record delivery", "err", err)
		}
		cancel()
	}
	return accepted, total
}

// endpointHost returns only the push service host; the full endpoint URL is a
// capability and is never logged or stored outside push_subscriptions.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return u.Host
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// GenerateVAPIDKeys generates a new VAPID key pair.
// Returns (publicKey, privateKey, err) — note webpush.GenerateVAPIDKeys returns (private, public, err).
func GenerateVAPIDKeys() (public, private string, err error) {
	private, public, err = webpush.GenerateVAPIDKeys()
	return
}
