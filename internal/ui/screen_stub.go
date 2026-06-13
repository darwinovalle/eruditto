//go:build !linux

package ui

import "time"

// positionNearCursor fallback for non-Linux platforms.
func positionNearCursor(winWidth, winHeight float32) (int, int) {
	return 100, 100
}

// moveWindow is a no-op on non-Linux platforms.
func moveWindow(x, y int) {}

// showAndPosition shows the window at a default position.
func (p *PopupWindow) showAndPosition() {
	p.win.Show()
	p.win.RequestFocus()
}
