package mailcontent

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNotificationMIMEPreview(t *testing.T) {
	padding := strings.Repeat("\u034f\u200c&nbsp;", 400)
	hidden := "<div style=\"display: none; max-height: 0px; overflow:\n hidden\"><span>" + padding + "</span></div>"
	visible := "96 items &amp; receipt &#x1f642; <span>می‌خواهم</span> 👩‍💻"
	want := "96 items & receipt 🙂 می‌خواهم 👩‍💻"
	for _, tc := range []struct{ name, headers, body, want string }{
		{"html", "Content-Type: text/html; charset=utf-8", hidden + "<p>" + visible + "</p>", want},
		{"clipped padding", "Content-Type: text/html", "<div style='overflow:hidden;max-height:0px'>padding</div><p>Visible</p>", "Visible"},
		{"base64", "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64", base64.StdEncoding.EncodeToString([]byte(hidden + visible)), want},
		{"quoted-printable", "Content-Type: text/html; charset=windows-1252\r\nContent-Transfer-Encoding: quoted-printable", "<p>Caf=E9 &amp; =A396.00</p>", "Café & £96.00"},
		{"literal padding", "Content-Type: text/plain", strings.Repeat("&zwnj;&nbsp;&#847;&#x200c;&#160;", 400) + "Actual message", "Actual message"},
		{"plain preserved", "Content-Type: text/plain; charset=utf-8", "96 items <tag> &lt;tag&gt; می‌خواهم 👩‍💻", "96 items <tag> &lt;tag&gt; می‌خواهم 👩‍💻"},
		{"hidden descendants", "Content-Type: text/html", "<head>no</head><script>no</script><div hidden><b>no</b></div><span style='visibility:hidden!important'>no</span><span style='mso-hide:all'>no</span><p>Yes <span>please</span>.</p>", "Yes please."},
		{"alternative", "Content-Type: multipart/alternative; boundary=alt", "--alt\r\nContent-Type: text/plain\r\n\r\n" + padding + "\r\n--alt\r\nContent-Type: text/html\r\n\r\n" + hidden + visible + "\r\n--alt--\r\n", want},
		{"clean alternative", "Content-Type: multipart/alternative; boundary=alt", "--alt\r\nContent-Type: text/plain\r\n\r\nPlain text\r\n--alt\r\nContent-Type: text/html\r\n\r\n<b>HTML text</b>\r\n--alt--\r\n", "Plain text"},
		{"attachment excluded", "Content-Type: multipart/mixed; boundary=alt", "--alt\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nNo\r\n--alt\r\nContent-Type: text/plain\r\n\r\nBody\r\n--alt--\r\n", "Body"},
		{"HTML contaminated alternative", "Content-Type: multipart/alternative; boundary=alt", "--alt\r\nContent-Type: text/plain\r\n\r\n<div>junk markup</div>\r\n--alt\r\nContent-Type: text/html\r\n\r\n<p>Real text</p>\r\n--alt--\r\n", "Real text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.headers + "\r\n\r\n" + tc.body)
			before := string(raw)
			if got := PreviewText(raw); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if string(raw) != before {
				t.Fatal("raw message changed")
			}
		})
	}
}

func TestPreviewResourceBounds(t *testing.T) {
	raw := []byte("Content-Type: text/html\r\n\r\n" + strings.Repeat("<div>", 10000) + "deep")
	if got := PreviewText(raw); got != "" {
		t.Fatalf("depth bound = %q", got)
	}
	raw = []byte("Content-Type: text/plain\r\n\r\n" + strings.Repeat("x", previewBytes+100))
	if got := PreviewText(raw); len(got) != previewBytes {
		t.Fatalf("decoded bound = %d", len(got))
	}
	if got := PreviewText([]byte("not MIME")); got != "" {
		t.Fatal("invalid MIME preview")
	}
}
