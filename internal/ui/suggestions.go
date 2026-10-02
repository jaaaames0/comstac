package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"comstac/internal/calendar"
	"comstac/internal/store"
)

type suggestionView struct {
	ID          int64
	Kind        string
	Title       string
	When        string
	Location    string
	Added       bool
	Date        string // home-zone start date, for the calendar link
	UpdatesWhen string // current time of the event an update suggestion changes
}

type rosterHint struct {
	Shifts int
	From   string
	To     string
	Error  string
}

type suggestionStripData struct {
	MessageID int64
	Items     []suggestionView
	Roster    *rosterHint
	Notice    string
}

// defaultReminders are applied when a suggestion is added with one click.
// All-day offsets count from local midnight: 900 = 9am the day before,
// -540 = 9am on the day, 3780 = 9am three days before.
func defaultReminders(kind string, allDay bool) []int {
	switch {
	case kind == "flight":
		return []int{1440, 180}
	case allDay && (kind == "deadline" || kind == "expiry"):
		return []int{3780, -540}
	case allDay:
		return []int{900}
	case kind == "deadline" || kind == "expiry" || kind == "promo":
		return []int{1440, 60}
	default:
		return []int{60}
	}
}

// describeWhen formats an event's time in its own zone.
func describeWhen(ev store.CalendarEvent) (when, homeDate string) {
	e, err := calendar.NewEvent(ev.ID, ev.Title, ev.Kind, ev.Location, ev.AllDay, ev.StartLocal, ev.EndLocal, ev.TZ, "", "")
	if err != nil {
		return ev.StartLocal, ""
	}
	homeDate = e.Start.In(sydneyLoc).Format(calendar.DateLayout)
	if ev.AllDay {
		when = e.Start.Format("Mon 2 Jan")
		if last := e.End.AddDate(0, 0, -1); last.After(e.Start) {
			when += " – " + last.Format("Mon 2 Jan")
		}
		return when, homeDate
	}
	when = e.Start.Format("Mon 2 Jan 15:04")
	if e.End.After(e.Start) {
		when += "–" + e.End.Format("15:04")
	}
	if ev.TZ != sydneyLoc.String() {
		when += " " + e.Start.Format("MST")
	}
	return when, homeDate
}

// isOwnMessage reports whether the message was sent from one of the
// operator's own accounts.
func isOwnMessage(ctx context.Context, db *sql.DB, from string) bool {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return false
	}
	accounts, err := store.ListAccounts(ctx, db, "")
	if err != nil {
		return false
	}
	for _, a := range accounts {
		if strings.EqualFold(a.EmailAddress, addr.Address) {
			return true
		}
	}
	return false
}

// rosterFromMessage parses a roster sent to yourself: a message from one of
// your own addresses whose subject starts with "roster".
func rosterFromMessage(ctx context.Context, db *sql.DB, d *store.MessageDetail) ([]calendar.RosterDay, *rosterHint) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(d.Subject)), "roster") || !isOwnMessage(ctx, db, d.FromAddr) {
		return nil, nil
	}
	days, err := calendar.ParseRoster(d.BodyText, time.Now(), sydneyLoc)
	if err != nil {
		return nil, &rosterHint{Error: err.Error()}
	}
	return days, &rosterHint{Shifts: len(rosterEvents(days)), From: days[0].Date, To: days[len(days)-1].Date}
}

func buildSuggestionStrip(ctx context.Context, db *sql.DB, messageID int64) (suggestionStripData, error) {
	data := suggestionStripData{MessageID: messageID}
	events, err := store.ListMessageCalendarEvents(ctx, db, messageID)
	if err != nil {
		return data, err
	}
	for _, ev := range events {
		v := suggestionView{ID: ev.ID, Kind: ev.Kind, Title: ev.Title, Location: ev.Location, Added: ev.Status == store.CalendarConfirmed}
		v.When, v.Date = describeWhen(ev)
		if target := ev.SuggestionDetails().Updates; target != 0 && !v.Added {
			if old, err := store.GetCalendarEvent(ctx, db, target); err == nil && old != nil {
				v.UpdatesWhen, _ = describeWhen(*old)
			}
		}
		data.Items = append(data.Items, v)
	}
	if detail, err := store.GetMessageDetail(ctx, db, messageID); err == nil && detail != nil {
		_, data.Roster = rosterFromMessage(ctx, db, detail)
	}
	return data, nil
}

