package extract

import (
	"regexp"
	"strings"
	"time"
)

// ProposedEvent is one event returned by a language model, before checking.
// Times are written as they appear in the email.
type ProposedEvent struct {
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	AllDay       bool   `json:"all_day"`
	StartDate    string `json:"start_date"`    // YYYY-MM-DD
	StartTime    string `json:"start_time"`    // HH:MM 24-hour, "" when all-day
	EndDate      string `json:"end_date"`      // "" when none
	EndTime      string `json:"end_time"`      // "" when none
	TimeZone     string `json:"time_zone"`     // IANA zone of the start, "" when unknown
	EndTimeZone  string `json:"end_time_zone"` // IANA zone of the end, "" when same or unknown
	Location     string `json:"location"`
	Notes        string `json:"notes"`
	FlightNumber string `json:"flight_number"` // e.g. "JQ761", "" when not a flight
	FromAirport  string `json:"departure_airport"`
	ToAirport    string `json:"arrival_airport"`
	Evidence     string `json:"evidence"` // verbatim quote from the email containing the date
}

// Kinds is the closed set of event kinds.
var Kinds = []string{"flight", "stay", "event", "deadline", "expiry", "promo", "shift"}

// MaxCompactChars caps the text sent to a model.
const MaxCompactChars = 30000

var (
	reURL      = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	reBlankRun = regexp.MustCompile(`\n{3,}`)
)

// CompactText renders a message body for a model: visible text only, table
// cells joined with " | ", links replaced with "[link]", capped at
// MaxCompactChars. It is also the text evidence quotes are checked against.
func CompactText(html, text string) string {
	d := parseDoc(html, text)
	var b strings.Builder
	for _, l := range d.Lines {
		l = reURL.ReplaceAllString(l, "[link]")
		if strings.Trim(l, "[]link |") == "" {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
		if b.Len() >= MaxCompactChars {
			break
		}
	}
	out := reBlankRun.ReplaceAllString(b.String(), "\n\n")
	if r := []rune(out); len(r) > MaxCompactChars {
		out = string(r[:MaxCompactChars])
	}
	return strings.TrimSpace(out)
}

// foldForMatch normalizes text for evidence matching: case, whitespace,
// typographic quotes and dashes, and the " | " cell separators.
var foldReplacer = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", "|", " ", " ", " ")

func foldForMatch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(foldReplacer.Replace(s))), " ")
}

var reWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// quotedNearby reports whether evidence's words occur in the haystack in
// order and close together. Models quoting a table often join cells or lines
// ("Wed, Dec 16 12:45 PM, Dec 16"); an invented quote still fails because its
// words do not appear near each other.
func quotedNearby(haystack, evidence string) bool {
	if strings.Contains(haystack, evidence) {
		return true
	}
	want := reWord.FindAllString(evidence, -1)
	if len(want) == 0 {
		return false
	}
	have := reWord.FindAllString(haystack, -1)
	span := 3*len(want) + 12 // allowed haystack words between first and last match
	for start := range have {
		if have[start] != want[0] {
			continue
		}
		i, j := 1, start+1
		for ; i < len(want) && j < len(have) && j-start <= span; j++ {
			if have[j] == want[i] {
				i++
			}
		}
		if i == len(want) {
			return true
		}
	}
	return false
}

