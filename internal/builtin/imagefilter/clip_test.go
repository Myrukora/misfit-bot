package imagefilter

import (
	"image/color"
	"path/filepath"
	"testing"
)

// TestClipEmbedEndToEnd runs the REAL exported model end-to-end:
// preprocess (pure Go) → ONNX session → L2-normalized 768-d embedding.
// Skipped when the lib or model files are absent (same gate as the probe).
func TestClipEmbedEndToEnd(t *testing.T) {
	// e2e and the probe share the runtime; both go through initRuntime.
	if err := initRuntime(); err != nil {
		t.Skipf("onnxruntime not available: %v", err)
	}
	// Prod layout: RegisterBuiltinsWithFilter sets DataDir = modules/imagefilter.
	dataDir := filepath.Join(repoRoot(t), "modules", "imagefilter")
	variant := DefaultClipVariant
	if !ModelFilePresent(dataDir, variant) {
		t.Skipf("model file for %s missing", variant)
	}

	sess, err := loadClip(dataDir, variant)
	if err != nil {
		t.Fatalf("loadClip: %v", err)
	}
	defer sess.Close()

	red, err := Preprocess(smallPNG(t, 224, 224, color.RGBA{220, 30, 30, 255}), sess.InputSize())
	if err != nil {
		t.Fatalf("Preprocess red: %v", err)
	}
	blue, err := Preprocess(smallPNG(t, 224, 224, color.RGBA{20, 30, 220, 255}), sess.InputSize())
	if err != nil {
		t.Fatalf("Preprocess blue: %v", err)
	}

	embRed, err := sess.Embed(red)
	if err != nil {
		t.Fatalf("Embed red: %v", err)
	}
	embRed2, err := sess.Embed(red)
	if err != nil {
		t.Fatalf("Embed red again: %v", err)
	}
	embBlue, err := sess.Embed(blue)
	if err != nil {
		t.Fatalf("Embed blue: %v", err)
	}

	if len(embRed) != 768 {
		t.Fatalf("embedding dim = %d, want 768", len(embRed))
	}
	// Deterministic model → same input, same output.
	if got := Cosine(embRed, embRed2); got < 0.9999 {
		t.Errorf("determinism: cosine(red, red) = %v, want ~1", got)
	}
	same := Cosine(embRed, embBlue)
	if same >= 0.9999 {
		t.Errorf("distinct images should differ: cosine = %v", same)
	}
	t.Logf("cosine(red, blue) = %.4f (distinct images, expected well below 1)", same)

	// Tensor size guard.
	if _, err := sess.Embed(make([]float32, 10)); err == nil {
		t.Error("wrong-size tensor should error")
	}

	// Double Close is safe.
	if err := sess.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
