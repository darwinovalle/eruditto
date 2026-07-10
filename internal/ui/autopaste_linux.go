//go:build linux

package ui

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

// shiftVPasteProcesses is the set of process names whose windows
// want Ctrl+Shift+V as the paste shortcut rather than the default
// Ctrl+V.
//
// Two categories of apps end up here:
//
//  1. Terminal emulators. TUI apps running inside them (bash, vim,
//     nvim, etc.) bind Ctrl+Shift+V as the literal-paste shortcut
//     because Ctrl+V is interpreted by the terminal itself as
//     "verbatim insert" / scrollback navigation. We walk up the
//     process tree from the focused window's PID because the
//     focused PID is usually the child shell, not the terminal.
//
//  2. Web-based and Electron-based apps that override the browser
//     default Ctrl+V to do something else (e.g., open a paste
//     dialog or open a "paste as plain text" confirmation) and
//     use Ctrl+Shift+V for the actual image paste. Excalidraw
//     (running in a browser), Figma, draw.io, Canva, and similar
//     apps behave this way. For these we match the focused PID's
//     own name because the renderer process IS the browser binary.
//
// Adding a process name here is the canonical way to extend
// auto-paste to a new application. Keep entries lowercase.
var shiftVPasteProcesses = map[string]struct{}{
	// Terminal emulators (and shell text editors reachable through them).
	"gnome-terminal":        {},
	"gnome-terminal-server": {},
	"kitty":                 {},
	"alacritty":             {},
	"wezterm":               {},
	"konsole":               {},
	"xterm":                 {},
	"terminator":            {},
	"tilix":                 {},
	"xfce4-terminal":        {},
	"lxterminal":            {},
	"mate-terminal":         {},
	"foot":                  {},
	"st":                    {},
	"tmux":                  {},
	"screen":                {},
	"bash":                  {},
	"zsh":                   {},
	"fish":                  {},
	"ssh":                   {},
	"vim":                   {},
	"nvim":                  {},
	"nano":                  {},

	// Browsers / web apps that use Ctrl+Shift+V for image paste.
	// The focused window's PID is usually the browser renderer,
	// which has the same binary name as the main browser process.
	"chrome":      {},
	"chromium":    {},
	"chrome-bin":  {},
	"vivaldi":     {},
	"brave":       {},
	"edge":        {},
	"firefox":     {},
	"firefox-bin": {},
	"epiphany":    {},
}

func focusedWindowPID() (int, error) {
	cmd := exec.Command(
		"xdotool",
		"getwindowfocus",
		"getwindowpid",
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return 0, err
	}

	return strconv.Atoi(strings.TrimSpace(out.String()))
}

