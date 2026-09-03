package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

var ErrInvalidMailboxAddress = errors.New("invalid mailbox address")

type RemoteImageSender struct {
	EmailAddress string
	CreatedAt    string
}

// NormalizeMailboxAddress extracts one RFC mailbox and returns the canonical
// exact-address key used by the remote-image privacy preference. Display names
// are deliberately ignored so a forged display name cannot match an approved
// mailbox address.
func NormalizeMailboxAddress(raw string) (string, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrInvalidMailboxAddress
	}
	address := strings.ToLower(strings.TrimSpace(parsed.Address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 || len(address) > 320 || strings.ContainsAny(address, "\r\n\x00") {
		return "", ErrInvalidMailboxAddress
	}
	return address, nil
}

func AddRemoteImageSender(ctx context.Context, db *sql.DB, rawAddress string) (string, error) {
	address, err := NormalizeMailboxAddress(rawAddress)
	if err != nil {
		return "", err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO remote_image_senders(email_address)
		VALUES (?)
		ON CONFLICT(email_address) DO NOTHING
	`, address)
	if err != nil {
		return "", fmt.Errorf("add remote-image sender: %w", err)
	}
	return address, nil
}

func RemoveRemoteImageSender(ctx context.Context, db *sql.DB, rawAddress string) error {
	address, err := NormalizeMailboxAddress(rawAddress)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM remote_image_senders WHERE email_address = ?`, address); err != nil {
		return fmt.Errorf("remove remote-image sender: %w", err)
	}
	return nil
}

func ListRemoteImageSenders(ctx context.Context, db *sql.DB) ([]RemoteImageSender, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT email_address, created_at
		FROM remote_image_senders
		ORDER BY email_address
	`)
	if err != nil {
		return nil, fmt.Errorf("list remote-image senders: %w", err)
	}
	defer rows.Close()

	var senders []RemoteImageSender
	for rows.Next() {
		var sender RemoteImageSender
		if err := rows.Scan(&sender.EmailAddress, &sender.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan remote-image sender: %w", err)
		}
		senders = append(senders, sender)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate remote-image senders: %w", err)
	}
	return senders, nil
}

// RemoteImageSenderAllowed matches only the mailbox parsed from the From
// header. A display name that happens to contain an approved address does not
// match. Invalid or ambiguous From headers fail closed.
func RemoteImageSenderAllowed(ctx context.Context, db *sql.DB, fromHeader string) (bool, error) {
	address, err := NormalizeMailboxAddress(fromHeader)
	if err != nil {
		return false, nil
	}
	var exists int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM remote_image_senders WHERE email_address = ?`, address).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check remote-image sender: %w", err)
	}
	return exists == 1, nil
}
