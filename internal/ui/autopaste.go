package ui

import (
	"fmt"
	"os/exec"
)

func AutoPaste() error {
	cmd := exec.Command(
		"xdotool",
		"key",
		"--clearmodifiers",
		"ctrl+v",
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	return nil
}
