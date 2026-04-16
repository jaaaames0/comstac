package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/mail"
	"strings"
)

type Account struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	EmailAddress string `json:"email_address"`
	CreatedAt    string `json:"created_at"`
}

func EnsureLocalAccounts(ctx context.Context, db *sql.DB, recipients []string) error {
	for _, raw := range recipients {
		addr, err := normalizeMailbox(raw)
		if err != nil || addr == "" {
			continue
		}

		var exists int
		err = db.QueryRowContext(ctx, `SELECT 1 FROM accounts WHERE kind = 'local' AND email_address = ? LIMIT 1`, addr).Scan(&exists)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("lookup local account %s: %w", addr, err)
		}

		if _, err := db.ExecContext(ctx, `INSERT INTO accounts(name, kind, email_address) VALUES (?, 'local', ?)`, addr, addr); err != nil {
			return fmt.Errorf("insert local account %s: %w", addr, err)
		}
	}
	return nil
}

func ResolveLocalAccountID(ctx context.Context, db *sql.DB, recipient string) (sql.NullInt64, error) {
	addr, err := normalizeMailbox(recipient)
	if err != nil {
		return sql.NullInt64{}, nil
	}
	var id int64
	err = db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE kind = 'local' AND email_address = ? LIMIT 1`, addr).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return sql.NullInt64{}, nil
		}
		return sql.NullInt64{}, fmt.Errorf("resolve local account: %w", err)
	}
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

func ListAccounts(ctx context.Context, db *sql.DB, kind string) ([]Account, error) {
	query := `
		SELECT id, name, kind, email_address, created_at
		FROM accounts
	`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, strings.ToLower(strings.TrimSpace(kind)))
	}
	query += ` ORDER BY id ASC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	defer rows.Close()

	items := make([]Account, 0, 16)
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.Kind, &a.EmailAddress, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return items, nil
}

func CreateLocalAccount(ctx context.Context, db *sql.DB, emailAddress string, name string) (*Account, error) {
	addr, err := normalizeMailbox(emailAddress)
	if err != nil || addr == "" {
		return nil, fmt.Errorf("invalid email address")
	}
	var existingID int64
	err = db.QueryRowContext(ctx, `
		SELECT id FROM accounts
		WHERE kind = 'local' AND email_address = ?
		LIMIT 1
	`, addr).Scan(&existingID)
	if err == nil {
		return nil, fmt.Errorf("local account already exists")
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("lookup local account: %w", err)
	}

	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		cleanName = addr
	}

	res, err := db.ExecContext(ctx, `
		INSERT INTO accounts(name, kind, email_address)
		VALUES (?, 'local', ?)
	`, cleanName, addr)
	if err != nil {
		return nil, fmt.Errorf("insert local account: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("local account id: %w", err)
	}
	var out Account
	if err := db.QueryRowContext(ctx, `
		SELECT id, name, kind, email_address, created_at
		FROM accounts
		WHERE id = ?
	`, id).Scan(&out.ID, &out.Name, &out.Kind, &out.EmailAddress, &out.CreatedAt); err != nil {
		return nil, fmt.Errorf("reload local account: %w", err)
	}
	return &out, nil
}

func normalizeMailbox(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty address")
	}
	if strings.Contains(raw, "<") {
		parsed, err := mail.ParseAddress(raw)
		if err != nil {
			return "", err
		}
		return strings.ToLower(strings.TrimSpace(parsed.Address)), nil
	}
	if !strings.Contains(raw, "@") {
		return "", fmt.Errorf("invalid address")
	}
	return strings.ToLower(raw), nil
}
