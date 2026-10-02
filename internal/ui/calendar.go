package ui

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"comstac/internal/calendar"
	"comstac/internal/store"
)

const (
	calWeeksBack       = 26                       // weeks rendered above the focus week on open
	calWeeksAhead      = 52                       // weeks rendered below it
	calMaxRefreshWeeks = 2*52*calMaxRangeYear + 2 // whole scrollable range, for refreshes
	calChipsPerDay     = 3
	calAgendaDays      = 45
	calMaxRangeYear    = 10 // how far from today the grid may scroll
)

// calendarKinds are the selectable event kinds, in form order.
var calendarKinds = []string{"custom", "event", "shift", "deadline", "expiry", "flight", "stay", "promo"}

type reminderPreset struct {
	Minutes int
	Label   string
}

var (
	timedReminderPresets = []reminderPreset{
		{0, "at start"}, {10, "10 min before"}, {30, "30 min before"}, {60, "1 hour before"},
		{180, "3 hours before"}, {1440, "1 day before"},
	}
	// All-day events start at local midnight: 900 = 9am the day before,
	// -540 = 9am on the day.
	allDayReminderPresets = []reminderPreset{
		{-540, "9am on the day"}, {900, "9am the day before"}, {3780, "9am 3 days before"},
		{9540, "9am a week before"},
	}
	rosterReminderPresets = []reminderPreset{{0, "none"}, {30, "30 min before"}, {60, "1 hour before"}, {120, "2 hours before"}}
)

type calChip struct {
	ID     int64
	Date   string
	Title  string
	Time   string
	Kind   string
	AllDay bool
}

type calDay struct {
	Date       string
	Num        int
	MonthLabel string // set on the 1st of each month
	Today      bool
	Past       bool
	OddMonth   bool
	EdgeTop    bool // first seven days of a month: boundary above
	EdgeLeft   bool // 1st of a month not on Monday: boundary to the left
	Chips      []calChip
	More       int
}

type calWeek struct {
	Start string
	Label string // month/year shown by the sticky header while this row is on top
	Days  []calDay
}

type calWeeksData struct {
	Weeks []calWeek
}

type calendarViewData struct {
	Weeks  []calWeek
	First  string
	Last   string
	Focus  string // week start to scroll to
	Agenda agendaData
	MinDay string
	MaxDay string
}

type agendaItem struct {
	ID       int64
	Date     string
	Time     string
	Title    string
	Location string
	Kind     string
	Repeats  bool
	Reminder bool
}

type agendaDay struct {
	Date  string
	Label string
	Today bool
	Items []agendaItem
}

type agendaData struct {
	From     string
	Days     []agendaDay
	NextFrom string
	Notice   string
}

type eventFormData struct {
	ID          int64
	Title       string
	Kind        string
	Location    string
	Notes       string
	AllDay      bool
	StartDate   string
	StartTime   string
	EndDate     string
	EndTime     string
	Repeat      string
	ByDay       map[string]bool
	Ends        string
	Until       string
	Count       int
	Reminders   map[int]bool
	OccDate     string
	Repeating   bool
	RepeatDesc  string
	Source      string
	Error       string
	Kinds       []string
	Weekdays    []string
	TimedPreset []reminderPreset
	AllDayPre   []reminderPreset
}

type rosterFormData struct {
	Text      string
	Error     string
	Days      []calendar.RosterDay
	Shifts    int
	Reminders []reminderPreset
	Reminder  int
	Replacing int
}

func todayLocal() time.Time {
	now := time.Now().In(sydneyLoc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, sydneyLoc)
}

func parseDay(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(calendar.DateLayout, s, sydneyLoc)
	if err != nil {
		return time.Time{}, false
	}
	today := todayLocal()
	if t.Before(today.AddDate(-calMaxRangeYear, 0, 0)) || t.After(today.AddDate(calMaxRangeYear, 0, 0)) {
		return time.Time{}, false
	}
	return t, true
}

