// Package domain — settings keys and defaults.
//
// Settings are stored in the SQLite settings table as key-value pairs
// where both key and value are TEXT. This file defines:
//
//  1. Key constants  — used everywhere settings are read or written.
//     A typo in a key string causes a silent miss (you get the default
//     instead of an error). Constants catch typos at compile time.
//
//  2. Default values — the source of truth for what a fresh install
//     looks like. Migration 003 seeds the database from these same
//     values. If you change a default here, update the migration too
//     (or add a new migration that updates existing rows).
//
//  3. Validation rules — each key has a validate function used by
//     the settings service before any Set() call reaches the database.
//     Invalid values are rejected with a descriptive error rather than
//     silently stored and breaking the application later.
package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Key constants
//
// Use these constants everywhere. Never write the string literal "hotkey"
// in application code — always write domain.KeyHotkey.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// KeyHotkey is the global shortcut that opens the popup.
	// Format: modifier+modifier+key, all lowercase, e.g. "ctrl+shift+v".
	KeyHotkey = "hotkey"

	// KeyMaxHistory is the maximum number of clips retained.
	// Oldest non-favourite clips are pruned when this limit is exceeded.
	// Stored as a decimal integer string, e.g. "5000".
	KeyMaxHistory = "max_history"

	// KeyTheme controls the UI colour theme.
	// Valid values: "dark", "light", "system".
	KeyTheme = "theme"

	// KeyStartOnBoot controls whether Eruditto launches at login.
	// Valid values: "true", "false".
	KeyStartOnBoot = "start_on_boot"

	// KeyDatabasePath overrides the default XDG database location.
	// Empty string means use the XDG default.
	KeyDatabasePath = "database_path"

	// KeyPollIntervalMs is the clipboard polling interval in milliseconds.
	// Minimum: 100 ms. Default: 500 ms.
	// Lower values are more responsive but use more CPU.
	KeyPollIntervalMs = "poll_interval_ms"


	KeyAutoPaste = "auto_paste"
)

// ─────────────────────────────────────────────────────────────────────────────
// Default values
//
// These are the values a fresh installation starts with.
// Migration 003 inserts these into the settings table.
// The service layer uses this map as the fallback when Get() is
// called for a key that has no row in the database.
// ─────────────────────────────────────────────────────────────────────────────

