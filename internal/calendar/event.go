package calendar

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TimeLayout is the storage and form layout for a local wall-clock time.
const TimeLayout = "15:04"

// LocalLayout is the storage layout for a local wall-clock date and time.
const LocalLayout = "2006-01-02T15:04"

// Event is a calendar event with times as local wall clock in Loc. All-day
// events start at local midnight; their End is the exclusive midnight after
// the last day.
type Event struct {
	ID       int64
	Title    string
	Kind     string
	Location string
	AllDay   bool
	Start    time.Time
	End      time.Time
	Rule     Rule
	ExDates  map[string]bool // local dates (YYYY-MM-DD) skipped in a series
}

// Occurrence is one instance of an event.
type Occurrence struct {
	Event *Event
	Start time.Time
	End   time.Time
}

// Date returns the occurrence's local start date (YYYY-MM-DD).
func (o Occurrence) Date() string { return o.Start.Format(DateLayout) }

// NewEvent builds an Event from stored local values. For all-day events start
// and end are dates (end inclusive); otherwise they are LocalLayout values and
// end may be empty (zero duration).
func NewEvent(id int64, title, kind, location string, allDay bool, start, end, tz, rrule, exdates string) (*Event, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("invalid time zone %q", tz)
	}
	rule, err := ParseRule(rrule)
	if err != nil {
		return nil, err
	}
	e := &Event{ID: id, Title: title, Kind: kind, Location: location, AllDay: allDay, Rule: rule, ExDates: map[string]bool{}}
	if allDay {
		s, err := time.ParseInLocation(DateLayout, start, loc)
		if err != nil {
			return nil, fmt.Errorf("invalid start date %q", start)
		}
		last := s
		if end != "" {
			if last, err = time.ParseInLocation(DateLayout, end, loc); err != nil {
				return nil, fmt.Errorf("invalid end date %q", end)
			}
		}
		if last.Before(s) {
			return nil, fmt.Errorf("end date is before start date")
		}
		e.Start, e.End = s, last.AddDate(0, 0, 1)
	} else {
		s, err := time.ParseInLocation(LocalLayout, start, loc)
		if err != nil {
			return nil, fmt.Errorf("invalid start %q", start)
		}
		en := s
		if end != "" {
			if en, err = time.ParseInLocation(LocalLayout, end, loc); err != nil {
				return nil, fmt.Errorf("invalid end %q", end)
			}
		}
		if en.Before(s) {
			return nil, fmt.Errorf("end is before start")
		}
		e.Start, e.End = s, en
	}
	for _, d := range strings.Split(exdates, ",") {
		if d = strings.TrimSpace(d); d != "" {
			e.ExDates[d] = true
		}
	}
	return e, nil
}

// Occurrences returns the event's occurrences that overlap [from, to), in
// start order. Zero-length occurrences overlap when they start in the range.
func (e *Event) Occurrences(from, to time.Time) []Occurrence {
	var out []Occurrence
	e.Rule.starts(e.Start, func(start time.Time) bool {
		if !start.Before(to) {
			return false
		}
		end := e.occurrenceEnd(start)
		if e.ExDates[start.Format(DateLayout)] {
			return true
		}
		if end.After(from) || (end.Equal(start) && !start.Before(from)) {
			out = append(out, Occurrence{Event: e, Start: start, End: end})
		}
		return true
	})
	return out
}

// occurrenceEnd keeps the series' duration. All-day spans are measured in
// days so DST changes do not shift them off midnight.
func (e *Event) occurrenceEnd(start time.Time) time.Time {
	if e.AllDay {
		days := int(e.End.Sub(e.Start).Hours()/24 + 0.5)
		return start.AddDate(0, 0, days)
	}
	return start.Add(e.End.Sub(e.Start))
}

// SortOccurrences orders occurrences by start, all-day first on equal starts,
// then by title.
func SortOccurrences(occ []Occurrence) {
	sort.SliceStable(occ, func(i, j int) bool {
		a, b := occ[i], occ[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.Event.AllDay != b.Event.AllDay {
			return a.Event.AllDay
		}
		return a.Event.Title < b.Event.Title
	})
}
