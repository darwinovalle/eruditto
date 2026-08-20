# Contributing to Eruditto

Thanks for wanting to help! This guide gets you from `git clone` to a running
debug build, then explains how we work so your PR merges cleanly.

## Table of contents

1. [Prerequisites](#prerequisites)
2. [One-command setup](#one-command-setup)
3. [Manual setup](#manual-setup)
4. [Build & run](#build--run)
5. [Tests & code quality](#tests--code-quality)
6. [Project layout](#project-layout)
7. [Git workflow](#git-workflow)
8. [Dependency health & security](#dependency-health--security)
9. [Questions](#questions)

---

## Prerequisites

- **Linux with X11** (Wayland works via XWayland) and an OpenGL-capable GPU.
  Eruditto is a desktop GUI app — it needs a display to run.
- **Go 1.25+** (check with `go version`).
- System packages — see the next section. On Ubuntu/Debian these are:

  ```bash
  sudo apt install xorg-dev libgl1-mesa-dev xclip xdotool
  ```

  > Why: `xorg-dev` / `libgl1-mesa-dev` provide the X11 + OpenGL headers Fyne
  > compiles against (CGO). `xclip` and `xdotool` are runtime tools for image
  > clipboard and auto-paste.

## One-command setup

```bash
git clone <your-fork-url> eruditto
cd eruditto
bash scripts/setup.sh
```

`scripts/setup.sh` detects your distro, installs the system packages above
(asks for sudo), verifies your Go toolchain, and builds `./eruditto`. It is
idempotent — safe to re-run. Useful flags:

```bash
bash scripts/setup.sh --no-deps   # skip the system install, just build
bash scripts/setup.sh --no-build  # install/verify only, don't build
```

## Manual setup

If you prefer to install by hand:

```bash
# System deps (Debian/Ubuntu)
sudo apt install xorg-dev libgl1-mesa-dev xclip xdotool

# Go modules — no venv needed. go.mod + go.sum pin exact versions and
# hashes; `go build` downloads them automatically.
go mod download

# Build
go build -o eruditto ./cmd/eruditto
```

## Build & run

```bash
go build -o eruditto ./cmd/eruditto   # or: make build  (outputs to ./build/)
./eruditto                            # normal run
./eruditto -debug                     # debug-level logging to stderr — use this when reproducing bugs
./eruditto -version                   # print version and exit
./eruditto --install-desktop          # opt-in: app launcher entry + icons
./eruditto --popup                    # show the popup (forwards to a running instance)
```

Running `./eruditto` never touches your system (no autostart files, no
`.desktop` entries). Everything is opt-in via flags or the settings UI.

## Tests & code quality

```bash
make test    # go test ./...
make vet     # go vet ./...
make fmt     # gofmt -w on the whole tree
make smoke   # environment smoke test (Fyne/CGO/X11 works on this host)
```

Always run `gofmt` and `go vet` before committing. Tests live next to the code
they cover under `internal/`.

## Project layout

```
cmd/eruditto/          — composition root: flags, startup wiring, single-instance IPC
internal/autostart/    — XDG + systemd autostart management
internal/clipboard/    — clipboard reader (xclip-based) + history service
internal/database/     — SQLite open/migrations
internal/domain/       — settings keys, domain types
internal/history/      — clipboard history repository
internal/hotkeys/      — global hotkey registration
internal/images/       — image detection and processing
internal/settings/     — SQLite-backed settings service
internal/tray/         — system tray, embedded icons, install/uninstall
internal/ui/           — Fyne UI: popup, settings, auto-paste, theming
pkg/xdg/               — XDG base-directory helpers
```

The `cmd/eruditto/main.go` file is the only place that knows about all
packages — keep it that way. New features should live in `internal/` and be
wired in `main.go`.

## Git workflow

All changes are submitted through pull requests to the protected `main`
branch. Do not push directly to `main`.

1. Fork the repository (if you do not have write access), then clone it and
   start from the latest `main` branch:

   ```bash
   git clone <your-fork-url> eruditto
   cd eruditto
   git switch main
   git pull --ff-only origin main
   ```

2. Create a short-lived branch named after the work, for example
   `fix/popup-position` or `feat/image-ocr`:

   ```bash
   git switch -c fix/describe-your-change
   ```

3. Make and test your changes, then commit them with signed commits and push
   the branch to your fork:

   ```bash
   make fmt
   make vet
   make test
   git add .
   git commit -S -m "Describe the change"
   git push -u origin fix/describe-your-change
   ```

4. Open a pull request from your branch to `main`. The CI checks must pass,
   the branch must be up to date, and all review conversations must be
   resolved. At least one approval from the project maintainer/code owner is
   required before the maintainer merges the pull request.

CI checks formatting, module tidiness, vetting, building, tests (including the
race detector), and known dependency vulnerabilities.

Additional commits pushed to the branch may dismiss existing approvals, so
please allow the checks and review to complete before requesting the final
merge.

If you're fixing a bug, it helps to include the debug-log output that shows
the failure (run `./eruditto -debug`).

## Dependency health & security

Eruditto's dependencies are ordinary open-source Go libraries, not binaries
you execute — you compile them yourself from source. That removes the
"executable virus" class of risk. The remaining risk is supply-chain: an
upstream package being compromised. The Go toolchain mitigates this strongly,
and you can verify it in seconds:

```bash
go mod verify        # confirms every downloaded module matches its go.sum hash
                     # (go.sum is checked into the repo — tampering breaks the build)
govulncheck ./...    # Google's official vulnerability scanner for Go deps
                     # (install: go install golang.org/x/vuln/cmd/govulncheck@latest)
```

Direct dependencies (all mainstream, widely adopted):

| Module | Purpose | Maintainer |
| --- | --- | --- |
| `fyne.io/fyne/v2` | GUI toolkit | Fyne project |
| `fyne.io/systray` | system tray | Fyne project |
| `github.com/atotto/clipboard` | clipboard primitives | community (also used by Fyne) |
| `golang.design/x/hotkey` | global hotkeys | golang.design |
| `golang.org/x/image`, `x/text`, `x/net`, `x/sys` | stdlib extensions | Go team (Google) |
| `modernc.org/sqlite` | pure-Go SQLite (no CGO) | modernc.org |

Practices we keep:
- **Pinned, hashed versions** — `go.sum` is committed; a dependency can't be
  silently swapped or downgraded.
- **Run `govulncheck` in CI** (or at least before a release) and update any
  flagged modules.
- **Stay current** — `go list -m -u all` shows what's out of date; bump in a
  dedicated PR so a version change is reviewable on its own.

There is no such thing as an absolute guarantee with third-party code, but the
above is the ecosystem-standard posture, and it's the same one large Go
projects rely on.

## Questions

Open an issue, or ask in the relevant PR — someone is usually around.
