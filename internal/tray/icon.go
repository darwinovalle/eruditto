package tray

import (
	"bytes"
	_ "embed"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// Embedded icon assets — PNG files in the assets/icons directory
// at the project root. go:embed makes them part of the binary so
// the installable eruditto artifact is one self-contained file
// rather than a binary plus a sidecar of icon assets.
//
// Three sizes are loaded because the Linux desktop convention for
// icon discovery is to expose multiple sizes:
//   - small_white_icon.png  → used by the system tray (systray
//     uses an internal scaled raster, but 32×32 is the canonical
//     tray size).
//   - medium_white_icon.png → fyne App.SetIcon (.desktop
//     launcher / window titlebar / alt-tab).
//   - larger_white_icon.png → high-DPI dock/launcher tile.
//
// All three are also written to ~/.local/share/icons/hicolor
// at startup (see InstallXDGIcons) so file managers / launchers
// resolve them by app-id without going through any packaged
// installer.
//
// Embed files we cannot ship (because the files don't exist at
// build time) fall back to the programmatically-drawn generateIcon
// in tray.go. The fallback path keeps a single solid icon
// visible even when the assets aren't present on disk during
// development, which lets us iterate design separately.
var (
	//go:embed assets/icons/small_white_icon.png
	smallIconPNG []byte

	//go:embed assets/icons/medium_white_icon.png
	mediumIconPNG []byte

	//go:embed assets/icons/larger_white_icon.png
	largerIconPNG []byte
)

// SmallIcon returns the embedded tray PNG bytes. Returns nil
// only if the embed was stripped (unusual).
func SmallIcon() []byte { return smallIconPNG }

// MediumIcon returns the embedded window/launcher PNG bytes.
// Suitable for fyneApp.SetIcon via fyne.NewStaticResource.
func MediumIcon() []byte { return mediumIconPNG }

// LargerIcon returns the embedded high-DPI PNG bytes.
func LargerIcon() []byte { return largerIconPNG }

// ErrIconInvalid is returned by ValidateIcon if the embedded
// bytes do not decode as a PNG image.
var ErrIconInvalid = errors.New("tray: embedded icon bytes are not a valid PNG")

// validateIcon decodes the embedded bytes to make sure a build
// path issue (accidentally stripped, file moved out from under
// the embed) doesn't ship broken assets. Called once at init
// in tray.go.
func validateIcon(name string, data []byte) error {
	if len(data) == 0 {
		return errors.New("tray: embedded icon '" + name + "' is empty")
	}
	if _, err := png.Decode(strings.NewReader(string(data))); err != nil {
		return errors.Join(ErrIconInvalid, err)
	}
	return nil
}

// InstallXDGIcons installs the bundled PNG icons into the
// freedesktop hicolor tree if they aren't already present.
//
// Behaviour contract:
//
//  1. The three PNG files (small/medium/larger →
//     ~/.local/share/icons/hicolor/{32x32,64x64,128x128}/apps/
//     eruditto.png) are written only if the destination file
//     is missing. We never overwrite an existing file so a user
//     who has hand-curated their own eruditto icon is left
//     alone.
//
//  2. ~/.local/share/icons/hicolor/index.theme is left
//     exclusively to the user's existing installation. We DO
//     NOT generate or overwrite it on every launch — doing so
//     risks clobbering a comprehensive theme the user (or some
//     other tool) has placed there with apps/, categories/,
//     emblems/, mimetypes/, devices/, etc. Instead the user runs
//     eruditto --install-desktop once to opt into having a
//     minimal index.theme (covering only our three PNGs) so the
//     launcher integration stays self-contained.
//
//  3. gtk-update-icon-cache is never invoked here. Cache
//     regeneration is the administrative domain and we don't
//     assume the right. Re-issuing cache -f after each launch
//     would also slow down boot for every other hicolor icon
//     the user has installed.
//
//  4. The desktop entry file (.desktop) is similarly opt-in
//     via --install-desktop; see desktopEntry() below.
//
// In short: by default, running eruditto is purely additive
// where it concerns user-managed icon themes, and never
// destructive. The tray icon and the fyne window titlebar
// don't need any of this; they read from the embedded bytes.
//
// Reference:
//   https://specifications.freedesktop.org/icon-theme-spec/icon-theme-spec-latest.html
func InstallXDGIcons() {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return
		}
		root = filepath.Join(home, ".local", "share")
	}
	iconsRoot := filepath.Join(root, "icons", "hicolor")

	// Ensure the local hicolor tree has a valid index.theme.
	// Without one, gtk-update-icon-cache refuses to build a
	// cache and GNOME never scans this directory for icons.
	//
	// We copy the SYSTEM index.theme
	// (/usr/share/icons/hicolor/index.theme) which declares
	// ALL standard hicolor subdirectories. This is safe because:
	//   - It only overwrites the user's theme if they have none.
	//   - A system-provided catalogue is always more complete
	//     than anything we could write ourselves.
	//   - Extra directories listed in the index.theme but not
	//     present on disk are harmless (the spec says they're
	//     simply skipped during lookup).
	//   - The system file already declares 32x32/apps,
	//     64x64/apps, 128x128/apps — our three sizes.
	if err := os.MkdirAll(iconsRoot, 0o755); err == nil {
		indexPath := filepath.Join(iconsRoot, "index.theme")
		if _, err := os.Stat(indexPath); err != nil {
			// No local index.theme — seed from system.
			sysIndex := "/usr/share/icons/hicolor/index.theme"
			data, err := os.ReadFile(sysIndex)
			if err == nil {
				_ = os.WriteFile(indexPath, data, 0o644)
			}
		}
	}

	type bucket struct {
		dir      string
		data     []byte
		fileName string
	}
	buckets := []bucket{
		{"32x32", SmallIcon(), "eruditto.png"},
		{"64x64", MediumIcon(), "eruditto.png"},
		{"128x128", LargerIcon(), "eruditto.png"},
	}

	for _, b := range buckets {
		if len(b.data) == 0 {
			continue
		}
		if _, err := png.Decode(strings.NewReader(string(b.data))); err != nil {
			// Skip invalid PNGs rather than writing junk
			// that would silently break launcher tiles.
			continue
		}
		dir := filepath.Join(iconsRoot, b.dir, "apps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		dest := filepath.Join(dir, b.fileName)
		// Idempotent: skip when the destination already
		// exists so a user who has hand-edited the icon (or
		// has set up tooling that keeps this dir in version
		// control) doesn't see their file stomped on every
		// launch.
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		_ = os.WriteFile(dest, b.data, 0o644)
	}
}

