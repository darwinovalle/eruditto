//go:build !linux

package ui

// positionNearCursor fallback for non-Linux platforms.
//
// Windows/macOS should rely on the platform-native
// "follow mouse" or "centre on active screen" semantics
// instead of manual coordinates. We return a sane
// top-left placeholder; production builds are linux-only
// for now (per the project README).
func positionNearCursor(winWidth, winHeight float32) (int, int) {
	return 100, 100
}

// positionCenterScreen is the centred fallback for non-Linux
// platforms. Same rationale as positionNearCursor.
func positionCenterScreen(winWidth, winHeight float32) (int, int) {
	return 100, 100
}

// moveWindow is a no-op on non-Linux platforms.
func moveWindow(x, y int) {}

// showAndPosition shows the window at the default position
// when mouse-tracking or centred mode is not available.
// Fyne on each platform will centre the window on the
// primary monitor if we don't issue explicit coordinates.
func (p *PopupWindow) showAndPosition() {
	p.win.Show()
	p.win.RequestFocus()
}
