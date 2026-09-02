package config

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime options for the single-binary service.
type Config struct {
	SMTPAddr        string
	SMTPDomain      string
	HTTPAddr        string
	DBPath          string
	LocalDomains    []string
	LocalRecipients []string
	AdminUsername   string
	AdminPassword   string
	SessionTTL      time.Duration
	CSRFSecret      string

	// Outbound SMTP relay
	RelayHost     string
	RelayPort     int
	RelayUsername string
	RelayPassword string
	RelayFrom     string

	// IMAP / Gmail OAuth2
	IMAPAddr         string
	IMAPUsername     string
	IMAPMailbox      string
	IMAPPollInterval time.Duration
	IMAPClientID     string
	IMAPClientSecret string
	IMAPRefreshToken string

	// Base URL for server-side OAuth callbacks (e.g. https://mail.example.com)
	BaseURL string
	// Web Application OAuth2 credentials for browser-based re-authorization.
	// Separate from IMAPClientID/Secret (which are Desktop app credentials).
	OAuthClientID     string
	OAuthClientSecret string

	// Web Push (VAPID)
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string // e.g. "mailto:you@example.com"

	// Agent SSE (ghost-mail integration)
	AgentToken string

	// Backup
	BackupDir    string
	BackupDest   string // remote SCP destination, e.g. user@host:/path/to/backups/
	BackupKey    string // path to SSH private key (optional)
	BackupRetain int    // number of local snapshots to keep (default 7)
}

var requiredServerEnv = []string{
	"COMSTAC_SMTP_ADDR",
	"COMSTAC_SMTP_DOMAIN",
	"COMSTAC_HTTP_ADDR",
	"COMSTAC_DB_PATH",
	"COMSTAC_LOCAL_DOMAINS",
	"COMSTAC_LOCAL_RECIPIENTS",
	"COMSTAC_ADMIN_USERNAME",
	"COMSTAC_ADMIN_PASSWORD",
	"COMSTAC_CSRF_SECRET",
}

// Load reads and validates the configuration used by the long-running server.
// Command-line maintenance helpers intentionally continue to read only the
// variables they need, so a missing server variable cannot block key generation
// or OAuth authorization.
func Load() (Config, error) {
	cfg := FromEnv()

	missing := make([]string, 0)
	for _, key := range requiredServerEnv {
		if value, ok := os.LookupEnv(key); !ok || strings.Trim(strings.TrimSpace(value), `"'`) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// FromEnv builds configuration from environment variables with sensible defaults.
func FromEnv() Config {
	ttlHours := int64(24)
	if raw := envOr("COMSTAC_SESSION_TTL_HOURS", "24"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			ttlHours = parsed
		}
	}

	relayPort := 587
	if raw := envOr("COMSTAC_RELAY_PORT", ""); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			relayPort = parsed
		}
	}

	pollInterval := 60 * time.Second
	if raw := envOr("COMSTAC_IMAP_POLL_INTERVAL", ""); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			pollInterval = d
		}
	}

	return Config{
		SMTPAddr:        envOr("COMSTAC_SMTP_ADDR", ":2525"),
		SMTPDomain:      envOr("COMSTAC_SMTP_DOMAIN", "localhost"),
		HTTPAddr:        envOr("COMSTAC_HTTP_ADDR", ":8080"),
		DBPath:          envOr("COMSTAC_DB_PATH", "./comstac.db"),
		LocalDomains:    splitCSV(envOr("COMSTAC_LOCAL_DOMAINS", "")),
		LocalRecipients: splitCSV(envOr("COMSTAC_LOCAL_RECIPIENTS", "")),
		AdminUsername:   envOr("COMSTAC_ADMIN_USERNAME", "admin"),
		AdminPassword:   envOr("COMSTAC_ADMIN_PASSWORD", "changeme123"),
		SessionTTL:      time.Duration(ttlHours) * time.Hour,
		CSRFSecret:      envOr("COMSTAC_CSRF_SECRET", ""),

		RelayHost:     envOr("COMSTAC_RELAY_HOST", ""),
		RelayPort:     relayPort,
		RelayUsername: envOr("COMSTAC_RELAY_USERNAME", ""),
		RelayPassword: envOr("COMSTAC_RELAY_PASSWORD", ""),
		RelayFrom:     envOr("COMSTAC_RELAY_FROM", ""),

		BaseURL:           envOr("COMSTAC_BASE_URL", ""),
		OAuthClientID:     envOr("COMSTAC_OAUTH_CLIENT_ID", ""),
		OAuthClientSecret: envOr("COMSTAC_OAUTH_CLIENT_SECRET", ""),

		IMAPAddr:         envOr("COMSTAC_IMAP_ADDR", "imap.gmail.com:993"),
		IMAPUsername:     envOr("COMSTAC_IMAP_USERNAME", ""),
		IMAPMailbox:      envOr("COMSTAC_IMAP_MAILBOX", "INBOX"),
		IMAPPollInterval: pollInterval,
		IMAPClientID:     envOr("COMSTAC_IMAP_CLIENT_ID", ""),
		IMAPClientSecret: envOr("COMSTAC_IMAP_CLIENT_SECRET", ""),
		IMAPRefreshToken: envOr("COMSTAC_IMAP_REFRESH_TOKEN", ""),

		VAPIDPublicKey:  envOr("COMSTAC_VAPID_PUBLIC", ""),
		VAPIDPrivateKey: envOr("COMSTAC_VAPID_PRIVATE", ""),
		VAPIDSubject:    envOr("COMSTAC_VAPID_SUBJECT", ""),

		BackupDir:    envOr("COMSTAC_BACKUP_DIR", "/var/lib/comstac/backups"),
		BackupDest:   envOr("COMSTAC_BACKUP_DEST", ""),
		BackupKey:    envOr("COMSTAC_BACKUP_KEY", ""),
		BackupRetain: parseIntOr(envOr("COMSTAC_BACKUP_RETAIN", ""), 7),

		AgentToken: envOr("COMSTAC_AGENT_TOKEN", ""),
	}
}

