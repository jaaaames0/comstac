package ui

import (
	"bytes"
	"context"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/store"
)

func formRequest(t *testing.T, v url.Values) *store.CalendarEvent {
	t.Helper()
	req := httptest.NewRequest("POST", "/ui/calendar/event", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	ev, _, _, problem := eventFromForm(req)
	if problem != "" {
		t.Fatalf("unexpected problem %q", problem)
	}
	return ev
}

func TestEventFromForm(t *testing.T) {
	base := url.Values{"title": {"Work"}, "kind": {"shift"}, "start_date": {"2026-10-06"}, "start_time": {"11:45"},
		"end_date": {"2026-10-06"}, "end_time": {"20:30"}, "repeat": {"fortnightly"}, "byday": {"WE", "TU"},
		"ends": {"count"}, "count": {"6"}, "reminder": {"60", "900"}}
	ev := formRequest(t, base)
	if ev.StartLocal != "2026-10-06T11:45" || ev.EndLocal != "2026-10-06T20:30" || ev.RRule != "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,WE;COUNT=6" || ev.Kind != "shift" {
		t.Fatalf("timed event = %+v", ev)
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(base.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	_, offsets, _, _ := eventFromForm(req)
	if len(offsets) != 1 || offsets[0] != 60 {
		t.Fatalf("timed offsets = %v (all-day preset must be ignored)", offsets)
	}

	allDay := url.Values{"title": {"Stay"}, "all_day": {"1"}, "start_date": {"2026-12-16"}, "end_date": {"2026-12-22"}, "repeat": {"none"}, "kind": {"bogus"}}
	ev = formRequest(t, allDay)
	if !ev.AllDay || ev.StartLocal != "2026-12-16" || ev.EndLocal != "2026-12-22" || ev.RRule != "" || ev.Kind != "custom" {
		t.Fatalf("all-day event = %+v", ev)
	}

	for name, v := range map[string]url.Values{
		"no title":     {"title": {" "}, "start_date": {"2026-10-06"}, "start_time": {"09:00"}},
		"end before":   {"title": {"x"}, "start_date": {"2026-10-06"}, "start_time": {"09:00"}, "end_date": {"2026-10-06"}, "end_time": {"08:00"}},
		"until before": {"title": {"x"}, "start_date": {"2026-10-06"}, "start_time": {"09:00"}, "repeat": {"daily"}, "ends": {"until"}, "until": {"2026-10-01"}},
		"bad repeat":   {"title": {"x"}, "start_date": {"2026-10-06"}, "start_time": {"09:00"}, "repeat": {"hourly"}},
		"bad start":    {"title": {"x"}, "start_date": {"tomorrow"}, "start_time": {"09:00"}},
		"zero count":   {"title": {"x"}, "start_date": {"2026-10-06"}, "start_time": {"09:00"}, "repeat": {"daily"}, "ends": {"count"}, "count": {"0"}},
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_ = req.ParseForm()
		if _, _, _, problem := eventFromForm(req); problem == "" {
			t.Fatalf("%s: expected a problem", name)
		}
	}
}

func TestBuildWeeksMarksMonthBoundariesAndChips(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "weeks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ev := range []*store.CalendarEvent{
		{Title: "Stay", Kind: "stay", AllDay: true, StartLocal: "2026-09-29", EndLocal: "2026-10-01"},
		{Title: "Work", Kind: "shift", StartLocal: "2026-09-30T11:45", EndLocal: "2026-09-30T20:30"},
		{Title: "A", StartLocal: "2026-09-30T08:00"},
		{Title: "B", StartLocal: "2026-09-30T09:00"},
	} {
		if _, err := store.SaveCalendarEvent(ctx, db, ev, nil); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := parseDay("2026-09-28")
	data, err := buildWeeks(ctx, db, first, 1)
	if err != nil {
		t.Fatal(err)
	}
	days := data.Weeks[0].Days
	// Thu 1 Oct starts October: boundary above and to its left, label "Oct".
	thu := days[3]
	if thu.Date != "2026-10-01" || !thu.EdgeTop || !thu.EdgeLeft || thu.MonthLabel != "Oct" {
		t.Fatalf("1 Oct cell = %+v", thu)
	}
	if days[0].EdgeTop || days[0].EdgeLeft || days[4].EdgeLeft {
		t.Fatal("unexpected boundary outside the first week of October")
	}
	if data.Weeks[0].Label != "October 2026" {
		t.Fatalf("row label = %q (Thursday decides)", data.Weeks[0].Label)
	}
	// The stay spans 29 Sep - 1 Oct. 30 Sep has four items: two chips are
	// shown and the third slot becomes "+2 more".
	if len(days[1].Chips) != 1 || days[1].Chips[0].Title != "Stay" {
		t.Fatalf("29 Sep chips = %+v", days[1].Chips)
	}
	wed := days[2]
	if len(wed.Chips) != 2 || wed.More != 2 || wed.Chips[0].Title != "Stay" || wed.Chips[1].Time != "08:00" {
		t.Fatalf("30 Sep chips = %+v more %d", wed.Chips, wed.More)
	}

	var out bytes.Buffer
	execute(&out, "calendar_weeks", data)
	html := out.String()
	for _, want := range []string{`data-week="2026-09-28"`, `cal-edge-top cal-edge-left" data-date="2026-10-01"`, `class="cal-chip k-stay cal-chip--allday"`, "+2 more"} {
		if !strings.Contains(html, want) {
			t.Fatalf("weeks html missing %q:\n%s", want, html)
		}
	}
}
