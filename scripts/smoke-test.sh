#!/usr/bin/env bash
#
# Eruditto Phase 0 smoke test.
#
# Builds a tiny Fyne program in an isolated temp directory to verify that
# the host has everything needed to compile and run a Fyne application:
#   - Go toolchain installed
#   - CGO toolchain (gcc) installed
#   - X11 / OpenGL system libraries present (libGL, libX11, libXi, ...)
#
# This script does NOT touch the eruditto source tree. It is meant to be
# runnable from a clean checkout before any feature code is written, to
# catch environment issues early.
#
# Exit codes:
#   0   Fyne initialized successfully
#   non-zero   any step failed
#
set -euo pipefail

readonly SCRIPT_NAME="$(basename "$0")"
readonly REQUIRED_GO_VERSION="1.25"

log() {
    printf '[%s] %s\n' "$SCRIPT_NAME" "$*"
}

fail() {
    printf '[%s] ERROR: %s\n' "$SCRIPT_NAME" "$*" >&2
    exit 1
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found in PATH: $1"
}

check_go_version() {
    local current
    current="$(go version | awk '{print $3}')"   # e.g. "go1.25.5"
    current="${current#go}"                       # strip "go" prefix -> "1.25.5"
    local required_major
    local current_major
    required_major="$(printf '%s' "$REQUIRED_GO_VERSION" | cut -d. -f1)"
    current_major="$(printf '%s' "$current" | cut -d. -f1)"
    if [ "$current_major" -lt "$required_major" ]; then
        fail "Go $REQUIRED_GO_VERSION or newer required, found go$current"
    fi
    log "Go version OK: go$current"
}

# NOTE: WORKDIR is intentionally NOT `local`. The cleanup trap is set in
# main() but fires at script-exit time, when the function's local scope is
# already gone. If WORKDIR were local to main(), the trap's `rm -rf
# "$WORKDIR"` would either expand an empty string (rm -rf without an arg
# is a no-op) or, with `set -u`, abort with "unbound variable". Keeping
# WORKDIR at file scope makes it visible to the trap from any context.
WORKDIR=""

cleanup() {
    # `set +u` because WORKDIR may legitimately be empty if we never got
    # past the mktemp line (e.g. Go version check failed earlier).
    set +u
    if [ -n "$WORKDIR" ] && [ -d "$WORKDIR" ]; then
        rm -rf "$WORKDIR"
    fi
}

main() {
    log "Eruditto Phase 0 smoke test starting"

    require_cmd go
    require_cmd awk
    require_cmd grep
    require_cmd mktemp
    require_cmd rm

    check_go_version

    WORKDIR="$(mktemp -d -t eruditto-smoke.XXXXXX)"
    # Always clean up, even on failure or interrupt.
    trap cleanup EXIT

    log "using temp directory: $WORKDIR"

    cat > "$WORKDIR/main.go" <<'GOFILE'
package main

import (
	"fmt"

	"fyne.io/fyne/v2/app"
)

func main() {
	a := app.New()
	fmt.Println("Fyne initialized successfully")
	fmt.Println("App ID:", a.UniqueID())
	a.Quit()
}
GOFILE

    (
        cd "$WORKDIR"

        log "go mod init fyne_smoke"
        go mod init fyne_smoke

        log "go get fyne.io/fyne/v2@v2.7.4"
        go get fyne.io/fyne/v2@v2.7.4

        log "go mod tidy"
        go mod tidy

        log "go run main.go"
        local output
        if ! output="$(go run main.go 2>&1)"; then
            printf '%s\n' "$output" >&2
            fail "go run main.go failed"
        fi
        printf '%s\n' "$output"

        if ! printf '%s' "$output" | grep -q "Fyne initialized successfully"; then
            fail "expected 'Fyne initialized successfully' in output, did not find it"
        fi
    )

    log "smoke test passed: Fyne linked and initialized successfully on this host"
}

main "$@"
