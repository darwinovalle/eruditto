package images_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/darwinovalle/eruditto/internal/images"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newTestStorage creates a Storage backed by a temp directory.
// The temp directory is cleaned up automatically by t.Cleanup.
func newTestStorage(t *testing.T) *images.Storage {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	s, err := images.NewWithDir(t.TempDir(), log)
	if err != nil {
		t.Fatalf("NewWithDir: %v", err)
	}
	return s
}

// makePNG creates a valid in-memory PNG image of the given dimensions
// filled with a solid colour. Used to produce test image bytes without
// depending on image files on disk.
func makePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with a non-black colour so the image is non-trivial.
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 149, B: 237, A: 255}) // cornflower blue
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("makePNG encode: %v", err)
	}
	return buf.Bytes()
}

// imageSize decodes a PNG file from disk and returns its dimensions.
func imageSize(t *testing.T, path string) (width, height int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode %q: %v", path, err)
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// ─────────────────────────────────────────────────────────────────────────────
// Construction
// ─────────────────────────────────────────────────────────────────────────────

func TestNewWithDir_CreatesDirectory(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	s, err := images.NewWithDir(nested, log)
	if err != nil {
		t.Fatalf("NewWithDir: %v", err)
	}

	info, err := os.Stat(s.ImagesDir())
	if err != nil {
		t.Fatalf("stat images dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("ImagesDir is not a directory")
	}
}

func TestNewWithDir_DirectoryPermissions(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "images")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	_, err := images.NewWithDir(dir, log)
	if err != nil {
		t.Fatalf("NewWithDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o700)
	if got != want {
		t.Errorf("dir permissions = %04o, want %04o", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Save
// ─────────────────────────────────────────────────────────────────────────────

func TestSave_ReturnsNonEmptyPath(t *testing.T) {
	s := newTestStorage(t)
	pngBytes := makePNG(t, 200, 150)

	path, err := s.Save(1, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if path == "" {
		t.Error("Save returned empty path")
	}
}

func TestSave_FileExistsOnDisk(t *testing.T) {
	s := newTestStorage(t)
	pngBytes := makePNG(t, 200, 150)

	path, err := s.Save(42, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("full image not on disk at %q: %v", path, err)
	}
}

func TestSave_ThumbnailCreated(t *testing.T) {
	s := newTestStorage(t)
	pngBytes := makePNG(t, 400, 300)

	path, err := s.Save(10, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	thumbPath := images.ThumbPath(path)
	if _, err := os.Stat(thumbPath); err != nil {
		t.Errorf("thumbnail not on disk at %q: %v", thumbPath, err)
	}
}

func TestSave_ThumbnailDimensionsWithinBounds(t *testing.T) {
	s := newTestStorage(t)
	// Large landscape image: 1920×1080
	pngBytes := makePNG(t, 1920, 1080)

	path, err := s.Save(7, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	thumbPath := images.ThumbPath(path)
	w, h := imageSize(t, thumbPath)

	if w > images.ThumbnailSize {
		t.Errorf("thumbnail width = %d, want <= %d", w, images.ThumbnailSize)
	}
	if h > images.ThumbnailSize {
		t.Errorf("thumbnail height = %d, want <= %d", h, images.ThumbnailSize)
	}
	// Verify aspect ratio is preserved (1920:1080 = 16:9)
	// Thumbnail should be 128×72
	if w != images.ThumbnailSize {
		t.Errorf("thumbnail width = %d, want %d (landscape, width is limiting)", w, images.ThumbnailSize)
	}
}

func TestSave_ThumbnailPortraitDimensions(t *testing.T) {
	s := newTestStorage(t)
	// Portrait image: 1080×1920
	pngBytes := makePNG(t, 1080, 1920)

	path, err := s.Save(8, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	thumbPath := images.ThumbPath(path)
	w, h := imageSize(t, thumbPath)

	if w > images.ThumbnailSize {
		t.Errorf("thumbnail width = %d, want <= %d", w, images.ThumbnailSize)
	}
	if h > images.ThumbnailSize {
		t.Errorf("thumbnail height = %d, want <= %d", h, images.ThumbnailSize)
	}
	if h != images.ThumbnailSize {
		t.Errorf("thumbnail height = %d, want %d (portrait, height is limiting)", h, images.ThumbnailSize)
	}
}

func TestSave_SmallImageNotUpscaled(t *testing.T) {
	s := newTestStorage(t)
	// Image smaller than ThumbnailSize on both dimensions.
	pngBytes := makePNG(t, 32, 24)

	path, err := s.Save(9, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	thumbPath := images.ThumbPath(path)
	w, h := imageSize(t, thumbPath)

	// No upscaling: dimensions must equal original.
	if w != 32 || h != 24 {
		t.Errorf("small image thumbnail = %dx%d, want 32x24 (no upscaling)", w, h)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	s := newTestStorage(t)
	pngBytes := makePNG(t, 100, 100)

	path, err := s.Save(99, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o600)
	if got != want {
		t.Errorf("file permissions = %04o, want %04o", got, want)
	}
}

func TestSave_ErrImageTooLarge(t *testing.T) {
	s := newTestStorage(t)

	// Construct a byte slice larger than MaxImageBytes without
	// allocating a real 10 MB PNG (which would be slow).
	// We pass raw bytes that exceed the limit — Save checks size
	// before decoding, so the content does not need to be valid PNG.
	oversized := make([]byte, images.MaxImageBytes+1)

	_, err := s.Save(1, oversized)
	if !errors.Is(err, images.ErrImageTooLarge) {
		t.Errorf("Save(oversized) = %v, want ErrImageTooLarge", err)
	}
}

func TestSave_ErrInvalidImage(t *testing.T) {
	s := newTestStorage(t)

	corrupt := []byte("this is not an image")

	_, err := s.Save(1, corrupt)
	if !errors.Is(err, images.ErrInvalidImage) {
		t.Errorf("Save(corrupt) = %v, want ErrInvalidImage", err)
	}
}

func TestSave_NoFileLeftOnError(t *testing.T) {
	s := newTestStorage(t)

	corrupt := []byte("not a png")
	_, _ = s.Save(55, corrupt)

	// No files should exist for clip 55.
	entries, err := os.ReadDir(s.ImagesDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		t.Errorf("unexpected file after failed Save: %s", e.Name())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Load
// ─────────────────────────────────────────────────────────────────────────────

func TestLoad_RoundTrip(t *testing.T) {
	s := newTestStorage(t)
	original := makePNG(t, 200, 150)

	path, err := s.Save(20, original)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The loaded bytes should decode to an image with the same dimensions.
	img, _, err := image.Decode(bytes.NewReader(loaded))
	if err != nil {
		t.Fatalf("decode loaded bytes: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 200 || b.Dy() != 150 {
		t.Errorf("loaded image size = %dx%d, want 200x150", b.Dx(), b.Dy())
	}
}

func TestLoad_MissingFileError(t *testing.T) {
	s := newTestStorage(t)

	_, err := s.Load(filepath.Join(s.ImagesDir(), "nonexistent.png"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(missing) = %v, want wrapping os.ErrNotExist", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

func TestDelete_RemovesBothFiles(t *testing.T) {
	s := newTestStorage(t)
	pngBytes := makePNG(t, 200, 150)

	path, err := s.Save(30, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	thumbPath := images.ThumbPath(path)

	// Verify both exist before delete.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("full image missing before delete: %v", err)
	}
	if _, err := os.Stat(thumbPath); err != nil {
		t.Fatalf("thumbnail missing before delete: %v", err)
	}

	if err := s.Delete(path); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify both are gone.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("full image still on disk after Delete: %v", err)
	}
	if _, err := os.Stat(thumbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("thumbnail still on disk after Delete: %v", err)
	}
}

func TestDelete_IdempotentForMissingFiles(t *testing.T) {
	s := newTestStorage(t)

	// Delete a path that was never saved — should not error.
	err := s.Delete(filepath.Join(s.ImagesDir(), "999.png"))
	if err != nil {
		t.Errorf("Delete(missing) = %v, want nil (idempotent)", err)
	}
}

func TestDelete_SaveLoadDeleteCycle(t *testing.T) {
	s := newTestStorage(t)
	pngBytes := makePNG(t, 100, 100)

	// Save.
	path, err := s.Save(50, pngBytes)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load.
	_, err = s.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Delete.
	if err := s.Delete(path); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Load after delete must fail.
	_, err = s.Load(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load after Delete = %v, want os.ErrNotExist", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GenerateThumbnail
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerateThumbnail_DimensionsWithinBounds(t *testing.T) {
	s := newTestStorage(t)

	tests := []struct {
		name         string
		srcW, srcH   int
		maxPx        int
		wantW, wantH int
	}{
		{
			name: "landscape 1920x1080 → 128x72",
			srcW: 1920, srcH: 1080,
			maxPx: 128,
			wantW: 128, wantH: 72,
		},
		{
			name: "portrait 1080x1920 → 72x128",
			srcW: 1080, srcH: 1920,
			maxPx: 128,
			wantW: 72, wantH: 128,
		},
		{
			name: "square 512x512 → 128x128",
			srcW: 512, srcH: 512,
			maxPx: 128,
			wantW: 128, wantH: 128,
		},
		{
			name: "small image not upscaled",
			srcW: 64, srcH: 48,
			maxPx: 128,
			wantW: 64, wantH: 48,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pngBytes := makePNG(t, tt.srcW, tt.srcH)

			thumbBytes, err := s.GenerateThumbnail(pngBytes, tt.maxPx)
			if err != nil {
				t.Fatalf("GenerateThumbnail: %v", err)
			}

			img, _, err := image.Decode(bytes.NewReader(thumbBytes))
			if err != nil {
				t.Fatalf("decode thumbnail: %v", err)
			}

			b := img.Bounds()
			gotW, gotH := b.Dx(), b.Dy()

			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("thumbnail size = %dx%d, want %dx%d",
					gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestGenerateThumbnail_InvalidInput(t *testing.T) {
	s := newTestStorage(t)

	_, err := s.GenerateThumbnail([]byte("not an image"), 128)
	if !errors.Is(err, images.ErrInvalidImage) {
		t.Errorf("GenerateThumbnail(invalid) = %v, want ErrInvalidImage", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ThumbPath
// ─────────────────────────────────────────────────────────────────────────────

func TestThumbPath_Derivation(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/.local/share/eruditto/images/42.png", "/home/user/.local/share/eruditto/images/42_thumb.png"},
		{"/tmp/images/1.png", "/tmp/images/1_thumb.png"},
		{"relative/path/99.png", "relative/path/99_thumb.png"},
	}

	for _, tt := range tests {
		got := images.ThumbPath(tt.input)
		if got != tt.want {
			t.Errorf("ThumbPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
