package relay

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func fixtureCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture.local"}, DNSNames: []string{"fixture.local"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func smtpFixture(t *testing.T, mode string) (*Relay, <-chan string) {
	t.Helper()
	cert, roots := fixtureCertificate(t)
	client, server := net.Pipe()
	server.SetDeadline(time.Now().Add(3 * time.Second))
	r := New(Config{Host: "fixture.local", Port: 587, Username: "synthetic-user", Password: "synthetic-password", From: "owner@example.test"})
	r.tlsConfig = &tls.Config{ServerName: "fixture.local", RootCAs: roots, MinVersion: tls.VersionTLS12}
	r.dialContext = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	phases := make(chan string, 4)
	done := make(chan struct{})
	t.Cleanup(func() {
		client.Close()
		server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SMTP fixture did not terminate")
		}
	})
	go func() {
		defer close(done)
		defer server.Close()
		if mode == "greeting" {
			phases <- "greeting"
			io.Copy(io.Discard, server)
			return
		}
		io.WriteString(server, "220 fixture ESMTP\r\n")
		reader := bufio.NewReader(server)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		io.WriteString(server, "250-fixture\r\n250 STARTTLS\r\n")
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		io.WriteString(server, "220 upgrade\r\n")
		if mode == "tls" {
			phases <- "tls"
			io.Copy(io.Discard, server)
			return
		}
		secure := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if err := secure.Handshake(); err != nil {
			return
		}
		reader = bufio.NewReader(secure)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				io.WriteString(secure, "250-fixture\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH"):
				io.WriteString(secure, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL"):
				if mode == "command" {
					phases <- "command"
					io.Copy(io.Discard, secure)
					return
				}
				io.WriteString(secure, "250 sender\r\n")
			case strings.HasPrefix(line, "RCPT"):
				io.WriteString(secure, "250 recipient\r\n")
			case strings.HasPrefix(line, "DATA"):
				io.WriteString(secure, "354 send body\r\n")
				if mode == "data" {
					phases <- "data"
					io.Copy(io.Discard, secure)
					return
				}
				for {
					line, err = reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
				}
				if mode == "drop-acceptance" {
					return
				}
				if mode == "reject" {
					io.WriteString(secure, "550 explicitly rejected\r\n")
					return
				}
				io.WriteString(secure, "250 accepted\r\n")
				return
			default:
				fmt.Fprint(secure, "500 fixture unexpected command\r\n")
				return
			}
		}
	}()
	return r, phases
}

func TestSendCancellationAtSMTPBoundaries(t *testing.T) {
	for _, mode := range []string{"greeting", "tls", "command", "data"} {
		t.Run(mode, func(t *testing.T) {
			r, phases := smtpFixture(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := r.Send(ctx, OutboundMessage{To: []string{"recipient@example.test"}, BodyText: "Fixture"})
				done <- err
			}()
			select {
			case <-phases:
			case <-time.After(time.Second):
				t.Fatal("send never reached fixture phase")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
				if errors.Is(err, ErrDeliveryUnknown) != (mode == "data") {
					t.Fatalf("delivery classification=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled SMTP remained blocked")
			}
		})
	}
}

func TestSendAcceptanceAndUncertainty(t *testing.T) {
	for _, mode := range []string{"accepted-drop", "drop-acceptance", "reject"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := smtpFixture(t, mode)
			_, _, err := r.Send(context.Background(), OutboundMessage{To: []string{"recipient@example.test"}, BodyText: "Fixture"})
			if mode == "accepted-drop" && err != nil {
				t.Fatalf("known acceptance converted to failure: %v", err)
			}
			if mode == "drop-acceptance" && !errors.Is(err, ErrDeliveryUnknown) {
				t.Fatalf("ambiguous acceptance=%v", err)
			}
			if mode == "reject" && (err == nil || errors.Is(err, ErrDeliveryUnknown)) {
				t.Fatalf("explicit rejection=%v", err)
			}
		})
	}
}

func TestSendVerifiesTLSCertificateAndHost(t *testing.T) {
	for _, wrongHost := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongHost), func(t *testing.T) {
			r, _ := smtpFixture(t, "accepted-drop")
			if wrongHost {
				r.tlsConfig.ServerName = "wrong.fixture"
			} else {
				r.tlsConfig.RootCAs = x509.NewCertPool()
			}
			_, _, err := r.Send(context.Background(), OutboundMessage{To: []string{"recipient@example.test"}, BodyText: "Fixture"})
			if err == nil || errors.Is(err, ErrDeliveryUnknown) {
				t.Fatalf("TLS validation=%v", err)
			}
		})
	}
}

func TestSendDialUsesRequestDeadline(t *testing.T) {
	r := New(Config{Host: "fixture.local", Port: 587, From: "owner@example.test"})
	r.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err := r.Send(ctx, OutboundMessage{To: []string{"recipient@example.test"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial deadline=%v", err)
	}
}
