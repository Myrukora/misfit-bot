package imagefilter

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// clipSession wraps one loaded ONNX CLIP vision tower. Sessions are
// long-lived; embedding calls are serialized by the manager's worker
// goroutine, so Run() itself needs no extra lock — but Load/Close are guarded
// to survive dashboard-triggered variant switches racing message events.
type clipSession struct {
	mu      sync.RWMutex
	variant string
	session *ort.DynamicAdvancedSession
	dim     int
	inSize  int
}

// variantSpec describes the ONNX artifacts for one CLIP variant. Dims/sizes
// mirror the export script's VARIANTS table (scripts/export_clip_onnx.py).
var variantSpecs = map[string]struct {
	dim    int // pooler_output width
	inSize int // square input the model was exported with
}{
	"b32":     {768, 224},
	"b16":     {768, 224},
	"l14":     {1024, 224},
	"l14-336": {1024, 336},
}

// ModelDir resolves the directory holding the exported ONNX files. Lives under
// the module's data dir so the whole feature stays self-contained; the file
// name matches the export script's output (clip-vision-<variant>.onnx).
func modelPath(dataDir, variant string) string {
	return filepath.Join(dataDir, "models", fmt.Sprintf("clip-vision-%s.onnx", variant))
}

// ModelFilePresent reports whether the variant's ONNX file exists (dashboard
// status surface: a selected variant without its file is a config problem).
// dataDir may be absolute (prod) or relative (tests) — resolved against repo
// root by walking up from CWD when the direct path doesn't exist. The walk is
// memoized: the dashboard calls this once per variant per status render.
func ModelFilePresent(dataDir, variant string) bool {
	if _, ok := variantSpecs[variant]; !ok {
		return false
	}
	path := modelPath(dataDir, variant)
	if _, err := os.Stat(path); err == nil {
		return true
	}
	if filepath.IsAbs(dataDir) {
		return false
	}
	root, ok := repoRootOnce()
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join(root, path))
	return err == nil
}

// repoRootOnce memoizes the upward walk to the module root (the directory
// holding go.mod); the CWD cannot change under a running process in practice,
// and the walk is only needed in dev when dataDir is repo-relative.
var repoRootOnce = sync.OnceValues(func() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
})

// loadClip builds a session for the given variant. The ONNX Runtime
// environment must already be initialized (see initRuntime).
func loadClip(dataDir, variant string) (*clipSession, error) {
	spec, ok := variantSpecs[variant]
	if !ok {
		return nil, fmt.Errorf("unknown CLIP variant %q", variant)
	}
	path := modelPath(dataDir, variant)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("model file for variant %q missing (run scripts/setup_imagefilter.sh): %w",
			variant, err)
	}
	s, err := ort.NewDynamicAdvancedSession(
		path,
		[]string{"pixel_values"},
		[]string{"embedding"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", path, err)
	}
	return &clipSession{variant: variant, session: s, dim: spec.dim, inSize: spec.inSize}, nil
}

// Close frees the underlying ONNX session. Safe to call twice.
func (c *clipSession) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	err := c.session.Destroy()
	c.session = nil
	return err
}

// Embed runs one preprocessed tensor through the model and returns the
// L2-normalized pooler embedding (length == c.dim). Input must be
// [1,3,inSize,inSize] — callers use c.InputSize().
func (c *clipSession) Embed(tensor []float32) ([]float32, error) {
	c.mu.RLock()
	s := c.session
	dim := c.dim
	inSize := c.inSize
	c.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("session closed")
	}
	if len(tensor) != 3*inSize*inSize {
		return nil, fmt.Errorf("tensor len %d, want %d (3*%d*%d)", len(tensor), 3*inSize*inSize, inSize, inSize)
	}

	input, err := ort.NewTensor(ort.NewShape(1, 3, int64(inSize), int64(inSize)), tensor)
	if err != nil {
		return nil, err
	}
	defer input.Destroy()

	outputs := []ort.Value{nil} // runtime-allocated
	if err := s.Run([]ort.Value{input}, outputs); err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("unexpected output type %T", outputs[0])
	}
	defer out.Destroy()

	shape := out.GetShape()
	if len(shape) != 2 || shape[1] != int64(dim) {
		return nil, fmt.Errorf("output shape %v, want [1 %d]", shape, dim)
	}
	return l2normalize(out.GetData()), nil
}

// InputSize reports the square input this session was exported with.
func (c *clipSession) InputSize() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.inSize
}

// Cosine computes cosine similarity of two (already L2-normalized) vectors.
// Mismatched or empty inputs score 0 (never punish on internal errors —
// same policy as the Python module).
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return float32(dot)
}

// l2normalize scales a vector to unit length; zero vectors return unchanged.
func l2normalize(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return v
	}
	inv := float32(1.0 / norm)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}