func registerSuggestionRoutes(mux *http.ServeMux, db *sql.DB) {
	render := func(w http.ResponseWriter, req *http.Request, messageID int64, notice string) {
		data, err := buildSuggestionStrip(req.Context(), db, messageID)
		if err != nil {
			slog.Error("calendar suggestions", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to load dates")
			return
		}
		data.Notice = notice
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		execute(w, "calendar_suggestions", data)
	}

	mux.HandleFunc("/ui/calendar/suggestions", func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(req.URL.Query().Get("message"), 10, 64)
		if err != nil || id <= 0 {
			renderUIError(w, http.StatusBadRequest, "invalid message")
			return
		}
		render(w, req, id, "")
	})

	mux.HandleFunc("/ui/calendar/suggestion", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		messageID, err := strconv.ParseInt(req.FormValue("message"), 10, 64)
		if err != nil || messageID <= 0 {
			renderUIError(w, http.StatusBadRequest, "invalid message")
			return
		}
		id, _ := strconv.ParseInt(req.FormValue("id"), 10, 64)
		notice := ""
		switch req.FormValue("action") {
		case "accept":
			ev, err := store.GetCalendarEvent(req.Context(), db, id)
			if err != nil || ev == nil || ev.MessageID != messageID {
				renderUIError(w, http.StatusNotFound, "suggestion not found")
				return
			}
			if _, err := store.AcceptSuggestion(req.Context(), db, id, defaultReminders(ev.Kind, ev.AllDay)); err != nil {
				if !errors.Is(err, store.ErrNotSuggestion) {
					slog.Error("accept suggestion", "component", "ui", "err", err)
					renderUIError(w, http.StatusInternalServerError, "failed to add event")
					return
				}
			}
			if ev.SuggestionDetails().Updates != 0 {
				notice = "updated “" + ev.Title + "”"
			}
		case "dismiss":
			ev, err := store.GetCalendarEvent(req.Context(), db, id)
			if err != nil || ev == nil || ev.MessageID != messageID {
				renderUIError(w, http.StatusNotFound, "suggestion not found")
				return
			}
			if err := store.DismissSuggestion(req.Context(), db, id); err != nil && !errors.Is(err, store.ErrNotSuggestion) {
				slog.Error("dismiss suggestion", "component", "ui", "err", err)
				renderUIError(w, http.StatusInternalServerError, "failed to dismiss")
				return
			}
		case "roster":
			detail, err := store.GetMessageDetail(req.Context(), db, messageID)
			if err != nil || detail == nil {
				renderUIError(w, http.StatusNotFound, "message not found")
				return
			}
			days, hint := rosterFromMessage(req.Context(), db, detail)
			if hint == nil || hint.Error != "" {
				renderUIError(w, http.StatusBadRequest, "this message is not a roster you sent yourself")
				return
			}
			events := rosterEvents(days)
			removed, err := store.ReplaceRosterShifts(req.Context(), db, days[0].Date, days[len(days)-1].Date, events, []int{60})
			if err != nil {
				slog.Error("import roster from mail", "component", "ui", "err", err)
				renderUIError(w, http.StatusInternalServerError, "failed to import roster")
				return
			}
			notice = fmt.Sprintf("imported %d shift(s)", len(events))
			if removed > 0 {
				notice += fmt.Sprintf(", replacing %d", removed)
			}
		default:
			renderUIError(w, http.StatusBadRequest, "unknown action")
			return
		}
		w.Header().Set("HX-Trigger", "calendar-changed")
		render(w, req, messageID, notice)
	})
}
