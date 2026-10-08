package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"modernc.org/sqlite"
)

var ErrDuplicateContact = errors.New("this email address is already saved; edit the existing contact")
var ErrContactNotFound = errors.New("contact not found")

type Contact struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (c Contact) Recipient() string {
	mailbox := (&mail.Address{Address: c.Email}).String()
	if c.Name == "" {
		return mailbox
	}
	// Keep names readable in the form, while quoting commas and escaping quotes.
	name := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(c.Name)
	return `"` + name + `" ` + mailbox
}

func (c Contact) Validate() error {
	_, _, _, err := contactValues(c.Name, c.Email)
	return err
}

func GetMessageSender(ctx context.Context, db *sql.DB, id int64) (string, error) {
	var sender string
	err := db.QueryRowContext(ctx, `SELECT from_addr FROM messages WHERE id=?`, id).Scan(&sender)
	return sender, err
}

func contactValues(name, email string) (string, string, string, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if len(name) > 200 || len(email) > 320 || !utf8.ValidString(name+email) {
		return "", "", "", errors.New("name or email is too long or invalid")
	}
	for _, r := range name + email {
		if unicode.IsControl(r) {
			return "", "", "", errors.New("name and email cannot contain control characters")
		}
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Name != "" {
		return "", "", "", errors.New("enter one email address, with the display name in the name field")
	}
	pos := strings.LastIndexByte(a.Address, '@')
	if pos <= 0 {
		return "", "", "", errors.New("invalid email address")
	}
	key := a.Address[:pos+1] + strings.ToLower(a.Address[pos+1:])
	return name, a.Address, key, nil
}

func SaveContact(ctx context.Context, db *sql.DB, c *Contact) error {
	name, email, key, err := contactValues(c.Name, c.Email)
	if err != nil {
		return err
	}
	if c.ID < 0 {
		return ErrContactNotFound
	}
	var result sql.Result
	if c.ID == 0 {
		result, err = db.ExecContext(ctx, `INSERT INTO contacts(name,email,address_key) VALUES (?,?,?)`, name, email, key)
	} else {
		result, err = db.ExecContext(ctx, `UPDATE contacts SET name=?,email=?,address_key=?,updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`, name, email, key, c.ID)
	}
	if err != nil {
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code() == 2067 {
			return ErrDuplicateContact
		}
		return fmt.Errorf("save contact: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrContactNotFound
	}
	if c.ID == 0 {
		c.ID, err = result.LastInsertId()
	}
	c.Name, c.Email = name, email
	return err
}

func GetContact(ctx context.Context, db *sql.DB, id int64) (*Contact, error) {
	c := &Contact{}
	err := db.QueryRowContext(ctx, `SELECT id,name,email FROM contacts WHERE id=?`, id).Scan(&c.ID, &c.Name, &c.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	return c, err
}

func DeleteContact(ctx context.Context, db *sql.DB, id int64) error {
	r, err := db.ExecContext(ctx, `DELETE FROM contacts WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrContactNotFound
	}
	return err
}

func SearchContacts(ctx context.Context, db *sql.DB, query string, limit, offset int) ([]Contact, error) {
	if len(query) > 128 || !utf8.ValidString(query) || limit < 1 || limit > 51 || offset < 0 || offset > 1000000 {
		return nil, errors.New("invalid contact search limits")
	}
	query = strings.TrimSpace(query)
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	rows, err := db.QueryContext(ctx, `SELECT id,name,email FROM contacts WHERE name LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\' ORDER BY name COLLATE NOCASE,email,id LIMIT ? OFFSET ?`, pattern, pattern, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Contact, 0)
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Email); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
