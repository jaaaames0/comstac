// Package snooze returns snoozed messages to the inbox when they fall due.
package snooze

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"comstac/internal/store"
)

// ReminderNotifier is told about each message that resurfaces.
type ReminderNotifier interface {
	QueueReminder(subject, from string, messageID int64) bool
}

// Waker periodically resurfaces messages whose snooze has expired.
type Waker struct {
	db       *sql.DB
	interval time.Duration
	notifier ReminderNotifier
	nowFn    func() time.Time
}

func NewWaker(db *sql.DB, interval time.Duration, notifier ReminderNotifier) *Waker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Waker{db: db, interval: interval, notifier: notifier, nowFn: time.Now}
}

func (w *Waker) Run(ctx context.Context) error {
	if w.db == nil {
		return fmt.Errorf("snooze waker requires db")
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		if err := w.wakeOnce(ctx); err != nil {
			slog.Error("snooze wake", "component", "snooze", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Waker) wakeOnce(ctx context.Context) error {
	woken, err := store.WakeDueSnoozes(ctx, w.db, w.nowFn())
	if err != nil {
		return err
	}
	for _, m := range woken {
		slog.Info("snooze resurfaced", "component", "snooze", "message_id", m.ID)
		if w.notifier != nil && !w.notifier.QueueReminder(m.Subject, m.FromAddr, m.ID) {
			slog.Warn("reminder notification dropped", "component", "snooze", "message_id", m.ID)
		}
	}
	return nil
}
