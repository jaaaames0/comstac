package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"comstac/internal/store"
)

func TestCalendarEventLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "cal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ev := &store.CalendarEvent{Title: "Gym", StartLocal: "2026-10-05T07:00", EndLocal: "2026-10-05T08:00", RRule: "FREQ=WEEKLY;BYDAY=MO,WE"}
	id, err := store.SaveCalendarEvent(ctx, db, ev, []int{60, 10})
	if err != nil || id == 0 {
		t.Fatalf("save: %v", err)
	}
	if _, err := db.Exec(`UPDATE calendar_reminders SET created_at = '2026-01-01T00:00:00Z' WHERE offset_minutes = 60`); err != nil {
		t.Fatal(err)
	}

	// Update keeps an unchanged reminder's creation time and drops removed ones.
	ev.Title = "Gym (legs)"
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60, 30}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCalendarEvent(ctx, db, id)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Gym (legs)" || got.Kind != "custom" || got.Source != "manual" || got.Status != store.CalendarConfirmed || got.TZ != "Australia/Sydney" {
		t.Fatalf("defaults/update wrong: %+v", got)
	}
	if len(got.Reminders) != 2 || got.Reminders[0].OffsetMinutes != 60 || got.Reminders[1].OffsetMinutes != 30 {
		t.Fatalf("reminders = %+v", got.Reminders)
	}
	if !got.Reminders[0].CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unchanged reminder lost its creation time: %v", got.Reminders[0].CreatedAt)
	}

	// Skipping dates is idempotent.
	for i := 0; i < 2; i++ {
		if err := store.AddCalendarExDate(ctx, db, id, "2026-10-07"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddCalendarExDate(ctx, db, id, "2026-10-12"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCalendarExDate(ctx, db, id, "nope"); err == nil {
		t.Fatal("invalid exdate accepted")
	}
	got, _ = store.GetCalendarEvent(ctx, db, id)
	if got.ExDates != "2026-10-07,2026-10-12" {
		t.Fatalf("exdates = %q", got.ExDates)
	}

	// Reminder claims are one-shot; deleting the event removes everything.
	occ := time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)
	if ok, _ := store.ClaimCalendarReminder(ctx, db, id, occ, 60); !ok {
		t.Fatal("first claim refused")
	}
	if ok, _ := store.ClaimCalendarReminder(ctx, db, id, occ, 60); ok {
		t.Fatal("second claim accepted")
	}
	if err := store.DeleteCalendarEvent(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"calendar_events", "calendar_reminders", "calendar_reminder_fires"} {
		var n int
		if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s has %d rows after delete (%v)", table, n, err)
		}
	}
}

func TestReplaceRosterShiftsOnlyTouchesRosterRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "roster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	shift := func(date string) store.CalendarEvent {
		return store.CalendarEvent{Title: "Work", Kind: "shift", StartLocal: date + "T11:45", EndLocal: date + "T20:30", DedupeKey: "roster/" + date}
	}
	if _, err := store.ReplaceRosterShifts(ctx, db, "2026-09-28", "2026-10-04",
		[]store.CalendarEvent{shift("2026-09-29"), shift("2026-09-30"), shift("2026-10-03")}, []int{60}); err != nil {
		t.Fatal(err)
	}
	// A manual event in range and a roster shift outside it must survive.
	if _, err := store.SaveCalendarEvent(ctx, db, &store.CalendarEvent{Title: "Dentist", StartLocal: "2026-09-30T09:00"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceRosterShifts(ctx, db, "2026-10-05", "2026-10-11", []store.CalendarEvent{shift("2026-10-06")}, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.CountRosterShifts(ctx, db, "2026-09-28", "2026-10-04"); n != 3 {
		t.Fatalf("roster shifts in first range = %d, want 3", n)
	}
	// Re-importing the first fortnight with a changed roster replaces it.
	removed, err := store.ReplaceRosterShifts(ctx, db, "2026-09-28", "2026-10-04", []store.CalendarEvent{shift("2026-10-01")}, []int{30})
	if err != nil || removed != 3 {
		t.Fatalf("replace removed %d (%v), want 3", removed, err)
	}
	events, _ := store.ListCalendarEvents(ctx, db, store.CalendarConfirmed)
	var titles []string
	for _, e := range events {
		titles = append(titles, e.StartLocal[:10]+" "+e.Title+" "+e.Source)
	}
	want := "[2026-09-30 Dentist manual 2026-10-01 Work roster 2026-10-06 Work roster]"
	if fmt.Sprint(titles) != want {
		t.Fatalf("events = %v\nwant %s", titles, want)
	}
	var orphan int
	_ = db.QueryRow(`SELECT count(*) FROM calendar_reminders WHERE event_id NOT IN (SELECT id FROM calendar_events)`).Scan(&orphan)
	if orphan != 0 {
		t.Fatalf("%d orphaned reminders", orphan)
	}
}
