package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"comstac/internal/store"
)

func TestAllDayPresetsFireAtLocalNineAcrossDST(t *testing.T) {
	for _, date := range []string{"2026-10-03", "2026-10-04", "2027-04-03", "2027-04-04"} {
		for offset, days := range map[int]int{-540: 0, 900: 1, 3780: 3, 9540: 7} {
			t.Run(fmt.Sprintf("%s/offset=%d", date, offset), func(t *testing.T) {
				ctx := context.Background()
				db := openDB(t)
				rec := &recordingNotifier{}
				day, err := time.ParseInLocation("2006-01-02", date, sydney)
				if err != nil {
					t.Fatal(err)
				}
				start := day.AddDate(0, 0, days)
				ev := &store.CalendarEvent{Title: "DST fixture", AllDay: true, StartLocal: start.Format("2006-01-02"), TZ: "Australia/Sydney", RRule: "FREQ=WEEKLY"}
				if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{offset}); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE calendar_reminders SET created_at='2026-01-01T00:00:00Z'`); err != nil {
					t.Fatal(err)
				}
				nine := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, sydney)
				s := New(db, time.Second, rec, sydney)
				if err := s.sendEventReminders(ctx, nine.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if len(rec.events) != 0 {
					t.Fatalf("reminded before local 9am: %v", rec.events)
				}
				rec.rejectEvents = true
				if err := s.sendEventReminders(ctx, nine); err != nil {
					t.Fatal(err)
				}
				rec.rejectEvents = false
				if err := s.sendEventReminders(ctx, nine.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if len(rec.events) != 1 {
					t.Fatalf("local 9am reminder missing: %v", rec.events)
				}
				s = New(db, time.Second, rec, sydney)
				if err := s.sendEventReminders(ctx, nine.Add(time.Hour)); err != nil || len(rec.events) != 1 {
					t.Fatalf("restart duplicate: %v %v", rec.events, err)
				}
			})
		}
	}
}

func TestImportedAllDayReminderGraceBoundary(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	fire := time.Date(2026, 11, 1, 9, 0, 0, 0, loc)
	for _, late := range []time.Duration{6*time.Hour - time.Second, 6 * time.Hour} {
		ctx := context.Background()
		db := openDB(t)
		ev := &store.CalendarEvent{Title: "Imported", AllDay: true, StartLocal: "2026-11-02", TZ: loc.String()}
		if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{900}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE calendar_reminders SET created_at='2026-01-01T00:00:00Z'`); err != nil {
			t.Fatal(err)
		}
		rec := &recordingNotifier{}
		s := New(db, time.Second, rec, sydney)
		if err := s.sendEventReminders(ctx, fire.Add(late)); err != nil {
			t.Fatal(err)
		}
		want := 1
		if late == 6*time.Hour {
			want = 0
		}
		if len(rec.events) != want {
			t.Fatalf("late=%v events=%v", late, rec.events)
		}
	}
}
