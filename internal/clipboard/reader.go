// Package clipboard is the bridge between the operating system's
// clipboard and Eruditto's storage and UI layers.
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────┐
//	│  Reader (this file)                                │
//	│  - abstracts the OS clipboard behind an interface  │
//	│  - supports text read/write and image write        │
//	│  - atotto/clipboard is the concrete implementation │
//	│    for text; image writes shell out to xclip       │
//	└────────────────────┬────────────────────────────────┘
//	                     │
//	                     ▼
//	┌─────────────────────────────────────────────────────┐
//	│  Monitor                                           │
//	│  - polls the reader on a fixed interval            │
//	│  - detects changes via content hash                │
//	│  - publishes events on a channel                   │
//	└────────────────────┬────────────────────────────────┘
//	                     │
//	                     ▼
//	┌─────────────────────────────────────────────────────┐
//	│  Service                                           │
//	│  - owns the monitor's lifecycle                    │
//	│  - routes new clips to the history repository      │
//	│  - persists image bytes via internal/images        │
//	│  - implements RestoreClip with self-write guard    │
//	└─────────────────────────────────────────────────────┘
//
// Why an interface?
//   - Testing: monitor and service can be exercised against an
//     in-memory mock without touching the real clipboard.
//   - Portability: if atotto/clipboard is ever replaced with a
//     different library (e.g., a Wayland-native one), only the
//     concrete reader changes. Monitor and service don't move.
//
// Why text-only read in Phase 5, image-only write added later?
//   - atotto/clipboard supports image read/write on X11, but the
//     full image pipeline (probe target, decode, hash, save to
//     images/ dir, generate thumbnail, restore from disk) spans
//     several other packages.
//   - Phase 7 splits the responsibility: monitor detects images
//     via xclip TARGETS (image_linux.go); the service persists
//     the bytes via internal/images; RestoreClip re-emits the
//     bytes via the new WriteImage method below.
package clipboard

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	// atotto/clipboard is a thin wrapper around the OS clipboard
	// APIs (X11 selections on Linux, Win32 on Windows, pbcopy/pbpaste
	// on macOS). It works without a GUI event loop, which is what we
	// need for a daemon-style background monitor.
	"github.com/atotto/clipboard"
)

// Reader is the abstraction Eruditto uses to read and write the
// system clipboard.
//
// All methods must be safe to call from multiple goroutines.
// The atotto implementation relies on xclip/xsel on X11, which are
// themselves safe; on other platforms the library guarantees it.
type Reader interface {
	// ReadText returns the current clipboard text.
	//
	// Returns ("", nil) if the clipboard is empty or holds non-text
	// content (e.g., an image on X11). Callers should treat empty
	// text as "no content" rather than as an error.
	//
	// A non-nil error indicates the OS clipboard could not be read
	// at all (e.g., no display server, xclip missing). The monitor
	// logs and retries on the next tick.
	ReadText() (string, error)

	// WriteText replaces the clipboard contents with s.
	//
	// Used by Service.RestoreClip to put a stored clip back on the
	// clipboard when the user selects it from the popup. The caller
	// is responsible for setting the self-write suppression flag
	// before invoking this method.
	WriteText(s string) error

	// WriteImage replaces the clipboard contents with the given
	// image bytes, advertised as image/png.
	//
	// Used by Service.RestoreClip to put a stored image clip back
	// on the clipboard. The caller is responsible for setting the
	// self-write suppression flag before invoking this method.
	//
	// The concrete implementation on Linux uses a long-lived xclip
	// daemon (-loops 0) so the image stays available until replaced
	// by another app or until Stop() is called.
	WriteImage(data []byte) error

	// Stop releases any long-lived resources held by the reader.
	// Called during graceful shutdown to prevent clipboard-owner
	// daemons from outliving the app.
	Stop()
}

// atottoReader is the production Reader, backed by atotto/clipboard
// for text and shell-out xclip for image writing.
//
// Constructed once at startup and shared across goroutines. Holds
// no mutable state for text operations; the imageOwner and
// imageOwnerMu fields track the long-lived xclip daemon for
// clipboard ownership — see WriteImage.
type atottoReader struct {
	// imageOwner is the long-lived xclip process that serves image
	// bytes to requesting apps. nil if no image is on the clipboard.
	// The process is replaced (killed + restarted) on each WriteImage
	// call because xclip reads from stdin once at startup and cannot
	// be updated in place.
	imageOwner *exec.Cmd

	// imageOwnerMu guards imageOwner. WriteImage and Stop may be
	// called from different goroutines.
	imageOwnerMu sync.Mutex
}

// NewAtottoReader returns a Reader backed by github.com/atotto/clipboard.
//
// The constructor is a plain function rather than `&atottoReader{}`
// at call sites so that future variants (a Wayland-native reader, a
// fake reader for benchmarks) can be selected at the composition root
// without changing call sites.
func NewAtottoReader() Reader {
	return &atottoReader{}
}

