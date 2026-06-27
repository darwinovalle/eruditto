package clipboard

import (
	"testing"
)

// TestNewAtottoReader_ReturnsNonNil verifies the constructor always
// returns a non-nil Reader. A nil Reader would panic in the Monitor
// before any useful error is surfaced.
func TestNewAtottoReader_ReturnsNonNil(t *testing.T) {
	r := NewAtottoReader()
	if r == nil {
		t.Fatal("NewAtottoReader returned nil")
	}
}

// TestAtottoReader_RoundTrip exercises the real OS clipboard.
//
// Requires a running display server (DISPLAY or WAYLAND_DISPLAY).
// Will be skipped automatically in headless CI when the write fails.
//
// Run locally with:
//
//	go test ./internal/clipboard/ -run TestAtottoReader_RoundTrip -v
func TestAtottoReader_RoundTrip(t *testing.T) {
	r := NewAtottoReader()

	const want = "eruditto_roundtrip_test"

	if err := r.WriteText(want); err != nil {
		t.Skipf("clipboard write failed (no display?): %v", err)
	}

	got, err := r.ReadText()
	if err != nil {
		t.Fatalf("ReadText after WriteText: %v", err)
	}

	if got != want {
		t.Errorf("round-trip mismatch: wrote %q, got %q", want, got)
	}
}
