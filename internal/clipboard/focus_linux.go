//go:build linux

package clipboard

// Window-focus helpers used by auto-paste.
//
// Why these live here: the popup layer captures the active window
// ID when it opens (so it knows which window was focused before
// the popup stole focus) and reactivates that window before
// sending ctrl+v. Without explicit reactivation, the X11 window
// manager may take a variable amount of time (or may never, on
// some compositors) to return focus to the previously-focused
// window after the popup closes — and ctrl+v sent into the void
// is silently swallowed.
//
// We shell out to xdotool because the alternatives are worse:
//   - cgo + libX11 adds a heavy native dependency.
//   - Reading _NET_ACTIVE_WINDOW via a pure-Go X client requires
//     shipping an X11 protocol implementation.
// xdotool is already a dependency of the image-read path
// (image_linux.go) so we are not introducing a new requirement.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// CaptureActiveWindowID returns the X11 window ID of the currently
// focused window, as reported by `xdotool getactivewindow`.
//
// Returns an empty string and a non-nil error if xdotool is not
// installed, if there is no display server, or if no window is
// currently focused. Callers should treat an empty result as "we
// could not determine the previous window — skip focus restoration
// and fall back to the time-based sleep".
func CaptureActiveWindowID() (string, error) {
	cmd := exec.Command("xdotool", "getactivewindow")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("focus: getactivewindow: %w", err)
	}
	id := strings.TrimSpace(out.String())
	if id == "" {
		return "", fmt.Errorf("focus: getactivewindow returned empty id")
	}
	return id, nil
}

// ActivateWindow brings the X11 window with the given ID to the
// foreground and blocks until the window manager has confirmed
// the focus change.
//
// The --sync flag is critical: without it, xdotool returns
// immediately after sending the _NET_ACTIVE_WINDOW client message,
// and our subsequent ctrl+v fires before the WM has actually
// transferred focus. With --sync, xdotool waits for the WM to
// confirm the change (or 30s, whichever comes first), so by the
// time this function returns the target window is guaranteed to
// be the active one.
//
// Returns an error if the ID is empty or xdotool fails. An error
// here is non-fatal for auto-paste — the caller logs and falls
// back to the time-based sleep.
func ActivateWindow(windowID string) error {
	if windowID == "" {
		return fmt.Errorf("focus: activate: empty window id")
	}
	cmd := exec.Command("xdotool", "windowactivate", "--sync", windowID)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("focus: windowactivate %q: %w", windowID, err)
	}
	return nil
}
