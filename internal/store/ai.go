package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// AIRun is one AI date-extraction call.
type AIRun struct {
	ID           int64
	MessageID    int64
	Model        string
	Trigger      string
	Status       string
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
	Proposed     int
	Kept         int
	Created      int
	Error        string
	RequestID    string
	CreatedAt    time.Time
}

// RecordAIRun stores a run.
func RecordAIRun(ctx context.Context, db *sql.DB, r AIRun) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO ai_extraction_runs (message_id, model, trigger, status, input_tokens, output_tokens,
			cost_microusd, proposed, kept, created, error, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.MessageID, r.Model, r.Trigger, r.Status, r.InputTokens, r.OutputTokens, r.CostMicroUSD,
		r.Proposed, r.Kept, r.Created, r.Error, r.RequestID)
	return err
}

// AIUsage summarizes runs since a time.
type AIUsage struct {
	Calls        int
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
}

// AIUsageSince totals runs made at or after since.
func AIUsageSince(ctx context.Context, db *sql.DB, since time.Time) (AIUsage, error) {
	var u AIUsage
	err := db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_microusd), 0)
		FROM ai_extraction_runs WHERE created_at >= ?`, since.UTC().Format(time.RFC3339)).
		Scan(&u.Calls, &u.InputTokens, &u.OutputTokens, &u.CostMicroUSD)
	return u, err
}

// LastAIRun returns the most recent run for a message, or nil.
func LastAIRun(ctx context.Context, db *sql.DB, messageID int64) (*AIRun, error) {
	var r AIRun
	var created string
	err := db.QueryRowContext(ctx, `
		SELECT id, message_id, model, trigger, status, input_tokens, output_tokens, cost_microusd, proposed, kept, created, error, created_at
		FROM ai_extraction_runs WHERE message_id = ? ORDER BY id DESC LIMIT 1`, messageID).
		Scan(&r.ID, &r.MessageID, &r.Model, &r.Trigger, &r.Status, &r.InputTokens, &r.OutputTokens, &r.CostMicroUSD,
			&r.Proposed, &r.Kept, &r.Created, &r.Error, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &r, nil
}

var reDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// ErrInvalidDomain is returned for a malformed sender domain.
var ErrInvalidDomain = errors.New("invalid domain")

// NormalizeDomain lower-cases a domain and checks its shape.
func NormalizeDomain(d string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if len(d) > 253 || !reDomain.MatchString(d) {
		return "", ErrInvalidDomain
	}
	return d, nil
}

// SenderDomain returns the lower-case domain of a From header, or "".
func SenderDomain(from string) string {
	a, err := mail.ParseAddress(from)
	if err != nil {
		return ""
	}
	_, dom, ok := strings.Cut(strings.ToLower(a.Address), "@")
	if !ok {
		return ""
	}
	return dom
}

// ListAISenderDomains returns the automatic AI sender domains.
func ListAISenderDomains(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT domain FROM ai_sender_domains ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AddAISenderDomain adds a domain to the automatic list.
func AddAISenderDomain(ctx context.Context, db *sql.DB, domain string) (string, error) {
	d, err := NormalizeDomain(domain)
	if err != nil {
		return "", err
	}
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO ai_sender_domains (domain) VALUES (?)`, d)
	return d, err
}

// RemoveAISenderDomain removes a domain from the automatic list.
func RemoveAISenderDomain(ctx context.Context, db *sql.DB, domain string) error {
	d, err := NormalizeDomain(domain)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM ai_sender_domains WHERE domain = ?`, d)
	return err
}

// PendingAISenderMessages returns up to limit inbox messages from automatic
// sender domains that have had a rules scan but no AI run, oldest first.
// Subdomains of a listed domain match ("mail.example.com" for "example.com").
func PendingAISenderMessages(ctx context.Context, db *sql.DB, limit int) ([]int64, error) {
	domains, err := ListAISenderDomains(ctx, db)
	if err != nil || len(domains) == 0 {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.from_addr, '')
		FROM messages m
		JOIN message_extractions x ON x.message_id = m.id
		WHERE m.archived = 0 AND m.spam = 0
		  AND NOT EXISTS (SELECT 1 FROM ai_extraction_runs r WHERE r.message_id = m.id)
		ORDER BY m.id`)
	if err != nil {
		return nil, fmt.Errorf("pending ai messages: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() && len(out) < limit {
		var id int64
		var from string
		if err := rows.Scan(&id, &from); err != nil {
			return nil, err
		}
		if dom := SenderDomain(from); dom != "" && domainListed(dom, domains) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func domainListed(dom string, list []string) bool {
	for _, d := range list {
		if dom == d || strings.HasSuffix(dom, "."+d) {
			return true
		}
	}
	return false
}

// AIDomainListed reports whether a From header's domain is on the automatic list.
func AIDomainListed(ctx context.Context, db *sql.DB, from string) bool {
	dom := SenderDomain(from)
	if dom == "" {
		return false
	}
	domains, err := ListAISenderDomains(ctx, db)
	return err == nil && domainListed(dom, domains)
}
