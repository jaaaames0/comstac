package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"comstac/internal/store"
)

func TestEventRemindersFireOnceWithinGrace(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	rec := &recordingNotifier{}
	s := New(db, time.Second, rec, sydney)

	// Weekly shift Tue 11:45-20:30 with 60 and 15 minute reminders. The
	// reminders were created well before the occurrences.
	ev := &store.CalendarEvent{Title: "Work", Location: "Store 072", Kind: "shift",
		StartLocal: "2026-09-29T11:45", EndLocal: "2026-09-29T20:30", RRule: "FREQ=WEEKLY"}
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60, 15}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE calendar_reminders SET created_at = '2026-09-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}

	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, sydney)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	run := func(now string) {
		t.Helper()
		if err := s.sendEventReminders(ctx, at(now)); err != nil {
			t.Fatal(err)
		}
	}

	run("2026-10-06 10:40") // before the 60-minute reminder for 6 Oct
	if len(rec.events) != 0 {
		t.Fatalf("fired early: %v", rec.events)
	}
	run("2026-10-06 10:45")
	run("2026-10-06 10:50") // a later tick must not resend
	if len(rec.events) != 1 {
		t.Fatalf("want one reminder, got %v", rec.events)
	}
	want := "Reminder: Work|Tue 6 Oct · 11:45–20:30 · Store 072|/?calendar=2026-10-06|event-"
	if !strings.HasPrefix(rec.events[0], want) {
		t.Fatalf("reminder = %q, want prefix %q", rec.events[0], want)
	}
	run("2026-10-06 11:31")
	if len(rec.events) != 2 {
		t.Fatalf("15-minute reminder missing: %v", rec.events)
	}

	// Restart 5 hours late: the 13 Oct reminders are still within grace and
	// sent once; the 29 Sep and earlier ones are long past grace.
	run("2026-10-13 16:00")
	if len(rec.events) != 4 {
		t.Fatalf("catch-up within grace: %v", rec.events)
	}
	run("2026-10-14 09:00")
	if len(rec.events) != 4 {
		t.Fatalf("nothing should be due: %v", rec.events)
	}

	// Skipping a date suppresses its reminders.
	if err := store.AddCalendarExDate(ctx, db, ev.ID, "2026-10-20"); err != nil {
		t.Fatal(err)
	}
	run("2026-10-20 11:40")
	if len(rec.events) != 4 {
		t.Fatalf("skipped occurrence reminded: %v", rec.events)
	}
}

func TestNewReminderDoesNotFireRetroactively(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	rec := &recordingNotifier{}
	s := New(db, time.Second, rec, sydney)
	now := time.Now()
	// Starts in 10 minutes with a 60-minute reminder created now: its reminder
	// time is already 50 minutes past, so it must not fire.
	start := now.Add(10 * time.Minute).In(sydney)
	ev := &store.CalendarEvent{Title: "Soon", StartLocal: start.Format("2006-01-02T15:04")}
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60, 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendEventReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("retroactive reminder sent: %v", rec.events)
	}
	if err := s.sendEventReminders(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("5-minute reminder missing: %v", rec.events)
	}
}

func TestAllDayReminderUsesLocalMidnightBase(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	rec := &recordingNotifier{}
	s := New(db, time.Second, rec, sydney)
	// Redbark removal on 3 Oct; remind 9am the day before (15 h before midnight).
	ev := &store.CalendarEvent{Title: "Redbark connections removed", Kind: "deadline", AllDay: true, StartLocal: "2026-10-03"}
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{900}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE calendar_reminders SET created_at = '2026-09-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if err := s.sendEventReminders(ctx, time.Date(2026, 10, 2, 8, 59, 0, 0, sydney)); err != nil {
		t.Fatal(err)
	}
	if err := s.sendEventReminders(ctx, time.Date(2026, 10, 2, 9, 0, 0, 0, sydney)); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || !strings.Contains(rec.events[0], "|Sat 3 Oct|") {
		t.Fatalf("all-day reminder = %v", rec.events)
	}
}
