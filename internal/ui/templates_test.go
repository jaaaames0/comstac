package ui

import (
	"bytes"
	"strings"
	"testing"

	"comstac/internal/store"
)

func TestEmailBodyDocumentBlocksRemoteLoadsByDefault(t *testing.T) {
	body := `<html><head><meta content="0; url=https://tracker.example/open" HTTP-EQUIV="ReFrEsH"></head>` +
		`<body style="background:url(https://tracker.example/bg.png)"><img src="https://tracker.example/pixel.png">` +
		`<script>location='https://tracker.example/script'</script></body></html>`

	blocked := emailBodyDocument(body, false)
	prefix := blocked[:strings.Index(blocked, `<base target="_blank">`)]
	for _, want := range []string{"default-src 'none'", "script-src 'none'", "connect-src 'none'", "img-src data:;"} {
		if !strings.Contains(prefix, want) {
			t.Errorf("blocked policy missing %q: %s", want, prefix)
		}
	}
	if strings.Contains(prefix, "img-src data: https:") {
		t.Fatalf("blocked policy permits remote images: %s", prefix)
	}
	if strings.Contains(strings.ToLower(blocked), "http-equiv=\"refresh\"") || strings.Contains(blocked, "0; url=https://tracker.example/open") {
		t.Fatalf("refresh directive survived: %s", blocked)
	}
	if !strings.Contains(blocked, `<img src="https://tracker.example/pixel.png">`) {
		t.Fatal("message markup was unexpectedly discarded")
	}
}

func TestEmailBodyDocumentAllowsHTTPSImagesOnlyWhenRequested(t *testing.T) {
	remote := emailBodyDocument(`<img src="https://images.example/pixel.png">`, true)
	prefix := remote[:strings.Index(remote, `<base target="_blank">`)]
	if !strings.Contains(prefix, "img-src data: https:") || !strings.Contains(prefix, "upgrade-insecure-requests") {
		t.Fatalf("remote-image policy = %s", prefix)
	}
	for _, forbidden := range []string{"script-src 'self'", "connect-src 'self'", "frame-src 'self'"} {
		if strings.Contains(prefix, forbidden) {
			t.Fatalf("remote-image policy unexpectedly permits %q: %s", forbidden, prefix)
		}
	}
}

func TestMessageDetailRemoteImageControlAndTemplateEscaping(t *testing.T) {
	detail := &store.MessageDetail{ID: 7, BodyHTML: `<img src="https://images.example/a.png">`}

	blocked := newMessageDetailData(detail, false)
	var rendered bytes.Buffer
	execute(&rendered, "message_detail", blocked)
	blockedHTML := rendered.String()
	if strings.Contains(blockedHTML, "remote images are blocked") || strings.Contains(blockedHTML, `class="remote-images"`) {
		t.Fatalf("large remote-image banner survived: %s", blockedHTML)
	}
	if !strings.Contains(blockedHTML, "remote_images=1") || !strings.Contains(blockedHTML, ">load images</button>") {
		t.Fatalf("blocked control missing: %s", rendered.String())
	}
	bodyIndex := strings.Index(blockedHTML, `<div class="msg-body`)
	loadIndex := strings.Index(blockedHTML, "remote_images=1")
	if bodyIndex < 0 || loadIndex < 0 || loadIndex > bodyIndex {
		t.Fatal("load-images action is not in the message header")
	}

	rendered.Reset()
	execute(&rendered, "message_detail", newMessageDetailData(detail, true))
	if strings.Contains(rendered.String(), "remote images loaded for this message") || strings.Contains(rendered.String(), ">load images</button>") {
		t.Fatalf("loaded state incorrect: %s", rendered.String())
	}

	rendered.Reset()
	execute(&rendered, "send_error", sendErrorData{Msg: `<img src=x onerror=alert(1)>`})
	if strings.Contains(rendered.String(), `<img src=x`) || !strings.Contains(rendered.String(), "&lt;img") {
		t.Fatalf("error output was not escaped: %s", rendered.String())
	}
}

func TestAccountsRemoteImagePreferenceEscapesAddresses(t *testing.T) {
	var rendered bytes.Buffer
	execute(&rendered, "accounts", accountsData{ImageSenders: []store.RemoteImageSender{{
		EmailAddress: `\"><img src=x onerror=alert(1)>@example.com`,
		CreatedAt:    "2026-09-03 00:00:00",
	}}})
	html := rendered.String()
	if strings.Contains(html, `<img src=x`) || !strings.Contains(html, "&lt;img") {
		t.Fatalf("remote-image address was not escaped: %s", html)
	}
	for _, want := range []string{"privacy preference", "From", "aligned DMARC", "remote_image_add", "remote_image_remove"} {
		if !strings.Contains(html, want) {
			t.Fatalf("accounts preference UI missing %q", want)
		}
	}
}

func TestIndexUsesOnlyVendoredHTMXAndNonceAuthorizedInlineScript(t *testing.T) {
	var rendered bytes.Buffer
	execute(&rendered, "index", indexData{CSRFToken: "csrf", CSPNonce: "nonce-value"})
	html := rendered.String()
	if strings.Contains(html, "unpkg.com") || !strings.Contains(html, `/static/htmx-1.9.12.min.js`) {
		t.Fatalf("index does not use only vendored HTMX")
	}
	if !strings.Contains(html, `nonce="nonce-value"`) {
		t.Fatalf("inline application script lacks CSP nonce")
	}
	if !strings.Contains(html, `"allowEval":false`) || !strings.Contains(html, `"allowScriptTags":false`) {
		t.Fatalf("HTMX execution controls missing")
	}
	if !strings.Contains(html, `/static/app.js`) {
		t.Fatalf("external application script missing")
	}
}
