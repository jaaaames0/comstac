package store_test

import (
	"context"
	"errors"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"

	"comstac/internal/store"
)

func TestContactsPersistenceValidationAndDuplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "contacts.db")
	db, err := store.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := store.Contact{Name: `Doe, José "JJ"`, Email: "Local.Part@Example.COM"}
	if err := store.SaveContact(ctx, db, &c); err != nil {
		t.Fatal(err)
	}
	duplicate := store.Contact{Name: "Other", Email: "Local.Part@example.com"}
	if err := store.SaveContact(ctx, db, &duplicate); !errors.Is(err, store.ErrDuplicateContact) {
		t.Fatalf("duplicate: %v", err)
	}
	other := store.Contact{Email: "local.part@example.com"}
	if err := store.SaveContact(ctx, db, &other); err != nil {
		t.Fatal("local part was case folded", err)
	}
	a, err := mail.ParseAddress(c.Recipient())
	if err != nil || a.Name != c.Name || a.Address != c.Email {
		t.Fatalf("recipient round trip: %+v %v", a, err)
	}
	c.Name = "Updated"
	if err := store.SaveContact(ctx, db, &c); err != nil {
		t.Fatal(err)
	}
	other.Email = c.Email
	if err := store.SaveContact(ctx, db, &other); !errors.Is(err, store.ErrDuplicateContact) {
		t.Fatal("update duplicate", err)
	}
	for _, invalid := range []store.Contact{{Email: "bad"}, {Email: "a@example.com,b@example.com"}, {Email: "Name <a@example.com>"}, {Email: "a@example.com\r\nBcc: b@example.com"}, {Email: "a@example.com", Name: "bad\x00name"}, {Email: "a@example.com", Name: strings.Repeat("x", 201)}} {
		if err := store.SaveContact(ctx, db, &invalid); err == nil {
			t.Fatal("invalid contact accepted")
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetContact(ctx, db, c.ID)
	if err != nil || got.Name != "Updated" || got.Email != c.Email {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	if err := store.DeleteContact(ctx, db, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetContact(ctx, db, c.ID); !errors.Is(err, store.ErrContactNotFound) {
		t.Fatal("deleted contact exists", err)
	}
	if err := store.DeleteContact(ctx, db, c.ID); !errors.Is(err, store.ErrContactNotFound) {
		t.Fatal("missing delete", err)
	}
}

func TestContactsSearchLimitsAndLiteralPatterns(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"100%_saved", "100XaSaved", "Other"} {
		c := store.Contact{Name: name, Email: name + "@example.com"}
		if err := store.SaveContact(ctx, db, &c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.SearchContacts(ctx, db, "%_", 10, 0)
	if err != nil || len(got) != 1 || got[0].Name != "100%_saved" {
		t.Fatalf("literal search: %+v %v", got, err)
	}
	first, _ := store.SearchContacts(ctx, db, "", 1, 0)
	second, _ := store.SearchContacts(ctx, db, "", 1, 1)
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatal("pagination failed")
	}
	for _, args := range []struct {
		q    string
		l, o int
	}{{"", 0, 0}, {"", 52, 0}, {"", 10, -1}, {strings.Repeat("x", 129), 10, 0}} {
		if _, err := store.SearchContacts(ctx, db, args.q, args.l, args.o); err == nil {
			t.Fatal("unbounded search")
		}
	}
}
