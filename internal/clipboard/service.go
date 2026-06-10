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
	monitor   *Monitor
	reader    Reader
	repo      *history.Repository
	settings  *settings.Service
	log       *slog.Logger

	events    chan Event

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
	done      chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
}

// NewService constructs a Service. All dependencies are required.
//
// repo is the clip persistence layer. settings is consulted for the
// polling interval. log is the structured logger.
//
// The Service does not start the monitor until Start is called.
// Construction is cheap and side-effect-free.
func NewService(
	reader Reader,
	repo *history.Repository,
	settings *settings.Service,
	log *slog.Logger,
) *Service {
	if reader == nil {
		panic("clipboard: service requires a non-nil reader")
	}
	if repo == nil {
		panic("clipboard: service requires a non-nil repository")
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

		s.log.Info("clipboard service stopped")
	})
}

// consume is the service's consumer goroutine. It reads events from
// the monitor (via s.events) and reacts:
//
//   - EventOpNewClip: build a domain.Clip, call repo.Insert, log.
//   - EventOpError:   log at warn level; the monitor has already
//                     logged the same error so this is a courtesy
//                     for anyone subscribed to events.
//
// Image clip support is intentionally not implemented here. If
// Phase 5 ever produces an image clip (it should not — the monitor
// only watches text), we log and skip.
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
				s.persistClip(ctx, e.Clip)

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
// The clip from the monitor has ID=0 (the database assigns it).
// The repo handles dedupe via the hash UNIQUE constraint, so
// re-pasting the same content is a no-op beyond a single SELECT.
func (s *Service) persistClip(ctx context.Context, clip domain.Clip) {
	// Defensive: Phase 5 should not produce image clips. If one
	// arrives, log and skip rather than persist a half-formed row.
	if clip.Type != domain.ClipTypeText {
		s.log.Warn("monitor produced non-text clip; skipping",
			"type", clip.Type.String(),
		)
		return
	}

	id, err := s.repo.Insert(ctx, clip)
	if err != nil {
		s.log.Error("failed to persist clip",
			"error", err,
			"hash", clip.Hash,
		)
		return
	}
	s.log.Debug("clip persisted",
		"id", id,
		"hash", clip.Hash,
		"length", len(clip.Content),
	)
	s.notifySubscribers()

	// After persisting, enforce the user's history cap. This is
	// cheap (a single COUNT + maybe a DELETE) and keeps the
	// database size in check without a separate cron job.
	maxHistory := s.settings.GetMaxHistory(ctx)
	if maxHistory > 0 {
		if _, err := s.repo.EnforceMaxHistory(ctx, maxHistory); err != nil {
			s.log.Warn("failed to enforce max history", "error", err)
		}
	}
}

// RestoreClip puts the given clip back on the system clipboard.
//
// For text clips, this is straightforward: ask the reader to write
// the clip's content, with a self-write guard so the monitor does
// not re-publish the same content as a new event.
//
// For image clips, this is a Phase 7 feature. We return an explicit
// error rather than silently no-oping — silent skippage makes
// debugging "why didn't my paste work?" much harder.
func (s *Service) RestoreClip(ctx context.Context, clip domain.Clip) error {
	switch clip.Type {
	case domain.ClipTypeText:
		return s.restoreText(ctx, clip)
	case domain.ClipTypeImage:
		return fmt.Errorf("clipboard: restore image clip: not supported in Phase 5 (image support arrives in Phase 7)")
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
		// If the write failed, the monitor will eventually re-pick-up
		// the original clipboard state, but the suppression flag is
		// already set. The next tick will be a no-op; the one after
		// that resumes normal operation. Acceptable trade-off: a
		// single skipped tick is invisible; failing to write is not.
		return fmt.Errorf("clipboard: write text: %w", err)
	}

	// The caller (UI) typically closes the popup after this returns.
	// The monitor will skip its next tick because of the flag.
	_ = ctx // reserved for future use (e.g., post-restore notifications)
	return nil
}
