package imagefilter

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // first frame
	_ "image/jpeg" // register decoders
	_ "image/png"
	"math"

	"golang.org/x/image/draw"
)

// CLIP ViT normalization constants (openai/clip-* processor defaults — the
// same values the Python module's CLIPProcessor applied). The resize target
// comes per-model from variantSpecs (clip.go); the 224 default is not baked in
// here.
var (
	clipMean = [3]float32{0.48145466, 0.4578275, 0.40821073}
	clipStd  = [3]float32{0.26862954, 0.26130258, 0.27577711}
)

// Preprocess turns image bytes into the CLIP model input tensor:
// [1, 3, size, size] float32 NCHW, normalized with CLIP mean/std.
// Pipeline mirrors CLIPProcessor defaults: RGB, resize shortest side to size
// (bicubic ≈ CatmullRom), center crop size×size, /255, normalize.
func Preprocess(data []byte, size int) ([]float32, error) {
	if size <= 0 {
		return nil, fmt.Errorf("invalid input size %d", size)
	}
	// Cheap header check first: dimensions + bomb cap without decoding pixels.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("zero-sized image")
	}
	if err := checkPixelBudget(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return preprocessImage(src, size)
}

// preprocessImage is the testable core (operating on a decoded image).
func preprocessImage(src image.Image, size int) ([]float32, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("zero-sized image")
	}

	// Shortest side → size, preserving aspect ratio (upscales small images,
	// exactly like PIL's bicubic resize in CLIPProcessor).
	scale := float64(size) / float64(min(w, h))
	newW := int(math.Round(float64(w) * scale))
	newH := int(math.Round(float64(h) * scale))
	if newW < size {
		newW = size
	}
	if newH < size {
		newH = size
	}

	// Resize into an RGBA canvas (CatmullRom ≈ PIL bicubic quality).
	resized := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(resized, resized.Bounds(), src, b, draw.Over, nil)

	// Center crop size×size.
	x0 := (newW - size) / 2
	y0 := (newH - size) / 2

	// NCHW float32 with CLIP normalization.
	out := make([]float32, 3*size*size)
	plane := size * size
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, bl, _ := resized.At(x0+x, y0+y).RGBA() // 16-bit
			i := y*size + x
			out[i] = (float32(r>>8)/255.0 - clipMean[0]) / clipStd[0]
			out[plane+i] = (float32(g>>8)/255.0 - clipMean[1]) / clipStd[1]
			out[2*plane+i] = (float32(bl>>8)/255.0 - clipMean[2]) / clipStd[2]
		}
	}
	return out, nil
}
