package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"

	"comstac/internal/mailcontent"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// Config holds SMTP relay connection parameters.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // approved sender address
}

// Relay sends outbound mail through a smart-host.
type Relay struct {
	cfg         Config
	dialContext func(context.Context, string, string) (net.Conn, error)
	tlsConfig   *tls.Config
}

const sendTimeout = 45 * time.Second

// ErrDeliveryUnknown marks transport failure after DATA began. Remote
// acceptance cannot be inferred from a disconnected socket; never auto-retry.
var ErrDeliveryUnknown = errors.New("delivery could not be confirmed; check sent mail or with the recipient before resending")

func New(cfg Config) *Relay {
	return &Relay{cfg: cfg}
}

// Attachment holds a file to be included in an outbound message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// OutboundMessage is the input to Send.
type OutboundMessage struct {
	To          []string
	CC          []string
	BCC         []string // recipients only; not included in headers
	Subject     string
	BodyText    string
	InReplyTo   string // original Message-ID header value, empty for compose
	References  string
	Attachments []Attachment
}

// Send constructs a MIME message and delivers it through the relay.
// Returns the generated Message-ID and raw MIME bytes.
func (r *Relay) Send(ctx context.Context, msg OutboundMessage) (msgID string, rawMIME []byte, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	msg, rcptTo, err := normalizeRecipients(msg)
	if err != nil {
		return "", nil, err
	}
	from, err := mail.ParseAddress(r.cfg.From)
	if err != nil {
		return "", nil, fmt.Errorf("invalid approved sender: %w", err)
	}
	msgID = generateMessageID(from.Address)
	rawMIME, err = buildRawMIME(r.cfg.From, msg, msgID)
	if err != nil {
		return "", nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	addr := net.JoinHostPort(r.cfg.Host, strconv.Itoa(r.cfg.Port))
	dial := r.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return msgID, rawMIME, relayError(ctx, err, false)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsConfig := r.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: r.cfg.Host, MinVersion: tls.VersionTLS12}
	}
	c, err := gosmtp.NewClientStartTLS(conn, tlsConfig)
	if err != nil {
		return msgID, rawMIME, relayError(ctx, err, false)
	}
	defer c.Close()
	c.CommandTimeout = 15 * time.Second
	c.SubmissionTimeout = 40 * time.Second
	saslClient := sasl.NewPlainClient("", r.cfg.Username, r.cfg.Password)
	if ok, _ := c.Extension("AUTH"); !ok {
		return msgID, rawMIME, relayError(ctx, errors.New("server does not support AUTH"), false)
	}
	if err := c.Auth(saslClient); err != nil {
		return msgID, rawMIME, relayError(ctx, err, false)
	}

	if err := c.Mail(from.Address, nil); err != nil {
		return msgID, rawMIME, relayError(ctx, err, false)
	}
	for _, addr := range rcptTo {
		if err := c.Rcpt(addr, nil); err != nil {
			return msgID, rawMIME, relayError(ctx, err, false)
		}
	}
	w, err := c.Data()
	if err != nil {
		return msgID, rawMIME, relayError(ctx, err, false)
	}
	if _, err := io.Copy(w, bytes.NewReader(rawMIME)); err != nil {
		return msgID, rawMIME, relayError(ctx, err, true)
	}
	if err := w.Close(); err != nil {
		return msgID, rawMIME, relayError(ctx, err, true)
	}
	// The final DATA reply confirmed acceptance. QUIT/disconnect must not turn
	// that known success into a failure inviting a duplicate submission.
	return msgID, rawMIME, nil
}

func relayError(ctx context.Context, err error, afterDATA bool) error {
	cause := err
	if ctx.Err() != nil {
		cause = ctx.Err()
	}
	var rejection *gosmtp.SMTPError
	if afterDATA && !errors.As(err, &rejection) {
		return fmt.Errorf("%w: %w", ErrDeliveryUnknown, cause)
	}
	return fmt.Errorf("smtp relay: %w", cause)
}

// sanitizeHeaderValue strips CR and LF characters to prevent MIME header injection.
func sanitizeHeaderValue(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, v)
}

// ValidateMessage validates recipients and exact serialized size without
// allocating the serialized payload or contacting the relay.
func ValidateMessage(from string, msg OutboundMessage) error {
	normalized, _, err := normalizeRecipients(msg)
	if err != nil {
		return err
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return fmt.Errorf("invalid approved sender: %w", err)
	}
	return writeRawMIME(io.Discard, from, normalized, generateMessageID(sender.Address), mailcontent.MaxMessageBytes)
}

