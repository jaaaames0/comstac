package api

import (
	"net/http"
	"testing"
)

func TestHTTPServerHasDefensiveTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:8080", http.NewServeMux())
	if srv.ReadHeaderTimeout != httpReadHeaderTimeout || srv.ReadTimeout != httpReadTimeout ||
		srv.WriteTimeout != httpWriteTimeout || srv.IdleTimeout != httpIdleTimeout {
		t.Fatalf("unexpected timeouts: %+v", srv)
	}
	if srv.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}
