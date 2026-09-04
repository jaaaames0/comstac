package smtpserver_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"comstac/internal/ingest"
	"comstac/internal/smtpserver"
)

func TestLoadTLSConfigValidatesIdentityValidityAndMinimumVersion(t *testing.T) {
	now := time.Now().UTC()
	certPath, keyPath, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	cfg, err := smtpserver.LoadTLSConfig(certPath, keyPath, "mail.example.com", now)
	if err != nil {
		t.Fatalf("LoadTLSConfig() error = %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2", cfg.MinVersion)
	}
	if len(cfg.Certificates) != 1 || cfg.Certificates[0].Leaf == nil {
		t.Fatal("validated leaf certificate was not retained")
	}

	if _, err := smtpserver.LoadTLSConfig(certPath, keyPath, "other.example.com", now); err == nil || !strings.Contains(err.Error(), "COMSTAC_SMTP_DOMAIN") {
		t.Fatalf("hostname error = %v", err)
	}
	expiredCert, expiredKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if _, err := smtpserver.LoadTLSConfig(expiredCert, expiredKey, "mail.example.com", now); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("expiry error = %v", err)
	}
	futureCert, futureKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(time.Hour), now.Add(2*time.Hour))
	if _, err := smtpserver.LoadTLSConfig(futureCert, futureKey, "mail.example.com", now); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("not-before error = %v", err)
	}
	_, wrongKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := smtpserver.LoadTLSConfig(certPath, wrongKey, "mail.example.com", now); err == nil || !strings.Contains(err.Error(), "unreadable or invalid") {
		t.Fatalf("mismatched-key error = %v", err)
	}
}

func TestLoadTLSConfigDoesNotLeakPaths(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "do-not-log-this-path.pem")
	_, err := smtpserver.LoadTLSConfig(secretPath, secretPath+".key", "mail.example.com", time.Now())
	if err == nil {
		t.Fatal("LoadTLSConfig() accepted missing files")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Fatalf("error leaked configured path: %v", err)
	}
}

func TestCertificateReloaderActivatesOnlyValidatedReplacements(t *testing.T) {
	now := time.Now().UTC()
	certPath, keyPath, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	reloader, err := smtpserver.NewCertificateReloader(certPath, keyPath, "mail.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := reloader.TLSConfig()
	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2", tlsConfig.MinVersion)
	}
	initial, err := handshakeCertificate(tlsConfig)
	if err != nil {
		t.Fatalf("initial handshake: %v", err)
	}

	replacementCert, replacementKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(2*time.Hour))
	replacement := readCertificateDER(t, replacementCert)
	replaceTLSFiles(t, certPath, keyPath, replacementCert, replacementKey)
	if err := reloader.Reload(now); err != nil {
		t.Fatalf("reload valid replacement: %v", err)
	}
	got, err := handshakeCertificate(tlsConfig)
	if err != nil {
		t.Fatalf("replacement handshake: %v", err)
	}
	if !bytes.Equal(got, replacement) || bytes.Equal(got, initial) {
		t.Fatal("new handshakes did not receive the validated replacement certificate")
	}

	_, mismatchedKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(2*time.Hour))
	replaceTLSFiles(t, certPath, keyPath, replacementCert, mismatchedKey)
	if err := reloader.Reload(now); err == nil {
		t.Fatal("Reload() accepted a mismatched private key")
	}
	assertHandshakeCertificate(t, tlsConfig, replacement)

	expiredCert, expiredKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(-2*time.Hour), now.Add(-time.Hour))
	replaceTLSFiles(t, certPath, keyPath, expiredCert, expiredKey)
	if err := reloader.Reload(now); err == nil {
		t.Fatal("Reload() accepted an expired certificate")
	}
	assertHandshakeCertificate(t, tlsConfig, replacement)
}

