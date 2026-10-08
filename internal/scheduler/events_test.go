package scheduler

import (
	"context"
	"fmt"
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

func TestSnoozedCalendarNotificationSurvivesSchedulerRestart(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	start := time.Date(2026, 10, 6, 11, 45, 0, 0, sydney)
	now := start.Add(-time.Hour)
	ev := &store.CalendarEvent{Title: "Work", StartLocal: start.Format("2006-01-02T15:04"), RRule: "FREQ=WEEKLY"}
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCalendarReminder(ctx, db, ev.ID, start, 60); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now, now.Add(time.Minute)} {
		if ok, err := store.SnoozeCalendarNotification(ctx, db, ev.ID, start, "offset:60", at); err != nil || !ok {
			t.Fatalf("snooze = %v %v", ok, err)
		}
	}
	rec := &recordingNotifier{}
	s := New(db, time.Second, rec, sydney)
	if err := s.sendSnoozedEventReminders(ctx, now.Add(9*time.Minute)); err != nil || len(rec.events) != 0 {
		t.Fatalf("early wake = %v %v", rec.events, err)
	}
	// A new scheduler reads the persisted follow-up and can retry a full queue.
	s = New(db, time.Second, rec, sydney)
	rec.rejectEvents = true
	if err := s.sendSnoozedEventReminders(ctx, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rec.rejectEvents = false
	if err := s.sendSnoozedEventReminders(ctx, now.Add(10*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(rec.notifications) != 1 {
		t.Fatalf("follow-ups = %+v", rec.notifications)
	}
	n := rec.notifications[0]
	if n.EventID != ev.ID || !n.Occurrence.Equal(start) || !strings.HasPrefix(n.SourceKey, "snooze:") || n.URL != "/?calendar=2026-10-06" {
		t.Fatalf("notification = %+v", n)
	}
	if err := s.sendSnoozedEventReminders(ctx, now.Add(11*time.Minute)); err != nil || len(rec.events) != 1 {
		t.Fatalf("duplicate wake = %v %v", rec.events, err)
	}
	// The follow-up has its own identity and can itself be snoozed once.
	if ok, err := store.SnoozeCalendarNotification(ctx, db, ev.ID, start, n.SourceKey, now.Add(11*time.Minute)); !ok || err != nil {
		t.Fatalf("repeat snooze = %v %v", ok, err)
	}
	if err := s.sendSnoozedEventReminders(ctx, now.Add(21*time.Minute)); err != nil || len(rec.events) != 2 {
		t.Fatalf("second follow-up = %v %v", rec.events, err)
	}
	got, _ := store.GetCalendarEvent(ctx, db, ev.ID)
	if got.StartLocal != ev.StartLocal || got.RRule != ev.RRule || len(got.Reminders) != 1 {
		t.Fatalf("snooze changed the event: %+v", got)
	}
}

func TestObsoleteCalendarNotificationSnoozesDoNotFire(t *testing.T) {
	for _, change := range []string{"deleted", "dismissed", "skipped", "moved", "stale"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			db := openDB(t)
			start := time.Date(2026, 10, 6, 11, 45, 0, 0, sydney)
			now := start.Add(-time.Hour)
			ev := &store.CalendarEvent{Title: "Work", StartLocal: start.Format("2006-01-02T15:04")}
			if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimCalendarReminder(ctx, db, ev.ID, start, 60); err != nil {
				t.Fatal(err)
			}
			if ok, err := store.SnoozeCalendarNotification(ctx, db, ev.ID, start, "offset:60", now); !ok || err != nil {
				t.Fatalf("snooze = %v %v", ok, err)
			}
			var err error
			switch change {
			case "deleted":
				err = store.DeleteCalendarEvent(ctx, db, ev.ID)
			case "dismissed":
				ev.Status = store.CalendarDismissed
				_, err = store.SaveCalendarEvent(ctx, db, ev, []int{60})
			case "skipped":
				err = store.AddCalendarExDate(ctx, db, ev.ID, "2026-10-06")
			case "moved":
				ev.StartLocal = "2026-10-06T12:45"
				_, err = store.SaveCalendarEvent(ctx, db, ev, []int{60})
			case "stale":
				now = now.Add(reminderGrace)
			}
			if err != nil {
				t.Fatal(err)
			}
			rec := &recordingNotifier{}
			s := New(db, time.Second, rec, sydney)
			if err := s.sendSnoozedEventReminders(ctx, now.Add(10*time.Minute)); err != nil || len(rec.events) != 0 {
				t.Fatalf("obsolete reminder fired: %v %v", rec.events, err)
			}
			if change == "deleted" {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM calendar_notification_snoozes`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("orphaned snoozes: %d %v", n, err)
				}
			}
		})
	}
}

func TestRegularCalendarReminderCarriesSnoozeIdentity(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	rec := &recordingNotifier{}
	s := New(db, time.Second, rec, sydney)
	start := time.Now().In(sydney).Add(time.Hour).Truncate(time.Minute)
	ev := &store.CalendarEvent{Title: "Meeting", StartLocal: start.Format("2006-01-02T15:04")}
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{15}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendEventReminders(ctx, start.Add(-15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(rec.notifications) != 1 {
		t.Fatalf("notifications = %+v", rec.notifications)
	}
	n := rec.notifications[0]
	if n.SourceKey != "offset:15" || n.EventID != ev.ID || !n.Occurrence.Equal(start) || n.Tag != fmt.Sprintf("event-%d-%d", ev.ID, start.Unix()) {
		t.Fatalf("snooze identity = %+v", n)
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

func TestOrdinaryReminderRetriesQueueRejectionOrDisabledNotifier(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%t", disabled), func(t *testing.T) {
			ctx := context.Background()
			db := openDB(t)
			start := time.Date(2026, 10, 6, 11, 45, 0, 0, sydney)
			ev := &store.CalendarEvent{Title: "Work", StartLocal: start.Format("2006-01-02T15:04"), RRule: "FREQ=WEEKLY"}
			if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60, 15}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE calendar_reminders SET created_at = '2026-09-01T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}
			rec := &recordingNotifier{rejectEvents: true}
			s := New(db, time.Second, rec, sydney)
			if disabled {
				s = New(db, time.Second, nil, sydney)
			}
			if err := s.sendEventReminders(ctx, start.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			var claims int
			if err := db.QueryRow(`SELECT count(*) FROM calendar_reminder_fires`).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if claims != 0 {
				t.Fatalf("undelivered reminder consumed: %d claims", claims)
			}
			// Recreate the scheduler: the retry depends on SQLite, not memory.
			rec.rejectEvents = false
			s = New(db, time.Second, rec, sydney)
			for _, now := range []time.Time{start.Add(-59 * time.Minute), start.Add(-58 * time.Minute)} {
				if err := s.sendEventReminders(ctx, now); err != nil {
					t.Fatal(err)
				}
			}
			if len(rec.notifications) != 1 || rec.notifications[0].SourceKey != "offset:60" || !rec.notifications[0].Occurrence.Equal(start) {
				t.Fatalf("retry notifications = %+v", rec.notifications)
			}
			if err := s.sendEventReminders(ctx, start.Add(-15*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := s.sendEventReminders(ctx, start.Add(-14*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM calendar_reminder_fires`).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if len(rec.notifications) != 2 || claims != 2 {
				t.Fatalf("notifications = %d, claims = %d", len(rec.notifications), claims)
			}
		})
	}
}

func TestRejectedOrdinaryReminderDoesNotRetryOutsideGrace(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	start := time.Date(2026, 10, 6, 11, 45, 0, 0, sydney)
	ev := &store.CalendarEvent{Title: "Work", StartLocal: start.Format("2006-01-02T15:04")}
	if _, err := store.SaveCalendarEvent(ctx, db, ev, []int{60}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE calendar_reminders SET created_at = '2026-09-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	rec := &recordingNotifier{rejectEvents: true}
	s := New(db, time.Second, rec, sydney)
	fireAt := start.Add(-time.Hour)
	if err := s.sendEventReminders(ctx, fireAt); err != nil {
		t.Fatal(err)
	}
	rec.rejectEvents = false
	if err := s.sendEventReminders(ctx, fireAt.Add(reminderGrace)); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("stale retry: %v", rec.events)
	}
}
