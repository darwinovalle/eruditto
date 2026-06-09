package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// migration represents one versioned, irreversible schema change.
//
// Rules that must never be broken:
//  1. Never modify a migration that has already been shipped.
//     Users who have applied version N already have that schema.
//     Changing the SQL means their schema_migrations table says "done"
//     but the SQL on disk is different — silent corruption.
//  2. Never delete a migration.
//     The version number sequence must be gapless and append-only.
//  3. To change the schema, add a new migration with the next version.
//
// Each migration runs inside its own transaction. If the SQL fails,
// the transaction rolls back and the migration is not recorded in
// schema_migrations. The application then exits with an error.
// This gives us all-or-nothing semantics per migration.
type migration struct {
	version     int
	description string
	sql         string
}

// migrator applies pending migrations to a *DB.
type migrator struct {
	db  *DB
	log *slog.Logger
}

func newMigrator(db *DB, log *slog.Logger) *migrator {
	return &migrator{db: db, log: log}
}

// migrate bootstraps the tracking table then applies every migration
// whose version is not yet recorded. It is safe to call on every
// application startup — applied migrations are skipped.
func (m *migrator) migrate() error {
	if err := m.bootstrap(); err != nil {
		return fmt.Errorf("migrations: bootstrap: %w", err)
	}

	applied, err := m.appliedVersions()
	if err != nil {
		return fmt.Errorf("migrations: read applied versions: %w", err)
	}

	for _, mg := range registry {
		if applied[mg.version] {
			m.log.Debug("migration already applied",
				"version", mg.version,
				"description", mg.description)
			continue
		}

		m.log.Info("applying migration",
			"version", mg.version,
			"description", mg.description)

		if err := m.apply(mg); err != nil {
			return fmt.Errorf("migrations: apply version %d (%s): %w",
				mg.version, mg.description, err)
		}

		m.log.Info("migration applied",
			"version", mg.version,
			"description", mg.description)
	}

	return nil
}

