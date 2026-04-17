package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/textproto"
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
	cfg Config
}

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
func (r *Relay) Send(_ context.Context, msg OutboundMessage) (msgID string, rawMIME []byte, err error) {
	msgID = generateMessageID(r.cfg.From)
	rawMIME = buildRawMIME(r.cfg.From, msg, msgID)

	addr := fmt.Sprintf("%s:%d", r.cfg.Host, r.cfg.Port)
	saslClient := sasl.NewPlainClient("", r.cfg.Username, r.cfg.Password)

	rcptTo := make([]string, 0, len(msg.To)+len(msg.CC)+len(msg.BCC))
	rcptTo = append(rcptTo, msg.To...)
	rcptTo = append(rcptTo, msg.CC...)
	rcptTo = append(rcptTo, msg.BCC...)
	if err := gosmtp.SendMail(addr, saslClient, r.cfg.From, rcptTo, bytes.NewReader(rawMIME)); err != nil {
		return msgID, rawMIME, fmt.Errorf("smtp relay: %w", err)
	}
	return msgID, rawMIME, nil
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

func buildRawMIME(from string, msg OutboundMessage, msgID string) []byte {
	to := strings.Join(msg.To, ", ")
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")

	if len(msg.Attachments) == 0 {
		var b strings.Builder
		h := func(k, v string) { b.WriteString(k + ": " + sanitizeHeaderValue(v) + "\r\n") }
		h("From", from)
		h("To", to)
		if len(msg.CC) > 0 {
			h("Cc", strings.Join(msg.CC, ", "))
		}
		h("Subject", msg.Subject)
		h("Date", date)
		h("Message-ID", "<"+msgID+">")
		h("MIME-Version", "1.0")
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "8bit")
		if msg.InReplyTo != "" {
			h("In-Reply-To", msg.InReplyTo)
			h("References", msg.References)
		}
		b.WriteString("\r\n")
		b.WriteString(msg.BodyText)
		return []byte(b.String())
	}

	// Build multipart/mixed body first so we have the boundary before writing headers.
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	boundary := mw.Boundary()

	// Text part
	th := make(textproto.MIMEHeader)
	th.Set("Content-Type", "text/plain; charset=utf-8")
	th.Set("Content-Transfer-Encoding", "8bit")
	if pw, err := mw.CreatePart(th); err == nil {
		pw.Write([]byte(msg.BodyText)) //nolint:errcheck
	}

	// Attachment parts — base64 encoded
	for _, att := range msg.Attachments {
		ah := make(textproto.MIMEHeader)
		ct := att.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.Set("Content-Type", ct)
		ah.Set("Content-Disposition", `attachment; filename="`+sanitizeHeaderValue(att.Filename)+`"`)
		ah.Set("Content-Transfer-Encoding", "base64")
		if pw, err := mw.CreatePart(ah); err == nil {
			enc := base64.NewEncoder(base64.StdEncoding, pw)
			enc.Write(att.Data) //nolint:errcheck
			enc.Close()
		}
	}
	mw.Close()

	// Outer message headers
	var out bytes.Buffer
	wh := func(k, v string) { out.WriteString(k + ": " + sanitizeHeaderValue(v) + "\r\n") }
	wh("From", from)
	wh("To", to)
	if len(msg.CC) > 0 {
		wh("Cc", strings.Join(msg.CC, ", "))
	}
	wh("Subject", msg.Subject)
	wh("Date", date)
	wh("Message-ID", "<"+msgID+">")
	wh("MIME-Version", "1.0")
	wh("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	if msg.InReplyTo != "" {
		wh("In-Reply-To", msg.InReplyTo)
		wh("References", msg.References)
	}
	out.WriteString("\r\n")
	out.Write(bodyBuf.Bytes())
	return out.Bytes()
}

func generateMessageID(fromAddr string) string {
	domain := "localhost"
	if at := strings.LastIndex(fromAddr, "@"); at >= 0 {
		domain = fromAddr[at+1:]
	}
	return fmt.Sprintf("%d.%x@%s", time.Now().UnixNano(), time.Now().UnixNano()&0xffffff, domain)
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
