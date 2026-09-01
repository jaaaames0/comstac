// Package push sends Web Push notifications to browser PWA clients
// and maintains live SSE connections for the agent (ghost-mail integration).
package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"

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
}

func New(db *sql.DB, vapidPublic, vapidPrivate, vapidSubject string, ac *AgentClients) *Notifier {
	// The webpush library prepends "mailto:" if the subject doesn't start with "https:".
	// Strip any existing "mailto:" prefix to avoid doubling it.
	subject := strings.TrimPrefix(vapidSubject, "mailto:")

	// Verify that the public key corresponds to the private key. Mismatch means
	// the keys were generated separately or swapped in the environment file.
	if err := validateVAPIDKeyPair(vapidPublic, vapidPrivate); err != nil {
		slog.Error("push: VAPID key pair mismatch — regenerate keys with 'comstac vapid' and update COMSTAC_VAPID_PUBLIC/PRIVATE", "err", err)
	}

	return &Notifier{
		db:           db,
		vapidPublic:  vapidPublic,
		vapidPrivate: vapidPrivate,
		vapidSubject: subject,
		agentClients: ac,
	}
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
	d := new(big.Int).SetBytes(privBytes)
	x, y := curve.ScalarBaseMult(privBytes)
	derivedPub := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
	derivedPubBytes := elliptic.Marshal(curve, derivedPub.X, derivedPub.Y)
	_ = d

	// Decode stored public key.
	storedX, storedY := elliptic.Unmarshal(curve, pubBytes)
	if storedX == nil {
		return fmt.Errorf("COMSTAC_VAPID_PUBLIC is not a valid P-256 uncompressed point (%d bytes)", len(pubBytes))
	}

	if storedX.Cmp(derivedPub.X) != 0 || storedY.Cmp(derivedPub.Y) != 0 {
		return fmt.Errorf("public key (len=%d) does not match private key (len=%d); derived pubkey starts %x, stored starts %x",
			len(pubBytes), len(privBytes), derivedPubBytes[:4], pubBytes[:4])
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
	p := payload{Title: "comstac", Body: body, MessageID: messageID}
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
		resp, err := webpush.SendNotification(msg, ws, opts)
		if err != nil {
			slog.Warn("push: send failed", "endpoint", sub.Endpoint[:min(len(sub.Endpoint), 60)], "err", err)
			continue
		}
		if resp.StatusCode == http.StatusGone {
			resp.Body.Close()
			slog.Info("push: subscription expired, removing", "endpoint", sub.Endpoint[:min(len(sub.Endpoint), 60)])
			_ = store.DeletePushSubscription(context.Background(), n.db, sub.Endpoint)
		} else if resp.StatusCode >= 400 {
			var bodyBytes [512]byte
			n2, _ := resp.Body.Read(bodyBytes[:])
			resp.Body.Close()
			slog.Warn("push: unexpected status", "status", resp.StatusCode,
				"endpoint", sub.Endpoint[:min(len(sub.Endpoint), 60)],
				"body", string(bodyBytes[:n2]))
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
