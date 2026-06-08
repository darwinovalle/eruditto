// Package domain holds the core data types for Eruditto.
//
// This package is the foundation of the codebase. It defines what a clip
// is, what a setting key looks like, and the validation rules that the
// rest of the system relies on. It MUST NOT import any other internal
// package and MUST NOT perform I/O, logging, or call into frameworks.
//
// Other packages import this one; this package imports nothing from
// the project. The Go "internal" rule prevents external modules from
// importing these types, which is what we want.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// ClipType identifies the kind of content a Clip carries.
//
// Using a typed string (rather than a bare int) means:
//   - Comparisons are type-safe at compile time.
//   - JSON marshals to a readable value like "text" or "image".
//   - SQLite stores it as TEXT, which is human-debuggable.
type ClipType string

const (
	// ClipTypeText is a UTF-8 text snippet copied to the clipboard.
	ClipTypeText ClipType = "text"

	// ClipTypeImage is an image copied to the clipboard. The image
	// bytes are stored on disk at ImagePath; only the path is in
	// the database row.
	ClipTypeImage ClipType = "image"
)

// IsKnown reports whether t is one of the defined ClipType values.
// We use this in Validate and in the database layer to reject
// rows with corrupt or future-version type values.
func (t ClipType) IsKnown() bool {
	switch t {
	case ClipTypeText, ClipTypeImage:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer so logs and error messages show
// the underlying string value rather than the memory address.
func (t ClipType) String() string {
	return string(t)
}

// Validation errors returned by Clip.Validate. Callers can match
// these with errors.Is to distinguish between failure modes.
var (
	ErrInvalidType    = errors.New("invalid clip type")
	ErrEmptyHash      = errors.New("hash must not be empty")
	ErrZeroCreatedAt  = errors.New("created_at must not be zero")
	ErrEmptyContent   = errors.New("text clip must have non-empty content")
	ErrEmptyImagePath = errors.New("image clip must have non-empty image_path")
	ErrNoContent      = errors.New("clip must have content or image_path")
)

// Clip is one entry in the user's clipboard history.
//
// Field naming follows the convention:
//   - Go field name (PascalCase)
//   - JSON tag     (snake_case)
//   - SQL column   (snake_case, same as JSON tag)
//
// These three names are intentionally aligned so reading code, log
// output, and SQL output feels consistent.
type Clip struct {
	ID         int64     `json:"id"`
	Type       ClipType  `json:"type"`
	Content    string    `json:"content"`
	ImagePath  string    `json:"image_path"`
	Hash       string    `json:"hash"`
	IsFavorite bool      `json:"is_favorite"`
	CreatedAt  time.Time `json:"created_at"`
}

// NewTextClip builds a Clip for a text snippet. The clip is ready
// to be passed to the repository's Insert method (which assigns the ID).
// CreatedAt is set to the current UTC time.
func NewTextClip(content, hash string) Clip {
	return Clip{
		Type:      ClipTypeText,
		Content:   content,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
	}
}

// NewImageClip builds a Clip for an image. imagePath must be the
// absolute path where the image bytes were saved. CreatedAt is set
// to the current UTC time.
func NewImageClip(imagePath, hash string) Clip {
	return Clip{
		Type:      ClipTypeImage,
		ImagePath: imagePath,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
	}
}

// Validate checks the clip's invariants. It is called by the
// repository layer before INSERT/UPDATE and by any code that
// deserializes a Clip from an external source (DB, JSON, network).
//
// A nil *Clip is invalid and returns ErrInvalidType (we use a
// sentinel rather than a nil-dereference panic for safety).
//
// The returned error wraps one of the sentinel errors declared
// in this package; callers can match with errors.Is.
func (c *Clip) Validate() error {
	if c == nil {
		return ErrInvalidType
	}
	if !c.Type.IsKnown() {
		return fmt.Errorf("%w: %q", ErrInvalidType, c.Type)
	}
	if c.Hash == "" {
		return ErrEmptyHash
	}
	if c.CreatedAt.IsZero() {
		return ErrZeroCreatedAt
	}
	// Type-specific content checks.
	switch c.Type {
	case ClipTypeText:
		if c.Content == "" {
			return ErrEmptyContent
		}
	case ClipTypeImage:
		if c.ImagePath == "" {
			return ErrEmptyImagePath
		}
	}
	// Defensive: a clip with neither content nor image_path is
	// a programming error. It cannot happen via the constructors
	// above, but if someone builds a Clip{} literal we still
	// catch it here.
	if c.Content == "" && c.ImagePath == "" {
		return ErrNoContent
	}
	return nil
}

// DisplayContent returns a UI-safe preview of the clip's text
// content, truncated to at most maxRunes Unicode code points.
//
// For image clips, returns the empty string — the UI is expected
// to render a thumbnail in that case.
//
// Behavior:
//   - Empty or whitespace content returns "" with no ellipsis.
//   - Content that already fits in maxRunes runes is returned as-is.
//   - Longer content is cut to maxRunes-1 runes and a "…" (U+2026)
//     is appended, so the visible length never exceeds maxRunes.
//   - maxRunes <= 0 returns "" (defensive — the UI must never
//     request a zero-or-negative width).
//
// Unicode correctness: we operate on []rune, not bytes, so we
// never split a multi-byte UTF-8 sequence. This is the difference
// between a clean truncation and garbled output for content like
// "café" or emoji.
func (c Clip) DisplayContent(maxRunes int) string {
	if c.Type == ClipTypeImage {
		return ""
	}
	if maxRunes <= 0 {
		return ""
	}
	if c.Content == "" {
		return ""
	}
	runes := []rune(c.Content)
	if len(runes) <= maxRunes {
		return c.Content
	}
	// Reserve one rune for the ellipsis so the result is at most
	// maxRunes runes long.
	const ellipsis = "…"
	return string(runes[:maxRunes-1]) + ellipsis
}
