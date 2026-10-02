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
					if event.reminder {
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
}

// SendNewMail dispatches a "new mail" push notification to all subscriptions
// and emits a new_mail event to all connected agent SSE clients.
// Delivery errors per-subscription are logged but do not fail the call.
func (n *Notifier) SendNewMail(ctx context.Context, subject, from string, messageID int64) {
	// Emit to agent SSE clients.
	if n.agentClients != nil {
		n.agentClients.EmitNewMail(messageID, from, subject)
	}
	n.sendPush(ctx, "comstac", subject, from, messageID)
}

// SendReminder dispatches a "snooze expired" push notification to all
// subscriptions.
func (n *Notifier) SendReminder(ctx context.Context, subject, from string, messageID int64) {
	n.sendPush(ctx, "comstac reminder", subject, from, messageID)
}

func (n *Notifier) sendPush(ctx context.Context, title, subject, from string, messageID int64) {
	subs, err := store.ListPushSubscriptions(ctx, n.db)
	if err != nil {
		slog.Error("push: list subscriptions", "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	body := subject
	if from != "" {
		body = fmt.Sprintf("%s — %s", from, subject)
	}
	p := payload{Title: title, Body: body, MessageID: messageID}
	msg, _ := json.Marshal(p)

	opts := &webpush.Options{
		Subscriber:      n.vapidSubject,
		VAPIDPublicKey:  n.vapidPublic,
		VAPIDPrivateKey: n.vapidPrivate,
		TTL:             30,
	}

	for _, sub := range subs {
		ws := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.P256dh,
				Auth:   sub.Auth,
			},
		}
		resp, err := webpush.SendNotificationWithContext(ctx, msg, ws, opts)
		if err != nil {
			slog.Warn("push: send failed", "err", err)
			continue
		}
		if resp.StatusCode == http.StatusGone {
			resp.Body.Close()
			slog.Info("push: subscription expired, removing")
			_ = store.DeletePushSubscription(ctx, n.db, sub.Endpoint)
		} else if resp.StatusCode >= 400 {
			var bodyBytes [512]byte
			n2, _ := resp.Body.Read(bodyBytes[:])
			resp.Body.Close()
			slog.Warn("push: unexpected status", "status", resp.StatusCode, "body", string(bodyBytes[:n2]))
		} else {
			resp.Body.Close()
		}
	}
}

// GenerateVAPIDKeys generates a new VAPID key pair.
// Returns (publicKey, privateKey, err) — note webpush.GenerateVAPIDKeys returns (private, public, err).
func GenerateVAPIDKeys() (public, private string, err error) {
	private, public, err = webpush.GenerateVAPIDKeys()
	return
}
