package imagefilter

import (
	"encoding/base64"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// webpFixture is a 4x4 solid-red lossy WebP (PIL, 68 bytes).
const webpFixture = "UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoEAAQAAUAmJaACdLoB+AADsAD+8ut//NgVzXPv9//S4P0uD9Lg/9KQAAA="

// TestPreprocessDecodesWebP pins the decoder registration: Discord serves and
// users upload WebP, which image.Decode rejects unless x/image/webp is linked.
func TestPreprocessDecodesWebP(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(webpFixture)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	out, err := Preprocess(data, 224)
	if err != nil {
		t.Fatalf("Preprocess(webp): %v", err)
	}
	if len(out) != 3*224*224 {
		t.Fatalf("tensor len = %d, want %d", len(out), 3*224*224)
	}
	// Solid red (255,0,0): the red channel normalizes positive, green negative.
	if out[0] <= 1.5 {
		t.Errorf("red channel = %v, want a strong positive value", out[0])
	}
	if out[224*224] >= -1.5 {
		t.Errorf("green channel = %v, want a strong negative value", out[224*224])
	}
}

// TestPreprocessExtremeAspectIsBounded pins the crop-before-resize fix: the old
// resize-then-crop allocated a 4000×224 RGBA canvas for a 4000×1 image (and
// ~2 GB for a 10000×1 one) before the crop; the output was still 224×224.
func TestPreprocessExtremeAspectIsBounded(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4000, 1))
	for x := 0; x < 4000; x++ {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	out, err := preprocessImage(img, 224)
	if err != nil {
		t.Fatalf("preprocessImage: %v", err)
	}
	if len(out) != 3*224*224 {
		t.Fatalf("tensor len = %d, want %d", len(out), 3*224*224)
	}
	if out[0] <= 1.5 {
		t.Errorf("red channel = %v, want a strong positive value", out[0])
	}
}

// TestL2NormalizeNeverAliasesInput pins the use-after-free fix: a zero vector
// used to be returned as the input slice, which for a real embedding is the
// ONNX tensor buffer destroyed immediately after.
func TestL2NormalizeNeverAliasesInput(t *testing.T) {
	in := make([]float32, 4)
	out := l2normalize(in)
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	if &out[0] == &in[0] {
		t.Fatal("l2normalize aliased the input slice")
	}
	in[0] = 7
	if out[0] != 0 {
		t.Fatalf("output changed with the input: %v", out[0])
	}

	nonzero := []float32{3, 4}
	norm := l2normalize(nonzero)
	if &norm[0] == &nonzero[0] || norm[0] != 0.6 || norm[1] != 0.8 {
		t.Fatalf("normalized = %v", norm)
	}
}

// TestReferenceImageOversizeRejected pins the 50 MiB cap at the module API,
// not just in the dashboard upload handler.
func TestReferenceImageOversizeRejected(t *testing.T) {
	im := newImages(t.TempDir())
	_, _, err := im.Add("1", make([]byte, MaxImageBytes+1), "huge.png")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want the size rejection", err)
	}
}

// TestReferenceImageKeepsTmpInFilename pins the staged-file naming: a real
// filename containing ".tmp-" must not be skipped by List/budget (only files
// starting with the staging prefix are).
func TestReferenceImageKeepsTmpInFilename(t *testing.T) {
	im := newImages(t.TempDir())
	png := smallPNG(t, 2, 2, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	name, added, err := im.Add("1", png, "shot.tmp-1.png")
	if err != nil || !added {
		t.Fatalf("Add = (%q, %v, %v)", name, added, err)
	}
	list, err := im.List("1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0] != name {
		t.Fatalf("List = %v, want [%s]", list, name)
	}
	if _, err := im.Read("1", name); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// TestConfigWriteFailureRollsBack pins the integrity rule: when persistence
// fails, the in-memory config must not keep a value the disk doesn't have.
func TestConfigWriteFailureRollsBack(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("plant file: %v", err)
	}
	c := &config{
		path: filepath.Join(blocked, "config.json"), // parent is a file → save fails
		file: fileConfig{ClipVariant: "b32", Guilds: map[string]GuildConfig{}},
	}

	gc := GuildConfig{Enabled: true, Threshold: 0.9, Punishment: PunishBan}
	if err := c.SetGuildConfig("1", gc); err == nil {
		t.Fatal("SetGuildConfig should fail when the save fails")
	}
	if _, ok := c.file.Guilds["1"]; ok {
		t.Fatal("failed SetGuildConfig left the entry in memory")
	}

	if changed, err := c.SetGuildEnabled("1", true); err == nil || changed {
		t.Fatalf("SetGuildEnabled = (%v, %v), want a save error", changed, err)
	}
	if _, ok := c.file.Guilds["1"]; ok {
		t.Fatal("failed SetGuildEnabled left the entry in memory")
	}

	if inv, err := c.SetClipVariant("b16"); err == nil || inv {
		t.Fatalf("SetClipVariant = (%v, %v), want a save error", inv, err)
	}
	if c.file.ClipVariant != "b32" {
		t.Fatalf("variant = %q, want b32 after the failed save", c.file.ClipVariant)
	}
}

// blockingEmbedder blocks inside Embed so a test can race an invalidation
// against a reference build.
type blockingEmbedder struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingEmbedder) Embed(tensor []float32) ([]float32, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return l2normalize(make([]float32, 8)), nil
}
func (b *blockingEmbedder) InputSize() int { return 224 }
func (b *blockingEmbedder) Close() error   { return nil }

// TestGuildRefsAreNotCachedAfterInvalidation pins the generation guard: a ref
// build racing an image add/remove (or variant switch) must not repopulate the
// cache with the pre-change set.
func TestGuildRefsAreNotCachedAfterInvalidation(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	m := newManager(cfg, newImages(dir), dir, nopLogger{})
	png := smallPNG(t, 2, 2, color.RGBA{R: 9, G: 9, B: 9, A: 255})
	if _, _, err := m.imgs.Add("1", png, "ref.png"); err != nil {
		t.Fatalf("add ref: %v", err)
	}

	sess := &blockingEmbedder{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan []refEmbed, 1)
	go func() { done <- m.guildRefsFor(sess, "b32", "1") }()

	<-sess.started
	m.InvalidateRefs("1")
	close(sess.release)

	if got := <-done; got != nil {
		t.Fatalf("stale refs returned after invalidation: %d refs", len(got))
	}
	m.refsMu.RLock()
	_, cached := m.refs["1"]
	m.refsMu.RUnlock()
	if cached {
		t.Fatal("refs built across an invalidation were cached")
	}
}

// TestLooksLikeImageFallback pins the extension fallback for attachments whose
// content type is missing or generic.
func TestLooksLikeImageFallback(t *testing.T) {
	cases := []struct {
		ct, name string
		want     bool
	}{
		{"image/png", "x", true},
		{"IMAGE/WEBP", "x", true},
		{"application/octet-stream", "photo.WEBP", true},
		{"binary/octet-stream", "pic.jpeg", true},
		{"", "pic.jpeg", true},
		{"text/plain", "notes.txt", false},
		{"text/plain", "evil.png", false},
		{"application/pdf", "scan.png", false},
		{"", "noext", false},
		{"application/octet-stream", "archive.zip", false},
	}
	for _, tc := range cases {
		if got := looksLikeImage(tc.ct, tc.name); got != tc.want {
			t.Errorf("looksLikeImage(%q, %q) = %v, want %v", tc.ct, tc.name, got, tc.want)
		}
	}
}
