package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

type MessageListItem struct {
	ID          int64  `json:"id"`
	Source      string `json:"source"`
	Subject     string `json:"subject"`
	FromAddr    string `json:"from_addr"`
	ToAddr      string `json:"to_addr"`
	CreatedAt   string `json:"created_at"`
	SentAt      string `json:"sent_at"` // COALESCE(sent_at, created_at) — use for display
	Read        bool   `json:"read"`
	Archived    bool   `json:"archived"`
	Spam        bool   `json:"spam"`
	SnoozeUntil string `json:"snooze_until,omitempty"`
}

// AttachmentInfo holds display metadata for one MIME attachment part.
type AttachmentInfo struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

type MessageDetail struct {
	ID          int64            `json:"id"`
	Source      string           `json:"source"`
	MessageID   string           `json:"message_id"`
	Subject     string           `json:"subject"`
	FromAddr    string           `json:"from_addr"`
	ToAddr      string           `json:"to_addr"`
	CcAddr      string           `json:"cc_addr,omitempty"`
	DateHdr     string           `json:"date"`
	SentAt      string           `json:"sent_at"` // COALESCE(sent_at, created_at) — for display in detail pane
	CreatedAt   string           `json:"created_at"`
	BodyText    string           `json:"body_text"`
	BodyHTML    string           `json:"body_html,omitempty"`
	AuthResults string           `json:"auth_results,omitempty"`
	Read        bool             `json:"read"`
	Archived    bool             `json:"archived"`
	Spam        bool             `json:"spam"`
	SnoozeUntil string           `json:"snooze_until,omitempty"`
	Attachments []AttachmentInfo `json:"attachments,omitempty"`
}

type ListMessageOptions struct {
	Limit    int
	BeforeID int64
	Unread   *bool
	Archived *bool
	Spam     *bool
	Source   string
	// Trash=true selects archived messages trashed within the last 14 days.
	// Takes precedence over Archived when set.
	Trash bool
}

func ListMessages(ctx context.Context, db *sql.DB, opts ListMessageOptions) ([]MessageListItem, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT
			m.id,
			mr.source,
			COALESCE(m.subject, ''),
			COALESCE(m.from_addr, ''),
			COALESCE(m.to_addr, ''),
			m.created_at,
			m.created_at,
			m.read,
			m.archived,
			m.spam,
			COALESCE(m.snooze_until, '')
		FROM messages m
		JOIN messages_raw mr ON mr.id = m.raw_id
		WHERE 1=1
	`)

	args := make([]any, 0, 8)
	if opts.BeforeID > 0 {
		// Keyset cursor: rows received earlier, or same received time and lower id.
		query.WriteString(`
			AND (
				m.created_at < (SELECT created_at FROM messages WHERE id = ?)
				OR (
					m.created_at = (SELECT created_at FROM messages WHERE id = ?)
					AND m.id < ?
				)
			)`)
		args = append(args, opts.BeforeID, opts.BeforeID, opts.BeforeID)
	}
	if opts.Unread != nil {
		if *opts.Unread {
			query.WriteString(` AND m.read = 0`)
		} else {
			query.WriteString(` AND m.read = 1`)
		}
	}
	if opts.Trash {
		query.WriteString(` AND m.archived = 1 AND (m.archived_at IS NULL OR m.archived_at >= datetime('now', '-14 days'))`)
	} else if opts.Archived != nil {
		if *opts.Archived {
			query.WriteString(` AND m.archived = 1`)
		} else {
			query.WriteString(` AND m.archived = 0`)
		}
	}
	if opts.Spam != nil {
		if *opts.Spam {
			query.WriteString(` AND m.spam = 1`)
		} else {
			query.WriteString(` AND m.spam = 0`)
		}
	}
	if opts.Source != "" {
		source := strings.ToLower(strings.TrimSpace(opts.Source))
		if source == "smtp" || source == "imap" {
			query.WriteString(` AND mr.source = ?`)
			args = append(args, source)
		}
	}
	query.WriteString(` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	items := make([]MessageListItem, 0, limit)
	for rows.Next() {
		var item MessageListItem
		var readInt, archivedInt, spamInt int
		if err := rows.Scan(
			&item.ID,
			&item.Source,
			&item.Subject,
			&item.FromAddr,
			&item.ToAddr,
			&item.CreatedAt,
			&item.SentAt,
			&readInt,
			&archivedInt,
			&spamInt,
			&item.SnoozeUntil,
		); err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		item.Read = readInt != 0
		item.Archived = archivedInt != 0
		item.Spam = spamInt != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}

	return items, nil
}

