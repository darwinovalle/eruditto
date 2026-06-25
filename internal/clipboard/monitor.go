// Monitor polls the clipboard on a fixed interval, detects changes
// via content hashing, and publishes new clips to subscribers.
//
// Lifecycle:
//
//	┌──────────────┐    Start(ctx)    ┌──────────────┐
//	│   (new)      │ ───────────────► │  running     │
//	└──────────────┘                  └──────┬───────┘
//	                                          │ ctx done
//	                                          ▼
//	                                   ┌──────────────┐
//	                                   │  stopped     │
//	                                   └──────────────┘
//
// Start launches a single goroutine. Stop is implicit via context
// cancellation; the goroutine returns when ctx is done. This avoids
// the bookkeeping of an explicit Stop method and integrates cleanly
// with the rest of the app's shutdown sequence (single root context,
// everything cancels together).
//
// What "change" means:
//
//   - The current clipboard text is hashed.
//   - If the hash equals the last seen hash, nothing happens.
//   - If the hash differs (or the clipboard was empty and now isn't),
//     a NewClip event is published and the hash is updated.
//
// Self-write suppression:
//
//   - Service.RestoreClip sets a "suppress next tick" flag on the
//     monitor before writing to the clipboard.
//   - The first tick after the flag is set is skipped: the monitor
//     sees the just-written content but does NOT publish a new event.
//   - After that one tick, the flag is cleared and monitoring resumes.
//
// Why one tick of suppression, not indefinite? Because the user's
// intent is "I am restoring this clip, don't re-store it." After one
// tick, the clipboard is stable and any subsequent change is a real
// user action.
//
// Why polling at all?
//
//   - X11 and Wayland have no unified event-based clipboard API that
//     works from a Go process without a GUI event loop.
//   - Polling is simple, reliable, easy to test (mock the reader),
//     and adds negligible CPU at 500ms.
//   - The interface is swappable: a future Wayland-native reader
//     that supports event-based watching can drop in without changing
//     this file.
package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/pkg/hash"
)

// imageProbe lets tests substitute ClipboardHasImage / ReadImage.
// In production these point at the real xclip-backed functions in
// image_linux.go. Tests can replace them to drive the image branch
// without shelling out to xclip.
//
// The fields are functions rather than an interface so the monitor
// does not need to import image_linux.go (which would pull `exec`
// into the test binary's link graph on every platform).
var imageProbe = struct {
	HasImage func() bool
	Read     func() ([]byte, error)
}{
	HasImage: ClipboardHasImage,
	Read:     ReadImage,
}

// Event is what the monitor publishes to subscribers.
//
// Op discriminates the event shape:
//
//	EventOpNewClip — a new clip was captured; Clip is populated.
//	                 ImageBytes is populated for image events and
//	                 nil for text events. Subscribers that persist
//	                 the event write the bytes to disk and then
//	                 update the DB row with the resulting path.
//	EventOpError   — a tick failed; Err is populated, Clip is zero.
type Event struct {
	Op         EventOp
	Clip       domain.Clip
	ImageBytes []byte // set only for image events; nil for text/error
	Err        error
}

// EventOp identifies the kind of monitor event.
type EventOp int

const (
	// EventOpNewClip is published when the monitor detects a new
	// clipboard entry. Clip.Type, Clip.Content (or ImagePath), and
	// Clip.CreatedAt are populated. Clip.ID is zero — the database
	// has not assigned one yet. The service is responsible for
	// calling repo.Insert to persist.
	EventOpNewClip EventOp = iota

	// EventOpError is published when a tick fails (e.g., the OS
	// clipboard is briefly unavailable). Err is populated.
	// Subscribers can choose to log this or surface it to the user;
	// the monitor itself has already logged the failure.
	EventOpError
)

// String makes EventOp readable in logs.
func (op EventOp) String() string {
	switch op {
	case EventOpNewClip:
		return "new_clip"
	case EventOpError:
		return "error"
	default:
		return "unknown"
	}
}

