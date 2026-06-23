//go:build !linux

package clipboard

import "errors"

// ErrFocusUnsupported is returned by focus helpers on non-Linux
// platforms. Auto-paste on macOS/Windows would need a different
// mechanism entirely (AppleScript / SendInput).
var ErrFocusUnsupported = errors.New("focus: window-focus helpers are Linux-only")

// CaptureActiveWindowID is a no-op stub on non-Linux platforms.
func CaptureActiveWindowID() (string, error) {
	return "", ErrFocusUnsupported
}

// ActivateWindow is a no-op stub on non-Linux platforms.
func ActivateWindow(windowID string) error {
	return ErrFocusUnsupported
}
