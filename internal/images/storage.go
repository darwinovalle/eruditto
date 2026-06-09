// Package images handles saving, loading, and deleting clipboard image
// files on the local filesystem.
//
// Responsibilities of this package:
//   - Writing full-size images to disk as PNG files
//   - Generating and writing thumbnail PNG files at save time
//   - Loading image bytes from disk
//   - Deleting image files (both full and thumbnail) on demand
//   - Enforcing a maximum image size limit before touching disk
//
// What this package does NOT do:
//   - It never touches the database
//   - It never knows about domain.Clip or any other domain type
//   - It never calls the history repository
//
// Directory layout:
//
//	~/.local/share/eruditto/images/
//	    42.png          ← full image for clip id=42
//	    42_thumb.png    ← 128×128 thumbnail for clip id=42
//
// Thumbnail path is always derivable from the full image path:
//
//	/path/to/42.png  →  /path/to/42_thumb.png
package images

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	// Register PNG and JPEG decoders so image.Decode handles both formats.
	// The image package uses a registration pattern: importing these
	// sub-packages as side effects adds their decoders to the global registry.
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"

	"github.com/darwinovalle/eruditto/pkg/xdg"
)

// MaxImageBytes is the maximum accepted image size in bytes (10 MB).
//
// A 10 MB PNG decoded into RGBA memory becomes 40–100 MB of raw pixels.
// We are a clipboard manager, not an image editor. Images above this
// limit are logged at WARN level and skipped — no crash, no dialog.
const MaxImageBytes = 10 * 1024 * 1024

// ThumbnailSize is the maximum dimension (width or height) of a
// generated thumbnail in pixels (128 px).
//
// Aspect ratio is always preserved. A 1920×1080 source produces a
// 128×72 thumbnail.
const ThumbnailSize = 128

// ErrImageTooLarge is returned by Save when the image exceeds MaxImageBytes.
var ErrImageTooLarge = errors.New("images: image exceeds maximum allowed size (10 MB)")

// ErrInvalidImage is returned when bytes cannot be decoded as a
// supported image format.
var ErrInvalidImage = errors.New("images: could not decode image bytes")

// Storage manages image and thumbnail files on the filesystem.
//
// All public methods are safe to call concurrently — there is no
// shared mutable state after construction.
type Storage struct {
	imagesDir string
	log       *slog.Logger
}

// New creates a Storage rooted at the XDG data images directory.
//
//	~/.local/share/eruditto/images/
//
// Creates the directory (mode 0700) if it does not exist.
func New(log *slog.Logger) (*Storage, error) {
	dataDir, err := xdg.DataDir()
	if err != nil {
		return nil, fmt.Errorf("images: resolve data dir: %w", err)
	}
	return newStorage(filepath.Join(dataDir, "images"), log)
}

// NewWithDir creates a Storage rooted at a specific directory.
// Intended for tests — production code uses New().
func NewWithDir(dir string, log *slog.Logger) (*Storage, error) {
	return newStorage(dir, log)
}

// newStorage is the shared constructor used by New and NewWithDir.
func newStorage(dir string, log *slog.Logger) (*Storage, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("images: create dir %q: %w", dir, err)
	}
	log.Info("images storage ready", "dir", dir)
	return &Storage{imagesDir: dir, log: log}, nil
}

// Save writes imageBytes to disk as a full-size PNG, generates a
// thumbnail at ThumbPath(fullPath), and returns the full image path.
//
// Errors:
//   - ErrImageTooLarge: len(imageBytes) > MaxImageBytes
//   - ErrInvalidImage:  bytes are not a decodable image format
//   - wrapped os error: disk write failed
//
// On any error no files are left on disk (best-effort cleanup via
// the atomic write pattern in writePNG).
func (s *Storage) Save(clipID int64, imageBytes []byte) (string, error) {
	// ── 1. Enforce size limit ─────────────────────────────────────────
	if len(imageBytes) > MaxImageBytes {
		s.log.Warn("image too large, skipping",
			"clip_id", clipID,
			"size_bytes", len(imageBytes),
			"limit_bytes", MaxImageBytes,
		)
		return "", ErrImageTooLarge
	}

	// ── 2. Decode ─────────────────────────────────────────────────────
	// Decoding here serves two purposes:
	//   a) Validates the bytes before writing anything to disk
	//   b) Provides the image.Image needed for thumbnail generation
	img, err := decodeImage(imageBytes)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidImage, err)
	}

	// ── 3. Write full image ───────────────────────────────────────────
	fullPath := s.fullPath(clipID)
	if err := writePNG(fullPath, img); err != nil {
		return "", fmt.Errorf("images: write full image clip=%d: %w", clipID, err)
	}
	s.log.Debug("full image saved", "clip_id", clipID, "path", fullPath)

	// ── 4. Write thumbnail ────────────────────────────────────────────
	// Thumbnail failure is non-fatal: the full image is already saved.
	// The UI falls back to a placeholder icon when the thumbnail is missing.
	thumbPath := ThumbPath(fullPath)
	thumb := resizeFit(img, ThumbnailSize)
	if err := writePNG(thumbPath, thumb); err != nil {
		s.log.Warn("thumbnail write failed",
			"clip_id", clipID,
			"error", err,
		)
	} else {
		s.log.Debug("thumbnail saved", "clip_id", clipID, "path", thumbPath)
	}

	return fullPath, nil
}

