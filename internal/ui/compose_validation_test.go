package ui

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/mailcontent"
	"comstac/internal/store"
)

func TestRecipientQuotedCommaRemainsOneAddress(t *testing.T) {
	sender := &fakeSender{}
	mux := http.NewServeMux()
	registerRoutes(mux, nil, sender, nil)
	form := url.Values{"to": {`"Doe, Jane" <jane@example.test>`}, "cc": {"Copy <copy@example.test>"}, "bcc": {"secret@example.test"}, "subject": {"Fixture"}, "body": {"Body"}}
	req := httptest.NewRequest(http.MethodPost, "/ui/compose", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if sender.calls != 1 || len(sender.message.To) != 1 {
		t.Fatalf("send split display name: %+v", sender.message.To)
	}
}

func TestOversizedAndUnavailableAttachmentsReturnErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/ui/compose", nil)
	req.MultipartForm = &multipart.Form{File: map[string][]*multipart.FileHeader{"attachments": {{Filename: "large.bin", Size: mailcontent.MaxMessageBytes + 1}}}}
	if files, err := extractAttachments(req); !errors.Is(err, mailcontent.ErrTooLarge) || files != nil {
		t.Fatalf("oversize=%v %v", files, err)
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("attachments", "missing.bin")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("fixture"))
	writer.Close()
	req = httptest.NewRequest(http.MethodPost, "/ui/compose", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(0); err != nil {
		t.Fatal(err)
	}
	req.MultipartForm.RemoveAll() // The upload's temporary file is now unavailable.
	if files, err := extractAttachments(req); err == nil || files != nil {
		t.Fatalf("missing upload silently omitted: %v %v", files, err)
	}
}

func TestInvalidRecipientsMakeZeroSendsAndPreserveDraft(t *testing.T) {
	for _, route := range []string{"/ui/compose", "/ui/reply"} {
		t.Run(route, func(t *testing.T) {
			db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "draft.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			sender := &fakeSender{}
			mux := http.NewServeMux()
			registerRoutes(mux, db, sender, nil)
			form := url.Values{"to": {"broken-address"}, "cc": {"copy@example.test"}, "bcc": {"secret@example.test"}, "subject": {"Keep subject"}, "body": {"Keep body"}}
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if sender.calls != 0 {
				t.Fatal("invalid recipient was sent")
			}
			for _, text := range []string{"broken-address", "copy@example.test", "secret@example.test", "Keep subject", "Keep body"} {
				if !strings.Contains(response.Body.String(), text) {
					t.Fatalf("draft lost %q: %s", text, response.Body.String())
				}
			}
		})
	}
}

func TestEmptyAttachmentIsIncluded(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("to", "recipient@example.test")
	writer.WriteField("body", "Body")
	if _, err := writer.CreateFormFile("attachments", "empty.txt"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	sender := &fakeSender{}
	mux := http.NewServeMux()
	registerRoutes(mux, nil, sender, nil)
	req := httptest.NewRequest(http.MethodPost, "/ui/compose", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if sender.calls != 1 || len(sender.message.Attachments) != 1 || len(sender.message.Attachments[0].Data) != 0 {
		t.Fatalf("attachment silently omitted: %+v", sender.message.Attachments)
	}
}
