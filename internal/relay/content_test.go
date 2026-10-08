package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"testing"

	"comstac/internal/mailcontent"
)

func TestRecipientsAndAttachmentRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte{0, 1, 255, 'x'}, 64)
	msg := OutboundMessage{To: []string{`"Doe, Jane" <jane@example.test>`}, CC: []string{"日本語 <copy@example.test>"}, BCC: []string{"Private <secret@example.test>"}, Subject: "日本語 receipt", BodyText: "  Keep body whitespace.\n", Attachments: []Attachment{{Filename: `receipt "日本語".bin`, Data: data}, {Filename: "empty.txt", Data: []byte{}}}}
	msg, envelope, err := normalizeRecipients(msg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(envelope, ",") != "jane@example.test,copy@example.test,secret@example.test" {
		t.Fatalf("envelope=%v", envelope)
	}
	raw, err := buildRawMIME("owner@example.test", msg, "fixture@example.test")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Bcc") != "" || strings.Contains(string(raw), "secret@example.test") {
		t.Fatal("BCC disclosed in MIME")
	}
	for header, want := range map[string]string{"To": "Doe, Jane", "Cc": "日本語"} {
		addresses, err := parsed.Header.AddressList(header)
		if err != nil || len(addresses) != 1 || addresses[0].Name != want {
			t.Fatalf("%s=%v %v", header, addresses, err)
		}
	}
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(part)
	if err != nil || string(body) != msg.BodyText {
		t.Fatalf("body=%q %v", body, err)
	}
	for _, want := range msg.Attachments {
		part, err = reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		if err != nil || !bytes.Equal(got, want.Data) || part.FileName() != want.Filename {
			t.Fatalf("attachment=%q len=%d %v", part.FileName(), len(got), err)
		}
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("oversized MIME line: %d", len(line))
		}
	}
}

func TestSerializedSizeExactLimitAndFailure(t *testing.T) {
	msg := OutboundMessage{To: []string{"recipient@example.test"}, BodyText: strings.Repeat("x", 128), Attachments: []Attachment{{Filename: "fixture.bin", Data: bytes.Repeat([]byte{'y'}, 128)}}}
	var buf bytes.Buffer
	if err := writeRawMIME(&buf, "owner@example.test", msg, "fixed@example.test", 4096); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{int64(buf.Len()), int64(buf.Len() - 1)} {
		var output bytes.Buffer
		err := writeRawMIME(&output, "owner@example.test", msg, "fixed@example.test", limit)
		if limit == int64(buf.Len()) && err != nil {
			t.Fatalf("exact fit=%v", err)
		}
		if limit < int64(buf.Len()) && !errors.Is(err, mailcontent.ErrTooLarge) {
			t.Fatalf("over-limit=%v", err)
		}
		if int64(output.Len()) > limit {
			t.Fatal("serialized allocation exceeded limit")
		}
	}
}

func TestBadContentNeverDialsSMTP(t *testing.T) {
	for _, msg := range []OutboundMessage{
		{To: []string{"broken-address"}},
		{To: []string{"valid@example.test\r\nBcc: victim@example.test"}},
		{To: []string{"valid@example.test"}, Attachments: []Attachment{{Filename: "x", ContentType: "text/plain\r\nX-Evil: true"}}},
	} {
		r := New(Config{From: "owner@example.test"})
		dialed := false
		r.dialContext = func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		}
		_, _, err := r.Send(context.Background(), msg)
		if err == nil || dialed {
			t.Fatalf("invalid content dialed=%t err=%v", dialed, err)
		}
	}
}