// bootstrap creates the schema_migrations tracking table if it does
// not exist. This is the only DDL we run outside a versioned
// migration. It must exist before we can check which migrations have
// been applied.
func (m *migrator) bootstrap() error {
	const ddl = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			description TEXT    NOT NULL DEFAULT '',
			applied_at  TEXT    NOT NULL DEFAULT ''
		) STRICT;
	`
	if _, err := m.db.SQL().Exec(ddl); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

// appliedVersions returns the set of version numbers already recorded
// in schema_migrations. The map value is always true; we use
// map[int]bool as a set for O(1) membership checks in migrate().
func (m *migrator) appliedVersions() (map[int]bool, error) {
	rows, err := m.db.SQL().Query(
		"SELECT version FROM schema_migrations ORDER BY version ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// apply runs a single migration inside a transaction.
//
// The commit is conditional: we set a flag only after Commit()
// succeeds. If Commit() panics or the process is killed, the
// deferred Rollback() runs and the migration is not recorded.
// SQLite's atomicity guarantees a consistent state either way.
func (m *migrator) apply(mg migration) error {
	tx, err := m.db.SQL().Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil &&
				rbErr != sql.ErrTxDone {
				m.log.Error("migration rollback failed",
					"version", mg.version,
					"error", rbErr)
			}
		}
	}()

	// Execute the migration SQL. A migration may contain multiple
	// statements separated by semicolons; SQLite executes them all.
	if _, err := tx.Exec(mg.sql); err != nil {
		return fmt.Errorf("exec migration SQL: %w", err)
	}

	// Record the migration as applied inside the same transaction.
	// If recording fails, the entire transaction rolls back — we
	// never have a schema change without a matching record.
	const record = `
		INSERT INTO schema_migrations (version, description, applied_at)
		VALUES (?, ?, ?)
	`
	appliedAt := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(record, mg.version, mg.description, appliedAt); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Migration registry
//
// Add new migrations at the BOTTOM of this slice only.
// Never change or remove an existing entry.
// ─────────────────────────────────────────────────────────────────────────────

var registry = []migration{
	{
		version:     1,
		description: "create clips table",
		// STRICT mode tells SQLite to enforce column types strictly
		// (e.g., inserting a string into an INTEGER column is an error).
		// This catches bugs in the repository layer at runtime rather
		// than silently coercing types.
		//
		// Indexes:
		//   idx_clips_hash        — deduplication lookup on every insert
		//   idx_clips_created_at  — ORDER BY created_at DESC (main list view)
		//   idx_clips_favorite    — WHERE is_favorite = 1 (favorites filter)
		sql: `
			CREATE TABLE clips (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				type        TEXT    NOT NULL CHECK(type IN ('text','image')),
				content     TEXT    NOT NULL DEFAULT '',
				image_path  TEXT    NOT NULL DEFAULT '',
				hash        TEXT    NOT NULL UNIQUE,
				is_favorite INTEGER NOT NULL DEFAULT 0
				                    CHECK(is_favorite IN (0,1)),
				created_at  TEXT    NOT NULL
			) STRICT;

			CREATE INDEX idx_clips_hash
				ON clips(hash);

			CREATE INDEX idx_clips_created_at
				ON clips(created_at DESC);

			CREATE INDEX idx_clips_favorite
				ON clips(is_favorite)
				WHERE is_favorite = 1;
		`,
	},
	{
		version:     2,
		description: "create FTS5 virtual table and sync triggers",
		// Why FTS5 and triggers in the same migration?
		// An FTS5 external-content table without its triggers is a
		// permanently stale index — searches would return wrong results
		// the moment any clip is inserted. Keeping both in one migration
		// ensures it is impossible to reach a state where the table
		// exists but the triggers do not.
		//
		// FTS5 options:
		//   content='clips'       — read document text from the clips table
		//   content_rowid='id'    — map FTS rowid to clips.id
		//   tokenize='unicode61'  — Unicode-aware tokeniser; lowercases
		//                          tokens, removes diacritics (café→cafe)
		//
		// Trigger pattern for external-content FTS5:
		//   INSERT  → add new row to FTS index
		//   DELETE  → remove old row from FTS index
		//   UPDATE  → remove old, add new (no native UPDATE in FTS5)
		sql: `
			CREATE VIRTUAL TABLE clips_fts USING fts5(
				content,
				content     = 'clips',
				content_rowid = 'id',
				tokenize    = 'unicode61'
			);

			CREATE TRIGGER clips_ai AFTER INSERT ON clips BEGIN
				INSERT INTO clips_fts(rowid, content)
				VALUES (new.id, new.content);
			END;

			CREATE TRIGGER clips_ad AFTER DELETE ON clips BEGIN
				INSERT INTO clips_fts(clips_fts, rowid, content)
				VALUES ('delete', old.id, old.content);
			END;

			CREATE TRIGGER clips_au AFTER UPDATE ON clips BEGIN
				INSERT INTO clips_fts(clips_fts, rowid, content)
				VALUES ('delete', old.id, old.content);
				INSERT INTO clips_fts(rowid, content)
				VALUES (new.id, new.content);
			END;
		`,
	},
	{
		version:     3,
		description: "create settings table with defaults",
		// INSERT OR IGNORE: if a row already exists (e.g., the migration
		// is somehow re-run in a future debugging scenario) we do not
		// overwrite user-modified values. The UNIQUE constraint on key
		// is implied by PRIMARY KEY.
		sql: `
			CREATE TABLE settings (
				key        TEXT PRIMARY KEY,
				value      TEXT NOT NULL DEFAULT '',
				updated_at TEXT NOT NULL DEFAULT ''
			) STRICT;

			INSERT OR IGNORE INTO settings (key, value, updated_at) VALUES
				('hotkey',           'ctrl+shift+v', datetime('now')),
				('max_history',      '5000',         datetime('now')),
				('theme',            'dark',         datetime('now')),
				('start_on_boot',    'false',        datetime('now')),
				('database_path',    '',             datetime('now')),
				('poll_interval_ms', '500',          datetime('now'));
		`,
	},
}
