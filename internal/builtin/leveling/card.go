package leveling

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/webp"
)

//go:embed assets/banner.png
var bannerFS embed.FS

// banner loading: decoded once, the card is rendered per request.
var (
	bannerOnce sync.Once
	bannerImg  image.Image
	bannerErr  error
)

func loadBanner() (image.Image, error) {
	bannerOnce.Do(func() {
		f, err := bannerFS.Open("assets/banner.png")
		if err != nil {
			bannerErr = fmt.Errorf("open embedded banner: %w", err)
			return
		}
		defer f.Close()
		raw, err := io.ReadAll(f)
		if err != nil {
			bannerErr = fmt.Errorf("read embedded banner: %w", err)
			return
		}
		bannerImg, bannerErr = png.Decode(bytes.NewReader(raw))
		if bannerErr != nil {
			bannerErr = fmt.Errorf("decode embedded banner: %w", bannerErr)
		}
	})
	return bannerImg, bannerErr
}

// ── card geometry (800×200, mascot banner cover-filled) ──────────────────
// The banner art (Krea2 Identity Edit) puts the girl on the right half, so
// all overlay text lives on the scrimmed bottom band and the right fade.
const (
	cardWidth  = 800
	cardHeight = 200

	cardAvatarSize = 128
	cardAvatarX    = 20
	cardAvatarY    = 10

	scrimTopY = 112 // below the girl's chin; quad-ease to full at scrimBotY
	scrimBotY = 196
	scrimMaxA = 170.0

	rightFadeX     = 560
	rightFadeMaxA  = 130.0
	cardStatsRight = 784

	cardNameBaseline = 126
	cardNameSize     = 32
	cardTextX        = 168
	cardNameMaxPx    = 280

	cardProgressX0 = 20
	cardProgressX1 = 460
	cardProgressY0 = 152
	cardProgressY1 = 180
	cardProgressR  = 11
)

var (
	scrimColor  = color.RGBA{0x08, 0x06, 0x05, 0xFF}
	cardWhite   = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	cardGray    = color.RGBA{0xD8, 0xDC, 0xE2, 0xFF}
	avatarBgC   = color.RGBA{0x31, 0x33, 0x3C, 0xFF}
	trackGray   = color.RGBA{0x22, 0x1D, 0x18, 0xFF}
	outlineC    = color.RGBA{0x0A, 0x08, 0x06, 0xFF}
	crimsonLow  = color.RGBA{0x6E, 0x14, 0x18, 0xFF}
	crimsonHigh = color.RGBA{0xE8, 0x3A, 0x3A, 0xFF}
)

// rankCardData is everything the card renderer needs; tests fill AvatarBytes
// to avoid network access.
type rankCardData struct {
	Username    string
	DisplayName string
	AvatarURL   string
	AvatarBytes []byte

	Level        int
	Rank         int
	XP           int64
	Progress     int64
	NextLevelXP  int64
	Messages     int64
	VoiceSeconds int64
	MaxLevel     int
}

var (
	cardFontsOnce sync.Once
	cardFonts     struct {
		name  font.Face
		stats font.Face
		glyph font.Face
		err   error
	}
)

func cardFontSet() (name, stats, glyph font.Face, err error) {
	cardFontsOnce.Do(func() {
		bold, err := opentype.Parse(gobold.TTF)
		if err != nil {
			cardFonts.err = fmt.Errorf("parse bold font: %w", err)
			return
		}
		regular, err := opentype.Parse(goregular.TTF)
		if err != nil {
			cardFonts.err = fmt.Errorf("parse regular font: %w", err)
			return
		}
		for _, spec := range []struct {
			dst  *font.Face
			src  *opentype.Font
			size float64
		}{
			{&cardFonts.name, bold, cardNameSize},
			{&cardFonts.stats, bold, 22},
			{&cardFonts.glyph, regular, 54},
		} {
			face, err := opentype.NewFace(spec.src, &opentype.FaceOptions{Size: spec.size, DPI: 72, Hinting: font.HintingFull})
			if err != nil {
				cardFonts.err = fmt.Errorf("build font face: %w", err)
				return
			}
			*spec.dst = face
		}
	})
	return cardFonts.name, cardFonts.stats, cardFonts.glyph, cardFonts.err
}

