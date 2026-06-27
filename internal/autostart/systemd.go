package autostart

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// systemd author probe: `systemctl --user is-enabled <unit>`
// exits 0 if the unit is enabled, 1 if disabled, "not-found"
// rc etc. We use that as the source of truth for the systemd
// layer's state — file presence is not enough; the unit must
// be `enable`d for it to actually start on next session.
func systemctlUser(args ...string) (string, error) {
	full := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// systemdUnitInstalled reports whether the unit file exists on
// disk at the path computed by systemdUnitPath(). Returns
// (true, nil) when present. We DO NOT distinguish between a
// file that has been enabled vs file-only.
func systemdUnitInstalled() (bool, error) {
	p, err := systemdUnitPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("autostart: stat unit %q: %w", p, err)
	}
	return true, nil
}

// systemdUnitEnabled reports whether the unit file is `enable`d
// for the user's session. We treat "static" / "alias" /
// "generated" / "indirect" as "enabled" because those reflect a
// state where systemd will start the unit on appropriate
// targets™. We treat "disabled" / "not-found" as "not enabled".
// Anything else falls through to enabled=true conservatively
// — if systemctl works but the verb returned something we
// don't recognise, we err on the side of "we believe systemd
// intends to start it" so the Enable/Disable state machine
// stays consistent with reality.
func systemdUnitEnabled() (bool, error) {
	out, err := systemctlUser("is-enabled", systemdUnitName())
	switch strings.TrimSpace(out) {
	case "enabled", "static", "alias", "generated", "indirect":
		return true, nil
	case "disabled", "not-found":
		return false, nil
	}
	if err == nil {
		// Unknown state but no error → assume enabled.
		return true, nil
	}
	// systemctl returned non-zero. Could be transient (dbus
	// unavailable in some headless containers). Surface as
	// "unknown" via error so the caller / user can decide.
	return false, fmt.Errorf("autostart: systemctl is-enabled: %v: %s", err, out)
}

// systemdEnable writes the unit file, runs `daemon-reload`,
// then `enable`. Returns nil on success.
//
// Important ordering: we have to write the unit BEFORE calling
// `daemon-reload` because systemd learns about the unit by
// scanning its unit path. Reload-after-write is the canonical
// sequence; after reload the `enable` resolves the symlink
// chain under default.target.wants → our unit file.
//
// We swallow transient failures from `daemon-reload` and
// `enable`: the file is on disk and a retry on next launch
// will succeed. But we surface real errors so the user can
// diagnose.
func systemdEnable(execPath string) error {
	p, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(strings.TrimSuffix(p, "/"+systemdUnitName()), 0o755); err != nil {
		return fmt.Errorf("autostart: mkdir systemd unit dir: %w", err)
	}
	if err := os.WriteFile(p, []byte(unitContents(execPath)), 0o644); err != nil {
		return fmt.Errorf("autostart: write unit %q: %w", p, err)
	}
	if _, err := systemctlUser("daemon-reload"); err != nil {
		// Continue anyway — was-active installs of `enable`
		// fail with "Failed to execute operation: No such
		// file or directory" at the time daemon-reload is
		// needed. Calling enable twice in a row picks up
		// the new file.
		_, _ = systemctlUser("daemon-reload")
	}
	if _, err := systemctlUser("enable", systemdUnitName()); err != nil {
		return fmt.Errorf("autostart: systemctl enable: %w", err)
	}
	return nil
}

// systemdDisable does `disable` then removes the unit file.
// Removing the file after disable is important — otherwise
// the unit lingers in the filesystem and a future
// `daemon-reload` would re-pick it up. Order matters: disable
// first removes the symlink under default.target.wants/ so
// systemd stops tracking the unit.
func systemdDisable() error {
	// It's fine if the unit wasn't enabled.
	_, _ = systemctlUser("disable", systemdUnitName())
	p, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("autostart: remove unit %q: %w", p, err)
	}
	return nil
}

// ErrSystemdNoSession signals that systemd --user is
// unavailable in the current environment. The most common
// cause is running from a system shell without a user
// session bus (e.g. sshd without PAM/user@uid). Callers should
// skip systemd and fall through to the XDG path.
var errSystemdNoSession = errors.New("autostart: no systemd --user session available")

// systemdAvailable probes whether the systemd user manager is
// reachable. We do this with a cheap `systemctl --user status`
// which exits non-zero if there's no user dbus session. The
// probe is best-effort — false-positives (we think systemd is
// unavailable when it isn't) are acceptable because we
// fall back to XDG, which is the universally-available path.
func systemdAvailable() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_, err := systemctlUser("status")
	if err == nil {
		return true
	}
	// exit-code 1+ from `status` typically indicates the
	// user manager is up but no services are running, which
	// is still "available" from our POV.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	return false
}
