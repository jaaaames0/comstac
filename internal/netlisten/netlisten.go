// Package netlisten opens Comstac's network listeners.
package netlisten

import (
	"context"
	"net"
)

// TCP listens on addr with plain TCP. Go enables Multipath TCP on listeners by
// default; Comstac has no use for it and it widens the kernel attack surface
// (CVE-2026-98432 was demonstrated against an MPTCP listener), so it is
// disabled explicitly. This setting also overrides GODEBUG=multipathtcp.
func TCP(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	return lc.Listen(ctx, "tcp", addr)
}
