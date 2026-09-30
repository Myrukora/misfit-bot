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
	"sync"
)

// MaxImageBytes caps fetched/uploaded image size (50 MiB) — same cap the
// Python module enforced against hostile CDN responses.
const MaxImageBytes = 50 * 1024 * 1024

// maxDecodePixels caps decoded image dimensions (decompression-bomb guard,
// matching the Python module's MAX_IMAGE_PIXELS = 25MP). Shared by the upload
// path (decodeCheck) and the detection path (Preprocess) so a single constant
// and error message govern both.
const maxDecodePixels = 25 * 1024 * 1024

// checkPixelBudget enforces the decompression-bomb cap for a declared
// width/height pair.
func checkPixelBudget(w, h int) error {
	if w*h > maxDecodePixels {
		return fmt.Errorf("image too large: %dx%d (>25MP)", w, h)
	}
	return nil
}

// Per-guild blacklist bounds. The gallery is owner/staff-curated, but every
// reference image is embedded on the worker (and re-embedded on every variant
// switch), so an unbounded list is an unbounded RAM/time sink.
const (
	maxRefImagesPerGuild = 200
	maxRefBytesPerGuild  = 250 * 1024 * 1024
)

// images manages per-guild reference (blacklist) images under
// <dataDir>/spam_images/<guildID>/<file>. Files are content-hashed so re-adding
// the same image is idempotent, and decode-checked so only real images land.
type images struct {
	root string
	// mu serializes the cap check + write in Add (and Remove), so concurrent
	// dashboard uploads cannot both pass the per-guild budget.
	mu sync.Mutex
}

func imagesRoot(dataDir string) string {
	return filepath.Join(dataDir, "spam_images")
}

func newImages(dataDir string) *images {
	return &images{root: imagesRoot(dataDir)}
}

// guildDir resolves the guild's directory, rejecting anything that is not a
// usable directory name. Callers pass Discord snowflakes; this is the last
// line of defense against a crafted ID (".", "..", "" or anything with a
// separator) escaping <root> — the dashboard validates the shape up front.
func (im *images) guildDir(guildID string) (string, error) {
	if guildID == "" || guildID == "." || guildID == ".." || filepath.Base(guildID) != guildID {
		return "", fmt.Errorf("invalid guild id %q", guildID)
	}
	return filepath.Join(im.root, guildID), nil
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

// decodeCheck ensures the bytes are a real image and enforces the shared
// decompression-bomb cap (same as the Python module's MAX_IMAGE_PIXELS).
func decodeCheck(data []byte) (image.Config, error) {
	if len(data) == 0 {
		return image.Config{}, fmt.Errorf("empty image data")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, fmt.Errorf("not a decodable image: %w", err)
	}
	if err := checkPixelBudget(cfg.Width, cfg.Height); err != nil {
		return image.Config{}, err
	}
	return cfg, nil
}

// Add stores bytes as a reference image for the guild. Returns the stored
// filename (content-hash based, collision-safe) and whether it was new.
func (im *images) Add(guildID string, data []byte, origName string) (string, bool, error) {
	dir, err := im.guildDir(guildID)
	if err != nil {
		return "", false, err
	}
	if _, err := decodeCheck(data); err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "_" + sanitizeFilename(origName)

	im.mu.Lock()
	defer im.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, false, nil // already present
	}
	if err := checkGuildBudget(dir, len(data)); err != nil {
		return "", false, err
	}

	// Staged write with a unique temp name: two concurrent adds of different
	// images must not race on a fixed "<path>.tmp".
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, err
	}
	return name, true, nil
}

// checkGuildBudget rejects an add that would push the guild past the
// configured reference-image count or byte budget.
func checkGuildBudget(dir string, added int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	count, total := 0, 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") || strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		count++
		total += int(info.Size())
	}
	if count+1 > maxRefImagesPerGuild {
		return fmt.Errorf("blacklist is full (%d images max)", maxRefImagesPerGuild)
	}
	if total+added > maxRefBytesPerGuild {
		return fmt.Errorf("blacklist is over %d MiB", maxRefBytesPerGuild>>20)
	}
	return nil
}

// Remove deletes a reference image by filename (validated against the
// directory listing, so no traversal).
func (im *images) Remove(guildID, name string) error {
	dir, err := im.guildDir(guildID)
	if err != nil {
		return err
	}
	name = filepath.Base(name)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no such reference image %q", name)
	}
	return os.Remove(path)
}

// List returns the guild's reference image filenames, sorted.
func (im *images) List(guildID string) ([]string, error) {
	dir, err := im.guildDir(guildID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") || strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Read returns the bytes of one reference image (for re-embedding).
func (im *images) Read(guildID, name string) ([]byte, error) {
	dir, err := im.guildDir(guildID)
	if err != nil {
		return nil, err
	}
	name = filepath.Base(name)
	return os.ReadFile(filepath.Join(dir, name))
}