func TestCertificateReloaderConcurrentHandshakes(t *testing.T) {
	now := time.Now().UTC()
	certPath, keyPath, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	otherCert, otherKey, _ := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(2*time.Hour))
	firstCertPEM, firstKeyPEM := readTLSFiles(t, certPath, keyPath)
	otherCertPEM, otherKeyPEM := readTLSFiles(t, otherCert, otherKey)
	firstDER := readCertificateDER(t, certPath)
	otherDER := readCertificateDER(t, otherCert)

	reloader, err := smtpserver.NewCertificateReloader(certPath, keyPath, "mail.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := reloader.TLSConfig()
	errCh := make(chan error, 64)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				got, handshakeErr := handshakeCertificate(tlsConfig)
				if handshakeErr != nil {
					errCh <- handshakeErr
					return
				}
				if !bytes.Equal(got, firstDER) && !bytes.Equal(got, otherDER) {
					errCh <- errors.New("handshake received an unknown certificate")
					return
				}
			}
		}()
	}
	for i := 0; i < 12; i++ {
		certPEM, keyPEM := firstCertPEM, firstKeyPEM
		if i%2 == 0 {
			certPEM, keyPEM = otherCertPEM, otherKeyPEM
		}
		if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := reloader.Reload(now); err != nil {
			t.Fatalf("Reload() iteration %d: %v", i, err)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestSMTPSTARTTLSIsOpportunisticAndVerified(t *testing.T) {
	now := time.Now().UTC()
	certPath, keyPath, roots := writeTLSFixture(t, "mail.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	serverTLS, err := smtpserver.LoadTLSConfig(certPath, keyPath, "mail.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t)
	srv := smtpserver.New(":0", ingest.NewService(db), smtpserver.Options{
		Domain:          "mail.example.com",
		LocalDomains:    []string{"example.com"},
		LocalRecipients: []string{"local@example.com"},
		TLSConfig:       serverTLS,
	})
	ln, done, cancel := runSMTPServer(t, srv)
	defer cancel()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, err := smtp.NewClient(conn, "mail.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		t.Fatal("server did not advertise STARTTLS")
	}
	if ok, _ := client.Extension("REQUIRETLS"); ok {
		t.Fatal("server advertised REQUIRETLS")
	}
	if err := client.StartTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: "mail.example.com",
	}); err != nil {
		t.Fatalf("StartTLS() error = %v", err)
	}
	state, ok := client.TLSConnectionState()
	if !ok || state.Version < tls.VersionTLS12 || !state.HandshakeComplete {
		t.Fatalf("TLS state = %+v, ok=%v", state, ok)
	}
	if err := sendWithClient(client, "Encrypted delivery"); err != nil {
		t.Fatalf("encrypted delivery: %v", err)
	}
	if err := client.Quit(); err != nil {
		t.Fatalf("QUIT: %v", err)
	}

	plain := []byte("Subject: Plaintext delivery\r\nFrom: sender@example.com\r\nTo: local@example.com\r\n\r\nplaintext remains accepted\r\n")
	if err := sendPlaintextSMTP(ln.Addr().String(), plain); err != nil {
		t.Fatalf("opportunistic plaintext delivery: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		count, queryErr := rawCount(db)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("message count = %d, want 2", count)
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancel()
	waitSMTPStop(t, done)
}

func sendPlaintextSMTP(addr string, message []byte) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	tp := textproto.NewConn(conn)
	defer tp.Close()
	if _, _, err := tp.ReadResponse(220); err != nil {
		return err
	}
	if err := tp.PrintfLine("EHLO plaintext-sender.example"); err != nil {
		return err
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		return err
	}
	for _, command := range []string{
		"MAIL FROM:<sender@example.com>",
		"RCPT TO:<local@example.com>",
	} {
		if err := tp.PrintfLine("%s", command); err != nil {
			return err
		}
		if _, _, err := tp.ReadResponse(250); err != nil {
			return err
		}
	}
	if err := tp.PrintfLine("DATA"); err != nil {
		return err
	}
	if _, _, err := tp.ReadResponse(354); err != nil {
		return err
	}
	writer := tp.DotWriter()
	if _, err := writer.Write(message); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		return err
	}
	return nil
}

func sendWithClient(client *smtp.Client, subject string) error {
	if err := client.Mail("sender@example.com"); err != nil {
		return err
	}
	if err := client.Rcpt("local@example.com"); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write([]byte("Subject: " + subject + "\r\nFrom: sender@example.com\r\nTo: local@example.com\r\n\r\nencrypted\r\n")); err != nil {
		return err
	}
	return writer.Close()
}

func handshakeCertificate(cfg *tls.Config) ([]byte, error) {
	serverConn, clientConn := net.Pipe()
	deadline := time.Now().Add(3 * time.Second)
	_ = serverConn.SetDeadline(deadline)
	_ = clientConn.SetDeadline(deadline)
	server := tls.Server(serverConn, cfg)
	client := tls.Client(clientConn, &tls.Config{ // #nosec G402 -- isolated test fixture
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Handshake()
	}()
	if err := client.Handshake(); err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		<-serverErr
		return nil, err
	}
	state := client.ConnectionState()
	_ = clientConn.Close()
	_ = serverConn.Close()
	if err := <-serverErr; err != nil {
		return nil, err
	}
	if len(state.PeerCertificates) != 1 {
		return nil, fmt.Errorf("peer certificate count = %d, want 1", len(state.PeerCertificates))
	}
	return append([]byte(nil), state.PeerCertificates[0].Raw...), nil
}

func assertHandshakeCertificate(t *testing.T, cfg *tls.Config, want []byte) {
	t.Helper()
	got, err := handshakeCertificate(cfg)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("failed reload displaced the last good certificate")
	}
}

func readCertificateDER(t *testing.T, path string) []byte {
	t.Helper()
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("test certificate PEM is invalid")
	}
	return append([]byte(nil), block.Bytes...)
}

func readTLSFiles(t *testing.T, certPath, keyPath string) ([]byte, []byte) {
	t.Helper()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM
}

func replaceTLSFiles(t *testing.T, certPath, keyPath, sourceCert, sourceKey string) {
	t.Helper()
	certPEM, keyPEM := readTLSFiles(t, sourceCert, sourceKey)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTLSFixture(t *testing.T, dnsName string, notBefore, notAfter time.Time) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to add test root")
	}
	return certPath, keyPath, roots
}
