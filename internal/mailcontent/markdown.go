package mailcontent

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToMarkdown renders an email's HTML body as plain Markdown for pasting
// into a language model: visible text only, links kept as [text](url),
// headings and list items marked, table cells on one row joined with " | ".
// Emphasis is dropped: layout emails wrap whole blocks in <b> and the
// markers would not nest. Hidden preheaders and tracking images are skipped.
func HTMLToMarkdown(src string) string {
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}
	var b strings.Builder
	mdNode(root, &b, 0)
	return tidyMarkdown(b.String())
}

var mdBlock = map[atom.Atom]bool{
	atom.Div: true, atom.Tr: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Center: true, atom.Tbody: true, atom.Thead: true, atom.Tfoot: true,
	atom.Main: true, atom.Nav: true, atom.Aside: true, atom.Figure: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
}

// mdPara elements are separated from their neighbours by a blank line.
var mdPara = map[atom.Atom]bool{atom.P: true, atom.Table: true, atom.Ul: true, atom.Ol: true, atom.Blockquote: true, atom.Pre: true}

func mdNode(n *html.Node, b *strings.Builder, depth int) {
	if depth > 200 {
		return
	}
	switch n.Type {
	case html.TextNode:
		b.WriteString(collapseSpace(n.Data))
		return
	case html.ElementNode:
	case html.DocumentNode:
		mdChildren(n, b, depth)
		return
	default:
		return
	}
	if mdHidden(n) {
		return
	}
	switch n.DataAtom {
	case atom.Br:
		b.WriteString("\n")
	case atom.Hr:
		b.WriteString("\n\n---\n\n")
	case atom.Img:
		if alt := strings.TrimSpace(mdAttr(n, "alt")); alt != "" && !insideLink(n) {
			b.WriteString("[image: " + collapseSpace(alt) + "]")
		}
	case atom.A:
		mdLink(n, b, depth)
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		level, _ := strconv.Atoi(n.Data[1:])
		if text := inlineText(n, depth); text != "" {
			b.WriteString("\n\n" + strings.Repeat("#", level) + " " + text + "\n\n")
		}
	case atom.Li:
		marker := "- "
		if p := n.Parent; p != nil && p.DataAtom == atom.Ol {
			i := 1
			for s := n.PrevSibling; s != nil; s = s.PrevSibling {
				if s.Type == html.ElementNode && s.DataAtom == atom.Li {
					i++
				}
			}
			marker = strconv.Itoa(i) + ". "
		}
		var inner strings.Builder
		mdChildren(n, &inner, depth)
		if text := tidyMarkdown(inner.String()); text != "" {
			if cur := b.String(); cur != "" && !strings.HasSuffix(cur, "\n") {
				b.WriteString("\n")
			}
			b.WriteString(marker + strings.ReplaceAll(text, "\n", "\n   ") + "\n")
		}
	case atom.Td, atom.Th:
		b.WriteString(" | ")
		mdChildren(n, b, depth)
	case atom.Pre:
		b.WriteString("\n\n" + strings.TrimRight(rawText(n), "\n") + "\n\n")
	case atom.Blockquote:
		var inner strings.Builder
		mdChildren(n, &inner, depth)
		b.WriteString("\n\n")
		for _, line := range strings.Split(tidyMarkdown(inner.String()), "\n") {
			b.WriteString("> " + line + "\n")
		}
		b.WriteString("\n")
	default:
		sep := ""
		if mdPara[n.DataAtom] {
			sep = "\n\n"
		} else if mdBlock[n.DataAtom] {
			sep = "\n"
		}
		b.WriteString(sep)
		mdChildren(n, b, depth)
		b.WriteString(sep)
	}
}

func mdChildren(n *html.Node, b *strings.Builder, depth int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		mdNode(c, b, depth+1)
	}
}

// mdLink writes [text](href), or <href> when the text is the address. Links
// with no visible text fall back to an image's alt text, then are dropped.
func mdLink(n *html.Node, b *strings.Builder, depth int) {
	href := strings.TrimSpace(mdAttr(n, "href"))
	text := inlineText(n, depth)
	if text == "" {
		text = imageAlt(n)
	}
	lower := strings.ToLower(href)
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(lower, "javascript:") {
		b.WriteString(text)
		return
	}
	if text == "" {
		return
	}
	bare := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(href, "mailto:"), "https://"), "http://")
	if text == href || text == bare || text == strings.TrimSuffix(bare, "/") {
		b.WriteString("<" + href + ">")
		return
	}
	b.WriteString("[" + strings.NewReplacer("[", "(", "]", ")").Replace(text) + "](" + strings.ReplaceAll(href, " ", "%20") + ")")
}

// inlineText renders n's children onto one line.
func inlineText(n *html.Node, depth int) string {
	var sb strings.Builder
	mdChildren(n, &sb, depth)
	return strings.Join(strings.Fields(invisibleChars.Replace(strings.ReplaceAll(sb.String(), "|", " "))), " ")
}

func imageAlt(n *html.Node) string {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Img {
			if alt := collapseSpace(strings.TrimSpace(mdAttr(c, "alt"))); alt != "" {
				return alt
			}
		}
		if alt := imageAlt(c); alt != "" {
			return alt
		}
	}
	return ""
}

func insideLink(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.DataAtom == atom.A {
			return true
		}
	}
	return false
}

func rawText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Br {
			sb.WriteString("\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func mdHidden(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Title, atom.Head, atom.Script, atom.Style, atom.Template, atom.Noscript:
		return true
	}
	return hiddenElement(html.Token{Type: html.StartTagToken, Data: n.Data, Attr: n.Attr})
}

func mdAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

var (
	reMDSpace      = regexp.MustCompile(`[ \t\r\n\f\v\x{a0}\x{2007}\x{202f}]+`)
	invisibleChars = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "", "\u00ad", "", "\u034f", "")
)

func collapseSpace(s string) string { return reMDSpace.ReplaceAllString(s, " ") }

// tidyMarkdown trims each line, strips table-cell separators left at line
// edges by layout tables, and keeps at most one blank line in a row.
func tidyMarkdown(s string) string {
	var out []string
	blank := true
	for _, line := range strings.Split(invisibleChars.Replace(s), "\n") {
		line = strings.TrimSpace(collapseSpace(line))
		line = strings.TrimSpace(strings.Trim(line, "| "))
		for strings.Contains(line, "|  |") || strings.Contains(line, "| |") {
			line = strings.ReplaceAll(strings.ReplaceAll(line, "|  |", "|"), "| |", "|")
		}
		if line == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
