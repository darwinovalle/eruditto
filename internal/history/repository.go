// Package history implements the repository for clipboard history.
//
// All SQL lives in this package. No other package issues queries
// against the clips or clips_fts tables. If we ever migrate away
// from SQLite, only this file changes.
//
// Repository methods follow these conventions:
//
//  1. Every method accepts a context.Context as its first argument.
//     This lets callers cancel long-running queries when the app
//     is shutting down or the user closes the popup.
//
//  2. Methods return domain types, never raw database rows.
//     Callers do not need to know how clips are stored.
//
//  3. Errors are always wrapped with context so the call stack is
//     visible in logs:
//     "history: insert: exec: UNIQUE constraint failed: clips.hash"
//
//  4. The repository NEVER deletes image files. It returns the path
//     so the caller (the clipboard service) can delete the file after
//     the database row is gone. Mixing file I/O and SQL in the same
//     method makes error recovery ambiguous.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/darwinovalle/eruditto/internal/database"
	"github.com/darwinovalle/eruditto/internal/domain"
)

// Repository provides read/write access to the clips table.
type Repository struct {
	db  *database.DB
	log *slog.Logger
}

// New creates a Repository. db must be a fully migrated *database.DB.
func New(db *database.DB, log *slog.Logger) *Repository {
	return &Repository{db: db, log: log}
}

// ─────────────────────────────────────────────────────────────────────────────
// Write operations
// ─────────────────────────────────────────────────────────────────────────────

