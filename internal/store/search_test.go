package store_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

func TestSearchMessagesTreatsInputAsText(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, subject := range []string{
		`Quarterly follow-up invoice ref:ABC-123 C++ OR 日本語`,
		`Follow unrelated up quarterly`,
		`Unrelated message`,
	} {
		raw := "From: billing-team@example.com\r\nTo: local@example.com\r\nSubject: " + subject +
			"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPlease review the attachment.\r\n"
		if err := ingest.NewService(db).IngestRaw(ctx, ingest.IngestInput{
			Source: ingest.SourceSMTP, EnvelopeFrom: "billing-team@example.com",
			EnvelopeTo: []string{"local@example.com"}, RawMIME: []byte(raw),
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"follow-up", []int64{1}},
		{"-follow-up", []int64{1}},
		{"follow up", []int64{1, 2}},
		{"invoice quarterly", []int64{1}},
		{"  invoice\tquarterly\n", []int64{1}},
		{"billing-team@example.com", []int64{1, 2, 3}},
		{"ref:ABC-123", []int64{1}},
		{"C++", []int64{1}},
		{`"follow-up`, []int64{1}},
		{`"follow-up"`, []int64{1}},
		{"(follow-up)", []int64{1}},
		{"OR", []int64{1}},
		{"日本語", []int64{1}},
		{"attachment", []int64{1, 2, 3}},
		{"missing-term", nil},
		{"-", nil},
		{`"`, nil},
		{"***", nil},
		{"", nil},
		{" \t\n", nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			items, err := store.SearchMessages(ctx, db, tc.query, 50)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, item := range items {
				ids = append(ids, item.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("search %s: IDs = %v, want %v", strings.TrimSpace(tc.query), ids, tc.want)
			}
		})
	}
}
