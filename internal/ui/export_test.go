package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"comstac/internal/store"
)

func TestMessageExport(t *testing.T) {
	raw := []byte("From: Shop <shop@example.com>\r\nReply-To: =?UTF-8?Q?Sh=C3=B6p?= <help@other.example>\r\nReturn-Path: <bounce@example.com>\r\nSubject: x\r\n\r\nbody")
	d := &store.MessageDetail{ID: 3, Subject: "Your order", FromAddr: "Shop <shop@example.com>", ToAddr: "me@example.org",
		SentAt: "2026-11-24 01:00:00", MessageID: "<abc@example.com>", BodyText: "plain body",
		BodyHTML:    `<p>See <a href="https://example.com/o/1">your order</a></p>`,
		AuthResults: `{"spf":"pass","spf_domain":"example.com","dkim":"fail","dkim_domain":"example.com","dmarc":"reject"}`,
		Spam:        true, Attachments: []store.AttachmentInfo{{Filename: "invoice.pdf", ContentType: "application/pdf"}}}
	e := buildMessageExport(d, raw)
	if e.ReplyTo != "Shöp <help@other.example>" || e.ReturnPath != "bounce@example.com" || e.MessageID != "abc@example.com" {
		t.Fatalf("headers = %+v", e)
	}
	if e.Date != "Tue, 24 Nov 2026 12:00:00 +1100" {
		t.Fatalf("date = %q", e.Date)
	}
	if e.BodyFormat != "markdown" || e.Body != "See [your order](https://example.com/o/1)" {
		t.Fatalf("body = %q (%s)", e.Body, e.BodyFormat)
	}
	md := e.markdown()
	for _, want := range []string{"# Your order\n", "- **Reply-To:** Shöp <help@other.example>",
		"- **Authentication:** SPF pass (example.com), DKIM fail (example.com), DMARC policy reject",
		"- **Flags:** marked spam", "invoice.pdf (application/pdf)", "\n---\n\nSee [your order]"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"authentication":{"spf":"pass"`) || !strings.Contains(string(b), `"body_format":"markdown"`) {
		t.Errorf("json = %s", b)
	}

	plain := buildMessageExport(&store.MessageDetail{BodyText: "hello", FromAddr: "a@b"}, nil)
	if plain.Subject != "(no subject)" || plain.BodyFormat != "text" || plain.Body != "hello" || plain.Auth != nil {
		t.Errorf("plain export = %+v", plain)
	}
	if got := exportFilename("Re: Your order #123 — confirmed!"); got != "re-your-order-123-confirmed" {
		t.Errorf("filename = %q", got)
	}
}
