package config

import (
	"os"
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
