package extract

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var monthNames = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

var weekdayNames = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

const (
	reMon = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`
	reWD  = `(?:(mon|tue|wed|thu|fri|sat|sun)[a-z]*\.?,?\s+)?`
)

// Date forms seen in real mail, most specific first.
var datePatterns = []struct {
	re   *regexp.Regexp
	kind int
}{
	{regexp.MustCompile(`(?i)\b(\d{4})-(\d{2})-(\d{2})\b`), formISO},
	{regexp.MustCompile(`(?i)\b` + reWD + `(\d{1,2})/` + reMon + `/(\d{4})\b`), formDMonY},                                    // Mon 10/Aug/2026
	{regexp.MustCompile(`(?i)\b(\d{1,2})` + reMon + `(\d{2})\b`), formCompact},                                                // 22Dec26
	{regexp.MustCompile(`(?i)\b(\d{1,2})/(\d{1,2})/(\d{4})\b`), formDMY},                                                      // 22/12/2026
	{regexp.MustCompile(`(?i)\b` + reWD + `(\d{1,2})(?:st|nd|rd|th)?\s+` + reMon + `\b\.?,?(?:\s+(\d{4})\b)?`), formDMonYOpt}, // Wed, 16 Dec; 3 Oct 2026
	{regexp.MustCompile(`(?i)\b` + reWD + reMon + `\.?\s+(\d{1,2})(?:st|nd|rd|th)?\b(?:,?\s+(\d{4})\b)?`), formMonDYOpt},      // Thu, Oct 29; October 29, 2026
}

const (
	formISO = iota
	formDMonY
	formCompact
	formDMY
	formDMonYOpt
	formMonDYOpt
)

// dateMatch is a calendar date found in text. Year is 0 when not stated.
type dateMatch struct {
	Start, End int
	Year       int
	Month      time.Month
	Day        int
	Weekday    time.Weekday
	HasWeekday bool
	HasTime    bool
	Hour, Min  int
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func monthOf(s string) time.Month { return monthNames[strings.ToLower(s)[:3]] }

// findDates returns non-overlapping dates in s, each with an attached time
// when one directly follows it.
func findDates(s string) []dateMatch {
	var all []dateMatch
	for _, p := range datePatterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(s, -1) {
			g := func(i int) string {
				if m[2*i] < 0 {
					return ""
				}
				return s[m[2*i]:m[2*i+1]]
			}
			dm := dateMatch{Start: m[0], End: m[1]}
			wd := ""
			switch p.kind {
			case formISO:
				dm.Year, dm.Month, dm.Day = atoiOr(g(1), 0), time.Month(atoiOr(g(2), 0)), atoiOr(g(3), 0)
			case formDMonY:
				wd, dm.Day, dm.Month, dm.Year = g(1), atoiOr(g(2), 0), monthOf(g(3)), atoiOr(g(4), 0)
			case formCompact:
				dm.Day, dm.Month, dm.Year = atoiOr(g(1), 0), monthOf(g(2)), 2000+atoiOr(g(3), 0)
			case formDMY:
				dm.Day, dm.Month, dm.Year = atoiOr(g(1), 0), time.Month(atoiOr(g(2), 0)), atoiOr(g(3), 0)
			case formDMonYOpt:
				wd, dm.Day, dm.Month, dm.Year = g(1), atoiOr(g(2), 0), monthOf(g(3)), atoiOr(g(4), 0)
			case formMonDYOpt:
				wd, dm.Month, dm.Day, dm.Year = g(1), monthOf(g(2)), atoiOr(g(3), 0), atoiOr(g(4), 0)
			}
			if dm.Month < 1 || dm.Month > 12 || dm.Day < 1 || dm.Day > 31 {
				continue
			}
			if wd != "" {
				dm.Weekday, dm.HasWeekday = weekdayNames[strings.ToLower(wd)[:3]], true
			}
			all = append(all, dm)
		}
	}
	// Keep the longest of overlapping matches.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Start != all[j].Start {
			return all[i].Start < all[j].Start
		}
		return all[i].End > all[j].End
	})
	var out []dateMatch
	for _, dm := range all {
		if len(out) > 0 && dm.Start < out[len(out)-1].End {
			if dm.End-dm.Start > out[len(out)-1].End-out[len(out)-1].Start {
				out[len(out)-1] = dm
			}
			continue
		}
		out = append(out, dm)
	}
	for i := range out {
		if h, mi, end, ok := timeAfter(s[out[i].End:]); ok {
			out[i].HasTime, out[i].Hour, out[i].Min = true, h, mi
			out[i].End += end
		}
	}
	return out
}

var (
	reTimeAfter = regexp.MustCompile(`(?i)^[\s,|]{0,4}(?:(?:at|from|between|@|-|–)\s+)?(\d{1,2})[:.](\d{2})(?:\s*([ap])\.?m\.?\b)?`)
	reHourAfter = regexp.MustCompile(`(?i)^[\s,|]{0,4}(?:(?:at|from|between|@)\s+)?(\d{1,2})\s*([ap])\.?m\.?\b`)
	reClock     = regexp.MustCompile(`(?i)^\s*(\d{1,2})(?:[:.](\d{2}))?\s*([ap])\.?m\.?\b|^\s*(\d{1,2})[:.](\d{2})\b`)
)

// timeAfter parses a time directly following a date ("12:00", ", 7:50 am",
// "at 6:00 pm", "between 06:40").
func timeAfter(s string) (hour, min, length int, ok bool) {
	if m := reTimeAfter.FindStringSubmatch(s); m != nil {
		if h, mi, ok := toClock(m[1], m[2], m[3]); ok {
			return h, mi, len(m[0]), true
		}
	}
	if m := reHourAfter.FindStringSubmatch(s); m != nil {
		if h, mi, ok := toClock(m[1], "0", m[2]); ok {
			return h, mi, len(m[0]), true
		}
	}
	return 0, 0, 0, false
}

// parseClock parses a standalone time such as "12:45 PM", "1:00 pm", "8am"
// or "09:15".
func parseClock(s string) (hour, min int, ok bool) {
	m := reClock.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	if m[1] != "" {
		return toClock(m[1], m[2], m[3])
	}
	return toClock(m[4], m[5], "")
}

func toClock(hh, mm, ampm string) (int, int, bool) {
	h, mi := atoiOr(hh, -1), atoiOr(mm, 0)
	if mm == "" {
		mi = 0
	}
	switch strings.ToLower(ampm) {
	case "a", "p":
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		h %= 12
		if strings.ToLower(ampm) == "p" {
			h += 12
		}
	default:
		if h < 0 || h > 23 {
			return 0, 0, false
		}
	}
	if mi < 0 || mi > 59 {
		return 0, 0, false
	}
	return h, mi, true
}

// resolve turns a match into a local date in loc. A missing year is the
// first year that puts the date no more than a week before the anchor (the
// message date). Stated weekdays do not override the date: senders get them
// wrong ("Friday, October 8th" in 2026), and the nearest date is the likelier
// meaning.
func (dm dateMatch) resolve(anchor time.Time, loc *time.Location) (time.Time, bool) {
	try := func(y int) (time.Time, bool) {
		t := time.Date(y, dm.Month, dm.Day, 0, 0, 0, 0, loc)
		if t.Day() != dm.Day {
			return time.Time{}, false
		}
		return t, true
	}
	if dm.Year != 0 {
		return try(dm.Year)
	}
	a := anchor.In(loc)
	floor := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -7)
	for y := a.Year(); y <= a.Year()+1; y++ {
		if t, ok := try(y); ok && !t.Before(floor) {
			return t, true
		}
	}
	return time.Time{}, false
}

var (
	reInDays  = regexp.MustCompile(`(?i)\bin\s+(\d{1,3}|one|two|three|four|five|six|seven|ten|fourteen|thirty)\s+(day|week)s?\b`)
	reTomorow = regexp.MustCompile(`(?i)\btomorrow\b`)
	wordNums  = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "ten": 10, "fourteen": 14, "thirty": 30}
)

// relativeDate finds "in N days/weeks" or "tomorrow" and returns the date it
// means relative to the anchor's local day.
func relativeDate(s string, anchor time.Time, loc *time.Location) (time.Time, bool) {
	a := anchor.In(loc)
	day := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
	if m := reInDays.FindStringSubmatch(s); m != nil {
		n, ok := wordNums[strings.ToLower(m[1])]
		if !ok {
			n = atoiOr(m[1], 0)
		}
		if n <= 0 {
			return time.Time{}, false
		}
		if strings.EqualFold(m[2], "week") {
			n *= 7
		}
		return day.AddDate(0, 0, n), true
	}
	if reTomorow.MatchString(s) {
		return day.AddDate(0, 0, 1), true
	}
	return time.Time{}, false
}
