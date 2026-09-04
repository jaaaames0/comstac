package securitymetrics

import (
	"sync"
	"testing"
)

func TestCountersRecordOnlyFixedTotalsConcurrently(t *testing.T) {
	var counters Counters
	const workers = 32

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counters.RecordLoginFailure()
			counters.RecordLoginRateLimitRejection()
			counters.RecordSMTPTemporaryRejection()
			counters.RecordSMTPSaturationRejection()
			counters.RecordNotificationDrop()
		}()
	}
	wg.Wait()

	got := counters.Snapshot()
	if got.LoginFailures != workers || got.LoginRateLimitRejections != workers {
		t.Fatalf("login counters = %+v", got)
	}
	if got.SMTPTemporaryRejections != 2*workers || got.SMTPSaturationRejections != workers {
		t.Fatalf("SMTP counters = %+v", got)
	}
	if got.NotificationDrops != workers {
		t.Fatalf("notification counters = %+v", got)
	}
}

func TestNilCountersAreNoop(t *testing.T) {
	var counters *Counters
	counters.RecordLoginFailure()
	counters.RecordLoginRateLimitRejection()
	counters.RecordSMTPTemporaryRejection()
	counters.RecordSMTPSaturationRejection()
	counters.RecordNotificationDrop()
	if got := counters.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("nil snapshot = %+v", got)
	}
}
