package calendar

import "time"

// Stored all-day presets represent local 9am on a calendar date. Their minute
// values remain stable for existing rows and reminder claim identities.
func allDayReminderDays(offset int) (int, bool) {
	days, ok := map[int]int{-540: 0, 900: 1, 3780: 3, 9540: 7}[offset]
	return days, ok
}

func ReminderTime(start time.Time, allDay bool, offset int) time.Time {
	if days, ok := allDayReminderDays(offset); allDay && ok {
		day := start.AddDate(0, 0, -days)
		return time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, start.Location())
	}
	return start.Add(-time.Duration(offset) * time.Minute)
}

// ReminderWindow returns candidate starts; ReminderTime performs the final
// grace check. Calendar-day arithmetic also matters when selecting candidates.
func ReminderWindow(now time.Time, grace time.Duration, loc *time.Location, allDay bool, offset int) (time.Time, time.Time) {
	if days, ok := allDayReminderDays(offset); allDay && ok {
		lower, upper := now.Add(-grace).In(loc), now.In(loc)
		from := time.Date(lower.Year(), lower.Month(), lower.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, days)
		to := time.Date(upper.Year(), upper.Month(), upper.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, days+1)
		return from, to
	}
	return now.Add(-grace + time.Duration(offset)*time.Minute), now.Add(time.Duration(offset)*time.Minute + time.Second)
}
