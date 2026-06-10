// Tests for the hotkeys package.
//
// Important constraint: we cannot test actual hotkey registration in
// CI or in headless environments — it requires a running X11 server
// and would grab system-wide key combinations during the test run.
//
// What we CAN test without a display server:
//   - ParseShortcut: pure function, no I/O
//   - noopManager: all methods must return nil and not panic
//   - New(): returns the right manager type based on env vars
//
// What requires X11 (manual / integration tests only):
//   - x11Manager.Register / Unregister / Close
//   - Actual hotkey firing
package hotkeys_test

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/darwinovalle/eruditto/internal/hotkeys"
)

// ─────────────────────────────────────────────────────────────────────────────
// ParseShortcut tests — pure logic, no display server needed
// ─────────────────────────────────────────────────────────────────────────────

func TestParseShortcut_ValidShortcuts(t *testing.T) {
	tests := []struct {
		input   string
		wantRaw string
	}{
		{"ctrl+shift+v", "ctrl+shift+v"},
		{"ctrl+alt+h", "ctrl+alt+h"},
		{"super+v", "super+v"},
		{"ctrl+v", "ctrl+v"},
		{"shift+alt+f1", "shift+alt+f1"},
		{"ctrl+shift+f12", "ctrl+shift+f12"},
		{"ctrl+shift+1", "ctrl+shift+1"},
		{"ctrl+space", "ctrl+space"},
		{"ctrl+escape", "ctrl+escape"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := hotkeys.ParseShortcut(tt.input)
			if err != nil {
				t.Fatalf("ParseShortcut(%q) unexpected error: %v", tt.input, err)
			}
			if got.Raw != tt.wantRaw {
				t.Errorf("Raw = %q, want %q", got.Raw, tt.wantRaw)
			}
			if len(got.Mods) == 0 {
				t.Error("Mods is empty, want at least one modifier")
			}
		})
	}
}

func TestParseShortcut_InvalidShortcuts(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"no modifier", "v"},
		{"uppercase modifier", "CTRL+V"},
		{"uppercase key", "ctrl+V"},
		{"mixed case", "Ctrl+Shift+V"},
		{"unknown modifier", "cmd+v"},
		{"unknown modifier 2", "windows+v"},
		{"trailing plus", "ctrl+"},
		{"leading plus", "+ctrl+v"},
		{"double plus", "ctrl++v"},
		{"unknown key", "ctrl+shift+zzz"},
		{"modifier only", "ctrl+shift"},
		{"duplicate modifier", "ctrl+ctrl+v"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := hotkeys.ParseShortcut(tt.input)
			if err == nil {
				t.Errorf("ParseShortcut(%q) = nil error, want ErrInvalidShortcut", tt.input)
				return
			}
			if !errors.Is(err, hotkeys.ErrInvalidShortcut) {
				t.Errorf("ParseShortcut(%q) = %v, want wrapping ErrInvalidShortcut", tt.input, err)
			}
		})
	}
}

func TestParseShortcut_ModifierAliases(t *testing.T) {
	// "ctrl" and "control" should both work.
	s1, err := hotkeys.ParseShortcut("ctrl+v")
	if err != nil {
		t.Fatalf("ParseShortcut(ctrl+v): %v", err)
	}

	s2, err := hotkeys.ParseShortcut("control+v")
	if err != nil {
		t.Fatalf("ParseShortcut(control+v): %v", err)
	}

	// Both should produce the same modifier value.
	if len(s1.Mods) != 1 || len(s2.Mods) != 1 {
		t.Fatalf("expected 1 modifier each, got %d and %d", len(s1.Mods), len(s2.Mods))
	}
	if s1.Mods[0] != s2.Mods[0] {
		t.Errorf("ctrl and control produce different modifier values: %v vs %v",
			s1.Mods[0], s2.Mods[0])
	}

	// "alt" and "option" should both work.
	s3, err := hotkeys.ParseShortcut("alt+v")
	if err != nil {
		t.Fatalf("ParseShortcut(alt+v): %v", err)
	}
	s4, err := hotkeys.ParseShortcut("option+v")
	if err != nil {
		t.Fatalf("ParseShortcut(option+v): %v", err)
	}
	if s3.Mods[0] != s4.Mods[0] {
		t.Errorf("alt and option produce different modifier values")
	}
}