// ReadText calls xclip/xsel on X11, Win32 on Windows, pbpaste on macOS.
//
// On Linux without a display server, atotto/clipboard returns
// "clipboard: cannot open display" or similar. We wrap the error
// with a clear context so the monitor's log line tells the user
// what is wrong.
func (a *atottoReader) ReadText() (string, error) {
    s, err := clipboard.ReadAll()
    if err != nil {

        // Image clipboard contents are expected.
        // The atotto library reports:
        //
        // Error: target STRING not available
        //
        // when the clipboard currently holds image/png.
        //
        // Treat it as "no text available" rather than a real error.

        if strings.Contains(err.Error(), "target STRING not available") {
            return "", nil
        }

        return "", fmt.Errorf(
            "clipboard: read text: %w",
            err,
        )
    }

    return s, nil
}

// WriteText calls the OS-specific clipboard write path.
//
// atotto/clipboard.WriteAll is synchronous: it does not return
// until the OS has accepted the data. That is the behaviour we
// want — the caller of RestoreClip should not proceed (clear the
// suppression flag, return to the user) until the clipboard is
// actually updated.
func (a *atottoReader) WriteText(s string) error {
	if err := clipboard.WriteAll(s); err != nil {
		return fmt.Errorf("clipboard: write text: %w", err)
	}
	return nil
}

// WriteImage places data on the clipboard as image/png via xclip.
//
// xclip is launched as a daemon (-loops 0) so it stays alive and
// serves the image bytes to requesting apps on demand. Without
// -loops 0, xclip exits after the first request, which often
// happens before the target app has finished its ICCCM handshake.
//
// Why a reaping goroutine:
//
// cmd.Start() returns immediately after spawning the xclip process
// and starting the goroutine that copies stdin into xclip's pipe.
// The calling goroutine (restoreImage) returns. The bytes.Reader
// we passed as cmd.Stdin is kept alive via the *exec.Cmd field;
// Go's runtime will not garbage-collect it while the *exec.Cmd is
// reachable.
//
// However, when xclip eventually exits — because it was killed by
// Stop(), or because another app took over the selection, or for
// any reason — the process becomes a zombie unless someone reaps
// it via cmd.Wait(). Calling cmd.Wait() in the SAME goroutine
// that called Start() would block restoreImage indefinitely, so
// we run cmd.Wait() in a fresh goroutine. This guarantees:
//
//   - restoreImage returns immediately
//   - xclip's data is fully consumed and copied to its internal
//     representation before xclip claims the selection
//   - When xclip exits for any reason, cmd.Wait() in the goroutine
//     reaps the process — no zombie accumulates
//
// Why kill the previous daemon before starting a new one:
//
// xclip reads from stdin once at startup and then serves the data
// already in memory. It cannot be "updated in place" — putting a
// new image on the clipboard requires a new xclip process. We kill
// the previous one inside the WriteImage lock so at most one xclip
// daemon is alive at any time.
//
// The mutex also ensures Stop() sees a consistent view: if Stop()
// runs concurrently with WriteImage(), exactly one of them will
// own the imageOwner at any moment.
//
// -q is set so xclip doesn't print errors to stderr (which would
// clutter Eruditto's log output — we have our own monitor that
// reports errors).
func (a *atottoReader) WriteImage(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("clipboard: write image: empty data")
	}

	a.imageOwnerMu.Lock()
	defer a.imageOwnerMu.Unlock()

	// Reap any previous daemon before starting a new one.
	if a.imageOwner != nil {
		_ = a.imageOwner.Process.Kill()
		// Don't call Wait() here: the reaping goroutine
		// launched by the previous WriteImage() already owns
		// the Wait. cmd.Wait() panics if called twice.
		a.imageOwner = nil
	}

	cmd := exec.Command(
		"xclip",
		"-selection", "clipboard",
		"-t", "image/png",
		"-loops", "0", // stay alive, serve data on demand
		"-q",          // suppress xclip's stderr noise
		"-i",
	)
	cmd.Stdin = bytes.NewReader(data)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("clipboard: write image: start xclip: %w", err)
	}

	a.imageOwner = cmd

	// Reap xclip when it eventually exits. Without this goroutine
	// xclip would become a zombie when it shuts down naturally.
	// cmd.Wait() blocks until the process exits; running it in a
	// goroutine ensures restoreImage returns immediately while the
	// reaper stays alive as long as xclip is alive.
	go func() {
		_ = cmd.Wait()
	}()

	return nil
}

// Stop kills the long-lived xclip clipboard owner, if any. Safe
// to call multiple times and from any goroutine.
//
// We send SIGKILL and drop the reference. The reaping goroutine
// started by WriteImage() continues to run and eventually calls
// cmd.Wait() on the killed process, reaping it as a zombie.
//
// Stop returns as soon as the signal is delivered; we do NOT
// block on cmd.Wait() because the reaper goroutine owns that
// call (cmd.Wait() must only be called once per Cmd).
func (a *atottoReader) Stop() {
	a.imageOwnerMu.Lock()
	defer a.imageOwnerMu.Unlock()

	if a.imageOwner != nil {
		_ = a.imageOwner.Process.Kill()
		a.imageOwner = nil
	}
}
