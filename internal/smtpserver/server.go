package smtpserver

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"strings"
	stdsync "sync"
	"time"

	"comstac/internal/ingest"
	gosmtp "github.com/emersion/go-smtp"
)

type Options struct {
	Domain           string
	LocalDomains     []string
	LocalRecipients  []string
	ResolveAccountID func(ctx context.Context, recipient string) (sql.NullInt64, error)
}

type Server struct {
	addr     string
	ingestor *ingest.Service
	opts     Options
}

func New(addr string, ingestor *ingest.Service, opts Options) *Server {
	return &Server{addr: addr, ingestor: ingestor, opts: opts}
}

func (s *Server) Run(ctx context.Context) error {
	srv := s.newSMTPServer()
	slog.Info("smtp listener starting", "component", "smtp", "addr", s.addr)
	return runWithContext(ctx, srv, srv.ListenAndServe)
}

// RunWithListener allows tests to start the SMTP server on an ephemeral listener.
func (s *Server) RunWithListener(ctx context.Context, ln net.Listener) error {
	srv := s.newSMTPServer()
	slog.Info("smtp listener starting", "component", "smtp", "addr", ln.Addr().String())
	return runWithContext(ctx, srv, func() error {
		return srv.Serve(ln)
	})
}

func (s *Server) newSMTPServer() *gosmtp.Server {
	backend := &backend{ingestor: s.ingestor, opts: s.opts}
	srv := gosmtp.NewServer(backend)
	srv.Addr = s.addr
	domain := s.opts.Domain
	if domain == "" {
		domain = "localhost"
	}
	srv.Domain = domain
	srv.AllowInsecureAuth = true
	srv.ReadTimeout = 5 * time.Minute
	srv.WriteTimeout = 5 * time.Minute
	srv.MaxMessageBytes = 25 * 1024 * 1024
	srv.MaxRecipients = 100
	return srv
}

func runWithContext(ctx context.Context, srv *gosmtp.Server, serve func() error) error {
	var wg stdsync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		if err := srv.Close(); err != nil {
			slog.Error("smtp close", "component", "smtp", "err", err)
		}
	}()

	err := serve()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type backend struct {
	ingestor *ingest.Service
	opts     Options
}

func (b *backend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	var remoteIP net.IP
	if addr, ok := c.Conn().RemoteAddr().(*net.TCPAddr); ok {
		remoteIP = addr.IP
	}
	return &session{ingestor: b.ingestor, opts: normalizeOptions(b.opts), remoteIP: remoteIP}, nil
}

type session struct {
	ingestor     *ingest.Service
	opts         Options
	remoteIP     net.IP
	envelopeFrom string
	envelopeTo   []string
}

func (s *session) Mail(from string, _ *gosmtp.MailOptions) error {
	s.envelopeFrom = from
	s.envelopeTo = s.envelopeTo[:0]
	return nil
}

func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	addr, err := normalizeAddress(to)
	if err != nil {
		return smtpError(550, "invalid recipient")
	}
	if err := s.validateRecipient(addr); err != nil {
		return err
	}
	s.envelopeTo = append(s.envelopeTo, addr)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if len(s.envelopeTo) == 0 {
		return smtpError(554, "no valid recipients")
	}

	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	slog.Info("smtp DATA received", "component", "smtp", "bytes", len(buf), "from", s.envelopeFrom, "to", s.envelopeTo)

	accountID := sql.NullInt64{}
	if s.opts.ResolveAccountID != nil && len(s.envelopeTo) > 0 {
		resolved, resolveErr := s.opts.ResolveAccountID(context.Background(), s.envelopeTo[0])
		if resolveErr != nil {
			return smtpError(451, "temporary account routing failure")
		}
		accountID = resolved
	}

	return s.ingestor.IngestRaw(context.Background(), ingest.IngestInput{
		Source:       ingest.SourceSMTP,
		AccountID:    accountID,
		EnvelopeFrom: s.envelopeFrom,
		EnvelopeTo:   append([]string(nil), s.envelopeTo...),
		RawMIME:      buf,
		RemoteIP:     s.remoteIP,
	})
}

func (s *session) Reset() {}

func (s *session) Logout() error { return nil }

func (s *session) validateRecipient(addr string) error {
	domain := domainPart(addr)
	if len(s.opts.LocalDomains) > 0 && !contains(s.opts.LocalDomains, domain) {
		return smtpError(550, fmt.Sprintf("relaying denied for domain %s", domain))
	}
	if len(s.opts.LocalRecipients) > 0 && !contains(s.opts.LocalRecipients, addr) {
		return smtpError(550, fmt.Sprintf("unknown local recipient %s", addr))
	}
	return nil
}

func normalizeOptions(opts Options) Options {
	out := Options{
		LocalDomains:     make([]string, 0, len(opts.LocalDomains)),
		LocalRecipients:  make([]string, 0, len(opts.LocalRecipients)),
		ResolveAccountID: opts.ResolveAccountID,
	}
	for _, d := range opts.LocalDomains {
		if clean := strings.ToLower(strings.TrimSpace(d)); clean != "" {
			out.LocalDomains = append(out.LocalDomains, clean)
		}
	}
	for _, r := range opts.LocalRecipients {
		if clean, err := normalizeAddress(r); err == nil {
			out.LocalRecipients = append(out.LocalRecipients, clean)
		}
	}
	return out
}

func normalizeAddress(raw string) (string, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		if strings.Contains(raw, "@") {
			return strings.ToLower(strings.TrimSpace(raw)), nil
		}
		return "", err
	}
	return strings.ToLower(parsed.Address), nil
}

func domainPart(addr string) string {
	parts := strings.Split(addr, "@")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func smtpError(code int, msg string) error {
	return &gosmtp.SMTPError{Code: code, Message: msg}
}
