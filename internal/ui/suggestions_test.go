package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSuggestionStripRendering(t *testing.T) {
	var out bytes.Buffer
	execute(&out, "calendar_suggestions", suggestionStripData{MessageID: 5})
	if got := strings.TrimSpace(out.String()); got != `<div class="cal-suggest" id="cal-suggest-5"></div>` {
		t.Fatalf("empty strip must have no content (CSS :empty hides it), got %q", got)
	}
	out.Reset()
	execute(&out, "calendar_suggestions", suggestionStripData{MessageID: 5, Items: []suggestionView{
		{ID: 9, Kind: "flight", Title: "JQ761 ADL → SYD", When: "Tue 22 Dec 09:15–11:15 ACDT", UpdatesWhen: "Tue 22 Dec 09:25–11:15 ACDT"},
		{ID: 10, Kind: "stay", Title: `<b>Hotel</b>`, When: "Wed 16 Dec – Mon 21 Dec", Added: true, Date: "2026-12-16"},
	}, Roster: &rosterHint{Shifts: 4, From: "2026-09-28", To: "2026-10-04"}})
	html := out.String()
	for _, want := range []string{">update</button>", "changes your event from Tue 22 Dec 09:25", `"action":"dismiss"`, "&lt;b&gt;Hotel&lt;/b&gt;", `href="/?calendar=2026-12-16"`, "roster · 4 shifts", `"action":"roster"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("strip missing %q:\n%s", want, html)
		}
	}
}

func TestDefaultReminders(t *testing.T) {
	for _, c := range []struct {
		kind   string
		allDay bool
		want   string
	}{
		{"flight", false, "[1440 180]"}, {"deadline", true, "[3780 -540]"}, {"expiry", true, "[3780 -540]"},
		{"stay", true, "[900]"}, {"promo", false, "[1440 60]"}, {"event", false, "[60]"},
	} {
		got := defaultReminders(c.kind, c.allDay)
		if fmt.Sprint(got) != c.want {
			t.Fatalf("%s/%v = %v, want %s", c.kind, c.allDay, got, c.want)
		}
		// Every default must be a preset the event form can show.
		presets := timedReminderPresets
		if c.allDay {
			presets = allDayReminderPresets
		}
		for _, o := range got {
			found := false
			for _, p := range presets {
				found = found || p.Minutes == o
			}
			if !found {
				t.Fatalf("%s default %d is not a form preset", c.kind, o)
			}
		}
	}
}
