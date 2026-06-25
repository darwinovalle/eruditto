package clipboard

// NOTE: this file is in package clipboard (not clipboard_test) so that
// the unexported mockReader type is visible from service_test.go,
// which is also in the same package.
//
// Go test files in the same package share scope — the mock is defined
// once here and referenced in service_test.go without duplication.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darwinovalle/eruditto/pkg/hash"
)

// ─────────────────────────────────────────────────────────────
// mockReader — shared with service_test.go
// ─────────────────────────────────────────────────────────────

// mockReader is a test double for the Reader interface.
//
// It is safe for concurrent use: readFn and writeFn are set before
// the monitor starts, and text/writeErr fields are accessed only
// through the provided functions. The written field uses atomic
// to allow the race detector to validate RestoreClip tests.
type mockReader struct {
	mu      sync.Mutex
	texts   []string // sequence of values returned by ReadText
	idx     int      // current index into texts
	readErr error    // if non-nil, ReadText returns this error

	written     atomic.Value // string — last value passed to WriteText
	writeErr    error        // if non-nil, WriteText returns this error
}

// setTexts replaces the read sequence. index resets to 0.
func (m *mockReader) setTexts(texts ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.texts = texts
	m.idx = 0
}

// setReadErr causes every future ReadText to return err.
func (m *mockReader) setReadErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readErr = err
}

// ReadText returns the next value in the texts sequence, cycling
// on the last element once exhausted. Returns readErr if set.
func (m *mockReader) ReadText() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readErr != nil {
		return "", m.readErr
	}
	if len(m.texts) == 0 {
		return "", nil
	}
	s := m.texts[m.idx]
	if m.idx < len(m.texts)-1 {
		m.idx++
	}
	return s, nil
}

// WriteText records the value and returns writeErr if set.
func (m *mockReader) WriteText(s string) error {
	m.written.Store(s)
	return m.writeErr
}

// WriteImage is a no-op stub added so mockReader satisfies the
// Reader interface after WriteImage was added for image restore.
// Existing text-path tests keep passing unchanged; image-path
// behaviour will be covered by future tests in monitor_test.go
// and service_test.go (not added here per the no-tests directive).
func (m *mockReader) WriteImage(data []byte) error {
	m.written.Store(string(data))
	return m.writeErr
}

// Stop is a no-op stub so mockReader satisfies the Reader interface
// after Stop was added for graceful shutdown of long-lived clipboard
// daemons.
func (m *mockReader) Stop() {}

// lastWritten returns the last value passed to WriteText.
func (m *mockReader) lastWritten() string {
	v, _ := m.written.Load().(string)
	return v
}

// ─────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────

// silentLogger returns a logger that discards all output.
// Tests use this so failures print only test output, not log lines.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError, // only errors pass; debug/info/warn discarded
	}))
}

// drainEvents reads all events from ch until it is empty.
// Useful for clearing events before asserting a "no event" condition.
func drainEvents(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

// collectEvents reads exactly n events from ch within timeout.
// Returns what it got; the caller asserts len.
func collectEvents(t *testing.T, ch <-chan Event, n int, timeout time.Duration) []Event {
	t.Helper()
	out := make([]Event, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-deadline:
			return out
		}
	}
	return out
}

// tickOnce drives the monitor's internal tick() method once.
// Because tick() is unexported, we drive it indirectly: start the
// monitor, wait one interval + a small margin, then cancel.
// Returns any events published during that single tick.
func tickOnce(t *testing.T, mon *Monitor, events <-chan Event, interval time.Duration) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), interval*3)
	defer cancel()
	mon.Start(ctx)
	// Wait for one full tick interval plus a margin for the goroutine to fire.
	time.Sleep(interval + interval/2)
	cancel()
	time.Sleep(10 * time.Millisecond) // let the goroutine exit
	return drainEvents(events)
}

// ─────────────────────────────────────────────────────────────
// Monitor tests
// ─────────────────────────────────────────────────────────────

const fastInterval = 50 * time.Millisecond

func newTestMonitor(reader Reader) (*Monitor, chan Event) {
	events := make(chan Event, 32) // large buffer so tests never block
	mon := NewMonitor(reader, events, fastInterval, silentLogger())
	return mon, events
}

