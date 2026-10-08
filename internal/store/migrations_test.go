package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func migrationFiles(t *testing.T, cutoff string) fstest.MapFS {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, e := range entries {
		if e.IsDir() || (cutoff != "" && e.Name() > cutoff) {
			continue
		}
		name := "migrations/" + e.Name()
		payload, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: payload}
	}
	return files
}

func migrationExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

var migrationRaw = []byte("Subject: Upgrade follow-up\r\n\r\nSynthetic migration fixture.")
var migrationOccurrence = time.Date(2026, 10, 6, 0, 45, 0, 0, time.UTC)

// All fixtures are synthetic. The schema itself comes from the original
// embedded SQL prefix, not a hand-maintained approximation or a live DB copy.
func seedMigrationData(t *testing.T, db *sql.DB, cutoff string) {
	t.Helper()
	migrationExec(t, db, `INSERT INTO accounts(id, name, kind, email_address) VALUES (1, 'Fixture', 'imap', 'fixture@example.test')`)
	migrationExec(t, db, `INSERT INTO threads(id, subject_norm) VALUES (1, 'upgrade follow-up')`)
	hash := sha256.Sum256(migrationRaw)
	migrationExec(t, db, `INSERT INTO messages_raw(id, source, account_id, remote_id, mime, mime_sha256) VALUES (1, 'imap', 1, '101', ?, ?)`, migrationRaw, fmt.Sprintf("%x", hash))
	migrationExec(t, db, `INSERT INTO messages(id, raw_id, account_id, thread_id, subject, from_addr, to_addr, read, archived, snooze_until) VALUES (1, 1, 1, 1, 'Upgrade follow-up', 'fixture@example.test', 'owner@example.test', 1, 1, '2026-10-09T00:00:00Z')`)
	if cutoff >= "0006" {
		migrationExec(t, db, `UPDATE messages SET body_text = 'preservedbody' WHERE id = 1`)
	}
	migrationExec(t, db, `INSERT INTO users(id, username, password_hash) VALUES (1, 'synthetic-user', 'synthetic-non-credential')`)
	migrationExec(t, db, `INSERT INTO sessions(token, user_id, expires_at_unix) VALUES ('synthetic-session', 1, 2000000000)`)
	migrationExec(t, db, `INSERT INTO imap_checkpoints(account_id, mailbox, last_uid) VALUES (1, 'INBOX', 101)`)
	migrationExec(t, db, `INSERT INTO outbound_messages(id, from_addr, to_addr, subject, raw_mime, in_reply_to) VALUES (1, 'owner@example.test', 'fixture@example.test', 'Fixture reply', ?, 1)`, migrationRaw)
	if cutoff >= "0013" {
		migrationExec(t, db, `INSERT INTO remote_image_senders(email_address) VALUES ('fixture@example.test')`)
	}
	if cutoff >= "0016" {
		migrationExec(t, db, `INSERT INTO calendar_events(id, title, start_local, end_local, tz, rrule, message_id, dedupe_key) VALUES (1, 'Fixture work', '2026-10-06T11:45', '2026-10-06T20:30', 'Australia/Sydney', 'FREQ=WEEKLY', 1, 'fixture/work')`)
		migrationExec(t, db, `INSERT INTO calendar_reminders(event_id, offset_minutes, created_at) VALUES (1, 60, '2026-09-01T00:00:00Z')`)
		migrationExec(t, db, `INSERT INTO calendar_reminder_fires(event_id, occurrence_start, offset_minutes) VALUES (1, ?, 60)`, migrationOccurrence.Format(time.RFC3339))
	}
	if cutoff >= "0017" {
		migrationExec(t, db, `INSERT INTO message_extractions(message_id, version, found) VALUES (1, 2, 1)`)
	}
	if cutoff >= "0018" {
		migrationExec(t, db, `INSERT INTO ai_extraction_runs(message_id, model, trigger, status, cost_microusd) VALUES (1, 'fake-model', 'manual', 'ok', 12)`)
	}
	if cutoff >= "0019" {
		migrationExec(t, db, `INSERT INTO message_event_refs(message_id, dedupe_key, ai) VALUES (1, 'fixture/work', 0)`)
	}
	if cutoff >= "0021" {
		migrationExec(t, db, `INSERT INTO contacts(id,name,email,address_key) VALUES (1,'Saved fixture','Local@Example.COM','Local@example.com')`)
	}
}

