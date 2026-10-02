package extract

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// flight builds a flight candidate. Times are wall clock at each airport;
// the end is the arrival instant shown in the departure zone.
func flight(extractor, number, depCode, depName string, dep time.Time, arrCode, arrName string, arr time.Time, hasArr bool, ref string) Candidate {
	from, to := depCode, arrCode
	if from == "" {
		from = depName
	}
	if to == "" {
		to = arrName
	}
	c := Candidate{
		Extractor: extractor, Kind: "flight", TZ: dep.Location().String(),
		Title:      flightTitle(number, from, to),
		StartLocal: localDateTime(dep),
		Key:        "flight/" + strings.ReplaceAll(number, " ", "") + "/" + localDate(dep),
		Details:    map[string]string{"flight": number, "from": from, "to": to},
	}
	if a, ok := airports[depCode]; ok {
		c.Location = a.City + " (" + depCode + ")"
	} else {
		c.Location = depName
	}
	var notes []string
	if ref != "" {
		notes = append(notes, "Booking "+ref)
		c.Details["booking"] = ref
	}
	if hasArr {
		c.EndLocal = localDateTime(arr.In(dep.Location()))
		notes = append(notes, fmt.Sprintf("Arrives %s %s local time", to, arr.Format("15:04")))
	}
	c.Notes = strings.Join(notes, "\n")
	return c
}

func zoneFor(code, name string) *time.Location {
	if a, ok := airports[strings.ToUpper(code)]; ok {
		return loadZone(a.TZ)
	}
	if tz := zoneForCity(name); tz != "" {
		return loadZone(tz)
	}
	return loadZone(HomeZone)
}

// wallClock reads the local date and time written in an ISO-8601 value,
// ignoring any offset. ok is false for unparseable values; allDay is true
// for date-only values.
func wallClock(v string, loc *time.Location) (t time.Time, allDay, ok bool) {
	v = strings.TrimSpace(v)
	if len(v) >= 16 && (v[10] == 'T' || v[10] == ' ') {
		t, err := time.ParseInLocation("2006-01-02T15:04", v[:10]+"T"+v[11:16], loc)
		return t, false, err == nil
	}
	if len(v) >= 10 {
		t, err := time.ParseInLocation("2006-01-02", v[:10], loc)
		return t, true, err == nil
	}
	return time.Time{}, false, false
}

func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	switch v := cur.(type) {
	case string:
		return normalize(v)
	case float64:
		return fmt.Sprintf("%g", v)
	case map[string]any:
		if n, ok := v["name"].(string); ok {
			return normalize(n)
		}
	}
	return ""
}

func address(v any) string {
	switch a := v.(type) {
	case string:
		return normalize(a)
	case map[string]any:
		if s := str(a, "address"); s != "" {
			return s
		}
		if inner, ok := a["address"].(map[string]any); ok {
			a = inner
		}
		var parts []string
		for _, k := range []string{"streetAddress", "addressLocality", "addressRegion", "postalCode"} {
			if s := str(a, k); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

// ldItems flattens JSON-LD documents, arrays and @graph lists into objects.
func ldItems(v any, out *[]map[string]any) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			ldItems(e, out)
		}
	case map[string]any:
		if g, ok := x["@graph"]; ok {
			ldItems(g, out)
		}
		if _, ok := x["@type"]; ok {
			*out = append(*out, x)
		}
	}
}

func ldType(m map[string]any) string {
	switch t := m["@type"].(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			s, _ := t[0].(string)
			return s
		}
	}
	return ""
}

