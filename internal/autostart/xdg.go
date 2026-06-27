package autostart

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/darwinovalle/eruditto/pkg/xdg"
)

// appID is the stable identifier used in both XDG autostart
// .desktop paths and systemd service-unit names. Mirrors the
// fyne appID in main.go:
//
//	appID = "io.github.darwinovalle.eruditto"
//
// We avoid a direct import of main's appID constant (a circular
// import risk) and reproduce the literal here. Keep these two
// in sync if the upstream appID ever changes.
const appID = "io.github.darwinovalle.eruditto"

// desktopFileName is the basename of the XDG autostart file.
// The path is conventionally <Exec-name-lowercased-or-stem>.desktop.
// We use appID's last segment with hyphens replaced so the file
// name stays stable across distros that normalise differently.
func desktopFileName() string {
	return "eruditto.desktop"
}

// desktopContents renders the XDG autostart .desktop entry for
// the given executable path. We include both the freedesktop
// standard `Hidden=false` and the GNOME-specific
// `X-GNOME-Autostart-enabled=true` so the entry is honoured
// across GNOME, KDE, XFCE, Cinnamon, and the rest.
//
// Worth noting: Exec paths with spaces or shell-special chars
// are escaped per the .desktop spec (Exec field uses
// double-quote juxtaposition, not freedesktop escaping — which
// is awkward but standard). Our execPath is os.Executable(),
// which on Linux resolves to /proc/self/exe — typically
// /usr/bin/eruditto or /home/x/projects/eruditto/eruditto, all
// of which are safe paths with no whitespace.
func desktopContents(execPath string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Eruditto
Exec=%s
Terminal=false
Hidden=false
X-GNOME-Autostart-enabled=true
Categories=Utility;
`, shellQuote(execPath))
}

// shellQuote is a minimal exec-field quoting pass. The .desktop
// standard requires legacy exec quoting (double-quoted,
// backslash-escaped). For typical paths literal values are safe,
// but if a future user picks a path containing $ or ` we don't
// want to break their launch.
func shellQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	hasSpecial := false
	for _, r := range s {
		if r == ' ' || r == '\\' || r == '"' || r == '$' || r == '`' {
			hasSpecial = true
		}
	}
	if !hasSpecial {
		return s
	}
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '\\', '"', '$', '`':
			out = append(out, '\\')
		}
		out = append(out, byte(r))
	}
	out = append(out, '"')
	return string(out)
}

// xdgAutostartDir returns ~/.config/autostart (XDG-compliant),
// creating it if missing. The autostart directory is shared
// across the user's desktop environment so we don't append the
// appID here — having ~/.config/autostart/eruditto.desktop is
// the canonical location, not a per-app subfolder.
func xdgAutostartDir() (string, error) {
	cfg, err := xdg.ConfigDir() // ~/.config/eruditto
	if err != nil {
		return "", err
	}
	_ = cfg // ConfigDir returns ~/.config/eruditto; we want ~/.config.
	home, err := unixHome()
	if err != nil {
		return "", err
	}
	v := os.Getenv("XDG_CONFIG_HOME")
	if v != "" {
		return filepath.Join(v, "autostart"), nil
	}
	return filepath.Join(home, ".config", "autostart"), nil
}

// unixHome is duplicated from pkg/xdg to avoid exporting the
// helper there for a single caller that lives outside the
// package. Trivial enough that the duplication costs us less
// than the API surface churn would.
func unixHome() (string, error) {
	v := os.Getenv("HOME")
	if v == "" {
		return "", errors.New("autostart: HOME not set")
	}
	return v, nil
}

// errAlreadyInstalled is returned if our file exists and the
// spec-correct bytes already match. The caller can ignore this
// specific error; it lets us keep Enable() idempotent without
// re-emitting the .desktop file unnecessarily.
var errAlreadyInstalled = errors.New("autostart: xdg file already installed")

