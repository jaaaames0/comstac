package push

import "testing"

func TestNotificationQueueIsBounded(t *testing.T) {
	n := &Notifier{queue: make(chan newMailNotification, 1)}
	if !n.QueueNewMail("first", "sender@example.com", 1) {
		t.Fatal("first notification was not queued")
	}
	if n.QueueNewMail("second", "sender@example.com", 2) {
		t.Fatal("notification queue accepted work past its capacity")
	}
}