// DefaultSettings maps every known setting key to its default value.
// The map is read-only at runtime — never modify it.
var DefaultSettings = map[string]string{
	KeyHotkey:         "ctrl+shift+z",
	KeyMaxHistory:     "5000",
	KeyTheme:          "dark",
	KeyStartOnBoot:    "false",
	KeyDatabasePath:   "",
	KeyPollIntervalMs: "500",
	KeyAutoPaste: "false",
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation errors
// ─────────────────────────────────────────────────────────────────────────────

// ErrUnknownKey is returned when Set() is called with a key that is
// not in DefaultSettings. We reject unknown keys to prevent typos
// from silently creating orphan rows in the settings table.
var ErrUnknownKey = errors.New("settings: unknown key")

// ErrInvalidValue is returned when Set() is called with a value that
// fails the validation rule for that key.
var ErrInvalidValue = errors.New("settings: invalid value")

// ─────────────────────────────────────────────────────────────────────────────
// Validation
// ─────────────────────────────────────────────────────────────────────────────

// ValidateSetting checks whether value is acceptable for key.
//
// Returns:
//   - ErrUnknownKey  if key is not a known setting
//   - ErrInvalidValue (with a descriptive message) if the value is wrong
//   - nil if the key+value pair is valid
//
// Called by the settings service before every Set() operation.
// Also callable directly by the UI to give inline validation feedback.
func ValidateSetting(key, value string) error {
	switch key {
	case KeyHotkey:
		return validateHotkey(value)
	case KeyMaxHistory:
		return validateMaxHistory(value)
	case KeyTheme:
		return validateTheme(value)
	case KeyStartOnBoot:
		return validateBool(key, value)
	case KeyDatabasePath:
		return nil // any string is acceptable, including empty
	case KeyPollIntervalMs:
		return validatePollInterval(value)
	case KeyAutoPaste:
		if value != "true" && value != "false" {
			return fmt.Errorf("must be true or false")
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
}

// validateHotkey checks that value looks like a valid hotkey string.
//
// Required format: one or more modifiers followed by a key, all
// joined with "+", all lowercase.
//
// Valid examples:
//   - "ctrl+shift+v"
//   - "ctrl+alt+h"
//   - "super+v"
//
// Invalid examples:
//   - ""          (empty)
//   - "v"         (no modifier)
//   - "CTRL+V"    (uppercase)
//   - "ctrl+"     (trailing plus, empty key)
func validateHotkey(value string) error {
	if value == "" {
		return fmt.Errorf("%w: %q: hotkey must not be empty", ErrInvalidValue, value)
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("%w: %q: hotkey must be lowercase", ErrInvalidValue, value)
	}

	parts := strings.Split(value, "+")
	if len(parts) < 2 {
		return fmt.Errorf("%w: %q: hotkey must have at least one modifier and one key (e.g. ctrl+v)",
			ErrInvalidValue, value)
	}

	for _, p := range parts {
		if p == "" {
			return fmt.Errorf("%w: %q: hotkey contains empty segment (check for trailing/double '+')",
				ErrInvalidValue, value)
		}
	}

	// The last segment is the key, everything before it is a modifier.
	// We validate that all modifier names are known.
	modifiers := parts[:len(parts)-1]
	knownModifiers := map[string]bool{
		"ctrl": true, "control": true,
		"shift": true,
		"alt":   true, "option": true,
		"super": true, "meta": true, "win": true,
	}
	for _, mod := range modifiers {
		if !knownModifiers[mod] {
			return fmt.Errorf("%w: %q: unknown modifier %q (known: ctrl, shift, alt, super)",
				ErrInvalidValue, value, mod)
		}
	}

	return nil
}

// validateMaxHistory checks that value is a positive integer.
func validateMaxHistory(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%w: %q: max_history must be an integer: %v",
			ErrInvalidValue, value, err)
	}
	if n <= 0 {
		return fmt.Errorf("%w: %q: max_history must be > 0",
			ErrInvalidValue, value)
	}
	// Practical upper limit: 1 million entries.
	// Beyond this, the FTS index size becomes a concern.
	if n > 1_000_000 {
		return fmt.Errorf("%w: %q: max_history must be <= 1000000",
			ErrInvalidValue, value)
	}
	return nil
}

// validateTheme checks that value is one of the accepted theme names.
func validateTheme(value string) error {
	switch value {
	case "dark", "light", "system":
		return nil
	default:
		return fmt.Errorf("%w: %q: theme must be one of: dark, light, system",
			ErrInvalidValue, value)
	}
}

// validateBool checks that value is "true" or "false".
func validateBool(key, value string) error {
	switch value {
	case "true", "false":
		return nil
	default:
		return fmt.Errorf("%w: %q: %s must be \"true\" or \"false\"",
			ErrInvalidValue, value, key)
	}
}

// validatePollInterval checks that value is an integer >= 100.
// Below 100 ms the monitor burns unnecessary CPU without meaningful
// responsiveness improvement.
func validatePollInterval(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%w: %q: poll_interval_ms must be an integer: %v",
			ErrInvalidValue, value, err)
	}
	if n < 100 {
		return fmt.Errorf("%w: %q: poll_interval_ms must be >= 100",
			ErrInvalidValue, value)
	}
	if n > 60_000 {
		return fmt.Errorf("%w: %q: poll_interval_ms must be <= 60000 (1 minute)",
			ErrInvalidValue, value)
	}
	return nil
}
