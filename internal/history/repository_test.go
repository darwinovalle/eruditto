package history_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/darwinovalle/eruditto/internal/database"
	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/history"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func newTestRepo(t *testing.T) *history.Repository {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	db, err := database.Open(path, log)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return history.New(db, log)
}

// textClip builds a valid text clip for tests.
// hash must be unique per test to avoid deduplication collisions.
func textClip(content, hash string) domain.Clip {
	return domain.Clip{
		Type:      domain.ClipTypeText,
		Content:   content,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
	}
}

// textClipAt builds a text clip with an explicit timestamp.
// Used by ordering and pruning tests that need known timestamps.
func textClipAt(content, hash string, at time.Time) domain.Clip {
	c := textClip(content, hash)
	c.CreatedAt = at
	return c
}

// ─────────────────────────────────────────────────────────────────────────────
// Insert
// ─────────────────────────────────────────────────────────────────────────────

func TestInsert_ReturnsPositiveID(t *testing.T) {
	repo := newTestRepo(t)
	id, err := repo.Insert(context.Background(), textClip("hello", "h1"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want > 0", id)
	}
}

func TestInsert_DuplicateHashReturnsExistingID(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	id1, err := repo.Insert(ctx, textClip("hello", "same-hash"))
	if err != nil {
		t.Fatalf("first Insert: %v", err)
	}

	// Second insert with the same hash must return the original id.
	id2, err := repo.Insert(ctx, textClip("hello", "same-hash"))
	if err != nil {
		t.Fatalf("second Insert: %v", err)
	}

	if id1 != id2 {
		t.Errorf("duplicate insert: id1=%d id2=%d, want equal", id1, id2)
	}

	// Only one row should exist.
	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (no duplicate stored)", count)
	}
}

