package smtpserver

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"strings"
	stdsync "sync"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/securitymetrics"
	"comstac/internal/storageguard"
	gosmtp "github.com/emersion/go-smtp"
)

type Options struct {
	Domain           string
	LocalDomains     []string
	LocalRecipients  []string
	ResolveAccountID func(ctx context.Context, recipient string) (sql.NullInt64, error)
	MaxConnections   int
	MaxDataWorkers   int
	MaxMessageBytes  int64
	MaxRecipients    int
	DataTimeout      time.Duration
	CheckStorage     func(payloadBytes int64) error
	TLSConfig        *tls.Config
	SecurityCounters *securitymetrics.Counters
}

type Server struct {
	addr     string
	ingestor *ingest.Service
	opts     Options
}

func New(addr string, ingestor *ingest.Service, opts Options) *Server {
	return &Server{addr: addr, ingestor: ingestor, opts: normalizeOptions(opts)}
}

func (s *Server) Run(ctx context.Context) error {
	srv := s.newSMTPServer()
	slog.Info("smtp listener starting", "component", "smtp", "addr", s.addr)
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	limited := newLimitedListener(ln, s.opts.MaxConnections, s.opts.SecurityCounters)
	return runWithContext(ctx, srv, func() error { return srv.Serve(limited) })
}

// RunWithListener allows tests to start the SMTP server on an ephemeral listener.
func (s *Server) RunWithListener(ctx context.Context, ln net.Listener) error {
	srv := s.newSMTPServer()
	slog.Info("smtp listener starting", "component", "smtp", "addr", ln.Addr().String())
	limited := newLimitedListener(ln, s.opts.MaxConnections, s.opts.SecurityCounters)
	return runWithContext(ctx, srv, func() error {
		return srv.Serve(limited)
	})
}

func (s *Server) newSMTPServer() *gosmtp.Server {
	backend := &backend{
		ingestor:  s.ingestor,
		opts:      s.opts,
		dataSlots: make(chan struct{}, s.opts.MaxDataWorkers),
	}
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
	srv.MaxMessageBytes = s.opts.MaxMessageBytes
	srv.MaxRecipients = s.opts.MaxRecipients
	srv.TLSConfig = s.opts.TLSConfig
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
	ingestor  *ingest.Service
	opts      Options
	dataSlots chan struct{}
}

func (b *backend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	var remoteIP net.IP
	if addr, ok := c.Conn().RemoteAddr().(*net.TCPAddr); ok {
		remoteIP = addr.IP
	}
	return &session{ingestor: b.ingestor, opts: b.opts, remoteIP: remoteIP, dataSlots: b.dataSlots}, nil
}

type session struct {
	ingestor     *ingest.Service
	opts         Options
	remoteIP     net.IP
	envelopeFrom string
	envelopeTo   []string
	dataSlots    chan struct{}
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
	select {
	case s.dataSlots <- struct{}{}:
		defer func() { <-s.dataSlots }()
	default:
		s.opts.SecurityCounters.RecordSMTPSaturationRejection()
		slog.Warn("smtp DATA temporarily rejected", "component", "smtp", "reason", "workers saturated")
		return smtpError(451, "server temporarily busy")
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.opts.DataTimeout)
	defer cancel()
	if s.opts.CheckStorage != nil {
		if err := s.opts.CheckStorage(s.opts.MaxMessageBytes); err != nil {
			s.opts.SecurityCounters.RecordSMTPTemporaryRejection()
			slog.Warn("smtp DATA temporarily rejected", "component", "smtp", "reason", "storage capacity")
			return smtpError(452, "insufficient system storage")
		}
	}

	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	slog.Info("smtp DATA received", "component", "smtp", "bytes", len(buf), "from", s.envelopeFrom, "to", s.envelopeTo)

	accountID := sql.NullInt64{}
	if s.opts.ResolveAccountID != nil && len(s.envelopeTo) > 0 {
		resolved, resolveErr := s.opts.ResolveAccountID(ctx, s.envelopeTo[0])
		if resolveErr != nil {
			s.opts.SecurityCounters.RecordSMTPTemporaryRejection()
			return smtpError(451, "temporary account routing failure")
		}
		accountID = resolved
	}

	err = s.ingestor.IngestRaw(ctx, ingest.IngestInput{
		Source:       ingest.SourceSMTP,
		AccountID:    accountID,
		EnvelopeFrom: s.envelopeFrom,
		EnvelopeTo:   append([]string(nil), s.envelopeTo...),
		RawMIME:      buf,
		RemoteIP:     s.remoteIP,
	})
	if errors.Is(err, storageguard.ErrUnavailable) {
		s.opts.SecurityCounters.RecordSMTPTemporaryRejection()
		slog.Warn("smtp DATA temporarily rejected", "component", "smtp", "reason", "storage capacity")
		return smtpError(452, "insufficient system storage")
	}
	return err
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
		Domain:           strings.TrimSpace(opts.Domain),
		LocalDomains:     make([]string, 0, len(opts.LocalDomains)),
		LocalRecipients:  make([]string, 0, len(opts.LocalRecipients)),
		ResolveAccountID: opts.ResolveAccountID,
		MaxConnections:   opts.MaxConnections,
		MaxDataWorkers:   opts.MaxDataWorkers,
		MaxMessageBytes:  opts.MaxMessageBytes,
		MaxRecipients:    opts.MaxRecipients,
		DataTimeout:      opts.DataTimeout,
		CheckStorage:     opts.CheckStorage,
		SecurityCounters: opts.SecurityCounters,
	}
	if opts.TLSConfig != nil {
		out.TLSConfig = opts.TLSConfig.Clone()
	}
	if out.MaxConnections <= 0 {
		out.MaxConnections = 32
	}
	if out.MaxDataWorkers <= 0 {
		out.MaxDataWorkers = 4
	}
	if out.MaxMessageBytes <= 0 {
		out.MaxMessageBytes = 25 * 1024 * 1024
	}
	if out.MaxRecipients <= 0 {
		out.MaxRecipients = 100
	}
	if out.DataTimeout <= 0 {
		out.DataTimeout = 2 * time.Minute
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

type limitedListener struct {
	net.Listener
	slots    chan struct{}
	security *securitymetrics.Counters
}

func newLimitedListener(ln net.Listener, maxConnections int, counters *securitymetrics.Counters) net.Listener {
	return &limitedListener{Listener: ln, slots: make(chan struct{}, maxConnections), security: counters}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			l.security.RecordSMTPSaturationRejection()
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(conn, "421 4.3.2 server temporarily busy\r\n")
			_ = conn.Close()
			slog.Warn("smtp connection temporarily rejected", "component", "smtp", "reason", "connection limit reached")
		}
	}
}

type limitedConn struct {
	net.Conn
	releaseOnce stdsync.Once
	release     func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
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
