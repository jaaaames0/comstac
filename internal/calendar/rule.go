// Package calendar holds the pure calendar logic: recurrence rules, occurrence
// expansion and roster parsing. It has no storage or HTTP dependencies.
package calendar

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Freq is a recurrence frequency from the supported RFC 5545 subset.
type Freq string

const (
	Daily   Freq = "DAILY"
	Weekly  Freq = "WEEKLY"
	Monthly Freq = "MONTHLY"
	Yearly  Freq = "YEARLY"
)

// DateLayout is the storage and form layout for calendar dates.
const DateLayout = "2006-01-02"

// maxIterations bounds expansion of any single series.
const maxIterations = 20000

// Rule is the supported RRULE subset: FREQ, INTERVAL, BYDAY (weekly only),
// UNTIL (a date, inclusive) and COUNT. The zero Rule means "does not repeat".
type Rule struct {
	Freq     Freq
	Interval int
	ByDay    []time.Weekday
	Until    string // YYYY-MM-DD, inclusive; "" for none
	Count    int
}

var dayCodes = map[string]time.Weekday{
	"MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday,
	"FR": time.Friday, "SA": time.Saturday, "SU": time.Sunday,
}

var dayNames = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// Repeats reports whether the rule describes a recurring series.
func (r Rule) Repeats() bool { return r.Freq != "" }

// ParseRule parses the supported RRULE subset. An empty string is a valid,
// non-repeating rule.
func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "RRULE:"))
	if s == "" {
		return Rule{}, nil
	}
	r := Rule{Interval: 1}
	for _, part := range strings.Split(s, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return Rule{}, fmt.Errorf("invalid rule part %q", part)
		}
		switch strings.ToUpper(key) {
		case "FREQ":
			switch Freq(strings.ToUpper(val)) {
			case Daily, Weekly, Monthly, Yearly:
				r.Freq = Freq(strings.ToUpper(val))
			default:
				return Rule{}, fmt.Errorf("unsupported frequency %q", val)
			}
		case "INTERVAL":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > 1000 {
				return Rule{}, fmt.Errorf("invalid interval %q", val)
			}
			r.Interval = n
		case "BYDAY":
			for _, code := range strings.Split(val, ",") {
				wd, ok := dayCodes[strings.ToUpper(strings.TrimSpace(code))]
				if !ok {
					return Rule{}, fmt.Errorf("invalid weekday %q", code)
				}
				r.ByDay = append(r.ByDay, wd)
			}
		case "UNTIL":
			d := val
			if len(val) >= 8 && !strings.Contains(val, "-") {
				d = val[:4] + "-" + val[4:6] + "-" + val[6:8]
			}
			if _, err := time.Parse(DateLayout, d); err != nil {
				return Rule{}, fmt.Errorf("invalid until %q", val)
			}
			r.Until = d
		case "COUNT":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > maxIterations {
				return Rule{}, fmt.Errorf("invalid count %q", val)
			}
			r.Count = n
		default:
			return Rule{}, fmt.Errorf("unsupported rule part %q", key)
		}
	}
	if r.Freq == "" {
		return Rule{}, fmt.Errorf("rule has no frequency")
	}
	if len(r.ByDay) > 0 && r.Freq != Weekly {
		return Rule{}, fmt.Errorf("BYDAY is only supported for weekly rules")
	}
	if r.Until != "" && r.Count > 0 {
		return Rule{}, fmt.Errorf("rule cannot have both UNTIL and COUNT")
	}
	r.ByDay = sortedDays(r.ByDay)
	return r, nil
}

// String formats the rule in RRULE syntax (without the "RRULE:" prefix).
func (r Rule) String() string {
	if !r.Repeats() {
		return ""
	}
	parts := []string{"FREQ=" + string(r.Freq)}
	if r.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(r.Interval))
	}
	if len(r.ByDay) > 0 {
		codes := make([]string, 0, len(r.ByDay))
		for _, wd := range sortedDays(r.ByDay) {
			codes = append(codes, dayNames[wd])
		}
		parts = append(parts, "BYDAY="+strings.Join(codes, ","))
	}
	if r.Until != "" {
		parts = append(parts, "UNTIL="+strings.ReplaceAll(r.Until, "-", ""))
	}
	if r.Count > 0 {
		parts = append(parts, "COUNT="+strconv.Itoa(r.Count))
	}
	return strings.Join(parts, ";")
}

