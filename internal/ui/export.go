package ui

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"comstac/internal/mailcontent"
	"comstac/internal/store"
	"comstac/internal/validation"
)

// messageExport is one email in a form an LLM reads cleanly: the headers
// that matter for judging it, authentication results, and a single body.
type messageExport struct {
	Subject     string             `json:"subject"`
	From        string             `json:"from"`
	ReplyTo     string             `json:"reply_to,omitempty"`
	ReturnPath  string             `json:"return_path,omitempty"`
	To          string             `json:"to"`
	Cc          string             `json:"cc,omitempty"`
	Date        string             `json:"date"`
	MessageID   string             `json:"message_id,omitempty"`
	Auth        *validation.Result `json:"authentication,omitempty"`
	Flags       []string           `json:"flags,omitempty"`
	Attachments []exportAttachment `json:"attachments,omitempty"`
	BodyFormat  string             `json:"body_format"` // markdown (from HTML) or text
	Body        string             `json:"body"`
}

type exportAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

func buildMessageExport(d *store.MessageDetail, raw []byte) messageExport {
	e := messageExport{Subject: d.Subject, From: d.FromAddr, To: d.ToAddr, Cc: d.CcAddr,
		MessageID: strings.Trim(d.MessageID, "<>"), BodyFormat: "text", Body: d.BodyText}
	if e.Subject == "" {
		e.Subject = "(no subject)"
	}
	if t, err := parseTimestamp(d.SentAt); err == nil {
		e.Date = t.In(sydneyLoc).Format(time.RFC1123Z)
	} else {
		e.Date = d.DateHdr
	}
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		dec := new(mime.WordDecoder)
		header := func(k string) string {
			v := strings.TrimSpace(msg.Header.Get(k))
			if out, err := dec.DecodeHeader(v); err == nil {
				return out
			}
			return v
		}
		e.ReplyTo = header("Reply-To")
		e.ReturnPath = strings.Trim(header("Return-Path"), "<>")
		if e.ReplyTo == e.From {
			e.ReplyTo = ""
		}
	}
	if d.AuthResults != "" {
		var res validation.Result
		if json.Unmarshal([]byte(d.AuthResults), &res) == nil && res != (validation.Result{}) {
			e.Auth = &res
		}
	}
	if d.Spam {
		e.Flags = append(e.Flags, "marked spam")
	}
	if d.Archived {
		e.Flags = append(e.Flags, "in trash")
	}
	for _, a := range d.Attachments {
		e.Attachments = append(e.Attachments, exportAttachment{Filename: a.Filename, ContentType: a.ContentType})
	}
	if d.BodyHTML != "" {
		if md := mailcontent.HTMLToMarkdown(d.BodyHTML); md != "" {
			e.BodyFormat, e.Body = "markdown", md
		}
	}
	return e
}

func (e messageExport) markdown() string {
	var b strings.Builder
	b.WriteString("# " + e.Subject + "\n\n")
	field := func(k, v string) {
		if v != "" {
			b.WriteString("- **" + k + ":** " + v + "\n")
		}
	}
	field("From", e.From)
	field("Reply-To", e.ReplyTo)
	field("Return-Path", e.ReturnPath)
	field("To", e.To)
	field("Cc", e.Cc)
	field("Date", e.Date)
	field("Message-ID", e.MessageID)
	if a := e.Auth; a != nil {
		var parts []string
		add := func(name, result, domain string) {
			if result == "" {
				return
			}
			if domain != "" {
				result += " (" + domain + ")"
			}
			parts = append(parts, name+" "+result)
		}
		add("SPF", a.SPF, a.SPFDomain)
		add("DKIM", a.DKIM, a.DKIMDomain)
		if a.DMARC != "" {
			parts = append(parts, "DMARC policy "+a.DMARC)
		}
		field("Authentication", strings.Join(parts, ", "))
	}
	field("Flags", strings.Join(e.Flags, ", "))
	if len(e.Attachments) > 0 {
		names := make([]string, len(e.Attachments))
		for i, a := range e.Attachments {
			names[i] = a.Filename
			if a.ContentType != "" {
				names[i] += " (" + a.ContentType + ")"
			}
		}
		field("Attachments", strings.Join(names, ", ")+" (contents not included)")
	}
	b.WriteString("\n---\n\n" + strings.TrimSpace(e.Body) + "\n")
	return b.String()
}

func registerExportRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /ui/message/export", func(w http.ResponseWriter, req *http.Request) {
		if db == nil {
			http.Error(w, "database not configured", http.StatusInternalServerError)
			return
		}
		q := req.URL.Query()
		id, err := strconv.ParseInt(q.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		format := q.Get("format")
		if format != "markdown" && format != "json" {
			http.Error(w, "format must be markdown or json", http.StatusBadRequest)
			return
		}
		detail, err := store.GetMessageDetail(req.Context(), db, id)
		if err != nil {
			slog.Error("message export", "component", "ui", "err", err)
			http.Error(w, "failed to fetch message", http.StatusInternalServerError)
			return
		}
		if detail == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		raw, err := store.GetRawMIMEByMessageID(req.Context(), db, id)
		if err != nil {
			http.Error(w, "failed to fetch message", http.StatusInternalServerError)
			return
		}
		e := buildMessageExport(detail, raw)
		var body []byte
		ext := "md"
		if format == "json" {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(e); err != nil {
				http.Error(w, "encode failed", http.StatusInternalServerError)
				return
			}
			body, ext = buf.Bytes(), "json"
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		} else {
			body = []byte(e.markdown())
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if q.Get("download") == "1" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, exportFilename(e.Subject), ext))
		}
		_, _ = w.Write(body)
	})
}

// exportFilename makes a short ASCII file name from a subject.
func exportFilename(subject string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(subject) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "email"
	}
	return name
}