// renderRankCard draws the 800×200 rank card over the mascot banner as a PNG.
func renderRankCard(data rankCardData) ([]byte, error) {
	banner, err := loadBanner()
	if err != nil {
		return nil, fmt.Errorf("load banner: %w", err)
	}
	nameFace, statsFace, glyphFace, err := cardFontSet()
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, cardWidth, cardHeight))

	fillBanner(img, banner)
	drawScrims(img)
	drawCardAvatar(img, data, glyphFace)

	levelText := fmt.Sprintf("Level: %d", data.Level)
	rankText := cardRankLabel(data.Rank)
	xpText := xpLabel(data)
	drawRightLine(img, statsFace, cardStatsRight, 60, cardWhite, levelText)
	drawRightLine(img, statsFace, cardStatsRight, 92, cardGray, rankText)
	drawRightLine(img, statsFace, cardStatsRight, 124, cardWhite, xpText)

	name := strings.TrimSpace(data.Username)
	if name == "" {
		name = strings.TrimSpace(data.DisplayName)
	}
	if name == "" {
		name = "member"
	}
	if !strings.HasPrefix(name, "@") {
		name = "@" + name
	}
	name = trimToWidth(nameFace, name, cardNameMaxPx)
	drawOutlined(img, nameFace, cardTextX, cardNameBaseline, cardWhite, name)
	wTotal := font.MeasureString(nameFace, name).Ceil()
	drawBoldUnderline(img, cardTextX, cardNameBaseline+12, wTotal, 5)

	drawProgress(img, data)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode rank card: %w", err)
	}
	return buf.Bytes(), nil
}

func xpLabel(data rankCardData) string {
	if data.NextLevelXP <= 0 {
		return "XP: MAX"
	}
	return fmt.Sprintf("XP: %s / %s", formatCompact(data.Progress), formatCompact(data.NextLevelXP))
}

func cardRankLabel(rank int) string {
	if rank <= 0 {
		return "Rank: —"
	}
	return fmt.Sprintf("Rank: %d", rank)
}

// drawBoldUnderline: solid crimson bar under the username, gradient toward
// the dark end with a darker bottom edge for depth.
func drawBoldUnderline(img *image.RGBA, x0, y, w, h int) {
	for dx := 0; dx < w; dx++ {
		t := float64(dx) / math.Max(1, float64(w))
		c := lerpColor(crimsonHigh, crimsonLow, t)
		for dy := 0; dy < h; dy++ {
			blendPxFull(img, x0+dx, y+dy, c, 235)
		}
	}
	for dx := 0; dx < w; dx++ {
		blendPx(img, x0+dx, y+h-1, outlineC, 140)
	}
}

func fillBanner(img *image.RGBA, banner image.Image) {
	bw := banner.Bounds().Dx()
	bh := banner.Bounds().Dy()
	scale := float64(cardHeight) / float64(bh)
	scaledW := int(float64(bw) * scale)
	scaled := image.NewRGBA(image.Rect(0, 0, scaledW, cardHeight))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), banner, banner.Bounds(), draw.Over, nil)
	draw.Draw(img, img.Bounds(), scaled, image.Point{}, draw.Src)
}

// drawScrims paints the bottom quad-ease fade and the mirrored right-edge
// fade. They are the first layer over the banner so every overlay (avatar,
// text, bar) draws above them untouched.
func drawScrims(img *image.RGBA) {
	for y := scrimTopY; y < cardHeight; y++ {
		t := float64(y-scrimTopY) / float64(scrimBotY-scrimTopY)
		if t > 1 {
			t = 1
		}
		a := scrimMaxA * t * t
		for x := 0; x < cardWidth; x++ {
			blendPx(img, x, y, scrimColor, a)
		}
	}
	for y := 0; y < cardHeight; y++ {
		for x := rightFadeX; x < cardWidth; x++ {
			t := float64(x-rightFadeX) / float64(cardWidth-1-rightFadeX)
			a := rightFadeMaxA * t * t
			blendPx(img, x, y, scrimColor, a)
		}
	}
}

