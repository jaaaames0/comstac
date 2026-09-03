package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		SMTPAddr:             ":2525",
		SMTPDomain:           "mail.example.com",
		SMTPTLSCert:          "/etc/comstac/tls/fullchain.pem",
		SMTPTLSKey:           "/etc/comstac/tls/privkey.pem",
		HTTPAddr:             "127.0.0.1:8080",
		DBPath:               "/var/lib/comstac/comstac.db",
		LocalDomains:         []string{"example.com"},
		LocalRecipients:      []string{"mail@example.com"},
		AdminUsername:        "operator",
		AdminPassword:        "a-long-test-password",
		SessionTTL:           24 * time.Hour,
		CSRFSecret:           "0123456789abcdef0123456789abcdef",
		StorageMaxBytes:      10 << 30,
		StorageMinFreeBytes:  50 << 30,
		StorageWarnFreeBytes: 75 << 30,
		RelayPort:            587,
	}
}

func TestValidateAcceptsSafeMinimalConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsSessionTTLBelowOneHour(t *testing.T) {
	cfg := validConfig()
	cfg.SessionTTL = 59 * time.Minute

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "between one hour and 30 days") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsUnsafeDefaultsAndOpenRecipientPolicy(t *testing.T) {
	cfg := validConfig()
	cfg.HTTPAddr = ":8080"
	cfg.AdminPassword = "changeme123"
	cfg.CSRFSecret = ""
	cfg.LocalDomains = nil
	cfg.LocalRecipients = nil

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted unsafe config")
	}
	for _, want := range []string{
		"loopback-only", "COMSTAC_ADMIN_PASSWORD", "COMSTAC_CSRF_SECRET",
		"COMSTAC_LOCAL_DOMAINS", "COMSTAC_LOCAL_RECIPIENTS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error %q does not contain %q", err, want)
		}
	}
}

func TestValidateRejectsIncompleteOptionalGroups(t *testing.T) {
	cfg := validConfig()
	cfg.RelayHost = "smtp.example.com"
	cfg.VAPIDPublicKey = "public"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted incomplete optional groups")
	}
	for _, want := range []string{"outbound relay", "VAPID"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error %q does not contain %q", err, want)
		}
	}
}

func TestLoadRequiresExplicitServerEnvironment(t *testing.T) {
	for _, key := range requiredServerEnv {
		t.Setenv(key, "")
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "missing required environment variables") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadAcceptsExplicitSafeEnvironment(t *testing.T) {
	values := map[string]string{
		"COMSTAC_SMTP_ADDR":               ":2525",
		"COMSTAC_SMTP_DOMAIN":             "mail.example.com",
		"COMSTAC_SMTP_TLS_CERT":           "/etc/comstac/tls/fullchain.pem",
		"COMSTAC_SMTP_TLS_KEY":            "/etc/comstac/tls/privkey.pem",
		"COMSTAC_HTTP_ADDR":               "127.0.0.1:8080",
		"COMSTAC_DB_PATH":                 "/var/lib/comstac/comstac.db",
		"COMSTAC_LOCAL_DOMAINS":           "example.com",
		"COMSTAC_LOCAL_RECIPIENTS":        "mail@example.com",
		"COMSTAC_ADMIN_USERNAME":          "operator",
		"COMSTAC_ADMIN_PASSWORD":          "a-long-test-password",
		"COMSTAC_CSRF_SECRET":             "0123456789abcdef0123456789abcdef",
		"COMSTAC_STORAGE_MAX_BYTES":       "10737418240",
		"COMSTAC_STORAGE_MIN_FREE_BYTES":  "53687091200",
		"COMSTAC_STORAGE_WARN_FREE_BYTES": "80530636800",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestValidateRejectsInvalidStorageLimits(t *testing.T) {
	cfg := validConfig()
	cfg.StorageMaxBytes = 0
	cfg.StorageWarnFreeBytes = cfg.StorageMinFreeBytes

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted invalid storage limits")
	}
	for _, want := range []string{"COMSTAC_STORAGE_MAX_BYTES", "COMSTAC_STORAGE_WARN_FREE_BYTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error %q does not contain %q", err, want)
		}
	}
}

func TestLoadRejectsMalformedStorageLimit(t *testing.T) {
	values := map[string]string{
		"COMSTAC_SMTP_ADDR": ":2525", "COMSTAC_SMTP_DOMAIN": "mail.example.com",
		"COMSTAC_SMTP_TLS_CERT": "/etc/comstac/tls/fullchain.pem", "COMSTAC_SMTP_TLS_KEY": "/etc/comstac/tls/privkey.pem",
		"COMSTAC_HTTP_ADDR": "127.0.0.1:8080", "COMSTAC_DB_PATH": "/var/lib/comstac/comstac.db",
		"COMSTAC_LOCAL_DOMAINS": "example.com", "COMSTAC_LOCAL_RECIPIENTS": "mail@example.com",
		"COMSTAC_ADMIN_USERNAME": "operator", "COMSTAC_ADMIN_PASSWORD": "a-long-test-password",
		"COMSTAC_CSRF_SECRET":       "0123456789abcdef0123456789abcdef",
		"COMSTAC_STORAGE_MAX_BYTES": "ten-gibibytes", "COMSTAC_STORAGE_MIN_FREE_BYTES": "53687091200",
		"COMSTAC_STORAGE_WARN_FREE_BYTES": "80530636800",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "COMSTAC_STORAGE_MAX_BYTES") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestValidateRejectsInvalidSMTPTLSPaths(t *testing.T) {
	cfg := validConfig()
	cfg.SMTPTLSCert = "relative-cert.pem"
	cfg.SMTPTLSKey = cfg.SMTPTLSCert

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted invalid SMTP TLS paths")
	}
	for _, want := range []string{"COMSTAC_SMTP_TLS_CERT", "COMSTAC_SMTP_TLS_KEY", "different paths"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error %q does not contain %q", err, want)
		}
	}
}
