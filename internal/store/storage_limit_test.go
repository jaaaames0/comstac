package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrateWithLimitAppliesSQLitePageCeilingToEveryConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "limited.db")
	const limit = int64(8 << 20)

	db, err := OpenAndMigrateWithLimit(ctx, path, limit)
	if err != nil {
		t.Fatal(err)
	}
	var pageSize, maxPages int64
	if err := db.QueryRowContext(ctx, `PRAGMA page_size;`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA max_page_count;`).Scan(&maxPages); err != nil {
		t.Fatal(err)
	}
	if got := maxPages * pageSize; got > limit {
		t.Fatalf("effective limit = %d, configured = %d", got, limit)
	}
	if stats := db.Stats(); stats.MaxOpenConnections != maxOpenConnections {
		t.Fatalf("max open connections = %d, want %d", stats.MaxOpenConnections, maxOpenConnections)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE storage_limit_probe (payload BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	full := false
	for i := 0; i < 16; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO storage_limit_probe(payload) VALUES (zeroblob(1048576))`); err != nil {
			full = true
			break
		}
	}
	if !full {
		t.Fatal("writes exceeded the configured SQLite page ceiling")
	}
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	for i, conn := range []*sql.Conn{conn1, conn2} {
		var connectionPages, busyTimeout int64
		if err := conn.QueryRowContext(ctx, `PRAGMA max_page_count;`).Scan(&connectionPages); err != nil {
			t.Fatal(err)
		}
		if connectionPages != maxPages {
			t.Fatalf("connection %d max_page_count = %d, want %d", i+1, connectionPages, maxPages)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout;`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if busyTimeout != sqliteBusyTimeoutMS {
			t.Fatalf("connection %d busy_timeout = %d, want %d", i+1, busyTimeout, sqliteBusyTimeoutMS)
		}
	}
}
