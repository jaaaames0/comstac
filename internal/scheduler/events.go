package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"comstac/internal/calendar"
	"comstac/internal/push"
	"comstac/internal/store"
)

// reminderGrace is how late a reminder may still be sent, e.g. after a
// restart. Older missed reminders are skipped rather than sent in a burst.
const reminderGrace = 6 * time.Hour

func (s *Scheduler) sendEventReminders(ctx context.Context, now time.Time) error {
	if s.notifier == nil {
		return nil // Do not consume reminders while push is disabled.
	}
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
		from, to := calendar.ReminderWindow(now, reminderGrace, e.Start.Location(), ev.AllDay, ev.Reminders[0].OffsetMinutes)
		for _, r := range ev.Reminders {
			lower, upper := calendar.ReminderWindow(now, reminderGrace, e.Start.Location(), ev.AllDay, r.OffsetMinutes)
			if lower.Before(from) {
				from = lower
			}
			if upper.After(to) {
				to = upper
			}
		}
		for _, occ := range e.Occurrences(from, to) {
			if occ.Start.Before(from) {
				continue // overlaps the window but started earlier
			}
			for _, r := range ev.Reminders {
				fireAt := calendar.ReminderTime(occ.Start, ev.AllDay, r.OffsetMinutes)
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
				title, body, url, tag := s.reminderContent(ev, occ)
				if !s.notifier.QueueEventReminder(push.EventReminder{
					Title: title, Body: body, URL: url, Tag: tag, EventID: ev.ID,
					Occurrence: occ.Start, SourceKey: fmt.Sprintf("offset:%d", r.OffsetMinutes),
				}) {
					if err := store.ReleaseCalendarReminder(ctx, s.db, ev.ID, occ.Start, r.OffsetMinutes); err != nil {
						return err
					}
					slog.Warn("event reminder push queue full", "component", "scheduler", "event_id", ev.ID)
				}
			}
		}
	}
	return nil
}

func (s *Scheduler) sendSnoozedEventReminders(ctx context.Context, now time.Time) error {
	snoozes, err := store.DueCalendarNotificationSnoozes(ctx, s.db, now, reminderGrace)
	if err != nil {
		return err
	}
	for _, snooze := range snoozes {
		ev, err := store.GetCalendarEvent(ctx, s.db, snooze.EventID)
		if err != nil {
			return err
		}
		var occurrence *calendar.Occurrence
		if ev != nil && ev.Status == store.CalendarConfirmed {
			e, err := calendar.NewEvent(ev.ID, ev.Title, ev.Kind, ev.Location, ev.AllDay, ev.StartLocal, ev.EndLocal, ev.TZ, ev.RRule, ev.ExDates)
			if err == nil {
				for _, occ := range e.Occurrences(snooze.Occurrence, snooze.Occurrence.Add(time.Second)) {
					if occ.Start.Equal(snooze.Occurrence) {
						occurrence = &occ
						break
					}
				}
			}
		}
		// Claim obsolete occurrences too, so cancelled or moved events do not
		// keep getting reconsidered. The calendar event itself is untouched.
		claimed, err := store.ClaimCalendarNotificationSnooze(ctx, s.db, snooze.ID)
		if err != nil {
			return err
		}
		if !claimed || occurrence == nil || s.notifier == nil {
			continue
		}
		title, body, url, tag := s.reminderContent(ev, *occurrence)
		if !s.notifier.QueueEventReminder(push.EventReminder{
			Title: title, Body: body, URL: url, Tag: tag, EventID: ev.ID,
			Occurrence: occurrence.Start, SourceKey: fmt.Sprintf("snooze:%d", snooze.ID),
		}) {
			if err := store.ReleaseCalendarNotificationSnooze(ctx, s.db, snooze.ID); err != nil {
				return err
			}
			slog.Warn("snoozed reminder push queue full", "event_id", ev.ID)
		}
	}
	return nil
}

func (s *Scheduler) reminderContent(ev *store.CalendarEvent, occ calendar.Occurrence) (title, body, url, tag string) {
	// Times read in the event's own zone (a flight's departure airport),
	// labelled when that differs from the home zone.
	start := occ.Start
	if ev.AllDay {
		body = start.Format("Mon 2 Jan")
		if days := int(occ.End.Sub(occ.Start).Hours()/24 + 0.5); days > 1 {
			body += " – " + occ.End.AddDate(0, 0, -1).Format("Mon 2 Jan")
		}
	} else {
		body = start.Format("Mon 2 Jan · 15:04")
		if occ.End.After(occ.Start) {
			body += "–" + occ.End.Format("15:04")
		}
		if start.Location().String() != s.loc.String() {
			body += " " + start.Format("MST")
		}
	}
	if ev.Location != "" {
		body += " · " + ev.Location
	}
	return "Reminder: " + ev.Title, body,
		"/?calendar=" + start.In(s.loc).Format(calendar.DateLayout),
		fmt.Sprintf("event-%d-%d", ev.ID, occ.Start.Unix())
}