// Load reads and returns the raw file bytes at path.
//
// Returns a wrapped os.ErrNotExist if the file does not exist.
func (s *Storage) Load(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("images: load %q: %w", path, err)
	}
	return data, nil
}

// Delete removes the full image and its thumbnail from disk.
//
// Idempotent: missing files are not errors. The caller (clipboard
// service) calls Delete after the database row is removed.
func (s *Storage) Delete(imagePath string) error {
	if err := removeIfExists(imagePath); err != nil {
		return fmt.Errorf("images: delete full %q: %w", imagePath, err)
	}

	thumbPath := ThumbPath(imagePath)
	if err := removeIfExists(thumbPath); err != nil {
		// Orphaned thumbnails waste a few KB but do not affect correctness.
		// Log and continue rather than surfacing a confusing error.
		s.log.Warn("could not delete thumbnail",
			"path", thumbPath,
			"error", err,
		)
	}

	s.log.Debug("image deleted", "path", imagePath)
	return nil
}

// GenerateThumbnail decodes imageBytes, resizes the image so its
// longest dimension is at most maxPx pixels (preserving aspect ratio),
// and returns PNG-encoded bytes.
//
// Exported for re-indexing scenarios. Normal callers use Save().
func (s *Storage) GenerateThumbnail(imageBytes []byte, maxPx int) ([]byte, error) {
	img, err := decodeImage(imageBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidImage, err)
	}

	thumb := resizeFit(img, maxPx)

	var buf bytes.Buffer
	if err := png.Encode(&buf, thumb); err != nil {
		return nil, fmt.Errorf("images: encode thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}

// ThumbPath derives the thumbnail path from a full image path.
//
//	/path/to/images/42.png  →  /path/to/images/42_thumb.png
//
// Exported so the UI layer can compute thumbnail paths from clip
// image_path fields without importing Storage or querying the database.
func ThumbPath(imagePath string) string {
	ext := filepath.Ext(imagePath)
	base := strings.TrimSuffix(imagePath, ext)
	return base + "_thumb" + ext
}

// ImagesDir returns the root directory for image storage.
func (s *Storage) ImagesDir() string {
	return s.imagesDir
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// fullPath returns the absolute path for a clip's full-size image.
// Format: <imagesDir>/<clipID>.png
func (s *Storage) fullPath(clipID int64) string {
	return filepath.Join(s.imagesDir, fmt.Sprintf("%d.png", clipID))
}

// decodeImage decodes raw bytes using registered image decoders.
// Supports PNG and JPEG via the blank imports at the top of this file.
func decodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return img, nil
}

// resizeFit scales img so its longest side equals maxPx, preserving
// aspect ratio. Images already smaller than maxPx are returned as-is
// (no upscaling).
//
// Uses BiLinear resampling (golang.org/x/image/draw):
//   - Smoother than NearestNeighbor for photographic content
//   - Fast enough at thumbnail sizes (128 px destination)
//   - CatmullRom would be sharper but overkill for list thumbnails
func resizeFit(img image.Image, maxPx int) image.Image {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	// No upscaling.
	if srcW <= maxPx && srcH <= maxPx {
		return img
	}

	dstW, dstH := fitDimensions(srcW, srcH, maxPx)
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	return dst
}

// fitDimensions computes dst dimensions that fit within maxPx×maxPx
// while preserving the src aspect ratio. Both values are >= 1.
func fitDimensions(srcW, srcH, maxPx int) (dstW, dstH int) {
	if srcW >= srcH {
		dstW = maxPx
		dstH = (srcH * maxPx) / srcW
	} else {
		dstH = maxPx
		dstW = (srcW * maxPx) / srcH
	}
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	return dstW, dstH
}

// writePNG encodes img as PNG and atomically writes it to path.
//
// Atomic write pattern:
//  1. Write to a temp file in the same directory (same filesystem)
//  2. chmod the temp file to 0600 before it becomes visible
//  3. Rename temp → final path (atomic on POSIX systems)
//
// This ensures the destination path always contains either a complete
// file or nothing — never a partial write.
func writePNG(path string, img image.Image) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".img-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := png.Encode(tmp, img); err != nil {
		return fmt.Errorf("PNG encode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %q → %q: %w", tmpName, path, err)
	}

	ok = true
	return nil
}

// removeIfExists removes a file, treating ErrNotExist as success.
func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