func weekStart(t time.Time) time.Time {
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

// loadOccurrences expands confirmed events over [from, to) in the home zone.
func loadOccurrences(ctx context.Context, db *sql.DB, from, to time.Time) ([]calendar.Occurrence, map[int64]*store.CalendarEvent, error) {
	events, err := store.ListCalendarEvents(ctx, db, store.CalendarConfirmed)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]*store.CalendarEvent, len(events))
	var occ []calendar.Occurrence
	for i := range events {
		ev := &events[i]
		byID[ev.ID] = ev
		e, err := calendar.NewEvent(ev.ID, ev.Title, ev.Kind, ev.Location, ev.AllDay, ev.StartLocal, ev.EndLocal, ev.TZ, ev.RRule, ev.ExDates)
		if err != nil {
			slog.Warn("skip invalid calendar event", "component", "ui", "event_id", ev.ID, "err", err)
			continue
		}
		occ = append(occ, e.Occurrences(from, to)...)
	}
	calendar.SortOccurrences(occ)
	return occ, byID, nil
}

// occurrenceDays returns the home-zone dates an occurrence is shown on:
// every day of an all-day span, otherwise its start day.
func occurrenceDays(o calendar.Occurrence) []string {
	start := o.Start.In(sydneyLoc)
	if !o.Event.AllDay {
		return []string{start.Format(calendar.DateLayout)}
	}
	var days []string
	for d := start; d.Before(o.End.In(sydneyLoc)) && len(days) < 366; d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format(calendar.DateLayout))
	}
	return days
}

func buildWeeks(ctx context.Context, db *sql.DB, first time.Time, count int) (calWeeksData, error) {
	end := first.AddDate(0, 0, 7*count)
	occ, _, err := loadOccurrences(ctx, db, first, end)
	if err != nil {
		return calWeeksData{}, err
	}
	byDay := map[string][]calChip{}
	for _, o := range occ {
		chip := calChip{ID: o.Event.ID, Date: o.Date(), Title: o.Event.Title, Kind: o.Event.Kind, AllDay: o.Event.AllDay}
		if !o.Event.AllDay {
			chip.Time = o.Start.In(sydneyLoc).Format("15:04")
		}
		for _, d := range occurrenceDays(o) {
			byDay[d] = append(byDay[d], chip)
		}
	}
	today := todayLocal()
	var data calWeeksData
	for w := 0; w < count; w++ {
		ws := first.AddDate(0, 0, 7*w)
		mid := ws.AddDate(0, 0, 3) // Thursday decides which month a row belongs to
		week := calWeek{Start: ws.Format(calendar.DateLayout), Label: mid.Format("January 2006")}
		for i := 0; i < 7; i++ {
			d := ws.AddDate(0, 0, i)
			key := d.Format(calendar.DateLayout)
			day := calDay{
				Date: key, Num: d.Day(), Today: d.Equal(today), Past: d.Before(today),
				OddMonth: (d.Year()*12+int(d.Month()))%2 == 1,
				EdgeTop:  d.Day() <= 7, EdgeLeft: d.Day() == 1 && i > 0,
			}
			if d.Day() == 1 {
				day.MonthLabel = d.Format("Jan")
				if d.Month() == time.January {
					day.MonthLabel = d.Format("Jan 2006")
				}
			}
			chips := byDay[key]
			if len(chips) > calChipsPerDay {
				day.More = len(chips) - calChipsPerDay + 1
				chips = chips[:calChipsPerDay-1]
			}
			day.Chips = chips
			week.Days = append(week.Days, day)
		}
		data.Weeks = append(data.Weeks, week)
	}
	return data, nil
}

