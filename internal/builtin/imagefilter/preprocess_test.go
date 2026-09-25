package imagefilter

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

// renderPNG encodes an arbitrary image to PNG bytes.
func renderPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestPreprocessOutputShapeAndLayout(t *testing.T) {
	// Odd aspect ratio forces the resize+crop path: 100x400 → shortest side
	// 100 → scale 224/100 → 224x896 → center crop 224x224.
	img := image.NewRGBA(image.Rect(0, 0, 100, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{200, 100, 50, 255})
		}
	}
	out, err := Preprocess(renderPNG(t, img), 224)
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	if len(out) != 3*224*224 {
		t.Fatalf("output len = %d, want %d", len(out), 3*224*224)
	}

	// Solid-color input: every pixel in each plane must be identical, and
	// equal to normalize(200,100,50).
	want := func(c, mean, std float32) float32 {
		return (c/255.0 - mean) / std
	}
	wr := want(200, clipMean[0], clipStd[0])
	wg := want(100, clipMean[1], clipStd[1])
	wb := want(50, clipMean[2], clipStd[2])
	plane := 224 * 224
	for i := 0; i < plane; i++ {
		if math.Abs(float64(out[i]-wr)) > 0.02 {
			t.Fatalf("R plane[%d] = %v, want ~%v", i, out[i], wr)
		}
		if math.Abs(float64(out[plane+i]-wg)) > 0.02 {
			t.Fatalf("G plane[%d] = %v, want ~%v", i, out[plane+i], wg)
		}
		if math.Abs(float64(out[2*plane+i]-wb)) > 0.02 {
			t.Fatalf("B plane[%d] = %v, want ~%v", i, out[2*plane+i], wb)
		}
	}
}

func TestPreprocessCenterCrop(t *testing.T) {
	// Wide image with a distinctive center stripe: 400x100, middle column
	// band red, rest blue. After crop, the center pixel must be red-ish and
	// the corner must be blue-ish.
	img := image.NewRGBA(image.Rect(0, 0, 400, 100))
	blue := color.RGBA{0, 0, 255, 255}
	red := color.RGBA{255, 0, 0, 255}
	for y := 0; y < 100; y++ {
		for x := 0; x < 400; x++ {
			if x >= 180 && x < 220 {
				img.Set(x, y, red)
			} else {
				img.Set(x, y, blue)
			}
		}
	}
	out, err := Preprocess(renderPNG(t, img), 224)
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}

	// Sample the output back at the crop-center column (center of the 224
	// window maps to the source's center — inside the red band).
	plane := 224 * 224
	centerIdx := 112*224 + 112
	rVal := (out[centerIdx]*clipStd[0] + clipMean[0]) * 255
	bVal := (out[2*plane+centerIdx]*clipStd[2] + clipMean[2]) * 255
	if rVal <= bVal {
		t.Errorf("center should be red-dominant: r=%.0f b=%.0f", rVal, bVal)
	}

	// Corner pixel (0,0) comes from x0=88-ish scaled — outside the band, blue.
	corner := 0
	rVal = (out[corner]*clipStd[0] + clipMean[0]) * 255
	bVal = (out[2*plane+corner]*clipStd[2] + clipMean[2]) * 255
	if bVal <= rVal {
		t.Errorf("corner should be blue-dominant: r=%.0f b=%.0f", rVal, bVal)
	}
}

func TestPreprocessSmallImageUpscales(t *testing.T) {
	// 8x8 tiny image must still produce a valid 224x224 tensor.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	out, err := Preprocess(renderPNG(t, img), 224)
	if err != nil {
		t.Fatalf("Preprocess small: %v", err)
	}
	plane := 224 * 224
	// White → normalize(255) = (1-mean)/std > 0 for all channels.
	for _, idx := range []int{0, plane, 2 * plane} {
		if out[idx] <= 0 {
			t.Errorf("channel %d should be positive for white input, got %v", idx, out[idx])
		}
	}
}

func TestPreprocessRejectsGarbageAndBombs(t *testing.T) {
	if _, err := Preprocess([]byte("not an image at all"), 224); err == nil {
		t.Error("garbage should be rejected")
	}
	if _, err := Preprocess(nil, 224); err == nil {
		t.Error("nil input should be rejected")
	}
	// 6000x6000 = 36MP exceeds the 25MP cap. No need to ENCODE 36M pixels:
	// a PNG with a 6000x6000 IHDR header is enough for DecodeConfig to see
	// the declared dimensions and trip the cap.
	if _, err := Preprocess(bombPNGHeader(t, 6000, 6000), 224); err == nil {
		t.Error("25MP+ image should be rejected")
	}
	// A real 4MP image (solid color → tiny PNG) passes the cap and decodes.
	solid := image.NewRGBA(image.Rect(0, 0, 2000, 2000))
	if _, err := Preprocess(renderPNG(t, solid), 224); err != nil {
		t.Errorf("4MP image should preprocess fine: %v", err)
	}
}

// bombPNGHeader crafts a minimal PNG (signature + IHDR) declaring the given
// dimensions — enough for image.DecodeConfig, no pixel data.
func bombPNGHeader(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) // signature
	chunk := make([]byte, 0, 17)
	chunk = append(chunk, []byte("IHDR")...)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(w))
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(h))
	chunk = append(chunk, 8, 6, 0, 0, 0) // 8-bit RGBA, no interlace
	buf.Write(binary.BigEndian.AppendUint32(nil, 13))
	buf.Write(chunk)
	buf.Write(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(chunk)))
	return buf.Bytes()
}

func TestPreprocessGrayscaleAndPaletted(t *testing.T) {
	// Grayscale source (no alpha weirdness, At() returns equal RGB).
	gray := image.NewGray(image.Rect(0, 0, 50, 50))
	gray.Set(10, 10, color.Gray{128})
	out, err := Preprocess(renderPNG(t, gray), 224)
	if err != nil {
		t.Fatalf("gray Preprocess: %v", err)
	}
	if len(out) != 3*224*224 {
		t.Fatalf("bad output size %d", len(out))
	}
	// Paletted (GIF-style) source.
	pal := image.NewPaletted(image.Rect(0, 0, 60, 40), color.Palette{color.RGBA{10, 200, 30, 255}})
	if _, err := Preprocess(renderPNG(t, pal), 224); err != nil {
		t.Fatalf("paletted Preprocess: %v", err)
	}
}

func TestCosine(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}
	if got := Cosine(a, b); math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("identical vectors: %v, want 1", got)
	}
	if got := Cosine(a, c); math.Abs(float64(got)) > 1e-6 {
		t.Errorf("orthogonal vectors: %v, want 0", got)
	}
	if got := Cosine(a, []float32{-1, 0, 0}); math.Abs(float64(got+1)) > 1e-6 {
		t.Errorf("opposite vectors: %v, want -1", got)
	}
	// Length mismatch and empty guards.
	if got := Cosine(a, []float32{1, 0}); got != 0 {
		t.Errorf("length mismatch should give 0, got %v", got)
	}
	if got := Cosine(nil, nil); got != 0 {
		t.Errorf("empty should give 0, got %v", got)
	}
}