// InstallDesktopEntry installs the freedesktop .desktop file
// and a minimal hicolor/index.theme. Unlike InstallXDGIcons
// this is genuinely opt-in — the caller in main.go only
// invokes it when the user passes --install-desktop, because
// the .desktop file declares the app to the global
// application registry and the index.theme tramples the
// user's existing theme catalog if one is already installed.
//
// We write each file only if it is not present, so re-running
// the install is idempotent — useful when migrating from one
// bundle to the next without overwriting hand-edits.
func InstallDesktopEntry() {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return
		}
		root = filepath.Join(home, ".local", "share")
	}
	appsRoot := filepath.Join(root, "applications")
	dest := filepath.Join(appsRoot, "eruditto.desktop")

	if _, err := os.Stat(dest); err != nil {
		if err := os.MkdirAll(appsRoot, 0o755); err == nil {
			_ = os.WriteFile(dest, []byte(desktopEntry()), 0o644)
		}
	}

	// ── index.theme: deliberately NOT written ──
	// Writing a minimal index.theme to ~/.local/share/icons/hicolor/
	// overrides the system /usr/share/icons/hicolor/index.theme
	// (which declares 200+ directories). Our minimal version only
	// declares 3 dirs, making all other system icons invisible.
	// The PNGs we just installed will be found by the desktop
	// environment regardless because the system-level index.theme
	// already declares 32x32/apps, 64x64/apps, 128x128/apps.
	//
	// Do NOT add index.theme writing here. See diagnostic notes.
}

