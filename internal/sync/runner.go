package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"comstac/internal/imap"
)

type Runner struct {
	db          *sql.DB
	interval    time.Duration
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	nowFn       func() time.Time
	imapAdapter imap.StateSyncAdapter
}

func NewRunner(db *sql.DB, interval time.Duration, imapAdapter imap.StateSyncAdapter) *Runner {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if imapAdapter == nil {
		imapAdapter = imap.NewNoopStateSyncAdapter(false)
	}
	return &Runner{
		db:          db,
		interval:    interval,
		maxAttempts: 5,
		baseBackoff: 15 * time.Second,
		maxBackoff:  15 * time.Minute,
		nowFn:       time.Now,
		imapAdapter: imapAdapter,
	}
}

func (r *Runner) Run(ctx context.Context) error {
	if r.db == nil {
		return fmt.Errorf("sync runner requires db")
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		if err := r.processOne(ctx); err != nil {
			slog.Error("sync process", "component", "sync", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runner) processOne(ctx context.Context) error {
	var (
		jobID     int64
		messageID int64
		action    string
		payload   string
		source    string
		accountID sql.NullInt64
		remoteID  string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT
			sj.id,
			sj.message_id,
			sj.action,
			sj.payload_json,
			mr.source,
			COALESCE(m.account_id, mr.account_id),
			COALESCE(mr.remote_id, '')
		FROM sync_jobs sj
		JOIN messages m ON m.id = sj.message_id
		JOIN messages_raw mr ON mr.id = m.raw_id
		WHERE status IN ('pending', 'retrying')
		  AND next_attempt_at_unix <= strftime('%s', 'now')
		ORDER BY sj.id ASC
		LIMIT 1
	`).Scan(&jobID, &messageID, &action, &payload, &source, &accountID, &remoteID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("select sync job: %w", err)
	}

	var applyErr error
	switch source {
	case "imap":
		applyErr = r.imapAdapter.ApplyStateAction(ctx, imap.StateAction{
			MessageID:   messageID,
			AccountID:   accountID,
			RemoteID:    remoteID,
			Action:      action,
			PayloadJSON: payload,
		})
	default:
		// Unknown sources are treated as complete to avoid queue blockage.
		applyErr = nil
	}

	if applyErr == nil {
		if err := r.markDone(ctx, jobID); err != nil {
			return err
		}
		slog.Info("sync job done", "component", "sync", "job_id", jobID, "message_id", messageID, "source", source, "action", action)
		return nil
	}

	if err := r.markFailure(ctx, jobID, applyErr); err != nil {
		return err
	}
	slog.Warn("sync job failed", "component", "sync", "job_id", jobID, "message_id", messageID, "source", source, "action", action, "err", applyErr)
	return nil
}

func (r *Runner) markDone(ctx context.Context, jobID int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status='done', attempts=attempts+1, last_error=NULL, updated_at=datetime('now')
		WHERE id = ?
	`, jobID)
	if err != nil {
		return fmt.Errorf("mark sync job done: %w", err)
	}
	return nil
}

func (r *Runner) markFailure(ctx context.Context, jobID int64, applyErr error) error {
	var attempts int
	err := r.db.QueryRowContext(ctx, `SELECT attempts FROM sync_jobs WHERE id = ?`, jobID).Scan(&attempts)
	if err != nil {
		return fmt.Errorf("load sync attempts: %w", err)
	}

	nextAttempts := attempts + 1
	status := "retrying"
	nextAttemptUnix := r.nowFn().Add(r.backoff(nextAttempts)).Unix()
	if nextAttempts >= r.maxAttempts || imap.IsPermanentError(applyErr) {
		status = "failed"
		nextAttemptUnix = 0
	}

	_, err = r.db.ExecContext(ctx, `
		UPDATE sync_jobs
		SET
			status = ?,
			attempts = ?,
			next_attempt_at_unix = ?,
			last_error = ?,
			updated_at = datetime('now')
		WHERE id = ?
	`, status, nextAttempts, nextAttemptUnix, trimErr(applyErr), jobID)
	if err != nil {
		return fmt.Errorf("mark sync job failure: %w", err)
	}
	return nil
}

func (r *Runner) backoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	seconds := r.baseBackoff.Seconds() * math.Pow(2, float64(attempt-1))
	d := time.Duration(seconds) * time.Second
	if d < r.baseBackoff {
		d = r.baseBackoff
	}
	if d > r.maxBackoff {
		d = r.maxBackoff
	}
	return d
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 1000 {
		return s[:1000]
	}
	return s
}
