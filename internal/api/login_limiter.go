package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	loginFailureLimit  = 5
	loginFailureWindow = 10 * time.Minute
	loginBlockDuration = 15 * time.Minute
)

type loginLimitEntry struct {
	windowStarted time.Time
	lastSeen      time.Time
	blockedUntil  time.Time
	failures      int
}

// loginLimiter is deliberately in-process and shared by the HTML and JSON
// login routes. nginx remains a separate outer layer, but a direct loopback
// request cannot bypass this limiter.
type loginLimiter struct {
	mu       sync.Mutex
	entries  map[string]loginLimitEntry
	now      func() time.Time
	requests uint64
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]loginLimitEntry), now: time.Now}
}

func (l *loginLimiter) allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maybeCleanup(now)

	entry, ok := l.entries[key]
	if !ok || entry.blockedUntil.IsZero() || !now.Before(entry.blockedUntil) {
		if ok && !entry.blockedUntil.IsZero() {
			delete(l.entries, key)
		}
		return true, 0
	}
	return false, entry.blockedUntil.Sub(now)
}

func (l *loginLimiter) failure(key string) (blocked bool, retryAfter time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maybeCleanup(now)

	entry := l.entries[key]
	if entry.windowStarted.IsZero() || now.Sub(entry.windowStarted) >= loginFailureWindow {
		entry.windowStarted = now
		entry.failures = 0
		entry.blockedUntil = time.Time{}
	}
	entry.failures++
	entry.lastSeen = now
	if entry.failures >= loginFailureLimit {
		entry.blockedUntil = now.Add(loginBlockDuration)
	}
	l.entries[key] = entry
	return !entry.blockedUntil.IsZero(), entry.blockedUntil.Sub(now)
}

func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func (l *loginLimiter) maybeCleanup(now time.Time) {
	l.requests++
	if l.requests%128 != 0 {
		return
	}
	cutoff := now.Add(-(loginFailureWindow + loginBlockDuration))
	for key, entry := range l.entries {
		if entry.lastSeen.Before(cutoff) && !now.Before(entry.blockedUntil) {
			delete(l.entries, key)
		}
	}
}

func loginClientKey(r *http.Request) string {
	host := remoteHost(r.RemoteAddr)
	remoteIP := net.ParseIP(host)
	if remoteIP != nil && remoteIP.IsLoopback() {
		// nginx is the only intended proxy and reaches Comstac over loopback.
		// Trust only its single-address X-Real-IP header; do not parse an
		// attacker-controlled forwarded chain.
		if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); forwarded != nil {
			return forwarded.String()
		}
	}
	if remoteIP != nil {
		return remoteIP.String()
	}
	if host != "" {
		return host
	}
	return "unknown"
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

func retryAfterSeconds(d time.Duration) string {
	seconds := int64((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10)
}
