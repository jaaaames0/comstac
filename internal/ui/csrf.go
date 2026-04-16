package ui

import "context"

type contextKey int

const csrfKey contextKey = 0

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
