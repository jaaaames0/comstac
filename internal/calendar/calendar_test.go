package calendar

import (
	"strings"
	"testing"
	"time"
)

var sydney = func() *time.Location {
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		panic(err)
	}
	return loc
}()

func mustEvent(t *testing.T, allDay bool, start, end, rrule, exdates string) *Event {
	t.Helper()
	e, err := NewEvent(1, "e", "custom", "", allDay, start, end, "Australia/Sydney", rrule, exdates)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func occDates(occ []Occurrence, layout string) string {
	var out []string
	for _, o := range occ {
		out = append(out, o.Start.Format(layout))
	}
	return strings.Join(out, " ")
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation(DateLayout, s, sydney)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRuleRoundTrip(t *testing.T) {
	for _, in := range []string{"", "FREQ=DAILY", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE", "FREQ=MONTHLY;UNTIL=20270101", "FREQ=YEARLY;COUNT=3"} {
		r, err := ParseRule(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if r.String() != in {
			t.Fatalf("round trip %q -> %q", in, r.String())
		}
	}
	for _, bad := range []string{"FREQ=HOURLY", "FREQ=DAILY;BYDAY=MO", "INTERVAL=2", "FREQ=DAILY;COUNT=2;UNTIL=20270101", "FREQ=WEEKLY;BYDAY=XX"} {
		if _, err := ParseRule(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	r, _ := ParseRule("FREQ=WEEKLY;INTERVAL=2;BYDAY=WE,MO")
	if r.Describe() != "fortnightly on Mon, Wed" {
		t.Fatalf("describe = %q", r.Describe())
	}
}

func TestOccurrences(t *testing.T) {
	cases := []struct {
		name                  string
		allDay                bool
		start, end, rule, exd string
		from, to              string
		layout                string
		want                  string
	}{
		{"single timed", false, "2026-10-29T12:45", "2026-10-29T14:10", "", "", "2026-10-01", "2026-11-01", "Jan 2 15:04", "Oct 29 12:45"},
		{"single outside range", false, "2026-10-29T12:45", "", "", "", "2026-11-01", "2026-12-01", "Jan 2", ""},
		{"multi-day all-day overlaps range start", true, "2026-12-16", "2026-12-22", "", "", "2026-12-20", "2026-12-21", "Jan 2", "Dec 16"},
		{"daily with exdate", false, "2026-10-01T09:00", "", "FREQ=DAILY;COUNT=4", "2026-10-02", "2026-09-01", "2026-11-01", "Jan 2", "Oct 1 Oct 3 Oct 4"},
		{"fortnightly mon/wed", false, "2026-10-05T09:00", "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE", "", "2026-10-01", "2026-11-01", "Mon Jan 2", "Mon Oct 5 Wed Oct 7 Mon Oct 19 Wed Oct 21"},
		{"weekly byday skips days before start", false, "2026-10-07T09:00", "", "FREQ=WEEKLY;BYDAY=MO,WE", "", "2026-10-01", "2026-10-15", "Mon Jan 2", "Wed Oct 7 Mon Oct 12 Wed Oct 14"},
		{"monthly on 31st skips short months", true, "2026-08-31", "", "FREQ=MONTHLY", "", "2026-08-01", "2027-01-01", "Jan 2", "Aug 31 Oct 31 Dec 31"},
		{"yearly leap day", true, "2028-02-29", "", "FREQ=YEARLY", "", "2028-01-01", "2033-01-01", "2006-01-02", "2028-02-29 2032-02-29"},
		{"until inclusive", true, "2026-10-01", "", "FREQ=DAILY;UNTIL=20261003", "", "2026-09-01", "2026-11-01", "Jan 2", "Oct 1 Oct 2 Oct 3"},
		{"wall clock kept across DST start (4 Oct 2026)", false, "2026-10-03T09:00", "", "FREQ=DAILY;COUNT=3", "", "2026-10-01", "2026-10-10", "Jan 2 15:04 MST", "Oct 3 09:00 AEST Oct 4 09:00 AEDT Oct 5 09:00 AEDT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := mustEvent(t, tc.allDay, tc.start, tc.end, tc.rule, tc.exd)
			got := occDates(e.Occurrences(day(t, tc.from), day(t, tc.to)), tc.layout)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAllDayDurationSurvivesDST(t *testing.T) {
	e := mustEvent(t, true, "2026-10-03", "2026-10-04", "FREQ=DAILY;COUNT=2", "")
	occ := e.Occurrences(day(t, "2026-10-01"), day(t, "2026-10-10"))
	if len(occ) != 2 || occ[0].End.Format("Jan 2 15:04") != "Oct 5 00:00" || occ[1].End.Format("Jan 2 15:04") != "Oct 6 00:00" {
		t.Fatalf("all-day ends drifted: %+v", occ)
	}
}

func TestNewEventRejectsBadInput(t *testing.T) {
	for _, c := range [][]string{
		{"2026-10-29T12:45", "2026-10-29T11:00"},
		{"2026-10-29", ""},
	} {
		if _, err := NewEvent(1, "x", "", "", false, c[0], c[1], "Australia/Sydney", "", ""); err == nil {
			t.Fatalf("expected %v rejected", c)
		}
	}
	if _, err := NewEvent(1, "x", "", "", true, "2026-10-29", "2026-10-28", "Australia/Sydney", "", ""); err == nil {
		t.Fatal("expected end before start rejected")
	}
	if _, err := NewEvent(1, "x", "", "", true, "2026-10-29", "", "Mars/Olympus", "", ""); err == nil {
		t.Fatal("expected bad zone rejected")
	}
}

// The roster text exactly as copied from the roster app on 2026-10-02.
const sampleRoster = `Mon

No Shift

28

Tue

11:45 AM - 08:30 PM

29

REGULAR SEGMENT

Shift Location: Store 072

Wed

11:45 AM -08:30 PM

(30

REGULAR SEGMENT

Shift Location: Store 072

Thu

No Shift

01

No Shift

02

Sat

11:15 AM -07:30 PM

03

REGULAR SEGMENT

Shift Location: Store 072

Sun

11:45 AM -08:15 PM

04

REGULAR SEGMENT

Shift Location: Store 072
`

// The same week copied from the roster app's laptop view (2026-10-02), with
// the trailing tabs after the day numbers.
const sampleRosterLaptop = "Mon\n28\t\nNo Shift\nTue\n29\t\n11:45 AM - 08:30 PM\n\nREGULAR SEGMENT\nShift Location: Store 072\n" +
	"Wed\n30\t\n11:45 AM - 08:30 PM\n\nREGULAR SEGMENT\nShift Location: Store 072\nThu\n01\t\nNo Shift\nFri\n02\t\nNo Shift\n" +
	"Sat\n03\t\n11:15 AM - 07:30 PM\n\nREGULAR SEGMENT\nShift Location: Store 072\nSun\n04\t\n11:45 AM - 08:15 PM\n\nREGULAR SEGMENT\nShift Location: Store 072\n"

func TestParseRosterSample(t *testing.T) {
	for name, text := range map[string]string{"phone": sampleRoster, "laptop": sampleRosterLaptop} {
		t.Run(name, func(t *testing.T) { checkSampleRoster(t, text) })
	}
}

func checkSampleRoster(t *testing.T, text string) {
	t.Helper()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, sydney)
	days, err := ParseRoster(text, now, sydney)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range days {
		s := d.Date + " " + d.Weekday.String()[:3]
		if d.NoShift {
			s += " off"
		} else {
			s += " " + d.Start + "-" + d.End + " @" + d.Location + " [" + d.Notes + "]"
		}
		got = append(got, s)
	}
	want := []string{
		"2026-09-28 Mon off",
		"2026-09-29 Tue 11:45-20:30 @Store 072 [REGULAR SEGMENT]",
		"2026-09-30 Wed 11:45-20:30 @Store 072 [REGULAR SEGMENT]",
		"2026-10-01 Thu off",
		"2026-10-02 Fri off",
		"2026-10-03 Sat 11:15-19:30 @Store 072 [REGULAR SEGMENT]",
		"2026-10-04 Sun 11:45-20:15 @Store 072 [REGULAR SEGMENT]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("roster parse\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseRosterRejectsInconsistentText(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, sydney)
	bad := map[string]string{
		"weekday mismatch": "Mon\n09:00 AM - 05:00 PM\n29\n", // 29 Sep 2026 is a Tuesday; no nearby month fits
		"missing number":   "Mon\n09:00 AM - 05:00 PM\nTue\nNo Shift\n30\n",
		"empty":            "\n\n",
		"bad time":         "Tue\n13:00 PM - 05:00 PM\n29\n",
		"laptop mismatch":  "Mon\n29\nNo Shift\n",
		"laptop no shift":  "Tue\n29\nWed\n30\nNo Shift\n",
	}
	for name, text := range bad {
		if _, err := ParseRoster(text, now, sydney); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	// 24-hour times and an overnight shift are accepted.
	days, err := ParseRoster("Tue\n22:00 - 06:00\n29\n", now, sydney)
	if err != nil || days[0].Start != "22:00" || days[0].End != "06:00" || days[0].Date != "2026-09-29" {
		t.Fatalf("overnight: %+v %v", days, err)
	}
}
