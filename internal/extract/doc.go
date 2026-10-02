package extract

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// doc is the parsed form of a message body that extractors work on.
type doc struct {
	// Lines are the visible text lines; table cells on one row are joined
	// with " | ".
	Lines []string
	// ByID maps element ids to their normalized visible text.
	ByID map[string]string
	// Tables holds every table's rows of cells.
	Tables [][][]cell
	// LDJSON holds the contents of application/ld+json scripts.
	LDJSON []string
}

type cell struct {
	Text  string
	Lines []string // the cell's text split at <br> and block boundaries
}

var (
	reSpace    = regexp.MustCompile(`[ \t\f\v\x{a0}\x{2007}\x{202f}]+`)
	invisible  = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "", "\u00ad", "", "\u034f", "")
	blockAtoms = map[atom.Atom]bool{}
	skipAtoms  = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true, atom.Template: true}
	blockNames = []atom.Atom{atom.P, atom.Div, atom.Tr, atom.Table, atom.Li, atom.Ul, atom.Ol, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Center, atom.Blockquote, atom.Pre, atom.Hr, atom.Tbody, atom.Thead}
)

func init() {
	for _, a := range blockNames {
		blockAtoms[a] = true
	}
}

func normalize(s string) string {
	s = invisible.Replace(s)
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = normalize(l); l != "" && strings.Trim(l, "| ") != "" {
			out = append(out, strings.Trim(l, "| "))
		}
	}
	return out
}

// parseDoc builds a doc from HTML, or from plain text when html is empty.
func parseDoc(htmlSrc, text string) *doc {
	d := &doc{ByID: map[string]string{}}
	if strings.TrimSpace(htmlSrc) == "" {
		d.Lines = splitLines(text)
		return d
	}
	root, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		d.Lines = splitLines(text)
		return d
	}
	var b strings.Builder
	renderNode(root, &b, d)
	d.Lines = splitLines(b.String())
	collectTables(root, d)
	collectLDJSON(root, d)
	return d
}

// collectLDJSON gathers application/ld+json scripts anywhere in the
// document, including <head>.
func collectLDJSON(n *html.Node, d *doc) {
	if n.Type == html.ElementNode && n.DataAtom == atom.Script && strings.Contains(strings.ToLower(attr(n, "type")), "ld+json") {
		var sb strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				sb.WriteString(c.Data)
			}
		}
		d.LDJSON = append(d.LDJSON, sb.String())
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectLDJSON(c, d)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// renderNode writes visible text with line breaks at block boundaries and
// " | " between table cells, recording element ids along the way.
func renderNode(n *html.Node, b *strings.Builder, d *doc) {
	if n.Type == html.ElementNode {
		if skipAtoms[n.DataAtom] {
			return
		}
		switch {
		case n.DataAtom == atom.Br:
			b.WriteString("\n")
			return
		case n.DataAtom == atom.Td || n.DataAtom == atom.Th:
			b.WriteString(" | ")
		case blockAtoms[n.DataAtom]:
			b.WriteString("\n")
		}
		if id := attr(n, "id"); id != "" {
			if _, seen := d.ByID[id]; !seen {
				d.ByID[id] = normalize(strings.ReplaceAll(textOf(n), "\n", " "))
			}
		}
	}
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(c, b, d)
	}
	if n.Type == html.ElementNode && blockAtoms[n.DataAtom] {
		b.WriteString("\n")
	}
}

// textOf returns n's visible text with newlines at <br> and block boundaries.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipAtoms[n.DataAtom] {
				return
			}
			if n.DataAtom == atom.Br || blockAtoms[n.DataAtom] {
				b.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockAtoms[n.DataAtom] {
			b.WriteString("\n")
		}
	}
	walk(n)
	return b.String()
}

// collectTables records each table's rows; a row belongs to its nearest
// table ancestor and its cells are its direct td/th children.
func collectTables(n *html.Node, d *doc) {
	if n.Type == html.ElementNode && n.DataAtom == atom.Table {
		var rows [][]cell
		var walkRows func(*html.Node)
		walkRows = func(p *html.Node) {
			for c := p.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.ElementNode {
					continue
				}
				switch c.DataAtom {
				case atom.Table:
					continue // nested table: collected separately
				case atom.Tr:
					var row []cell
					for td := c.FirstChild; td != nil; td = td.NextSibling {
						if td.Type == html.ElementNode && (td.DataAtom == atom.Td || td.DataAtom == atom.Th) {
							lines := splitLines(textOf(td))
							row = append(row, cell{Text: strings.Join(lines, " "), Lines: lines})
						}
					}
					rows = append(rows, row)
				default:
					walkRows(c)
				}
			}
		}
		walkRows(n)
		d.Tables = append(d.Tables, rows)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectTables(c, d)
	}
}