func GetMessageDetail(ctx context.Context, db *sql.DB, id int64) (*MessageDetail, error) {
	var d MessageDetail
	var rawMIME []byte
	var readInt int
	var archivedInt int
	var spamInt int
	err := db.QueryRowContext(ctx, `
		SELECT
			m.id,
			mr.source,
			COALESCE(m.message_id, ''),
			COALESCE(m.subject, ''),
			COALESCE(m.from_addr, ''),
			COALESCE(m.to_addr, ''),
			COALESCE(m.date_hdr, ''),
			COALESCE(m.sent_at, m.created_at),
			m.created_at,
			m.read,
			m.archived,
			m.spam,
			COALESCE(m.snooze_until, ''),
			COALESCE(m.auth_results, ''),
			mr.mime
		FROM messages m
		JOIN messages_raw mr ON mr.id = m.raw_id
		WHERE m.id = ?
	`, id).Scan(&d.ID, &d.Source, &d.MessageID, &d.Subject, &d.FromAddr, &d.ToAddr, &d.DateHdr, &d.SentAt, &d.CreatedAt, &readInt, &archivedInt, &spamInt, &d.SnoozeUntil, &d.AuthResults, &rawMIME)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query message detail: %w", err)
	}

	d.BodyText = ExtractBodyText(rawMIME)
	if d.BodyText == "" {
		d.BodyText = "(no body)"
	}
	d.BodyHTML = extractBodyHTML(rawMIME)
	d.Read = readInt != 0
	d.Archived = archivedInt != 0
	d.Spam = spamInt != 0
	d.Attachments = ExtractAttachments(rawMIME)
	if msg, err := mail.ReadMessage(bytes.NewReader(rawMIME)); err == nil {
		d.CcAddr = msg.Header.Get("Cc")
	}
	return &d, nil
}

func SetMessageRead(ctx context.Context, db *sql.DB, id int64, read bool) (bool, error) {
	v := 0
	if read {
		v = 1
	}
	res, err := db.ExecContext(ctx, `UPDATE messages SET read = ? WHERE id = ?`, v, id)
	if err != nil {
		return false, fmt.Errorf("set read: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set read rows: %w", err)
	}
	return affected > 0, nil
}

