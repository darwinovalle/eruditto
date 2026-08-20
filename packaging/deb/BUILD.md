# Building the .deb Package

## Prerequisites

- Go 1.25+
- `dpkg-deb` (installed by default on Ubuntu/Debian)
- All runtime dependencies (xclip, xdotool, etc.) for testing

## Quick Build

```bash
make deb
```

Produces: `build/eruditto_0.1.0_amd64.deb`

## Custom Version

```bash
make deb VERSION=0.2.0
```

Produces: `build/eruditto_0.2.0_amd64.deb`

The version is stamped in two places automatically:
- The binary (`main.version` via `-ldflags -X`)
- The `DEBIAN/control` file (`Version:` field)

## What make deb Does

1. Runs `go vet ./...` and `go test ./...` — aborts if either fails
2. Builds a stripped binary: `go build -ldflags "-s -w -X main.version=X.Y.Z"`
3. Copies the binary into `packaging/deb/eruditto/usr/bin/eruditto`
4. Stamps the version into `DEBIAN/control`
5. Runs `dpkg-deb --build` to produce the .deb

## Package Structure

```
packaging/deb/eruditto/
├── DEBIAN/
│   ├── control      ← package metadata (name, version, depends)
│   ├── postinst      ← runs after install: icon cache + desktop database updates
│   └── postrm        ← runs after remove: cleans user-level files + cache updates
└── usr/
    ├── bin/
    │   └── eruditto                         ← the binary
    └── share/
        ├── applications/
        │   └── eruditto.desktop             ← Exec=/usr/bin/eruditto
        └── icons/hicolor/
            ├── 32x32/apps/eruditto.png
            ├── 64x64/apps/eruditto.png
            └── 128x128/apps/eruditto.png
```

Source files live in `packaging/deb/` (control, postinst, postrm).
The `eruditto/` install root is the tree that `dpkg-deb` packages.

## Dependencies

The .deb declares these runtime dependencies:

| Package | Why |
|---|---|
| libx11-6 | X11 client library (Fyne/GL) |
| libgl1 | OpenGL (Fyne rendering) |
| libxcb1 | X C Binding |
| libxau6 | X Auth |
| libxdmcp6 | X Display Manager Control Protocol |
| libbsd0 | BSD utility functions |
| xclip | Clipboard image read/write |
| xdotool | Auto-paste keystroke synthesis |
| xdg-utils | xdg-desktop-menu for .desktop registration |
| desktop-file-utils | update-desktop-database |
| libnotify-bin | notify-send (Wayland + error notifications) |

All are available on Ubuntu 22.04+ and 24.04+.
`dpkg -i` will warn if any are missing; `apt install -f` resolves them.

## Install / Uninstall / Reinstall

```bash
# Install
sudo dpkg -i build/eruditto_0.1.0_amd64.deb

# If dpkg reports missing deps, fix with:
sudo apt install -f

# Uninstall
sudo dpkg -r eruditto

# Reinstall (upgrade)
sudo dpkg -i build/eruditto_0.2.0_amd64.deb
```

### What postinst does

After install, the postinst script runs:
1. `gtk-update-icon-cache -f /usr/share/icons/hicolor/` — makes icons visible in GNOME
2. `update-desktop-database /usr/share/applications/` — registers the .desktop file
3. `xdg-desktop-menu forceupdate` — triggers DE to rescan

No manual steps needed. The app appears in the app launcher immediately.

### What postrm does

After remove, the postrm script:
1. Deletes user-level files that `--install-desktop` may have written:
   - `~/.local/share/icons/hicolor/*/apps/eruditto.png`
   - `~/.local/share/applications/eruditto.desktop`
   - `~/.config/autostart/eruditto.desktop`
2. Re-runs icon cache and desktop database updates

User data (clipboard history DB, images, settings) in `~/.local/share/eruditto/`
is intentionally NOT deleted on uninstall — the user may want to keep it.

## Verify Before Release

Always run these before shipping a .deb:

```bash
# Check metadata (version, depends, description)
dpkg --info build/eruditto_0.1.0_amd64.deb

# Check file listing (paths, permissions)
dpkg -c build/eruditto_0.1.0_amd64.deb
```

Expected output:
- Version matches your intent
- Architecture is amd64
- All Depends listed
- Files at /usr/bin/eruditto, /usr/share/applications/, /usr/share/icons/hicolor/

## Manual Build (without Make)

If you need to build step by step:

```bash
# 1. Build the binary
go build -ldflags "-s -w -X main.version=0.1.0" \
  -o packaging/deb/eruditto/usr/bin/eruditto ./cmd/eruditto

# 2. Stage the desktop entry (registers the app in the launcher and
#    gives the dock a matching icon via StartupWMClass)
mkdir -p packaging/deb/eruditto/usr/share/applications
cp packaging/deb/eruditto.desktop \
  packaging/deb/eruditto/usr/share/applications/eruditto.desktop

# 3. Sync DEBIAN/control version
sed -i 's/^Version: .*/Version: 0.1.0/' packaging/deb/eruditto/DEBIAN/control

# 4. Package
dpkg-deb --build packaging/deb/eruditto build/eruditto_0.1.0_amd64.deb
```

## Updating Icons

If you change the icons in `assets/icons/`, you must also copy the new PNGs
into the .deb install root:

```bash
cp assets/icons/small_white_icon.png  packaging/deb/eruditto/usr/share/icons/hicolor/32x32/apps/eruditto.png
cp assets/icons/medium_white_icon.png packaging/deb/eruditto/usr/share/icons/hicolor/64x64/apps/eruditto.png
cp assets/icons/larger_white_icon.png packaging/deb/eruditto/usr/share/icons/hicolor/128x128/apps/eruditto.png
```

Then `make deb` to rebuild the package with the fresh icons.

Don't forget to also update the embedded icons used by the running app:
```bash
cp assets/icons/small_white_icon.png  internal/tray/assets/icons/
cp assets/icons/medium_white_icon.png internal/tray/assets/icons/
cp assets/icons/larger_white_icon.png internal/tray/assets/icons/
```
