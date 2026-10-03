package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"comstac/internal/aiextract"
	"comstac/internal/store"
)

func TestSuggestionStripRendering(t *testing.T) {
	var out bytes.Buffer
	// Nothing found: a greyed-out button that cannot open.
	execute(&out, "calendar_suggestions", suggestionStripData{MessageID: 5, State: "off"})
	if html := out.String(); !strings.Contains(html, `id="cal-btn-5" type="button" disabled`) || strings.Contains(html, "dropdown-btn") {
		t.Fatalf("off button:\n%s", html)
	}
	out.Reset()
	execute(&out, "calendar_suggestions", suggestionStripData{MessageID: 5, State: "pending", Pending: 2, Items: []suggestionView{
		{ID: 9, Kind: "flight", Title: "JQ761 ADL → SYD", When: "Tue 22 Dec 09:15–11:15 ACDT", UpdatesWhen: "Tue 22 Dec 09:25–11:15 ACDT"},
		{ID: 10, Kind: "stay", Title: `<b>Hotel</b>`, When: "Wed 16 Dec – Mon 21 Dec", Added: true, Date: "2026-12-16"},
		{ID: 11, Kind: "flight", Title: "JQ460 SYD → BNK", When: "Tue 22 Dec 14:40", Elsewhere: true, Dismissed: true},
	}, Roster: &rosterHint{Shifts: 4, From: "2026-09-28", To: "2026-10-04"}})
	html := out.String()
	for _, want := range []string{`class="act-p dropdown-btn cal-btn" id="cal-btn-5"`, "</svg> 2</button>", `id="cal-menu-5"`, `hx-target="#cal-menu-5" hx-swap="innerHTML"`,
		">update</button>", "changes your event from Tue 22 Dec 09:25", `"action":"dismiss"`, "&lt;b&gt;Hotel&lt;/b&gt;", `href="/?calendar=2026-12-16"`,
		"dismissed earlier", `{"id":"11","message":"5","action":"accept"}`, "roster · 4 shifts", `"action":"roster"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("panel missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, `{"id":"11","message":"5","action":"dismiss"}`) || strings.Contains(html, "hx-swap-oob") {
		t.Fatalf("dismissed item offers dismiss, or initial render is out of band:\n%s", html)
	}
	// An action response is the panel plus the button out of band.
	out.Reset()
	execute(&out, "calendar_panel_update", suggestionStripData{MessageID: 5, State: "added", Update: true, Items: []suggestionView{{ID: 10, Title: "Hotel", Added: true}}})
	html = out.String()
	if !strings.Contains(html, `id="cal-btn-5" type="button"`) || !strings.Contains(html, `hx-swap-oob="true" aria-label="dates">`) || strings.Contains(html, `class="dropdown`) {
		t.Fatalf("update response:\n%s", html)
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

func TestSuggestionStripAIControls(t *testing.T) {
	var out bytes.Buffer
	execute(&out, "calendar_suggestions", suggestionStripData{MessageID: 7, State: "ai", AI: &aiStripView{Domain: "tickets.example"}})
	html := out.String()
	// One touch: the button itself asks AI and opens the panel.
	for _, want := range []string{`hx-post="/ui/calendar/suggestion" hx-target="#cal-menu-7"`, "</svg> AI</button>", "checking with AI…", `"action":"ai"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("AI controls missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "find dates with AI</button>") {
		t.Fatalf("panel offers a second AI call while the first runs:\n%s", html)
	}
	// Once AI is unavailable to the button (e.g. daily limit), the panel offers it.
	out.Reset()
	execute(&out, "calendar_panel_update", suggestionStripData{MessageID: 7, State: "ai", Update: true, Notice: "daily AI limit reached", AI: &aiStripView{Domain: "tickets.example"}})
	html = out.String()
	for _, want := range []string{">find dates with AI</button>", `"action":"ai_sender_on"`, "always for tickets.example"} {
		if !strings.Contains(html, want) {
			t.Fatalf("AI controls missing %q:\n%s", want, html)
		}
	}
	out.Reset()
	execute(&out, "calendar_suggestions", suggestionStripData{MessageID: 7, State: "checked", AI: &aiStripView{Domain: "tickets.example", DomainAuto: true, LastRun: "AI checked Fri 2 Oct 14:02 · 1 found · $0.0021"}})
	html = out.String()
	for _, want := range []string{"cal-btn--quiet", "no upcoming dates found", ">check again with AI</button>", "1 found · $0.0021", `"action":"ai_sender_off"`, "✓ always for"} {
		if !strings.Contains(html, want) {
			t.Fatalf("AI controls missing %q:\n%s", want, html)
		}
	}
	if formatUSD(2700) != "$0.0027" || formatUSD(12_340_000) != "$12.34" {
		t.Fatalf("formatUSD: %s %s", formatUSD(2700), formatUSD(12_340_000))
	}
}

func TestAIFailureExplainsPolicyBlocks(t *testing.T) {
	blocked := fmt.Errorf("wrap: %w", &aiextract.APIError{Status: 400, Code: "content_policy_violation", Message: "blocked"})
	if got := aiFailure(blocked, "openai/gpt-6-luna"); !strings.Contains(got, "openai/gpt-6-luna") || !strings.Contains(got, "COMSTAC_AI_MODEL") {
		t.Fatalf("policy block = %q", got)
	}
	if got := aiFailure(&aiextract.APIError{Status: 500}, "m"); got != "NanoGPT status 500" {
		t.Fatalf("other = %q", got)
	}
	run := &store.AIRun{Status: "error", Error: "NanoGPT status 500", CreatedAt: time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)}
	if got := describeAIRun(run); got != "AI request failed Sat 3 Oct 11:00: NanoGPT status 500" {
		t.Fatalf("describe = %q", got)
	}
}
