package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"strings"

	"comstac/internal/store"
	"comstac/internal/validation"
)

type Source string

const (
	SourceSMTP Source = "smtp"
	SourceIMAP Source = "imap"
)

// wordDecoder decodes RFC 2047 encoded-words in headers.
var wordDecoder mime.WordDecoder

// decodeAddrHeader decodes RFC 2047 encoded-words in an address-list header
// (From, To, Cc, etc.) and returns a clean string suitable for storage and display.
func decodeAddrHeader(header string) string {
	decoded, err := wordDecoder.DecodeHeader(header)
	if err == nil {
		return decoded
	}
	return header
}

type IngestInput struct {
	Source       Source
	AccountID    sql.NullInt64
	EnvelopeFrom string
	EnvelopeTo   []string
	RemoteID     string
	RawMIME      []byte
	RemoteIP     net.IP // set for SMTP; nil for IMAP
}

// MailNotifier is an optional hook called after a message is successfully persisted.
type MailNotifier interface {
	SendNewMail(ctx context.Context, subject, from string, messageID int64)
}

type Service struct {
	db       *sql.DB
	notifier MailNotifier
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// SetNotifier attaches a push notification backend to the ingest service.
func (s *Service) SetNotifier(n MailNotifier) {
	s.notifier = n
}

// IngestRaw persists raw MIME and normalized metadata using one provider-agnostic path.
func (s *Service) IngestRaw(ctx context.Context, in IngestInput) error {
	if len(in.RawMIME) == 0 {
		return fmt.Errorf("raw mime is required")
	}
	if in.Source != SourceSMTP && in.Source != SourceIMAP {
		return fmt.Errorf("unsupported source: %q", in.Source)
	}

	h := sha256.Sum256(in.RawMIME)
	sha := hex.EncodeToString(h[:])

	parsed, parseErr := mail.ReadMessage(strings.NewReader(string(in.RawMIME)))
	subject := ""
	fromAddr := ""
	toAddr := ""
	msgID := ""
	dateHdr := ""

	sentAt := ""
	if parseErr == nil {
		subject, _ = wordDecoder.DecodeHeader(parsed.Header.Get("Subject"))
		fromAddr = decodeAddrHeader(parsed.Header.Get("From"))
		toAddr = decodeAddrHeader(parsed.Header.Get("To"))
		msgID = parsed.Header.Get("Message-Id")
		dateHdr = parsed.Header.Get("Date")
		if t, err := mail.ParseDate(dateHdr); err == nil {
			sentAt = t.UTC().Format("2006-01-02T15:04:05Z")
		}
	}

	bodyText := store.ExtractBodyText(in.RawMIME)
	authRes := validation.CheckMessage(in.RawMIME, in.RemoteIP, in.EnvelopeFrom)
	authJSON := authRes.ToJSON()

	// Auto-flag as spam when both SPF and DKIM hard-fail (strong signal of forgery).
	autoSpam := 0
	if authRes.SPF == "fail" && authRes.DKIM == "fail" {
		autoSpam = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages_raw (
			source, account_id, envelope_from, envelope_to, remote_id, mime, mime_sha256
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, string(in.Source), nullableID(in.AccountID), in.EnvelopeFrom, strings.Join(in.EnvelopeTo, ","), in.RemoteID, in.RawMIME, sha)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("insert raw: %w", err)
	}

	rawID, err := res.LastInsertId()
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("raw id: %w", err)
	}

	threadID, err := ensureThread(ctx, tx, strings.TrimSpace(strings.ToLower(subject)))
	if err != nil {
		tx.Rollback()
		return err
	}

	msgRes, err := tx.ExecContext(ctx, `
		INSERT INTO messages (
			raw_id, account_id, thread_id, message_id, subject, from_addr, to_addr, date_hdr, sent_at, body_text, auth_results, spam
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rawID, nullableID(in.AccountID), threadID, msgID, subject, fromAddr, toAddr, dateHdr, nullableStr(sentAt), bodyText, authJSON, autoSpam)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("insert message: %w", err)
	}

	messageID, err := msgRes.LastInsertId()
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("message id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if s.notifier != nil {
		go s.notifier.SendNewMail(context.Background(), subject, fromAddr, messageID)
	}

	return nil
}

func ensureThread(ctx context.Context, tx *sql.Tx, subjectNorm string) (int64, error) {
	if subjectNorm == "" {
		res, err := tx.ExecContext(ctx, `INSERT INTO threads(subject_norm) VALUES (NULL)`)
		if err != nil {
			return 0, fmt.Errorf("insert thread: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("thread id: %w", err)
		}
		return id, nil
	}

	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM threads WHERE subject_norm = ? LIMIT 1`, subjectNorm).Scan(&id)
	if err == nil {
		if _, uErr := tx.ExecContext(ctx, `UPDATE threads SET updated_at = datetime('now') WHERE id = ?`, id); uErr != nil {
			return 0, fmt.Errorf("touch thread: %w", uErr)
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("select thread: %w", err)
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO threads(subject_norm) VALUES (?)`, subjectNorm)
	if err != nil {
		return 0, fmt.Errorf("insert thread: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("thread id: %w", err)
	}
	return id, nil
}

func nullableID(id sql.NullInt64) any {
	if id.Valid {
		return id.Int64
	}
	return nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
