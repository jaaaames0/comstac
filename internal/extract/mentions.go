package extract

import "time"

// MentionsUpcomingDate reports whether the subject or body names a date from
// today (home zone) onward, either outright or as "in N days"/"tomorrow"
// counted from the message date. It is the cheap signal for whether a message
// is worth a closer look when the rules found nothing.
func MentionsUpcomingDate(in Input, now time.Time) bool {
	loc := loadZone(HomeZone)
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	anchor := in.Date
	if anchor.IsZero() {
		anchor = now
	}
	d := parseDoc(in.HTML, in.Text)
	for _, line := range append([]string{in.Subject}, d.Lines...) {
		for _, dm := range findDates(line) {
			if t, ok := dm.resolve(anchor, loc); ok && !t.Before(today) && t.Before(today.AddDate(3, 0, 0)) {
				return true
			}
		}
		if t, ok := relativeDate(line, anchor, loc); ok && !t.Before(today) {
			return true
		}
	}
	return false
}
