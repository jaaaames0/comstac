package ui

import (
	"fmt"
	"time"
)

// snoozeLocalLayout is the value format of an HTML datetime-local input.
const snoozeLocalLayout = "2006-01-02T15:04"

// maxSnooze bounds how far ahead a snooze may be set.
const maxSnooze = 366 * 24 * time.Hour

type snoozePreset struct {
	Label string // e.g. "tomorrow"
	When  string // Sydney-local wake time for display
	Until string // RFC3339 UTC value posted back to the server
}

// snoozePresets returns Gmail-style wake times in Australia/Sydney: later
// today, tomorrow morning, the weekend and next week. Presets that land on
// the same instant as an earlier one are dropped (e.g. "weekend" on Friday).
func snoozePresets(now time.Time) []snoozePreset {
	local := now.In(sydneyLoc)
	at := func(daysAhead, hour int) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day()+daysAhead, hour, 0, 0, 0, sydneyLoc)
	}
	daysUntil := func(wd time.Weekday) int {
		d := (int(wd) - int(local.Weekday()) + 7) % 7
		if d == 0 {
			d = 7
		}
		return d
	}

	later := at(0, 18)
	laterLabel := "later today"
	if later.Sub(local) < time.Hour {
		later = local.Add(3 * time.Hour)
		if t := later.Truncate(time.Hour); !t.Equal(later) {
			later = t.Add(time.Hour)
		}
		if later.Day() != local.Day() {
			laterLabel = "later"
		}
	}
	weekendLabel := "this weekend"
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		weekendLabel = "next weekend"
	}

	candidates := []struct {
		label string
		t     time.Time
	}{
		{laterLabel, later},
		{"tomorrow", at(1, 8)},
		{weekendLabel, at(daysUntil(time.Saturday), 8)},
		{"next week", at(daysUntil(time.Monday), 8)},
	}
	out := make([]snoozePreset, 0, len(candidates))
	seen := map[int64]bool{}
	for _, c := range candidates {
		if seen[c.t.Unix()] {
			continue
		}
		seen[c.t.Unix()] = true
		when := c.t.In(sydneyLoc).Format("Mon 15:04")
		out = append(out, snoozePreset{Label: c.label, When: when, Until: c.t.UTC().Format(time.RFC3339)})
	}
	return out
}

// parseSnoozeUntil accepts an RFC3339 instant (presets) or a datetime-local
// value interpreted in Australia/Sydney (custom picker). The result must be in
// the future and no more than maxSnooze ahead.
func parseSnoozeUntil(raw string, now time.Time) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t, err = time.ParseInLocation(snoozeLocalLayout, raw, sydneyLoc)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid snooze time")
		}
	}
	if !t.After(now) {
		return time.Time{}, fmt.Errorf("snooze time is in the past")
	}
	if t.Sub(now) > maxSnooze {
		return time.Time{}, fmt.Errorf("snooze time is too far ahead")
	}
	return t.UTC(), nil
}
