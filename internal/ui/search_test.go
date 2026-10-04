package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/store"
	"golang.org/x/net/html"
)

func TestSearchResponsesPreserveListTarget(t *testing.T) {
	db, err := store.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mux := http.NewServeMux()
	RegisterRoutes(mux, db, nil, nil)

	for _, tc := range []struct {
		name, query, message string
		brokenDB             bool
	}{
		{"no results", "missing-term", "no messages", false},
		{"empty query", " \t", "Enter a search term.", false},
		{"database failure", "invoice", "Search temporarily unavailable.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.brokenDB {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/ui/search?q="+url.QueryEscape(tc.query), nil)
			req.Header.Set("HX-Request", "true")
			mux.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), tc.message) {
				t.Fatalf("search response = %d %s", resp.Code, resp.Body.String())
			}
			// outerHTML swaps must leave exactly one root with the same ID so
			// the next inbox/brand request still targets the message list.
			root, err := html.Parse(strings.NewReader(resp.Body.String()))
			if err != nil {
				t.Fatal(err)
			}
			var body *html.Node
			var findBody func(*html.Node)
			findBody = func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "body" {
					body = n
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					findBody(child)
				}
			}
			findBody(root)
			count := 0
			for child := body.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.ElementNode {
					continue
				}
				count++
				id := ""
				for _, attr := range child.Attr {
					if attr.Key == "id" {
						id = attr.Val
					}
				}
				if id != "message-list" {
					t.Fatalf("search response root ID = %q, want message-list", id)
				}
			}
			if count != 1 {
				t.Fatalf("search response has %d roots, want 1", count)
			}
		})
	}
}
