package mailcontent

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdown(t *testing.T) {
	src := `<html><head><title>x</title><style>p{}</style></head><body>
<div style="display:none;max-height:0;overflow:hidden">hidden preheader</div>
<table><tr><td><h1>Your  booking</h1></td></tr>
<tr><td><p>Hi James,<br>your flight is confirmed.</p></td></tr>
<tr><td>Flight</td><td>JQ761</td><td>06:10</td></tr></table>
<p>Manage it <a href="https://example.com/m?a=1&amp;b=2">here</a> or visit <a href="https://example.com">example.com</a>.</p>
<a href="https://example.com/logo"><img src="l.png" alt="Example logo"></a>
<img src="https://t.example/pixel.gif" width="1" height="1">
<ul><li>one</li><li><p>two</p></li></ul>
<ol><li>first</li><li>second</li></ol>
<blockquote>quoted<br>text</blockquote>
<a href="javascript:alert(1)">bad</a>
</body></html>`
	got := HTMLToMarkdown(src)
	for _, want := range []string{
		"# Your booking",
		"Hi James,\nyour flight is confirmed.",
		"Flight | JQ761 | 06:10",
		"Manage it [here](https://example.com/m?a=1&b=2) or visit <https://example.com>.",
		"[Example logo](https://example.com/logo)",
		"- one\n- two",
		"1. first\n2. second",
		"> quoted\n> text",
		"bad",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"hidden preheader", "pixel", "javascript:", "p{}", "\n\n\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, got)
		}
	}
}