func fromLDJSON(scripts []string, in Input) []Candidate {
	var items []map[string]any
	for _, s := range scripts {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(s)), &v) == nil {
			ldItems(v, &items)
		}
	}
	var out []Candidate
	for _, m := range items {
		res, _ := m["reservationFor"].(map[string]any)
		ref := str(m, "reservationNumber")
		switch typ := ldType(m); {
		case typ == "FlightReservation" && res != nil:
			depCode, arrCode := str(res, "departureAirport", "iataCode"), str(res, "arrivalAirport", "iataCode")
			depName, arrName := str(res, "departureAirport", "name"), str(res, "arrivalAirport", "name")
			dep, _, ok := wallClock(str(res, "departureTime"), zoneFor(depCode, depName))
			if !ok {
				continue
			}
			arr, _, hasArr := wallClock(str(res, "arrivalTime"), zoneFor(arrCode, arrName))
			number := str(res, "airline", "iataCode") + str(res, "flightNumber")
			out = append(out, flight("jsonld", number, depCode, depName, dep, arrCode, arrName, arr, hasArr, ref))
		case typ == "LodgingReservation":
			name := str(m, "reservationFor", "name")
			addr := address(res)
			loc := loadZone(firstNonEmpty(zoneForAddress(addr), HomeZone))
			in, _, ok1 := wallClock(firstNonEmpty(str(m, "checkinTime"), str(m, "checkinDate")), loc)
			outT, _, ok2 := wallClock(firstNonEmpty(str(m, "checkoutTime"), str(m, "checkoutDate")), loc)
			if ok1 && ok2 {
				out = append(out, stay("jsonld", name, addr, in, outT, true, true, ref, loc))
			}
		case strings.HasSuffix(typ, "Reservation") && res != nil && str(res, "startDate") != "":
			out = append(out, ldEvent(res, ref)...)
		case strings.HasSuffix(typ, "Event") && str(m, "startDate") != "":
			out = append(out, ldEvent(m, ref)...)
		case typ == "RentalCarReservation" || typ == "TaxiReservation" || typ == "FoodEstablishmentReservation":
			when := firstNonEmpty(str(m, "pickupTime"), str(m, "startTime"))
			place := firstNonEmpty(str(m, "pickupLocation"), str(res, "name"))
			addr := firstNonEmpty(address(m["pickupLocation"]), address(res))
			loc := loadZone(firstNonEmpty(zoneForAddress(addr), HomeZone))
			t, allDay, ok := wallClock(when, loc)
			if !ok {
				continue
			}
			title := map[string]string{"RentalCarReservation": "Car pick-up", "TaxiReservation": "Pick-up", "FoodEstablishmentReservation": "Reservation"}[typ]
			if place != "" {
				title += " · " + place
			}
			out = append(out, simpleEvent("jsonld", "event", title, addr, t, allDay, ref, in))
		}
	}
	return out
}

func ldEvent(m map[string]any, ref string) []Candidate {
	name := firstNonEmpty(str(m, "name"), "Event")
	addr := firstNonEmpty(address(m["location"]), str(m, "location"))
	loc := loadZone(firstNonEmpty(zoneForAddress(addr), HomeZone))
	start, allDay, ok := wallClock(str(m, "startDate"), loc)
	if !ok {
		return nil
	}
	c := Candidate{Extractor: "jsonld", Kind: "event", Title: name, Location: addr, TZ: loc.String(), AllDay: allDay}
	if allDay {
		c.StartLocal = localDate(start)
	} else {
		c.StartLocal = localDateTime(start)
	}
	if end, endAllDay, ok := wallClock(str(m, "endDate"), loc); ok && end.After(start) {
		if allDay && endAllDay {
			c.EndLocal = localDate(end)
		} else if !allDay {
			c.EndLocal = localDateTime(end)
		}
	}
	c.Key = "event/" + strings.ToLower(name) + "/" + c.StartLocal
	if ref != "" {
		c.Notes = "Booking " + ref
		c.Details = map[string]string{"booking": ref}
	}
	return []Candidate{c}
}

// stay builds an all-day span from check-in to check-out, with the times in
// the notes.
func stay(extractor, name, addr string, in, out time.Time, inTime, outTime bool, ref string, loc *time.Location) Candidate {
	if name == "" {
		name = "Stay"
	}
	c := Candidate{
		Extractor: extractor, Kind: "stay", Title: name, AllDay: true, TZ: loc.String(),
		StartLocal: localDate(in), EndLocal: localDate(out), Location: addr,
		Key:     "stay/" + localDate(in) + "/" + strings.ToLower(name),
		Details: map[string]string{},
	}
	var notes []string
	if inTime && (in.Hour() != 0 || in.Minute() != 0) {
		notes = append(notes, "Check-in from "+in.Format("15:04"))
	}
	if outTime && (out.Hour() != 0 || out.Minute() != 0) {
		notes = append(notes, "Check-out by "+out.Format("15:04"))
	}
	if ref != "" {
		notes = append(notes, "Booking "+ref)
		c.Details["booking"] = ref
	}
	c.Notes = strings.Join(notes, "\n")
	return c
}

