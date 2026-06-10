package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/darwinovalle/eruditto/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// Pure helper tests (no Fyne dependency — fast, always runnable)
// ─────────────────────────────────────────────────────────────────────────────

func TestRelativeTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "just now"},
		{now.Add(-59 * time.Second), "just now"},
		{now.Add(-1 * time.Minute), "1 minute ago"},
		{now.Add(-5 * time.Minute), "5 minutes ago"},
		{now.Add(-59 * time.Minute), "59 minutes ago"},
		{now.Add(-1 * time.Hour), "1 hour ago"},
		{now.Add(-3 * time.Hour), "3 hours ago"},
		{now.Add(-23 * time.Hour), "23 hours ago"},
		{now.Add(-24 * time.Hour), "yesterday"},
		{now.Add(-48 * time.Hour), "2 days ago"},
		{now.Add(-6 * 24 * time.Hour), "6 days ago"},
	}
	for _, tc := range cases {
		got := relativeTime(tc.t)
		if got != tc.want {
			t.Errorf("relativeTime(%v ago) = %q, want %q",
				now.Sub(tc.t).Round(time.Second), got, tc.want)
		}
	}
}

func TestRelativeTime_OldDate(t *testing.T) {
	old := time.Date(2024, time.March, 15, 10, 0, 0, 0, time.UTC)
	got := relativeTime(old)
	want := "Mar 15"
	if got != want {
		t.Errorf("relativeTime(old) = %q, want %q", got, want)
	}
}

func TestFormatInt(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{1247, "1,247"},
		{10000, "10,000"},
		{50000, "50,000"},
		{1000000, "1,000,000"},
	}
	for _, tc := range cases {
		got := formatInt(tc.n)
		if got != tc.want {
			t.Errorf("formatInt(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestRemoveByID(t *testing.T) {
	clips := []domain.Clip{
		{ID: 1, Content: "a"},
		{ID: 2, Content: "b"},
		{ID: 3, Content: "c"},
	}
	got := removeByID(clips, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 clips after removal, got %d", len(got))
	}
	for _, c := range got {
		if c.ID == 2 {
			t.Error("clip with ID 2 still present after removeByID")
		}
	}
}

func TestRemoveByID_NotFound(t *testing.T) {
	clips := []domain.Clip{{ID: 1}, {ID: 2}}
	got := removeByID(clips, 99)
	if len(got) != 2 {
		t.Errorf("expected 2 clips, got %d", len(got))
	}
}

func TestRemoveByID_Empty(t *testing.T) {
	got := removeByID(nil, 1)
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// buildCountString — pure logic, no widgets
// ─────────────────────────────────────────────────────────────────────────────

func buildCountString(clips []domain.Clip) string {
	total := len(clips)
	favs := 0
	for _, c := range clips {
		if c.IsFavorite {
			favs++
		}
	}
	switch {
	case total == 0:
		return "No clips"
	case total == 1 && favs == 0:
		return "1 clip"
	case favs == 0:
		return fmt.Sprintf("%s clips", formatInt(total))
	case favs == 1:
		return fmt.Sprintf("%s clips · 1 pinned", formatInt(total))
	default:
		return fmt.Sprintf("%s clips · %s pinned", formatInt(total), formatInt(favs))
	}
}

func TestCountString(t *testing.T) {
	cases := []struct {
		clips []domain.Clip
		want  string
	}{
		{nil, "No clips"},
		{[]domain.Clip{{ID: 1}}, "1 clip"},
		{[]domain.Clip{{ID: 1}, {ID: 2}}, "2 clips"},
		{[]domain.Clip{{ID: 1, IsFavorite: true}, {ID: 2}}, "2 clips · 1 pinned"},
		{[]domain.Clip{{ID: 1, IsFavorite: true}, {ID: 2, IsFavorite: true}}, "2 clips · 2 pinned"},
	}
	for _, tc := range cases {
		got := buildCountString(tc.clips)
		if got != tc.want {
			t.Errorf("buildCountString(%d clips) = %q, want %q",
				len(tc.clips), got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Filter logic (in-memory, no DB)
// ─────────────────────────────────────────────────────────────────────────────

func makeTestClips() []domain.Clip {
	now := time.Now()
	return []domain.Clip{
		{ID: 1, Type: domain.ClipTypeText, Content: "docker compose up -d", CreatedAt: now},
		{ID: 2, Type: domain.ClipTypeText, Content: "hello world", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: 3, Type: domain.ClipTypeText, Content: "SELECT * FROM clips", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 4, Type: domain.ClipTypeText, Content: "git commit -m 'fix'", CreatedAt: now.Add(-3 * time.Minute)},
		{ID: 5, Type: domain.ClipTypeImage, ImagePath: "/tmp/img.png", CreatedAt: now.Add(-4 * time.Minute)},
	}
}

func filterClips(all []domain.Clip, query string) []domain.Clip {
	if query == "" {
		return all
	}
	q := lowercaseASCII(query)
	var out []domain.Clip
	for _, c := range all {
		if c.Type == domain.ClipTypeText && containsInsensitive(c.Content, q) {
			out = append(out, c)
		}
	}
	return out
}

func TestFilter_EmptyQueryShowsAll(t *testing.T) {
	all := makeTestClips()
	got := filterClips(all, "")
	if len(got) != len(all) {
		t.Errorf("empty query: got %d, want %d", len(got), len(all))
	}
}

func TestFilter_InMemorySubset(t *testing.T) {
	all := makeTestClips()
	got := filterClips(all, "docker")
	if len(got) != 1 {
		t.Errorf("filter 'docker': got %d, want 1", len(got))
	}
	if len(got) > 0 && got[0].ID != 1 {
		t.Errorf("filter 'docker': got clip ID %d, want 1", got[0].ID)
	}
}

func TestFilter_NoMatch(t *testing.T) {
	all := makeTestClips()
	got := filterClips(all, "xyzzy_no_match")
	if len(got) != 0 {
		t.Errorf("no-match query: got %d, want 0", len(got))
	}
}

func TestFilter_CaseInsensitive(t *testing.T) {
	all := makeTestClips()
	got := filterClips(all, "DOCKER")
	if len(got) != 1 {
		t.Errorf("case-insensitive 'DOCKER': got %d, want 1", len(got))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// friendlyHotkeyError tests
// ─────────────────────────────────────────────────────────────────────────────

func TestFriendlyHotkeyError_StripsPrefix(t *testing.T) {
	raw := `settings: invalid value: "ctrl+": hotkey contains empty segment (check for trailing/double '+')`
	got := friendlyHotkeyError(raw)
	if got == raw {
		t.Error("should strip prefix, returned raw string unchanged")
	}
	if len(got) > 80 {
		t.Errorf("result too long: %d chars", len(got))
	}
}

func TestFriendlyHotkeyError_Empty(t *testing.T) {
	if got := friendlyHotkeyError(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// test-only helpers
// ─────────────────────────────────────────────────────────────────────────────

func containsInsensitive(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	sl := lowercaseASCII(s)
	subl := lowercaseASCII(sub)
	return stringContainsLower(sl, subl)
}

func lowercaseASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func stringContainsLower(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
