package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/mail"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed IANA timezone data so Australia/Sydney works without system tzdata

	"comstac/internal/store"
	"comstac/internal/validation"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var StaticFS embed.FS

var tmpl *template.Template

func init() {
	funcMap := template.FuncMap{
		"fmtTime":        fmtTime,
		"fmtFullDate":    fmtFullDate,
		"fmtSender":      fmtSender,
		"authClass":      authClass,
		"actionBtn":      actionBtn,
		"fmtSnooze":      fmtSnooze,
		"json": func(v any) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}
	var err error
	tmpl, err = template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		panic(fmt.Sprintf("ui: failed to parse templates: %v", err))
	}
}

func execute(w io.Writer, name string, data any) {
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template execute", "template", name, "err", err)
	}
}

var sydneyLoc = func() *time.Location {
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// parseTimestamp parses a timestamp string in RFC3339 or common SQLite formats.
func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp: %q", s)
}

// fmtTime formats a SQLite timestamp string as a Gmail-style expanding date
// in Australia/Sydney time: HH:MM today, "Mon HH:MM" this week,
// "2 Jan" this year, "02/01/06" prior years.
func fmtTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := parseTimestamp(s)
	if err != nil {
		return s
	}
	local := t.In(sydneyLoc)
	now := time.Now().In(sydneyLoc)
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return local.Format("15:04")
	}
	if now.Sub(local) < 7*24*time.Hour {
		return local.Format("Mon 15:04")
	}
	if local.Year() == now.Year() {
		return local.Format("2 Jan")
	}
	return local.Format("02/01/06")
}

// fmtSender extracts just the display name (or email address) from a From header.
func fmtSender(addr string) string {
	if a, err := mail.ParseAddress(addr); err == nil {
		if a.Name != "" {
			return a.Name
		}
		return a.Address
	}
	return addr
}

// authClass maps an auth result string to a CSS class suffix (pass / fail / neutral).
func authClass(result string) string {
	switch result {
	case "pass":
		return "pass"
	case "fail", "reject", "quarantine":
		return "fail"
	default:
		return "neutral"
	}
}


// actionBtn renders a POST action form button as safe HTML (used in reading pane).
func actionBtn(id int64, action, value, label, class string) template.HTML {
	idStr := strconv.FormatInt(id, 10)
	return template.HTML(
		`<form style="display:inline" hx-post="/ui/message/actions" hx-target="#reading-pane" hx-swap="innerHTML">` +
			`<input type="hidden" name="id" value="` + idStr + `"/>` +
			`<input type="hidden" name="action" value="` + template.HTMLEscapeString(action) + `"/>` +
			`<input type="hidden" name="value" value="` + template.HTMLEscapeString(value) + `"/>` +
			`<button class="` + template.HTMLEscapeString(class) + `" type="submit">` + template.HTMLEscapeString(label) + `</button>` +
			`</form>`,
	)
}

// --- template data types ---

type indexData struct {
	CSRFToken      string
	IMAPAuthFailed bool // true when the IMAP token source has an active invalid_grant error
}

type messageListData struct {
	Items        []store.MessageListItem
	ShowSource   bool
	MoreURL      string
	IsTrash      bool
	SourceFilter string
}

func newMessageListData(items []store.MessageListItem, opts store.ListMessageOptions) messageListData {
	d := messageListData{
		Items:        items,
		ShowSource:   opts.Source == "",
		IsTrash:      opts.Trash,
		SourceFilter: opts.Source,
	}
	if len(items) == opts.Limit {
		last := items[len(items)-1].ID
		u := fmt.Sprintf("/ui/messages?limit=%d&before_id=%d", opts.Limit, last)
		if opts.Trash {
			u += "&trash=1"
		} else {
			if opts.Source != "" {
				u += "&source=" + opts.Source
			}
			if opts.Spam != nil {
				if *opts.Spam {
					u += "&spam=1"
				} else {
					u += "&spam=0"
				}
			}
			if opts.Archived != nil {
				if *opts.Archived {
					u += "&archived=1"
				} else {
					u += "&archived=0"
				}
			}
		}
		d.MoreURL = u
	}
	return d
}

type messageDetailData struct {
	store.MessageDetail
	Auth        *validation.Result
	HasReplyAll bool   // true when there are other recipients worth reply-all-ing
	BodyHTML    string // BodyHTML with <base target="_blank"> injected; shadows embedded field
}

func newMessageDetailData(d *store.MessageDetail) messageDetailData {
	out := messageDetailData{MessageDetail: *d}
	if d.AuthResults != "" {
		var res validation.Result
		if err := json.Unmarshal([]byte(d.AuthResults), &res); err == nil {
			out.Auth = &res
		}
	}
	out.HasReplyAll = d.CcAddr != "" || strings.Contains(d.ToAddr, ",")
	if d.BodyHTML != "" {
		out.BodyHTML = `<base target="_blank">` + d.BodyHTML
	}
	return out
}

type accountsData struct {
	Accounts         []store.Account
	OAuthEnabled     bool   // true if web-based Gmail re-authorization is configured
	OAuthStatus      string // "ok" on successful re-auth, "" otherwise
	OAuthCallbackURL string // the redirect URI to register in Google Cloud Console
	IMAPAuthFailed   bool
	IMAPAuthFailedAt string // formatted time when the auth failure was first recorded

	// Push notifications
	VAPIDPublicKey string // base64url-encoded; empty when push is not configured
	PushCount      int    // number of currently registered push subscriptions
}

type composeData struct {
	Title   string // "compose" or "forward"; defaults to "compose" in template
	To      string
	Subject string
	Body    string
}

type replyData struct {
	ReplyToID  int64
	To         string
	CC         string // pre-filled for reply-all
	Subject    string
	QuotedBody string
}

type sentListData struct {
	Items   []store.OutboundListItem
	MoreURL string
}

func newSentListData(items []store.OutboundListItem, limit int) sentListData {
	d := sentListData{Items: items}
	if len(items) > 0 {
		last := items[len(items)-1].ID
		d.MoreURL = fmt.Sprintf("/ui/sent?limit=%d&before_id=%d", limit, last)
	}
	return d
}

type sendSuccessData struct {
	To      string
	Subject string
}

type sendErrorData struct {
	Msg string
}

// fmtFullDate formats a SQLite timestamp as a full human-readable date in
// Australia/Sydney time: "Mon, 2 Jan 2006 15:04:05".
func fmtFullDate(s string) string {
	if s == "" {
		return ""
	}
	t, err := parseTimestamp(s)
	if err != nil {
		return s
	}
	return t.In(sydneyLoc).Format("Mon, 2 Jan 2006 15:04:05")
}

// fmtSnooze formats a snooze timestamp for display; returns "" if empty.
func fmtSnooze(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("Jan 2, 15:04")
}
