package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// EnsureIMAPAccount creates an account with kind='imap' if one doesn't exist
// for the given email address, then returns its ID.
func EnsureIMAPAccount(ctx context.Context, db *sql.DB, email string) (int64, error) {
	addr := strings.ToLower(strings.TrimSpace(email))
	if addr == "" {
		return 0, fmt.Errorf("empty email")
	}

	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE kind = 'imap' AND email_address = ? LIMIT 1`, addr,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("lookup imap account: %w", err)
	}

	res, err := db.ExecContext(ctx,
		`INSERT INTO accounts(name, kind, email_address) VALUES (?, 'imap', ?)`, addr, addr,
	)
	if err != nil {
		return 0, fmt.Errorf("insert imap account: %w", err)
	}
	return res.LastInsertId()
}

// GetIMAPCheckpoint returns the last fetched UID for an account+mailbox pair.
// Returns 0 if no checkpoint exists yet.
func GetIMAPCheckpoint(ctx context.Context, db *sql.DB, accountID int64, mailbox string) (uint32, error) {
	var uid uint32
	err := db.QueryRowContext(ctx,
		`SELECT last_uid FROM imap_checkpoints WHERE account_id = ? AND mailbox = ? LIMIT 1`,
		accountID, mailbox,
	).Scan(&uid)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get imap checkpoint: %w", err)
	}
	return uid, nil
}

// SetIMAPCheckpoint upserts the last fetched UID for an account+mailbox pair.
func SetIMAPCheckpoint(ctx context.Context, db *sql.DB, accountID int64, mailbox string, lastUID uint32) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO imap_checkpoints(account_id, mailbox, last_uid, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(account_id, mailbox) DO UPDATE SET
			last_uid   = excluded.last_uid,
			updated_at = excluded.updated_at
	`, accountID, mailbox, lastUID)
	if err != nil {
		return fmt.Errorf("set imap checkpoint: %w", err)
	}
	return nil
}