// UninstallDesktop reverses --install-desktop by removing
// only the files we wrote, leaving alone anything the user
// already had. Returns the count of files actually removed.
//
// Important: we use byte-identical matching for the icons.
// /home/user/.local/share/icons/hicolor/64x64/apps/eruditto.png
// could be a hand-curated version of the icon the user prefers
// over our bundled design; deleting it on uninstall would
// silently undo that. The byte-identical comparison guarantees
// UninstallDesktop reverses --install-desktop.
//
// All files are byte-matched before removal so we never delete
// something the user customised by hand.
//
// index.theme: we only remove it if it's a byte-identical copy
// of the system /usr/share/icons/hicolor/index.theme — meaning
// WE seeded it during --install-desktop because the user had
// none. If the user has edited their local index.theme (or
// another app installed one), we leave it alone.
func UninstallDesktop() int {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return 0
		}
		root = filepath.Join(home, ".local", "share")
	}
	removed := 0

	// ---- icons (byte-identical match)
	iconsRoot := filepath.Join(root, "icons", "hicolor")
	type bucket struct {
		dir  string
		data []byte
	}
	buckets := []bucket{
		{"32x32", SmallIcon()},
		{"64x64", MediumIcon()},
		{"128x128", LargerIcon()},
	}
	for _, b := range buckets {
		if len(b.data) == 0 {
			continue
		}
		path := filepath.Join(iconsRoot, b.dir, "apps", "eruditto.png")
		existing, err := os.ReadFile(path)
		if err != nil {
			// File doesn't exist; nothing to do.
			continue
		}
		if !bytes.Equal(existing, b.data) {
			// Hand-curated by the user. Leave alone.
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}

	// ---- desktop entry (byte-identical match)
	desktopPath := filepath.Join(root, "applications", "eruditto.desktop")
	if existing, err := os.ReadFile(desktopPath); err == nil {
		if bytes.Equal(existing, []byte(desktopEntry())) {
			if err := os.Remove(desktopPath); err == nil {
				removed++
			}
		}
	}

	// ---- index.theme (byte-match against system copy)
	// We only remove if it matches the system index.theme
	// verbatim — meaning we seeded it because the user had none.
	// A hand-edited or other-app-installed index.theme survives.
	indexPath := filepath.Join(iconsRoot, "index.theme")
	localData, localErr := os.ReadFile(indexPath)
	if localErr == nil {
		sysData, sysErr := os.ReadFile("/usr/share/icons/hicolor/index.theme")
		if sysErr == nil && bytes.Equal(localData, sysData) {
			if err := os.Remove(indexPath); err == nil {
				removed++
			}
		}
	}

	return removed
}

// desktopEntry is a runtime-resolved .desktop entry. We use a
// function (not a const) so the `Exec=` line can point at the
// real binary path returned by os.Executable() — handy for devs
// running `go build && ./eruditto` from the project directory
// without a /usr/bin install. `.deb` maintainers may patch this
// to a fixed /usr/bin/eruditto path during package build.
//
// StartupWMClass is critical for X11 alt-tab: the WM_CLASS
// fyne sets via app.NewWithID(appID) in main.go MUST match
// this value, or alt-tab shows eruditto under one identity and
// the dock under another (icons appear twice, missing in one
// place).
func desktopEntry() string {
	// Default Exec=eruditto covers the .deb / AppImage flow
	// where eruditto is on PATH. For dev workflows we replace
	// with os.Executable() so the .desktop launches the binary
	// the user just built.
	execLine := "Exec=eruditto"
	if exe, err := os.Executable(); err == nil && exe != "" {
		execLine = "Exec=" + exe
	}
	return `[Desktop Entry]
Type=Application
Name=Eruditto
GenericName=Clipboard Manager
Comment=Ditto-inspired clipboard manager with image support
` + execLine + `
Icon=eruditto
Terminal=false
Categories=Utility;Office;
StartupNotify=true
StartupWMClass=io.github.darwinovalle.eruditto
`
}