func buildAgenda(ctx context.Context, db *sql.DB, from time.Time, days int) (agendaData, error) {
	to := from.AddDate(0, 0, days)
	occ, byID, err := loadOccurrences(ctx, db, from, to)
	if err != nil {
		return agendaData{}, err
	}
	today := todayLocal()
	byDay := map[string][]agendaItem{}
	for _, o := range occ {
		ev := byID[o.Event.ID]
		start := o.Start.In(sydneyLoc)
		item := agendaItem{ID: ev.ID, Date: o.Date(), Title: ev.Title, Location: ev.Location, Kind: ev.Kind,
			Repeats: ev.RRule != "", Reminder: len(ev.Reminders) > 0}
		lastDay := o.End.In(sydneyLoc).AddDate(0, 0, -1)
		switch {
		case ev.AllDay && lastDay.After(start):
			item.Time = "until " + lastDay.Format("Mon 2 Jan")
		case ev.AllDay:
			item.Time = "all day"
		case o.End.After(o.Start):
			item.Time = start.Format("15:04") + "–" + o.End.In(sydneyLoc).Format("15:04")
			if o.End.In(sydneyLoc).Format(calendar.DateLayout) != start.Format(calendar.DateLayout) {
				item.Time += " +1"
			}
		default:
			item.Time = start.Format("15:04")
		}
		for _, d := range occurrenceDays(o) {
			if dt, _ := time.ParseInLocation(calendar.DateLayout, d, sydneyLoc); dt.Before(from) || !dt.Before(to) {
				continue
			}
			byDay[d] = append(byDay[d], item)
		}
	}
	keys := make([]string, 0, len(byDay))
	for k := range byDay {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	data := agendaData{From: from.Format(calendar.DateLayout), NextFrom: to.Format(calendar.DateLayout)}
	fromKey := data.From
	if len(keys) == 0 || keys[0] != fromKey {
		keys = append([]string{fromKey}, keys...)
	}
	for _, k := range keys {
		d, _ := time.ParseInLocation(calendar.DateLayout, k, sydneyLoc)
		data.Days = append(data.Days, agendaDay{Date: k, Label: d.Format("Mon 2 Jan 2006"), Today: d.Equal(today), Items: byDay[k]})
	}
	return data, nil
}

func newEventForm(date time.Time) eventFormData {
	return eventFormData{
		Kind: "custom", StartDate: date.Format(calendar.DateLayout), StartTime: "09:00",
		EndDate: date.Format(calendar.DateLayout), EndTime: "10:00", Repeat: "none", Ends: "never",
		ByDay: map[string]bool{}, Reminders: map[int]bool{},
	}
}

func (f *eventFormData) fill() {
	f.Kinds = calendarKinds
	f.Weekdays = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}
	f.TimedPreset = timedReminderPresets
	f.AllDayPre = allDayReminderPresets
	if f.ByDay == nil {
		f.ByDay = map[string]bool{}
	}
	if f.Reminders == nil {
		f.Reminders = map[int]bool{}
	}
}

func eventFormFromStore(ev *store.CalendarEvent, occDate string) eventFormData {
	f := eventFormData{ID: ev.ID, Title: ev.Title, Kind: ev.Kind, Location: ev.Location, Notes: ev.Notes,
		AllDay: ev.AllDay, Repeat: "none", Ends: "never", ByDay: map[string]bool{}, Reminders: map[int]bool{},
		OccDate: occDate, Source: ev.Source}
	if ev.AllDay {
		f.StartDate, f.EndDate = ev.StartLocal, ev.EndLocal
		if f.EndDate == "" {
			f.EndDate = f.StartDate
		}
		f.StartTime, f.EndTime = "09:00", "10:00"
	} else {
		f.StartDate, f.StartTime, _ = strings.Cut(ev.StartLocal, "T")
		f.EndDate, f.EndTime = f.StartDate, f.StartTime
		if ev.EndLocal != "" {
			f.EndDate, f.EndTime, _ = strings.Cut(ev.EndLocal, "T")
		}
	}
	if r, err := calendar.ParseRule(ev.RRule); err == nil && r.Repeats() {
		f.Repeating = true
		f.RepeatDesc = r.Describe()
		switch {
		case r.Freq == calendar.Daily:
			f.Repeat = "daily"
		case r.Freq == calendar.Weekly && r.Interval == 2:
			f.Repeat = "fortnightly"
		case r.Freq == calendar.Weekly:
			f.Repeat = "weekly"
		case r.Freq == calendar.Monthly:
			f.Repeat = "monthly"
		case r.Freq == calendar.Yearly:
			f.Repeat = "yearly"
		}
		for _, c := range r.DayCodes() {
			f.ByDay[c] = true
		}
		if r.Until != "" {
			f.Ends, f.Until = "until", r.Until
		} else if r.Count > 0 {
			f.Ends, f.Count = "count", r.Count
		}
	}
	for _, r := range ev.Reminders {
		f.Reminders[r.OffsetMinutes] = true
	}
	return f
}

