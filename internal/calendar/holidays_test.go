package calendar

import (
	"strings"
	"testing"
	"time"
)

func publicDays(t *testing.T, y int) map[string]string {
	t.Helper()
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip("no zone data")
	}
	out := map[string]string{}
	for _, h := range HolidaysBetween(time.Date(y, 1, 1, 0, 0, 0, 0, loc), time.Date(y+1, 1, 1, 0, 0, 0, 0, loc), loc) {
		if h.Public() {
			out[h.Date] = h.Name
		}
	}
	return out
}

// Published NSW public holiday lists (nsw.gov.au) for years exercising each
// weekend rule.
func TestNSWPublicHolidays(t *testing.T) {
	cases := map[int][]string{
		// Boxing Day Saturday → Monday 28th; Anzac Day Saturday, no substitute.
		2026: {"2026-01-01", "2026-01-26", "2026-04-03", "2026-04-04", "2026-04-05", "2026-04-06", "2026-04-25",
			"2026-06-08", "2026-10-05", "2026-12-25", "2026-12-26", "2026-12-28"},
		// Christmas Saturday → Monday 27th, Boxing Sunday → Tuesday 28th; Anzac Sunday, no substitute.
		2027: {"2027-01-01", "2027-01-26", "2027-03-26", "2027-03-27", "2027-03-28", "2027-03-29", "2027-04-25",
			"2027-06-14", "2027-10-04", "2027-12-25", "2027-12-26", "2027-12-27", "2027-12-28"},
		// New Year Saturday → Monday 3rd; Christmas Sunday → Tuesday 27th.
		2022: {"2022-01-01", "2022-01-03", "2022-01-26", "2022-04-15", "2022-04-16", "2022-04-17", "2022-04-18",
			"2022-04-25", "2022-06-13", "2022-10-03", "2022-12-25", "2022-12-26", "2022-12-27"},
		// Australia Day Sunday → Monday 27th.
		2025: {"2025-01-01", "2025-01-27", "2025-04-18", "2025-04-19", "2025-04-20", "2025-04-21", "2025-04-25",
			"2025-06-09", "2025-10-06", "2025-12-25", "2025-12-26"},
	}
	for y, want := range cases {
		got := publicDays(t, y)
		if len(got) != len(want) {
			t.Errorf("%d: got %d public holidays %v, want %d", y, len(got), got, len(want))
		}
		for _, d := range want {
			if _, ok := got[d]; !ok {
				t.Errorf("%d: missing %s in %v", y, d, got)
			}
		}
	}
}

func TestHolidayExtras(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip("no zone data")
	}
	hs := HolidaysBetween(time.Date(2026, 1, 1, 0, 0, 0, 0, loc), time.Date(2027, 1, 1, 0, 0, 0, 0, loc), loc)
	find := func(date, kind string) string {
		for _, h := range hs {
			if h.Date == date && h.Kind == kind {
				return h.Name
			}
		}
		return ""
	}
	if n := find("2026-04-05", HolidayDST); !strings.Contains(n, "ends") {
		t.Errorf("DST end on 5 Apr 2026 = %q", n)
	}
	if n := find("2026-10-04", HolidayDST); !strings.Contains(n, "starts") {
		t.Errorf("DST start on 4 Oct 2026 = %q", n)
	}
	if find("2026-08-03", HolidayBank) == "" {
		t.Error("bank holiday missing")
	}
	for date, name := range map[string]string{"2026-05-10": "Mother's Day", "2026-09-06": "Father's Day", "2026-11-03": "Melbourne Cup"} {
		if got := find(date, HolidayObservance); got != name {
			t.Errorf("%s = %q, want %q", date, got, name)
		}
	}
	// Easter Sunday and the DST change share 5 April; the public holiday sorts first.
	var same []string
	for _, h := range hs {
		if h.Date == "2026-04-05" {
			same = append(same, h.Kind)
		}
	}
	if strings.Join(same, ",") != "public,dst" {
		t.Errorf("5 Apr order = %v", same)
	}
}