// Insert stores a clip and returns its assigned ID.
//
// Deduplication: the clips table has a UNIQUE constraint on hash. If a
// clip with the same hash already exists, Insert returns the existing
// row's ID and a nil error — it is not an error to insert a duplicate.
// The caller (the monitor service) uses the returned ID to identify
// the clip regardless of whether it was just created or already existed.
//
// Validation: Insert calls clip.Validate() before touching the database.
// A clip that fails validation is rejected immediately with the
// validation error — no database round-trip.
func (r *Repository) Insert(ctx context.Context, clip domain.Clip) (int64, error) {
	if err := clip.Validate(); err != nil {
		return 0, fmt.Errorf("history: insert: validation: %w", err)
	}

	const q = `
		INSERT INTO clips (type, content, image_path, hash, is_favorite, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO NOTHING
	`

	result, err := r.db.SQL().ExecContext(ctx, q,
		string(clip.Type),
		clip.Content,
		clip.ImagePath,
		clip.Hash,
		boolToInt(clip.IsFavorite),
		clip.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("history: insert: exec: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("history: insert: last insert id: %w", err)
	}

	// LastInsertId returns 0 when ON CONFLICT DO NOTHING fires (no row
	// was actually inserted). Look up the existing row's ID instead.
	if id == 0 {
		existing, err := r.idByHash(ctx, clip.Hash)
		if err != nil {
			return 0, fmt.Errorf("history: insert: lookup existing: %w", err)
		}
		r.log.Debug("clip already exists", "hash", clip.Hash, "id", existing)
		return existing, nil
	}

	r.log.Debug("clip inserted", "id", id, "type", clip.Type, "hash", clip.Hash)
	return id, nil
}

// UpdateImagePath sets the image_path column for an existing clip row.
//
// Used by the clipboard service when persisting an image clip:
//
//  1. Insert the clip with an empty image_path → repo.Insert returns id
//  2. Save the bytes to disk via internal/images.Storage.Save(id, bytes)
//  3. Call UpdateImagePath(id, savedPath) so the row points at the file
//
// Two-step rather than one-step because the on-disk filename is
// derived from the DB-assigned id (see internal/images/storage.go
// fullPath), and the id is not knowable until the row exists.
//
// Returns a non-nil error if no row with the given id exists.
// RowsAffected == 0 is treated as "not found" rather than silent
// success so callers do not silently lose image_path updates.
func (r *Repository) UpdateImagePath(ctx context.Context, id int64, imagePath string) error {
	const q = `
		UPDATE clips
		SET    image_path = ?
		WHERE  id = ?
	`
	result, err := r.db.SQL().ExecContext(ctx, q, imagePath, id)
	if err != nil {
		return fmt.Errorf("history: update image_path id=%d: %w", id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("history: update image_path id=%d: rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("history: update image_path id=%d: not found", id)
	}
	r.log.Debug("clip image_path updated", "id", id, "image_path", imagePath)
	return nil
}

// ToggleFavorite flips the is_favorite flag for the given clip ID.
// Returns the new value of is_favorite after the toggle.
func (r *Repository) ToggleFavorite(ctx context.Context, id int64) (bool, error) {
	const q = `
		UPDATE clips
		SET is_favorite = CASE is_favorite WHEN 0 THEN 1 ELSE 0 END
		WHERE id = ?
	`
	result, err := r.db.SQL().ExecContext(ctx, q, id)
	if err != nil {
		return false, fmt.Errorf("history: toggle favorite id=%d: %w", id, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return false, fmt.Errorf("history: toggle favorite: id=%d not found", id)
	}

	// Read back the new value.
	var fav int
	row := r.db.SQL().QueryRowContext(ctx,
		"SELECT is_favorite FROM clips WHERE id = ?", id)
	if err := row.Scan(&fav); err != nil {
		return false, fmt.Errorf("history: toggle favorite: read back: %w", err)
	}
	return fav == 1, nil
}

// Delete removes a clip row and returns its image_path so the caller
// can delete the file on disk. Returns ("", nil) for text clips.
//
// If no row with the given id exists, Delete returns ("", nil) —
// idempotent deletion simplifies error handling at call sites.
func (r *Repository) Delete(ctx context.Context, id int64) (imagePath string, err error) {
	// Read the image_path before deleting so we can return it.
	var path string
	row := r.db.SQL().QueryRowContext(ctx,
		"SELECT image_path FROM clips WHERE id = ?", id)
	if err := row.Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil // already gone — idempotent
		}
		return "", fmt.Errorf("history: delete: read image_path id=%d: %w", id, err)
	}

	if _, err := r.db.SQL().ExecContext(ctx,
		"DELETE FROM clips WHERE id = ?", id); err != nil {
		return "", fmt.Errorf("history: delete: exec id=%d: %w", id, err)
	}

	r.log.Debug("clip deleted", "id", id, "image_path", path)
	return path, nil
}

// EnforceMaxHistory deletes the oldest non-favourite clips until the
// total clip count is at or below maxCount.
//
// Favourites are never deleted automatically — the user has explicitly
// pinned them. Only the oldest non-favourite clips are removed.
//
// Returns the number of rows deleted.
func (r *Repository) EnforceMaxHistory(ctx context.Context, maxCount int) (int64, error) {
	var total int64
	row := r.db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM clips")
	if err := row.Scan(&total); err != nil {
		return 0, fmt.Errorf("history: enforce max: count: %w", err)
	}

	if total <= int64(maxCount) {
		return 0, nil
	}

	excess := total - int64(maxCount)

	// Delete the `excess` oldest non-favourite clips. The subquery
	// identifies which rows to remove; the outer DELETE removes them.
	// We avoid a temporary table for simplicity — at 50 000 rows this
	// is fast (indexed on created_at).
	const q = `
		DELETE FROM clips
		WHERE id IN (
			SELECT id FROM clips
			WHERE  is_favorite = 0
			ORDER  BY created_at ASC
			LIMIT  ?
		)
	`
	result, err := r.db.SQL().ExecContext(ctx, q, excess)
	if err != nil {
		return 0, fmt.Errorf("history: enforce max: delete: %w", err)
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("history: enforce max: rows affected: %w", err)
	}

	if deleted > 0 {
		r.log.Info("history pruned", "deleted", deleted, "max", maxCount)
	}
	return deleted, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Read operations
// ─────────────────────────────────────────────────────────────────────────────

// GetByID returns the clip with the given id.
// Returns a wrapped sql.ErrNoRows if the id does not exist.
func (r *Repository) GetByID(ctx context.Context, id int64) (domain.Clip, error) {
	const q = `
		SELECT id, type, content, image_path, hash, is_favorite, created_at
		FROM   clips
		WHERE  id = ?
	`
	row := r.db.SQL().QueryRowContext(ctx, q, id)
	clip, err := scanClip(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Clip{}, fmt.Errorf("history: get by id=%d: %w", id, err)
		}
		return domain.Clip{}, fmt.Errorf("history: get by id=%d: scan: %w", id, err)
	}
	return clip, nil
}

// Recent returns up to limit clips ordered by favourites first, then
// by creation time descending (newest first).
//
// Cursor-based loading strategy:
// The popup loads an initial page of ~200 clips (fast, < 5 ms for
// 50 000 rows). The UI renders the first ~20 visible items from that
// slice — no additional DB queries needed for scrolling within the
// page. If the user scrolls past 200 items, call Recent again with a
// larger limit or implement a cursor using the last-seen created_at.
func (r *Repository) Recent(ctx context.Context, limit int) ([]domain.Clip, error) {
	const q = `
		SELECT id, type, content, image_path, hash, is_favorite, created_at
		FROM   clips
		ORDER  BY is_favorite DESC, created_at DESC
		LIMIT  ?
	`
	rows, err := r.db.SQL().QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("history: recent: query: %w", err)
	}
	defer rows.Close()

	return scanClips(rows)
}

// Search performs full-text search over clip content using the FTS5
// index. Results are ordered by relevance rank then by recency.
//
// If query is empty, Search falls back to Recent(ctx, limit).
//
// FTS5 query format:
// We wrap the user's raw input in double-quotes and append * for
// prefix matching. "docker"* matches "docker", "docker-compose",
// "dockerd", etc. This gives instant results as the user types.
//
// Sanitization: internal double-quotes in the user's input are
// escaped by doubling them (FTS5 convention). This prevents syntax
// errors when the user types something like: he said "hello".
func (r *Repository) Search(ctx context.Context, query string, limit int) ([]domain.Clip, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return r.Recent(ctx, limit)
	}

	ftsQuery := buildFTSQuery(query)

	const q = `
		SELECT c.id, c.type, c.content, c.image_path, c.hash,
		       c.is_favorite, c.created_at
		FROM   clips c
		JOIN   clips_fts ON c.id = clips_fts.rowid
		WHERE  clips_fts MATCH ?
		ORDER  BY c.is_favorite DESC, rank, c.created_at DESC
		LIMIT  ?
	`
	rows, err := r.db.SQL().QueryContext(ctx, q, ftsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("history: search %q: query: %w", query, err)
	}
	defer rows.Close()

	return scanClips(rows)
}