// drawOutlined draws text with a ~4px black outline (dilated glyph mask)
// under the fill, plus a soft drop shadow — readable on any background.
func drawOutlined(img *image.RGBA, face font.Face, x, baseline int, c color.Color, text string) {
	mask, pad, ascent := textMask(face, text)
	drawMaskPass(img, mask, x-pad+2, baseline-ascent-pad+2, 0.55)
	dilated := dilateMask(mask, 2)
	drawMaskPassColor(img, dilated, x-pad, baseline-ascent-pad, outlineC, 1.0)
	drawFace(img, face, x, baseline, c, text)
}

// textMask renders the string into an offscreen white-on-transparent mask.
func textMask(face font.Face, text string) (image.Image, int, int) {
	w := font.MeasureString(face, text).Ceil()
	ascent := face.Metrics().Ascent.Ceil()
	descent := face.Metrics().Descent.Ceil()
	pad := 12 // room for stroke + offset
	mask := image.NewRGBA(image.Rect(0, 0, w+pad*2, ascent+descent+pad*2))
	md := &font.Drawer{
		Dst:  mask,
		Src:  image.NewUniform(color.RGBA{255, 255, 255, 255}),
		Face: face,
		Dot:  fixed.P(pad, pad+ascent),
	}
	md.DrawString(text)
	return mask, pad, ascent
}

// drawMaskPass blends the mask alpha as black at the given strength.
func drawMaskPass(img *image.RGBA, mask image.Image, ox, oy int, strength float64) {
	b := mask.Bounds()
	for yy := 0; yy < b.Dy(); yy++ {
		for xx := 0; xx < b.Dx(); xx++ {
			_, _, _, a := mask.At(xx, yy).RGBA()
			if a == 0 {
				continue
			}
			cardX, cardY := ox+xx, oy+yy
			if cardX < 0 || cardY < 0 || cardX >= cardWidth || cardY >= cardHeight {
				continue
			}
			blendPx(img, cardX, cardY, color.RGBA{0, 0, 0, 0xFF}, float64(a>>8)*strength)
		}
	}
}

// drawMaskPassColor blends a mask as an arbitrary solid color (OVER).
func drawMaskPassColor(img *image.RGBA, mask image.Image, ox, oy int, c color.RGBA, strength float64) {
	b := mask.Bounds()
	for yy := 0; yy < b.Dy(); yy++ {
		for xx := 0; xx < b.Dx(); xx++ {
			_, _, _, a := mask.At(xx, yy).RGBA()
			if a == 0 {
				continue
			}
			cardX, cardY := ox+xx, oy+yy
			if cardX < 0 || cardY < 0 || cardX >= cardWidth || cardY >= cardHeight {
				continue
			}
			blendPxFull(img, cardX, cardY, c, float64(a>>8)*strength)
		}
	}
}

// dilateMask expands the mask by Chebyshev radius r with a soft rim — the
// outline stroke.
func dilateMask(src image.Image, r int) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()+r*2, b.Dy()+r*2))
	for yy := 0; yy < out.Bounds().Dy(); yy++ {
		for xx := 0; xx < out.Bounds().Dx(); xx++ {
			best := r + 1
		search:
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					sx, sy := xx-r+dx, yy-r+dy
					if sx < 0 || sy < 0 || sx >= b.Dx() || sy >= b.Dy() {
						continue
					}
					_, _, _, a := src.At(sx, sy).RGBA()
					if a > 0 && dx*dx+dy*dy < best*best {
						best = dx*dx + dy*dy
						if best == 0 {
							break search
						}
					}
				}
			}
			if best <= r {
				v := uint8(255)
				if best >= r-1 {
					v = 128
				}
				out.SetRGBA(xx, yy, color.RGBA{255, 255, 255, v})
			}
		}
	}
	return out
}

func drawRightLine(img *image.RGBA, face font.Face, rightX, baseline int, c color.Color, text string) {
	w := font.MeasureString(face, text).Ceil()
	drawOutlined(img, face, rightX-w, baseline, c, text)
}

