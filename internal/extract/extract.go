// Package extract finds calendar-worthy dates in email: structured data
// (schema.org JSON-LD, iCalendar parts), known booking layouts (Sabre/Virgin
// e-tickets, columnar flight tables, hotel check-in/check-out blocks) and
// conservative generic rules (expiries, deadlines, promotions, pick-ups).
// It is pure: results are suggestions for the caller to store and the user to
// confirm.
package extract

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Version identifies the extractor behaviour. Bumping it re-scans messages.
const Version = 2

// HomeZone is the default zone for dates without a better-known place.
const HomeZone = "Australia/Sydney"

// Input is one message to scan.
type Input struct {
	Subject  string
	From     string
	Date     time.Time // when the message was sent; anchors yearless and relative dates
	Text     string
	HTML     string
	Calendar []string // text/calendar parts
}

// Candidate is a proposed calendar event. Times are local wall clock in TZ:
// all-day values are YYYY-MM-DD (end inclusive), timed values
// YYYY-MM-DDTHH:MM.
type Candidate struct {
	Extractor  string
	Kind       string
	Title      string
	AllDay     bool
	StartLocal string
	EndLocal   string
	TZ         string
	Location   string
	Notes      string
	Key        string
	Evidence   string
	Details    map[string]string
}

// Start returns the candidate's start instant.
func (c Candidate) Start() time.Time {
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		loc = time.UTC
	}
	layout := "2006-01-02T15:04"
	if c.AllDay {
		layout = "2006-01-02"
	}
	t, _ := time.ParseInLocation(layout, c.StartLocal, loc)
	return t
}

// Extract returns the message's candidates that start after now, without
// duplicate keys. Structured and layout extractors take precedence over the
// generic rules.
func Extract(in Input, now time.Time) []Candidate {
	d := parseDoc(in.HTML, in.Text)
	var all []Candidate
	all = append(all, fromLDJSON(d.LDJSON, in)...)
	all = append(all, fromICS(in.Calendar, in)...)
	all = append(all, fromSabre(d, in)...)
	all = append(all, fromFlightTables(d, in)...)
	all = append(all, fromStayBlock(d, in)...)
	all = append(all, fromRules(d, in)...)

	seen := map[string]bool{}
	specificDays := map[string]bool{} // dates covered by structured/layout results
	var out []Candidate
	for _, c := range all {
		if c.Key == "" || seen[c.Key] {
			continue
		}
		if c.Extractor == "rules" && c.Kind == "event" && specificDays[c.StartLocal[:10]] {
			continue // a precise booking already covers this day
		}
		if !c.Start().After(now) {
			continue
		}
		seen[c.Key] = true
		if c.Extractor != "rules" {
			specificDays[c.StartLocal[:10]] = true
			if c.EndLocal != "" {
				specificDays[c.EndLocal[:10]] = true
			}
		}
		out = append(out, c)
	}
	return out
}

// senderName returns the From display name, or the address's domain.
func senderName(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if n := strings.TrimSpace(a.Name); n != "" {
			return n
		}
		if _, dom, ok := strings.Cut(a.Address, "@"); ok {
			return dom
		}
	}
	return strings.TrimSpace(from)
}

// senderDomain returns the From address's domain (lower case), or "".
func senderDomain(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if _, dom, ok := strings.Cut(strings.ToLower(a.Address), "@"); ok {
			return dom
		}
	}
	return ""
}

var reBookingRef = regexp.MustCompile(`(?:[Bb]ooking|BOOKING|[Rr]eservation|PNR)(?:\s+(?:[Rr]ef(?:erence)?|[Cc]ode|[Nn]umber))?\s*(?:#|:|ref#)?\s*\b([A-Z0-9]{6})\b`)

// bookingRef finds a six-character booking reference with at least one
// letter and one digit or a run of capitals ("PSFU4K", "DCRBSP").
func bookingRef(d *doc, subject string) string {
	for _, l := range append([]string{subject}, d.Lines...) {
		for _, m := range reBookingRef.FindAllStringSubmatch(l, -1) {
			if strings.ContainsAny(m[1], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
				return m[1]
			}
		}
	}
	return ""
}

func localDate(t time.Time) string     { return t.Format("2006-01-02") }
func localDateTime(t time.Time) string { return t.Format("2006-01-02T15:04") }

func loadZone(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	loc, _ := time.LoadLocation(HomeZone)
	return loc
}

// truncate shortens s to at most n runes, preferring a word boundary.
func truncate(s string, n int) string {
	r := []rune(normalize(s))
	if len(r) <= n {
		return string(r)
	}
	cut := n
	for i := n; i > n/2; i-- {
		if r[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " ,;:-") + "…"
}

func flightTitle(code, from, to string) string {
	return fmt.Sprintf("%s %s → %s", code, from, to)
}