// DayCodes returns the BYDAY weekday codes (MO..SU), Monday first.
func (r Rule) DayCodes() []string {
	codes := make([]string, 0, len(r.ByDay))
	for _, wd := range sortedDays(r.ByDay) {
		codes = append(codes, dayNames[wd])
	}
	return codes
}

// Describe returns a short human description such as "every 2 weeks on Mon, Wed".
func (r Rule) Describe() string {
	if !r.Repeats() {
		return ""
	}
	unit := map[Freq]string{Daily: "day", Weekly: "week", Monthly: "month", Yearly: "year"}[r.Freq]
	var out string
	switch {
	case r.Interval == 1 && r.Freq == Daily:
		out = "daily"
	case r.Interval == 1 && r.Freq == Weekly:
		out = "weekly"
	case r.Interval == 2 && r.Freq == Weekly:
		out = "fortnightly"
	case r.Interval == 1 && r.Freq == Monthly:
		out = "monthly"
	case r.Interval == 1 && r.Freq == Yearly:
		out = "yearly"
	default:
		out = fmt.Sprintf("every %d %ss", r.Interval, unit)
	}
	if len(r.ByDay) > 0 {
		names := make([]string, 0, len(r.ByDay))
		for _, wd := range r.ByDay {
			names = append(names, wd.String()[:3])
		}
		out += " on " + strings.Join(names, ", ")
	}
	if r.Until != "" {
		out += " until " + r.Until
	}
	if r.Count > 0 {
		out += fmt.Sprintf(", %d times", r.Count)
	}
	return out
}

// mondayIndex orders weekdays Monday-first.
func mondayIndex(wd time.Weekday) int { return (int(wd) + 6) % 7 }

func sortedDays(days []time.Weekday) []time.Weekday {
	seen := map[time.Weekday]bool{}
	out := make([]time.Weekday, 0, len(days))
	for _, d := range days {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return mondayIndex(out[i]) < mondayIndex(out[j]) })
	return out
}

// starts calls fn with each occurrence start of a series beginning at first,
// in order, until fn returns false, the rule ends or the iteration cap is hit.
// Starts keep first's wall-clock time in its location across DST changes.
func (r Rule) starts(first time.Time, fn func(time.Time) bool) {
	if !r.Repeats() {
		fn(first)
		return
	}
	interval := r.Interval
	if interval < 1 {
		interval = 1
	}
	var until time.Time
	if r.Until != "" {
		u, err := time.ParseInLocation(DateLayout, r.Until, first.Location())
		if err == nil {
			until = u.AddDate(0, 0, 1) // inclusive date
		}
	}
	emitted := 0
	emit := func(t time.Time) bool {
		if !until.IsZero() && !t.Before(until) {
			return false
		}
		if r.Count > 0 && emitted >= r.Count {
			return false
		}
		emitted++
		return fn(t)
	}
	y, m, d := first.Date()
	hh, mm, ss := first.Clock()
	loc := first.Location()
	at := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, hh, mm, ss, 0, loc)
	}

	for k := 0; k < maxIterations; k++ {
		switch r.Freq {
		case Daily:
			if !emit(at(y, m, d+k*interval)) {
				return
			}
		case Weekly:
			days := r.ByDay
			if len(days) == 0 {
				days = []time.Weekday{first.Weekday()}
			}
			weekStart := d - mondayIndex(first.Weekday()) + k*7*interval
			for _, wd := range days {
				t := at(y, m, weekStart+mondayIndex(wd))
				if t.Before(first) {
					continue
				}
				if !emit(t) {
					return
				}
			}
		case Monthly:
			t := at(y, m+time.Month(k*interval), d)
			if t.Day() != d { // month too short: no occurrence
				continue
			}
			if !emit(t) {
				return
			}
		case Yearly:
			t := at(y+k*interval, m, d)
			if t.Day() != d { // 29 Feb in a non-leap year
				continue
			}
			if !emit(t) {
				return
			}
		default:
			return
		}
	}
}