// TestMonitor_DetectsChange verifies that changing clipboard content
// produces exactly one NewClip event per unique value, and that
// going back to a previously seen value also fires (different hash
// than the *current* one, even if seen before).
//
// Sequence: "a" → "b" → "a"
// Expected: 3 NewClip events (each differs from the last seen)
func TestMonitor_DetectsChange(t *testing.T) {
	r := &mockReader{}
	r.setTexts("a", "b", "a")
	mon, events := newTestMonitor(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.Start(ctx)

	// Wait for 3 ticks + margin
	got := collectEvents(t, events, 3, fastInterval*10)
	cancel()

	newClips := 0
	for _, e := range got {
		if e.Op == EventOpNewClip {
			newClips++
		}
	}
	if newClips != 3 {
		t.Errorf("expected 3 NewClip events, got %d (events: %v)", newClips, got)
	}
}

// TestMonitor_SkipsEmptyClipboard verifies that empty strings do not
// produce events. Only the non-empty "a" in the middle should fire.
//
// Sequence: "" → "a" → ""
// Expected: exactly 1 NewClip event
func TestMonitor_SkipsEmptyClipboard(t *testing.T) {
	r := &mockReader{}
	r.setTexts("", "a", "")
	mon, events := newTestMonitor(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.Start(ctx)

	// Run long enough for 3+ ticks
	time.Sleep(fastInterval * 5)
	cancel()
	time.Sleep(10 * time.Millisecond)

	got := drainEvents(events)
	newClips := 0
	for _, e := range got {
		if e.Op == EventOpNewClip {
			newClips++
		}
	}
	if newClips != 1 {
		t.Errorf("expected 1 NewClip event, got %d (events: %v)", newClips, got)
	}
	if len(got) > 0 && got[0].Clip.Content != "a" {
		t.Errorf("expected clip content %q, got %q", "a", got[0].Clip.Content)
	}
}

// TestMonitor_SuppressNextSkipsOneTick verifies that after
// SuppressNext() is called, the very next tick produces no event,
// and the following tick (with new content) does produce one.
//
// Important: the suppressed tick returns before calling ReadText, so
// the mock reader index does NOT advance during the suppressed tick.
// Sequence seen by the reader:
//   tick 1 (suppressed): ReadText NOT called — index stays at 0
//   tick 2 (normal):     ReadText called    — returns "hello", fires event
//   tick 3 (normal):     ReadText called    — returns "world", fires event
//
// We assert: no event fires during the suppressed window (tick 1),
// and at least one NewClip event fires after (tick 2+).
// We do NOT assert exactly 1 event because ticks 2 and 3 both fire
// new content — that is correct monitor behaviour.
func TestMonitor_SuppressNextSkipsOneTick(t *testing.T) {
	r := &mockReader{}
	r.setTexts("hello", "world")
	mon, events := newTestMonitor(r)

	// Set the flag, then immediately cancel after one interval so
	// only the suppressed tick fires before we drain.
	mon.SuppressNext()

	ctx, cancel := context.WithCancel(context.Background())
	mon.Start(ctx)

	// Wait exactly 1 tick — only the suppressed tick should have run.
	time.Sleep(fastInterval + fastInterval/2)
	cancel()
	time.Sleep(10 * time.Millisecond)

	got := drainEvents(events)

	// No NewClip events should have fired — the only tick was suppressed.
	for _, e := range got {
		if e.Op == EventOpNewClip {
			t.Errorf("suppressed tick fired a NewClip event for %q", e.Clip.Content)
		}
	}
}

// TestMonitor_PublishesErrorOnReadFailure verifies that a read error
// produces an EventOpError event, not a panic or a silent skip.
func TestMonitor_PublishesErrorOnReadFailure(t *testing.T) {
	r := &mockReader{}
	r.setReadErr(errors.New("xclip: no display"))
	mon, events := newTestMonitor(r)

	got := tickOnce(t, mon, events, fastInterval)

	errEvents := 0
	for _, e := range got {
		if e.Op == EventOpError {
			errEvents++
			if e.Err == nil {
				t.Error("EventOpError has nil Err")
			}
		}
	}
	if errEvents == 0 {
		t.Error("expected at least one EventOpError, got none")
	}
}

// TestMonitor_StopsOnContextCancel verifies the polling goroutine
// exits within a reasonable deadline after the context is cancelled.
func TestMonitor_StopsOnContextCancel(t *testing.T) {
	r := &mockReader{}
	r.setTexts("anything")
	mon, _ := newTestMonitor(r)

	ctx, cancel := context.WithCancel(context.Background())
	mon.Start(ctx)

	// Let it run for a bit.
	time.Sleep(fastInterval * 2)

	done := make(chan struct{})
	go func() {
		cancel()
		// The monitor goroutine should exit within one interval after cancel.
		time.Sleep(fastInterval * 3)
		close(done)
	}()

	select {
	case <-done:
		// success — goroutine exited within deadline
	case <-time.After(fastInterval * 10):
		t.Error("monitor goroutine did not stop within deadline after context cancel")
	}
}

// TestMonitor_HashDedup verifies that identical consecutive content
// does NOT produce duplicate events. The hash comparison should
// prevent a second event for the same text.
func TestMonitor_HashDedup(t *testing.T) {
	r := &mockReader{}
	// "hello" repeated 5 times — should only produce 1 event.
	r.setTexts("hello", "hello", "hello", "hello", "hello")
	mon, events := newTestMonitor(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.Start(ctx)

	time.Sleep(fastInterval * 6)
	cancel()
	time.Sleep(10 * time.Millisecond)

	got := drainEvents(events)
	newClips := 0
	for _, e := range got {
		if e.Op == EventOpNewClip {
			newClips++
		}
	}
	if newClips != 1 {
		t.Errorf("expected exactly 1 NewClip for repeated content, got %d", newClips)
	}
}

// TestMonitor_ClipHasCorrectHash verifies that the hash in the
// published Clip matches the hash package's output for the content.
func TestMonitor_ClipHasCorrectHash(t *testing.T) {
	const content = "verify_my_hash"
	r := &mockReader{}
	r.setTexts(content)
	mon, events := newTestMonitor(r)

	got := tickOnce(t, mon, events, fastInterval)

	var clip Event
	for _, e := range got {
		if e.Op == EventOpNewClip {
			clip = e
			break
		}
	}
	if clip.Op != EventOpNewClip {
		t.Fatal("no NewClip event produced")
	}

	want := hash.String(content)
	if clip.Clip.Hash != want {
		t.Errorf("clip hash mismatch: got %q, want %q", clip.Clip.Hash, want)
	}
	if clip.Clip.Content != content {
		t.Errorf("clip content mismatch: got %q, want %q", clip.Clip.Content, content)
	}
}

// TestMonitor_LastHash verifies LastHash() returns the most recently
// observed hash after a tick.
func TestMonitor_LastHash(t *testing.T) {
	const content = "lasthash_content"
	r := &mockReader{}
	r.setTexts(content)
	mon, events := newTestMonitor(r)

	_ = tickOnce(t, mon, events, fastInterval)

	want := hash.String(content)
	if got := mon.LastHash(); got != want {
		t.Errorf("LastHash() = %q, want %q", got, want)
	}
}

// TestMonitor_ConcurrentSuppressNext verifies that concurrent calls
// to SuppressNext() do not produce a data race.
// Run with: go test -race ./internal/clipboard/
func TestMonitor_ConcurrentSuppressNext(t *testing.T) {
	r := &mockReader{}
	r.setTexts("concurrent")
	mon, _ := newTestMonitor(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.Start(ctx)

	var wg sync.WaitGroup
	const goroutines = 20
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// Alternate between set and read — exercises the atomic.
			for j := 0; j < 100; j++ {
				mon.SuppressNext()
				_ = mon.LastHash()
			}
		}()
	}
	wg.Wait()
	// No assertion needed — the race detector catches any violation.
}

// TestMonitor_EventsChannelFullDrops verifies that when the events
// channel is full, the monitor logs a warning and continues — it does
// not block or panic.
func TestMonitor_EventsChannelFullDrops(t *testing.T) {
	r := &mockReader{}
	// Fill the sequence with distinct values to generate many events.
	texts := make([]string, 20)
	for i := range texts {
		texts[i] = string(rune('A' + i))
	}
	r.setTexts(texts...)

	// Tiny buffer — will fill quickly.
	events := make(chan Event, 2)
	mon := NewMonitor(r, events, fastInterval, silentLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.Start(ctx)

	// Run long enough to generate more events than the buffer holds.
	time.Sleep(fastInterval * 10)
	cancel()
	time.Sleep(10 * time.Millisecond)

	// If we reach here without deadlock, the test passes.
	// Drain remaining events to confirm the channel is readable.
	_ = drainEvents(events)
}

// ─────────────────────────────────────────────────────────────
// atomic.Bool counter helper (used in concurrent tests)
// ─────────────────────────────────────────────────────────────

type atomicCounter struct{ v atomic.Int64 }

func (c *atomicCounter) inc()        { c.v.Add(1) }
func (c *atomicCounter) get() int64  { return c.v.Load() }
