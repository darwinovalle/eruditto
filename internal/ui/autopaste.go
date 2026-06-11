package ui

import (
	"fmt"
	"os/exec"
)

func AutoPaste() error {
	shortcut := DetectPasteShortcut()
	cmd := exec.Command(
		"xdotool",
		"key",
		"--clearmodifiers",
		shortcut,
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	return nil
}
