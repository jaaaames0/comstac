package ui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"comstac/internal/imap"
	"comstac/internal/relay"
	"comstac/internal/store"
)

// OAuthConfig holds parameters needed for server-side Gmail re-authorization
// and Web Push (VAPID) configuration.
// BaseURL must be the public HTTPS root (e.g. "https://mail.example.com") so
// that the redirect URI can be registered in Google Cloud Console.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	BaseURL      string // public root; callback = BaseURL + "/ui/oauth/callback"
	CSRFSecret   string // same CSRF secret used by auth package
	TokenSource  *imap.TokenSource
	DB           *sql.DB

	// Web Push (VAPID) — set when COMSTAC_VAPID_* env vars are configured.
	VAPIDPublicKey string // base64url-encoded; passed to template for browser PushManager.subscribe
}

func RegisterRoutes(mux *http.ServeMux, db *sql.DB, r *relay.Relay, oauth *OAuthConfig) {
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}
		imapAuthFailed := false
		if oauth != nil && oauth.TokenSource != nil {
			authErr, _ := oauth.TokenSource.AuthError()
			imapAuthFailed = authErr != nil
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		execute(w, "index", indexData{
			CSRFToken:      CSRFToken(req.Context()),
			CSPNonce:       CSPNonce(req.Context()),
			IMAPAuthFailed: imapAuthFailed,
		})
	})

	mux.HandleFunc("/ui/messages", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}

		opts, err := parseListOpts(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, err := store.ListMessages(req.Context(), db, opts)
		if err != nil {
			http.Error(w, "failed to list messages", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		tmplName := "message_list"
		if req.URL.Query().Get("append") == "1" {
			tmplName = "message_list_more"
		}
		execute(w, tmplName, newMessageListData(items, opts))
	})

	mux.HandleFunc("/ui/message", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(req.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		detail, err := store.GetMessageDetail(req.Context(), db, id)
		if err != nil {
			http.Error(w, "failed to fetch message", http.StatusInternalServerError)
			return
		}
		if detail == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		loadRemoteImages := remoteImagesAllowed(req.Context(), db, detail.FromAddr,
			req.URL.Query().Get("remote_images") == "1")
		execute(w, "message_detail", newMessageDetailData(detail, loadRemoteImages))
	})

	mux.HandleFunc("/ui/accounts", func(w http.ResponseWriter, req *http.Request) {
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		if req.Method == http.MethodPost {
			switch req.FormValue("action") {
			case "remote_image_add":
				if _, err := store.AddRemoteImageSender(req.Context(), db, req.FormValue("email")); err != nil {
					http.Error(w, "invalid mailbox address", http.StatusBadRequest)
					return
				}
			case "remote_image_remove":
				if err := store.RemoveRemoteImageSender(req.Context(), db, req.FormValue("email")); err != nil {
					http.Error(w, "invalid mailbox address", http.StatusBadRequest)
					return
				}
			case "":
				email := strings.TrimSpace(req.FormValue("email"))
				name := strings.TrimSpace(req.FormValue("name"))
				if email == "" {
					http.Error(w, "email is required", http.StatusBadRequest)
					return
				}
				if _, err := store.CreateLocalAccount(req.Context(), db, email, name); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			default:
				http.Error(w, "invalid account action", http.StatusBadRequest)
				return
			}
		} else if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		accounts, err := store.ListAccounts(req.Context(), db, "local")
		if err != nil {
			http.Error(w, "failed to list accounts", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		oauthEnabled := oauth != nil && oauth.ClientID != "" && oauth.BaseURL != ""
		callbackURL := ""
		if oauthEnabled {
			callbackURL = strings.TrimRight(oauth.BaseURL, "/") + "/ui/oauth/callback"
		}
		imapAuthFailed := false
		imapAuthFailedAt := ""
		if oauth != nil && oauth.TokenSource != nil {
			authErr, authErrAt := oauth.TokenSource.AuthError()
			if authErr != nil {
				imapAuthFailed = true
				imapAuthFailedAt = authErrAt.In(sydneyLoc).Format("2 Jan 15:04")
			}
		}
		vapidPublicKey := ""
		if oauth != nil {
			vapidPublicKey = oauth.VAPIDPublicKey
		}
		pushCount := 0
		if vapidPublicKey != "" {
			pushCount, _ = store.CountPushSubscriptions(req.Context(), db)
		}
		imageSenders, err := store.ListRemoteImageSenders(req.Context(), db)
		if err != nil {
			http.Error(w, "failed to list remote-image preferences", http.StatusInternalServerError)
			return
		}
		execute(w, "accounts", accountsData{
			Accounts:         accounts,
			OAuthEnabled:     oauthEnabled,
			OAuthStatus:      req.URL.Query().Get("oauth"),
			OAuthCallbackURL: callbackURL,
			IMAPAuthFailed:   imapAuthFailed,
			IMAPAuthFailedAt: imapAuthFailedAt,
			VAPIDPublicKey:   vapidPublicKey,
			PushCount:        pushCount,
			ImageSenders:     imageSenders,
		})
	})

	mux.HandleFunc("/ui/compose", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if req.Method == http.MethodGet {
			execute(w, "compose", composeData{})
			return
		}
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r == nil {
			execute(w, "send_error", sendErrorData{Msg: "Relay not configured."})
			return
		}
		if err := req.ParseMultipartForm(25 << 20); err != nil && err != http.ErrNotMultipart {
			http.Error(w, "failed to parse form", http.StatusBadRequest)
			return
		}
		to := parseAddressList(req.FormValue("to"))
		subject := strings.TrimSpace(req.FormValue("subject"))
		body := strings.TrimSpace(req.FormValue("body"))
		cc := parseAddressList(req.FormValue("cc"))
		bcc := parseAddressList(req.FormValue("bcc"))
		attachments := extractAttachments(req)
		if len(to) == 0 {
			execute(w, "compose", composeData{Subject: subject, Body: body})
			return
		}
		msgID, rawMIME, sendErr := r.Send(req.Context(), relay.OutboundMessage{
			To: to, CC: cc, BCC: bcc, Subject: subject, BodyText: body, Attachments: attachments,
		})
		if db != nil {
			if _, err := store.SaveOutbound(req.Context(), db, r.From(), strings.Join(to, ", "), subject, body, msgID, 0, rawMIME, sendErr); err != nil {
				slog.Error("save outbound", "component", "ui", "err", err)
			}
		}
		if sendErr != nil {
			slog.Error("compose send", "component", "ui", "err", sendErr)
			execute(w, "send_error", sendErrorData{Msg: sendErr.Error()})
			return
		}
		execute(w, "send_success", sendSuccessData{To: strings.Join(to, ", "), Subject: subject})
	})

	mux.HandleFunc("/ui/forward", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(req.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		detail, err := store.GetMessageDetail(req.Context(), db, id)
		if err != nil || detail == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body := "\n\n--- Forwarded message ---\nFrom: " + detail.FromAddr +
			"\nDate: " + detail.DateHdr +
			"\nSubject: " + detail.Subject +
			"\n\n" + detail.BodyText
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		execute(w, "compose", composeData{
			Title:   "forward",
			Subject: relay.ForwardSubject(detail.Subject),
			Body:    body,
		})
	})

	mux.HandleFunc("/ui/reply", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(req.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			if req.Method == http.MethodPost {
				id2, _ := strconv.ParseInt(req.FormValue("id"), 10, 64)
				id = id2
			}
		}

		if req.Method == http.MethodGet {
			if id <= 0 {
				http.Error(w, "invalid id", http.StatusBadRequest)
				return
			}
			detail, err := store.GetMessageDetail(req.Context(), db, id)
			if err != nil || detail == nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			cc := ""
			if req.URL.Query().Get("all") == "1" {
				cc = buildReplyAllCC(detail, r)
			}
			execute(w, "reply", replyData{
				ReplyToID:  id,
				To:         extractEmailAddress(detail.FromAddr),
				CC:         cc,
				Subject:    relay.ReplySubject(detail.Subject),
				QuotedBody: "\n\n---\n" + strings.ReplaceAll(strings.TrimSpace(detail.BodyText), "\n", "\n> "),
			})
			return
		}

		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r == nil {
			execute(w, "send_error", sendErrorData{Msg: "Relay not configured."})
			return
		}
		if err := req.ParseMultipartForm(25 << 20); err != nil && err != http.ErrNotMultipart {
			http.Error(w, "failed to parse form", http.StatusBadRequest)
			return
		}
		replyToID, _ := strconv.ParseInt(req.FormValue("id"), 10, 64)
		to := parseAddressList(req.FormValue("to"))
		subject := strings.TrimSpace(req.FormValue("subject"))
		body := strings.TrimSpace(req.FormValue("body"))
		cc := parseAddressList(req.FormValue("cc"))
		bcc := parseAddressList(req.FormValue("bcc"))
		attachments := extractAttachments(req)

		inReplyTo := ""
		references := ""
		if replyToID > 0 && db != nil {
			orig, err := store.GetMessageDetail(req.Context(), db, replyToID)
			if err == nil && orig != nil {
				inReplyTo = orig.MessageID
				references = orig.MessageID
			}
		}

		msgID, rawMIME, sendErr := r.Send(req.Context(), relay.OutboundMessage{
			To: to, CC: cc, BCC: bcc, Subject: subject, BodyText: body,
			InReplyTo: inReplyTo, References: references, Attachments: attachments,
		})
		if db != nil {
			if _, err := store.SaveOutbound(req.Context(), db, r.From(), strings.Join(to, ", "), subject, body, msgID, replyToID, rawMIME, sendErr); err != nil {
				slog.Error("save outbound", "component", "ui", "err", err)
			}
		}
		if sendErr != nil {
			slog.Error("reply send", "component", "ui", "err", sendErr)
			execute(w, "send_error", sendErrorData{Msg: sendErr.Error()})
			return
		}
		execute(w, "send_success", sendSuccessData{To: strings.Join(to, ", "), Subject: subject})
	})

	mux.HandleFunc("/ui/sent", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		limit := 50
		if raw := req.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = n
			}
		}
		var beforeID int64
		if raw := req.URL.Query().Get("before_id"); raw != "" {
			beforeID, _ = strconv.ParseInt(raw, 10, 64)
		}
		items, err := store.ListOutbound(req.Context(), db, limit, beforeID)
		if err != nil {
			http.Error(w, "failed to list sent mail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		execute(w, "sent_list", newSentListData(items, limit))
	})

	mux.HandleFunc("/ui/search", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		q := strings.TrimSpace(req.URL.Query().Get("q"))
		if q == "" {
			fmt.Fprintf(w, `<div class="list-wrap"><p style="color:var(--muted);padding:10px">Enter a search term.</p></div>`)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		items, err := store.SearchMessages(req.Context(), db, q, 50)
		if err != nil {
			slog.Error("search messages", "component", "ui", "err", err)
			execute(w, "search_error", struct{ Msg string }{Msg: "Search temporarily unavailable."})
			return
		}
		opts := store.ListMessageOptions{Limit: 50}
		execute(w, "message_list", newMessageListData(items, opts))
	})

	mux.HandleFunc("/ui/attachment", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(req.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		partIndex, err := strconv.Atoi(req.URL.Query().Get("part"))
		if err != nil || partIndex < 0 {
			http.Error(w, "invalid part", http.StatusBadRequest)
			return
		}
		rawMIME, err := store.GetRawMIMEByMessageID(req.Context(), db, id)
		if err != nil {
			http.Error(w, "failed to fetch message", http.StatusInternalServerError)
			return
		}
		if rawMIME == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		info, data, err := store.GetAttachmentPart(rawMIME, partIndex)
		if err != nil {
			http.Error(w, "attachment not found", http.StatusNotFound)
			return
		}
		ct := safeAttachmentContentType(info.ContentType)
		filename := info.Filename
		if filename == "" {
			filename = fmt.Sprintf("attachment-%d", partIndex)
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(filename)+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	})

	mux.HandleFunc("/ui/message/actions", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(req.FormValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		action := strings.TrimSpace(req.FormValue("action"))
		switch action {
		case "read":
			read := req.FormValue("value") == "1"
			_, err = store.SetMessageRead(req.Context(), db, id, read)
			if err == nil {
				_, _ = store.EnqueueIMAPSyncJob(req.Context(), db, id, "read", map[string]any{"read": read})
			}
		case "archive":
			archived := req.FormValue("value") == "1"
			_, err = store.SetMessageArchived(req.Context(), db, id, archived)
			if err == nil {
				_, _ = store.EnqueueIMAPSyncJob(req.Context(), db, id, "archive", map[string]any{"archived": archived})
			}
			if err != nil {
				http.Error(w, "failed to apply action", http.StatusInternalServerError)
				return
			}
			// Remove message from list and clear reading pane via OOB swap.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			execute(w, "row_removed", id)
			return
		case "snooze":
			hours, hErr := strconv.Atoi(req.FormValue("hours"))
			if hErr != nil || hours <= 0 {
				http.Error(w, "invalid snooze hours", http.StatusBadRequest)
				return
			}
			until := time.Now().UTC().Add(time.Duration(hours) * time.Hour)
			_, err = store.SetMessageSnoozeUntil(req.Context(), db, id, &until)
			if err == nil {
				_, _ = store.EnqueueIMAPSyncJob(req.Context(), db, id, "snooze", map[string]any{"until": until.Format(time.RFC3339)})
			}
		case "clear_snooze":
			_, err = store.SetMessageSnoozeUntil(req.Context(), db, id, nil)
			if err == nil {
				_, _ = store.EnqueueIMAPSyncJob(req.Context(), db, id, "snooze", map[string]any{"until": ""})
			}
		case "spam":
			_, err = store.SetMessageSpam(req.Context(), db, id, req.FormValue("value") == "1")
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "failed to apply action", http.StatusInternalServerError)
			return
		}

		detail, err := store.GetMessageDetail(req.Context(), db, id)
		if err != nil || detail == nil {
			http.Error(w, "failed to reload message", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		execute(w, "message_detail", newMessageDetailData(detail,
			remoteImagesAllowed(req.Context(), db, detail.FromAddr, false)))
	})

	mux.HandleFunc("/ui/push/subscribe", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
			P256dh   string `json:"p256dh"`
			Auth     string `json:"auth"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Endpoint == "" || body.P256dh == "" || body.Auth == "" {
			http.Error(w, "invalid subscription payload", http.StatusBadRequest)
			return
		}
		if err := store.SavePushSubscription(req.Context(), db, body.Endpoint, body.P256dh, body.Auth); err != nil {
			slog.Error("save push subscription", "err", err)
			http.Error(w, "failed to save subscription", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/ui/push/unsubscribe", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Endpoint == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if err := store.DeletePushSubscription(req.Context(), db, body.Endpoint); err != nil {
			slog.Error("delete push subscription", "err", err)
			http.Error(w, "failed to remove subscription", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if oauth != nil && oauth.ClientID != "" && oauth.ClientSecret != "" && oauth.BaseURL != "" {
		registerOAuthRoutes(mux, oauth)
	}
}

func remoteImagesAllowed(ctx context.Context, db *sql.DB, fromHeader string, explicitlyRequested bool) bool {
	if explicitlyRequested {
		return true
	}
	allowed, err := store.RemoteImageSenderAllowed(ctx, db, fromHeader)
	if err != nil {
		slog.Error("check remote-image preference", "component", "ui", "err", err)
		return false
	}
	return allowed
}

// registerOAuthRoutes wires GET /ui/oauth/start and GET /ui/oauth/callback.
func registerOAuthRoutes(mux *http.ServeMux, cfg *OAuthConfig) {
	redirectURI := strings.TrimRight(cfg.BaseURL, "/") + "/ui/oauth/callback"

	// oauthState signs a random nonce with the CSRF secret so we can verify it
	// on callback without storing server-side state.
	signState := func(nonce string) string {
		mac := hmac.New(sha256.New, []byte(cfg.CSRFSecret))
		mac.Write([]byte(nonce))
		return nonce + "." + hex.EncodeToString(mac.Sum(nil))
	}
	verifyState := func(state string) bool {
		dot := strings.LastIndex(state, ".")
		if dot < 1 {
			return false
		}
		nonce, sig := state[:dot], state[dot+1:]
		mac := hmac.New(sha256.New, []byte(cfg.CSRFSecret))
		mac.Write([]byte(nonce))
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(sig), []byte(expected))
	}

	mux.HandleFunc("/ui/oauth/start", func(w http.ResponseWriter, req *http.Request) {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		state := signState(hex.EncodeToString(nonce))

		params := url.Values{
			"client_id":     {cfg.ClientID},
			"redirect_uri":  {redirectURI},
			"response_type": {"code"},
			"scope":         {"https://mail.google.com/"},
			"access_type":   {"offline"},
			"prompt":        {"consent"},
			"state":         {state},
		}
		http.Redirect(w, req, "https://accounts.google.com/o/oauth2/v2/auth?"+params.Encode(), http.StatusFound)
	})

	mux.HandleFunc("/ui/oauth/callback", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if errMsg := q.Get("error"); errMsg != "" {
			slog.Warn("oauth callback rejected", "component", "imap", "reason", "provider_error")
			http.Error(w, "OAuth authorization failed", http.StatusBadRequest)
			return
		}
		if !verifyState(q.Get("state")) {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		refreshToken, err := imap.ExchangeCode(req.Context(), cfg.ClientID, cfg.ClientSecret, code, redirectURI)
		if err != nil {
			slog.Error("oauth callback: exchange code", "err", err)
			http.Error(w, "token exchange failed", http.StatusInternalServerError)
			return
		}

		// Persist to DB so the token survives service restarts.
		if err := store.SetSetting(req.Context(), cfg.DB, "imap.refresh_token", refreshToken); err != nil {
			slog.Error("oauth callback: persist token", "err", err)
			// Non-fatal — update in-memory token anyway.
		}

		// Update the live TokenSource so the fetcher resumes immediately.
		cfg.TokenSource.UpdateRefreshToken(refreshToken)
		slog.Info("oauth re-authorization successful", "component", "imap")

		http.Redirect(w, req, "/ui/accounts?oauth=ok", http.StatusFound)
	})
}

// safeAttachmentContentType returns a content-type safe for serving attachment
// bytes. It passes through a known-safe allow-list and falls back to the
// generic binary type, preventing a malicious email from causing the browser
// to render an attachment as HTML or JavaScript.
func safeAttachmentContentType(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	// Normalise: strip parameters for the comparison only.
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	allowed := map[string]bool{
		"application/pdf":   true,
		"image/jpeg":        true,
		"image/png":         true,
		"image/gif":         true,
		"image/webp":        true,
		"image/svg+xml":     true,
		"text/plain":        true,
		"text/calendar":     true,
		"text/csv":          true,
		"audio/mpeg":        true,
		"audio/ogg":         true,
		"video/mp4":         true,
		"video/webm":        true,
		"application/zip":   true,
		"application/gzip":  true,
		"application/x-tar": true,
	}
	if allowed[base] {
		return ct
	}
	return "application/octet-stream"
}

// sanitizeFilename strips characters that could break the Content-Disposition
// header value or enable path traversal.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '\r', '\n', '\t', '/':
			return '_'
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, name)
}

// extractAttachments reads uploaded files from a parsed multipart form.
func extractAttachments(req *http.Request) []relay.Attachment {
	if req.MultipartForm == nil {
		return nil
	}
	var attachments []relay.Attachment
	for _, fh := range req.MultipartForm.File["attachments"] {
		f, err := fh.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 25<<20))
		f.Close()
		if err != nil || len(data) == 0 {
			continue
		}
		ct := fh.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		attachments = append(attachments, relay.Attachment{
			Filename:    fh.Filename,
			ContentType: ct,
			Data:        data,
		})
	}
	return attachments
}

// buildReplyAllCC returns a comma-separated CC string for reply-all:
// the original To + Cc recipients, excluding the relay's own address.
func buildReplyAllCC(detail *store.MessageDetail, r *relay.Relay) string {
	ownAddr := ""
	if r != nil {
		ownAddr = strings.ToLower(r.From())
	}
	candidates := parseAddressList(detail.ToAddr + ", " + detail.CcAddr)
	kept := make([]string, 0, len(candidates))
	for _, addr := range candidates {
		a, err := mail.ParseAddress(addr)
		if err != nil {
			if strings.ToLower(strings.TrimSpace(addr)) != ownAddr {
				kept = append(kept, addr)
			}
			continue
		}
		if strings.ToLower(a.Address) != ownAddr {
			kept = append(kept, addr)
		}
	}
	return strings.Join(kept, ", ")
}

func parseListOpts(r *http.Request) (store.ListMessageOptions, error) {
	opts := store.ListMessageOptions{Limit: 50}
	q := r.URL.Query()
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return opts, fmt.Errorf("invalid limit")
		}
		opts.Limit = n
	}
	if raw := q.Get("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return opts, fmt.Errorf("invalid before_id")
		}
		opts.BeforeID = n
	}
	if raw := q.Get("unread"); raw != "" {
		b, err := parseBool(raw)
		if err != nil {
			return opts, fmt.Errorf("invalid unread")
		}
		opts.Unread = &b
	}
	if raw := q.Get("archived"); raw != "" {
		b, err := parseBool(raw)
		if err != nil {
			return opts, fmt.Errorf("invalid archived")
		}
		opts.Archived = &b
	}
	if raw := q.Get("source"); raw != "" {
		opts.Source = strings.ToLower(strings.TrimSpace(raw))
	}
	if raw := q.Get("spam"); raw != "" {
		b, err := parseBool(raw)
		if err != nil {
			return opts, fmt.Errorf("invalid spam")
		}
		opts.Spam = &b
	}
	if raw := q.Get("trash"); raw != "" {
		b, err := parseBool(raw)
		if err != nil {
			return opts, fmt.Errorf("invalid trash")
		}
		opts.Trash = b
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
		return false, fmt.Errorf("invalid bool")
	}
}

func parseAddressList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if a := strings.TrimSpace(p); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func extractEmailAddress(addr string) string {
	if a, err := mail.ParseAddress(addr); err == nil {
		return a.Address
	}
	return strings.TrimSpace(addr)
}
