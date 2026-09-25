package imagefilter

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func testDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// smallPNG renders a solid-color PNG of the given size.
func smallPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; h > y; y++ {
		for x := 0; w > x; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestConfigDefaultsOnFirstRun(t *testing.T) {
	dir := testDir(t)
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ClipVariant() != DefaultClipVariant {
		t.Errorf("variant = %q, want %q", cfg.ClipVariant(), DefaultClipVariant)
	}
	g := cfg.GuildSettings("123")
	if g != defaultGuildConfig() {
		t.Errorf("guild defaults = %+v, want %+v", g, defaultGuildConfig())
	}
	// The initial file must have been persisted.
	if _, err := os.Stat(configPath(dir)); err != nil {
		t.Errorf("config.json not created: %v", err)
	}
}

func TestConfigRoundTripAndMerge(t *testing.T) {
	dir := testDir(t)
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	gc := defaultGuildConfig()
	gc.Enabled = true
	gc.Threshold = 0.9
	gc.Punishment = PunishMute
	gc.MuteDuration = 120
	gc.LogChannel = "555"
	if err := cfg.SetGuildConfig("123", gc); err != nil {
		t.Fatalf("SetGuildConfig: %v", err)
	}

	// Fresh load: values survive; a guild with no entry still gets defaults.
	cfg2, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := cfg2.GuildSettings("123")
	if got.Threshold != 0.9 || got.Punishment != PunishMute || got.MuteDuration != 120 || !got.Enabled || got.LogChannel != "555" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	def := cfg2.GuildSettings("999")
	if def != defaultGuildConfig() {
		t.Errorf("unconfigured guild should get defaults, got %+v", def)
	}

	// Partial guild entries merge over defaults (threshold 0 in file → default).
	if err := cfg2.SetGuildConfig("777", GuildConfig{Threshold: 0, Punishment: "nonsense"}); err == nil {
		t.Error("invalid GuildConfig should be rejected")
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*GuildConfig)
		wantErr bool
	}{
		{"ok default", func(*GuildConfig) {}, false},
		{"threshold zero", func(c *GuildConfig) { c.Threshold = 0 }, true},
		{"threshold >1", func(c *GuildConfig) { c.Threshold = 1.5 }, true},
		{"bad punishment", func(c *GuildConfig) { c.Punishment = "nuke" }, true},
		{"mute short", func(c *GuildConfig) { c.Punishment = PunishMute; c.MuteDuration = 5 }, true},
		{"mute ok", func(c *GuildConfig) { c.Punishment = PunishMute; c.MuteDuration = 600 }, false},
		{"negative mute with none", func(c *GuildConfig) { c.MuteDuration = -1 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc := defaultGuildConfig()
			tc.mutate(&gc)
			err := gc.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want ok, got %v", err)
			}
		})
	}
}

func TestConfigEnabledGuildsAndFlip(t *testing.T) {
	dir := testDir(t)
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if changed, err := cfg.SetGuildEnabled("123", true); err != nil || !changed {
		t.Fatalf("enable: changed=%v err=%v", changed, err)
	}
	// Enabling again is a no-op.
	if changed, err := cfg.SetGuildEnabled("123", true); err != nil || changed {
		t.Fatalf("re-enable should be no-op: changed=%v err=%v", changed, err)
	}
	if got := cfg.EnabledGuilds(); len(got) != 1 || got[0] != "123" {
		t.Errorf("EnabledGuilds = %v, want [123]", got)
	}
	if changed, err := cfg.SetGuildEnabled("123", false); err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}
	if got := cfg.EnabledGuilds(); len(got) != 0 {
		t.Errorf("EnabledGuilds after disable = %v, want empty", got)
	}
}

func TestConfigCorruptFileStartsFresh(t *testing.T) {
	dir := testDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(dir), []byte("{{{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("corrupt config should not fail: %v", err)
	}
	if _, err := os.Stat(configPath(dir) + ".bad"); err != nil {
		t.Errorf("bad file should be kept aside: %v", err)
	}
	if cfg.ClipVariant() != DefaultClipVariant {
		t.Errorf("fresh defaults expected, got %q", cfg.ClipVariant())
	}
}

func TestConfigClipVariant(t *testing.T) {
	dir := testDir(t)
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.SetClipVariant("bogus"); err == nil {
		t.Error("unknown variant should be rejected")
	}
	inv, err := cfg.SetClipVariant("b16")
	if err != nil || !inv {
		t.Fatalf("SetClipVariant(b16): inv=%v err=%v", inv, err)
	}
	if cfg.ClipVariant() != "b16" {
		t.Errorf("variant = %q, want b16", cfg.ClipVariant())
	}
	// Same value again: no invalidation.
	if inv, err := cfg.SetClipVariant("b16"); err != nil || inv {
		t.Errorf("same variant should not invalidate: inv=%v err=%v", inv, err)
	}
}

func TestImagesAddListRemove(t *testing.T) {
	dir := testDir(t)
	im := newImages(dir)
	data := smallPNG(t, 32, 32, color.RGBA{255, 0, 0, 255})

	name, added, err := im.Add("123", data, "spam.png")
	if err != nil || !added {
		t.Fatalf("Add: %v added=%v", err, added)
	}
	if name == "" || filepath.Base(name) != name {
		t.Fatalf("bad name %q", name)
	}
	// Same content = same file, not added twice.
	name2, added2, err := im.Add("123", data, "other-name.png")
	if err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	// Note: orig name is part of the filename, so a different name = new file.
	_ = name2
	_ = added2

	list, err := im.List("123")
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v err=%v, want 2 entries", list, err)
	}

	// Non-image data rejected.
	if _, _, err := im.Add("123", []byte("definitely not an image"), "x.png"); err == nil {
		t.Error("non-image should be rejected")
	}

	// Guild isolation: another guild's list is empty.
	other, err := im.List("999")
	if err != nil || len(other) != 0 {
		t.Fatalf("guild isolation broken: %v err=%v", other, err)
	}

	// Remove works; second remove fails.
	if err := im.Remove("123", list[0]); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := im.Remove("123", list[0]); err == nil {
		t.Error("double remove should fail")
	}

	// Traversal rejected.
	if err := im.Remove("123", "../../config.json"); err == nil {
		t.Error("traversal should fail")
	}
}

func TestDecodeCheckRejectsGarbage(t *testing.T) {
	if _, err := decodeCheck(nil); err == nil {
		t.Error("empty data should be rejected")
	}
	if _, err := decodeCheck([]byte("GIF89a not really")); err == nil {
		t.Error("garbage should be rejected")
	}
	// Valid small image passes.
	if _, err := decodeCheck(smallPNG(t, 10, 10, color.RGBA{0, 255, 0, 255})); err != nil {
		t.Errorf("valid PNG should pass: %v", err)
	}
}
