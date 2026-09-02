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
	slots chan struct{}

	mu    sync.RWMutex
	conns map[chan<- AgentEvent]struct{}
}

const maxAgentConnections = 4

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
	return &AgentClients{
		token: token,
		slots: make(chan struct{}, maxAgentConnections),
		conns: make(map[chan<- AgentEvent]struct{}),
	}
}

// Add registers a new SSE connection.
func (ac *AgentClients) Add() (chan AgentEvent, bool) {
	select {
	case ac.slots <- struct{}{}:
	default:
		return nil, false
	}
	ch := make(chan AgentEvent, 50)
	ac.mu.Lock()
	ac.conns[ch] = struct{}{}
	ac.mu.Unlock()
	return ch, true
}

// Remove deregisters a connection. Idempotent.
func (ac *AgentClients) Remove(ch chan AgentEvent) {
	ac.mu.Lock()
	_, ok := ac.conns[ch]
	if ok {
		delete(ac.conns, ch)
	}
	ac.mu.Unlock()
	if ok {
		<-ac.slots
	}
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
	events, ok := ac.Add()
	if !ok {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many agent connections", http.StatusServiceUnavailable)
		return
	}
	defer ac.Remove(events)

	// Streaming responses intentionally outlive the normal HTTP write timeout.
	// The request context still ends promptly when nginx or the client closes.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		slog.Error("push: agent: clear write deadline", "err", err)
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
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

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
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
		case <-r.Context().Done():
			slog.Info("push: agent client disconnected", "remote", r.RemoteAddr)
			return
		}
	}
}
