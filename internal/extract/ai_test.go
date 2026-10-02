package extract

import (
	"strings"
	"testing"
)

func TestFromProposalsChecksEvidenceAndDates(t *testing.T) {
	sent := at("2026-06-05T00:00:00Z")
	now := at("2026-10-01T00:00:00Z")
	body := "Your tickets\nEvent | Date\nThe Example Show | Sat 14 Nov 2026, 7:30pm\nDoors open 6:45pm\nQuote expires in 14 days."
	in := Input{Subject: "Ticket confirmation", From: "Tickets <no-reply@tickets.example>", Date: sent}
	proposals := []ProposedEvent{
		// Valid: evidence is a (case/whitespace-insensitive) quote with the date.
		{Kind: "event", Title: "The Example Show", StartDate: "2026-11-14", StartTime: "19:30", EndTime: "22:00",
			TimeZone: "Australia/Brisbane", Evidence: "the example show  sat 14 nov 2026, 7:30pm"},
		// Hallucinated: quote not in the email.
		{Kind: "event", Title: "Bonus show", StartDate: "2026-11-15", AllDay: true, Evidence: "Bonus show Sun 15 Nov"},
		// Evidence does not state this date (30 vs 3).
		{Kind: "deadline", Title: "Wrong date", StartDate: "2026-11-30", AllDay: true, Evidence: "Doors open 6:45pm"},
		// In the past relative to now.
		{Kind: "deadline", Title: "Old", StartDate: "2026-06-19", AllDay: true, Evidence: "Quote expires in 14 days."},
		// Duplicate of the first.
		{Kind: "event", Title: "The Example Show", StartDate: "2026-11-14", StartTime: "19:30", TimeZone: "Australia/Brisbane",
			Evidence: "Sat 14 Nov 2026, 7:30pm"},
		// Unknown kind falls back to event; bad time drops.
		{Kind: "party", Title: "Bad time", StartDate: "2026-11-14", StartTime: "7:30pm", Evidence: "Sat 14 Nov 2026, 7:30pm"},
	}
	got, dropped := FromProposals(proposals, in, body, now, "ai:test")
	if len(got) != 1 || dropped != 5 {
		t.Fatalf("kept %d dropped %d: %+v", len(got), dropped, got)
	}
	c := got[0]
	if c.StartLocal != "2026-11-14T19:30" || c.EndLocal != "2026-11-14T22:00" || c.TZ != "Australia/Brisbane" ||
		c.Key != "event/tickets.example/2026-11-14T19:30" || c.Extractor != "ai:test" {
		t.Fatalf("candidate = %+v", c)
	}
}

func TestFromProposalsFlightsAndZones(t *testing.T) {
	sent := at("2026-06-05T00:00:00Z")
	now := at("2026-10-01T00:00:00Z")
	body := "22Dec26 | JQ 123 | Adelaide | 09:15 | Sydney | 11:45\nCheck-in Wed, 16 Dec · Check-out Tue, 22 Dec"
	in := Input{From: "x@airline.example", Date: sent}
	got, dropped := FromProposals([]ProposedEvent{
		{Kind: "flight", Title: "JQ123 ADL → SYD", StartDate: "2026-12-22", StartTime: "09:15", EndTime: "11:45",
			TimeZone: "Australia/Adelaide", EndTimeZone: "Australia/Sydney", FlightNumber: "jq 123", Evidence: "22Dec26 | JQ 123 | Adelaide | 09:15"},
		{Kind: "stay", Title: "Hotel", AllDay: true, StartDate: "2026-12-16", EndDate: "2026-12-22", TimeZone: "Australia/Adelaide",
			Evidence: "Check-in Wed, 16 Dec"},
		{Kind: "event", Title: "Overnight", StartDate: "2026-12-22", StartTime: "23:00", EndTime: "01:00", Evidence: "22Dec26"},
	}, in, body, now, "ai:test")
	if dropped != 0 || len(got) != 3 {
		t.Fatalf("dropped %d: %+v", dropped, got)
	}
	// Arrival 11:45 Sydney is 11:15 in Adelaide.
	if got[0].Key != "flight/JQ123/2026-12-22" || got[0].EndLocal != "2026-12-22T11:15" || got[0].Details["flight"] != "JQ123" {
		t.Fatalf("flight = %+v", got[0])
	}
	if got[1].Key != "stay/2026-12-16/hotel" || !got[1].AllDay || got[1].EndLocal != "2026-12-22" {
		t.Fatalf("stay = %+v", got[1])
	}
	if got[2].EndLocal != "2026-12-23T01:00" || got[2].TZ != HomeZone {
		t.Fatalf("overnight = %+v", got[2])
	}
}