// Count returns the total number of clips in the database.
func (r *Repository) Count(ctx context.Context) (int64, error) {
	var n int64
	row := r.db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM clips")
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("history: count: %w", err)
	}
	return n, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// idByHash looks up a clip's ID by its content hash.
// Used by Insert to return the existing ID when ON CONFLICT fires.
func (r *Repository) idByHash(ctx context.Context, hash string) (int64, error) {
	var id int64
	row := r.db.SQL().QueryRowContext(ctx,
		"SELECT id FROM clips WHERE hash = ?", hash)
	if err := row.Scan(&id); err != nil {
		return 0, fmt.Errorf("id by hash %q: %w", hash, err)
	}
	return id, nil
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanClip can be
// used for both QueryRow (single) and Query (multiple) results.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanClip reads one row into a domain.Clip.
// Column order must match every SELECT in this file:
//
//	id, type, content, image_path, hash, is_favorite, created_at
func scanClip(s rowScanner) (domain.Clip, error) {
	var (
		c          domain.Clip
		clipType   string
		isFavorite int
		createdAt  string
	)
	err := s.Scan(
		&c.ID,
		&clipType,
		&c.Content,
		&c.ImagePath,
		&c.Hash,
		&isFavorite,
		&createdAt,
	)
	if err != nil {
		return domain.Clip{}, err
	}

	c.Type = domain.ClipType(clipType)
	c.IsFavorite = isFavorite == 1

	// Try RFC3339 first (what we write); fall back to SQLite's
	// datetime() format which appears in migration-inserted defaults.
	c.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		c.CreatedAt, err = time.Parse("2006-01-02 15:04:05", createdAt)
		if err != nil {
			return domain.Clip{}, fmt.Errorf("parse created_at %q: %w", createdAt, err)
		}
	}

	return c, nil
}

// scanClips iterates rows and scans each into a domain.Clip.
func scanClips(rows *sql.Rows) ([]domain.Clip, error) {
	var clips []domain.Clip
	for rows.Next() {
		c, err := scanClip(rows)
		if err != nil {
			return nil, fmt.Errorf("scan clip: %w", err)
		}
		clips = append(clips, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return clips, nil
}

// boolToInt converts a Go bool to SQLite's 0/1 integer representation.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// buildFTSQuery wraps raw user input into a safe FTS5 MATCH expression.
//
// Strategy: phrase search with prefix matching.
//   - Wrap in double-quotes → treat entire input as a phrase
//   - Escape any internal double-quotes by doubling them
//   - Append * → enable prefix matching on the last token
//
// Examples:
//
//	"docker"        → `"docker"*`
//	"docker compose"→ `"docker compose"*`
//	`he said "hi"` → `"he said ""hi"""*`
func buildFTSQuery(raw string) string {
	escaped := strings.ReplaceAll(raw, `"`, `""`)
	return `"` + escaped + `"*`
}