// ErrSelfWriteSuppressed is a sentinel returned (in Event.Err) for
// tick failures that were caused by the self-write guard skipping
// a cycle. Subscribers can use errors.Is to distinguish a routine
// "we just wrote this ourselves" from a real clipboard error.
var ErrSelfWriteSuppressed = errors.New("clipboard: tick skipped due to self-write suppression")

// Monitor polls the clipboard and publishes new clips.
//
// The zero value is not usable; construct via NewMonitor.
type Monitor struct {
	reader Reader
	events chan Event
	log    *slog.Logger

	// interval is read-only after construction; no synchronization.
	interval time.Duration

	// lastHash is the hash of the last clip we observed.
	// atomic.Value because the polling goroutine writes and a future
	// "is the monitor idle?" status query might read it.
	// LastHash() exposes it without forcing every reader to know
	// about atomic.
	lastHash atomic.Value // string

	// suppressNext, when set, causes the next tick to be skipped.
	// Cleared after one skip. Set by Service.RestoreClip before it
	// writes to the clipboard.
	//
	// We use atomic.Bool so the polling goroutine can read it
	// without taking a mutex on every tick. The set/clear pattern
	// is "set, write to clipboard, polling goroutine sees the flag
	// on the next tick, skips, clears". This is racy in the
	// theoretical sense (Service could be setting the flag at the
	// same instant the polling goroutine reads it) but in practice
	// the polling interval (≥100ms) and the WriteText call (~1ms)
	// are separated by orders of magnitude, so a missed set just
	// means one spurious event — not a data race.
	suppressNext atomic.Bool
}

// NewMonitor constructs a Monitor.
//
// interval is the time between polls. It must be positive; the
// monitor will panic on a non-positive interval because there is
// no sensible default to fall back to (500ms is a hint, not a
// guarantee — the caller chose it for a reason).
//
// events is the channel that NewClip and Error events are sent on.
// The caller owns the channel; the monitor does not close it
// because multiple subscribers may be reading. The monitor stops
// publishing when its context is cancelled; the caller closes the
// channel once all subscribers have returned.
func NewMonitor(reader Reader, events chan Event, interval time.Duration, log *slog.Logger) *Monitor {
	if interval <= 0 {
		panic("clipboard: monitor interval must be positive")
	}
	if log == nil {
		panic("clipboard: monitor requires a non-nil logger")
	}
	m := &Monitor{
		reader:   reader,
		events:   events,
		log:      log,
		interval: interval,
	}
	m.lastHash.Store("")
	m.suppressNext.Store(false)
	return m
}

// LastHash returns the hash of the most recently observed clip.
// Useful for status displays and tests; not part of the production
// hot path.
func (m *Monitor) LastHash() string {
	v, _ := m.lastHash.Load().(string)
	return v
}

// SuppressNext marks the next tick as a self-write skip.
// Called by Service.RestoreClip before it writes to the clipboard.
func (m *Monitor) SuppressNext() {
	m.suppressNext.Store(true)
}

// Start launches the polling goroutine. It returns immediately.
// The goroutine exits when ctx is cancelled.
//
// Start must be called at most once per Monitor.
func (m *Monitor) Start(ctx context.Context) {
	go m.run(ctx)
}

// run is the polling loop body. Exits when ctx is done.
func (m *Monitor) run(ctx context.Context) {
	m.log.Info("clipboard monitor started", "interval", m.interval)

	// Use a time.Ticker, not time.Sleep in a loop. A ticker accounts
	// for the time spent inside the tick body: if a read takes 200ms
	// and the interval is 500ms, the next tick fires 500ms after the
	// previous one started, not 500ms after it ended. This keeps the
	// observed cadence predictable as the tick body grows.
	t := time.NewTicker(m.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Info("clipboard monitor stopped")
			return

		case <-t.C:
			m.tick(ctx)
		}
	}
}

