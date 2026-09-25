package modules

// ImageFilterAdmin is the OPTIONAL interface the image spam filter module
// implements so the dashboard can manage it without importing the module's
// package (same resolution pattern as TicketProvider):
//
//	if mod, ok := manager.Get("imagefilter"); ok {
//	    if adm, ok := mod.(ImageFilterAdmin); ok { ... }
//	}
//
// If either step fails, the dashboard shows the "module not installed" stub.
// All mutating calls are dashboard-driven; the module has no Discord commands.
type ImageFilterAdmin interface {
	// GetGuildConfig returns the effective per-guild config (defaults merged).
	GetGuildConfig(guildID string) (map[string]string, error)
	// SetGuildConfig validates and persists the per-guild config. Accepted
	// keys: threshold, punishment, mute_duration, log_channel, delete_on_none.
	SetGuildConfig(guildID string, values map[string]string) error
	// SetGuildEnabled flips the feature for a guild and drives the model
	// warm/cold lifecycle. Returns after the transition completes.
	SetGuildEnabled(guildID string, enabled bool) error
	// EnabledGuilds lists guild IDs with the filter active.
	EnabledGuilds() []string

	// ListImages returns the guild's blacklisted reference image filenames.
	ListImages(guildID string) ([]string, error)
	// AddImageFromBytes stores an uploaded image (dashboard multipart).
	AddImageFromBytes(guildID string, data []byte, filename string) (string, error)
	// AddImageFromURL downloads a Discord CDN image and stores it.
	AddImageFromURL(guildID, url string) (string, error)
	// RemoveImage deletes one reference image by filename.
	RemoveImage(guildID, name string) error
	// ReadImage returns one reference image's bytes (dashboard gallery
	// thumbnails). Errors when the file doesn't exist.
	ReadImage(guildID, name string) ([]byte, error)

	// Status reports the model lifecycle for the dashboard panel:
	// warm (loaded + RAM held), variant in use, available variants, and
	// whether each variant's model file exists on disk.
	Status() ImageFilterStatus
	// SetVariant switches the bot-wide CLIP variant (owner action). Closes
	// the warm session, wipes cached embeddings (different vector space) and
	// reloads when any guild is enabled. Errors when the variant is unknown
	// or its model file is missing.
	SetVariant(variant string) error
	// Variant returns the currently configured variant.
	Variant() string
}

// ImageFilterStatus is the snapshot behind the dashboard's model panel.
type ImageFilterStatus struct {
	Warm          bool            `json:"warm"`
	Variant       string          `json:"variant"`
	Available     []string        `json:"available"` // selectable variants
	Files         map[string]bool `json:"files"`     // variant → .onnx present
	EnabledGuilds int             `json:"enabled_guilds"`
}
