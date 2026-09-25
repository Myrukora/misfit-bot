package imagefilter

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/misfit/bot/modules"
)

// dashboard.go — the modules.ImageFilterAdmin surface. The dashboard is this
// module's ONLY configuration path (no Discord commands); everything here is
// called from dashboard HTTP handlers (staff/owner-gated there).
// WebConfigurable is deliberately NOT implemented: the image filter's config
// UI (image gallery + threshold + punishment) is bespoke, not a generic field
// schema — Phase B builds its dedicated page against ImageFilterAdmin.

var _ modules.ImageFilterAdmin = (*ImageFilterModule)(nil)

// admin retrieves the manager under load-gate; ok=false after unload.
func (m *ImageFilterModule) admin() (*manager, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.ok || m.mgr == nil {
		return nil, false
	}
	return m.mgr, true
}

// GetGuildConfig returns the effective per-guild config as string values
// (dashboard wire format).
func (m *ImageFilterModule) GetGuildConfig(guildID string) (map[string]string, error) {
	mgr, ok := m.admin()
	if !ok {
		return nil, fmt.Errorf("imagefilter not loaded")
	}
	g := mgr.cfg.GuildSettings(guildID)
	return map[string]string{
		"enabled":        strconv.FormatBool(g.Enabled),
		"threshold":      strconv.FormatFloat(g.Threshold, 'f', -1, 64),
		"punishment":     g.Punishment,
		"mute_duration":  strconv.Itoa(g.MuteDuration),
		"log_channel":    g.LogChannel,
		"delete_on_none": strconv.FormatBool(g.DeleteOnNone),
	}, nil
}

// SetGuildConfig validates and persists the per-guild config. Only the keys
// present in the map are updated (partial updates from the settings form).
func (m *ImageFilterModule) SetGuildConfig(guildID string, values map[string]string) error {
	mgr, ok := m.admin()
	if !ok {
		return fmt.Errorf("imagefilter not loaded")
	}
	g := mgr.cfg.GuildSettings(guildID)
	for k, v := range values {
		switch strings.TrimSpace(k) {
		case "threshold":
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return fmt.Errorf("threshold: %v", err)
			}
			g.Threshold = f
		case "punishment":
			g.Punishment = strings.TrimSpace(v)
		case "mute_duration":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("mute_duration: %v", err)
			}
			g.MuteDuration = n
		case "log_channel":
			g.LogChannel = strings.TrimSpace(v)
		case "delete_on_none":
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("delete_on_none: %v", err)
			}
			g.DeleteOnNone = b
		case "enabled":
			// intentionally ignored here — goes through SetGuildEnabled so
			// the model lifecycle stays consistent
		default:
			return fmt.Errorf("unknown config key %q", k)
		}
	}
	if err := g.Validate(); err != nil {
		return err
	}
	return mgr.cfg.SetGuildConfig(guildID, g)
}

// SetGuildEnabled flips the feature and drives the warm/cold lifecycle.
func (m *ImageFilterModule) SetGuildEnabled(guildID string, enabled bool) error {
	mgr, ok := m.admin()
	if !ok {
		return fmt.Errorf("imagefilter not loaded")
	}
	return mgr.SetGuildEnabled(guildID, enabled)
}

// EnabledGuilds lists guild IDs with the filter active.
func (m *ImageFilterModule) EnabledGuilds() []string {
	mgr, ok := m.admin()
	if !ok {
		return nil
	}
	return mgr.cfg.EnabledGuilds()
}

// ListImages returns the guild's blacklisted reference image filenames.
func (m *ImageFilterModule) ListImages(guildID string) ([]string, error) {
	mgr, ok := m.admin()
	if !ok {
		return nil, fmt.Errorf("imagefilter not loaded")
	}
	return mgr.imgs.List(guildID)
}

// AddImageFromBytes stores an uploaded image as a guild reference.
func (m *ImageFilterModule) AddImageFromBytes(guildID string, data []byte, filename string) (string, error) {
	mgr, ok := m.admin()
	if !ok {
		return "", fmt.Errorf("imagefilter not loaded")
	}
	name, _, err := mgr.imgs.Add(guildID, data, filename)
	if err == nil {
		mgr.InvalidateRefs(guildID)
	}
	return name, err
}

// AddImageFromURL downloads a Discord CDN image and stores it as a reference.
// The same SSRF rules as detection apply (fetchImage).
func (m *ImageFilterModule) AddImageFromURL(guildID, url string) (string, error) {
	mgr, ok := m.admin()
	if !ok {
		return "", fmt.Errorf("imagefilter not loaded")
	}
	data, err := fetchImage(url)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("url_%d.png", time.Now().Unix())
	stored, _, err := mgr.imgs.Add(guildID, data, name)
	if err == nil {
		mgr.InvalidateRefs(guildID)
	}
	return stored, err
}

// RemoveImage deletes one reference image by filename.
func (m *ImageFilterModule) RemoveImage(guildID, name string) error {
	mgr, ok := m.admin()
	if !ok {
		return fmt.Errorf("imagefilter not loaded")
	}
	if err := mgr.imgs.Remove(guildID, name); err != nil {
		return err
	}
	mgr.InvalidateRefs(guildID)
	return nil
}

// ReadImage returns one reference image's bytes (dashboard gallery).
func (m *ImageFilterModule) ReadImage(guildID, name string) ([]byte, error) {
	mgr, ok := m.admin()
	if !ok {
		return nil, fmt.Errorf("imagefilter not loaded")
	}
	return mgr.imgs.Read(guildID, name)
}

// SetVariant switches the bot-wide CLIP variant (owner action).
func (m *ImageFilterModule) SetVariant(variant string) error {
	mgr, ok := m.admin()
	if !ok {
		return fmt.Errorf("imagefilter not loaded")
	}
	if !validVariant(variant) {
		return fmt.Errorf("unknown CLIP variant %q (available: %s)", variant, strings.Join(ClipVariants, ", "))
	}
	if !ModelFilePresent(mgr.dataDir, variant) {
		return fmt.Errorf("model file for variant %q missing — run scripts/export_clip_onnx.py --variant %s", variant, variant)
	}
	return mgr.SetClipVariant(variant)
}

// Variant returns the currently configured variant.
func (m *ImageFilterModule) Variant() string {
	mgr, ok := m.admin()
	if !ok {
		return DefaultClipVariant
	}
	return mgr.Variant()
}

// Status reports the model lifecycle for the dashboard panel.
func (m *ImageFilterModule) Status() modules.ImageFilterStatus {
	mgr, ok := m.admin()
	if !ok {
		return modules.ImageFilterStatus{Available: append([]string(nil), ClipVariants...), Files: map[string]bool{}}
	}
	dataDir := mgr.dataDir
	mgr.mu.Lock()
	warm := mgr.sess != nil
	variant := mgr.sessVariant
	enabled := mgr.refcount
	mgr.mu.Unlock()

	files := map[string]bool{}
	for _, v := range ClipVariants {
		files[v] = ModelFilePresent(dataDir, v)
	}
	return modules.ImageFilterStatus{
		Warm:          warm,
		Variant:       variant,
		Available:     append([]string(nil), ClipVariants...),
		Files:         files,
		EnabledGuilds: enabled,
	}
}