func SetMessageArchived(ctx context.Context, db *sql.DB, id int64, archived bool) (bool, error) {
	var res sql.Result
	var err error
	if archived {
		res, err = db.ExecContext(ctx,
			`UPDATE messages SET archived = 1, archived_at = datetime('now') WHERE id = ?`, id)
	} else {
		res, err = db.ExecContext(ctx,
			`UPDATE messages SET archived = 0, archived_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return false, fmt.Errorf("set archived: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set archived rows: %w", err)
	}
	return affected > 0, nil
}

func SetMessageSpam(ctx context.Context, db *sql.DB, id int64, spam bool) (bool, error) {
	v := 0
	if spam {
		v = 1
	}
	res, err := db.ExecContext(ctx, `UPDATE messages SET spam = ? WHERE id = ?`, v, id)
	if err != nil {
		return false, fmt.Errorf("set spam: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set spam rows: %w", err)
	}
	return affected > 0, nil
}

func SetMessageSnoozeUntil(ctx context.Context, db *sql.DB, id int64, until *time.Time) (bool, error) {
	var val any
	if until != nil {
		val = until.UTC().Format(time.RFC3339)
	}
	res, err := db.ExecContext(ctx, `UPDATE messages SET snooze_until = ? WHERE id = ?`, val, id)
	if err != nil {
		return false, fmt.Errorf("set snooze: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set snooze rows: %w", err)
	}
	return affected > 0, nil
}

func SearchMessages(ctx context.Context, db *sql.DB, q string, limit int) ([]MessageListItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
			m.id,
			mr.source,
			COALESCE(m.subject, ''),
			COALESCE(m.from_addr, ''),
			COALESCE(m.to_addr, ''),
			m.created_at,
			m.read,
			m.archived,
			m.spam,
			COALESCE(m.snooze_until, '')
		FROM messages_fts f
		JOIN messages m ON m.id = f.rowid
		JOIN messages_raw mr ON mr.id = m.raw_id
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	items := make([]MessageListItem, 0)
	for rows.Next() {
		var item MessageListItem
		var readInt, archivedInt, spamInt int
		if err := rows.Scan(&item.ID, &item.Source, &item.Subject, &item.FromAddr, &item.ToAddr, &item.CreatedAt, &readInt, &archivedInt, &spamInt, &item.SnoozeUntil); err != nil {
			return nil, fmt.Errorf("scan search row: %w", err)
		}
		item.Read = readInt != 0
		item.Archived = archivedInt != 0
		item.Spam = spamInt != 0
		items = append(items, item)
	}
	return items, rows.Err()
}

// ExtractBodyText returns readable plain text from a raw MIME message.
// HTML parts are stripped to text; multipart/alternative prefers text/plain.
func ExtractBodyText(rawMIME []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMIME))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(extractBodyPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body))
}

// extractBodyHTML returns the raw HTML content of a message, if any.
// Returns empty string for plain-text-only messages.
func extractBodyHTML(rawMIME []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMIME))
	if err != nil {
		return ""
	}
	return extractHTMLPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
}

func extractHTMLPart(contentType, transferEncoding string, body io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = ""
		params = map[string]string{}
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return ""
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, partErr := mr.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}
			if disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition")); disp == "attachment" {
				continue
			}
			// Recurse so nested structures such as multipart/mixed wrapping
			// multipart/alternative still yield their text/html part.
			if html := extractHTMLPart(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part); html != "" {
				return html
			}
		}
		return ""
	}

	if mediaType == "text/html" {
		decoded := decodeBody(transferEncoding, body)
		content, readErr := io.ReadAll(io.LimitReader(decoded, 1024*1024))
		if readErr != nil {
			return ""
		}
		return string(content)
	}
	return ""
}

var (
	reHTMLStyleScript = regexp.MustCompile(`(?is)<(style|script|head)[^>]*>.*?</(style|script|head)>`)
	reHTMLBR          = regexp.MustCompile(`(?i)<br\s*/?>`)
	reHTMLBlockEnd    = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|tr|blockquote)[^>]*>`)
	reHTMLTag         = regexp.MustCompile(`<[^>]+>`)
	reHTMLMultiSpace  = regexp.MustCompile(`[ \t]+`)
	reHTMLMultiNL     = regexp.MustCompile(`\n{3,}`)
)

// htmlToText strips HTML markup and returns readable plain text.
func htmlToText(src string) string {
	s := reHTMLStyleScript.ReplaceAllString(src, " ")
	s = reHTMLBR.ReplaceAllString(s, "\n")
	s = reHTMLBlockEnd.ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reHTMLMultiSpace.ReplaceAllString(s, " ")
	s = reHTMLMultiNL.ReplaceAllString(s, "\n\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

func extractBodyPart(contentType string, transferEncoding string, body io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = ""
		params = map[string]string{}
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return ""
		}
		mr := multipart.NewReader(body, boundary)
		fallback := ""
		for {
			part, partErr := mr.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}
			parsedType := part.Header.Get("Content-Type")
			parsedEncoding := part.Header.Get("Content-Transfer-Encoding")
			partText := extractBodyPart(parsedType, parsedEncoding, part)
			if strings.TrimSpace(partText) == "" {
				continue
			}
			ptMediaType, _, _ := mime.ParseMediaType(parsedType)
			if ptMediaType == "text/plain" {
				return strings.TrimSpace(partText)
			}
			if fallback == "" {
				fallback = strings.TrimSpace(partText)
			}
		}
		return fallback
	}

	decoded := decodeBody(transferEncoding, body)
	content, readErr := io.ReadAll(io.LimitReader(decoded, 1024*1024))
	if readErr != nil {
		return ""
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return ""
	}
	if mediaType == "text/html" {
		return htmlToText(text)
	}
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") {
		return text
	}
	return ""
}

