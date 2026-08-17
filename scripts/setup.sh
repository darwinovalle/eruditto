#!/usr/bin/env bash
#
# Eruditto development environment setup.
#
# Installs the system packages needed to build and run eruditto, verifies
# the Go toolchain, and builds the binary. Idempotent — safe to re-run.
#
# This is the one-command entry point for new contributors:
#   bash scripts/setup.sh
#
# Usage:
#   bash scripts/setup.sh            # install system deps (asks for sudo) + build
#   bash scripts/setup.sh --no-deps  # skip system package install, just build
#   bash scripts/setup.sh --no-build # install deps / verify toolchain only
#   bash scripts/setup.sh --help
#
# What gets installed (system level — Go modules are already pinned in
# go.mod/go.sum, so no venv is involved):
#   - X11 + OpenGL dev headers   (build-time, Fyne/CGO)
#   - xclip, xdotool             (run-time, clipboard + auto-paste)
#
# Exit codes:
#   0   success
#   1   missing requirement / unsupported distro / build failure
#
set -euo pipefail

readonly SCRIPT_NAME="$(basename "$0")"
readonly REQUIRED_GO_VERSION="1.25"

log()  { printf '[%s] %s\n' "$SCRIPT_NAME" "$*"; }
warn() { printf '[%s] WARNING: %s\n' "$SCRIPT_NAME" "$*" >&2; }
fail() { printf '[%s] ERROR: %s\n' "$SCRIPT_NAME" "$*" >&2; exit 1; }

check_go_version() {
    if ! command -v go >/dev/null 2>&1; then
        fail "Go not found. Install Go $REQUIRED_GO_VERSION or newer (https://go.dev/dl/), then re-run."
    fi
    local current
    current="$(go version | awk '{print $3}')"   # e.g. "go1.26.5"
    current="${current#go}"                       # strip "go" prefix -> "1.26.5"
    local current_major
    current_major="$(printf '%s' "$current" | cut -d. -f1)"
    local required_major
    required_major="$(printf '%s' "$REQUIRED_GO_VERSION" | cut -d. -f1)"
    if [ "$current_major" -lt "$required_major" ]; then
        fail "Go $REQUIRED_GO_VERSION or newer required, found go$current. Update Go (https://go.dev/dl/)."
    fi
    log "Go version OK: go$current (>= $REQUIRED_GO_VERSION)"
}

# detect_pkg_manager echoes the package manager name, or "unknown".
detect_pkg_manager() {
    if command -v apt-get >/dev/null 2>&1; then echo "apt";   return; fi
    if command -v dnf     >/dev/null 2>&1; then echo "dnf";   return; fi
    if command -v pacman  >/dev/null 2>&1; then echo "pacman"; return; fi
    if command -v zypper  >/dev/null 2>&1; then echo "zypper"; return; fi
    echo "unknown"
}

install_system_deps() {
    local pm
    pm="$(detect_pkg_manager)"

    # Package sets per distro. The build-time set mirrors the Fyne
    # prerequisites for each family; xclip/xdotool are runtime tools.
    local pkgs
    case "$pm" in
        apt)
            pkgs="xorg-dev libgl1-mesa-dev xclip xdotool"
            ;;
        dnf)
            pkgs="libX11-devel libXi-devel libXcursor-devel libXrandr-devel libXinerama-devel mesa-libGL-devel xclip xdotool"
            ;;
        pacman)
            pkgs="xorg-server-devel libx11 libxrandr libxinerama libxcursor mesa xclip xdotool"
            ;;
        zypper)
            pkgs="xorg-x11-devel libXcursor-devel libXrandr-devel libXi-devel libXinerama-devel Mesa-libGL-devel xclip xdotool"
            ;;
        *)
            fail "Unsupported package manager. Install manually: X11/OpenGL dev headers, xclip, xdotool, then re-run with --no-deps."
            ;;
    esac

    # Determine how to run privileged installs (root, sudo, or fail).
    local run
    if [ "$(id -u)" -eq 0 ]; then
        run=""
    elif command -v sudo >/dev/null 2>&1; then
        run="sudo"
    else
        fail "Not running as root and sudo is not available — install packages yourself, then re-run with --no-deps."
    fi

    log "Installing system packages via $pm (may prompt for your password):"
    log "  $pkgs"

    case "$pm" in
        apt)
            $run apt-get update
            $run apt-get install -y $pkgs
            ;;
        dnf)
            $run dnf install -y $pkgs
            ;;
        pacman)
            $run pacman -S --noconfirm $pkgs
            ;;
        zypper)
            $run zypper install -y $pkgs
            ;;
    esac
}

build() {
    log "Building binary (go build -o eruditto ./cmd/eruditto)"
    go build -o eruditto ./cmd/eruditto
    log "Build complete: ./eruditto"
    log "Run it with: ./eruditto -debug   (debug logging) or just ./eruditto"
}

usage() {
    cat <<EOF
Usage: bash $SCRIPT_NAME [options]

Sets up the eruditto development environment:
  1. Install system packages (X11/OpenGL dev headers, xclip, xdotool)
  2. Verify the Go toolchain (>= $REQUIRED_GO_VERSION)
  3. Build the eruditto binary

Options:
  --no-deps    Skip system package installation (already installed)
  --no-build   Install deps / verify toolchain only
  -h, --help   Show this help
EOF
}

main() {
    local do_deps=true
    local do_build=true

    for arg in "$@"; do
        case "$arg" in
            --no-deps)  do_deps=false ;;
            --no-build) do_build=false ;;
            -h|--help)  usage; exit 0 ;;
            *) fail "unknown option: $arg (see --help)" ;;
        esac
    done

    log "Eruditto development setup starting"

    if $do_deps; then
        install_system_deps
    else
        log "Skipping system dependency installation (--no-deps)"
    fi

    check_go_version

    if $do_build; then
        build
    fi

    log "Done."
}

main "$@"
