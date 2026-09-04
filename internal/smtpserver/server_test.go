package smtpserver

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"comstac/internal/securitymetrics"
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
	var counters securitymetrics.Counters
	limited := newLimitedListener(ln, 1, &counters)
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
	got := counters.Snapshot()
	if got.SMTPTemporaryRejections != 1 || got.SMTPSaturationRejections != 1 {
		t.Fatalf("security counters = %+v", got)
	}
}

func TestDataReturnsTemporaryFailureWhenWorkersAreSaturated(t *testing.T) {
	var counters securitymetrics.Counters
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	s := &session{
		envelopeTo: []string{"mail@example.com"},
		dataSlots:  slots,
		opts:       Options{DataTimeout: time.Second, SecurityCounters: &counters},
	}
	err := s.Data(strings.NewReader("Subject: test\r\n\r\nbody\r\n"))
	if err == nil || !strings.Contains(err.Error(), "451") {
		t.Fatalf("Data() error = %v, want SMTP 451", err)
	}
	got := counters.Snapshot()
	if got.SMTPTemporaryRejections != 1 || got.SMTPSaturationRejections != 1 {
		t.Fatalf("security counters = %+v", got)
	}
}

func TestDataReturnsTemporaryFailureWhenStorageIsUnavailable(t *testing.T) {
	var counters securitymetrics.Counters
	s := &session{
		envelopeTo: []string{"mail@example.com"},
		dataSlots:  make(chan struct{}, 1),
		opts: Options{
			DataTimeout:     time.Second,
			MaxMessageBytes: 25 << 20,
			CheckStorage: func(int64) error {
				return errors.New("unavailable")
			},
			SecurityCounters: &counters,
		},
	}
	err := s.Data(strings.NewReader("Subject: test\r\n\r\nbody\r\n"))
	if err == nil || !strings.Contains(err.Error(), "452") {
		t.Fatalf("Data() error = %v, want SMTP 452", err)
	}
	got := counters.Snapshot()
	if got.SMTPTemporaryRejections != 1 || got.SMTPSaturationRejections != 0 {
		t.Fatalf("security counters = %+v", got)
	}
}
