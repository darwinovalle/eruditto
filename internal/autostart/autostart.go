package autostart

import (
	"errors"
	"fmt"
)

// Method identifies which autostart backing store is in use
// for the active "launch at login" preference.
type Method string

const (
	MethodXDG     Method = "xdg"     // XDG autostart .desktop
	MethodSystemd Method = "systemd" // systemd --user unit
	MethodNone    Method = "none"    // neither (preference=false or no execPath)
)

// ErrAlreadyDone is returned by Enable when the autostart file
// is already byte-identical. Callers may ignore this; it lets
// the call stay idempotent without re-emitting crosstalk.
var ErrAlreadyDone = errors.New("autostart: already enabled")

// Enable turns on login-launch for the given executable.
//
// Order:
//  1. XDG autostart .desktop — works on every freedesktop DE.
//  2. systemd --user unit — best-effort fallback.
//
// We DO NOT enable both paths simultaneously — that creates
// two eruditto instances on login. systemd path is used only if
// XDG is unavailable (rare edge cases, mostly headless).
func Enable(execPath string) error {
	if err := xdgEnable(execPath); err == nil {
		return nil
	} else if !errors.Is(err, ErrAlreadyDone) && !isPermission(err) {
		return fmt.Errorf("autostart: xdg enable failed: %w", err)
	} else if errors.Is(err, ErrAlreadyDone) {
		return ErrAlreadyDone
	}
	if !systemdAvailable() {
		return fmt.Errorf("autostart: xdg enable failed and systemd unavailable")
	}
	if err := systemdEnable(execPath); err != nil {
		return err
	}
	return nil
}

// isPermission keeps the fallback path narrow: filesystem
// permission errors should NOT trigger the systemd fallback
// (same user wrote / wrote it; fallback won't help).
func isPermission(err error) bool { return false }

// Disable removes any eruditto autostart entry from both
// systems we may have written to. Returns the count of files
// removed.
func Disable() (int, error) {
	n, err := xdgDisable()
	if err != nil {
		return n, err
	}
	if systemdAvailable() {
		if err := systemdDisable(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// IsEnabled reports whether any of the autostart paths are
// currently engaged. We check XDG first because it's the
// canonical answer.
func IsEnabled() (bool, error) {
	xdg, err := xdgIsEnabled()
	if err != nil {
		return false, err
	}
	if xdg {
		return true, nil
	}
	if systemdAvailable() {
		return systemdUnitEnabled()
	}
	return false, nil
}

// Method returns which autostart path is currently in use.
// Useful for diagnostics in the settings window or logs.
func MethodInUse() Method {
	if ok, err := xdgIsEnabled(); err == nil && ok {
		return MethodXDG
	}
	if systemdAvailable() {
		if ok, _ := systemdUnitEnabled(); ok {
			return MethodSystemd
		}
	}
	return MethodNone
}

// Reconcile synchronises the system with the persisted
// preference. Called at startup; idempotent across runs. If
// preference=true but the file is missing, re-create. If
// preference=false, remove any stale entry.
//
// execPath is normally os.Executable() — passed through to
// the file timestamps so the autostart unit points at the
// actual binary the user just ran.
func Reconcile(preference bool, execPath string) error {
	currentlyEnabled, err := IsEnabled()
	if err != nil {
		return err
	}
	switch {
	case preference && !currentlyEnabled:
		return Enable(execPath)
	case !preference && currentlyEnabled:
		_, err := Disable()
		return err
	default:
		return nil
	}
}
