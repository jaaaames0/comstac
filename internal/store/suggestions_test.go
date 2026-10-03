package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"comstac/internal/store"
)

func openSuggestionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "sugg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func flightSuggestion(start, end string) store.CalendarEvent {
	return store.CalendarEvent{Title: "JQ123 ADL → SYD", Kind: "flight", TZ: "Australia/Adelaide",
		StartLocal: start, EndLocal: end, DedupeKey: "flight/JQ123/2026-12-22", Details: `{"extractor":"jsonld"}`}
}

func messageEvents(t *testing.T, db *sql.DB, msg int64) string {
	t.Helper()
	evs, err := store.ListMessageCalendarEvents(context.Background(), db, msg)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, fmt.Sprintf("%s %s updates=%d", e.Status, e.StartLocal, e.SuggestionDetails().Updates))
	}
	return fmt.Sprint(out)
}

func TestSuggestionLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openSuggestionDB(t)

	// Itinerary (message 10) suggests 09:25; a duplicate e-mail (11) is suppressed.
	if n, err := store.SaveExtraction(ctx, db, 10, 1, []store.CalendarEvent{flightSuggestion("2026-12-22T09:25", "2026-12-22T11:15")}); err != nil || n != 1 {
		t.Fatalf("save 10: %d %v", n, err)
	}
	if n, _ := store.SaveExtraction(ctx, db, 11, 1, []store.CalendarEvent{flightSuggestion("2026-12-22T09:25", "2026-12-22T11:15")}); n != 0 {
		t.Fatalf("duplicate suggestion created %d", n)
	}
	// Re-scanning message 10 replaces rather than duplicates its suggestions.
	if n, _ := store.SaveExtraction(ctx, db, 10, 2, []store.CalendarEvent{flightSuggestion("2026-12-22T09:25", "2026-12-22T11:15")}); n != 1 {
		t.Fatalf("re-scan created %d", n)
	}
	evs, _ := store.ListMessageCalendarEvents(ctx, db, 10)
	if len(evs) != 1 || evs[0].Source != "email" || evs[0].MessageID != 10 || evs[0].SuggestionDetails().Extractor != "jsonld" {
		t.Fatalf("suggestion = %+v", evs)
	}

	// Accept with reminders: it becomes a confirmed event.
	id, err := store.AcceptSuggestion(ctx, db, evs[0].ID, []int{1440, 180})
	if err != nil || id != evs[0].ID {
		t.Fatalf("accept: %d %v", id, err)
	}
	if _, err := store.AcceptSuggestion(ctx, db, id, nil); err != store.ErrNotSuggestion {
		t.Fatalf("second accept: %v", err)
	}
	// The user renames it.
	confirmed, _ := store.GetCalendarEvent(ctx, db, id)
	confirmed.Title = "Flight home"
	if _, err := store.SaveCalendarEvent(ctx, db, confirmed, []int{1440, 180}); err != nil {
		t.Fatal(err)
	}

	// A change notice (message 20) with a new time becomes an update suggestion.
	if n, _ := store.SaveExtraction(ctx, db, 20, 1, []store.CalendarEvent{flightSuggestion("2026-12-22T09:15", "2026-12-22T11:15")}); n != 1 {
		t.Fatalf("update suggestion not created")
	}
	if got := messageEvents(t, db, 20); got != fmt.Sprintf("[suggested 2026-12-22T09:15 updates=%d]", id) {
		t.Fatalf("message 20 = %s", got)
	}
	evs, _ = store.ListMessageCalendarEvents(ctx, db, 20)
	if got, err := store.AcceptSuggestion(ctx, db, evs[0].ID, nil); err != nil || got != id {
		t.Fatalf("accept update: %d %v", got, err)
	}
	updated, _ := store.GetCalendarEvent(ctx, db, id)
	if updated.StartLocal != "2026-12-22T09:15" || updated.Title != "Flight home" || len(updated.Reminders) != 2 {
		t.Fatalf("update must change times only: %+v", updated)
	}
	if got := messageEvents(t, db, 20); got != "[confirmed 2026-12-22T09:15 updates=0]" {
		t.Fatalf("update suggestion should be gone, leaving the event it names: %s", got)
	}
	// The original itinerary scanned again now matches the confirmed event? No:
	// it still says 09:25, so it becomes an update suggestion (the user decides).
	if n, _ := store.SaveExtraction(ctx, db, 10, 3, []store.CalendarEvent{flightSuggestion("2026-12-22T09:15", "2026-12-22T11:15")}); n != 0 {
		t.Fatal("identical to confirmed must be suppressed")
	}

	// Dismissed suggestions stay hidden for identical times.
	other := store.CalendarEvent{Title: "Sale ends", Kind: "promo", AllDay: true, StartLocal: "2026-10-09", DedupeKey: "promo/shop.example/2026-10-09"}
	store.SaveExtraction(ctx, db, 30, 1, []store.CalendarEvent{other})
	evs, _ = store.ListMessageCalendarEvents(ctx, db, 30)
	if err := store.DismissSuggestion(ctx, db, evs[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.SaveExtraction(ctx, db, 31, 1, []store.CalendarEvent{other}); n != 0 {
		t.Fatal("dismissed suggestion came back")
	}
	if got := messageEvents(t, db, 30); got != "[]" {
		t.Fatalf("dismissed suggestion still listed: %s", got)
	}
}

func TestNewerMessageSupersedesOlderSuggestion(t *testing.T) {
	ctx := context.Background()
	db := openSuggestionDB(t)
	store.SaveExtraction(ctx, db, 10, 1, []store.CalendarEvent{flightSuggestion("2026-12-22T09:25", "")})
	store.SaveExtraction(ctx, db, 20, 1, []store.CalendarEvent{flightSuggestion("2026-12-22T09:15", "")})
	// The older message still shows the flight it names, in the newer times.
	if a, b := messageEvents(t, db, 10), messageEvents(t, db, 20); a != "[suggested 2026-12-22T09:15 updates=0]" || b != a {
		t.Fatalf("older should be superseded: 10=%s 20=%s", a, b)
	}
	// An older message scanned later does not override the newer suggestion.
	store.SaveExtraction(ctx, db, 5, 1, []store.CalendarEvent{flightSuggestion("2026-12-22T09:00", "")})
	if a, b := messageEvents(t, db, 5), messageEvents(t, db, 20); a != "[suggested 2026-12-22T09:15 updates=0]" || b != a {
		t.Fatalf("older scan won: 5=%s 20=%s", a, b)
	}
}

func TestMessagesShareEventsTheyBothName(t *testing.T) {
	ctx := context.Background()
	db := openSuggestionDB(t)
	// An itinerary (30) suggests the flight; the booking confirmation (31)
	// names the same flight via AI and creates nothing new, but shows it.
	store.SaveExtraction(ctx, db, 30, 2, []store.CalendarEvent{flightSuggestion("2026-12-22T09:15", "")})
	ai := flightSuggestion("2026-12-22T09:15", "")
	ai.Details = `{"extractor":"ai:test"}`
	if n, err := store.SaveAISuggestions(ctx, db, 31, []store.CalendarEvent{ai}); err != nil || n != 0 {
		t.Fatalf("created %d, %v", n, err)
	}
	if got := messageEvents(t, db, 31); got != "[suggested 2026-12-22T09:15 updates=0]" {
		t.Fatalf("31 = %s", got)
	}
	evs, _ := store.ListMessageCalendarEvents(ctx, db, 31)
	id := evs[0].ID
	if !store.MessageNamesEvent(ctx, db, 31, &evs[0]) || store.MessageNamesEvent(ctx, db, 32, &evs[0]) {
		t.Fatal("MessageNamesEvent")
	}

	// Dismissed on its own message, it stays hidden there but is offered
	// again on the other message naming it, and can be added from there.
	if err := store.DismissSuggestion(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	if a, b := messageEvents(t, db, 30), messageEvents(t, db, 31); a != "[]" || b != "[dismissed 2026-12-22T09:15 updates=0]" {
		t.Fatalf("after dismiss: 30=%s 31=%s", a, b)
	}
	if _, err := store.AcceptSuggestion(ctx, db, id, nil); err != nil {
		t.Fatal(err)
	}
	if a, b := messageEvents(t, db, 30), messageEvents(t, db, 31); a != "[confirmed 2026-12-22T09:15 updates=0]" || b != a {
		t.Fatalf("after accept: 30=%s 31=%s", a, b)
	}

	// A rule rescan of 31 keeps its AI refs; a new AI run replaces them.
	store.SaveExtraction(ctx, db, 31, 2, nil)
	if got := messageEvents(t, db, 31); got != "[confirmed 2026-12-22T09:15 updates=0]" {
		t.Fatalf("rule rescan dropped AI refs: %s", got)
	}
	store.SaveAISuggestions(ctx, db, 31, nil)
	if got := messageEvents(t, db, 31); got != "[]" {
		t.Fatalf("AI rerun kept stale refs: %s", got)
	}
}
