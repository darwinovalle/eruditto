// Service is the orchestration layer for the clipboard subsystem.
//
// Responsibilities:
//
//   - Owns the monitor's goroutine lifecycle (Start, Stop).
//   - Subscribes to monitor events and persists new clips via the
//     history repository.
//   - Exposes RestoreClip, the "put this back on the clipboard"
//     operation that the popup UI calls when the user picks a row.
//
// Layering:
//
//	┌─────────────────────────────────────────────────────────┐
//	│              Popup UI (Phase 8)                          │
//	└────────────────────────┬────────────────────────────────┘
//	                         │ RestoreClip, Events
//	                         ▼
//	┌─────────────────────────────────────────────────────────┐
//	│              Service (this file)                         │
//	│  - subscribes to monitor events, persists them          │
//	│  - exposes RestoreClip with self-write guard            │
//	└───────┬─────────────────────────────┬───────────────────┘
//	        │                             │
//	        ▼                             ▼
//	┌────────────────┐           ┌────────────────┐
//	│   Monitor      │           │   Repository   │
//	│   (polling)    │           │   (Phase 2)    │
//	└────────────────┘           └────────────────┘
//
// Why a separate Service rather than collapsing into Monitor?
//
//   - Monitor's job is "tell me when the clipboard changes".
//   - Service's job is "when it changes, do something useful and
//     give the UI a way to undo it".
//   - Different responsibilities, different reasons to change. The
//     monitor changes if we add a different polling strategy; the
//     service changes if we add new restore semantics (e.g.,
//     pasting on click in Phase 8).
package clipboard

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/history"
	"github.com/darwinovalle/eruditto/internal/images"
	"github.com/darwinovalle/eruditto/internal/settings"
)

// eventsBufferSize is the buffer size of the channel between the
// monitor and the service. The monitor is the producer, the service
// is the consumer. The buffer absorbs short bursts (e.g., user pastes
// 5 things quickly) without forcing the monitor to block.
//
// 16 is enough for a 500ms polling interval and 32 events/second,
// which is well above any realistic user paste rate.
const eventsBufferSize = 16

// Service is the clipboard subsystem's orchestration layer.
//
// Constructed via NewService. Once constructed, call Start to begin
// monitoring and Stop to shut down cleanly. Events() exposes the
// channel that UI code subscribes to.
type Service struct {
	monitor  *Monitor
	reader   Reader
	repo     *history.Repository
	images   *images.Storage
	settings *settings.Service
	log      *slog.Logger

	events chan Event

	subscribersMu sync.Mutex
	subscribers   []chan struct{}

	// runCtx and runCancel coordinate the consumer goroutine.
	// runCtx is cancelled by Stop; the consumer goroutine observes
	// it and exits. We keep a separate context from the caller's
	// because Stop must be able to cancel even if the caller's
	// context is still alive (e.g., during a clean shutdown where
	// the root context cancels other services first).
	runCtx    context.Context
	runCancel context.CancelFunc

	// done is closed when the consumer goroutine has fully exited.
	// Stop blocks on this to guarantee no goroutine outlives the
	// Service. Without it, a caller that immediately calls
	// repo.Close() after Stop could race with a still-running
	// consumer that holds a *sql.DB.
	done chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
}

// NewService constructs a Service. All dependencies are required.
//
// repo is the clip persistence layer. images is the on-disk image
// storage (used to save clipboard images and to read them back when
// restoring). settings is consulted for the polling interval. log is
// the structured logger.
//
// The Service does not start the monitor until Start is called.
// Construction is cheap and side-effect-free.
func NewService(
	reader Reader,
	repo *history.Repository,
	imagesStore *images.Storage,
	settings *settings.Service,
	log *slog.Logger,
) *Service {
	if reader == nil {
		panic("clipboard: service requires a non-nil reader")
	}
	if repo == nil {
		panic("clipboard: service requires a non-nil repository")
	}
	if imagesStore == nil {
		panic("clipboard: service requires a non-nil images storage")
	}
	if settings == nil {
		panic("clipboard: service requires a non-nil settings service")
	}
	if log == nil {
		panic("clipboard: service requires a non-nil logger")
	}

	return &Service{
		reader:   reader,
		repo:     repo,
		images:   imagesStore,
		settings: settings,
		log:      log,
		events:   make(chan Event, eventsBufferSize),
		done:     make(chan struct{}),
	}
}

// Events returns a read-only channel of monitor events.
// UI code consumes this; the service publishes to it.
//
// The channel is closed when the service is stopped. Subscribers
// should range over it or select on a done signal.
func (s *Service) Events() <-chan Event {
	return s.events
}

