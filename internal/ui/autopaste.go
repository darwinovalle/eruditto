package ui

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// AutoPaste sends the paste shortcut to the target window.
//
// On terminals (bash, vim, etc.) the correct shortcut is
// Ctrl+Shift+V rather than Ctrl+V because Ctrl+V is captured by
// the TTY. On web-based apps (Excalidraw, Figma, draw.io) the
// choice depends on what's on the clipboard:
//
//   - For TEXT clips, both Ctrl+V and Ctrl+Shift+V work in
//     Excalidraw (the shift modifier toggles "paste as plain
//     text", harmless for text content).
//
//   - For IMAGE clips, Ctrl+V is the right choice in Excalidraw.
//     Ctrl+Shift+V in Excalidraw when the clipboard holds an
//     image causes Excalidraw to paste as plain text, silently
//     DROPPING the image — the user sees nothing happen.
//
// AutoPaste dispatches to DetectPasteShortcut which selects the
// correct shortcut based on the focus target and the clip type.
//
// targetWindowID, when non-empty, is the X11 window ID the
// shortcut is sent to. Passing it explicitly bypasses the window
// manager's input-focus tracking. This is essential for terminal
// apps (where Ctrl+V is captured by the TTY and Ctrl+Shift+V is
// the only reliable way to paste).
//
// If targetWindowID is empty, xdotool sends the key to whatever
// window currently has focus.
func AutoPaste(targetWindowID string, isImage bool) error {
	shortcut := DetectPasteShortcut(isImage)
	slog.Debug("AutoPaste: selected shortcut",
		"shortcut", shortcut,
		"target_window_id", targetWindowID,
		"is_image", isImage,
	)

	args := []string{"key"}
	// We intentionally do NOT honour --window here. AutoPaste is
	// called after ActivateWindow has already moved X11 input
	// focus to the desired window — exposing that target via
	// --window to xdotool routes events to the window object but,
	// on gnome-terminal and several other terminals, the
	// subordinate input widget (vte, gtk-launch-pane, etc.) is
	// the actual key listener. Synthesising at the parent window
	// therefore loses the focus-driven propagation; the inner
	// widget never receives the paste.
	//
	// Sending without --window routes through X11's normal focus
	// graph, which already points at the subordinate widget after
	// ActivateWindow --sync + 50ms sleep.
	//
	// We intentionally do NOT pass --clearmodifiers either. See
	// the comment further down for the rationale.
	args = append(args, shortcut)

	cmd := exec.Command("xdotool", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("autopaste: %w (shortcut=%s, target=%q, is_image=%t)",
			err, shortcut, redactWindowID(targetWindowID), isImage)
	}
	return nil
}

// shouldAutoPasteForWindow returns false if the target window is a
// web browser that runs web apps like Excalidraw or Figma. For
// those windows, we deliberately skip the synthetic Ctrl+V keypress
// because it does not reach the web app's paste listener (X11
// synthetic events do not propagate through Chromium's internal
// focus model to the page's document.activeElement). The image is
// already on the clipboard via the xclip daemon, so the user can
// press Ctrl+V manually in the browser.
//
// windowName is the X11 window's WM_NAME (from
// `xdotool getwindowname`). We match on substrings because exact
// matches are fragile across browsers and versions.
//
// Pass empty string to skip the check (returns true).
func shouldAutoPasteForWindow(windowName string) bool {
	if windowName == "" {
		return true
	}
	lower := strings.ToLower(windowName)
	// Browsers and Electron-based apps. We are intentionally
	// broad: any window whose title looks like a browser gets the
	// safe path (clipboard + manual paste).
	browserNames := []string{
		"chrome", "chromium", "firefox", "brave", "edge",
		"vivaldi", "epiphany", "electron",
	}
	for _, b := range browserNames {
		if strings.Contains(lower, b) {
			return false
		}
	}
	return true
}

// windowNameForID returns the WM_NAME of the given X11 window ID,
// or empty string on failure. Used to decide whether to skip
// auto-paste for browsers.
func windowNameForID(windowID string) string {
	if windowID == "" {
		return ""
	}
	cmd := exec.Command("xdotool", "getwindowname", windowID)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// redactWindowID returns a short prefix of the window ID for
// logging, or "<empty>" if blank. Window IDs are not sensitive
// but a full ID is noisy in logs.
func redactWindowID(id string) string {
	if id == "" {
		return "<empty>"
	}
	if len(id) <= 6 {
		return id
	}
	return id[:6] + "…"
}
