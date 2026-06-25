//go:build linux

package clipboard

import (
	"bytes"
	"fmt"
	"os/exec"
)

func ClipboardHasImage() bool {
	cmd := exec.Command(
		"xclip",
		"-selection",
		"clipboard",
		"-t",
		"TARGETS",
		"-o",
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return false
	}

	targets := out.String()

	return bytes.Contains([]byte(targets), []byte("image/png")) ||
		bytes.Contains([]byte(targets), []byte("image/jpeg")) ||
		bytes.Contains([]byte(targets), []byte("image/bmp")) ||
		bytes.Contains([]byte(targets), []byte("image/webp"))
}

func ReadImage() ([]byte, error) {
	cmd := exec.Command(
		"xclip",
		"-selection",
		"clipboard",
		"-t",
		"image/png",
		"-o",
	)

	var out bytes.Buffer

	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf(
			"clipboard: read image: %w",
			err,
		)
	}

	return out.Bytes(), nil
}
