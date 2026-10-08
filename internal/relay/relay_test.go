package relay

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestSendHonoursAlreadyCancelledContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		io.WriteString(conn, "220 fixture ESMTP\r\n")
		bufio.NewReader(conn).ReadString('\n')
		io.WriteString(conn, "250 fixture\r\n")
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	r := New(Config{Host: host, Port: n, From: "owner@example.test"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = r.Send(ctx, OutboundMessage{To: []string{"recipient@example.test"}, BodyText: "fixture"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled send error = %v", err)
	}
}