// Subscribe returns a notification channel that receives a signal
// whenever clipboard history changes.
func (s *Service) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)

	s.subscribersMu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.subscribersMu.Unlock()

	return ch
}

func (s *Service) notifySubscribers() {
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()

	for _, ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Start begins monitoring. It is safe to call Start at most once.
//
// Internally:
//   - Reads the poll interval from settings (falling back to 500ms).
//   - Creates a Monitor with that interval.
//   - Launches the monitor goroutine.
//   - Launches a consumer goroutine that persists NewClip events.
//
// Start returns immediately. Both goroutines exit when Stop is
// called or when the caller's context (passed to Start) is cancelled.
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		interval := s.settings.GetPollInterval(ctx)
		s.log.Info("clipboard service starting", "interval", interval)

		s.runCtx, s.runCancel = context.WithCancel(ctx)

		s.monitor = NewMonitor(s.reader, s.events, interval, s.log)
		s.monitor.Start(s.runCtx)

		go s.consume(s.runCtx)
	})
}

// Stop ends monitoring. It is safe to call Stop at most once.
// Subsequent calls are no-ops.
//
// Stop cancels the monitor's context, which causes both goroutines
// (the monitor and the consumer) to exit. Stop then waits on the
// done channel so the caller can be sure no goroutine outlives
// this call.
//
// Stop closes the events channel so subscribers see a clean
// termination.
//
// Stop also releases X11 clipboard ownership held by the
// image writer, so the selection owner goroutine inside
// golang.design/x/clipboard exits and does not outlive the app.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		if s.runCancel != nil {
			s.runCancel()
		}
		<-s.done

		// Close the events channel so subscribers' `for e := range
		// s.Events()` loops exit. Safe to do after `done` is closed:
		// no producer is still writing.
		close(s.events)

		// Release any long-lived clipboard resources held by the
		// reader (e.g., the xclip -loops 0 daemon for images).
		s.reader.Stop()

		s.log.Info("clipboard service stopped")
	})
}

// consume is the service's consumer goroutine. It reads events from
// the monitor (via s.events) and reacts:
//
//   - EventOpNewClip (text):  repo.Insert, notify, enforce max history.
//   - EventOpNewClip (image): save bytes to disk, Insert, set
//     image_path, notify, enforce max history.
//   - EventOpError:           log at warn level; the monitor has
//     already logged the same error so this is a courtesy for
//     anyone subscribed to events.
func (s *Service) consume(ctx context.Context) {
	defer close(s.done)

	for {
		select {
		case <-ctx.Done():
			return

		case e, ok := <-s.events:
			if !ok {
				// Channel closed (should not happen — Stop closes
				// after we're done — but be defensive).
				return
			}

			switch e.Op {
			case EventOpNewClip:
				s.persistClip(ctx, e)

			case EventOpError:
				// Monitor already logged. Re-log here only if we
				// want a per-subscriber audit trail. For now, debug
				// level keeps it quiet by default.
				s.log.Debug("clipboard event: error", "error", e.Err)
			}
		}
	}
}

// persistClip inserts a clip into the history repository.
//
// For text clips the path is straightforward: build a Clip, Insert,
// notify subscribers, enforce max history.
//
// For image clips the path is two-step because the on-disk filename
// is derived from the DB-assigned id:
//
//  1. Insert the clip with an empty image_path — repo.Insert returns id.
//     Dedupe by hash: if the same image was already captured, Insert
//     returns the existing id and we do NOT re-save the bytes to disk.
//  2. Save the bytes to disk via s.images.Save(id, bytes). The returned
//     path is the full path of the saved file.
//  3. UPDATE the row to set image_path = savedPath.
//
// On any failure between step 1 and step 3 (storage error, decode
// error, too-large image), the inserted row is rolled back by
// deleting it so we never leave an "image clip with no file behind
// it" dangling in history.
//
// TODO(cleanup): EnforceMaxHistory currently does not delete the
// on-disk image when it trims an image row. Files leak. Out of
// scope for this change.
func (s *Service) persistClip(ctx context.Context, e Event) {
	clip := e.Clip

	switch clip.Type {
	case domain.ClipTypeText:
		s.persistTextClip(ctx, clip)

	case domain.ClipTypeImage:
		if e.ImageBytes == nil {
			s.log.Error("image event with no bytes; skipping",
				"hash", clip.Hash,
			)
			return
		}
		s.persistImageClip(ctx, clip, e.ImageBytes)

	default:
		s.log.Warn("unknown clip type; skipping",
			"type", clip.Type.String(),
			"hash", clip.Hash,
		)
		return
	}

	// History cap enforcement runs once per clip, regardless of type.
	maxHistory := s.settings.GetMaxHistory(ctx)
	if maxHistory > 0 {
		if _, err := s.repo.EnforceMaxHistory(ctx, maxHistory); err != nil {
			s.log.Warn("failed to enforce max history", "error", err)
		}
	}
}

