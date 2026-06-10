// Package tray manages the system tray icon and menu for Eruditto.
//
// The tray is the application's permanent visible presence on the desktop.
// When the user is not actively using the popup, the tray icon is how they
// know Eruditto is running and how they access its functions.
//
// Architecture:
//
//	main.go calls tray.New(...) then go tray.Run() in a goroutine.
//	systray.Run() blocks until systray.Quit() is called.
//	All inter-component communication is via callbacks injected at
//	construction — the tray package imports nothing from ui, clipboard,
//	or hotkeys, keeping the dependency graph clean.
//
// Icon strategy (Phase 7):
//
//	We generate a minimal placeholder icon at init time using the
//	standard library image/png package. This means:
//	  - No external files needed to compile
//	  - No fyne bundle step needed
//	  - Always produces a valid PNG
//	Replace generateIcon() with go:embed before release.
package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"sync/atomic"

	"fyne.io/systray"
)

// Callbacks holds the functions the tray calls in response to user actions.
// All callbacks must be non-nil. All are called from the systray goroutine.
type Callbacks struct {
	// OnShowPopup is called when "Open History" is clicked or the
	// tray icon is left-clicked.
	OnShowPopup func()

	// OnOpenSettings is called when "Settings" is clicked.
	OnOpenSettings func()

	// OnQuit is called when "Quit" is clicked. The callback must
	// stop all goroutines and call systray.Quit() to exit the loop.
	OnQuit func()
}

// Tray manages the system tray icon, tooltip, and menu.
type Tray struct {
	callbacks Callbacks
	log       *slog.Logger
	version   string
	clipCount atomic.Int64
}

// New creates a Tray ready to be started with Run().
//
//   - callbacks: user action handlers — all fields must be non-nil
//   - version:   build version string e.g. "v1.0.0" or "dev"
//   - log:       structured logger
func New(callbacks Callbacks, version string, log *slog.Logger) *Tray {
	if callbacks.OnShowPopup == nil {
		panic("tray: Callbacks.OnShowPopup must not be nil")
	}
	if callbacks.OnOpenSettings == nil {
		panic("tray: Callbacks.OnOpenSettings must not be nil")
	}
	if callbacks.OnQuit == nil {
		panic("tray: Callbacks.OnQuit must not be nil")
	}
	return &Tray{
		callbacks: callbacks,
		log:       log,
		version:   version,
	}
}

// Run starts the systray event loop. Blocks until systray.Quit() is called.
// Must be called in a dedicated goroutine:
//
//	go t.Run()
func (t *Tray) Run() {
	systray.Run(t.onReady, t.onExit)
}

// UpdateClipCount updates the tray tooltip to show the current clip count.
// Safe to call from any goroutine.
//
//	"Eruditto — 1,247 clips stored"
func (t *Tray) UpdateClipCount(count int64) {
	t.clipCount.Store(count)
	systray.SetTooltip(t.buildTooltip(count))
}

// ─────────────────────────────────────────────────────────────────────────────
// systray lifecycle
// ─────────────────────────────────────────────────────────────────────────────

// onReady is called by systray once the tray icon is initialised.
// Sets icon, tooltip, builds menu, starts event listener.
func (t *Tray) onReady() {
	t.log.Info("system tray ready", "version", t.version)

	// Icon — generated placeholder; replace with real PNG before release.
	systray.SetIcon(generateIcon())
	systray.SetTitle("Eruditto")
	systray.SetTooltip("Eruditto — clipboard manager")

	// ── Menu layout ───────────────────────────────────────────────────
	// Primary action first, then utilities, then destructive last.
	mHistory := systray.AddMenuItem("Open History", "Show clipboard history (Ctrl+Shift+V)")
	systray.AddSeparator()
	mSettings := systray.AddMenuItem("Settings", "Configure Eruditto")
	mAbout := systray.AddMenuItem(
		fmt.Sprintf("About Eruditto %s", t.version),
		"Version information",
	)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Eruditto", "Stop Eruditto and remove from tray")

	// About is informational only — shows the version string, no action.
	mAbout.Disable()

	go t.handleEvents(mHistory, mSettings, mQuit)
}

// handleEvents is the menu event loop. Runs until Quit is selected.
func (t *Tray) handleEvents(
	mHistory *systray.MenuItem,
	mSettings *systray.MenuItem,
	mQuit *systray.MenuItem,
) {
	for {
		select {
		case <-mHistory.ClickedCh:
			t.log.Debug("tray: open history")
			t.callbacks.OnShowPopup()

		case <-mSettings.ClickedCh:
			t.log.Debug("tray: open settings")
			t.callbacks.OnOpenSettings()

		case <-mQuit.ClickedCh:
			t.log.Info("tray: quit requested")
			// OnQuit must call systray.Quit() to unblock Run().
			t.callbacks.OnQuit()
			return
		}
	}
}

// onExit is called by systray after the event loop finishes.
func (t *Tray) onExit() {
	t.log.Info("system tray exited cleanly")
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildTooltip returns the tooltip string for a given clip count.
func (t *Tray) buildTooltip(count int64) string {
	switch count {
	case 0:
		return "Eruditto — no clips stored"
	case 1:
		return "Eruditto — 1 clip stored"
	default:
		return fmt.Sprintf("Eruditto — %s clips stored", formatCount(count))
	}
}

// formatCount formats an integer with thousands separators.
//
//	999     → "999"
//	1000    → "1,000"
//	50000   → "50,000"
func formatCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	result := make([]byte, 0, len(s)+len(s)/3)
	offset := len(s) % 3
	if offset == 0 {
		offset = 3
	}

	result = append(result, s[:offset]...)
	for i := offset; i < len(s); i += 3 {
		result = append(result, ',')
		result = append(result, s[i:i+3]...)
	}

	return string(result)
}

// generateIcon creates a minimal 32×32 PNG icon as a byte slice.
//
// The icon is a solid cornflower blue (#6495ED) square — a clearly
// visible placeholder that works in both dark and light system themes.
//
// Why generate at runtime instead of embedding a file?
//   - No external assets needed to compile or run
//   - No fyne bundle or go:embed setup required in Phase 7
//   - Always produces a valid, decodable PNG
//
// Replace this function with go:embed before v1 release:
//
//	//go:embed ../../assets/icons/tray.png
//	var iconBytes []byte
func generateIcon() []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Cornflower blue — visible on both dark and light system trays.
	fill := color.RGBA{R: 100, G: 149, B: 237, A: 255}
	// Darker border for definition against light backgrounds.
	border := color.RGBA{R: 70, G: 110, B: 200, A: 255}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if x == 0 || x == size-1 || y == 0 || y == size-1 {
				img.Set(x, y, border)
			} else {
				img.Set(x, y, fill)
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		// This cannot fail for a valid RGBA image — panic is correct here.
		panic(fmt.Sprintf("tray: failed to generate icon: %v", err))
	}
	return buf.Bytes()
}
