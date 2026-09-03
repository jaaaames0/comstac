package ui

import "context"

type contextKey int

const (
	csrfKey contextKey = iota
	cspNonceKey
)

// WithCSRFToken stores the CSRF token in the request context.
func WithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfKey, token)
}

// CSRFToken retrieves the CSRF token from the request context.
func CSRFToken(ctx context.Context) string {
	if v, ok := ctx.Value(csrfKey).(string); ok {
		return v
	}
	return ""
}

// WithCSPNonce stores the response's Content-Security-Policy nonce in the
// request context so the full-page template can authorize its fixed inline
// script without permitting arbitrary inline JavaScript.
func WithCSPNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, cspNonceKey, nonce)
}

// CSPNonce retrieves the response's Content-Security-Policy nonce.
func CSPNonce(ctx context.Context) string {
	if v, ok := ctx.Value(cspNonceKey).(string); ok {
		return v
	}
	return ""
}