func simpleEvent(extractor, kind, title, location string, t time.Time, allDay bool, ref string, in Input) Candidate {
	c := Candidate{Extractor: extractor, Kind: kind, Title: title, Location: location, TZ: t.Location().String(), AllDay: allDay}
	if allDay {
		c.StartLocal = localDate(t)
	} else {
		c.StartLocal = localDateTime(t)
	}
	c.Key = kind + "/" + senderDomain(in.From) + "/" + c.StartLocal
	if ref != "" {
		c.Notes = "Booking " + ref
		c.Details = map[string]string{"booking": ref}
	}
	return c
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// fromICS reads VEVENTs from text/calendar parts (invitations, "add to
// calendar" attachments). Cancellations are ignored.
func fromICS(parts []string, in Input) []Candidate {
	var out []Candidate
	for _, part := range parts {
		unfolded := strings.ReplaceAll(strings.ReplaceAll(part, "\r\n", "\n"), "\n ", "")
		unfolded = strings.ReplaceAll(unfolded, "\n\t", "")
		if strings.Contains(strings.ToUpper(unfolded), "METHOD:CANCEL") {
			continue
		}
		var ev map[string]string
		for _, line := range strings.Split(unfolded, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.EqualFold(line, "BEGIN:VEVENT"):
				ev = map[string]string{}
			case strings.EqualFold(line, "END:VEVENT") && ev != nil:
				if c, ok := icsEvent(ev); ok {
					out = append(out, c)
				}
				ev = nil
			case ev != nil:
				key, val, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				name, params, _ := strings.Cut(key, ";")
				ev[strings.ToUpper(name)] = val
				if params != "" {
					ev[strings.ToUpper(name)+";"] = params
				}
			}
		}
	}
	return out
}

func icsTime(val, params string) (time.Time, bool, bool) {
	home := loadZone(HomeZone)
	loc := home
	for _, p := range strings.Split(params, ";") {
		if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, "TZID") {
			loc = loadZone(strings.Trim(v, `"`))
		}
	}
	switch {
	case len(val) == 8:
		t, err := time.ParseInLocation("20060102", val, loc)
		return t, true, err == nil
	case strings.HasSuffix(val, "Z"):
		t, err := time.Parse("20060102T150405Z", val)
		return t.In(home), false, err == nil
	default:
		t, err := time.ParseInLocation("20060102T150405", val, loc)
		return t, false, err == nil
	}
}

func icsUnescape(s string) string {
	return normalize(strings.NewReplacer(`\n`, " ", `\N`, " ", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(s))
}

func icsEvent(ev map[string]string) (Candidate, bool) {
	start, allDay, ok := icsTime(ev["DTSTART"], ev["DTSTART;"])
	if !ok {
		return Candidate{}, false
	}
	title := firstNonEmpty(icsUnescape(ev["SUMMARY"]), "Event")
	c := Candidate{Extractor: "ics", Kind: "event", Title: title, Location: icsUnescape(ev["LOCATION"]), AllDay: allDay, TZ: start.Location().String()}
	if allDay {
		c.StartLocal = localDate(start)
	} else {
		c.StartLocal = localDateTime(start)
	}
	if end, endAllDay, ok := icsTime(ev["DTEND"], ev["DTEND;"]); ok && end.After(start) {
		switch {
		case allDay && endAllDay:
			if last := end.AddDate(0, 0, -1); last.After(start) {
				c.EndLocal = localDate(last) // DTEND is exclusive
			}
		case !allDay:
			c.EndLocal = localDateTime(end.In(start.Location()))
		}
	}
	if uid := strings.TrimSpace(ev["UID"]); uid != "" {
		c.Key = "ics/" + uid + "/" + c.StartLocal
	} else {
		c.Key = "event/" + strings.ToLower(title) + "/" + c.StartLocal
	}
	return c, true
}