func processName(pid int) (string, error) {
	cmd := exec.Command(
		"ps",
		"-p",
		strconv.Itoa(pid),
		"-o",
		"comm=",
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

func parentPID(pid int) (int, error) {
	cmd := exec.Command(
		"ps",
		"-p",
		strconv.Itoa(pid),
		"-o",
		"ppid=",
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return 0, err
	}

	return strconv.Atoi(strings.TrimSpace(out.String()))
}

// processInShiftVSet reports whether the given process name (case-
// insensitive substring match) is in shiftVPasteProcesses.
func processInShiftVSet(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for known := range shiftVPasteProcesses {
		if strings.Contains(name, known) {
			return true
		}
	}
	return false
}

// wantsShiftVPaste reports whether the focused window wants
// Ctrl+Shift+V instead of Ctrl+V as the paste shortcut.
//
// Decision logic:
//
//  1. If the focused window's own process is a browser / Electron
//     binary, the window is a web app that probably uses
//     Ctrl+Shift+V for image paste. Return true immediately — do
//     not walk up the parent tree, because the focused process IS
//     the relevant one.
//
//  2. Otherwise, walk up the parent chain looking for a terminal
//     emulator. If any ancestor is a terminal, return true.
//
//  3. Otherwise, return false (default Ctrl+V).
func wantsShiftVPaste(focusedPID int) bool {
	// Step 1: check the focused PID itself. Browsers and Electron
	// apps run renderer processes with names matching their binary
	// (chrome, vivaldi, firefox, etc.), so the focused PID's name
	// is the most direct signal.
	if name, err := processName(focusedPID); err == nil {
		lower := strings.ToLower(name)
		// If the focused process is a known browser, it's almost
		// certainly a web-app-using-Ctrl-Shift-V case.
		browserNames := []string{
			"chrome", "chromium", "vivaldi", "brave", "edge",
			"firefox", "epiphany",
		}
		for _, b := range browserNames {
			if strings.Contains(lower, b) {
				return true
			}
		}
		// Non-browser, non-terminal focused process: still might
		// be a match (e.g., a standalone Electron app like
		// excalidraw-desktop). Use the full set as a fallback.
		if processInShiftVSet(name) {
			return true
		}
	}

	// Step 2: walk up the parent chain for terminals.
	pid := focusedPID
	for i := 0; i < 10 && pid > 1; i++ {
		parent, err := parentPID(pid)
		if err != nil || parent == pid {
			break
		}
		name, err := processName(parent)
		if err != nil {
			break
		}
		if processInShiftVSet(name) {
			return true
		}
		pid = parent
	}

	return false
}

// DetectPasteShortcut decides which paste shortcut should be sent
// to the focused application. isImage reports whether the clip on
// the clipboard is an image (true) or text (false).
//
// Returns "ctrl+shift+v" whenever the focused process matches the
// terminal/shift-requires-paste set AND that process is not a known
// browser binary.
//
// In all other cases returns "ctrl+v":
//
//   - Text clips into Electron / browser apps (Excalidraw, Figma,
//     draw.io): ctrl+v. Plain Ctrl+V reaches the paste handler in
//     web apps; SHIFT in Excalidraw toggles "paste as plain text",
//     which is harmless for text but does drop images, and is
//     unnecessary when the clipboard holds text.
//
//   - Text clips into terminals (vim/bash): "ctrl+shift+v". The
//     TTY captures plain Ctrl+V (e.g., to enter visual-block
//     mode in nvim), so SHIFT is required to reach the terminal's
//     paste handler.
//
//   - Image clips into Electron / browser apps: ctrl+v. Critical
//     — in Excalidraw the SHIFT modifier toggles "paste as plain
//     text"; when an image is on the clipboard and Shift is held,
//     Excalidraw silently drops the image. Sending Ctrl+V avoids
//     the trap. (Excalidraw binds both shortcuts to the same
//     paste handler — Shift only acts as the plain-text toggle —
//     so we don't lose functionality by avoiding SHIFT.)
//
//   - Image clips into terminals (rare — pasting PNGs into
//     vim/bash): ctrl+shift+v because the TTY captures plain
//     Ctrl+V.
//
//   - Image clips into non-terminal GUI apps: ctrl+v.
//
// Note: for browser-based apps, the keyboard shortcut is sent
// regardless of whether the target web app will actually accept
// it. If the user has manually clicked into Excalidraw's canvas,
// the keypress will reach Excalidraw and the paste will work;
// otherwise the user pastes manually with Ctrl+V in the browser.
func DetectPasteShortcut(isImage bool) string {
	pid, err := focusedWindowPID()
	if err != nil {
		// No focused window — fall back to ctrl+v, the
		// universally-safe shortcut.
		return "ctrl+v"
	}

	// For text and image alike: if the focused process does not
	// require SHIFT for pasting (regular GUI apps), use plain
	// ctrl+v. The isImage distinction matters only at the very
	// end, because browser apps can match wantsShiftVPaste but
	// still need ctrl+v (image case).
	if !wantsShiftVPaste(pid) {
		return "ctrl+v"
	}

	// wantsShiftVPaste matched. Verify it's not actually a
	// browser/electron process before sending SHIFT — both
	// terminals and electron-based apps can populate
	// shiftVPasteProcesses via process-tree ancestors, but only
	// terminals want SHIFT.
	name, err := processName(pid)
	if err == nil && isBrowserProcessName(name) {
		// Browser: don't send SHIFT (would drop images and is
		// pointless for text in Excalidraw).
		return "ctrl+v"
	}

	// Treating as a terminal. SHIFT is required to bypass the
	// TTY's handling of Ctrl+V. Identical behavior for text and
	// image clips — terminals don't differentiate.
	_ = isImage // isImage does not change the terminal shortcut.
	return "ctrl+shift+v"
}

// isBrowserProcessName reports whether the given process name
// (from `ps -o comm=`) is one of the known Electron / browser
// binaries. Used to disambiguate terminal-vs-browser selection
// in DetectPasteShortcut.
func isBrowserProcessName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	browserNames := []string{
		"chrome", "chromium", "vivaldi", "brave", "edge",
		"firefox", "epiphany",
	}
	for _, b := range browserNames {
		if strings.Contains(lower, b) {
			return true
		}
	}
	return false
}
