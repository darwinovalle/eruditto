package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeClock returns a fixed time for tests that need a stable
// CreatedAt value. We don't reach for time.Time itself because
// time.Now() makes tests flaky; a fixed reference is what the
// repository will actually see at Insert time.
var fakeNow = time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

func TestClipType_IsKnown(t *testing.T) {
	tests := []struct {
		name string
		typ  ClipType
		want bool
	}{
		{"text is known", ClipTypeText, true},
		{"image is known", ClipTypeImage, true},
		{"empty is unknown", ClipType(""), false},
		{"bogus is unknown", ClipType("audio"), false},
		{"uppercase text is unknown (case-sensitive)", ClipType("TEXT"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.typ.IsKnown(); got != tt.want {
				t.Errorf("IsKnown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClipType_String(t *testing.T) {
	if got := ClipTypeText.String(); got != "text" {
		t.Errorf("ClipTypeText.String() = %q, want %q", got, "text")
	}
	if got := ClipTypeImage.String(); got != "image" {
		t.Errorf("ClipTypeImage.String() = %q, want %q", got, "image")
	}
}

func TestNewTextClip(t *testing.T) {
	c := NewTextClip("hello world", "abc123")
	if c.Type != ClipTypeText {
		t.Errorf("Type = %q, want %q", c.Type, ClipTypeText)
	}
	if c.Content != "hello world" {
		t.Errorf("Content = %q, want %q", c.Content, "hello world")
	}
	if c.Hash != "abc123" {
		t.Errorf("Hash = %q, want %q", c.Hash, "abc123")
	}
	if c.IsFavorite {
		t.Error("IsFavorite should default to false")
	}
	if c.ID != 0 {
		t.Errorf("ID = %d, want 0 (unassigned)", c.ID)
	}
	if c.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set, got zero")
	}
	// ImagePath is not relevant for text clips but should be empty
	// so a text clip never accidentally carries an image path.
	if c.ImagePath != "" {
		t.Errorf("ImagePath = %q, want empty for text clip", c.ImagePath)
	}
}

func TestNewImageClip(t *testing.T) {
	c := NewImageClip("/tmp/clip.png", "deadbeef")
	if c.Type != ClipTypeImage {
		t.Errorf("Type = %q, want %q", c.Type, ClipTypeImage)
	}
	if c.ImagePath != "/tmp/clip.png" {
		t.Errorf("ImagePath = %q, want %q", c.ImagePath, "/tmp/clip.png")
	}
	if c.Hash != "deadbeef" {
		t.Errorf("Hash = %q, want %q", c.Hash, "deadbeef")
	}
	if c.Content != "" {
		t.Errorf("Content = %q, want empty for image clip", c.Content)
	}
	if c.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set, got zero")
	}
}

func TestClip_Validate(t *testing.T) {
	tests := []struct {
		name    string
		clip    Clip
		wantErr error
	}{
		{
			name: "valid text clip",
			clip: Clip{
				Type:      ClipTypeText,
				Content:   "hello",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: nil,
		},
		{
			name: "valid image clip",
			clip: Clip{
				Type:      ClipTypeImage,
				ImagePath: "/tmp/x.png",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: nil,
		},
		{
			name:    "nil clip",
			clip:    Clip{},
			wantErr: ErrInvalidType,
		},
		{
			name: "unknown type",
			clip: Clip{
				Type:      ClipType("audio"),
				Content:   "x",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: ErrInvalidType,
		},
		{
			name: "empty hash",
			clip: Clip{
				Type:      ClipTypeText,
				Content:   "hello",
				Hash:      "",
				CreatedAt: fakeNow,
			},
			wantErr: ErrEmptyHash,
		},
		{
			name: "zero created_at",
			clip: Clip{
				Type:    ClipTypeText,
				Content: "hello",
				Hash:    "abc",
			},
			wantErr: ErrZeroCreatedAt,
		},
		{
			name: "text clip with empty content",
			clip: Clip{
				Type:      ClipTypeText,
				Content:   "",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: ErrEmptyContent,
		},
		{
			name: "image clip with empty path (tentative insert)",
			// The service inserts image rows with an empty
			// image_path so the DB can assign an id, then runs
			// UpdateImagePath once the bytes are saved to disk.
			// Validate() must permit this intermediate state.
			clip: Clip{
				Type:      ClipTypeImage,
				ImagePath: "",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: nil,
		},
		{
			name: "image clip with non-empty path (persisted record)",
			clip: Clip{
				Type:      ClipTypeImage,
				ImagePath: "/tmp/clip.png",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: nil,
		},
		{
			name: "image clip with Content set is invalid",
			// Image clips carry data in the file, never in
			// Content. A non-empty Content on an image clip
			// indicates a programming error somewhere upstream.
			clip: Clip{
				Type:      ClipTypeImage,
				ImagePath: "/tmp/clip.png",
				Content:   "stray text",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: ErrInvalidType,
		},
		{
			name: "clip with neither content nor image_path",
			clip: Clip{
				Type:      ClipTypeText,
				Content:   "",
				Hash:      "abc",
				CreatedAt: fakeNow,
			},
			wantErr: ErrEmptyContent, // type-specific check fires first
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.clip
			err := c.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("Validate() = nil, want error wrapping %v", tt.wantErr)
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want error wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestClip_Validate_NilReceiver(t *testing.T) {
	// A nil *Clip should not panic. We deliberately exercise
	// the nil-receiver path because callers may pass nil when
	// deserializing from a corrupt source.
	var c *Clip
	err := c.Validate()
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("nil Clip.Validate() = %v, want ErrInvalidType", err)
	}
}

func TestClip_DisplayContent_Empty(t *testing.T) {
	c := NewTextClip("", "h")
	if got := c.DisplayContent(80); got != "" {
		t.Errorf("empty content DisplayContent(80) = %q, want %q", got, "")
	}
}

func TestClip_DisplayContent_Short(t *testing.T) {
	c := NewTextClip("hi", "h")
	if got := c.DisplayContent(80); got != "hi" {
		t.Errorf("short content DisplayContent(80) = %q, want %q", got, "hi")
	}
}

func TestClip_DisplayContent_ExactLength(t *testing.T) {
	c := NewTextClip("hello", "h")
	// 5 runes fits exactly in 5; no ellipsis.
	if got := c.DisplayContent(5); got != "hello" {
		t.Errorf("DisplayContent(5) = %q, want %q", got, "hello")
	}
}

func TestClip_DisplayContent_TruncatesASCII(t *testing.T) {
	c := NewTextClip("hello world", "h")
	got := c.DisplayContent(6)
	want := "hello…"
	if got != want {
		t.Errorf("DisplayContent(6) = %q, want %q", got, want)
	}
	if n := len([]rune(got)); n != 6 {
		t.Errorf("DisplayContent(6) rune length = %d, want 6", n)
	}
}

func TestClip_DisplayContent_TruncatesUnicode(t *testing.T) {
	// "café" is 4 runes but 5 bytes in UTF-8. If we cut at byte
	// 4 we'd split the 'é' (0xC3 0xA9) and produce invalid UTF-8.
	// Truncating on runes avoids that.
	c := NewTextClip("café au lait", "h")
	got := c.DisplayContent(4)
	want := "caf…"
	if got != want {
		t.Errorf("DisplayContent(4) = %q, want %q", got, want)
	}
	// Result must be valid UTF-8 (the == comparison above would
	// already fail on invalid bytes, but we assert explicitly for
	// clarity).
	if !isValidUTF8(got) {
		t.Errorf("DisplayContent(4) = %q is not valid UTF-8", got)
	}
}

func TestClip_DisplayContent_TruncatesEmoji(t *testing.T) {
	// Each emoji can be one or more runes; we don't test the
	// exact width but we do test that we don't split a multi-rune
	// sequence. "👨‍👩‍👧" is 5 runes (ZWJ family sequence).
	c := NewTextClip("hello 👨‍👩‍👧 world", "h")
	got := c.DisplayContent(8)
	if !isValidUTF8(got) {
		t.Errorf("DisplayContent(8) = %q is not valid UTF-8", got)
	}
	if n := len([]rune(got)); n != 8 {
		t.Errorf("DisplayContent(8) rune length = %d, want 8", n)
	}
}

func TestClip_DisplayContent_ImageClipReturnsEmpty(t *testing.T) {
	c := NewImageClip("/tmp/x.png", "h")
	if got := c.DisplayContent(80); got != "" {
		t.Errorf("image clip DisplayContent(80) = %q, want %q", got, "")
	}
}

func TestClip_DisplayContent_DefensiveMaxRunes(t *testing.T) {
	c := NewTextClip("hello world", "h")
	if got := c.DisplayContent(0); got != "" {
		t.Errorf("DisplayContent(0) = %q, want %q", got, "")
	}
	if got := c.DisplayContent(-5); got != "" {
		t.Errorf("DisplayContent(-5) = %q, want %q", got, "")
	}
}

func TestClip_DisplayContent_MaxRunesOne(t *testing.T) {
	// Pathological case: maxRunes=1 should produce just the
	// ellipsis (or empty if the input is empty). Make sure we
	// don't produce a negative-slice panic.
	c := NewTextClip("hello", "h")
	got := c.DisplayContent(1)
	want := "…"
	if got != want {
		t.Errorf("DisplayContent(1) = %q, want %q", got, want)
	}
}

// isValidUTF8 is a tiny helper to make UTF-8 validity explicit
// in test failure messages. It also catches the case where a
// byte-level slice would have produced invalid output.
func isValidUTF8(s string) bool {
	// strings.ContainsRune with the invalid rune sentinel is
	// the idiomatic way to test for invalid UTF-8 in Go.
	return !strings.ContainsRune(s, 0xFFFD) || s == "\uFFFD"
}