func migrationLedger(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT version, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ledger := map[string]string{}
	for rows.Next() {
		var version, applied string
		if err := rows.Scan(&version, &applied); err != nil {
			t.Fatal(err)
		}
		ledger[version] = applied
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func assertMigrationData(t *testing.T, db *sql.DB, cutoff string) {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	var subject, body, snooze string
	var read, archived, account int
	if err := db.QueryRow(`SELECT mr.mime, m.subject, m.body_text, m.read, m.archived, m.snooze_until, m.account_id FROM messages m JOIN messages_raw mr ON mr.id = m.raw_id WHERE m.id = 1`).Scan(&raw, &subject, &body, &read, &archived, &snooze, &account); err != nil {
		t.Fatal(err)
	}
	wantBody := "preservedbody"
	if cutoff < "0006" {
		wantBody = ""
	}
	if !bytes.Equal(raw, migrationRaw) || subject != "Upgrade follow-up" || body != wantBody || read != 1 || archived != 1 || snooze != "2026-10-09T00:00:00Z" || account != 1 {
		t.Fatalf("mail changed: %q %q %q %d %d %q %d", raw, subject, body, read, archived, snooze, account)
	}
	if uid, found, err := GetIMAPCheckpoint(ctx, db, 1, "INBOX"); err != nil || !found || uid != 101 {
		t.Fatalf("checkpoint = %d %t %v", uid, found, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM users WHERE id = 1 AND username = 'synthetic-user' AND password_hash = 'synthetic-non-credential'`,
		`SELECT count(*) FROM sessions WHERE token = 'synthetic-session' AND user_id = 1 AND expires_at_unix = 2000000000`,
		`SELECT count(*) FROM threads WHERE id = 1 AND subject_norm = 'upgrade follow-up'`,
		`SELECT count(*) FROM accounts WHERE id = 1 AND kind = 'imap' AND email_address = 'fixture@example.test'`,
		`SELECT count(*) FROM outbound_messages WHERE id = 1 AND subject = 'Fixture reply' AND in_reply_to = 1 AND status = 'sent'`,
	} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil || n != 1 {
			t.Fatalf("retention query %q = %d, %v", query, n, err)
		}
	}
	items, err := SearchMessages(ctx, db, "follow-up", 10)
	if err != nil || len(items) != 1 || items[0].ID != 1 {
		t.Fatalf("historical FTS search = %+v, %v", items, err)
	}
	if cutoff >= "0006" {
		items, err = SearchMessages(ctx, db, "preservedbody", 10)
		if err != nil || len(items) != 1 || items[0].ID != 1 {
			t.Fatalf("body FTS search = %+v, %v", items, err)
		}
	}
	if cutoff >= "0013" {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM remote_image_senders WHERE email_address = 'fixture@example.test'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("sender preference = %d %v", n, err)
		}
	}
	if cutoff >= "0016" {
		ev, err := GetCalendarEvent(ctx, db, 1)
		if err != nil || ev == nil {
			t.Fatalf("calendar event = %+v %v", ev, err)
		}
		if ev.StartLocal != "2026-10-06T11:45" || ev.EndLocal != "2026-10-06T20:30" || ev.TZ != "Australia/Sydney" || ev.RRule != "FREQ=WEEKLY" || ev.MessageID != 1 || ev.DedupeKey != "fixture/work" || len(ev.Reminders) != 1 || ev.Reminders[0].OffsetMinutes != 60 || ev.Reminders[0].CreatedAt.Format(time.RFC3339) != "2026-09-01T00:00:00Z" {
			t.Fatalf("calendar state changed: %+v", ev)
		}
		if ok, err := ClaimCalendarReminder(ctx, db, 1, migrationOccurrence, 60); err != nil || ok {
			t.Fatalf("existing reminder reclaimed: %t %v", ok, err)
		}
	}
	if cutoff >= "0021" {
		c, err := GetContact(ctx, db, 1)
		if err != nil || c.Name != "Saved fixture" || c.Email != "Local@Example.COM" {
			t.Fatalf("contact retention: %+v %v", c, err)
		}
	}
	queries := map[string]string{
		"0017": `SELECT count(*) FROM message_extractions WHERE message_id = 1 AND version = 2 AND found = 1`,
		"0018": `SELECT count(*) FROM ai_extraction_runs WHERE message_id = 1 AND status = 'ok' AND cost_microusd = 12`,
		"0019": `SELECT count(*) FROM message_event_refs WHERE message_id = 1 AND dedupe_key = 'fixture/work' AND ai = 0`,
	}
	for version, query := range queries {
		if cutoff < version {
			continue
		}
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s state = %d %v", version, n, err)
		}
	}
}

func TestMigrationFreshInstallAndPopulatedUpgrades(t *testing.T) {
	for _, cutoff := range []string{"fresh", "0005_outbound.sql", "0013_remote_image_senders.sql", "0017_calendar_suggestions.sql", "0019_message_event_refs.sql", "0020_notification_snoozes.sql", "0021_contacts.sql"} {
		t.Run(cutoff, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			oldLedger := map[string]string{}
			dataVersion := cutoff
			if cutoff != "fresh" {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				if err := runMigrationsFrom(ctx, db, migrationFiles(t, cutoff)); err != nil {
					t.Fatal(err)
				}
				seedMigrationData(t, db, cutoff)
				oldLedger = migrationLedger(t, db)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			// Exercise the actual production opening path, including size policy.
			db, err := OpenAndMigrateWithLimit(ctx, path, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if cutoff == "fresh" {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("fresh mail = %d %v", n, err)
				}
				dataVersion = "0021"
				seedMigrationData(t, db, dataVersion)
			}
			ledger := migrationLedger(t, db)
			files := migrationFiles(t, "")
			if len(ledger) != len(files) {
				t.Fatalf("ledger contains %d migrations; want %d", len(ledger), len(files))
			}
			for name := range files {
				if _, ok := ledger[strings.TrimPrefix(name, "migrations/")]; !ok {
					t.Fatalf("missing migration %s", name)
				}
			}
			for name, applied := range oldLedger {
				if ledger[name] != applied {
					t.Fatalf("old ledger entry changed: %s", name)
				}
			}
			assertMigrationData(t, db, dataVersion)
			added := Contact{Name: "Post-upgrade fixture", Email: "Added@Example.COM"}
			if err := SaveContact(ctx, db, &added); err != nil {
				t.Fatal(err)
			}
			// Latest notification schema accepts actions for retained fire claims.
			if dataVersion >= "0016" {
				for i := 0; i < 2; i++ {
					if ok, err := SnoozeCalendarNotification(ctx, db, 1, migrationOccurrence, "offset:60", migrationOccurrence.Add(-time.Hour)); err != nil || !ok {
						t.Fatalf("retained reminder snooze = %t %v", ok, err)
					}
				}
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM calendar_notification_snoozes`).Scan(&n); err != nil || n != 1 {
					t.Fatalf("notification identity = %d %v", n, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenAndMigrateWithLimit(ctx, path, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			if !reflect.DeepEqual(ledger, migrationLedger(t, reopened)) {
				t.Fatal("reopen changed migration ledger")
			}
			assertMigrationData(t, reopened, dataVersion)
			retained, err := GetContact(ctx, reopened, added.ID)
			if err != nil || *retained != added {
				t.Fatalf("new contact after upgrade/reopen: %+v %v", retained, err)
			}
			// Verify FTS triggers still work on updates after upgrade and reopen.
			migrationExec(t, reopened, `UPDATE messages SET body_text = 'updatedsearchterm' WHERE id = 1`)
			items, err := SearchMessages(ctx, reopened, "updatedsearchterm", 10)
			if err != nil || len(items) != 1 || items[0].ID != 1 {
				t.Fatalf("updated FTS = %+v %v", items, err)
			}
		})
	}
}

func TestMigrationFailureRollsBackPayloadAndLedger(t *testing.T) {
	for _, failure := range []string{"payload", "ledger"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			db, err := OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "rollback.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			before := migrationLedger(t, db)
			migrationExec(t, db, `CREATE TABLE migration_probe(value TEXT); INSERT INTO migration_probe VALUES ('before')`)
			payload := `CREATE TABLE failed_migration(value TEXT); UPDATE migration_probe SET value = 'after';`
			files := fstest.MapFS{
				"migrations/0021_previous.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE previous_migration(value TEXT)`)},
				"migrations/0022_failure.sql":  &fstest.MapFile{Data: []byte(payload)},
			}
			if failure == "payload" {
				files["migrations/0022_failure.sql"].Data = []byte(payload + `INSERT INTO nonexistent_table VALUES (1);`)
			} else {
				migrationExec(t, db, `CREATE TRIGGER fail_ledger BEFORE INSERT ON schema_migrations WHEN NEW.version = '0022_failure.sql' BEGIN SELECT RAISE(FAIL, 'injected ledger failure'); END`)
			}
			if err := runMigrationsFrom(ctx, db, files); err == nil || !strings.Contains(err.Error(), "0022_failure.sql") {
				t.Fatalf("migration error = %v", err)
			}
			ledger := migrationLedger(t, db)
			if len(ledger) != len(before)+1 || ledger["0021_previous.sql"] == "" || ledger["0022_failure.sql"] != "" {
				t.Fatalf("failure ledger = %v", ledger)
			}
			for version, at := range before {
				if ledger[version] != at {
					t.Fatalf("earlier migration changed: %s", version)
				}
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'failed_migration'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("DDL not rolled back: %d %v", n, err)
			}
			var value string
			if err := db.QueryRow(`SELECT value FROM migration_probe`).Scan(&value); err != nil || value != "before" {
				t.Fatalf("DML not rolled back: %q %v", value, err)
			}
			if failure == "ledger" {
				migrationExec(t, db, `DROP TRIGGER fail_ledger`)
			}
			files["migrations/0022_failure.sql"].Data = []byte(payload)
			if err := runMigrationsFrom(ctx, db, files); err != nil {
				t.Fatalf("corrected migration cannot retry: %v", err)
			}
			if err := db.QueryRow(`SELECT value FROM migration_probe`).Scan(&value); err != nil || value != "after" {
				t.Fatalf("retry did not commit: %q %v", value, err)
			}
			if got := migrationLedger(t, db); len(got) != len(before)+2 || got["0022_failure.sql"] == "" {
				t.Fatalf("retry ledger = %v", got)
			}
		})
	}
}
