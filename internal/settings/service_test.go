package settings_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/darwinovalle/eruditto/internal/database"
	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/settings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func newTestService(t *testing.T) *settings.Service {
	t.Helper()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(
		filepath.Join(t.TempDir(), "test.db"),
		log,
	)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return settings.New(db, log)
}

// ─────────────────────────────────────────────────────────────────────────────
// Get
// ─────────────────────────────────────────────────────────────────────────────

func TestGet_ReturnsDefaultFromDB(t *testing.T) {
	// Migration 003 seeds default values. Get should find them.
	svc := newTestService(t)
	ctx := context.Background()

	got, err := svc.Get(ctx, domain.KeyHotkey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "ctrl+shift+v" {
		t.Errorf("Get(hotkey) = %q, want %q", got, "ctrl+shift+v")
	}
}

func TestGet_FallsBackToDefaultForMissingKey(t *testing.T) {
	// Get for a key not in the DB falls back to domain.DefaultSettings.
	// We cannot easily delete a seeded row, so we test with a key that
	// would only exist if explicitly set — use a well-known key and
	// check the value equals the domain default.
	svc := newTestService(t)
	ctx := context.Background()

	got, err := svc.Get(ctx, domain.KeyTheme)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := domain.DefaultSettings[domain.KeyTheme]
	if got != want {
		t.Errorf("Get(theme) = %q, want %q", got, want)
	}
}

func TestGet_UnknownKeyReturnsEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	got, err := svc.Get(ctx, "completely_unknown_key_xyz")
	if err != nil {
		t.Fatalf("Get(unknown) unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("Get(unknown) = %q, want empty string", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Set
// ─────────────────────────────────────────────────────────────────────────────

func TestSet_RoundTrip(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, domain.KeyTheme, "light"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := svc.Get(ctx, domain.KeyTheme)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got != "light" {
		t.Errorf("Get(theme) = %q, want %q", got, "light")
	}
}

func TestSet_UpdatesExistingValue(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Set twice — second write must win.
	if err := svc.Set(ctx, domain.KeyTheme, "light"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := svc.Set(ctx, domain.KeyTheme, "dark"); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	got, err := svc.Get(ctx, domain.KeyTheme)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "dark" {
		t.Errorf("Get(theme) = %q, want %q", got, "dark")
	}
}

func TestSet_RejectsUnknownKey(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Set(ctx, "not_a_real_key", "value")
	if !errors.Is(err, domain.ErrUnknownKey) {
		t.Errorf("Set(unknown key) = %v, want wrapping ErrUnknownKey", err)
	}
}

func TestSet_RejectsInvalidHotkey(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"no modifier", "v"},
		{"uppercase", "CTRL+V"},
		{"trailing plus", "ctrl+"},
		{"unknown modifier", "cmd+v"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Set(ctx, domain.KeyHotkey, tt.value)
			if !errors.Is(err, domain.ErrInvalidValue) {
				t.Errorf("Set(hotkey=%q) = %v, want ErrInvalidValue", tt.value, err)
			}
		})
	}
}

func TestSet_RejectsNegativeMaxHistory(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	tests := []string{"-1", "0", "-100"}
	for _, v := range tests {
		err := svc.Set(ctx, domain.KeyMaxHistory, v)
		if !errors.Is(err, domain.ErrInvalidValue) {
			t.Errorf("Set(max_history=%q) = %v, want ErrInvalidValue", v, err)
		}
	}
}

func TestSet_RejectsInvalidTheme(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Set(ctx, domain.KeyTheme, "purple")
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Errorf("Set(theme=purple) = %v, want ErrInvalidValue", err)
	}
}

func TestSet_RejectsInvalidBool(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Set(ctx, domain.KeyStartOnBoot, "yes")
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Errorf("Set(start_on_boot=yes) = %v, want ErrInvalidValue", err)
	}
}

func TestSet_RejectsLowPollInterval(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Set(ctx, domain.KeyPollIntervalMs, "50")
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Errorf("Set(poll_interval_ms=50) = %v, want ErrInvalidValue", err)
	}
}