// eventFromForm validates the posted form and returns the event plus its
// reminder offsets, or an error message for the user.
func eventFromForm(req *http.Request) (*store.CalendarEvent, []int, eventFormData, string) {
	f := eventFormData{
		Title: strings.TrimSpace(req.FormValue("title")), Kind: req.FormValue("kind"),
		Location: strings.TrimSpace(req.FormValue("location")), Notes: strings.TrimSpace(req.FormValue("notes")),
		AllDay: req.FormValue("all_day") == "1", StartDate: req.FormValue("start_date"), StartTime: req.FormValue("start_time"),
		EndDate: req.FormValue("end_date"), EndTime: req.FormValue("end_time"), Repeat: req.FormValue("repeat"),
		Ends: req.FormValue("ends"), Until: req.FormValue("until"), ByDay: map[string]bool{}, Reminders: map[int]bool{},
		OccDate: req.FormValue("occ_date"),
	}
	f.ID, _ = strconv.ParseInt(req.FormValue("id"), 10, 64)
	f.Count, _ = strconv.Atoi(req.FormValue("count"))
	for _, c := range req.Form["byday"] {
		f.ByDay[c] = true
	}
	for _, raw := range req.Form["reminder"] {
		if n, err := strconv.Atoi(raw); err == nil {
			f.Reminders[n] = true
		}
	}
	if f.Title == "" {
		return nil, nil, f, "title is required"
	}
	if len(f.Title) > 200 || len(f.Location) > 200 || len(f.Notes) > 4000 {
		return nil, nil, f, "title, location or notes are too long"
	}
	validKind := false
	for _, k := range calendarKinds {
		validKind = validKind || k == f.Kind
	}
	if !validKind {
		f.Kind = "custom"
	}
	if f.EndDate == "" {
		f.EndDate = f.StartDate
	}
	ev := &store.CalendarEvent{ID: f.ID, Title: f.Title, Kind: f.Kind, Location: f.Location, Notes: f.Notes,
		AllDay: f.AllDay, TZ: sydneyLoc.String()}
	if f.AllDay {
		ev.StartLocal, ev.EndLocal = f.StartDate, f.EndDate
		if ev.EndLocal == ev.StartLocal {
			ev.EndLocal = ""
		}
	} else {
		if f.EndTime == "" {
			f.EndTime = f.StartTime
		}
		ev.StartLocal = f.StartDate + "T" + f.StartTime
		ev.EndLocal = f.EndDate + "T" + f.EndTime
	}

	var rule calendar.Rule
	switch f.Repeat {
	case "", "none":
	case "daily":
		rule = calendar.Rule{Freq: calendar.Daily, Interval: 1}
	case "weekly", "fortnightly":
		rule = calendar.Rule{Freq: calendar.Weekly, Interval: 1}
		if f.Repeat == "fortnightly" {
			rule.Interval = 2
		}
		r, err := calendar.ParseRule("FREQ=WEEKLY;BYDAY=" + strings.Join(selectedDays(f.ByDay), ","))
		if err == nil && len(f.ByDay) > 0 {
			rule.ByDay = r.ByDay
		}
	case "monthly":
		rule = calendar.Rule{Freq: calendar.Monthly, Interval: 1}
	case "yearly":
		rule = calendar.Rule{Freq: calendar.Yearly, Interval: 1}
	default:
		return nil, nil, f, "unknown repeat option"
	}
	if rule.Repeats() {
		switch f.Ends {
		case "until":
			if _, err := time.Parse(calendar.DateLayout, f.Until); err != nil || f.Until < f.StartDate {
				return nil, nil, f, "repeat end date must be on or after the start date"
			}
			rule.Until = f.Until
		case "count":
			if f.Count < 1 || f.Count > 1000 {
				return nil, nil, f, "repeat count must be between 1 and 1000"
			}
			rule.Count = f.Count
		}
	}
	ev.RRule = rule.String()
	if _, err := calendar.NewEvent(0, ev.Title, ev.Kind, ev.Location, ev.AllDay, ev.StartLocal, ev.EndLocal, ev.TZ, ev.RRule, ""); err != nil {
		return nil, nil, f, err.Error()
	}

	presets := timedReminderPresets
	if f.AllDay {
		presets = allDayReminderPresets
	}
	var offsets []int
	for _, p := range presets {
		if f.Reminders[p.Minutes] {
			offsets = append(offsets, p.Minutes)
		}
	}
	return ev, offsets, f, ""
}

