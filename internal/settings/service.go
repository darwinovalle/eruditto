// Package settings provides read/write access to user preferences
// stored in the SQLite settings table.
//
// Architecture:
//
//	┌─────────────────┐
//	│  Service        │  ← this package
//	├─────────────────┤
//	│ Get / Set       │  typed accessors
//	│ GetInt / GetBool│  convenience wrappers
//	│ GetAll / Reset  │  bulk operations
//	└────────┬────────┘
//	         │ SQL
//	         ▼
//	┌─────────────────┐
//	│  settings table │  (created by Migration 003)
//	│  key TEXT PK    │
//	│  value TEXT     │
//	│  updated_at TEXT│
//	└─────────────────┘
//
// The service never caches values in memory. Every Get reads from
// SQLite. This means settings changes made by one goroutine are
// immediately visible to all others. SQLite's WAL mode makes these
// reads non-blocking.
//
// Why no in-memory cache?
// Settings change at most a few times per session (user opens
// settings, clicks Save). The overhead of a SQLite read for each
// Get() call is negligible (~0.1 ms). A cache would add complexity
// and require invalidation logic across goroutines.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/darwinovalle/eruditto/internal/database"
	"github.com/darwinovalle/eruditto/internal/domain"
)

// Service provides access to application settings persisted in SQLite.
type Service struct {
	db  *database.DB
	log *slog.Logger
}

// New creates a Service backed by the given database.
// The database must already have Migration 003 applied (settings table
// with default rows). Call database.Open() before New().
func New(db *database.DB, log *slog.Logger) *Service {
	return &Service{db: db, log: log}
}

// ─────────────────────────────────────────────────────────────────────────────
// Core read/write operations
// ─────────────────────────────────────────────────────────────────────────────

// Get returns the value for key.
//
// Lookup order:
//  1. settings table in SQLite
//  2. domain.DefaultSettings (fallback for missing rows)
//  3. "" (empty string) if the key is not in DefaultSettings either
//
// A missing row is not an error. This makes Get safe to call even
// on a partially migrated database or in tests that only populate
// some settings.
func (s *Service) Get(ctx context.Context, key string) (string, error) {
	const q = `SELECT value FROM settings WHERE key = ?`

	var value string
	err := s.db.SQL().QueryRowContext(ctx, q, key).Scan(&value)
	if err == nil {
		return value, nil
	}

	// Row not found — use the compiled-in default.
	if errors.Is(err, sql.ErrNoRows) {
		if def, ok := domain.DefaultSettings[key]; ok {
			s.log.Debug("settings: key not in DB, using default",
				"key", key, "default", def)
			return def, nil
		}
		// Key is completely unknown — return empty string, not an error.
		// The caller can decide whether this is a problem.
		return "", nil
	}

	return "", fmt.Errorf("settings: get %q: %w", key, err)
}

// Set validates and persists a setting.
//
// Validation runs before any database write:
//   - Unknown keys are rejected (ErrUnknownKey)
//   - Values that fail the key-specific rule are rejected (ErrInvalidValue)
//
// Uses INSERT OR REPLACE so the first Set creates the row and
// subsequent Sets update it. updated_at is always refreshed.
func (s *Service) Set(ctx context.Context, key, value string) error {
	// Validate before touching the database.
	if err := domain.ValidateSetting(key, value); err != nil {
		return fmt.Errorf("settings: set %q=%q: %w", key, value, err)
	}

	const q = `
		INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at
	`

	updatedAt := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.SQL().ExecContext(ctx, q, key, value, updatedAt); err != nil {
		return fmt.Errorf("settings: set %q: %w", key, err)
	}

	s.log.Info("setting updated", "key", key, "value", value)
	return nil
}

