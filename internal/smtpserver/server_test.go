package smtpserver

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNormalizeOptionsAppliesResourceBounds(t *testing.T) {
	opts := normalizeOptions(Options{})
	if opts.MaxConnections != 32 || opts.MaxDataWorkers != 4 {
		t.Fatalf("unexpected concurrency defaults: %+v", opts)
	}
	if opts.MaxMessageBytes != 25*1024*1024 || opts.MaxRecipients != 100 {
		t.Fatalf("unexpected message defaults: %+v", opts)
	}
	if opts.DataTimeout != 2*time.Minute {
		t.Fatalf("unexpected data timeout: %v", opts.DataTimeout)
	}
}

func TestLimitedListenerRejectsExcessConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := newLimitedListener(ln, 1)
	defer limited.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := limited.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	first, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	serverConn := <-accepted
	defer serverConn.Close()
	go func() {
		// Accept rejects saturated connections internally and keeps waiting.
		// Closing the listener at test cleanup releases this goroutine.
		_, _ = limited.Accept()
	}()

	second, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(second).ReadString('\n')
	if err != nil {
		t.Fatalf("read rejection: %v", err)
	}
	if !strings.HasPrefix(line, "421 ") {
		t.Fatalf("rejection = %q", line)
	}
}

func TestDataReturnsTemporaryFailureWhenWorkersAreSaturated(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	s := &session{
		envelopeTo: []string{"mail@example.com"},
		dataSlots:  slots,
		opts:       Options{DataTimeout: time.Second},
	}
	err := s.Data(strings.NewReader("Subject: test\r\n\r\nbody\r\n"))
	if err == nil || !strings.Contains(err.Error(), "451") {
		t.Fatalf("Data() error = %v, want SMTP 451", err)
	}
}