// drawCardAvatar paints the avatar clipped to an AA circle. No ring — the
// bare disc sits directly on the banner.
func drawCardAvatar(img *image.RGBA, data rankCardData, glyphFace font.Face) {
	var avatar image.Image
	if len(data.AvatarBytes) > 0 {
		avatar = decodeAvatar(data.AvatarBytes)
	} else if data.AvatarURL != "" {
		avatar = decodeAvatar(avatarFetch(data.AvatarURL))
	}
	cx := float64(cardAvatarX) + float64(cardAvatarSize)/2
	cy := float64(cardAvatarY) + float64(cardAvatarSize)/2

	if avatar != nil {
		scaled := image.NewRGBA(image.Rect(0, 0, cardAvatarSize, cardAvatarSize))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), avatar, avatar.Bounds(), draw.Over, nil)
		for dy := 0; dy < cardAvatarSize; dy++ {
			for dx := 0; dx < cardAvatarSize; dx++ {
				px, py := float64(cardAvatarX+dx)+0.5, float64(cardAvatarY+dy)+0.5
				d := math.Hypot(px-cx, py-cy)
				cov := math.Max(0, math.Min(1, (61.5-d)+0.5))
				if cov <= 0 {
					continue
				}
				r, g, b, a := scaled.At(dx, dy).RGBA()
				if a == 0 {
					blendPx(img, cardAvatarX+dx, cardAvatarY+dy, avatarBgC, cov*255)
					continue
				}
				blendPxFull(img, cardAvatarX+dx, cardAvatarY+dy,
					color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xFF}, cov*255)
			}
		}
		return
	}

	// fallback: flat disc with the member's initial
	circleBlend(img, cx, cy, 61.5, 1.5, avatarBgC, 1.0)
	label := strings.TrimSpace(data.DisplayName)
	if label == "" {
		label = strings.TrimPrefix(strings.TrimSpace(data.Username), "@")
	}
	initial := "?"
	if runes := []rune(label); len(runes) > 0 {
		initial = strings.ToUpper(string(runes[0]))
	}
	w := font.MeasureString(glyphFace, initial).Ceil()
	x := cardAvatarX + (cardAvatarSize-w)/2
	y := cardAvatarY + cardAvatarSize/2 + glyphFace.Metrics().Ascent.Ceil()/2
	drawFace(img, glyphFace, x, y, cardWhite, initial)
}

// drawProgress: crimson gradient pill fill, SDF-coverage AA on every pixel,
// inset track shadow and a soft top highlight — both clipped to the SDF so
// nothing pokes past the rounded caps.
func drawProgress(img *image.RGBA, data rankCardData) {
	roundedRectBlend(img, float64(cardProgressX0), float64(cardProgressY0),
		float64(cardProgressX1), float64(cardProgressY1), float64(cardProgressR), trackGray, 1.0)
	if data.NextLevelXP <= 0 || data.Progress <= 0 {
		return
	}
	frac := float64(data.Progress) / float64(data.NextLevelXP)
	if frac > 1 {
		frac = 1
	}
	width := math.Round(frac * float64(cardProgressX1-cardProgressX0))
	if width < 22 {
		width = 22
	}
	radius := float64(cardProgressR)
	if width/2 < radius {
		radius = width / 2
	}
	x1 := float64(cardProgressX0) + width
	span := width - 1
	if span <= 0 {
		span = 1
	}
	// fill: each pixel scaled by the pill SDF coverage (correct AA caps on
	// BOTH ends; no post-clip pass)
	fw, fh := (x1-float64(cardProgressX0))/2, float64(cardProgressY1-cardProgressY0)/2
	fcx, fcy := (float64(cardProgressX0)+x1)/2, float64(cardProgressY0+cardProgressY1)/2
	for y := cardProgressY0; y <= cardProgressY1; y++ {
		for x := cardProgressX0; x <= int(x1); x++ {
			qx := math.Abs(float64(x)+0.5-fcx) - (fw - radius)
			qy := math.Abs(float64(y)+0.5-fcy) - (fh - radius)
			dOut := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - radius
			cov := math.Max(0, math.Min(1, -(dOut)+0.5))
			if cov <= 0 {
				continue
			}
			t := float64(x-cardProgressX0) / span
			c := lerpColor(crimsonLow, crimsonHigh, t)
			blendPx(img, x, y, c, cov*255)
		}
	}
	// soft top highlight, clipped to the fill pill's SDF
	hlY := cardProgressY0 + 2
	hw, hh := (x1-float64(cardProgressX0))/2, float64(cardProgressY1-cardProgressY0)/2
	ccx, ccy := (float64(cardProgressX0)+x1)/2, float64(cardProgressY0+cardProgressY1)/2
	for x := cardProgressX0 + 2; x < int(x1)-2; x++ {
		qx := math.Abs(float64(x)+0.5-ccx) - (hw - radius)
		qy := math.Abs(float64(hlY)+0.5-ccy) - (hh - radius)
		dOut := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - radius
		if dOut < -2.0 {
			blendPx(img, x, hlY, color.RGBA{0xFF, 0xE2, 0xE0, 0xFF}, 90)
		}
	}
	// inset top shadow, drawn over the fill like the approved preview
	shY := cardProgressY0 + 1
	thw, thh := float64(cardProgressX1-cardProgressX0)/2, float64(cardProgressY1-cardProgressY0)/2
	tccx, tccy := float64(cardProgressX0+cardProgressX1)/2, float64(cardProgressY0+cardProgressY1)/2
	trad := float64(cardProgressR)
	for x := cardProgressX0 + 2; x < cardProgressX1-2; x++ {
		qx := math.Abs(float64(x)+0.5-tccx) - (thw - trad)
		qy := math.Abs(float64(shY)+0.5-tccy) - (thh - trad)
		dOut := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - trad
		if dOut < -1.5 {
			blendPx(img, x, shY, color.RGBA{0, 0, 0, 0xFF}, 80)
		}
	}
}

