package imagefilter

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

// The probe (and later the real session) needs the ONNX Runtime C library and
// an exported CLIP ONNX model. Neither is committed: both come from
// scripts/setup_imagefilter.sh (which wraps scripts/setup_onnx.sh + the export
// script). When either is missing we skip so `go test ./...` stays green on
// machines without them.
const (
	defaultModelRel = "modules/imagefilter/models/clip-vision-b32.onnx"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	// Tests run with the package dir as CWD; walk up to the go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("could not locate repo root (no go.mod upward)")
		}
		dir = parent
	}
}

// TestONNXProbe loads the exported CLIP vision model and runs a dummy
// [1,3,224,224] tensor through it, asserting the 768-d pooler_output shape the
// whole filter is built around (see the A0 port sheet in the plan).
func TestONNXProbe(t *testing.T) {
	if err := initRuntime(); err != nil {
		t.Skipf("onnxruntime not available (run scripts/setup_imagefilter.sh): %v", err)
	}
	modelPath := filepath.Join(repoRoot(t), defaultModelRel)
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("CLIP ONNX model not present (run scripts/setup_imagefilter.sh): %v", err)
	}

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		[]string{"pixel_values"},
		[]string{"embedding"},
		nil,
	)
	if err != nil {
		t.Fatalf("NewDynamicAdvancedSession: %v", err)
	}
	defer session.Destroy()

	input, err := ort.NewTensor(ort.NewShape(1, 3, 224, 224), make([]float32, 1*3*224*224))
	if err != nil {
		t.Fatalf("NewTensor(input): %v", err)
	}
	defer input.Destroy()

	// nil output = the runtime allocates + fills it during Run; the slice
	// entry is replaced with the real tensor.
	outputs := []ort.Value{nil}
	if err := session.Run([]ort.Value{input}, outputs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		t.Fatalf("output is %T, want *ort.Tensor[float32]", outputs[0])
	}
	defer out.Destroy()

	shape := out.GetShape()
	if len(shape) != 2 || shape[0] != 1 || shape[1] != 768 {
		t.Fatalf("output shape = %v, want [1 768]", shape)
	}

	// A real embedding must be non-degenerate: non-zero, finite, norm>0.
	var norm float64
	for _, v := range out.GetData() {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatalf("non-finite value in embedding: %v", v)
		}
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		t.Fatal("embedding is all zeros")
	}
	t.Logf("OK: embedding [1,768], L2 norm %.4f", norm)
}
