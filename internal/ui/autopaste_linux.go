//go:build linux

package ui

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

var terminalProcesses = map[string]struct{}{
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

func isTerminalProcessTree(pid int) bool {
	for i := 0; i < 10 && pid > 1; i++ {
		name, err := processName(pid)
		if err != nil {
			return false
		}

		name = strings.ToLower(name)

		for known := range terminalProcesses {
			if strings.Contains(name, strings.ToLower(known)) {
				return true
			}
		}

		parent, err := parentPID(pid)
		if err != nil {
			return false
		}

		if parent == pid {
			break
		}

		pid = parent
	}

	return false
}

// DetectPasteShortcut decides which paste shortcut
// should be sent to the focused application.
func DetectPasteShortcut() string {
	pid, err := focusedWindowPID()
	if err != nil {
		return "ctrl+v"
	}

	if isTerminalProcessTree(pid) {
		return "ctrl+shift+v"
	}

	return "ctrl+v"
}
