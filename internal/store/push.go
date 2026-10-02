package store

import (
	"context"
	"database/sql"
)

type PushSubscription struct {
	ID        int64
	Endpoint  string
	P256dh    string
	Auth      string
	CreatedAt string
}

// ListPushSubscriptions returns all registered push subscriptions.
func ListPushSubscriptions(ctx context.Context, db *sql.DB) ([]PushSubscription, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, endpoint, p256dh, auth, created_at FROM push_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var s PushSubscription
		if err := rows.Scan(&s.ID, &s.Endpoint, &s.P256dh, &s.Auth, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SavePushSubscription upserts a push subscription by endpoint.
func SavePushSubscription(ctx context.Context, db *sql.DB, endpoint, p256dh, auth string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO push_subscriptions (endpoint, p256dh, auth)
		 VALUES (?, ?, ?)
		 ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh, auth=excluded.auth`,
		endpoint, p256dh, auth)
	return err
}

// DeletePushSubscription removes a subscription by endpoint.
func DeletePushSubscription(ctx context.Context, db *sql.DB, endpoint string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// CountPushSubscriptions returns the number of registered subscriptions.
func CountPushSubscriptions(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&n)
	return n, err
}

// pushDeliveryLogLimit bounds the delivery log.
const pushDeliveryLogLimit = 200

// PushDelivery is one recorded Web Push delivery attempt. Status is the push
// service HTTP status, or 0 when the request itself failed.
type PushDelivery struct {
	CreatedAt    string
	Kind         string
	EndpointHost string
	Status       int
	Error        string
}

// RecordPushDelivery appends a delivery attempt and trims the log.
func RecordPushDelivery(ctx context.Context, db *sql.DB, d PushDelivery) error {
	if _, err := db.ExecContext(ctx,
		`INSERT INTO push_deliveries (kind, endpoint_host, status, error) VALUES (?, ?, ?, ?)`,
		d.Kind, d.EndpointHost, d.Status, d.Error); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx,
		`DELETE FROM push_deliveries WHERE id <= (SELECT MAX(id) FROM push_deliveries) - ?`,
		pushDeliveryLogLimit)
	return err
}

// ListPushDeliveries returns the most recent delivery attempts, newest first.
func ListPushDeliveries(ctx context.Context, db *sql.DB, limit int) ([]PushDelivery, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT created_at, kind, endpoint_host, status, error FROM push_deliveries ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushDelivery
	for rows.Next() {
		var d PushDelivery
		if err := rows.Scan(&d.CreatedAt, &d.Kind, &d.EndpointHost, &d.Status, &d.Error); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
