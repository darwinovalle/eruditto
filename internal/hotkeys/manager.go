// Package hotkeys provides global keyboard shortcut registration for
// Eruditto. A global hotkey fires even when the application window
// does not have focus — it is what lets the user press Ctrl+Shift+V
// in any application and have Eruditto's popup appear.
//
// Display server compatibility:
//
//	X11:     Full support via golang.design/x/hotkey.
//	         The hotkey is registered with the X server and fires
//	         regardless of which window has focus.
//
//	Wayland: Global hotkeys are blocked by the Wayland security model.
//	         Unprivileged applications cannot intercept keyboard events
//	         system-wide. We detect Wayland at startup, log a clear
//	         explanation, send a desktop notification via notify-send,
//	         and return a no-op manager that never crashes.
//
// Architecture:
//
//	HotkeyManager is an interface. main.go calls New() which inspects
//	the environment and returns either an x11Manager or a noopManager.
//	All other code is written against the interface — no if/else for
//	display servers anywhere except in New().
package hotkeys

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"golang.design/x/hotkey"
)

// ─────────────────────────────────────────────────────────────────────────────
// Errors
// ─────────────────────────────────────────────────────────────────────────────

// ErrInvalidShortcut is returned by ParseShortcut when the shortcut
// string does not match the expected format.
var ErrInvalidShortcut = errors.New("hotkeys: invalid shortcut string")

// ErrAlreadyRegistered is returned when Register is called for a
// shortcut that is already active.
var ErrAlreadyRegistered = errors.New("hotkeys: shortcut already registered")

// ErrNotRegistered is returned when Unregister is called for a
// shortcut that was never registered.
var ErrNotRegistered = errors.New("hotkeys: shortcut not registered")

// ─────────────────────────────────────────────────────────────────────────────
// Shortcut — parsed representation of a hotkey string
// ─────────────────────────────────────────────────────────────────────────────

// Shortcut holds a parsed hotkey combination ready to be registered.
// It is the output of ParseShortcut and the input to Register.
type Shortcut struct {
	// Raw is the original string, e.g. "ctrl+shift+v".
	// Stored for logging and display in the settings UI.
	Raw string

	// Mods is the list of modifier keys in the combination.
	Mods []hotkey.Modifier

	// Key is the non-modifier key in the combination.
	Key hotkey.Key
}

// String returns the raw shortcut string.
func (s Shortcut) String() string {
	return s.Raw
}

// ─────────────────────────────────────────────────────────────────────────────
// HotkeyManager interface
// ─────────────────────────────────────────────────────────────────────────────

