package extract

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// dateTimeIn combines a date phrase and a clock phrase in loc.
func dateTimeIn(date, clock string, loc *time.Location, anchor time.Time) (time.Time, bool) {
	ds := findDates(date)
	if len(ds) == 0 {
		return time.Time{}, false
	}
	day, ok := ds[0].resolve(anchor, loc)
	if !ok {
		return time.Time{}, false
	}
	h, m := ds[0].Hour, ds[0].Min
	if !ds[0].HasTime {
		var ok bool
		if h, m, ok = parseClock(clock); !ok {
			return time.Time{}, false
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc), true
}

// fromSabre reads Sabre-generated e-tickets (Virgin Australia), whose flight
// segments carry stable element ids: air-N-flight-number,
// air-N-departure-date, air-N-departure-time and so on.
func fromSabre(d *doc, in Input) []Candidate {
	ref := bookingRef(d, in.Subject)
	var out []Candidate
	for n := 0; n < 30; n++ {
		p := fmt.Sprintf("air-%d-", n)
		num := d.ByID[p+"flight-number"]
		if num == "" {
			break
		}
		code := strings.TrimRight(d.ByID[p+"marketing-airline-code"], " ,")
		depCode := firstNonEmpty(d.ByID[p+"departure-city-code"], d.ByID[p+"departure-city-code2"])
		arrCode := firstNonEmpty(d.ByID[p+"arrival-city-code"], d.ByID[p+"arrival-city-code2"])
		depName := strings.TrimRight(firstNonEmpty(d.ByID[p+"departure-city"], d.ByID[p+"departure-city2"]), " ,")
		arrName := strings.TrimRight(firstNonEmpty(d.ByID[p+"arrival-city"], d.ByID[p+"arrival-city2"]), " ,")
		dep, ok := dateTimeIn(d.ByID[p+"departure-date"], d.ByID[p+"departure-time"], zoneFor(depCode, depName), in.Date)
		if !ok {
			continue
		}
		arr, hasArr := dateTimeIn(d.ByID[p+"arrival-date"], d.ByID[p+"arrival-time"], zoneFor(arrCode, arrName), in.Date)
		out = append(out, flight("sabre", code+num, depCode, depName, dep, arrCode, arrName, arr, hasArr, ref))
	}
	return out
}

var reFlightNumber = regexp.MustCompile(`^([A-Z0-9]{2})\s?(\d{1,4})$`)

// flightColumns maps header text to a column role.
func flightColumn(h string) string {
	h = strings.ToLower(strings.TrimRight(normalize(h), ":"))
	switch h {
	case "date", "flight date":
		return "date"
	case "flight", "flight number", "flight no", "flight no.":
		return "flight"
	case "from", "origin":
		return "from"
	case "to", "destination":
		return "to"
	case "depart", "departs", "departure", "dep", "departure time":
		return "dep"
	case "arrive", "arrives", "arrival", "arr", "arrival time":
		return "arr"
	}
	return ""
}

// fromFlightTables reads flight tables with a Date/Flight/From/Depart/To/
// Arrive header. Data rows may hold one flight each, or stack several flights
// in one row with values separated by line breaks (Jetstar change notices).
func fromFlightTables(d *doc, in Input) []Candidate {
	ref := bookingRef(d, in.Subject)
	var out []Candidate
	for _, rows := range d.Tables {
		for i, row := range rows {
			cols := map[string]int{}
			for j, c := range row {
				if role := flightColumn(c.Text); role != "" {
					cols[role] = j
				}
			}
			need := []string{"date", "flight", "from", "to", "dep"}
			complete := true
			for _, r := range need {
				if _, ok := cols[r]; !ok {
					complete = false
				}
			}
			if !complete {
				continue
			}
			for _, data := range rows[i+1:] {
				if len(data) != len(row) {
					break
				}
				n := len(data[cols["flight"]].Lines)
				for f := 0; f < n; f++ {
					get := func(role string) string {
						j, ok := cols[role]
						if !ok {
							return ""
						}
						lines := data[j].Lines
						switch {
						case f < len(lines):
							return lines[f]
						case len(lines) > 0:
							return lines[len(lines)-1]
						}
						return ""
					}
					m := reFlightNumber.FindStringSubmatch(strings.ToUpper(get("flight")))
					if m == nil {
						continue
					}
					from, to := get("from"), get("to")
					fromCode, toCode := iataForCity(from), iataForCity(to)
					dep, ok := dateTimeIn(get("date"), get("dep"), zoneFor(fromCode, from), in.Date)
					if !ok {
						continue
					}
					arr, hasArr := dateTimeIn(get("date"), get("arr"), zoneFor(toCode, to), in.Date)
					if hasArr && arr.Before(dep) {
						arr = arr.AddDate(0, 0, 1) // overnight arrival on a one-date row
					}
					if fromCode == "" {
						fromCode = from
					}
					if toCode == "" {
						toCode = to
					}
					out = append(out, flight("flight-table", m[1]+m[2], fromCode, from, dep, toCode, to, arr, hasArr, ref))
				}
			}
		}
	}
	return out
}

var (
	reCheckInLabel  = regexp.MustCompile(`(?i)^check[- ]?in\b`)
	reCheckOutLabel = regexp.MustCompile(`(?i)\bcheck[- ]?out\b`)
	reCheckInTime   = regexp.MustCompile(`(?i)check[- ]?in[^0-9]{0,40}?(?:from|at|after|starts at)\s+(\d{1,2}(?::\d{2})?\s*[ap]\.?m\.?|\d{1,2}:\d{2})`)
	reCheckOutTime  = regexp.MustCompile(`(?i)check[- ]?out[^0-9]{0,40}?(?:by|at|before|until)\s+(\d{1,2}(?::\d{2})?\s*[ap]\.?m\.?|\d{1,2}:\d{2})`)
	reStayRef       = regexp.MustCompile(`(?i)(?:itinerary|confirmation|booking|reservation)\s*(?:#|number|no\.)?\s*:?\s*([A-Z0-9-]{6,})`)
	reRefLine       = regexp.MustCompile(`(?i)\b(itinerary|confirmation|booking|reservation)\b.{0,12}(#|number|no\.|:)\s*[A-Z0-9-]{4,}`)
)

// fromStayBlock reads hotel confirmations laid out as "Check-in" and
// "Check-out" labels followed by their two dates and times (Expedia).
func fromStayBlock(d *doc, in Input) []Candidate {
	lines := d.Lines
	for i, l := range lines {
		if !reCheckInLabel.MatchString(l) || len(l) > 40 {
			continue
		}
		var dates []time.Time
		addr := ""
		for _, al := range lines {
			if zoneForAddress(al) != "" && len(al) < 120 {
				addr = al
				break
			}
		}
		loc := loadZone(firstNonEmpty(zoneForAddress(addr), HomeZone))
		hasOut := false
		for j := i; j < len(lines) && j < i+8 && len(dates) < 2; j++ {
			if reCheckOutLabel.MatchString(lines[j]) {
				hasOut = true
			}
			if strings.Contains(strings.ToLower(lines[j]), "cancel") {
				continue
			}
			for _, dm := range findDates(lines[j]) {
				if t, ok := dm.resolve(in.Date, loc); ok && len(dates) < 2 {
					dates = append(dates, t)
				}
			}
		}
		if !hasOut || len(dates) != 2 || !dates[1].After(dates[0]) {
			continue
		}
		inT, outT := dates[0], dates[1]
		var inTime, outTime bool
		window := strings.Join(lines[i:min(len(lines), i+10)], "\n")
		if m := reCheckInTime.FindStringSubmatch(window); m != nil {
			if h, mi, ok := parseClock(m[1]); ok {
				inT, inTime = time.Date(inT.Year(), inT.Month(), inT.Day(), h, mi, 0, 0, loc), true
			}
		}
		if m := reCheckOutTime.FindStringSubmatch(window); m != nil {
			if h, mi, ok := parseClock(m[1]); ok {
				outT, outTime = time.Date(outT.Year(), outT.Month(), outT.Day(), h, mi, 0, 0, loc), true
			}
		} else {
			// Expedia puts the bare check-out time after the check-in time
			// sentence: in the next table cell or on the next line.
			for j := i; j < len(lines) && j < i+10 && !outTime; j++ {
				loc0 := reCheckInTime.FindStringIndex(lines[j])
				if loc0 == nil {
					continue
				}
				next := strings.TrimLeft(lines[j][loc0[1]:], " |")
				if next == "" && j+1 < len(lines) {
					next = lines[j+1]
				}
				if h, mi, ok := parseClock(next); ok && len(next) <= 10 {
					outT, outTime = time.Date(outT.Year(), outT.Month(), outT.Day(), h, mi, 0, 0, loc), true
				}
			}
		}
		name := ""
		for j, rl := range lines {
			if reRefLine.MatchString(rl) && j > 0 && len(lines[j-1]) <= 60 && !strings.ContainsAny(lines[j-1], "0123456789") {
				name = lines[j-1]
				break
			}
		}
		ref := ""
		if m := reStayRef.FindStringSubmatch(strings.Join(lines, "\n")); m != nil {
			ref = m[1]
		}
		return []Candidate{stay("stay-block", name, addr, inT, outT, inTime, outTime, ref, loc)}
	}
	return nil
}
