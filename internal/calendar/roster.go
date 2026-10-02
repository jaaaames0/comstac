package calendar

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RosterDay is one parsed day of a work roster.
type RosterDay struct {
	Date     string // YYYY-MM-DD
	Weekday  time.Weekday
	NoShift  bool
	Start    string // HH:MM, empty when NoShift
	End      string // HH:MM; may be earlier than Start for an overnight shift
	Location string
	Notes    string
}

var (
	reRosterWeekday = regexp.MustCompile(`(?i)^(mon|tue|wed|thu|fri|sat|sun)[a-z]*\.?$`)
	reRosterNoShift = regexp.MustCompile(`(?i)^no\s+shift$`)
	reRosterDayNum  = regexp.MustCompile(`^[\(\[]?\s*(\d{1,2})\s*[\)\]]?$`)
	reRosterShift   = regexp.MustCompile(`(?i)^(\d{1,2})[:.](\d{2})\s*([ap]\.?m\.?)?\s*(?:-|–|—|to)\s*(\d{1,2})[:.](\d{2})\s*([ap]\.?m\.?)?$`)
	reRosterLoc     = regexp.MustCompile(`(?i)^(?:shift\s+)?location\s*:\s*(.+)$`)
)

var rosterWeekdays = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

type rosterDraft struct {
	weekday    time.Weekday
	hasWeekday bool
	shiftSeen  bool
	noShift    bool
	start, end string
	num        int
	details    []string
}

// ParseRoster parses roster text copied from the roster app. A day has an
// optional weekday, a day number, a shift time or "No Shift", and detail
// lines. The laptop view lists weekday -> number -> shift -> details; the
// phone view lists weekday -> shift -> number -> details and may omit a
// weekday. Either order works: a new day starts when a field the current day
// already has appears again. Encircled day numbers may copy as "(30". Month
// and year are inferred from the day numbers and weekdays, choosing the
// candidate nearest now; text that fits no month is rejected.
func ParseRoster(text string, now time.Time, loc *time.Location) ([]RosterDay, error) {
	var drafts []*rosterDraft
	cur := &rosterDraft{num: -1}
	flush := func() {
		if cur.num >= 0 || cur.shiftSeen || cur.hasWeekday {
			drafts = append(drafts, cur)
		}
		cur = &rosterDraft{num: -1}
	}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := strings.Join(strings.Fields(raw), " ")
		if line == "" {
			continue
		}
		switch {
		case reRosterWeekday.MatchString(line):
			if cur.hasWeekday || (cur.num >= 0 && cur.shiftSeen) {
				flush()
			}
			cur.weekday = rosterWeekdays[strings.ToLower(line[:3])]
			cur.hasWeekday = true
		case reRosterNoShift.MatchString(line):
			if cur.shiftSeen {
				flush()
			}
			cur.shiftSeen, cur.noShift = true, true
		case reRosterShift.MatchString(line):
			if cur.shiftSeen {
				flush()
			}
			start, end, err := parseShiftTimes(line)
			if err != nil {
				return nil, err
			}
			cur.shiftSeen, cur.start, cur.end = true, start, end
		case reRosterDayNum.MatchString(line):
			if cur.num >= 0 {
				flush()
			}
			n, _ := strconv.Atoi(reRosterDayNum.FindStringSubmatch(line)[1])
			if n < 1 || n > 31 {
				return nil, fmt.Errorf("invalid day number %q", line)
			}
			cur.num = n
		default:
			cur.details = append(cur.details, line)
		}
	}
	flush()

	if len(drafts) == 0 {
		return nil, fmt.Errorf("no roster days found")
	}
	for i, d := range drafts {
		if d.num < 0 {
			return nil, fmt.Errorf("day %d has no date number", i+1)
		}
		if !d.shiftSeen {
			return nil, fmt.Errorf("day %d (%d) has no shift time or \"No Shift\"", i+1, d.num)
		}
	}

	dates, err := inferRosterDates(drafts, now.In(loc), loc)
	if err != nil {
		return nil, err
	}
	out := make([]RosterDay, 0, len(drafts))
	for i, d := range drafts {
		day := RosterDay{Date: dates[i].Format(DateLayout), Weekday: dates[i].Weekday(), NoShift: d.noShift, Start: d.start, End: d.end}
		var notes []string
		for _, line := range d.details {
			if m := reRosterLoc.FindStringSubmatch(line); m != nil {
				day.Location = strings.TrimSpace(m[1])
			} else {
				notes = append(notes, line)
			}
		}
		day.Notes = strings.Join(notes, "\n")
		out = append(out, day)
	}
	return out, nil
}

// inferRosterDates tries each month around now as the first day's month; each
// later day is the next date carrying its day number. A candidate is valid
// only if every stated weekday matches.
func inferRosterDates(drafts []*rosterDraft, now time.Time, loc *time.Location) ([]time.Time, error) {
	var best []time.Time
	var bestDist time.Duration
	for delta := -3; delta <= 3; delta++ {
		y, m, _ := now.AddDate(0, delta, 0).Date()
		first := time.Date(y, m, drafts[0].num, 0, 0, 0, 0, loc)
		if first.Day() != drafts[0].num {
			continue
		}
		dates := []time.Time{first}
		ok := true
		for _, d := range drafts[1:] {
			prev := dates[len(dates)-1]
			next := time.Time{}
			for step := 1; step <= 31; step++ {
				c := prev.AddDate(0, 0, step)
				if c.Day() == d.num {
					next = c
					break
				}
			}
			if next.IsZero() {
				ok = false
				break
			}
			dates = append(dates, next)
		}
		if !ok {
			continue
		}
		for i, d := range drafts {
			if d.hasWeekday && dates[i].Weekday() != d.weekday {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		dist := first.Sub(now)
		if dist < 0 {
			dist = -dist
		}
		if best == nil || dist < bestDist {
			best, bestDist = dates, dist
		}
	}
	if best == nil {
		return nil, fmt.Errorf("the day numbers and weekdays do not fit any month near today")
	}
	return best, nil
}

func parseShiftTimes(line string) (string, string, error) {
	m := reRosterShift.FindStringSubmatch(line)
	start, err := clock(m[1], m[2], m[3])
	if err != nil {
		return "", "", err
	}
	end, err := clock(m[4], m[5], m[6])
	if err != nil {
		return "", "", err
	}
	return start, end, nil
}

func clock(hh, mm, ampm string) (string, error) {
	h, _ := strconv.Atoi(hh)
	mi, _ := strconv.Atoi(mm)
	ampm = strings.ToLower(strings.ReplaceAll(ampm, ".", ""))
	switch ampm {
	case "am", "pm":
		if h < 1 || h > 12 {
			return "", fmt.Errorf("invalid time %s:%s %s", hh, mm, ampm)
		}
		h %= 12
		if ampm == "pm" {
			h += 12
		}
	case "":
		if h > 23 {
			return "", fmt.Errorf("invalid time %s:%s", hh, mm)
		}
	}
	if mi > 59 {
		return "", fmt.Errorf("invalid time %s:%s", hh, mm)
	}
	return fmt.Sprintf("%02d:%02d", h, mi), nil
}
