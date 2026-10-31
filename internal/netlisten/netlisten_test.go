package netlisten

import (
	"context"
	"net"
	"testing"
)

func TestTCPRefusesMultipath(t *testing.T) {
	ln, err := TCP(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Dial with MPTCP requested: an MPTCP listener would accept it as MPTCP.
	var d net.Dialer
	d.SetMultipathTCP(true)
	client, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	mptcp, err := conn.(*net.TCPConn).MultipathTCP()
	if err != nil {
		t.Fatal(err)
	}
	if mptcp {
		t.Fatal("accepted connection uses Multipath TCP")
	}
}