// HotkeyManager registers and unregisters global keyboard shortcuts.
//
// Implementations:
//   - x11Manager:  registers real X11 hotkeys (X11 sessions)
//   - noopManager: does nothing gracefully (Wayland sessions)
//
// All methods are safe to call from any goroutine.
type HotkeyManager interface {
	// Register registers shortcut and calls handler in a new goroutine
	// each time the shortcut is pressed. handler must be non-nil.
	// Returns ErrAlreadyRegistered if the shortcut is active.
	Register(shortcut Shortcut, handler func()) error

	// Unregister deactivates a previously registered shortcut.
	// Returns ErrNotRegistered if the shortcut was not active.
	Unregister(shortcut Shortcut) error

	// Close releases all registered hotkeys and frees resources.
	// Must be called exactly once when the application exits.
	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// Constructor — detects display server and returns the right manager
// ─────────────────────────────────────────────────────────────────────────────

// New detects the display server and returns the appropriate
// HotkeyManager implementation.
//
// Detection logic:
//   - WAYLAND_DISPLAY set and non-empty → noopManager
//   - DISPLAY set and non-empty         → x11Manager
//   - Neither set                       → noopManager with a warning
//
// The returned manager is ready to use immediately.
func New(log *slog.Logger) HotkeyManager {
	if isWayland() {
		log.Warn("hotkeys: Wayland detected — global hotkeys are not supported",
			"reason", "Wayland security model blocks unprivileged global key interception",
			"workaround", "bind your hotkey in System Settings → Keyboard → Custom Shortcuts",
			"command", "eruditto --popup",
		)
		sendWaylandNotification(log)
		return &noopManager{log: log}
	}

	if !isX11() {
		log.Warn("hotkeys: no display server detected — global hotkeys disabled",
			"DISPLAY", os.Getenv("DISPLAY"),
			"WAYLAND_DISPLAY", os.Getenv("WAYLAND_DISPLAY"),
		)
		return &noopManager{log: log}
	}

	log.Info("hotkeys: X11 detected — global hotkeys enabled",
		"DISPLAY", os.Getenv("DISPLAY"),
	)
	return &x11Manager{
		log:     log,
		hotkeys: make(map[string]*registeredHotkey),
	}
}

// isWayland returns true when running under a Wayland compositor.
func isWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// isX11 returns true when running under an X11 server.
func isX11() bool {
	return os.Getenv("DISPLAY") != ""
}

// sendWaylandNotification sends a desktop notification via notify-send
// telling the user how to configure the hotkey manually.
//
// Why notify-send via os/exec instead of a Go notification library?
//   - notify-send is pre-installed on every Ubuntu desktop
//   - Zero new dependencies
//   - Works with GNOME, KDE, XFCE, and all other DEs
//   - The notification appears in the system notification centre
//
// We do not error-check the result. If notify-send is not available,
// the log warning is still there. We never crash over a notification.
func sendWaylandNotification(log *slog.Logger) {
	cmd := exec.Command("notify-send",
		"--icon=dialog-information",
		"--urgency=normal",
		"Eruditto — Wayland Hotkey Setup Required",
		"Global hotkeys are not available on Wayland.\n\n"+
			"To use Ctrl+Shift+V:\n"+
			"Open System Settings → Keyboard → Custom Shortcuts\n"+
			"Add a shortcut with command: eruditto --popup",
	)

	if err := cmd.Run(); err != nil {
		// notify-send not available or failed — log and continue.
		log.Debug("hotkeys: notify-send unavailable", "error", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// x11Manager — real implementation for X11 sessions
// ─────────────────────────────────────────────────────────────────────────────

// registeredHotkey holds a live hotkey and its stop channel.
type registeredHotkey struct {
	hk   *hotkey.Hotkey
	stop chan struct{}
}

// x11Manager registers global hotkeys via golang.design/x/hotkey.
type x11Manager struct {
	log     *slog.Logger
	mu      sync.Mutex
	hotkeys map[string]*registeredHotkey // key: shortcut.Raw
}

// Register registers shortcut and starts listening for it.
//
// The handler is called in a dedicated goroutine each time the
// shortcut fires. Multiple rapid presses before the handler returns
// are handled by the goroutine — we do not queue them.
func (m *x11Manager) Register(shortcut Shortcut, handler func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.hotkeys[shortcut.Raw]; exists {
		return fmt.Errorf("%w: %q", ErrAlreadyRegistered, shortcut.Raw)
	}

	hk := hotkey.New(shortcut.Mods, shortcut.Key)
	if err := hk.Register(); err != nil {
		return fmt.Errorf("hotkeys: register %q: %w", shortcut.Raw, err)
	}

	stop := make(chan struct{})
	m.hotkeys[shortcut.Raw] = &registeredHotkey{hk: hk, stop: stop}

	// Start the listener goroutine.
	go m.listen(hk, handler, stop, shortcut.Raw)

	m.log.Info("hotkey registered", "shortcut", shortcut.Raw)
	return nil
}

// listen waits for hotkey events and calls handler.
// Exits when stop is closed or the hotkey channel is closed.
func (m *x11Manager) listen(
	hk *hotkey.Hotkey,
	handler func(),
	stop chan struct{},
	raw string,
) {
	for {
		select {
		case <-stop:
			m.log.Debug("hotkey listener stopped", "shortcut", raw)
			return
		case _, ok := <-hk.Keydown():
			if !ok {
				// Channel closed — hotkey was unregistered.
				return
			}
			m.log.Debug("hotkey fired", "shortcut", raw)
			// Call handler in its own goroutine so a slow handler
			// does not block the next keypress event.
			go handler()
		}
	}
}

// Unregister deactivates a registered shortcut.
func (m *x11Manager) Unregister(shortcut Shortcut) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	reg, exists := m.hotkeys[shortcut.Raw]
	if !exists {
		return fmt.Errorf("%w: %q", ErrNotRegistered, shortcut.Raw)
	}

	// Signal the listener goroutine to stop.
	close(reg.stop)

	if err := reg.hk.Unregister(); err != nil {
		return fmt.Errorf("hotkeys: unregister %q: %w", shortcut.Raw, err)
	}

	delete(m.hotkeys, shortcut.Raw)
	m.log.Info("hotkey unregistered", "shortcut", shortcut.Raw)
	return nil
}

// Close unregisters all active hotkeys and stops all listener goroutines.
func (m *x11Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for raw, reg := range m.hotkeys {
		close(reg.stop)
		if err := reg.hk.Unregister(); err != nil {
			errs = append(errs, fmt.Sprintf("%q: %v", raw, err))
		}
		delete(m.hotkeys, raw)
	}

	if len(errs) > 0 {
		return fmt.Errorf("hotkeys: close errors: %s", strings.Join(errs, "; "))
	}

	m.log.Info("hotkey manager closed")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// noopManager — graceful no-op for Wayland / headless sessions
// ─────────────────────────────────────────────────────────────────────────────

// noopManager implements HotkeyManager by doing nothing.
// Used on Wayland and when no display server is detected.
// All methods log at Debug level and return nil errors so callers
// do not need special-case handling.
type noopManager struct {
	log *slog.Logger
}

func (n *noopManager) Register(shortcut Shortcut, _ func()) error {
	n.log.Debug("hotkeys: noop register (Wayland/headless)",
		"shortcut", shortcut.Raw)
	return nil
}

func (n *noopManager) Unregister(shortcut Shortcut) error {
	n.log.Debug("hotkeys: noop unregister (Wayland/headless)",
		"shortcut", shortcut.Raw)
	return nil
}

func (n *noopManager) Close() error {
	n.log.Debug("hotkeys: noop close (Wayland/headless)")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseShortcut — "ctrl+shift+v" → Shortcut
// ─────────────────────────────────────────────────────────────────────────────

// ParseShortcut converts a human-readable shortcut string into a
// Shortcut that can be passed to Register.
//
// Format: modifier+modifier+key (all lowercase, plus-separated)
// Examples: "ctrl+shift+v", "ctrl+alt+h", "super+v"
//
// Rules:
//   - At least one modifier is required
//   - The last segment is always the key
//   - All segments before the last are modifiers
//   - Unknown modifiers return ErrInvalidShortcut
//   - Unknown keys return ErrInvalidShortcut
//   - Uppercase input returns ErrInvalidShortcut (use lowercase)
//   - Empty string returns ErrInvalidShortcut
//
// This function is pure — no I/O, no side effects.
// It is fully testable without a display server.
func ParseShortcut(s string) (Shortcut, error) {
	if s == "" {
		return Shortcut{}, fmt.Errorf("%w: empty string", ErrInvalidShortcut)
	}

	// Enforce lowercase. The domain validator also enforces this, but
	// ParseShortcut is called independently (e.g., from tests) so we
	// validate here too.
	if s != strings.ToLower(s) {
		return Shortcut{}, fmt.Errorf(
			"%w: %q must be lowercase (got uppercase characters)",
			ErrInvalidShortcut, s,
		)
	}

	parts := strings.Split(s, "+")
	if len(parts) < 2 {
		return Shortcut{}, fmt.Errorf(
			"%w: %q must have at least one modifier and one key",
			ErrInvalidShortcut, s,
		)
	}

	// Validate no empty segments (catches "ctrl+" or "ctrl++v").
	for i, p := range parts {
		if p == "" {
			return Shortcut{}, fmt.Errorf(
				"%w: %q has empty segment at position %d",
				ErrInvalidShortcut, s, i,
			)
		}
	}

	// Last segment is the key; everything before it is a modifier.
	modStrings := parts[:len(parts)-1]
	keyString := parts[len(parts)-1]

	// Parse modifiers.
	mods, err := parseModifiers(modStrings, s)
	if err != nil {
		return Shortcut{}, err
	}

	// Parse key.
	key, err := parseKey(keyString, s)
	if err != nil {
		return Shortcut{}, err
	}

	return Shortcut{
		Raw:  s,
		Mods: mods,
		Key:  key,
	}, nil
}

// parseModifiers converts modifier name strings to hotkey.Modifier values.
func parseModifiers(names []string, raw string) ([]hotkey.Modifier, error) {
	modMap := map[string]hotkey.Modifier{
		"ctrl":    hotkey.ModCtrl,
		"control": hotkey.ModCtrl,
		"shift":   hotkey.ModShift,
		"alt":     hotkey.Mod1,
		"option":  hotkey.Mod1,
		"super":   hotkey.Mod4,
		"meta":    hotkey.Mod4,
		"win":     hotkey.Mod4,
	}

	seen := make(map[string]bool)
	var mods []hotkey.Modifier

	for _, name := range names {
		mod, ok := modMap[name]
		if !ok {
			return nil, fmt.Errorf(
				"%w: %q has unknown modifier %q (known: ctrl, shift, alt, super)",
				ErrInvalidShortcut, raw, name,
			)
		}
		// Deduplicate: "ctrl+ctrl+v" is a user error.
		canonical := fmt.Sprintf("%d", mod)
		if seen[canonical] {
			return nil, fmt.Errorf(
				"%w: %q has duplicate modifier %q",
				ErrInvalidShortcut, raw, name,
			)
		}
		seen[canonical] = true
		mods = append(mods, mod)
	}

	return mods, nil
}

// parseKey converts a key name string to a hotkey.Key value.
//
// We support single lowercase letters (a-z) and a selection of
// named keys (F1-F12, arrow keys, etc.).
func parseKey(name, raw string) (hotkey.Key, error) {
	// Single lowercase letter: a-z
	if len(name) == 1 {
		ch := name[0]
		if ch >= 'a' && ch <= 'z' {
			// hotkey.Key values for letters follow the ASCII uppercase
			// values on most platforms. golang.design/x/hotkey defines
			// KeyA through KeyZ as constants.
			key, ok := letterKeys[ch]
			if ok {
				return key, nil
			}
		}
	}

	// Named keys.
	if key, ok := namedKeys[name]; ok {
		return key, nil
	}

	return 0, fmt.Errorf(
		"%w: %q has unknown key %q",
		ErrInvalidShortcut, raw, name,
	)
}

// letterKeys maps lowercase ASCII letters to hotkey.Key constants.
var letterKeys = map[byte]hotkey.Key{
	'a': hotkey.KeyA, 'b': hotkey.KeyB, 'c': hotkey.KeyC,
	'd': hotkey.KeyD, 'e': hotkey.KeyE, 'f': hotkey.KeyF,
	'g': hotkey.KeyG, 'h': hotkey.KeyH, 'i': hotkey.KeyI,
	'j': hotkey.KeyJ, 'k': hotkey.KeyK, 'l': hotkey.KeyL,
	'm': hotkey.KeyM, 'n': hotkey.KeyN, 'o': hotkey.KeyO,
	'p': hotkey.KeyP, 'q': hotkey.KeyQ, 'r': hotkey.KeyR,
	's': hotkey.KeyS, 't': hotkey.KeyT, 'u': hotkey.KeyU,
	'v': hotkey.KeyV, 'w': hotkey.KeyW, 'x': hotkey.KeyX,
	'y': hotkey.KeyY, 'z': hotkey.KeyZ,
}

// namedKeys maps named key strings to hotkey.Key constants.
var namedKeys = map[string]hotkey.Key{
	"f1": hotkey.KeyF1, "f2": hotkey.KeyF2, "f3": hotkey.KeyF3,
	"f4": hotkey.KeyF4, "f5": hotkey.KeyF5, "f6": hotkey.KeyF6,
	"f7": hotkey.KeyF7, "f8": hotkey.KeyF8, "f9": hotkey.KeyF9,
	"f10": hotkey.KeyF10, "f11": hotkey.KeyF11, "f12": hotkey.KeyF12,
	"return": hotkey.KeyReturn, "space": hotkey.KeySpace,
	"tab": hotkey.KeyTab, "escape": hotkey.KeyEscape,
	"0": hotkey.Key0, "1": hotkey.Key1, "2": hotkey.Key2,
	"3": hotkey.Key3, "4": hotkey.Key4, "5": hotkey.Key5,
	"6": hotkey.Key6, "7": hotkey.Key7, "8": hotkey.Key8,
	"9": hotkey.Key9,
}