// tick performs one poll cycle: read, hash, dedupe, publish.
//
// All errors are logged and (where appropriate) published as
// EventOpError events. A single failed tick never crashes the loop.
func (m *Monitor) tick(ctx context.Context) {
	// Self-write guard. If the flag is set, the previous tick's
	// caller wrote to the clipboard. Skip this tick entirely and
	// clear the flag. The skip is invisible to subscribers unless
	// they care to listen for ErrSelfWriteSuppressed.
	if m.suppressNext.Swap(false) {
		m.log.Debug("clipboard tick skipped (self-write suppression)")
		// We do not publish an event for a self-write skip.
		// Subscribers would just see noise. The next real change
		// publishes a normal NewClip event.
		return
	}

	// Read current clipboard content.
	//
	// Image-first because xclip is the only reliable way to discover
	// an image is present — ReadText on a clipboard holding image/png
	// returns "target STRING not available". We probe for the image
	// target first; only if it is absent do we fall back to text.
	if imageProbe.HasImage() {
		imageBytes, err := imageProbe.Read()
		if err != nil {
			m.log.Warn(
				"failed to read clipboard image",
				"error",
				err,
			)
			return
		}

		h := hash.Bytes(imageBytes)

		if h == m.LastHash() {
			return
		}

		m.lastHash.Store(h)

		// Build a tentative image clip with no path. The service
		// layer is responsible for saving the bytes to disk and
		// assigning a real image_path before inserting into the
		// database. We cannot use domain.NewImageClip("path", h)
		// here because the path is not yet known (the file is
		// named after the DB-assigned clip ID, see
		// internal/images/storage.go Save).
		clip := domain.Clip{
			Type:      domain.ClipTypeImage,
			Hash:      h,
			CreatedAt: time.Now().UTC(),
		}

		m.log.Debug(
			"new image detected",
			"hash", h,
			"size", len(imageBytes),
		)

		m.publish(ctx, Event{
			Op:         EventOpNewClip,
			Clip:       clip,
			ImageBytes: imageBytes,
		})
		return
	}

	text, err := m.reader.ReadText()
	if err != nil {
		m.log.Warn("clipboard read failed", "error", err)
		m.publish(ctx, Event{Op: EventOpError, Err: err})
		return
	}

	// Skip empty clipboard. Per the checklist: do not store empty
	// selections (e.g., Ctrl+C with no selection). Also do not
	// publish an event — the previous tick already saw this state
	// (or the clipboard was just emptied by the user, which we
	// also ignore).
	if text == "" {
		return
	}

	// Hash for change detection.
	h := hash.String(text)
	if h == m.LastHash() {
		return
	}

	// New content. Build the clip and publish.
	clip := domain.NewTextClip(text, h)
	m.lastHash.Store(h)
	m.log.Debug("clipboard change detected",
		"type", clip.Type,
		"hash", h,
		"length", len(text),
	)
	m.publish(ctx, Event{Op: EventOpNewClip, Clip: clip})
}

// publish sends an event to the events channel, respecting the
// context. If the context is cancelled mid-publish, the event is
// dropped. If the channel is full, the publish blocks for up to a
// short window then drops the event with a warning.
//
// Why not block forever? A subscriber that is slow (e.g., a UI
// thread doing heavy layout) would freeze the monitor. Better to
// drop than to freeze.
//
// The "full channel" scenario should be rare because:
//   - The events channel is buffered (16 by default in main.go).
//   - NewClip events are at most a few per second in normal use.
//   - The UI is expected to drain the channel promptly.
func (m *Monitor) publish(ctx context.Context, e Event) {
	select {
	case m.events <- e:
		// delivered
	case <-ctx.Done():
		// shutting down; drop silently
	default:
		// channel is full; drop with a warning.
		// The subscriber will miss one event but the monitor
		// continues. This is the right trade-off: monitor
		// availability > event delivery.
		m.log.Warn("clipboard events channel full, dropping event",
			"op", e.Op.String(),
		)
	}
}
