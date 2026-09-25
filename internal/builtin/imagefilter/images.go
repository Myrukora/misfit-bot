package imagefilter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // first frame of GIFs is a valid reference image
	_ "image/jpeg" // register decoders for image.Decode
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxImageBytes caps fetched/uploaded image size (50 MiB) — same cap the
// Python module enforced against hostile CDN responses.
const MaxImageBytes = 50 * 1024 * 1024

// images manages per-guild reference (blacklist) images under
// <dataDir>/spam_images/<guildID>/<file>. Files are content-hashed so re-adding
// the same image is idempotent, and decode-checked so only real images land.
type images struct {
	root string
}

func imagesRoot(dataDir string) string {
	return filepath.Join(dataDir, "spam_images")
}

func newImages(dataDir string) *images {
	return &images{root: imagesRoot(dataDir)}
}

func (im *images) guildDir(guildID string) string {
	return filepath.Join(im.root, guildID)
}

// sanitizeFilename keeps alphanumerics, dot, dash, underscore; everything else
// collapses to _. Prevents traversal and weird characters from user uploads.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		s = "image"
	}
	return s
}

// decodeCheck ensures the bytes are a real image and enforces the 25 MP
// decompression-bomb cap (same as the Python module's MAX_IMAGE_PIXELS).
func decodeCheck(data []byte) (image.Config, error) {
	if len(data) == 0 {
		return image.Config{}, fmt.Errorf("empty image data")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, fmt.Errorf("not a decodable image: %w", err)
	}
	const maxPixels = 25 * 1024 * 1024
	if cfg.Width*cfg.Height > maxPixels {
		return image.Config{}, fmt.Errorf("image too large: %dx%d (>25MP)", cfg.Width, cfg.Height)
	}
	return cfg, nil
}

// Add stores bytes as a reference image for the guild. Returns the stored
// filename (content-hash based, collision-safe) and whether it was new.
func (im *images) Add(guildID string, data []byte, origName string) (string, bool, error) {
	if _, err := decodeCheck(data); err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "_" + sanitizeFilename(origName)

	dir := im.guildDir(guildID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, false, nil // already present
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", false, err
	}
	return name, true, nil
}

// Remove deletes a reference image by filename (validated against the
// directory listing, so no traversal).
func (im *images) Remove(guildID, name string) error {
	name = filepath.Base(name)
	path := filepath.Join(im.guildDir(guildID), name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no such reference image %q", name)
	}
	return os.Remove(path)
}

// List returns the guild's reference image filenames, sorted.
func (im *images) List(guildID string) ([]string, error) {
	dir := im.guildDir(guildID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Read returns the bytes of one reference image (for re-embedding).
func (im *images) Read(guildID, name string) ([]byte, error) {
	name = filepath.Base(name)
	return os.ReadFile(filepath.Join(im.guildDir(guildID), name))
}
