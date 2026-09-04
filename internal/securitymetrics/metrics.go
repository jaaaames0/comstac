// Package securitymetrics records a deliberately small set of sanitized,
// process-local security and saturation counters. It stores no addresses,
// credentials, message content, paths, or dynamic labels.
package securitymetrics

import "sync/atomic"

// Counters is safe for concurrent use. Values reset when Comstac restarts.
type Counters struct {
	loginFailures            atomic.Int64
	loginRateLimitRejections atomic.Int64
	smtpTemporaryRejections  atomic.Int64
	smtpSaturationRejections atomic.Int64
	notificationDrops        atomic.Int64
}

// Snapshot is the fixed, non-secret view exposed by authenticated metrics.
type Snapshot struct {
	LoginFailures            int64
	LoginRateLimitRejections int64
	SMTPTemporaryRejections  int64
	SMTPSaturationRejections int64
	NotificationDrops        int64
}

func (c *Counters) RecordLoginFailure() {
	if c != nil {
		c.loginFailures.Add(1)
	}
}

func (c *Counters) RecordLoginRateLimitRejection() {
	if c != nil {
		c.loginRateLimitRejections.Add(1)
	}
}

func (c *Counters) RecordSMTPTemporaryRejection() {
	if c != nil {
		c.smtpTemporaryRejections.Add(1)
	}
}

// RecordSMTPSaturationRejection records both the temporary SMTP rejection and
// the narrower saturation subset without accepting a caller-controlled label.
func (c *Counters) RecordSMTPSaturationRejection() {
	if c != nil {
		c.smtpTemporaryRejections.Add(1)
		c.smtpSaturationRejections.Add(1)
	}
}

func (c *Counters) RecordNotificationDrop() {
	if c != nil {
		c.notificationDrops.Add(1)
	}
}

func (c *Counters) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	return Snapshot{
		LoginFailures:            c.loginFailures.Load(),
		LoginRateLimitRejections: c.loginRateLimitRejections.Load(),
		SMTPTemporaryRejections:  c.smtpTemporaryRejections.Load(),
		SMTPSaturationRejections: c.smtpSaturationRejections.Load(),
		NotificationDrops:        c.notificationDrops.Load(),
	}
}