// GetRawMIMEByMessageID returns the raw MIME bytes for a message.
func GetRawMIMEByMessageID(ctx context.Context, db *sql.DB, id int64) ([]byte, error) {
	var raw []byte
	err := db.QueryRowContext(ctx, `
		SELECT mr.mime FROM messages m
		JOIN messages_raw mr ON mr.id = m.raw_id
		WHERE m.id = ?`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return raw, err
}

// ExtractAttachments returns metadata for all attachment parts in a raw MIME message.
func ExtractAttachments(rawMIME []byte) []AttachmentInfo {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMIME))
	if err != nil {
		return nil
	}
	var result []AttachmentInfo
	idx := 0
	walkForAttachments(msg.Header.Get("Content-Type"), msg.Body, &idx, func(info AttachmentInfo, _ string, _ io.Reader) bool {
		result = append(result, info)
		return true
	})
	return result
}

// GetAttachmentPart decodes and returns the raw bytes of the attachment at the given index.
func GetAttachmentPart(rawMIME []byte, index int) (AttachmentInfo, []byte, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMIME))
	if err != nil {
		return AttachmentInfo{}, nil, err
	}
	var found AttachmentInfo
	var data []byte
	var walkErr error
	idx := 0
	walkForAttachments(msg.Header.Get("Content-Type"), msg.Body, &idx, func(info AttachmentInfo, te string, body io.Reader) bool {
		if info.Index != index {
			return true
		}
		found = info
		decoded := decodeBody(te, body)
		data, walkErr = io.ReadAll(io.LimitReader(decoded, 50*1024*1024))
		return false
	})
	if data == nil && walkErr == nil {
		return AttachmentInfo{}, nil, fmt.Errorf("attachment %d not found", index)
	}
	return found, data, walkErr
}

type attachVisitor func(info AttachmentInfo, transferEncoding string, body io.Reader) bool

func walkForAttachments(contentType string, body io.Reader, idx *int, fn attachVisitor) bool {
	mediaType, params, _ := mime.ParseMediaType(contentType)

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return true
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			ct := part.Header.Get("Content-Type")
			te := part.Header.Get("Content-Transfer-Encoding")
			ptMedia, ptParams, _ := mime.ParseMediaType(ct)

			if strings.HasPrefix(ptMedia, "multipart/") {
				if !walkForAttachments(ct, part, idx, fn) {
					return false
				}
				continue
			}

			if !isAttachmentPart(ptMedia) {
				continue
			}

			filename := part.FileName() // handles Content-Disposition filename and RFC 5987 encoding
			if filename == "" {
				filename = ptParams["name"]
			}
			if filename == "" {
				filename = fmt.Sprintf("attachment-%d", *idx)
			}
			info := AttachmentInfo{Index: *idx, Filename: filename, ContentType: ptMedia}
			*idx++
			if !fn(info, te, part) {
				return false
			}
		}
		return true
	}

	// Root-level single part: treat as attachment only if clearly not a body type.
	if isAttachmentPart(mediaType) {
		filename := fmt.Sprintf("attachment-%d", *idx)
		info := AttachmentInfo{Index: *idx, Filename: filename, ContentType: mediaType}
		te := "" // transfer encoding not available at this call level; caller handles separately
		*idx++
		return fn(info, te, body)
	}
	return true
}

func isAttachmentPart(mediaType string) bool {
	if mediaType == "" || mediaType == "text/plain" || mediaType == "text/html" {
		return false
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		return false
	}
	return true
}

func decodeBody(transferEncoding string, body io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	default:
		return body
	}
}
