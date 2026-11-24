package calendar

import (
	"sort"
	"time"
)

// Holiday kinds, in display priority order.
const (
	HolidayPublic     = "public"     // gazetted NSW public holiday
	HolidayBank       = "bank"       // NSW Bank Holiday: banks close, most people work
	HolidayDST        = "dst"        // daylight saving starts or ends
	HolidayObservance = "observance" // not a day off
)

// Holiday is a fixed, computed calendar marker. Holidays are not events: they
// are never stored, have no reminders and cannot be edited.
type Holiday struct {
	Date string // YYYY-MM-DD
	Name string
	Kind string
}

// Public reports whether the day is a day off for most people.
func (h Holiday) Public() bool { return h.Kind == HolidayPublic }

// HolidaysBetween returns the NSW holidays, daylight-saving changes in loc
// and common Australian observances on dates in [from, to), sorted by date
// and then kind priority.
func HolidaysBetween(from, to time.Time, loc *time.Location) []Holiday {
	fromKey, toKey := from.In(loc).Format(DateLayout), to.In(loc).Format(DateLayout)
	var out []Holiday
	for y := from.In(loc).Year(); y <= to.In(loc).Year(); y++ {
		for _, h := range holidaysInYear(y, loc) {
			if h.Date >= fromKey && h.Date < toKey {
				out = append(out, h)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return kindRank(out[i].Kind) < kindRank(out[j].Kind)
	})
	return out
}

func kindRank(kind string) int {
	switch kind {
	case HolidayPublic:
		return 0
	case HolidayBank:
		return 1
	case HolidayDST:
		return 2
	}
	return 3
}

// holidaysInYear follows the Public Holidays Act 2010 (NSW) and the Retail
// Trading Act's Bank Holiday. Weekend rules differ by day:
//   - New Year's Day on a weekend adds the following Monday.
//   - Australia Day on a weekend moves to the following Monday.
//   - Anzac Day is never substituted.
//   - Christmas Day or Boxing Day on a weekend adds the next weekday that is
//     not already a holiday (so Christmas on Sunday adds Tuesday 27th).
func holidaysInYear(y int, loc *time.Location) []Holiday {
	date := func(m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	key := func(t time.Time) string { return t.Format(DateLayout) }
	weekend := func(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }
	nextMonday := func(t time.Time) time.Time {
		return t.AddDate(0, 0, (8-int(t.Weekday()))%7)
	}
	var hs []Holiday
	add := func(t time.Time, name, kind string) { hs = append(hs, Holiday{Date: key(t), Name: name, Kind: kind}) }

	nyd := date(time.January, 1)
	add(nyd, "New Year's Day", HolidayPublic)
	if weekend(nyd) {
		add(nextMonday(nyd), "New Year's Day holiday", HolidayPublic)
	}
	if aus := date(time.January, 26); weekend(aus) {
		add(nextMonday(aus), "Australia Day (observed)", HolidayPublic)
	} else {
		add(aus, "Australia Day", HolidayPublic)
	}

	easter := easterSunday(y)
	add(easter.AddDate(0, 0, -2), "Good Friday", HolidayPublic)
	add(easter.AddDate(0, 0, -1), "Easter Saturday", HolidayPublic)
	add(easter, "Easter Sunday", HolidayPublic)
	add(easter.AddDate(0, 0, 1), "Easter Monday", HolidayPublic)
	add(date(time.April, 25), "Anzac Day", HolidayPublic)
	add(nthWeekday(y, time.June, time.Monday, 2), "King's Birthday", HolidayPublic)
	add(nthWeekday(y, time.August, time.Monday, 1), "Bank Holiday (banks only)", HolidayBank)
	add(nthWeekday(y, time.October, time.Monday, 1), "Labour Day", HolidayPublic)

	xmas, boxing := date(time.December, 25), date(time.December, 26)
	add(xmas, "Christmas Day", HolidayPublic)
	add(boxing, "Boxing Day", HolidayPublic)
	taken := map[string]bool{key(xmas): true, key(boxing): true}
	for _, d := range []struct {
		t    time.Time
		name string
	}{{xmas, "Christmas Day holiday"}, {boxing, "Boxing Day holiday"}} {
		if !weekend(d.t) {
			continue
		}
		sub := d.t.AddDate(0, 0, 1)
		for weekend(sub) || taken[key(sub)] {
			sub = sub.AddDate(0, 0, 1)
		}
		taken[key(sub)] = true
		add(sub, d.name, HolidayPublic)
	}

	hs = append(hs, dstChanges(y, loc)...)

	add(date(time.February, 14), "Valentine's Day", HolidayObservance)
	add(nthWeekday(y, time.May, time.Sunday, 2), "Mother's Day", HolidayObservance)
	add(nthWeekday(y, time.September, time.Sunday, 1), "Father's Day", HolidayObservance)
	add(date(time.October, 31), "Halloween", HolidayObservance)
	add(nthWeekday(y, time.November, time.Tuesday, 1), "Melbourne Cup", HolidayObservance)
	add(date(time.November, 11), "Remembrance Day", HolidayObservance)
	add(date(time.December, 24), "Christmas Eve", HolidayObservance)
	add(date(time.December, 31), "New Year's Eve", HolidayObservance)
	return hs
}

// dstChanges finds the days in year y on which loc's UTC offset changes,
// read from the zone database rather than a fixed rule.
func dstChanges(y int, loc *time.Location) []Holiday {
	var out []Holiday
	offset := func(t time.Time) int { _, o := t.Zone(); return o }
	prev := offset(time.Date(y-1, time.December, 31, 12, 0, 0, 0, loc))
	for d := time.Date(y, time.January, 1, 12, 0, 0, 0, loc); d.Year() == y; d = d.AddDate(0, 0, 1) {
		cur := offset(d)
		if cur == prev {
			continue
		}
		name := "Daylight saving ends · clocks back 1h"
		if cur > prev {
			name = "Daylight saving starts · clocks forward 1h"
		}
		out = append(out, Holiday{Date: d.Format(DateLayout), Name: name, Kind: HolidayDST})
		prev = cur
	}
	return out
}

// nthWeekday returns the nth (1-based) given weekday of a month.
func nthWeekday(y int, m time.Month, wd time.Weekday, n int) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, 0, (int(wd)-int(first.Weekday())+7)%7+7*(n-1))
}

// easterSunday is the anonymous Gregorian algorithm (Meeus/Jones/Butcher).
func easterSunday(y int) time.Time {
	a := y % 19
	b, c := y/100, y%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}
