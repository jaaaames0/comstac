package smtpserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"time"
)

// LoadTLSConfig loads and validates the dedicated inbound SMTP certificate.
// Errors deliberately omit paths and certificate contents because production
// configuration values must not be copied into logs.
func LoadTLSConfig(certPath, keyPath, serverName string, now time.Time) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, errors.New("inbound SMTP TLS certificate/key is unreadable or invalid")
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("inbound SMTP TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("inbound SMTP TLS leaf certificate is invalid")
	}
	serverName = strings.TrimSpace(serverName)
	if err := leaf.VerifyHostname(serverName); err != nil {
		return nil, errors.New("inbound SMTP TLS certificate does not cover COMSTAC_SMTP_DOMAIN")
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("inbound SMTP TLS certificate is not currently valid")
	}
	pair.Leaf = leaf
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
