package relay

import (
	"context"
	"fmt"
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

// OutboundMessage is the input to Send.
type OutboundMessage struct {
	To         string
	CC         []string
	BCC        []string // recipients only; not included in headers
	Subject    string
	BodyText   string
	InReplyTo  string // original Message-ID header value, empty for compose
	References string
}

// Send constructs a MIME message and delivers it through the relay.
// Returns the generated Message-ID and raw MIME bytes.
func (r *Relay) Send(_ context.Context, msg OutboundMessage) (msgID string, rawMIME []byte, err error) {
	msgID = generateMessageID(r.cfg.From)
	rawMIME = buildRawMIME(r.cfg.From, msg, msgID)

	addr := fmt.Sprintf("%s:%d", r.cfg.Host, r.cfg.Port)
	saslClient := sasl.NewPlainClient("", r.cfg.Username, r.cfg.Password)

	rcptTo := []string{msg.To}
	rcptTo = append(rcptTo, msg.CC...)
	rcptTo = append(rcptTo, msg.BCC...)
	if err := gosmtp.SendMail(addr, saslClient, r.cfg.From, rcptTo, strings.NewReader(string(rawMIME))); err != nil {
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
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + sanitizeHeaderValue(v) + "\r\n") }

	h("From", from)
	h("To", msg.To)
	if len(msg.CC) > 0 {
		h("Cc", strings.Join(msg.CC, ", "))
	}
	// BCC is intentionally omitted from headers
	h("Subject", msg.Subject)
	h("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
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