// Validate rejects unsafe or internally inconsistent server configuration.
// Errors name variables but never include their values.
func (c Config) Validate() error {
	problems := make([]string, 0)

	if !validListenAddr(c.SMTPAddr, false) {
		problems = append(problems, "COMSTAC_SMTP_ADDR must be a valid TCP listen address")
	}
	if !validListenAddr(c.HTTPAddr, true) {
		problems = append(problems, "COMSTAC_HTTP_ADDR must be a loopback-only TCP listen address")
	}
	if strings.TrimSpace(c.SMTPDomain) == "" || strings.EqualFold(strings.TrimSpace(c.SMTPDomain), "localhost") {
		problems = append(problems, "COMSTAC_SMTP_DOMAIN must be an explicit non-localhost hostname")
	}
	if !filepath.IsAbs(c.DBPath) {
		problems = append(problems, "COMSTAC_DB_PATH must be an absolute path")
	}
	if strings.TrimSpace(c.AdminUsername) == "" {
		problems = append(problems, "COMSTAC_ADMIN_USERNAME is required")
	}
	if len(c.AdminPassword) < 12 || c.AdminPassword == "changeme123" {
		problems = append(problems, "COMSTAC_ADMIN_PASSWORD must be at least 12 characters and not the example default")
	}
	if len(c.CSRFSecret) < 32 {
		problems = append(problems, "COMSTAC_CSRF_SECRET must be at least 32 characters")
	}
	if c.SessionTTL < time.Hour || c.SessionTTL > 30*24*time.Hour {
		problems = append(problems, "COMSTAC_SESSION_TTL_HOURS must be between one hour and 30 days")
	}

	domains := make(map[string]struct{}, len(c.LocalDomains))
	for _, domain := range c.LocalDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" || strings.ContainsAny(domain, "@ /\\") || !strings.Contains(domain, ".") {
			problems = append(problems, "COMSTAC_LOCAL_DOMAINS contains an invalid domain")
			continue
		}
		domains[domain] = struct{}{}
	}
	if len(domains) == 0 {
		problems = append(problems, "COMSTAC_LOCAL_DOMAINS must contain at least one domain")
	}
	if len(c.LocalRecipients) == 0 {
		problems = append(problems, "COMSTAC_LOCAL_RECIPIENTS must contain at least one recipient")
	}
	for _, recipient := range c.LocalRecipients {
		parsed, err := mail.ParseAddress(recipient)
		if err != nil || !strings.EqualFold(parsed.Address, recipient) {
			problems = append(problems, "COMSTAC_LOCAL_RECIPIENTS contains an invalid address")
			continue
		}
		parts := strings.Split(parsed.Address, "@")
		if len(parts) != 2 {
			problems = append(problems, "COMSTAC_LOCAL_RECIPIENTS contains an invalid address")
			continue
		}
		if _, ok := domains[strings.ToLower(parts[1])]; !ok {
			problems = append(problems, "COMSTAC_LOCAL_RECIPIENTS contains an address outside COMSTAC_LOCAL_DOMAINS")
		}
	}

	validateAllOrNone(&problems, "outbound relay", map[string]string{
		"COMSTAC_RELAY_HOST": c.RelayHost, "COMSTAC_RELAY_USERNAME": c.RelayUsername,
		"COMSTAC_RELAY_PASSWORD": c.RelayPassword, "COMSTAC_RELAY_FROM": c.RelayFrom,
	})
	if c.RelayHost != "" && (c.RelayPort < 1 || c.RelayPort > 65535) {
		problems = append(problems, "COMSTAC_RELAY_PORT must be between 1 and 65535")
	}
	if c.RelayFrom != "" {
		if _, err := mail.ParseAddress(c.RelayFrom); err != nil {
			problems = append(problems, "COMSTAC_RELAY_FROM must be a valid address")
		}
	}

	validateAllOrNone(&problems, "IMAP OAuth", map[string]string{
		"COMSTAC_IMAP_USERNAME": c.IMAPUsername, "COMSTAC_IMAP_CLIENT_ID": c.IMAPClientID,
		"COMSTAC_IMAP_CLIENT_SECRET": c.IMAPClientSecret,
	})
	validateAllOrNone(&problems, "web OAuth", map[string]string{
		"COMSTAC_BASE_URL": c.BaseURL, "COMSTAC_OAUTH_CLIENT_ID": c.OAuthClientID,
		"COMSTAC_OAUTH_CLIENT_SECRET": c.OAuthClientSecret,
	})
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			problems = append(problems, "COMSTAC_BASE_URL must be an HTTPS origin without credentials, query or fragment")
		}
	}
	validateAllOrNone(&problems, "VAPID", map[string]string{
		"COMSTAC_VAPID_PUBLIC": c.VAPIDPublicKey, "COMSTAC_VAPID_PRIVATE": c.VAPIDPrivateKey,
		"COMSTAC_VAPID_SUBJECT": c.VAPIDSubject,
	})
	if c.VAPIDSubject != "" {
		u, err := url.Parse(c.VAPIDSubject)
		if err != nil || (u.Scheme != "mailto" && u.Scheme != "https") || u.Opaque == "" && u.Host == "" {
			problems = append(problems, "COMSTAC_VAPID_SUBJECT must be a valid mailto or HTTPS URL")
		}
	}
	if c.AgentToken != "" && len(c.AgentToken) < 32 {
		problems = append(problems, "COMSTAC_AGENT_TOKEN must be at least 32 characters when enabled")
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

func validListenAddr(addr string, loopbackOnly bool) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return false
	}
	if !loopbackOnly {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateAllOrNone(problems *[]string, group string, values map[string]string) {
	set := 0
	missing := make([]string, 0)
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		} else {
			set++
		}
	}
	if set == 0 || set == len(values) {
		return
	}
	sort.Strings(missing)
	*problems = append(*problems, fmt.Sprintf("%s configuration is incomplete (missing %s)", group, strings.Join(missing, ", ")))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		// Strip surrounding quotes if the value was written as key="value" in a .env file.
		v = strings.Trim(v, `"'`)
		return v
	}
	return fallback
}

func parseIntOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		clean := strings.ToLower(strings.TrimSpace(p))
		if clean == "" {
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
