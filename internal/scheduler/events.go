package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"comstac/internal/calendar"
	"comstac/internal/store"
)

// reminderGrace is how late a reminder may still be sent, e.g. after a
// restart. Older missed reminders are skipped rather than sent in a burst.
const reminderGrace = 6 * time.Hour

func (s *Scheduler) sendEventReminders(ctx context.Context, now time.Time) error {
	events, err := store.ListCalendarEvents(ctx, s.db, store.CalendarConfirmed)
	if err != nil {
		return err
	}
	for i := range events {
		ev := &events[i]
		if len(ev.Reminders) == 0 {
			continue
		}
		e, err := calendar.NewEvent(ev.ID, ev.Title, ev.Kind, ev.Location, ev.AllDay, ev.StartLocal, ev.EndLocal, ev.TZ, ev.RRule, ev.ExDates)
		if err != nil {
			slog.Warn("skip invalid calendar event", "component", "scheduler", "event_id", ev.ID, "err", err)
			continue
		}
		minOff, maxOff := ev.Reminders[0].OffsetMinutes, ev.Reminders[0].OffsetMinutes
		for _, r := range ev.Reminders {
			minOff = min(minOff, r.OffsetMinutes)
			maxOff = max(maxOff, r.OffsetMinutes)
		}
		// fireAt = start - offset must fall in (now-grace, now], so starts lie
		// in (now-grace+minOff, now+maxOff].
		from := now.Add(-reminderGrace + time.Duration(minOff)*time.Minute)
		to := now.Add(time.Duration(maxOff)*time.Minute + time.Second)
		for _, occ := range e.Occurrences(from, to) {
			if occ.Start.Before(from) {
				continue // overlaps the window but started earlier
			}
			for _, r := range ev.Reminders {
				fireAt := occ.Start.Add(-time.Duration(r.OffsetMinutes) * time.Minute)
				if fireAt.After(now) || !fireAt.After(now.Add(-reminderGrace)) {
					continue
				}
				if !r.CreatedAt.IsZero() && fireAt.Before(r.CreatedAt.Add(-time.Minute)) {
					continue // reminder added after this occurrence's reminder time
				}
				claimed, err := store.ClaimCalendarReminder(ctx, s.db, ev.ID, occ.Start, r.OffsetMinutes)
				if err != nil {
					return err
				}
				if !claimed {
					continue
				}
				slog.Info("event reminder", "component", "scheduler", "event_id", ev.ID, "occurrence", occ.Start.Format(time.RFC3339), "offset_minutes", r.OffsetMinutes)
				if s.notifier != nil {
					title, body, url, tag := s.reminderContent(ev, occ)
					if !s.notifier.QueueEventReminder(title, body, url, tag) {
						slog.Warn("event reminder notification dropped", "component", "scheduler", "event_id", ev.ID)
					}
				}
			}
		}
	}
	return nil
}

func (s *Scheduler) reminderContent(ev *store.CalendarEvent, occ calendar.Occurrence) (title, body, url, tag string) {
	start := occ.Start.In(s.loc)
	if ev.AllDay {
		body = start.Format("Mon 2 Jan")
		if days := int(occ.End.Sub(occ.Start).Hours()/24 + 0.5); days > 1 {
			body += " – " + occ.End.AddDate(0, 0, -1).In(s.loc).Format("Mon 2 Jan")
		}
	} else {
		body = start.Format("Mon 2 Jan · 15:04")
		if occ.End.After(occ.Start) {
			body += "–" + occ.End.In(s.loc).Format("15:04")
		}
	}
	if ev.Location != "" {
		body += " · " + ev.Location
	}
	return "Reminder: " + ev.Title, body,
		"/?calendar=" + start.Format(calendar.DateLayout),
		fmt.Sprintf("event-%d-%d", ev.ID, occ.Start.Unix())
}
