package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	authpkg "comstac/internal/auth"
	"comstac/internal/relay"
	"comstac/internal/store"
	"comstac/internal/ui"
)

type Server struct {
	addr      string
	db        *sql.DB
	auth      *authpkg.Manager
	relay     *relay.Relay
	oauth     *ui.OAuthConfig
	startedAt time.Time
	reqCount  atomic.Int64
}

func New(addr string, db *sql.DB, auth *authpkg.Manager, r *relay.Relay, oauth *ui.OAuthConfig) *Server {
	return &Server{addr: addr, db: db, auth: auth, relay: r, oauth: oauth, startedAt: time.Now()}
}

func (s *Server) Handler() http.Handler {
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	publicMux.HandleFunc("/login", s.handleLogin)
	publicMux.HandleFunc("/api/login", s.handleAPILogin)
	publicMux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := ui.StaticFS.ReadFile("static/sw.js")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
	publicMux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		data, err := ui.StaticFS.ReadFile("static/manifest.json")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
	publicMux.HandleFunc("/static/icon-192.png", func(w http.ResponseWriter, r *http.Request) {
		data, err := ui.StaticFS.ReadFile("static/icon-192.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(data)
	})
	publicMux.HandleFunc("/static/icon-512.png", func(w http.ResponseWriter, r *http.Request) {
		data, err := ui.StaticFS.ReadFile("static/icon-512.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(data)
	})
	publicMux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		data, err := ui.StaticFS.ReadFile("static/icon-192.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(data)
	})

	protectedMux := http.NewServeMux()
	ui.RegisterRoutes(protectedMux, s.db, s.relay, s.oauth)
	protectedMux.HandleFunc("/metrics", s.handleMetrics)
	protectedMux.HandleFunc("/logout", s.handleLogout)
	protectedMux.HandleFunc("/api/logout", s.handleAPILogout)
	protectedMux.HandleFunc("/api/messages", s.handleListMessages)
	protectedMux.HandleFunc("/api/messages/", s.handleMessageRoute)
	protectedMux.HandleFunc("/api/accounts", s.handleAccounts)

	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reqCount.Add(1)
		if isPublicPath(r.URL.Path) {
			publicMux.ServeHTTP(w, r)
			return
		}

		token := ""
		if s.auth != nil {
			token = s.auth.ReadToken(r)
		} else {
			handleUnauthorized(w, r)
			return
		}
		authed, err := s.auth.IsAuthenticated(r.Context(), token)
		if err != nil {
			slog.Error("auth check", "component", "api", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "auth check failed")
			return
		}
		if !authed {
			handleUnauthorized(w, r)
			return
		}

		// Inject CSRF token into context for the index page renderer.
		ctx := ui.WithCSRFToken(r.Context(), s.auth.CSRFToken(token))
		r = r.WithContext(ctx)

		// Validate CSRF for all state-changing requests.
		if r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			if !s.auth.ValidateCSRF(r, token) {
				slog.Warn("csrf validation failed", "component", "api", "path", r.URL.Path, "method", r.Method)
				if strings.HasPrefix(r.URL.Path, "/api/") {
					writeJSONError(w, http.StatusForbidden, "invalid CSRF token")
				} else {
					http.Error(w, "invalid CSRF token", http.StatusForbidden)
				}
				return
			}
		}

		protectedMux.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		core.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func (s *Server) Run(ctx context.Context) error {
	httpSrv := &http.Server{Addr: s.addr, Handler: s.Handler()}
	go shutdownOnContext(ctx, httpSrv)

	slog.Info("http api listening", "component", "api", "addr", s.addr)
	err := httpSrv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (s *Server) RunWithListener(ctx context.Context, ln net.Listener) error {
	httpSrv := &http.Server{Handler: s.Handler()}
	go shutdownOnContext(ctx, httpSrv)

	slog.Info("http api listening", "component", "api", "addr", ln.Addr().String())
	err := httpSrv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func shutdownOnContext(ctx context.Context, httpSrv *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http shutdown", "component", "api", "err", err)
	}
}

func isPublicPath(path string) bool {
	switch {
	case path == "/healthz", path == "/login", path == "/api/login",
		path == "/sw.js", path == "/manifest.json",
		path == "/favicon.ico",
		path == "/static/icon-192.png", path == "/static/icon-512.png":
		return true
	default:
		return false
	}
}

func handleUnauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	type metrics struct {
		UptimeSeconds  int64 `json:"uptime_seconds"`
		RequestsTotal  int64 `json:"requests_total"`
		MessagesTotal  int64 `json:"messages_total"`
		MessagesUnread int64 `json:"messages_unread"`
		SyncPending    int64 `json:"sync_jobs_pending"`
		SyncFailed     int64 `json:"sync_jobs_failed"`
	}
	m := metrics{
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		RequestsTotal: s.reqCount.Load(),
	}
	if s.db != nil {
		_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM messages`).Scan(&m.MessagesTotal)
		_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM messages WHERE read = 0`).Scan(&m.MessagesUnread)
		_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM sync_jobs WHERE status IN ('pending','retrying')`).Scan(&m.SyncPending)
		_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM sync_jobs WHERE status = 'failed'`).Scan(&m.SyncFailed)
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginPageHTML))
		return
	case http.MethodPost:
		username, password, err := readCredentials(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		token, expires, err := s.auth.Login(r.Context(), username, password)
		if err != nil {
			if errors.Is(err, authpkg.ErrInvalidCredentials) {
				http.Error(w, "invalid credentials", http.StatusUnauthorized)
				return
			}
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		s.auth.SetSessionCookie(w, token, expires)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (s *Server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	username, password, err := readCredentials(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, expires, err := s.auth.Login(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, authpkg.ErrInvalidCredentials) {
			writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "login failed")
		return
	}
	s.auth.SetSessionCookie(w, token, expires)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := s.auth.ReadToken(r)
	_ = s.auth.Logout(r.Context(), token)
	s.auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleAPILogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token := s.auth.ReadToken(r)
	_ = s.auth.Logout(r.Context(), token)
	s.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.db == nil {
		writeJSONError(w, http.StatusInternalServerError, "database not configured")
		return
	}

	opts, err := parseListMessageOptions(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	items, err := store.ListMessages(r.Context(), s.db, opts)
	if err != nil {
		slog.Error("list messages", "component", "api", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	resp := map[string]any{"items": items}
	if len(items) > 0 {
		resp["next_before_id"] = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMessageRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/messages/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if len(parts) == 1 {
		s.handleGetMessage(w, r, id)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		s.handleMessageAction(w, r, id, parts[1])
		return
	}
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	detail, err := store.GetMessageDetail(r.Context(), s.db, id)
	if err != nil {
		slog.Error("get message detail", "component", "api", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to get message")
		return
	}
	if detail == nil {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleMessageAction(w http.ResponseWriter, r *http.Request, id int64, action string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var found bool
	var err error

	switch action {
	case "read":
		var payload struct {
			Read bool `json:"read"`
		}
		if decErr := json.NewDecoder(r.Body).Decode(&payload); decErr != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid payload")
			return
		}
		found, err = store.SetMessageRead(r.Context(), s.db, id, payload.Read)
		if err == nil && found {
			_, _ = store.EnqueueIMAPSyncJob(r.Context(), s.db, id, "read", payload)
		}
	case "archive":
		var payload struct {
			Archived bool `json:"archived"`
		}
		if decErr := json.NewDecoder(r.Body).Decode(&payload); decErr != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid payload")
			return
		}
		found, err = store.SetMessageArchived(r.Context(), s.db, id, payload.Archived)
		if err == nil && found {
			_, _ = store.EnqueueIMAPSyncJob(r.Context(), s.db, id, "archive", payload)
		}
	case "snooze":
		var payload struct {
			Until string `json:"until"`
		}
		if decErr := json.NewDecoder(r.Body).Decode(&payload); decErr != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid payload")
			return
		}
		var until *time.Time
		if strings.TrimSpace(payload.Until) != "" {
			parsed, parseErr := time.Parse(time.RFC3339, payload.Until)
			if parseErr != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid until timestamp")
				return
			}
			until = &parsed
		}
		found, err = store.SetMessageSnoozeUntil(r.Context(), s.db, id, until)
		if err == nil && found {
			_, _ = store.EnqueueIMAPSyncJob(r.Context(), s.db, id, "snooze", payload)
		}
	default:
		writeJSONError(w, http.StatusNotFound, "unknown action")
		return
	}

	if err != nil {
		slog.Error("message action", "component", "api", "action", action, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "action failed")
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	detail, err := store.GetMessageDetail(r.Context(), s.db, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load updated message")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": detail})
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSONError(w, http.StatusInternalServerError, "database not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
		if kind != "" && kind != "local" && kind != "imap" {
			writeJSONError(w, http.StatusBadRequest, "invalid kind")
			return
		}
		items, err := store.ListAccounts(r.Context(), s.db, kind)
		if err != nil {
			slog.Error("list accounts", "component", "api", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to list accounts")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	case http.MethodPost:
		var payload struct {
			Kind         string `json:"kind"`
			Name         string `json:"name"`
			EmailAddress string `json:"email_address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid payload")
			return
		}
		kind := strings.ToLower(strings.TrimSpace(payload.Kind))
		if kind != "local" {
			writeJSONError(w, http.StatusBadRequest, "only local account creation is supported")
			return
		}
		account, err := store.CreateLocalAccount(r.Context(), s.db, payload.EmailAddress, payload.Name)
		if err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "already exists") {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"item": account})
		return
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
}

func parseListMessageOptions(r *http.Request) (store.ListMessageOptions, error) {
	opts := store.ListMessageOptions{Limit: 50}
	q := r.URL.Query()

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return opts, errors.New("invalid limit")
		}
		opts.Limit = n
	}
	if raw := q.Get("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return opts, errors.New("invalid before_id")
		}
		opts.BeforeID = n
	}
	if raw := q.Get("unread"); raw != "" {
		v, err := parseBool(raw)
		if err != nil {
			return opts, errors.New("invalid unread")
		}
		opts.Unread = &v
	}
	if raw := q.Get("archived"); raw != "" {
		v, err := parseBool(raw)
		if err != nil {
			return opts, errors.New("invalid archived")
		}
		opts.Archived = &v
	}
	if raw := q.Get("source"); raw != "" {
		opts.Source = strings.ToLower(strings.TrimSpace(raw))
	}
	return opts, nil
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y":
		return true, nil
	case "0", "false", "no", "n":
		return false, nil
	default:
		return false, errors.New("invalid bool")
	}
}

func readCredentials(r *http.Request) (username string, password string, err error) {
	ctype := r.Header.Get("Content-Type")
	if strings.Contains(ctype, "application/json") {
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if decErr := json.NewDecoder(r.Body).Decode(&payload); decErr != nil {
			return "", "", fmt.Errorf("invalid json payload")
		}
		username = strings.TrimSpace(payload.Username)
		password = payload.Password
	} else {
		if err := r.ParseForm(); err != nil {
			return "", "", fmt.Errorf("invalid form payload")
		}
		username = strings.TrimSpace(r.FormValue("username"))
		password = r.FormValue("password")
	}
	if username == "" || password == "" {
		return "", "", fmt.Errorf("username and password are required")
	}
	return username, password, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

const loginPageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1"/>
  <title>comstac</title>
  <style>
    *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
    :root{
      --bg:#0d1117;--surface:#161b22;--border:#30363d;
      --text:#e6edf3;--muted:#8b949e;--dim:#484f58;
      --accent:#34d399;--accent-dim:rgba(52,211,153,.10);--accent-ring:rgba(52,211,153,.25);
      --mono:ui-monospace,'Cascadia Code','JetBrains Mono',monospace;
    }
    html,body{height:100%}
    body{font-family:var(--mono);background:var(--bg);color:var(--text);display:grid;place-items:center}
    .card{width:min(360px,92vw);background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:28px 24px}
    .brand{font-size:13px;font-weight:700;color:var(--accent);letter-spacing:.06em;margin-bottom:24px}
    .field{margin-bottom:14px}
    label{display:block;font-size:11px;color:var(--dim);margin-bottom:4px;letter-spacing:.04em}
    input{
      width:100%;background:var(--bg);border:1px solid var(--border);
      color:var(--text);font-family:var(--mono);font-size:13px;
      padding:8px 10px;border-radius:3px;outline:none;
      transition:border-color .15s;
    }
    input:focus{border-color:var(--accent-ring)}
    .submit{
      width:100%;margin-top:6px;padding:9px;
      background:var(--accent-dim);border:1px solid var(--accent-ring);
      color:var(--accent);font-family:var(--mono);font-size:13px;
      border-radius:3px;cursor:pointer;transition:background .12s;
    }
    .submit:hover{background:rgba(52,211,153,.17)}
    .error{margin-top:12px;font-size:12px;color:#f85149;text-align:center}
  </style>
</head>
<body>
  <form class="card" method="post" action="/login">
    <div class="brand">comstac</div>
    <div class="field">
      <label>username</label>
      <input name="username" type="text" autocomplete="username" required/>
    </div>
    <div class="field">
      <label>password</label>
      <input name="password" type="password" autocomplete="current-password" required/>
    </div>
    <button class="submit" type="submit">sign in</button>
  </form>
</body>
</html>`