var (
	reRelativeWords = regexp.MustCompile(`(?i)\b(today|tonight|tomorrow|in \d+ (day|week|month)s?|in (a|one|two|three) (day|week|month)s?|next (week|month|mon|tue|wed|thu|fri|sat|sun)[a-z]*|this (mon|tue|wed|thu|fri|sat|sun)[a-z]*|(mon|tues?|wed(nes)?|thu(rs)?|fri|sat(ur)?|sun)(day)?)\b`)
	reHHMM          = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// evidenceSupportsDate reports whether the quoted evidence plausibly states
// the date: a date in it resolves to the same day, it uses relative or
// weekday wording, or it contains the day of the month as a number.
func evidenceSupportsDate(evidence string, day time.Time, anchor time.Time) bool {
	for _, dm := range findDates(evidence) {
		if d, ok := dm.resolve(anchor, day.Location()); ok && d.Format("2006-01-02") == day.Format("2006-01-02") {
			return true
		}
	}
	if reRelativeWords.MatchString(evidence) {
		return true
	}
	re := regexp.MustCompile(`(^|\D)0?` + day.Format("2") + `(st|nd|rd|th)?(\D|$)`)
	return re.MatchString(evidence)
}

func validKind(k string) string {
	for _, v := range Kinds {
		if k == v {
			return k
		}
	}
	return "event"
}

// FromProposals checks model output against the message and turns what
// survives into candidates. An event is dropped when its evidence is not a
// verbatim (whitespace- and case-insensitive) quote of the subject or body,
// when the evidence does not support its date, when its date or times do not
// parse, or when it starts at or before now or more than three years ahead.
// The returned count is how many were dropped.
func FromProposals(proposals []ProposedEvent, in Input, compact string, now time.Time, extractor string) (out []Candidate, dropped int) {
	haystack := foldForMatch(in.Subject + "\n" + compact)
	seen := map[string]bool{}
	for _, p := range proposals {
		c, ok := proposalCandidate(p, in, haystack, now, extractor)
		if !ok || seen[c.Key] {
			dropped++
			continue
		}
		seen[c.Key] = true
		out = append(out, c)
	}
	return out, dropped
}

var reControl = regexp.MustCompile(`[\x00-\x1f\x7f]+`)

func proposalCandidate(p ProposedEvent, in Input, haystack string, now time.Time, extractor string) (Candidate, bool) {
	title := truncate(reControl.ReplaceAllString(p.Title, " "), 120)
	evidence := strings.TrimSpace(p.Evidence)
	if title == "" || len(foldForMatch(evidence)) < 4 || !quotedNearby(haystack, foldForMatch(evidence)) {
		return Candidate{}, false
	}
	kind := validKind(p.Kind)
	flightNo := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(p.FlightNumber), " ", ""))
	from, to := strings.ToUpper(strings.TrimSpace(p.FromAirport)), strings.ToUpper(strings.TrimSpace(p.ToAirport))
	if reFlightNumber.MatchString(flightNo) {
		kind = "flight" // a flight number decides the kind, whatever the model labelled it
	}

	// Zones: airports from the built-in table beat the model's guess.
	loc := loadZone(firstNonEmpty(strings.TrimSpace(p.TimeZone), HomeZone))
	endLoc := loc
	if tz := strings.TrimSpace(p.EndTimeZone); tz != "" {
		endLoc = loadZone(tz)
	}
	if kind == "flight" {
		if a, ok := airports[from]; ok {
			loc, endLoc = loadZone(a.TZ), loadZone(a.TZ)
		}
		if a, ok := airports[to]; ok {
			endLoc = loadZone(a.TZ)
		}
	}

	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(p.StartDate), loc)
	relative := false
	if rel, ok := relativeDate(evidence, in.Date, loc); ok && len(findDates(evidence)) == 0 {
		// "expires in 14 days": compute the date rather than trust the model.
		day, err, relative = rel, nil, true
	}
	if err != nil || !evidenceSupportsDate(evidence, day, in.Date) {
		return Candidate{}, false
	}
	c := Candidate{
		Extractor: extractor, Kind: kind, Title: title, TZ: loc.String(),
		Location: truncate(p.Location, 200), Notes: truncate(p.Notes, 1000), Evidence: truncate(evidence, 240),
		Details: map[string]string{},
	}
	start := day
	// Stays are all-day spans with times in the notes, as from the rules.
	allDay := p.AllDay || strings.TrimSpace(p.StartTime) == "" || relative || kind == "stay"
	if kind == "stay" {
		var times []string
		if t := strings.TrimSpace(p.StartTime); reHHMM.MatchString(t) {
			times = append(times, "Check-in from "+t)
		}
		if t := strings.TrimSpace(p.EndTime); reHHMM.MatchString(t) {
			times = append(times, "Check-out by "+t)
		}
		if len(times) > 0 {
			c.Notes = strings.TrimSpace(strings.Join(times, "\n") + "\n" + c.Notes)
		}
	}
	if allDay {
		c.AllDay, c.StartLocal = true, localDate(day)
		if end, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(p.EndDate), loc); err == nil && end.After(day) && !relative {
			c.EndLocal = localDate(end)
		}
	} else {
		if !reHHMM.MatchString(strings.TrimSpace(p.StartTime)) {
			return Candidate{}, false
		}
		start, _ = time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(p.StartDate)+" "+strings.TrimSpace(p.StartTime), loc)
		c.StartLocal = localDateTime(start)
		if reHHMM.MatchString(strings.TrimSpace(p.EndTime)) {
			endDate := firstNonEmpty(strings.TrimSpace(p.EndDate), strings.TrimSpace(p.StartDate))
			if end, err := time.ParseInLocation("2006-01-02 15:04", endDate+" "+strings.TrimSpace(p.EndTime), endLoc); err == nil {
				if end.Before(start) && strings.TrimSpace(p.EndDate) == "" {
					end = end.AddDate(0, 0, 1) // overnight
				}
				if !end.Before(start) {
					c.EndLocal = localDateTime(end.In(loc))
				}
			}
		}
	}
	// Timed events must start after now; all-day ones may be today.
	today := now.In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	if (!allDay && !start.After(now)) || (allDay && start.Before(today)) || start.After(now.AddDate(3, 0, 0)) {
		return Candidate{}, false
	}

	switch {
	case c.Kind == "flight" && reFlightNumber.MatchString(flightNo):
		c.Key = "flight/" + flightNo + "/" + localDate(start)
		c.Details["flight"] = flightNo
		_, knownFrom := airports[from]
		_, knownTo := airports[to]
		if knownFrom && knownTo {
			c.Title = flightTitle(flightNo, from, to)
			c.Details["from"], c.Details["to"] = from, to
			if c.Location == "" {
				c.Location = airports[from].City + " (" + from + ")"
			}
		}
	case c.Kind == "stay":
		c.Key = "stay/" + localDate(start) + "/" + strings.ToLower(title)
	default:
		c.Key = c.Kind + "/" + senderDomain(in.From) + "/" + c.StartLocal
	}
	return c, true
}
