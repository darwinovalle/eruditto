// Package database manages the SQLite connection and schema migrations
// for Eruditto. It is the only package that speaks directly to the
// database file. Every other package that needs persistence goes through
// either this package (for the *DB handle) or internal/history (for
// clip-specific queries).
//
// Design decisions:
//
//  1. Single connection (MaxOpenConns=1).
//     SQLite serializes all writes at the file level. Having more than one
//     write connection from the same process does not improve throughput and
//     causes "database is locked" errors in WAL mode when two writers race.
//     One connection, one writer, zero locking surprises.
//
//  2. WAL journal mode.
//     WAL allows one writer and many concurrent readers without blocking
//     each other. Our clipboard monitor writes every ~500 ms while the UI
//     reads constantly — WAL is exactly the right fit.
//
//  3. Pure-Go driver (modernc.org/sqlite).
//     No CGO required for the database layer. CGO is only needed by Fyne
//     (OpenGL/X11). Keeping SQLite CGO-free simplifies cross-compilation
//     and makes the driver easier to audit.
//
//  4. Pragmas via DSN query parameters.
//     database/sql can open multiple physical connections even with
//     MaxOpenConns(1) during connection replacement. Setting pragmas
//     via the DSN ensures they are applied to every physical connection
//     the driver opens, not just the first one we happen to PRAGMA on.
package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	// Blank import registers modernc's pure-Go SQLite driver under
	// the name "sqlite". Do not use "sqlite3" — that is the CGO driver
	// from mattn/go-sqlite3. Mixing them in one binary causes panics.
	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB and owns the database file lifecycle.
//
// All repositories and services receive a *DB from the composition
// root (main.go) and call DB.SQL() to get the underlying *sql.DB for
// queries. We wrap rather than embed so we can add methods (Stats,
// Ping, Close) without polluting the *sql.DB namespace.
type DB struct {
	sql    *sql.DB
	path   string
	log    *slog.Logger
}

// Open opens (or creates) the SQLite database at path, applies all
// pending migrations, and returns a ready-to-use *DB.
//
// The caller is responsible for calling DB.Close() when the
// application shuts down. The idiomatic pattern in main.go is:
//
//	db, err := database.Open(path, log)
//	if err != nil { ... }
//	defer db.Close()
//
// Open fails fast: if the file cannot be opened, if a required pragma
// cannot be verified, or if any migration fails, it returns a non-nil
// error and leaves no resources open.
func Open(path string, log *slog.Logger) (*DB, error) {
	// ── 1. Ensure the parent directory exists ─────────────────────────
	// This handles both the default XDG path and any user-configured
	// override. MkdirAll is idempotent and safe to call every time.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("database: create parent dir %q: %w", dir, err)
	}

	// ── 2. Build the DSN with pragma overrides ────────────────────────
	//
	// Each _pragma=name(value) in the query string is executed as
	// "PRAGMA name = value" immediately after the physical connection
	// is established. This is the only reliable way to set pragmas when
	// using database/sql because the pool may replace connections.
	//
	// Pragma choices:
	//
	//   journal_mode=WAL
	//     Enable Write-Ahead Logging. Readers and writers no longer
	//     block each other. Essential for our concurrent read/write
	//     pattern (monitor writes, UI reads).
	//
	//   foreign_keys=ON
	//     SQLite disables FK enforcement by default for backwards
	//     compatibility. We always want it on.
	//
	//   busy_timeout=5000
	//     If a write is blocked by another writer, retry for up to
	//     5 seconds before returning SQLITE_BUSY. Without this, any
	//     brief write contention immediately errors.
	//
	//   synchronous=NORMAL
	//     With WAL mode, NORMAL is safe (transactions are durable after
	//     commit) and faster than FULL (which fsyncs on every commit).
	//
	//   cache_size=-32768
	//     Negative values are interpreted as KiB. -32768 = 32 MiB page
	//     cache. SQLite's default is 2 MiB, which is too small when
	//     searching 50 000 entries. The cache is per-connection and lives
	//     in process memory; 32 MiB is cheap on modern hardware.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode%%28WAL%%29&_pragma=foreign_keys%%28ON%%29&_pragma=busy_timeout%%285000%%29&_pragma=synchronous%%28NORMAL%%29&_pragma=cache_size%%28-32768%%29",
		path,
	)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("database: open %q: %w", path, err)
	}

	// ── 3. Connection pool tuning ─────────────────────────────────────
	//
	// MaxOpenConns(1): Only one physical connection at a time.
	// SQLite is an embedded database — it is not a server. Multiple
	// concurrent writers cause SQLITE_BUSY even in WAL mode (WAL allows
	// one writer at a time). With one connection the pool serialises
	// all operations through it, which is correct and predictable.
	//
	// MaxIdleConns(1): Keep the one connection alive in the idle pool
	// instead of closing it after each query. Reopening a connection
	// is not free (file open, pragma execution, page cache warm-up).
	//
	// SetConnMaxLifetime(0) / SetConnMaxIdleTime(0):
	// Never expire connections by age or idle time. We want the single
	// connection to live for the entire application lifetime so the WAL
	// mode, page cache, and prepared statement cache remain hot.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)

	db := &DB{
		sql:  sqlDB,
		path: path,
		log:  log,
	}

	// ── 4. Verify the connection is healthy ───────────────────────────
	if err := db.verifyPragmas(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database: pragma verification failed: %w", err)
	}

	// ── 5. Set file permissions on the database ───────────────────────
	// We do this after opening because SQLite creates the file on first
	// Open. Restricting to 0600 ensures only the owner can read it.
	// This is a best-effort security measure; it does not affect an
	// already-open file descriptor.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		log.Warn("database: could not set file permissions",
			"path", path, "error", err)
	}

	// ── 6. Run migrations ─────────────────────────────────────────────
	m := newMigrator(db, log)
	if err := m.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database: migrations failed: %w", err)
	}

	log.Info("database ready", "path", path)
	return db, nil
}

// SQL returns the underlying *sql.DB so repositories can execute
// queries. The name is deliberately short — it appears on every
// repository call site.
func (db *DB) SQL() *sql.DB {
	return db.sql
}

// Close closes the database connection. It should be called exactly
// once, after all goroutines that use the database have stopped.
//
// Typical usage in main.go:
//
//	defer db.Close()
func (db *DB) Close() error {
	db.log.Info("database closing", "path", db.path)
	if err := db.sql.Close(); err != nil {
		return fmt.Errorf("database: close: %w", err)
	}
	return nil
}

// verifyPragmas checks that the pragmas we set via the DSN actually
// took effect. We treat WAL mode as a hard requirement because
// the rest of the application is written under the assumption that
// reads and writes do not block each other.
func (db *DB) verifyPragmas() error {
	type pragmaCheck struct {
		name string
		want string
	}

	checks := []pragmaCheck{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
	}

	for _, c := range checks {
		var got string
		row := db.sql.QueryRow("PRAGMA " + c.name)
		if err := row.Scan(&got); err != nil {
			return fmt.Errorf("query PRAGMA %s: %w", c.name, err)
		}
		if got != c.want {
			return fmt.Errorf("PRAGMA %s = %q, want %q", c.name, got, c.want)
		}
		db.log.Debug("pragma verified", "name", c.name, "value", got)
	}

	return nil
}
