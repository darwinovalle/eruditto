// Package clipboard is the bridge between the operating system's
// clipboard and Eruditto's storage and UI layers.
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────┐
//	│  Reader (this file)                                │
//	│  - abstracts the OS clipboard behind an interface  │
//	│  - Phase 5 supports text only                       │
//	│  - atotto/clipboard is the concrete implementation  │
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
// Why text-only in Phase 5?
//   - atotto/clipboard supports image read/write on X11, but the
//     full image pipeline (decode, hash, save to images/ dir,
//     generate thumbnail, restore from disk) spans several other
//     packages. Wiring that in stages risks partial work sitting
//     in main. Phase 5 ships a working text-only clipboard manager.
//   - Phase 7 widens the interface with ReadImage/WriteImage.
//     The breakage is contained: a single PR adds the methods,
//     updates the atotto implementation, and updates the mocks.
//   - domain.ClipTypeImage and domain.NewImageClip remain in the
//     domain layer. Other code paths (manual insert, import) can
//     still create image clips; the monitor simply does not.
package clipboard

import (
	"fmt"

	// atotto/clipboard is a thin wrapper around the OS clipboard
	// APIs (X11 selections on Linux, Win32 on Windows, pbcopy/pbpaste
	// on macOS). It works without a GUI event loop, which is what we
	// need for a daemon-style background monitor.
	"github.com/atotto/clipboard"
)

// Reader is the abstraction Eruditto uses to read and write the
// system clipboard.
//
// Phase 5 exposes only the text methods. Image support is added in
// Phase 7 by extending this interface — see the package comment for
// the migration plan.
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
}

// atottoReader is the production Reader, backed by atotto/clipboard.
//
// Constructed once at startup and shared across goroutines. Holds
// no mutable state, so a zero-value atottoReader is a valid
// implementation.
type atottoReader struct{}

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
		return "", fmt.Errorf("clipboard: read text: %w", err)
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