func selectedDays(set map[string]bool) []string {
	var out []string
	for _, c := range []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"} {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// rosterEvents converts parsed roster days into shift events.
func rosterEvents(days []calendar.RosterDay) []store.CalendarEvent {
	var out []store.CalendarEvent
	for _, d := range days {
		if d.NoShift {
			continue
		}
		endDate := d.Date
		if d.End <= d.Start {
			t, _ := time.Parse(calendar.DateLayout, d.Date)
			endDate = t.AddDate(0, 0, 1).Format(calendar.DateLayout)
		}
		out = append(out, store.CalendarEvent{
			Title: "Work", Kind: "shift", Location: d.Location, Notes: d.Notes, TZ: sydneyLoc.String(),
			StartLocal: d.Date + "T" + d.Start, EndLocal: endDate + "T" + d.End, DedupeKey: "roster/" + d.Date,
		})
	}
	return out
}

func registerCalendarRoutes(mux *http.ServeMux, db *sql.DB) {
	htmlHeader := func(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/html; charset=utf-8") }
	changed := func(w http.ResponseWriter) { w.Header().Set("HX-Trigger", "calendar-changed") }
	serveAgenda := func(w http.ResponseWriter, req *http.Request, from time.Time, notice string) {
		data, err := buildAgenda(req.Context(), db, from, calAgendaDays)
		if err != nil {
			slog.Error("calendar agenda", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to load agenda")
			return
		}
		data.Notice = notice
		htmlHeader(w)
		execute(w, "calendar_agenda", data)
	}

	mux.HandleFunc("/ui/calendar", func(w http.ResponseWriter, req *http.Request) {
		focus := todayLocal()
		if d, ok := parseDay(req.URL.Query().Get("date")); ok {
			focus = d
		}
		first := weekStart(focus).AddDate(0, 0, -7*calWeeksBack)
		count := calWeeksBack + calWeeksAhead
		weeks, err := buildWeeks(req.Context(), db, first, count)
		if err != nil {
			slog.Error("calendar weeks", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to load calendar")
			return
		}
		agenda, err := buildAgenda(req.Context(), db, focus, calAgendaDays)
		if err != nil {
			slog.Error("calendar agenda", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to load agenda")
			return
		}
		today := todayLocal()
		htmlHeader(w)
		execute(w, "calendar_view", calendarViewData{
			Weeks:  weeks.Weeks,
			First:  first.Format(calendar.DateLayout),
			Last:   first.AddDate(0, 0, 7*(count-1)).Format(calendar.DateLayout),
			Focus:  weekStart(focus).Format(calendar.DateLayout),
			Agenda: agenda,
			MinDay: weekStart(today.AddDate(-calMaxRangeYear, 0, 7)).Format(calendar.DateLayout),
			MaxDay: weekStart(today.AddDate(calMaxRangeYear, 0, -7)).Format(calendar.DateLayout),
		})
	})

	mux.HandleFunc("/ui/calendar/weeks", func(w http.ResponseWriter, req *http.Request) {
		from, ok := parseDay(req.URL.Query().Get("from"))
		count, err := strconv.Atoi(req.URL.Query().Get("count"))
		if !ok || err != nil || count < 1 || count > calMaxRefreshWeeks {
			renderUIError(w, http.StatusBadRequest, "invalid week range")
			return
		}
		weeks, err := buildWeeks(req.Context(), db, weekStart(from), count)
		if err != nil {
			slog.Error("calendar weeks", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to load calendar")
			return
		}
		htmlHeader(w)
		execute(w, "calendar_weeks", weeks)
	})

	mux.HandleFunc("/ui/calendar/agenda", func(w http.ResponseWriter, req *http.Request) {
		from, ok := parseDay(req.URL.Query().Get("from"))
		if !ok {
			from = todayLocal()
		}
		serveAgenda(w, req, from, "")
	})

	mux.HandleFunc("/ui/calendar/event", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			q := req.URL.Query()
			var f eventFormData
			if id, err := strconv.ParseInt(q.Get("id"), 10, 64); err == nil && id > 0 {
				ev, err := store.GetCalendarEvent(req.Context(), db, id)
				if err != nil || ev == nil {
					renderUIError(w, http.StatusNotFound, "event not found")
					return
				}
				f = eventFormFromStore(ev, q.Get("date"))
			} else {
				date, ok := parseDay(q.Get("date"))
				if !ok {
					date = todayLocal()
				}
				f = newEventForm(date)
			}
			f.fill()
			htmlHeader(w)
			execute(w, "calendar_event_form", f)
		case http.MethodPost:
			ev, offsets, f, problem := eventFromForm(req)
			if problem == "" && ev.ID > 0 {
				existing, err := store.GetCalendarEvent(req.Context(), db, ev.ID)
				if err != nil || existing == nil {
					problem = "event not found"
				} else {
					// Preserve fields the form does not edit.
					ev.Source, ev.Status, ev.MessageID, ev.Details, ev.DedupeKey = existing.Source, existing.Status, existing.MessageID, existing.Details, existing.DedupeKey
					if ev.RRule == existing.RRule {
						ev.ExDates = existing.ExDates
					}
				}
			}
			if problem == "" {
				if _, err := store.SaveCalendarEvent(req.Context(), db, ev, offsets); err != nil {
					slog.Error("save calendar event", "component", "ui", "err", err)
					problem = "failed to save event"
				}
			}
			if problem != "" {
				f.Error = problem
				f.fill()
				htmlHeader(w)
				execute(w, "calendar_event_form", f)
				return
			}
			start, _ := time.ParseInLocation(calendar.DateLayout, f.StartDate, sydneyLoc)
			changed(w)
			serveAgenda(w, req, start, "saved “"+ev.Title+"”")
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/ui/calendar/event/delete", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, err := strconv.ParseInt(req.FormValue("id"), 10, 64)
		if err != nil || id <= 0 {
			renderUIError(w, http.StatusBadRequest, "invalid event")
			return
		}
		ev, err := store.GetCalendarEvent(req.Context(), db, id)
		if err != nil || ev == nil {
			renderUIError(w, http.StatusNotFound, "event not found")
			return
		}
		from := todayLocal()
		notice := "deleted “" + ev.Title + "”"
		if date, ok := parseDay(req.FormValue("date")); ok && req.FormValue("scope") == "occurrence" && ev.RRule != "" {
			err = store.AddCalendarExDate(req.Context(), db, id, date.Format(calendar.DateLayout))
			from, notice = date, "skipped “"+ev.Title+"” on "+date.Format("Mon 2 Jan")
		} else {
			err = store.DeleteCalendarEvent(req.Context(), db, id)
			if d, ok := parseDay(req.FormValue("date")); ok {
				from = d
			}
		}
		if err != nil {
			slog.Error("delete calendar event", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to delete event")
			return
		}
		changed(w)
		serveAgenda(w, req, from, notice)
	})

	mux.HandleFunc("/ui/calendar/roster", func(w http.ResponseWriter, req *http.Request) {
		htmlHeader(w)
		execute(w, "calendar_roster", rosterFormData{Reminders: rosterReminderPresets, Reminder: 60})
	})

	mux.HandleFunc("/ui/calendar/roster/preview", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data := rosterFormData{Text: req.FormValue("text"), Reminders: rosterReminderPresets, Reminder: 60}
		if len(data.Text) > 20000 {
			data.Text, data.Error = "", "roster text is too long"
		} else if days, err := calendar.ParseRoster(data.Text, time.Now(), sydneyLoc); err != nil {
			data.Error = err.Error()
		} else {
			data.Days = days
			data.Shifts = len(rosterEvents(days))
			data.Replacing, _ = store.CountRosterShifts(req.Context(), db, days[0].Date, days[len(days)-1].Date)
		}
		htmlHeader(w)
		execute(w, "calendar_roster", data)
	})

	mux.HandleFunc("/ui/calendar/roster/import", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		text := req.FormValue("text")
		days, err := calendar.ParseRoster(text, time.Now(), sydneyLoc)
		if err != nil || len(text) > 20000 {
			htmlHeader(w)
			execute(w, "calendar_roster", rosterFormData{Text: text, Error: "roster could not be parsed", Reminders: rosterReminderPresets, Reminder: 60})
			return
		}
		var offsets []int
		if n, err := strconv.Atoi(req.FormValue("reminder")); err == nil && n > 0 && n <= 1440 {
			offsets = []int{n}
		}
		events := rosterEvents(days)
		removed, err := store.ReplaceRosterShifts(req.Context(), db, days[0].Date, days[len(days)-1].Date, events, offsets)
		if err != nil {
			slog.Error("import roster", "component", "ui", "err", err)
			renderUIError(w, http.StatusInternalServerError, "failed to import roster")
			return
		}
		from, _ := time.ParseInLocation(calendar.DateLayout, days[0].Date, sydneyLoc)
		notice := fmt.Sprintf("imported %d shift(s)", len(events))
		if removed > 0 {
			notice += fmt.Sprintf(", replacing %d", removed)
		}
		changed(w)
		serveAgenda(w, req, from, notice)
	})
}
