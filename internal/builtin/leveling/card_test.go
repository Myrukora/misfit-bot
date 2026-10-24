package leveling

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// solidPNG builds a square PNG used as a fake avatar.
func solidPNG(t *testing.T, size int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode avatar: %v", err)
	}
	return buf.Bytes()
}

func decodeCard(t *testing.T, raw []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 200 {
		t.Fatalf("card is %dx%d, want 800x200", b.Dx(), b.Dy())
	}
	return img
}

func rgbaAt(t *testing.T, img image.Image, x, y int) color.RGBA {
	t.Helper()
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func luminance(c color.RGBA) float64 {
	return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
}

// nearAvatarRing reports whether a pixel sits in the light-gray band the old
// avatar ring used to paint ({0xE2,0xE5,0xEA} and AA variants).
func nearAvatarRing(c color.RGBA, tol uint8) bool {
	lo, hi := int(avatarRing0.R)-int(tol), int(avatarRing0.R)+int(tol)
	for _, ch := range []uint8{c.R, c.G, c.B} {
		if int(ch) < lo || int(ch) > hi {
			return false
		}
	}
	// band must be neutral gray, not a colored highlight
	if math.Abs(float64(c.R)-float64(c.G)) > 24 || math.Abs(float64(c.G)-float64(c.B)) > 24 {
		return false
	}
	return true
}

// avatarRing0 is the ring color the old card drew behind the avatar; the new
// card must not paint it anywhere on the avatar's rim.
var avatarRing0 = color.RGBA{0xE2, 0xE5, 0xEA, 0xFF}

// TestRankCardRendersPNG pins the banner-backed layout: banner art, a ring-less
// circular avatar, outlined right-aligned stats and the crimson underline.
func TestRankCardRendersPNG(t *testing.T) {
	avatarC := color.RGBA{0x5A, 0x8A, 0xC4, 0xFF}
	raw, err := renderRankCard(rankCardData{
		Username:     "@vixen",
		DisplayName:  "Vixen",
		AvatarBytes:  solidPNG(t, 256, avatarC),
		Level:        70,
		Rank:         13,
		XP:           5800,
		Progress:     1100,
		NextLevelXP:  7100,
		Messages:     1200,
		VoiceSeconds: 12000,
		MaxLevel:     500,
	})
	if err != nil {
		t.Fatalf("renderRankCard: %v", err)
	}
	img := decodeCard(t, raw)

	// The avatar circle is painted and masked: its centre carries the avatar
	// colour, the corner of its bounding box stays banner.
	if got := rgbaAt(t, img, cardAvatarX+cardAvatarSize/2, cardAvatarY+cardAvatarSize/2); got != avatarC {
		t.Fatalf("avatar centre = %#v, want the avatar colour", got)
	}
	if got := rgbaAt(t, img, cardAvatarX, cardAvatarY); got == avatarC {
		t.Fatal("avatar corner is still avatar-coloured; circle not masked")
	}

	// No white ring: sample the rim just outside the clip radius; the old ring
	// would paint ~all of it light gray, the banner should not.
	cx := float64(cardAvatarX) + float64(cardAvatarSize)/2
	cy := float64(cardAvatarY) + float64(cardAvatarSize)/2
	ringish := 0
	for i := 0; i < 8; i++ {
		ang := float64(i) * math.Pi / 4
		px := int(cx + 64.5*math.Cos(ang))
		py := int(cy + 64.5*math.Sin(ang))
		if nearAvatarRing(rgbaAt(t, img, px, py), 0x1E) {
			ringish++
		}
	}
	if ringish >= 3 {
		t.Fatalf("avatar rim looks ringed: %d/8 sampled rim pixels match the old ring color", ringish)
	}

	// Right-aligned stats text: pure-white glyph pixels exist in the right column.
	white := 0
	for y := 40; y < 132 && white == 0; y++ {
		for x := 600; x < cardStatsRight; x++ {
			if rgbaAt(t, img, x, y) == cardWhite {
				white++
				break
			}
		}
	}
	if white == 0 {
		t.Fatal("no white stats text found on the right side")
	}

	// Crimson underline under the username: reddish-dominant pixels along its row.
	red := 0
	for x := cardTextX; x < cardTextX+200; x++ {
		c := rgbaAt(t, img, x, cardNameBaseline+14)
		if c.R > 120 && c.R > c.G+40 && c.R > c.B+40 {
			red++
		}
	}
	if red < 50 {
		t.Fatalf("underline row has only %d reddish pixels, want the bold crimson bar", red)
	}
}

// TestRankCardProgressFill pins the crimson fill at the left of the track and
// the dark empty track at max level.
func TestRankCardProgressFill(t *testing.T) {
	raw, err := renderRankCard(rankCardData{
		Username: "@vixen", DisplayName: "Vixen",
		Level: 5, Rank: 2, Progress: 110, NextLevelXP: 220, MaxLevel: 500,
	})
	if err != nil {
		t.Fatalf("renderRankCard: %v", err)
	}
	img := decodeCard(t, raw)
	// 110/220 = half of 440px = 220px, starting at x=20.
	c := rgbaAt(t, img, cardProgressX0+40, 170)
	if !(c.R > 120 && c.R > c.G+40 && c.R > c.B+40) {
		t.Fatalf("fill start = %#v, want crimson", c)
	}
	// The empty half must be the dark track, not banner.
	if got := rgbaAt(t, img, cardProgressX0+300, 170); got != trackGray {
		t.Fatalf("fill end = %#v, want the dark track", got)
	}

	// Max level: no fill at all.
	raw, err = renderRankCard(rankCardData{
		Username: "@vixen", DisplayName: "Vixen", Level: 500, Rank: 1, MaxLevel: 500,
	})
	if err != nil {
		t.Fatalf("renderRankCard(max): %v", err)
	}
	img = decodeCard(t, raw)
	if got := rgbaAt(t, img, cardProgressX0+10, 170); got != trackGray {
		t.Fatalf("max-level bar = %#v, want an empty dark track", got)
	}
	if got := rgbaAt(t, img, cardProgressX0+400, 170); got != trackGray {
		t.Fatalf("max-level bar end = %#v, want an empty dark track", got)
	}
}

// TestRankCardFallbackInitial pins that a member without a fetchable avatar
// still gets a drawn initial instead of an empty circle.
func TestRankCardFallbackInitial(t *testing.T) {
	raw, err := renderRankCard(rankCardData{
		Username: "vixen", DisplayName: "Vixen", Level: 1, Rank: 1, MaxLevel: 500,
	})
	if err != nil {
		t.Fatalf("renderRankCard: %v", err)
	}
	img := decodeCard(t, raw)
	found := false
	for y := cardAvatarY + 40; y < cardAvatarY+cardAvatarSize-20 && !found; y++ {
		for x := cardAvatarX + 30; x < cardAvatarX+cardAvatarSize-30; x++ {
			if rgbaAt(t, img, x, y) == cardWhite {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("no fallback glyph drawn in the avatar circle")
	}
}

// TestRankCardPreview writes a card for human inspection. Set
// RANK_CARD_PREVIEW=/path/to/rank.png to keep it.
func TestRankCardPreview(t *testing.T) {
	raw, err := renderRankCard(rankCardData{
		Username:    "@baronvondani",
		DisplayName: "Baron",
		Level:       70,
		Rank:        13,
		XP:          5800,
		Progress:    1100,
		NextLevelXP: 7100, // illustrative, matching the reference preview
		MaxLevel:    500,
	})
	if err != nil {
		t.Fatalf("renderRankCard: %v", err)
	}
	path := os.Getenv("RANK_CARD_PREVIEW")
	if path == "" {
		path = filepath.Join(t.TempDir(), "rank.png")
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write preview: %v", err)
	}
	t.Logf("rank card written to %s", path)
}
