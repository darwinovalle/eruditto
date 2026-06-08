# Eruditto

A free, lightweight clipboard manager for Linux inspired by Ditto Clipboard Manager.

> **Status:** Active development — v1 in progress.

## What it does

- Monitors your clipboard continuously in the background
- Stores clipboard history (text and images) locally
- Opens a fast search popup with `Ctrl+Shift+V`
- Never sends your data anywhere — fully offline, local only

## Requirements

### To run
- Linux with X11 (Wayland support via XWayland)
- OpenGL capable GPU (virtually all modern hardware)
- `libgl1`, `libx11-6`, `libxi6`, `libxrandr2`, `libxinerama1`, `libxcursor1`

### To build from source
- Go 1.22 or newer
- GCC (for Fyne's CGO dependencies)
- X11 development headers: `sudo apt install xorg-dev libgl1-mesa-dev`

## Install

### AppImage (recommended — no install needed)
```bash
chmod +x Eruditto-x86_64.AppImage
./Eruditto-x86_64.AppImage
