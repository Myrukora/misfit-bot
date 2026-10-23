package imagefilter

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // first frame
	_ "image/jpeg" // register decoders
	_ "image/png"

	_ "golang.org/x/image/webp" // Discord serves/uploads webp

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

	// CLIPProcessor resizes the shortest side to size and center-crops the
	// middle size×size region. That is equivalent to resizing the centered
	// square of the SOURCE (side min(w,h)) to size×size — and cropping first
	// bounds the intermediate to size² pixels. The old resize-then-crop form
	// allocated newW×newH, which is ~2 GB for a hostile 10000×1 image.
	side := min(w, h)
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2
	crop := image.Rect(x0, y0, x0+side, y0+side)

	resized := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(resized, resized.Bounds(), src, crop, draw.Over, nil)

	// NCHW float32 with CLIP normalization.
	out := make([]float32, 3*size*size)
	plane := size * size
	for y := range size {
		for x := range size {
			r, g, bl, _ := resized.At(x, y).RGBA() // 16-bit
			i := y*size + x
			out[i] = (float32(r>>8)/255.0 - clipMean[0]) / clipStd[0]
			out[plane+i] = (float32(g>>8)/255.0 - clipMean[1]) / clipStd[1]
			out[2*plane+i] = (float32(bl>>8)/255.0 - clipMean[2]) / clipStd[2]
		}
	}
	return out, nil
}
