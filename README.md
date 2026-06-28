# Eruditto

A free, lightweight clipboard manager for Linux inspired by Ditto Clipboard Manager.

> **Status:** Active development — v1 in progress.

## What it does

- Monitors your clipboard continuously in the background
- Stores clipboard history (text and images) locally in SQLite
- Opens a fast search popup with `Ctrl+Shift+V`
- Slash-mode search (type `/` to filter, `Enter` to paste, `Esc` to close)
- Vim-style navigation (`j`/`k` to move, `Enter` to paste) — opt-in in settings
- Image clipboard support via xclip (paste into browsers, editors, terminals)
- System tray icon with quick access to settings and quit
- Autostart at login (XDG autostart + systemd user-unit fallback)
- Never sends your data anywhere — fully offline, local only

## Requirements

### To run
- Linux with X11 (Wayland support via XWayland)
- OpenGL capable GPU (virtually all modern hardware)
- `xclip` (pre-installed on Ubuntu; used for image clipboard ownership)
- `xdotool` (for auto-paste after selecting a clip)

### To build from source
- Go 1.25 or newer
- X11 development headers: `sudo apt install xorg-dev libgl1-mesa-dev`

## Build

```bash
go build -o eruditto ./cmd/eruditto
```

## Run

```bash
./eruditto
```

### Debug mode

```bash
./eruditto -debug
```

## Desktop integration

By default, running `./eruditto` does **not** touch your system. Icons and .desktop files are opt-in.

### Install (show in app launcher + autostart-ready)

```bash
./eruditto --install-desktop
update-desktop-database ~/.local/share/applications/
xdg-desktop-menu forceupdate
```

This writes:
- `~/.local/share/applications/eruditto.desktop` — app launcher entry
- `~/.local/share/icons/hicolor/{32x32,64x64,128x128}/apps/eruditto.png` — icons
- `~/.local/share/icons/hicolor/index.theme` — only if missing; seeded from the system copy at `/usr/share/icons/hicolor/index.theme` (never overwrites an existing one)

### Uninstall (remove only what we wrote)

```bash
./eruditto --uninstall-desktop
update-desktop-database ~/.local/share/applications/
xdg-desktop-menu forceupdate
```

All files are byte-matched before removal. If you hand-edited a file, it survives uninstall. The `index.theme` is only removed if it still matches the system copy byte-for-byte (meaning we seeded it).

### Changing the embedded icons

Icons are embedded at build time via `//go:embed` from:

```
internal/tray/assets/icons/
├── small_white_icon.png   (32x32)
├── medium_white_icon.png  (64x64)
└── larger_white_icon.png  (128x128)
```

After replacing these files, you **must**:
1. Delete the old PNGs from disk: `rm -f ~/.local/share/icons/hicolor/*/apps/eruditto.png`
2. Rebuild: `go build -o eruditto ./cmd/eruditto`
3. Reinstall: `./eruditto --install-desktop`

The install skips files that already exist on disk. Stale icons from a previous install won't be overwritten — they must be removed first.

## Autostart at login

Toggle "Launch Eruditto at login" in the settings UI. This immediately writes or removes:

- **Primary:** `~/.config/autostart/eruditto.desktop` (XDG autostart spec)
- **Fallback:** `~/.config/systemd/user/io.github.darwinovalle.eruditto.service` (if XDG dir is unavailable)

On every launch, Eruditto reconciles the system state with the persisted preference — so if the autostart file goes missing (e.g. after a system update), it gets recreated automatically.

Note: `os.Executable()` resolves to the real running binary path. During development (`go build`), this points to the built binary in your project directory. For a proper install, use `--install-desktop` or a system package.

## Tests

```bash
go test -count=1 -timeout 30s ./internal/...
```

## Project layout

```
cmd/eruditto/          — main entry point, flag handling, startup wiring
internal/autostart/     — XDG + systemd autostart management
internal/clipboard/     — clipboard reader (xclip-based)
internal/domain/        — settings keys, domain types
internal/hotkeys/       — global hotkey registration
internal/images/        — image detection and processing
internal/settings/      — SQLite-backed settings service
internal/tray/          — system tray, embedded icons, install/uninstall
internal/ui/            — Fyne UI: popup, settings, auto-paste, theming
pkg/xdg/                — XDG base-directory helpers
```

## License

MIT