func TestParseShortcut_AllLetterKeys(t *testing.T) {
	// Every letter a-z must parse successfully with ctrl modifier.
	for ch := byte('a'); ch <= 'z'; ch++ {
		input := "ctrl+" + string(ch)
		_, err := hotkeys.ParseShortcut(input)
		if err != nil {
			t.Errorf("ParseShortcut(%q) unexpected error: %v", input, err)
		}
	}
}

func TestParseShortcut_NeverPanics(t *testing.T) {
	// Feed adversarial inputs and verify we never panic.
	inputs := []string{
		"",
		"+",
		"++",
		"+++",
		"ctrl",
		"CTRL+SHIFT+V",
		strings.Repeat("ctrl+", 100) + "v",
		"ctrl+\x00",
		"ctrl+shift+v+extra",
	}

	for _, input := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ParseShortcut(%q) panicked: %v", input, r)
				}
			}()
			hotkeys.ParseShortcut(input)
		}()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// noopManager tests — safe to run anywhere, no display server needed
// ─────────────────────────────────────────────────────────────────────────────

func newNoopManager(t *testing.T) hotkeys.HotkeyManager {
	t.Helper()
	// Force Wayland detection by setting WAYLAND_DISPLAY.
	// t.Setenv restores the original value after the test.
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	return hotkeys.New(log)
}

func TestNoopManager_RegisterReturnsNil(t *testing.T) {
	m := newNoopManager(t)
	sc, err := hotkeys.ParseShortcut("ctrl+shift+v")
	if err != nil {
		t.Fatalf("ParseShortcut: %v", err)
	}
	if err := m.Register(sc, func() {}); err != nil {
		t.Errorf("Register on noopManager = %v, want nil", err)
	}
}

func TestNoopManager_UnregisterReturnsNil(t *testing.T) {
	m := newNoopManager(t)
	sc, err := hotkeys.ParseShortcut("ctrl+shift+v")
	if err != nil {
		t.Fatalf("ParseShortcut: %v", err)
	}
	if err := m.Unregister(sc); err != nil {
		t.Errorf("Unregister on noopManager = %v, want nil", err)
	}
}

func TestNoopManager_CloseReturnsNil(t *testing.T) {
	m := newNoopManager(t)
	if err := m.Close(); err != nil {
		t.Errorf("Close on noopManager = %v, want nil", err)
	}
}

func TestNoopManager_MultipleOperationsNoPanic(t *testing.T) {
	m := newNoopManager(t)
	sc, _ := hotkeys.ParseShortcut("ctrl+shift+v")

	// Register, unregister, register again, close — must not panic.
	_ = m.Register(sc, func() {})
	_ = m.Unregister(sc)
	_ = m.Register(sc, func() {})
	_ = m.Close()
}

// ─────────────────────────────────────────────────────────────────────────────
// New() constructor tests
// ─────────────────────────────────────────────────────────────────────────────

func TestNew_WaylandReturnsWorkingManager(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	m := hotkeys.New(log)
	if m == nil {
		t.Fatal("New() returned nil on Wayland")
	}

	// The manager must be usable (noop, but not nil).
	sc, _ := hotkeys.ParseShortcut("ctrl+shift+v")
	if err := m.Register(sc, func() {}); err != nil {
		t.Errorf("Register on Wayland manager = %v, want nil", err)
	}
	_ = m.Close()
}

func TestNew_NoDisplayReturnsWorkingManager(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	m := hotkeys.New(log)
	if m == nil {
		t.Fatal("New() returned nil with no display")
	}
	_ = m.Close()
}
