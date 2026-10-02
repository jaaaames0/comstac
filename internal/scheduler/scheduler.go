// Package scheduler runs time-based work: resurfacing expired snoozes and
// sending calendar event reminders.
package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"comstac/internal/store"
)

// Notifier delivers reminder notifications.
type Notifier interface {
	QueueReminder(subject, from string, messageID int64) bool
	QueueEventReminder(title, body, url, tag string) bool
}

// Scheduler periodically resurfaces due snoozes and sends due event reminders.
type Scheduler struct {
	db       *sql.DB
	interval time.Duration
	notifier Notifier
	loc      *time.Location
	nowFn    func() time.Time
}

// New returns a scheduler that displays reminder times in loc.
func New(db *sql.DB, interval time.Duration, notifier Notifier, loc *time.Location) *Scheduler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if loc == nil {
		loc = time.UTC
	}
	return &Scheduler{db: db, interval: interval, notifier: notifier, loc: loc, nowFn: time.Now}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("scheduler requires db")
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	now := s.nowFn()
	if err := s.wakeSnoozes(ctx, now); err != nil {
		slog.Error("snooze wake", "component", "scheduler", "err", err)
	}
	if err := s.sendEventReminders(ctx, now); err != nil {
		slog.Error("event reminders", "component", "scheduler", "err", err)
	}
}

func (s *Scheduler) wakeSnoozes(ctx context.Context, now time.Time) error {
	woken, err := store.WakeDueSnoozes(ctx, s.db, now)
	if err != nil {
		return err
	}
	for _, m := range woken {
		slog.Info("snooze resurfaced", "component", "scheduler", "message_id", m.ID)
		if s.notifier != nil && !s.notifier.QueueReminder(m.Subject, m.FromAddr, m.ID) {
			slog.Warn("reminder notification dropped", "component", "scheduler", "message_id", m.ID)
		}
	}
	return nil
}
