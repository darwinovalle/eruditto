//go:build !linux
package ui

// DetectPasteShortcut returns the default "ctrl+v" on non-Linux
// platforms. The isImage hint is ignored — on Linux the value
// only changes for image clips into terminals; on other platforms
// we don't have terminal-vs-GUI detection, so we always emit
// ctrl+v which is the universally-accepted shortcut.
//
// The Linux-only autopaste_linux.go file provides the real
// implementation that distinguishes terminals and browser apps
// from regular GUI apps.
func DetectPasteShortcut(isImage bool) string {
	return "ctrl+v"
}