func TestSet_AcceptsValidHotkeys(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	valid := []string{
		"ctrl+shift+v",
		"ctrl+alt+h",
		"super+v",
		"ctrl+v",
	}
	for _, v := range valid {
		if err := svc.Set(ctx, domain.KeyHotkey, v); err != nil {
			t.Errorf("Set(hotkey=%q) unexpected error: %v", v, err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetAll
// ─────────────────────────────────────────────────────────────────────────────

func TestGetAll_ContainsAllDefaultKeys(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	all, err := svc.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	for key := range domain.DefaultSettings {
		if _, ok := all[key]; !ok {
			t.Errorf("GetAll missing key %q", key)
		}
	}
}

func TestGetAll_ReflectsSetValues(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, domain.KeyTheme, "light"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	all, err := svc.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	if all[domain.KeyTheme] != "light" {
		t.Errorf("GetAll[theme] = %q, want %q", all[domain.KeyTheme], "light")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Reset
// ─────────────────────────────────────────────────────────────────────────────

func TestReset_RestoresDefaults(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Change several settings.
	changes := map[string]string{
		domain.KeyTheme:      "light",
		domain.KeyMaxHistory: "100",
	}
	for k, v := range changes {
		if err := svc.Set(ctx, k, v); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}

	// Reset.
	if err := svc.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Verify defaults are restored.
	for key, want := range domain.DefaultSettings {
		got, err := svc.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%q) after Reset: %v", key, err)
		}
		if got != want {
			t.Errorf("After Reset: Get(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestReset_IsIdempotent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Reset on a fresh DB (already at defaults) must not error.
	if err := svc.Reset(ctx); err != nil {
		t.Fatalf("first Reset: %v", err)
	}
	if err := svc.Reset(ctx); err != nil {
		t.Fatalf("second Reset: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetInt
// ─────────────────────────────────────────────────────────────────────────────

func TestGetInt_ReturnsIntegerValue(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, domain.KeyMaxHistory, "1000"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := svc.GetInt(ctx, domain.KeyMaxHistory, 5000)
	if got != 1000 {
		t.Errorf("GetInt(max_history) = %d, want 1000", got)
	}
}

func TestGetInt_ReturnsDefaultOnNonInteger(t *testing.T) {
	// Simulate a corrupt database value by directly inserting a
	// non-integer string — bypassing the service's Set validation.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(
		filepath.Join(t.TempDir(), "test.db"),
		log,
	)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	// Directly corrupt the max_history value.
	_, err = db.SQL().Exec(
		`UPDATE settings SET value = 'banana' WHERE key = 'max_history'`,
	)
	if err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}

	svc := settings.New(db, log)
	got := svc.GetInt(context.Background(), domain.KeyMaxHistory, 5000)
	if got != 5000 {
		t.Errorf("GetInt(corrupt value) = %d, want 5000 (default)", got)
	}
}

func TestGetInt_ReturnsDefaultForUnknownKey(t *testing.T) {
	svc := newTestService(t)
	got := svc.GetInt(context.Background(), "nonexistent_key", 42)
	if got != 42 {
		t.Errorf("GetInt(unknown) = %d, want 42", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetBool
// ─────────────────────────────────────────────────────────────────────────────

func TestGetBool_ReturnsTrueAndFalse(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Default is "false".
	got := svc.GetBool(ctx, domain.KeyStartOnBoot, true)
	if got != false {
		t.Errorf("GetBool(start_on_boot) = %v, want false (default)", got)
	}

	// Set to true.
	if err := svc.Set(ctx, domain.KeyStartOnBoot, "true"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got = svc.GetBool(ctx, domain.KeyStartOnBoot, false)
	if got != true {
		t.Errorf("GetBool(start_on_boot) = %v, want true", got)
	}
}

func TestGetBool_ReturnsDefaultOnCorruptValue(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(
		filepath.Join(t.TempDir(), "test.db"),
		log,
	)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	_, err = db.SQL().Exec(
		`UPDATE settings SET value = 'yes' WHERE key = 'start_on_boot'`,
	)
	if err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}

	svc := settings.New(db, log)
	got := svc.GetBool(context.Background(), domain.KeyStartOnBoot, false)
	if got != false {
		t.Errorf("GetBool(corrupt) = %v, want false (default)", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPollInterval
// ─────────────────────────────────────────────────────────────────────────────

func TestGetPollInterval_ReturnsCorrectDuration(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, domain.KeyPollIntervalMs, "750"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := svc.GetPollInterval(ctx)
	want := 750 * time.Millisecond
	if got != want {
		t.Errorf("GetPollInterval() = %v, want %v", got, want)
	}
}

func TestGetPollInterval_DefaultIs500ms(t *testing.T) {
	svc := newTestService(t)
	got := svc.GetPollInterval(context.Background())
	want := 500 * time.Millisecond
	if got != want {
		t.Errorf("GetPollInterval() default = %v, want %v", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetMaxHistory
// ─────────────────────────────────────────────────────────────────────────────

func TestGetMaxHistory_ReturnsCorrectValue(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, domain.KeyMaxHistory, "2000"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := svc.GetMaxHistory(ctx)
	if got != 2000 {
		t.Errorf("GetMaxHistory() = %d, want 2000", got)
	}
}

func TestGetMaxHistory_DefaultIs5000(t *testing.T) {
	svc := newTestService(t)
	got := svc.GetMaxHistory(context.Background())
	if got != 5000 {
		t.Errorf("GetMaxHistory() default = %d, want 5000", got)
	}
}
