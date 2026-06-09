package database_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/darwinovalle/eruditto/internal/database"
)

// newTestDB opens a fresh database in a temporary directory and
// registers a cleanup function that closes it. Each test gets its
// own file so tests are fully isolated.
func newTestDB(t *testing.T) *database.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError, // suppress noise in test output
	}))

	db, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})
	return db
}

// queryPragma executes "PRAGMA <name>" and returns the result as a
// string. It is a test-only helper — production code uses verifyPragmas
// inside Open.
func queryPragma(t *testing.T, db *database.DB, name string) string {
	t.Helper()
	var val string
	row := db.SQL().QueryRow("PRAGMA " + name)
	if err := row.Scan(&val); err != nil {
		t.Fatalf("query PRAGMA %s: %v", name, err)
	}
	return val
}

// ── Open / Close ──────────────────────────────────────────────────────────────

func TestOpen_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eruditto.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file not created at %q: %v", path, err)
	}
}

func TestOpen_CreatesParentDirectory(t *testing.T) {
	// Open must create the parent dir if it does not exist.
	base := t.TempDir()
	path := filepath.Join(base, "nested", "dir", "test.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory not created: %v", err)
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	// Opening the same database file twice (sequentially) must not
	// error. The second Open sees existing migrations and skips them.
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db1, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	db2, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
}

func TestClose_IsClean(t *testing.T) {
	db := newTestDB(t)
	// t.Cleanup already calls Close; we call it manually here to
	// verify the return value (t.Cleanup cannot inspect it).
	if err := db.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ── Pragma assertions ─────────────────────────────────────────────────────────

func TestPragma_WALMode(t *testing.T) {
	db := newTestDB(t)
	got := queryPragma(t, db, "journal_mode")
	if got != "wal" {
		t.Errorf("journal_mode = %q, want %q", got, "wal")
	}
}

func TestPragma_ForeignKeys(t *testing.T) {
	db := newTestDB(t)
	got := queryPragma(t, db, "foreign_keys")
	if got != "1" {
		t.Errorf("foreign_keys = %q, want %q", got, "1")
	}
}

func TestPragma_Synchronous(t *testing.T) {
	// synchronous=NORMAL returns "1" from the integer PRAGMA query.
	// Values: 0=OFF 1=NORMAL 2=FULL 3=EXTRA
	db := newTestDB(t)
	got := queryPragma(t, db, "synchronous")
	if got != "1" {
		t.Errorf("synchronous = %q, want %q (NORMAL)", got, "1")
	}
}

func TestPragma_CacheSize(t *testing.T) {
	// cache_size is stored as a signed integer. We set -32768 (KiB).
	// SQLite returns the raw value we set, so we expect "-32768".
	db := newTestDB(t)
	got := queryPragma(t, db, "cache_size")
	if got != "-32768" {
		t.Errorf("cache_size = %q, want %q", got, "-32768")
	}
}

// ── Migrations ────────────────────────────────────────────────────────────────

func TestMigrations_AllAppliedOnFirstOpen(t *testing.T) {
	db := newTestDB(t)

	// Verify schema_migrations has exactly 3 rows (our 3 migrations).
	var count int
	row := db.SQL().QueryRow("SELECT COUNT(*) FROM schema_migrations")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != 3 {
		t.Errorf("schema_migrations count = %d, want 3", count)
	}
}

func TestMigrations_ClipsTableExists(t *testing.T) {
	db := newTestDB(t)

	_, err := db.SQL().Exec(
		`INSERT INTO clips (type, content, hash, created_at)
		 VALUES ('text', 'hello', 'abc123', datetime('now'))`,
	)
	if err != nil {
		t.Errorf("insert into clips: %v", err)
	}
}

func TestMigrations_SettingsTableHasDefaults(t *testing.T) {
	db := newTestDB(t)

	var hotkey string
	row := db.SQL().QueryRow(
		"SELECT value FROM settings WHERE key = 'hotkey'",
	)
	if err := row.Scan(&hotkey); err != nil {
		t.Fatalf("query hotkey setting: %v", err)
	}
	if hotkey != "ctrl+shift+v" {
		t.Errorf("hotkey default = %q, want %q", hotkey, "ctrl+shift+v")
	}
}

func TestMigrations_FTSTableExists(t *testing.T) {
	db := newTestDB(t)

	// Insert a clip and verify the FTS trigger fired.
	_, err := db.SQL().Exec(
		`INSERT INTO clips (type, content, hash, created_at)
		 VALUES ('text', 'docker compose up', 'ftstest1', datetime('now'))`,
	)
	if err != nil {
		t.Fatalf("insert clip: %v", err)
	}

	// Search via FTS5 — if the table or trigger is missing this panics.
	var id int64
	row := db.SQL().QueryRow(
		`SELECT c.id FROM clips c
		 JOIN clips_fts ON c.id = clips_fts.rowid
		 WHERE clips_fts MATCH 'docker'`,
	)
	if err := row.Scan(&id); err != nil {
		t.Errorf("FTS5 search after insert: %v", err)
	}
	if id == 0 {
		t.Error("FTS5 returned id=0, trigger may not have fired")
	}
}

func TestMigrations_SecondRunIsNoop(t *testing.T) {
	// Open the same file twice. The second Open must not error and
	// must not insert duplicate rows into schema_migrations.
	dir := t.TempDir()
	path := filepath.Join(dir, "noop.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db1, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	db2, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var count int
	row := db2.SQL().QueryRow("SELECT COUNT(*) FROM schema_migrations")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	// Still exactly 3 — no duplicates from the second Open.
	if count != 3 {
		t.Errorf("schema_migrations count after second Open = %d, want 3", count)
	}
}

func TestMigrations_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perm.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat db file: %v", err)
	}
	// We set 0600 — owner read/write only.
	got := info.Mode().Perm()
	want := os.FileMode(0o600)
	if got != want {
		t.Errorf("file permissions = %04o, want %04o", got, want)
	}
}