func TestCompactText(t *testing.T) {
	html := `<p>Hi <a href="https://example.com/x?y=1">here</a> https://track.example/abc</p><p>www.example.com</p>` +
		`<table><tr><td>Date</td><td>Sat 14 Nov</td></tr></table><img src="x.png">`
	got := CompactText(html, "")
	if strings.Contains(got, "http") || strings.Contains(got, "www.") || !strings.Contains(got, "Date | Sat 14 Nov") {
		t.Fatalf("compact = %q", got)
	}
	long := CompactText("", strings.Repeat("a long line of text\n", 5000))
	if len([]rune(long)) > MaxCompactChars {
		t.Fatalf("compact text not capped: %d", len(long))
	}
}

func TestQuotedNearby(t *testing.T) {
	hay := foldForMatch("BNK | BALLINA, AUSTRALIA\nWed, Dec 16\n⋅ 1h 25min\nVA | 1140\n12:45 PM, Dec 16 | 2:10 PM, Dec 16\nCheck-in\nCheck-out\nWed, 16 Dec\nTue, 22 Dec")
	for _, ok := range []string{"Wed, Dec 16 12:45 PM, Dec 16", "Check-out\nTue, 22 Dec", "12:45 pm, dec 16"} {
		if !quotedNearby(hay, foldForMatch(ok)) {
			t.Fatalf("%q should match", ok)
		}
	}
	for _, bad := range []string{"Thu, Dec 17 9:00 AM", "Bonus show Dec 16", "Check-out Wed, 23 Dec"} {
		if quotedNearby(hay, foldForMatch(bad)) {
			t.Fatalf("%q should not match", bad)
		}
	}
}

func TestCompactTextUnstacksTables(t *testing.T) {
	got := CompactText(flightTableHTML, "")
	for _, want := range []string{"22Dec26 | JQ 123 | Adelaide | 09:15 | Sydney | 11:45", "22Dec26 | JQ 456 | Sydney | 14:40 | Ballina Byron | 15:55"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact text missing %q:\n%s", want, got)
		}
	}
	// Element ids inside an unstacked row are still recorded (Sabre relies on them).
	d := parseDoc(`<table><tr><td id="a">1<br>2</td><td>x<br>y</td><td>p<br>q</td></tr></table>`, "")
	if d.ByID["a"] != "1 2" {
		t.Fatalf("ids lost: %v", d.ByID)
	}
}

func TestFromProposalsCorrectsModelSlips(t *testing.T) {
	sent := at("2026-10-01T03:00:00Z") // Thu 1 Oct, Sydney
	now := at("2026-10-02T00:00:00Z")
	body := "Heads up, your quote expires in 14 days.\nVA | 1140\nBNK BALLINA → SYD SYDNEY\n12:45 PM, Dec 16 | 2:10 PM, Dec 16\nCheck-in Wed, 16 Dec 1:00 pm\nCheck-out Tue, 22 Dec 10:00 am"
	in := Input{From: "x@shop.example", Date: sent}
	got, dropped := FromProposals([]ProposedEvent{
		// The model returned the email date for a relative deadline.
		{Kind: "expiry", Title: "Quote expires", StartDate: "2026-10-01", Evidence: "your quote expires in 14 days"},
		// Wrong zone (Ballina guessed as Brisbane), wrong kind, mangled arrow.
		{Kind: "deadline", Title: "VA1140 Ballina \x12 Sydney", StartDate: "2026-12-16", StartTime: "12:45", EndTime: "14:10",
			TimeZone: "Australia/Brisbane", FlightNumber: "VA 1140", FromAirport: "bnk", ToAirport: "SYD", Evidence: "12:45 PM, Dec 16"},
		// A timed stay becomes an all-day span with times in the notes.
		{Kind: "stay", Title: "Hotel", StartDate: "2026-12-16", StartTime: "13:00", EndDate: "2026-12-22", EndTime: "10:00",
			TimeZone: "Australia/Adelaide", Notes: "Booking 1", Evidence: "Check-in Wed, 16 Dec 1:00 pm"},
	}, in, body, now, "ai:test")
	if dropped != 0 || len(got) != 3 {
		t.Fatalf("dropped %d: %+v", dropped, got)
	}
	if got[0].StartLocal != "2026-10-15" || !got[0].AllDay || got[0].EndLocal != "" {
		t.Fatalf("relative date = %+v", got[0])
	}
	f := got[1]
	if f.Kind != "flight" || f.Title != "VA1140 BNK → SYD" || f.TZ != "Australia/Sydney" || f.StartLocal != "2026-12-16T12:45" ||
		f.EndLocal != "2026-12-16T14:10" || f.Key != "flight/VA1140/2026-12-16" || f.Location != "Ballina (BNK)" {
		t.Fatalf("flight = %+v", f)
	}
	s := got[2]
	if !s.AllDay || s.StartLocal != "2026-12-16" || s.EndLocal != "2026-12-22" || s.Notes != "Check-in from 13:00\nCheck-out by 10:00\nBooking 1" {
		t.Fatalf("stay = %+v", s)
	}
}
