package push

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// AgentClients manages live SSE connections for the agent.
// It is safe for concurrent use by multiple goroutines.
type AgentClients struct {
	token string

	mu    sync.RWMutex
	conns map[chan<- AgentEvent]struct{}
}

// AgentEvent represents an event sent to the agent over SSE.
type AgentEvent struct {
	Type      string `json:"type"` // "new_mail" | "ping"
	MessageID int64  `json:"id,omitempty"`
	From      string `json:"from,omitempty"`
	Subject   string `json:"subject,omitempty"`
}

// NewAgentClients creates a manager for agent SSE connections.
// token is the expected X-Agent-Token header value.
func NewAgentClients(token string) *AgentClients {
	return &AgentClients{token: token, conns: make(map[chan<- AgentEvent]struct{})}
}

// Add registers a new SSE connection and returns the event channel and a stop signal.
// The caller should receive from events and stop receiving when stop closes.
func (ac *AgentClients) Add() (events chan AgentEvent, stop chan<- struct{}) {
	ch := make(chan AgentEvent, 50)
	stopCh := make(chan struct{})
	ac.mu.Lock()
	ac.conns[ch] = struct{}{}
	ac.mu.Unlock()
	return ch, stopCh
}

// Remove deregisters a connection. Idempotent.
func (ac *AgentClients) Remove(ch chan AgentEvent) {
	ac.mu.Lock()
	delete(ac.conns, ch)
	ac.mu.Unlock()
	close(ch)
}

// EmitNewMail sends a new_mail event to all connected agent clients.
// Silently drops events if a client's buffer is full (caller doesn't block).
func (ac *AgentClients) EmitNewMail(messageID int64, from, subject string) {
	ev := AgentEvent{Type: "new_mail", MessageID: messageID, From: from, Subject: subject}

	ac.mu.RLock()
	conns := make([]chan<- AgentEvent, 0, len(ac.conns))
	for ch := range ac.conns {
		conns = append(conns, ch)
	}
	ac.mu.RUnlock()

	for _, ch := range conns {
		select {
		case ch <- ev:
		default:
			// Buffer full — drop and let reconnect handle it.
		}
	}
}

// ServeHTTP handles GET /api/push/sse — the agent SSE endpoint.
func (ac *AgentClients) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.Header.Get("X-Agent-Token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(ac.token)) != 1 {
		slog.Warn("push: agent: auth failed", "remote", r.RemoteAddr)
		// Don't leak whether token was missing vs invalid.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable nginx buffering so events stream immediately.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	slog.Info("push: agent client connected", "remote", r.RemoteAddr)

	stop := make(chan struct{})
	defer close(stop)

	events, stopCh := ac.Add()
	defer close(stopCh)

	_ = events // used in select below

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	notify := w.(http.CloseNotifier).CloseNotify()

	for {
		select {
		case ev := <-events:
			data, _ := json.Marshal(map[string]any{"id": ev.MessageID, "from": ev.From, "subject": ev.Subject})
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-ticker.C:
			fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-notify:
			slog.Info("push: agent client disconnected", "remote", r.RemoteAddr)
			return
		case <-stop:
			return
		case <-r.Context().Done():
			return
		}
	}
}