type mimeLimitWriter struct {
	dst          io.Writer
	bytes, limit int64
	err          error
}

func (w *mimeLimitWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if int64(len(p)) > w.limit-w.bytes {
		w.err = mailcontent.ErrTooLarge
		return 0, w.err
	}
	n, err := w.dst.Write(p)
	w.bytes += int64(n)
	w.err = err
	return n, err
}

func buildRawMIME(from string, msg OutboundMessage, msgID string) ([]byte, error) {
	var out bytes.Buffer
	if err := writeRawMIME(&out, from, msg, msgID, mailcontent.MaxMessageBytes); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeRawMIME(dst io.Writer, from string, msg OutboundMessage, msgID string, limit int64) error {
	out := &mimeLimitWriter{dst: dst, limit: limit}
	h := func(key, value string) { fmt.Fprintf(out, "%s: %s\r\n", key, sanitizeHeaderValue(value)) }
	h("From", from)
	h("To", strings.Join(msg.To, ", "))
	if len(msg.CC) > 0 {
		h("Cc", strings.Join(msg.CC, ", "))
	}
	h("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	h("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
	h("Message-ID", "<"+msgID+">")
	h("MIME-Version", "1.0")
	if msg.InReplyTo != "" {
		h("In-Reply-To", msg.InReplyTo)
		h("References", msg.References)
	}
	if len(msg.Attachments) == 0 {
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "8bit")
		io.WriteString(out, "\r\n")
		io.WriteString(out, msg.BodyText)
		return out.err
	}
	mw := multipart.NewWriter(out)
	h("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	io.WriteString(out, "\r\n")
	if out.err != nil {
		return out.err
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Content-Transfer-Encoding", "8bit")
	part, err := mw.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(part, msg.BodyText); err != nil {
		return err
	}
	for _, att := range msg.Attachments {
		contentType := att.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		media, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			return fmt.Errorf("invalid attachment content type")
		}
		header = make(textproto.MIMEHeader)
		header.Set("Content-Type", mime.FormatMediaType(media, params))
		header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": sanitizeHeaderValue(att.Filename)}))
		header.Set("Content-Transfer-Encoding", "base64")
		part, err = mw.CreatePart(header)
		if err != nil {
			return err
		}
		enc := base64.NewEncoder(base64.StdEncoding, &mimeLineWriter{dst: part})
		if _, err = enc.Write(att.Data); err != nil {
			return err
		}
		if err = enc.Close(); err != nil {
			return err
		}
	}
	if err = mw.Close(); err != nil {
		return err
	}
	return out.err
}

func generateMessageID(fromAddr string) string {
	domain := "localhost"
	if at := strings.LastIndex(fromAddr, "@"); at >= 0 {
		domain = fromAddr[at+1:]
	}
	return fmt.Sprintf("%d.%06x@%s", time.Now().UnixNano(), time.Now().UnixNano()&0xffffff, domain)
}

// MIME base64 lines are at most 76 characters, even for a large attachment.
type mimeLineWriter struct {
	dst    io.Writer
	column int
}

func (w *mimeLineWriter) Write(p []byte) (int, error) {
	consumed := 0
	for len(p) > 0 {
		if w.column == 76 {
			if _, err := io.WriteString(w.dst, "\r\n"); err != nil {
				return consumed, err
			}
			w.column = 0
		}
		n := min(76-w.column, len(p))
		written, err := w.dst.Write(p[:n])
		consumed += written
		w.column += written
		if err != nil {
			return consumed, err
		}
		if written != n {
			return consumed, io.ErrShortWrite
		}
		p = p[n:]
	}
	return consumed, nil
}

// From returns the approved sender address for this relay.
func (r *Relay) From() string { return r.cfg.From }

// ReplySubject returns the correct subject line for a reply.
func ReplySubject(original string) string {
	if strings.HasPrefix(strings.ToLower(original), "re:") {
		return original
	}
	return "Re: " + original
}

// ForwardSubject returns the correct subject line for a forward.
func ForwardSubject(original string) string {
	lower := strings.ToLower(original)
	if strings.HasPrefix(lower, "fwd:") || strings.HasPrefix(lower, "fw:") {
		return original
	}
	return "Fwd: " + original
}
