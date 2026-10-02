package extract

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Fixtures are synthetic: they reproduce the structure of real booking mail
// (Sabre e-tickets, Jetstar JSON-LD and change notices, Expedia stays) with
// invented names, references and flights.

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func summary(cs []Candidate) string {
	var out []string
	for _, c := range cs {
		s := fmt.Sprintf("%s|%s|%s|%s", c.Extractor, c.Kind, c.Title, c.StartLocal)
		if c.EndLocal != "" {
			s += ">" + c.EndLocal
		}
		s += "|" + strings.TrimPrefix(c.TZ, "Australia/")
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

func expect(t *testing.T, got []Candidate, want ...string) {
	t.Helper()
	if summary(got) != strings.Join(want, "\n") {
		t.Fatalf("candidates\n got:\n%s\nwant:\n%s", summary(got), strings.Join(want, "\n"))
	}
}

const sabreHTML = `<html><body><p>Booking reference QWERTY</p>
<table><tr><td id="air-0-segment">
 <strong id="air-0-marketing-airline-code">VA </strong><strong id="air-0-flight-number"> 901 </strong>
 <strong id="air-0-departure-city-code">BNK</strong><span id="air-0-departure-city">BALLINA, AUSTRALIA</span>
 <strong id="air-0-arrival-city-code">SYD</strong><span id="air-0-arrival-city">SYDNEY, AUSTRALIA</span>
 <strong id="air-0-departure-time">12:45 PM</strong>, <span id="air-0-departure-date">Oct 29</span>
 <strong id="air-0-arrival-time">2:10 PM</strong>, <span id="air-0-arrival-date">Oct 29</span>
</td></tr>
<tr><td id="air-1-segment">
 <strong id="air-1-marketing-airline-code">VA</strong><strong id="air-1-flight-number">902</strong>
 <strong id="air-1-departure-city-code">SYD</strong><strong id="air-1-arrival-city-code">ADL</strong>
 <strong id="air-1-departure-time">4:30 PM</strong>, <span id="air-1-departure-date">Oct 29</span>
 <strong id="air-1-arrival-time">6:40 PM</strong>, <span id="air-1-arrival-date">Oct 29</span>
</td></tr></table></body></html>`

func TestSabreETicket(t *testing.T) {
	got := Extract(Input{Subject: "e-Ticket", From: "Airline <no-reply@example.com>", Date: at("2026-09-10T08:00:00Z"), HTML: sabreHTML}, at("2026-09-10T08:00:00Z"))
	expect(t, got,
		"sabre|flight|VA901 BNK → SYD|2026-10-29T12:45>2026-10-29T14:10|Sydney",
		// Arrival 18:40 Adelaide is 19:10 Sydney time.
		"sabre|flight|VA902 SYD → ADL|2026-10-29T16:30>2026-10-29T19:10|Sydney")
	if got[0].Details["booking"] != "QWERTY" || !strings.Contains(got[1].Notes, "Arrives ADL 18:40 local time") {
		t.Fatalf("details/notes: %+v", got)
	}
}

const ldFlightHTML = `<html><head><script type="application/ld+json">[
 {"@context":"http://schema.org","@type":"FlightReservation","reservationNumber":"ZX9Y8W",
  "reservationFor":{"@type":"Flight","flightNumber":"123","airline":{"@type":"Airline","iataCode":"JQ"},
   "departureAirport":{"@type":"Airport","iataCode":"ADL","name":"Adelaide"},
   "arrivalAirport":{"@type":"Airport","iataCode":"SYD","name":"Sydney (Kingsford Smith)"},
   "departureTime":"2026-12-22T09:25:00+10:00","arrivalTime":"2026-12-22T11:45:00+11:00"}},
 {"@context":"http://schema.org","@type":"LodgingReservation","reservationNumber":"H1",
  "reservationFor":{"@type":"LodgingBusiness","name":"Test Hotel","address":{"streetAddress":"1 Test St","addressLocality":"Perth","addressRegion":"WA","postalCode":"6000"}},
  "checkinTime":"2026-12-10T15:00:00+08:00","checkoutTime":"2026-12-12T10:00:00+08:00"}
]</script></head><body>Your booking</body></html>`

func TestJSONLDUsesAirportZoneNotEmbeddedOffset(t *testing.T) {
	got := Extract(Input{From: "x@example.com", Date: at("2026-06-05T00:00:00Z"), HTML: ldFlightHTML}, at("2026-06-05T00:00:00Z"))
	// 09:25 is Adelaide local time despite the wrong +10:00 offset; arrival
	// 11:45 Sydney is 11:15 in Adelaide.
	expect(t, got,
		"jsonld|flight|JQ123 ADL → SYD|2026-12-22T09:25>2026-12-22T11:15|Adelaide",
		"jsonld|stay|Test Hotel|2026-12-10>2026-12-12|Perth")
	if got[0].Key != "flight/JQ123/2026-12-22" || !strings.Contains(got[1].Notes, "Check-in from 15:00") {
		t.Fatalf("key/notes: %+v", got)
	}
}

const flightTableHTML = `<p>your flight/s on booking ABC123 have changed.</p>
<table><tr><td><strong>Date</strong></td><td><strong>Flight</strong></td><td><strong>From</strong></td>
<td><strong>Depart</strong></td><td><strong>To</strong></td><td><strong>Arrive</strong></td></tr>
<tr><td> 22Dec26 <br> 22Dec26 <br></td><td>JQ 123 <br> JQ 456 <br></td><td>Adelaide <br> Sydney <br></td>
<td>09:15 <br> 14:40 <br></td><td>Sydney <br> Ballina Byron <br></td><td>11:45 <br> 15:55 <br></td></tr></table>`

func TestStackedFlightTable(t *testing.T) {
	got := Extract(Input{From: "x@example.com", Date: at("2026-07-29T00:00:00Z"), HTML: flightTableHTML}, at("2026-07-29T00:00:00Z"))
	expect(t, got,
		"flight-table|flight|JQ123 ADL → SYD|2026-12-22T09:15>2026-12-22T11:15|Adelaide",
		"flight-table|flight|JQ456 SYD → BNK|2026-12-22T14:40>2026-12-22T15:55|Sydney")
	if got[0].Details["booking"] != "ABC123" {
		t.Fatalf("booking ref: %+v", got[0].Details)
	}
}

const stayHTML = `<div>All set. Your hotel is confirmed.</div><div>Sample Hotel</div><div>Expedia itinerary: 12345678901234</div>
<div>12 Sample St, Adelaide, SA, 5000 Australia</div>
<table><tr><td>Check-in</td><td>Check-out</td></tr><tr><td>Wed, 16 Dec</td><td>Tue, 22 Dec</td></tr>
<tr><td>Check-in time starts at 1:00 pm</td><td>10:00 am</td></tr></table>
<p>Free cancellation until 13 Dec 2026 at 6:00 pm (property local time)</p>`

func TestStayBlockAndCancellationDeadline(t *testing.T) {
	got := Extract(Input{Subject: "Travel confirmation", From: "Travel <mail@travel.example>", Date: at("2026-06-05T15:00:00Z"), HTML: stayHTML}, at("2026-06-05T15:00:00Z"))
	expect(t, got,
		"stay-block|stay|Sample Hotel|2026-12-16>2026-12-22|Adelaide",
		"rules|deadline|Free cancellation until 13 Dec 2026 at 6:00 pm (property local time)|2026-12-13T18:00|Sydney")
	if got[0].Notes != "Check-in from 13:00\nCheck-out by 10:00\nBooking 12345678901234" || got[0].Location != "12 Sample St, Adelaide, SA, 5000 Australia" {
		t.Fatalf("stay notes/location: %q / %q", got[0].Notes, got[0].Location)
	}
}

func TestGenericRules(t *testing.T) {
	sent := at("2026-10-01T03:00:00Z") // 1 Oct, Sydney
	cases := []struct {
		name, subject, text string
		want                []string
	}{
		{"removal deadline", "Your bank connections will be removed on 3 Oct 2026",
			"Your trial has ended. Your bank connections are scheduled to be removed on 3 Oct 2026.",
			[]string{"rules|deadline|Your bank connections will be removed on 3 Oct 2026|2026-10-03|Sydney"}},
		{"relative expiry", "You've got a personalised quote ➡️",
			"Heads up, your quote expires in 14 days, so don't forget to complete your application before then.",
			[]string{"rules|expiry|Heads up, your quote expires in 14 days, so don't forget to complete your…|2026-10-15|Sydney"}},
		{"promo with time", "Sale on now", "Explore the whole Autumn Sale, now through 9 Oct 4:00am AEDT.",
			[]string{"rules|promo|Explore the whole Autumn Sale, now through 9 Oct 4:00am AEDT.|2026-10-09T04:00|Sydney"}},
		{"label/value pick-up", "Vehicle Booking Confirmation",
			"Pickup Date Time: | Mon 12/Oct/2026 12:00\nReturn Date Time: | Tue 13/Oct/2026 12:00",
			[]string{"rules|event|Pickup Date Time · Rentals|2026-10-12T12:00|Sydney", "rules|event|Return Date Time · Rentals|2026-10-13T12:00|Sydney"}},
		{"past dates ignored", "Card expiring", "Your card is set to expire on 30 September 2026.", nil},
		{"due to is not a due date", "Notice", "Due to a change at our partner, from 1 November 2026 accounts move.", nil},
		{"sports preview is not an event", "Round 25", "The team will then travel to face the Jets, returning home on 29 Oct.", nil},
		{"bare 'close' is not a deadline", "News", "The big tournament on Oct 18 is getting close now.", nil},
		{"weekday typo keeps nearest date", "Last chance", "Use code X for 10% off through Friday, October 8th!",
			[]string{"rules|promo|Use code X for 10% off through Friday, October 8th!|2026-10-08|Sydney"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract(Input{Subject: c.subject, From: "Rentals <bookings@rentals.example>", Date: sent, Text: c.text}, sent)
			expect(t, got, c.want...)
		})
	}
}

func TestICSAndPrecedence(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:abc@example\r\nSUMMARY:Dentist\\, check-up\r\nLOCATION:1 Test St\r\n" +
		"DTSTART;TZID=Australia/Brisbane:20261020T093000\r\nDTEND;TZID=Australia/Brisbane:20261020T101500\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:allday@example\r\nSUMMARY:Holiday\r\nDTSTART;VALUE=DATE:20261102\r\nDTEND;VALUE=DATE:20261105\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	sent := at("2026-10-01T00:00:00Z")
	got := Extract(Input{From: "a@example.com", Date: sent, Calendar: []string{ics},
		Text: "Your appointment is booked for 20 Oct 2026 at 9:30 am."}, sent)
	// The rules' "appointment" event on the same day is dropped in favour of
	// the precise invitation.
	expect(t, got,
		"ics|event|Dentist, check-up|2026-10-20T09:30>2026-10-20T10:15|Brisbane",
		"ics|event|Holiday|2026-11-02>2026-11-04|Sydney")
	cancelled := strings.Replace(ics, "METHOD:REQUEST", "METHOD:CANCEL", 1)
	if got := Extract(Input{Date: sent, Calendar: []string{cancelled}}, sent); len(got) != 0 {
		t.Fatalf("cancellation produced %v", got)
	}
}

func TestDateForms(t *testing.T) {
	anchor := at("2026-06-05T00:00:00Z")
	for in, want := range map[string]string{
		"3 Oct 2026": "2026-10-03", "30 September 2026": "2026-09-30", "22Dec26": "2026-12-22",
		"Thu, Oct 29": "2026-10-29", "October 29, 2026": "2026-10-29", "Mon 10/Aug/2026": "2026-08-10",
		"22/12/2026": "2026-12-22", "Wed, 16 Dec": "2026-12-16", "Sat 02 May 2026": "2026-05-02",
		"26th April": "2027-04-26", // yearless and more than a week before the anchor: next year
		"2026-11-04": "2026-11-04",
	} {
		ds := findDates(in)
		if len(ds) != 1 {
			t.Fatalf("%q: %d matches", in, len(ds))
		}
		d, ok := ds[0].resolve(anchor, loadZone(HomeZone))
		if !ok || d.Format("2006-01-02") != want {
			t.Fatalf("%q -> %v %v, want %s", in, d, ok, want)
		}
	}
	ds := findDates("Pick-up time: | Sat 02 May 2026 between 06:40 (06:40 AM) and 07:00")
	if len(ds) != 1 || !ds[0].HasTime || ds[0].Hour != 6 || ds[0].Min != 40 {
		t.Fatalf("time after date: %+v", ds)
	}
	for _, s := range []string{"12:45 PM", "1:00 pm", "8am", "09:15", "12 AM"} {
		if _, _, ok := parseClock(s); !ok {
			t.Fatalf("parseClock(%q) failed", s)
		}
	}
}