// persistTextClip handles the text branch of persistClip.
func (s *Service) persistTextClip(ctx context.Context, clip domain.Clip) {
	id, err := s.repo.Insert(ctx, clip)
	if err != nil {
		s.log.Error("failed to persist text clip",
			"error", err,
			"hash", clip.Hash,
		)
		return
	}
	s.log.Debug("clip persisted",
		"id", id,
		"type", "text",
		"hash", clip.Hash,
		"length", len(clip.Content),
	)
	s.notifySubscribers()
}

// persistImageClip handles the image branch of persistClip.
//
// Sequence:
//
//  1. Insert row with empty image_path → returns id (or existing id
//     if the hash already exists; in that case we skip the disk save
//     because the file is already there).
//  2. Save bytes to disk via images.Storage.Save(id, bytes).
//     Returns the full file path. May fail with ErrImageTooLarge,
//     ErrInvalidImage, or a wrapped os error.
//  3. UPDATE the row to set image_path.
//
// Any failure between Insert and UpdateImagePath triggers a
// compensating delete of the row so the DB does not contain a
// half-formed image clip.
func (s *Service) persistImageClip(ctx context.Context, clip domain.Clip, imageBytes []byte) {
	// Step 1: insert the row. The monitor built this clip with
	// ImagePath="" because the disk filename is derived from the
	// DB-assigned id; we cannot know the id until after Insert.
	// domain.Clip.Validate permits image clips with empty ImagePath
	// as the explicit "tentative insert" state — see Validate's
	// doc comment in internal/domain/clip.go.
	id, err := s.repo.Insert(ctx, clip)
	if err != nil {
		s.log.Error("failed to persist image clip row",
			"error", err,
			"hash", clip.Hash,
		)
		return
	}

	// Check whether this was a fresh insert or a dedup hit.
	// GetByID lets us see the current image_path; if non-empty,
	// the file is already on disk from a previous capture and we
	// can skip the Save call entirely.
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		s.log.Error("failed to read back inserted image clip",
			"error", err,
			"id", id,
		)
		return
	}
	if existing.ImagePath != "" {
		s.log.Debug("image clip already on disk; skipping save",
			"id", id,
			"hash", clip.Hash,
			"image_path", existing.ImagePath,
		)
		s.notifySubscribers()
		return
	}

	// Step 2: save the bytes. id > 0 because Insert returns the
	// existing id on dedup (which we handled above) or a freshly
	// assigned positive id otherwise.
	imagePath, err := s.images.Save(id, imageBytes)
	if err != nil {
		s.log.Error("failed to save image to disk",
			"error", err,
			"id", id,
			"hash", clip.Hash,
			"size", len(imageBytes),
		)
		// Compensating delete: remove the orphaned DB row so the
		// history list does not show a clip with no file behind it.
		if _, delErr := s.repo.Delete(ctx, id); delErr != nil {
			s.log.Error("failed to roll back orphaned image row",
				"error", delErr,
				"id", id,
			)
		}
		return
	}

	// Step 3: update the row with the real image_path.
	if err := s.repo.UpdateImagePath(ctx, id, imagePath); err != nil {
		s.log.Error("failed to update image_path",
			"error", err,
			"id", id,
			"image_path", imagePath,
		)
		// Compensating cleanup: delete both the row and the file
		// we just wrote, otherwise we leave a dangling file with
		// no DB row pointing at it.
		if _, delErr := s.repo.Delete(ctx, id); delErr != nil {
			s.log.Error("failed to roll back orphaned image row",
				"error", delErr,
				"id", id,
			)
		}
		if delErr := s.images.Delete(imagePath); delErr != nil {
			s.log.Error("failed to delete orphaned image file",
				"error", delErr,
				"path", imagePath,
			)
		}
		return
	}

	s.log.Debug("image clip persisted",
		"id", id,
		"hash", clip.Hash,
		"size", len(imageBytes),
		"image_path", imagePath,
	)
	s.notifySubscribers()
}