// ── blending primitives ──────────────────────────────────────────────────

// blendPx lerps the destination toward c by alpha a (0..255).
func blendPx(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	r0, g0, b0, _ := img.At(x, y).RGBA()
	br, bg, bb := float64(r0>>8), float64(g0>>8), float64(b0>>8)
	k := a / 255
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(math.Round(br + (float64(c.R)-br)*k)),
		G: uint8(math.Round(bg + (float64(c.G)-bg)*k)),
		B: uint8(math.Round(bb + (float64(c.B)-bb)*k)),
		A: 0xFF,
	})
}

// blendPxFull composites an opaque source at coverage a (0..255): OVER.
func blendPxFull(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if a <= 0 {
		return
	}
	k := a / 255
	r0, g0, b0, _ := img.At(x, y).RGBA()
	br, bg, bb := float64(r0>>8), float64(g0>>8), float64(b0>>8)
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(math.Round(float64(c.R)*k + br*(1-k))),
		G: uint8(math.Round(float64(c.G)*k + bg*(1-k))),
		B: uint8(math.Round(float64(c.B)*k + bb*(1-k))),
		A: 0xFF,
	})
}

// circleBlend fills an AA circle via signed-distance coverage.
func circleBlend(img *image.RGBA, cx, cy, radius, edgeWidth float64, c color.RGBA, aScale float64) {
	minX := int(math.Floor(cx - radius - 1))
	maxX := int(math.Ceil(cx + radius + 1))
	minY := int(math.Floor(cy - radius - 1))
	maxY := int(math.Ceil(cy + radius + 1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			cov := math.Max(0, math.Min(1, (radius-d)/edgeWidth+0.5))
			if cov <= 0 {
				continue
			}
			blendPx(img, x, y, c, cov*255*aScale)
		}
	}
}