func TestInsert_InvalidClipIsRejected(t *testing.T) {
	repo := newTestRepo(t)

	// Empty hash fails Clip.Validate()
	bad := domain.Clip{
		Type:      domain.ClipTypeText,
		Content:   "hello",
		Hash:      "", // invalid
		CreatedAt: time.Now().UTC(),
	}
	_, err := repo.Insert(context.Background(), bad)
	if err == nil {
		t.Error("Insert(invalid clip) = nil error, want validation error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetByID
// ─────────────────────────────────────────────────────────────────────────────

func TestGetByID_RoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	original := textClip("round trip", "rt1")
	id, err := repo.Insert(ctx, original)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.Content != original.Content {
		t.Errorf("Content = %q, want %q", got.Content, original.Content)
	}
	if got.Hash != original.Hash {
		t.Errorf("Hash = %q, want %q", got.Hash, original.Hash)
	}
	if got.Type != original.Type {
		t.Errorf("Type = %q, want %q", got.Type, original.Type)
	}
}

func TestGetByID_NotFound(t *testing.T) {
	repo := newTestRepo(t)

	_, err := repo.GetByID(context.Background(), 99999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID(unknown): error = %v, want wrapping sql.ErrNoRows", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Recent
// ─────────────────────────────────────────────────────────────────────────────

func TestRecent_NewestFirst(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	clips := []domain.Clip{
		textClipAt("oldest", "r1", now.Add(-2*time.Hour)),
		textClipAt("middle", "r2", now.Add(-1*time.Hour)),
		textClipAt("newest", "r3", now),
	}
	for _, c := range clips {
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert %q: %v", c.Content, err)
		}
	}

	results, err := repo.Recent(ctx, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}
	if results[0].Content != "newest" {
		t.Errorf("results[0] = %q, want %q", results[0].Content, "newest")
	}
	if results[2].Content != "oldest" {
		t.Errorf("results[2] = %q, want %q", results[2].Content, "oldest")
	}
}

func TestRecent_FavoritesFirst(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert a regular clip (newer).
	regular := textClipAt("regular", "fav-r1", now)
	regID, err := repo.Insert(ctx, regular)
	if err != nil {
		t.Fatalf("Insert regular: %v", err)
	}

	// Insert a favourite clip (older, so recency alone would push it down).
	fav := textClipAt("favourite", "fav-r2", now.Add(-time.Hour))
	favID, err := repo.Insert(ctx, fav)
	if err != nil {
		t.Fatalf("Insert favourite: %v", err)
	}

	// Pin the older clip.
	if _, err := repo.ToggleFavorite(ctx, favID); err != nil {
		t.Fatalf("ToggleFavorite: %v", err)
	}

	results, err := repo.Recent(ctx, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("len = %d, want >= 2", len(results))
	}

	// Favourite must be first despite being older.
	if results[0].ID != favID {
		t.Errorf("results[0].ID = %d, want favID=%d", results[0].ID, favID)
	}
	if results[1].ID != regID {
		t.Errorf("results[1].ID = %d, want regID=%d", results[1].ID, regID)
	}
}

func TestRecent_RespectsLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		c := textClip("clip", fmt.Sprintf("lim%d", i))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	results, err := repo.Recent(ctx, 3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Search
// ─────────────────────────────────────────────────────────────────────────────

func TestSearch_FindsExactMatch(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	clips := []string{"docker compose up -d", "kubectl apply", "git commit"}
	for i, content := range clips {
		c := textClip(content, fmt.Sprintf("s%d", i))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert %q: %v", content, err)
		}
	}

	results, err := repo.Search(ctx, "docker", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Content != "docker compose up -d" {
		t.Errorf("content = %q, want %q", results[0].Content, "docker compose up -d")
	}
}

func TestSearch_PrefixMatch(t *testing.T) {
	// Typing "doc" should find "docker compose up -d".
	// This verifies that buildFTSQuery appends * for prefix matching.
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, textClip("docker compose up -d", "pfx1")); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	results, err := repo.Search(ctx, "doc", 10)
	if err != nil {
		t.Fatalf("Search(\"doc\"): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search(\"doc\") len = %d, want 1 (prefix match)", len(results))
	}
}

func TestSearch_CaseInsensitive(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, textClip("Hello World", "ci1")); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for _, q := range []string{"hello", "HELLO", "Hello", "world"} {
		results, err := repo.Search(ctx, q, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(results) != 1 {
			t.Errorf("Search(%q) len = %d, want 1", q, len(results))
		}
	}
}

func TestSearch_EmptyQueryReturnsRecent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		c := textClip("clip", fmt.Sprintf("eq%d", i))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	results, err := repo.Search(ctx, "", 10)
	if err != nil {
		t.Fatalf("Search(\"\"): %v", err)
	}
	if len(results) != 5 {
		t.Errorf("Search(\"\") len = %d, want 5", len(results))
	}
}

func TestSearch_NoResults(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, textClip("hello world", "nr1")); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	results, err := repo.Search(ctx, "zzznomatch", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search(\"zzznomatch\") len = %d, want 0", len(results))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ToggleFavorite
// ─────────────────────────────────────────────────────────────────────────────

func TestToggleFavorite_TogglesCorrectly(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	id, err := repo.Insert(ctx, textClip("pin me", "tf1"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Default is false.
	c, _ := repo.GetByID(ctx, id)
	if c.IsFavorite {
		t.Fatal("expected IsFavorite=false initially")
	}

	// Toggle → true.
	fav, err := repo.ToggleFavorite(ctx, id)
	if err != nil {
		t.Fatalf("ToggleFavorite (on): %v", err)
	}
	if !fav {
		t.Error("ToggleFavorite returned false, want true")
	}

	// Toggle → false.
	fav, err = repo.ToggleFavorite(ctx, id)
	if err != nil {
		t.Fatalf("ToggleFavorite (off): %v", err)
	}
	if fav {
		t.Error("ToggleFavorite returned true, want false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

func TestDelete_RemovesClip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	id, err := repo.Insert(ctx, textClip("delete me", "del1"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if _, err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = repo.GetByID(ctx, id)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID after delete: error = %v, want sql.ErrNoRows", err)
	}
}

func TestDelete_ReturnsImagePath(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	img := domain.Clip{
		Type:      domain.ClipTypeImage,
		ImagePath: "/tmp/eruditto/images/test.png",
		Hash:      "imgdel1",
		CreatedAt: time.Now().UTC(),
	}
	id, err := repo.Insert(ctx, img)
	if err != nil {
		t.Fatalf("Insert image clip: %v", err)
	}

	path, err := repo.Delete(ctx, id)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if path != img.ImagePath {
		t.Errorf("Delete returned path=%q, want %q", path, img.ImagePath)
	}
}

func TestDelete_TextClipReturnsEmptyPath(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	id, err := repo.Insert(ctx, textClip("text only", "del-txt"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	path, err := repo.Delete(ctx, id)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if path != "" {
		t.Errorf("Delete text clip returned path=%q, want empty", path)
	}
}

func TestDelete_IdempotentForMissingID(t *testing.T) {
	repo := newTestRepo(t)

	// Deleting a non-existent ID should not error.
	path, err := repo.Delete(context.Background(), 99999)
	if err != nil {
		t.Errorf("Delete(missing id): %v, want nil", err)
	}
	if path != "" {
		t.Errorf("Delete(missing id) path=%q, want empty", path)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EnforceMaxHistory
// ─────────────────────────────────────────────────────────────────────────────

func TestEnforceMaxHistory_DeletesOldest(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		c := textClipAt("clip", fmt.Sprintf("prune%d", i),
			now.Add(time.Duration(i)*time.Second))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	deleted, err := repo.EnforceMaxHistory(ctx, 5)
	if err != nil {
		t.Fatalf("EnforceMaxHistory: %v", err)
	}
	if deleted != 5 {
		t.Errorf("deleted = %d, want 5", deleted)
	}

	count, _ := repo.Count(ctx)
	if count != 5 {
		t.Errorf("count after prune = %d, want 5", count)
	}
}

func TestEnforceMaxHistory_NeverDeletesFavorites(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert 8 regular + 2 favourites = 10 total.
	for i := 0; i < 8; i++ {
		c := textClipAt("regular", fmt.Sprintf("ndel%d", i),
			now.Add(time.Duration(i)*time.Second))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert regular: %v", err)
		}
	}

	for i := 0; i < 2; i++ {
		c := textClipAt("favourite", fmt.Sprintf("fndel%d", i),
			now.Add(time.Duration(8+i)*time.Second))
		id, err := repo.Insert(ctx, c)
		if err != nil {
			t.Fatalf("Insert favourite: %v", err)
		}
		if _, err := repo.ToggleFavorite(ctx, id); err != nil {
			t.Fatalf("ToggleFavorite: %v", err)
		}
	}

	// Enforce max 2. Should delete all 8 regular clips.
	// The 2 favourites are protected.
	deleted, err := repo.EnforceMaxHistory(ctx, 2)
	if err != nil {
		t.Fatalf("EnforceMaxHistory: %v", err)
	}
	if deleted != 8 {
		t.Errorf("deleted = %d, want 8", deleted)
	}

	remaining, _ := repo.Recent(ctx, 100)
	for _, c := range remaining {
		if !c.IsFavorite {
			t.Errorf("non-favourite survived prune: id=%d content=%q",
				c.ID, c.Content)
		}
	}
}

func TestEnforceMaxHistory_NoopWhenUnderLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		c := textClip("clip", fmt.Sprintf("noop%d", i))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	deleted, err := repo.EnforceMaxHistory(ctx, 100)
	if err != nil {
		t.Fatalf("EnforceMaxHistory: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (under limit)", deleted)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Count
// ─────────────────────────────────────────────────────────────────────────────

func TestCount_Empty(t *testing.T) {
	repo := newTestRepo(t)
	n, err := repo.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Errorf("Count = %d, want 0", n)
	}
}

func TestCount_AfterInserts(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		c := textClip("clip", fmt.Sprintf("cnt%d", i))
		if _, err := repo.Insert(ctx, c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	n, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 5 {
		t.Errorf("Count = %d, want 5", n)
	}
}
