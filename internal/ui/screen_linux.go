//go:build linux

package ui

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
)

// getMousePosition returns the current mouse cursor position (x, y).
func getMousePosition() (int, int, error) {
	out, err := exec.Command("xdotool", "getmouselocation", "--shell").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("xdotool not available: %w", err)
	}

	var x, y int
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "X=") {
			x, _ = strconv.Atoi(strings.TrimPrefix(line, "X="))
		}
		if strings.HasPrefix(line, "Y=") {
			y, _ = strconv.Atoi(strings.TrimPrefix(line, "Y="))
		}
	}
	return x, y, nil
}

// getAllScreens returns all connected screen offsets and dimensions.
func getAllScreens() [][4]int {
	out, err := exec.Command("xrandr", "--current").Output()
	if err != nil {
		return [][4]int{{0, 0, 1920, 1080}}
	}

	var screens [][4]int
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, " connected ") || !strings.Contains(line, "x") {
			continue
		}

		fields := strings.Fields(line)
		for _, f := range fields {
			if !strings.Contains(f, "x") {
				continue
			}

			var resPart string
			if strings.Contains(f, "+") {
				resPart = strings.Split(f, "+")[0]
			} else {
				resPart = f
			}

			dims := strings.Split(resPart, "x")
			if len(dims) != 2 {
				continue
			}

			w, err1 := strconv.Atoi(dims[0])
			h, err2 := strconv.Atoi(dims[1])
			if err1 != nil || err2 != nil {
				continue
			}

			x, y := 0, 0
			if strings.Contains(f, "+") {
				parts := strings.Split(f, "+")
				if len(parts) >= 3 {
					x, _ = strconv.Atoi(parts[1])
					y, _ = strconv.Atoi(parts[2])
				}
			}

			screens = append(screens, [4]int{x, y, w, h})
			break
		}
	}

	if len(screens) == 0 {
		return [][4]int{{0, 0, 1920, 1080}}
	}
	return screens
}

// getScreenForPoint returns the screen containing the point.
func getScreenForPoint(px, py int) [4]int {
	screens := getAllScreens()
	for _, s := range screens {
		if px >= s[0] && px < s[0]+s[2] && py >= s[1] && py < s[1]+s[3] {
			return s
		}
	}
	return screens[0]
}

// positionNearCursor calculates window position near the mouse cursor.
func positionNearCursor(winWidth, winHeight float32) (int, int) {
	mx, my, err := getMousePosition()
	if err != nil {
		screens := getAllScreens()
		s := screens[0]
		return s[0] + s[2]/2 - int(winWidth)/2, s[1] + s[3]/2 - int(winHeight)/2
	}

	screen := getScreenForPoint(mx, my)
	sx, sy, sw, sh := screen[0], screen[1], screen[2], screen[3]

	wx := mx + 20
	wy := my + 20

	if wx+int(winWidth) > sx+sw {
		wx = mx - int(winWidth) - 10
	}
	if wy+int(winHeight) > sy+sh {
		wy = my - int(winHeight) - 10
	}
	if wx < sx {
		wx = sx + 5
	}
	if wy < sy {
		wy = sy + 5
	}

	return wx, wy
}

// positionCenterScreen centres the popup on the screen that
// contains the mouse cursor. Used when the user has disabled
// "popup follows mouse" — the popup still appears on the same
// screen as the user, but in the middle of it instead of next
// to the cursor.
//
// We deliberately read cursor position via xdotool here rather
// than from a captured snapshot: the popup wait (Sleep 50ms in
// showAndPosition) clamps the cursor in place once the window
// has grabbed focus, so a fresh read at the moment we position
// is the most faithful snapshot.
//
// Falls back to the first screen's centre if xdotool fails.
func positionCenterScreen(winWidth, winHeight float32) (int, int) {
	mx, my, err := getMousePosition()
	screens := getAllScreens()

	if err != nil {
		s := screens[0]
		return s[0] + s[2]/2 - int(winWidth)/2,
			s[1] + s[3]/2 - int(winHeight)/2
	}

	screen := getScreenForPoint(mx, my)
	sx, sy, sw, sh := screen[0], screen[1], screen[2], screen[3]

	wx := sx + sw/2 - int(winWidth)/2
	wy := sy + sh/2 - int(winHeight)/2

	if wx < sx {
		wx = sx + 5
	}
	if wy < sy {
		wy = sy + 5
	}
	// Clamp to screen bounds in case the popup is wider than
	// the screen on the right edge.
	if wx+int(winWidth) > sx+sw {
		wx = sx + sw - int(winWidth) - 5
		if wx < sx {
			wx = sx
		}
	}
	if wy+int(winHeight) > sy+sh {
		wy = sy + sh - int(winHeight) - 5
		if wy < sy {
			wy = sy
		}
	}

	return wx, wy
}

// moveWindow moves the active window (the popup) to the given position.
func moveWindow(x, y int) {
	exec.Command("xdotool", "getactivewindow", "windowmove", "--",
		strconv.Itoa(x), strconv.Itoa(y)).Run()
}

// showAndPosition shows the window and moves it to the correct position.
//
// Two modes, controlled by the
// domain.KeyPopupMouseTracking setting:
//
//   - "true" (default): the popup follows the mouse cursor and
//     lands a small offset from it on the cursor's screen.
//   - "false": the popup is centred on the screen that contains
//     the mouse cursor.
//
// In both modes the screen is selected by querying the cursor
// position, which is a faithful proxy for "which monitor is the
// user on" given that pressing the global hotkey happens with
// the keyboard at hand, not next to the cursor.
func (p *PopupWindow) showAndPosition() {
	winWidth := float32(200)
	winHeight := float32(300)

	followMouse := p.readMouseTrackingSetting()
	var x, y int
	if followMouse {
		x, y = positionNearCursor(winWidth, winHeight)
	} else {
		x, y = positionCenterScreen(winWidth, winHeight)
	}

	p.win.Resize(fyne.NewSize(winWidth, winHeight))
	p.win.Show()
	p.win.RequestFocus()

	go func() {
		time.Sleep(50 * time.Millisecond)
		moveWindow(x, y)
	}()
}
