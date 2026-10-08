package calendar

import (
	"testing"
	"time"
)

func TestReminderTimesAndCandidateWindows(t *testing.T) {
	for _, zone := range []string{"Australia/Sydney", "America/New_York"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, date := range []string{"2026-10-03", "2026-10-04", "2027-04-03", "2027-04-04", "2026-03-07", "2026-03-08", "2026-10-31", "2026-11-01"} {
			day, _ := time.ParseInLocation(DateLayout, date, loc)
			for offset, days := range map[int]int{-540: 0, 900: 1, 3780: 3, 9540: 7} {
				start := day.AddDate(0, 0, days)
				fire := ReminderTime(start, true, offset)
				want := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, loc)
				if !fire.Equal(want) {
					t.Fatalf("%s %s %d: %v != %v", zone, date, offset, fire, want)
				}
				for _, late := range []time.Duration{0, time.Minute, 6*time.Hour - time.Second} {
					// The scheduler may run in UTC; the event owns the date math.
					from, to := ReminderWindow(fire.Add(late).UTC(), 6*time.Hour, loc, true, offset)
					if start.Before(from) || !start.Before(to) {
						t.Fatalf("candidate excluded: %v not in [%v,%v)", start, from, to)
					}
				}
			}
			for _, allDay := range []bool{false, true} {
				for _, offset := range []int{10, 1440, 10080} {
					fire := ReminderTime(day, allDay, offset)
					if day.Sub(fire) != time.Duration(offset)*time.Minute {
						t.Fatal("custom/timed elapsed offset changed")
					}
				}
			}
		}
	}
}