// GetAll returns every row in the settings table as a map.
//
// Keys that exist in domain.DefaultSettings but are missing from the
// database are included in the result using their default values.
// This ensures GetAll always returns a complete settings snapshot,
// even on a freshly created database before Migration 003 runs.
func (s *Service) GetAll(ctx context.Context) (map[string]string, error) {
	const q = `SELECT key, value FROM settings`

	rows, err := s.db.SQL().QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("settings: get all: %w", err)
	}
	defer rows.Close()

	// Start with defaults so unknown/missing keys have a value.
	result := make(map[string]string, len(domain.DefaultSettings))
	for k, v := range domain.DefaultSettings {
		result[k] = v
	}

	// Overwrite defaults with actual DB values.
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("settings: get all: scan: %w", err)
		}
		result[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settings: get all: iterate: %w", err)
	}

	return result, nil
}

// Reset restores all settings to their default values.
//
// This is a destructive operation: all user customisations are lost.
// The UI must confirm with the user before calling Reset.
//
// Implementation: upserts each default key-value pair in a single
// transaction so the reset is atomic — either all defaults are
// restored or none are.
func (s *Service) Reset(ctx context.Context) error {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("settings: reset: begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil &&
				!errors.Is(rbErr, sql.ErrTxDone) {
				s.log.Error("settings: reset rollback failed", "error", rbErr)
			}
		}
	}()

	const q = `
		INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at
	`

	updatedAt := time.Now().UTC().Format(time.RFC3339)
	for key, value := range domain.DefaultSettings {
		if _, err := tx.ExecContext(ctx, q, key, value, updatedAt); err != nil {
			return fmt.Errorf("settings: reset: upsert %q: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("settings: reset: commit: %w", err)
	}
	committed = true

	s.log.Info("settings reset to defaults")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Typed convenience accessors
//
// Why typed accessors instead of raw Get() everywhere?
//
// poll_interval_ms is always used as time.Duration.
// max_history is always used as int.
// start_on_boot is always used as bool.
//
// Without typed accessors, every call site would contain:
//
//   raw, _ := svc.Get(ctx, domain.KeyPollIntervalMs)
//   n, err := strconv.Atoi(raw)
//   if err != nil { n = 500 }
//
// That is four lines of boilerplate per call site, each potentially
// getting the default wrong. Typed accessors centralise the parsing
// and the fallback in one place.
// ─────────────────────────────────────────────────────────────────────────────

// GetInt returns the setting for key parsed as an integer.
//
// If the stored value cannot be parsed as an integer (e.g., someone
// manually edited the database and set max_history="banana"), returns
// defaultValue instead of an error.
//
// This defensive behaviour prevents a corrupt settings value from
// crashing the application.
func (s *Service) GetInt(ctx context.Context, key string, defaultValue int) int {
	raw, err := s.Get(ctx, key)
	if err != nil {
		s.log.Warn("settings: GetInt read failed, using default",
			"key", key, "default", defaultValue, "error", err)
		return defaultValue
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		s.log.Warn("settings: GetInt parse failed, using default",
			"key", key, "raw", raw, "default", defaultValue, "error", err)
		return defaultValue
	}

	return n
}

// GetBool returns the setting for key parsed as a boolean.
//
// Accepts "true" and "false" (case-insensitive). Any other value
// returns defaultValue.
func (s *Service) GetBool(ctx context.Context, key string, defaultValue bool) bool {
	raw, err := s.Get(ctx, key)
	if err != nil {
		s.log.Warn("settings: GetBool read failed, using default",
			"key", key, "default", defaultValue, "error", err)
		return defaultValue
	}

	switch raw {
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	default:
		s.log.Warn("settings: GetBool parse failed, using default",
			"key", key, "raw", raw, "default", defaultValue)
		return defaultValue
	}
}

// GetPollInterval returns the polling interval as a time.Duration.
//
// Returns the default (500ms) if the setting is missing, unparseable,
// or below the minimum (100ms).
func (s *Service) GetPollInterval(ctx context.Context) time.Duration {
	ms := s.GetInt(ctx, domain.KeyPollIntervalMs, 500)
	if ms < 100 {
		ms = 500
	}
	return time.Duration(ms) * time.Millisecond
}

// GetMaxHistory returns the maximum history count as an int.
// Returns 5000 if the setting is missing or invalid.
func (s *Service) GetMaxHistory(ctx context.Context) int {
	n := s.GetInt(ctx, domain.KeyMaxHistory, 5000)
	if n <= 0 {
		return 5000
	}
	return n
}