// RestoreClip puts the given clip back on the system clipboard.
//
// For text clips, this is straightforward: ask the reader to write
// the clip's content, with a self-write guard so the monitor does
// not re-publish the same content as a new event.
//
// For image clips, load the bytes from disk via the images storage
// and ask the reader to put them on the clipboard as image/png,
// again under the self-write guard.
func (s *Service) RestoreClip(ctx context.Context, clip domain.Clip) error {
	switch clip.Type {
	case domain.ClipTypeText:
		return s.restoreText(ctx, clip)
	case domain.ClipTypeImage:
		return s.restoreImage(ctx, clip)
	default:
		return fmt.Errorf("clipboard: restore: unknown clip type %q", clip.Type.String())
	}
}

// restoreText is the text-only branch of RestoreClip.
//
// Order of operations matters:
//
//  1. Set the self-write suppression flag BEFORE writing.
//  2. Write the text to the clipboard.
//  3. Return.
//
// The flag is set first because the monitor's tick is async: if we
// wrote first and then set the flag, a tick that fires in the
// millisecond between the two would see the new content without
// the guard and republish it. Setting the flag first closes that
// window.
func (s *Service) restoreText(ctx context.Context, clip domain.Clip) error {
	if clip.Content == "" {
		return fmt.Errorf("clipboard: restore: text clip has empty content")
	}

	s.monitor.SuppressNext()
	s.log.Debug("restoring text clip",
		"hash", clip.Hash,
		"length", len(clip.Content),
	)

	if err := s.reader.WriteText(clip.Content); err != nil {
		return fmt.Errorf("clipboard: write text: %w", err)
	}

	// Note: auto-paste is intentionally NOT done here. The popup
	// layer (internal/ui/popup.go pasteClip) is responsible for
	// auto-paste because it owns the captured previously-focused
	// window ID and can explicitly reactivate it before sending
	// ctrl+v. Doing it from the service would race with the popup
	// closing and the focus not yet having transferred.
	return nil
}

func (s *Service) isAutoPasteEnabled(ctx context.Context) bool {
	val, err := s.settings.Get(ctx, domain.KeyAutoPaste)
	if err != nil {
		return false
	}

	return val == "true"
}

// IsAutoPasteEnabled returns the user's Auto Paste setting.
func (s *Service) IsAutoPasteEnabled(ctx context.Context) bool {
	return s.isAutoPasteEnabled(ctx)
}

func (s *Service) autoPaste() error {
	// Unused. Kept as a reference for tests that may want to call
	// it directly. The production auto-paste path lives in
	// internal/ui/popup.go pasteClip, which captures the previously
	// focused window via xdotool and explicitly reactivates it
	// before sending ctrl+v. Doing it from the service layer would
	// require plumbing the captured window ID through, and the
	// popup already has the right context (Fyne window lifecycle).
	//
	// Removed the runtime body so it does not silently do the wrong
	// thing if it is ever called.
	return fmt.Errorf("clipboard: service.autoPaste is a no-op; use ui.AutoPaste via popup.pasteClip")
}

// restoreImage is the image branch of RestoreClip.
//
// Order of operations:
//
//  1. Set the self-write suppression flag BEFORE any side effects.
//  2. Load the bytes from disk via s.images.Load.
//  3. Write the bytes to the clipboard via s.reader.WriteImage,
//     which launches a long-lived xclip daemon (-loops 0). The
//     daemon owns the X11 CLIPBOARD selection and serves the image
//     bytes to requesting apps on demand until another app takes
//     ownership or eruditto exits.
//
// If Load fails, the suppression flag has already been consumed by
// the next monitor tick — this is harmless because the next tick
// will simply see whatever is on the clipboard (no new content, no
// event) and resume normal operation. The cost of an "extra" skipped
// tick is negligible.
//
// If WriteImage fails, the same logic applies: the flag is consumed
// either way. The user sees the error from RestoreClip and can retry.
func (s *Service) restoreImage(_ context.Context, clip domain.Clip) error {
	if clip.ImagePath == "" {
		return fmt.Errorf("clipboard: restore: image clip has empty image_path")
	}

	s.monitor.SuppressNext()
	s.log.Debug("restoring image clip",
		"hash", clip.Hash,
		"image_path", clip.ImagePath,
	)

	data, err := s.images.Load(clip.ImagePath)
	if err != nil {
		return fmt.Errorf("clipboard: load image %q: %w", clip.ImagePath, err)
	}

	if err := s.reader.WriteImage(data); err != nil {
		return fmt.Errorf("clipboard: write image: %w", err)
	}

	// Optional auto-paste is intentionally NOT done here. The
	// popup layer (internal/ui/popup.go pasteClip) handles
	// auto-paste with explicit focus restoration — see restoreText
	// for the same comment.
	return nil
}
