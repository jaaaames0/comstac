package push

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestNotificationQueueIsBounded(t *testing.T) {
	n := &Notifier{queue: make(chan newMailNotification, 1)}
	if !n.QueueNewMail("first", "sender@example.com", 1) {
		t.Fatal("first notification was not queued")
	}
	if n.QueueNewMail("second", "sender@example.com", 2) {
		t.Fatal("notification queue accepted work past its capacity")
	}
}

func TestMailPreviewFromMIME(t *testing.T) {
	n := newTestNotifier(t)
	ctx := context.Background()
	raw := "From: Alex <alex@example.com>\r\nTo: me@example.com\r\nSubject: Delivery\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><head><style>hidden CSS</style></head><body><p>Your order &amp; receipt</p><p>Arrives Thursday.</p></body></html>"
	if err := ingest.NewService(n.db).IngestRaw(ctx, ingest.IngestInput{
		Source: ingest.SourceSMTP, EnvelopeFrom: "alex@example.com", EnvelopeTo: []string{"me@example.com"}, RawMIME: []byte(raw),
	}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := n.db.QueryRow(`SELECT id FROM messages`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	p := n.mailPayload(ctx, "mail", "Delivery", "Alex <alex@example.com>", id)
	if p.Title != "Alex · Delivery" || p.Body != "Your order & receipt Arrives Thursday." || p.Kind != "mail" || p.MessageID != id {
		t.Fatalf("notification = %+v", p)
	}
	r := n.mailPayload(ctx, "reminder", "Delivery", "Alex <alex@example.com>", id)
	if r.Title != "Reminder: Alex · Delivery" || r.Body != p.Body || r.Tag == p.Tag {
		t.Fatalf("resurfaced notification = %+v", r)
	}
	p = n.mailPayload(ctx, "mail", "", "sender@example.com", 9999)
	if p.Title != "sender@example.com · (no subject)" || p.Body != "" {
		t.Fatalf("missing body fallback = %+v", p)
	}
}

func TestNotificationTextBoundsUnicodeAndWhitespace(t *testing.T) {
	if got := notificationText("  Hello\n\tworld\u00a0!  ", 300); got != "Hello world !" {
		t.Fatalf("whitespace = %q", got)
	}
	got := notificationText(strings.Repeat("🙂", 400), 300)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 300 || !strings.HasSuffix(got, "…") {
		t.Fatalf("Unicode truncation = %q", got)
	}
}

func TestMailPreviewOmitsLinkTargetsBeforeTruncation(t *testing.T) {
	n := newTestNotifier(t)
	ctx := context.Background()
	body := "View your receipt [https://example.com/track?token=" + strings.Repeat("x", 400) + "]\nYour order arrives Thursday."
	raw := "From: Alex <alex@example.com>\r\nTo: me@example.com\r\nSubject: Delivery\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body
	if err := ingest.NewService(n.db).IngestRaw(ctx, ingest.IngestInput{
		Source: ingest.SourceSMTP, EnvelopeFrom: "alex@example.com", EnvelopeTo: []string{"me@example.com"}, RawMIME: []byte(raw),
	}); err != nil {
		t.Fatal(err)
	}
	var id int64
	var storedBody string
	if err := n.db.QueryRow(`SELECT id, body_text FROM messages`).Scan(&id, &storedBody); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"mail", "reminder"} {
		p := n.mailPayload(ctx, kind, "Delivery", "Alex <alex@example.com>", id)
		if p.Body != "View your receipt Your order arrives Thursday." {
			t.Fatalf("%s preview = %q", kind, p.Body)
		}
	}
	if !strings.Contains(storedBody, "https://example.com/track?token=") {
		t.Fatal("preview cleanup changed the stored email")
	}
}

func TestNotificationPreviewKeepsReadableLabels(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Read [the receipt](https://example.com/receipt) today.", "Read the receipt today."},
		{"Receipt [ HTTP://example.com/receipt ] for order [INV-123].", "Receipt for order [INV-123]."},
		{"Receipt <https://example.com/receipt> arrives today.", "Receipt arrives today."},
		{"[https://example.com/only-a-link]", ""},
		{"Meet at 10 [bring ID].", "Meet at 10 [bring ID]."},
	} {
		if got := notificationPreview(tc.text); got != tc.want {
			t.Errorf("preview of %q = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestHiddenPaddingPreviewDoesNotChangeMail(t *testing.T) {
	n := newTestNotifier(t)
	ctx := context.Background()
	raw := []byte("Subject: Fixture\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<div style='display:none;max-height:0px;overflow:\n hidden'>" + strings.Repeat("\u034f\u200c&nbsp;", 400) + "</div><p>96 items arrive Thursday.</p>")
	if err := ingest.NewService(n.db).IngestRaw(ctx, ingest.IngestInput{Source: ingest.SourceSMTP, RawMIME: raw}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := n.db.QueryRow(`SELECT id FROM messages`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"mail", "reminder"} {
		if p := n.mailPayload(ctx, kind, "Fixture", "Sender", id); p.Body != "96 items arrive Thursday." {
			t.Fatalf("%s: %+v", kind, p)
		}
	}
	stored, err := store.GetRawMIMEByMessageID(ctx, n.db, id)
	if err != nil || string(stored) != string(raw) {
		t.Fatalf("stored MIME changed: %v", err)
	}
}
