package smtpserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"sync/atomic"
	"time"
)

// CertificateReloader owns the currently active inbound SMTP certificate.
// Reload validates a complete replacement before atomically publishing it, so
// failed renewals cannot displace the last known-good in-memory certificate.
type CertificateReloader struct {
	certPath   string
	keyPath    string
	serverName string
	current    atomic.Pointer[tls.Certificate]
}

// NewCertificateReloader loads and validates the initial certificate. The
// returned reloader is ready to supply a TLS config to the SMTP server.
func NewCertificateReloader(certPath, keyPath, serverName string, now time.Time) (*CertificateReloader, error) {
	pair, err := loadValidatedCertificate(certPath, keyPath, serverName, now)
	if err != nil {
		return nil, err
	}
	r := &CertificateReloader{
		certPath:   certPath,
		keyPath:    keyPath,
		serverName: strings.TrimSpace(serverName),
	}
	r.current.Store(pair)
	return r, nil
}

// TLSConfig selects the current certificate for each new handshake. Existing
// TLS sessions continue with the certificate they used when established.
func (r *CertificateReloader) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			pair := r.current.Load()
			if pair == nil {
				return nil, errors.New("inbound SMTP TLS certificate is unavailable")
			}
			return pair, nil
		},
	}
}

// Reload validates the certificate/key pair currently present at the
// configured paths and publishes it only after every check succeeds.
func (r *CertificateReloader) Reload(now time.Time) error {
	pair, err := loadValidatedCertificate(r.certPath, r.keyPath, r.serverName, now)
	if err != nil {
		return err
	}
	r.current.Store(pair)
	return nil
}

// LoadTLSConfig loads and validates the dedicated inbound SMTP certificate.
// Errors deliberately omit paths and certificate contents because production
// configuration values must not be copied into logs.
func LoadTLSConfig(certPath, keyPath, serverName string, now time.Time) (*tls.Config, error) {
	pair, err := loadValidatedCertificate(certPath, keyPath, serverName, now)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{*pair},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func loadValidatedCertificate(certPath, keyPath, serverName string, now time.Time) (*tls.Certificate, error) {
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
	return &pair, nil
}
