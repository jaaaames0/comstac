package mailcontent

import (
	"bytes"
	"encoding/base64"
	stdhtml "html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const previewBytes = 1 << 20

// PreviewText extracts notification text only. It never changes stored bodies,
// fetches external resources or treats a plain-text message as HTML. Bounds
// include the raw message, decoded text budget, MIME depth/parts and HTML depth.
func PreviewText(raw []byte) string {
	if int64(len(raw)) > MaxMessageBytes {
		return ""
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	p := previewReader{remaining: previewBytes}
	p.read(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), "", msg.Body, 0)
	plain := cleanPadding(strings.Join(p.plain, " "))
	visible := cleanPadding(strings.Join(p.html, " "))
	if visible != "" && (plain == "" || htmlTags.MatchString(plain)) {
		return visible
	}
	return plain
}

var htmlTags = regexp.MustCompile(`(?i)<(?:html|body|div|table|p|span)(?:\s|>)`)

type previewReader struct {
	remaining, parts int
	plain, html      []string
}

func (p *previewReader) read(contentType, encoding, disposition string, body io.Reader, depth int) {
	p.parts++
	if depth > 8 || p.parts > 100 || p.remaining <= 0 {
		return
	}
	disp, _, _ := mime.ParseMediaType(disposition)
	if disp == "attachment" {
		return
	}
	typ, params, err := mime.ParseMediaType(contentType)
	if contentType == "" {
		typ, err = "text/plain", nil
	}
	if err != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	}
	if strings.HasPrefix(typ, "multipart/") {
		if params["boundary"] == "" {
			return
		}
		mr := multipart.NewReader(body, params["boundary"])
		for p.parts < 100 && p.remaining > 0 {
			part, err := mr.NextRawPart()
			if err != nil {
				break
			}
			p.read(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part.Header.Get("Content-Disposition"), part, depth+1)
		}
		return
	}
	if typ != "text/plain" && typ != "text/html" {
		return
	}
	if label := params["charset"]; label != "" && !strings.EqualFold(label, "utf-8") && !strings.EqualFold(label, "us-ascii") {
		converted, err := charset.NewReaderLabel(label, body)
		if err != nil {
			return
		}
		body = converted
	}
	text, err := io.ReadAll(io.LimitReader(body, int64(p.remaining)))
	p.remaining -= len(text)
	if err != nil {
		return
	}
	if typ == "text/html" {
		p.html = append(p.html, visibleHTML(text))
	} else {
		p.plain = append(p.plain, string(text))
	}
}

func visibleHTML(src []byte) string {
	z := html.NewTokenizer(bytes.NewReader(src))
	z.SetMaxBuf(previewBytes)
	type frame struct {
		tag    string
		hidden bool
	}
	var stack []frame
	var out strings.Builder
	for {
		typ := z.Next()
		if typ == html.ErrorToken {
			break
		}
		hidden := len(stack) > 0 && stack[len(stack)-1].hidden
		switch typ {
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			hidden = hidden || hiddenElement(t)
			if len(stack) >= 128 {
				return out.String()
			}
			if typ == html.StartTagToken && !voidElement(t.Data) {
				stack = append(stack, frame{t.Data, hidden})
			}
			if !hidden && blockElement(t.Data) {
				out.WriteByte(' ')
			}
		case html.EndTagToken:
			t := z.Token()
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == t.Data {
					stack = stack[:i]
					break
				}
			}
			if !hidden && blockElement(t.Data) {
				out.WriteByte(' ')
			}
		case html.TextToken:
			if !hidden {
				out.Write(z.Text())
			}
		}
	}
	return out.String()
}

func hiddenElement(t html.Token) bool {
	switch t.Data {
	case "head", "script", "style", "template", "noscript":
		return true
	}
	for _, a := range t.Attr {
		if a.Key == "hidden" {
			return true
		}
		if a.Key != "style" {
			continue
		}
		zeroHeight, clipped := false, false
		for _, decl := range strings.Split(strings.ToLower(a.Val), ";") {
			key, value, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(strings.Split(value, "!")[0])
			switch strings.TrimSpace(key) {
			case "display":
				if value == "none" {
					return true
				}
			case "visibility":
				if value == "hidden" || value == "collapse" {
					return true
				}
			case "mso-hide":
				if value == "all" {
					return true
				}
			case "max-height", "height":
				if value == "0" || value == "0px" || value == "0pt" {
					zeroHeight = true
				}
			case "overflow":
				if value == "hidden" {
					clipped = true
				}
			}
		}
		if zeroHeight && clipped {
			return true
		}
	}
	return false
}

func voidElement(tag string) bool {
	return strings.Contains("|area|base|br|col|embed|hr|img|input|link|meta|param|source|track|wbr|", "|"+tag+"|")
}
func blockElement(tag string) bool {
	return strings.Contains("|div|p|br|hr|li|tr|td|table|section|article|h1|h2|h3|h4|h5|h6|", "|"+tag+"|")
}

// Decode only padding entities in plain text. Removing wordless padding tokens
// preserves joiners inside words and emoji sequences, and literal markup.
var paddingEntity = regexp.MustCompile(`(?i)&(?:zwnj|zwj|nbsp|shy|#(?:0*160|0*847|0*8203|0*8204|0*8205)|#x(?:0*a0|0*34f|0*200b|0*200c|0*200d));`)

func cleanPadding(s string) string {
	s = paddingEntity.ReplaceAllStringFunc(s, stdhtml.UnescapeString)
	var words []string
	for _, word := range strings.Fields(s) {
		padding := true
		for _, r := range word {
			if !unicode.IsSpace(r) && r != '\u034f' && r != '\u200b' && r != '\u200c' && r != '\u200d' && r != '\u00ad' && r != '\ufeff' {
				padding = false
				break
			}
		}
		if !padding {
			words = append(words, word)
		}
	}
	return strings.Join(words, " ")
}