// roundedRectBlend fills an AA rounded rect via SDF coverage.
func roundedRectBlend(img *image.RGBA, x0, y0, x1, y1, radius float64, c color.RGBA, aScale float64) {
	minX := int(math.Floor(x0 - 1))
	maxX := int(math.Ceil(x1 + 1))
	minY := int(math.Floor(y0 - 1))
	maxY := int(math.Ceil(y1 + 1))
	hw := (x1 - x0) / 2
	hh := (y1 - y0) / 2
	ccx := (x0 + x1) / 2
	ccy := (y0 + y1) / 2
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			qx := math.Abs(float64(x)+0.5-ccx) - (hw - radius)
			qy := math.Abs(float64(y)+0.5-ccy) - (hh - radius)
			dOut := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - radius
			cov := math.Max(0, math.Min(1, -dOut+0.5))
			if cov <= 0 {
				continue
			}
			blendPx(img, x, y, c, cov*255*aScale)
		}
	}
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		R: uint8(math.Round(float64(a.R) + (float64(b.R)-float64(a.R))*t)),
		G: uint8(math.Round(float64(a.G) + (float64(b.G)-float64(a.G))*t)),
		B: uint8(math.Round(float64(a.B) + (float64(b.B)-float64(a.B))*t)),
		A: 0xFF,
	}
}

func drawFace(img draw.Image, face font.Face, x, baseline int, c color.Color, text string) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, baseline),
	}
	d.DrawString(text)
}

// trimToWidth shortens s with an ellipsis until it fits maxPx.
func trimToWidth(face font.Face, s string, maxPx int) string {
	if font.MeasureString(face, s).Ceil() <= maxPx {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if font.MeasureString(face, candidate).Ceil() <= maxPx {
			return candidate
		}
	}
	return "…"
}

// formatCompact renders 999 / 1.1K / 7.1K / 1.2M: one decimal below ten units,
// none above.
func formatCompact(v int64) string {
	if v < 0 {
		v = 0
	}
	switch {
	case v < 1000:
		return fmt.Sprintf("%d", v)
	case v < 1_000_000:
		scaled := float64(v) / 1000
		if scaled < 10 {
			return fmt.Sprintf("%.1fK", scaled)
		}
		return fmt.Sprintf("%dK", int64(math.Round(scaled)))
	default:
		scaled := float64(v) / 1_000_000
		if scaled < 10 {
			return fmt.Sprintf("%.1fM", scaled)
		}
		return fmt.Sprintf("%dM", int64(math.Round(scaled)))
	}
}

// formatVoice renders a voice duration for the embed fallback (3h 20m).
func formatVoice(seconds int64) string {
	if seconds <= 0 {
		return "0m"
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// ── avatar decoding ──────────────────────────────────────────────────────

var avatarClient = &http.Client{Timeout: 5 * time.Second}

// avatarFetch is the network seam the renderer uses; tests replace it so a
// card render never touches the network.
var avatarFetch = fetchAvatar

// avatarCache is a bounded URL→bytes cache: avatars change rarely and the bot
// may render many cards per minute.
var avatarCache = struct {
	mu    sync.Mutex
	data  map[string][]byte
	order []string
}{data: map[string][]byte{}}

const avatarCacheLimit = 256

func fetchAvatar(url string) []byte {
	if url == "" {
		return nil
	}
	avatarCache.mu.Lock()
	if raw, ok := avatarCache.data[url]; ok {
		avatarCache.mu.Unlock()
		return raw
	}
	avatarCache.mu.Unlock()

	resp, err := avatarClient.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || len(raw) == 0 {
		return nil
	}

	avatarCache.mu.Lock()
	if _, ok := avatarCache.data[url]; !ok {
		avatarCache.data[url] = raw
		avatarCache.order = append(avatarCache.order, url)
		for len(avatarCache.order) > avatarCacheLimit {
			oldest := avatarCache.order[0]
			avatarCache.order = avatarCache.order[1:]
			delete(avatarCache.data, oldest)
		}
	}
	avatarCache.mu.Unlock()
	return raw
}

func decodeAvatar(raw []byte) image.Image {
	if len(raw) == 0 {
		return nil
	}
	if img, err := png.Decode(bytes.NewReader(raw)); err == nil {
		return img
	}
	if img, err := jpeg.Decode(bytes.NewReader(raw)); err == nil {
		return img
	}
	if img, err := webp.Decode(bytes.NewReader(raw)); err == nil {
		return img
	}
	return nil
}

// avatarURL resolves a member's avatar at the card's preferred size.
func avatarURL(user discord.User) string {
	return user.EffectiveAvatarURL(discord.WithSize(256))
}