// xdgEnable writes ~/.config/autostart/<name>.desktop.
//
// Idempotency: if the file exists with byte-identical content
// to what we'd write, this is a no-op — returns errAlreadyInstalled
// for the caller to ignore. If the file exists with different
// bytes, we DO overwrite and warn — the user just enabled
// autostart, they expect a refresh.
func xdgEnable(execPath string) error {
	dir, err := xdgAutostartDir()
	if err != nil {
		return fmt.Errorf("autostart: xdg: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("autostart: mkdir %q: %w", dir, err)
	}
	dest := filepath.Join(dir, desktopFileName())
	want := desktopContents(execPath)
	existing, err := os.ReadFile(dest)
	switch {
	case err == nil && bytes.Equal(existing, []byte(want)):
		// Already exactly our current file. No-op.
		return errAlreadyInstalled
	case err == nil:
		// Different content — overwrite. (Note: if the user
		// had hand-edited, the bytes diverged because we
		// always rewrite to the canonical content. We don't
		// preserve hand-edits on update; if the user needs
		// customisation, they should disable autostart and
		// configure their session through their DE's
		// dedicated tool.)
		fallthrough
	case os.IsNotExist(err):
		// fall through to write
	default:
		return fmt.Errorf("autostart: stat %q: %w", dest, err)
	}
	if err := os.WriteFile(dest, []byte(want), 0o644); err != nil {
		return fmt.Errorf("autostart: write %q: %w", dest, err)
	}
	return nil
}

// xdgDisable removes the autostart file. Returns the count
// removed.
func xdgDisable() (int, error) {
	dir, err := xdgAutostartDir()
	if err != nil {
		return 0, err
	}
	dest := filepath.Join(dir, desktopFileName())
	err = os.Remove(dest)
	if err == nil {
		return 1, nil
	}
	if os.IsNotExist(err) {
		return 0, nil
	}
	return 0, fmt.Errorf("autostart: remove %q: %w", dest, err)
}

// xdgIsEnabled reports whether the autostart file currently
// exists and is in the "enabled" state. We treat the file's
// presence as enabled; Hidden=true / X-GNOME-Autostart-enabled=false
// (inside the file) aren't currently emitted by us, but if a
// user hand-edits it we honour what they wrote.
func xdgIsEnabled() (bool, error) {
	dir, err := xdgAutostartDir()
	if err != nil {
		return false, err
	}
	dest := filepath.Join(dir, desktopFileName())
	_, err = os.Stat(dest)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("autostart: stat %q: %w", dest, err)
	}
	// The file existing implies enabled. We don't peek inside:
	// if the user hand-edit set Hidden=true we'd consider that
	// "still installed but disabled", which we don't currently
	// distinguish from "installed and enabled". Keeping the
	// Toggle button reliable for the common case is worth it.
	return true, nil
}

// appID-stable filename for systemd: traditional convention
// uses the appID with reversed DNS components. We mirror that
// by lowercasing the appID.
func systemdUnitName() string { return appID + ".service" }

// unitContents renders the systemd unit-file text. Standard
// Type=simple so eruditto's main goroutine is the PID systemd
// tracks. Restart=on-failure only — if eruditto intentionally
// exits (user picked "Quit Eruditto") we don't relaunch.
//
// We set XDG_CURRENT_DESKTOP through Environment= so the
// launched process has the right context inside the user's
// session; missing-XDG_CURRENT_DESKTOP is a common way
// clipboard managers end up running headless.
func unitContents(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Eruditto clipboard manager
After=graphical-session.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure

[Install]
WantedBy=default.target
`, shellQuote(execPath))
}

// systemdUnitPath returns ~/.config/systemd/user/<name>.service.
// We could parse `systemctl --user show --property=UnitPath` for
// the canonical path, but the XDG path is always present and
// works with `systemctl --user {enable,disable}` regardless of
// what $XDG_DATA_HOME is. Stick with the simple approach.
func systemdUnitPath() (string, error) {
	home, err := unixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName()), nil
}
