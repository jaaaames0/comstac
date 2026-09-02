package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	maxOpenConnections  = 8
	maxIdleConnections  = 4
	sqliteBusyTimeoutMS = 5000
)

// OpenAndMigrate initializes SQLite and executes embedded migrations.
func OpenAndMigrate(ctx context.Context, path string) (*sql.DB, error) {
	return OpenAndMigrateWithLimit(ctx, path, 0)
}

// OpenAndMigrateWithLimit initializes SQLite with an absolute database-size
// ceiling. A zero limit is retained for tests and development helpers that do
// not run the long-lived server.
func OpenAndMigrateWithLimit(ctx context.Context, path string, maxBytes int64) (*sql.DB, error) {
	dsn := path
	var maxPages int64
	if maxBytes > 0 {
		pageSize, err := sqlitePageSize(ctx, path)
		if err != nil {
			return nil, err
		}
		if pageSize <= 0 || maxBytes < pageSize {
			return nil, fmt.Errorf("database byte limit is smaller than one SQLite page")
		}
		maxPages = maxBytes / pageSize
		dsn = sqliteDSN(path, maxPages)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if maxBytes > 0 {
		if err := verifyMaxDatabasePages(ctx, db, maxPages); err != nil {
			db.Close()
			return nil, err
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set wal mode: %w", err)
	}

	if err := runMigrations(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func sqlitePageSize(ctx context.Context, path string) (int64, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var pageSize int64
	if err := db.QueryRowContext(ctx, `PRAGMA page_size;`).Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("read sqlite page size: %w", err)
	}
	return pageSize, nil
}

func sqliteDSN(path string, maxPages int64) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "max_page_count("+strconv.FormatInt(maxPages, 10)+")")
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(sqliteBusyTimeoutMS)+")")
	u.RawQuery = q.Encode()
	return u.String()
}

func verifyMaxDatabasePages(ctx context.Context, db *sql.DB, maxPages int64) error {
	var effectivePages int64
	if err := db.QueryRowContext(ctx, `PRAGMA max_page_count;`).Scan(&effectivePages); err != nil {
		return fmt.Errorf("read sqlite page limit: %w", err)
	}
	// SQLite refuses to lower max_page_count below the current page count. That
	// behavior must not silently weaken the configured ceiling.
	if effectivePages > maxPages {
		return fmt.Errorf("existing database exceeds configured byte limit")
	}
	return nil
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version := entry.Name()

		var exists int
		if err := db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if exists == 1 {
			continue
		}

		payload, err := migrationsFS.ReadFile("migrations/" + version)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration tx %s: %w", version, err)
		}

		if _, err := tx.ExecContext(ctx, string(payload)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", version, err)
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
	}

	return nil
}
