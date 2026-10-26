package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/gorilla/websocket"

	"github.com/misfit/bot/config"
	"github.com/misfit/bot/embed"
)

// TestLegalURLUsesDashboardBase pins the deprecated-config replacement: with
// the file-only dashboard public URL set, /info links resolve to the canonical
// dashboard /tos and /policy pages.
func TestLegalURLUsesDashboardBase(t *testing.T) {
	oldCfg, oldClient := Cfg, Client
	defer func() { Cfg, Client = oldCfg, oldClient }()
	Cfg = &config.Config{Dashboard: config.DashboardCoreConfig{PublicURL: "https://dash.example/"}}
	Client = nil

	b := &botAdapter{}
	if got := b.GetToS(); got != "https://dash.example/tos" {
		t.Fatalf("GetToS = %q, want dashboard /tos", got)
	}
	if got := b.GetPrivacy(); got != "https://dash.example/policy" {
		t.Fatalf("GetPrivacy = %q, want dashboard /policy", got)
	}
}

// TestGatewayIntents pins the identify mask: presence is never requested, the
// non-privileged intents are always on, and the privileged bits follow config.
func TestGatewayIntents(t *testing.T) {
	all := gatewayIntents(&config.Config{Intents: config.IntentsConfig{MessageContent: true, GuildMembers: true}})
	if !all.Has(gateway.IntentGuildMembers, gateway.IntentMessageContent) {
		t.Fatal("configured privileged intents missing")
	}
	if all.Has(gateway.IntentGuildPresences) {
		t.Fatal("presence must never be requested")
	}
	for _, want := range []gateway.Intents{
		gateway.IntentGuilds,
		gateway.IntentGuildMessages,
		gateway.IntentDirectMessages,
		gateway.IntentGuildVoiceStates,
		gateway.IntentGuildModeration,
		gateway.IntentGuildMessageReactions,
	} {
		if !all.Has(want) {
			t.Errorf("base intent %v missing from the mask", want)
		}
	}

	none := gatewayIntents(&config.Config{})
	if none.Has(gateway.IntentGuildMembers) || none.Has(gateway.IntentMessageContent) {
		t.Error("privileged intents requested despite being disabled")
	}
	if !none.Has(gateway.IntentGuilds) {
		t.Error("base intents must stay on without privileged ones")
	}
	if gatewayIntents(nil) != none {
		t.Error("nil config should produce the same mask as an empty one")
	}
}

// TestIntentMaskWithoutPrivileged pins the 4014 fallback arithmetic.
func TestIntentMaskWithoutPrivileged(t *testing.T) {
	mask := gateway.IntentGuilds | gateway.IntentGuildMembers | gateway.IntentMessageContent
	reduced, ok := intentMaskWithoutPrivileged(mask)
	if !ok {
		t.Fatal("expected the privileged bits to be dropped")
	}
	if reduced.Has(gateway.IntentGuildMembers) || reduced.Has(gateway.IntentMessageContent) {
		t.Error("privileged intents survived the reduction")
	}
	if !reduced.Has(gateway.IntentGuilds) {
		t.Error("non-privileged intents must survive the reduction")
	}
	if _, ok := intentMaskWithoutPrivileged(gateway.IntentGuilds); ok {
		t.Error("there is nothing privileged to drop in a base-only mask")
	}
}

// TestIsDisallowedIntentClose pins the 4014 detection (wrapped, other codes and
// non-websocket errors).
func TestIsDisallowedIntentClose(t *testing.T) {
	wrapped := fmt.Errorf("open gateway: %w", &websocket.CloseError{Code: 4014})
	if !isDisallowedIntentClose(wrapped) {
		t.Error("wrapped 4014 not detected")
	}
	if isDisallowedIntentClose(&websocket.CloseError{Code: 4004}) {
		t.Error("a non-4014 close must not trigger the fallback")
	}
	if isDisallowedIntentClose(errors.New("network down")) {
		t.Error("a non-websocket error must not trigger the fallback")
	}
	if isDisallowedIntentClose(nil) {
		t.Error("nil must not trigger the fallback")
	}
}

// TestAutoDeleteTiming locks in the simplified message-disappearance rule:
// the bot auto-deletes ONLY error-colored embeds — and it does so after 7s so
// the user can read them. Every other response (success, info, warning,
// usage/reference listings, status reports, plain text) STAYS on screen
// permanently. There is no per-command "preserve" list and no opt-in hook:
// the dispatcher decides purely from the first embed's color (red ⇒ error).
func TestAutoDeleteTiming(t *testing.T) {
	if errorAutoDeleteDelay != 7*time.Second {
		t.Errorf("errorAutoDeleteDelay = %v, want 7s", errorAutoDeleteDelay)
	}

	// Error embed (red) → detected as an error response → auto-deletes at 7s.
	errEmbed := embed.Error("❌ Error", "Command failed")
	if !isErrorResponse([]discord.Embed{errEmbed}) {
		t.Error("embed.Error() should be detected as an error response (red)")
	}

	// Non-error embeds → NOT errors → stay on screen (never auto-delete).
	for name, e := range map[string]discord.Embed{
		"success": embed.Success("✅", "ok"),
		"info":    embed.Info("ℹ️", "ok"),
		"warning": embed.Warning("⚠️", "ok"),
		"usage":   embed.New().WithTitle("📘 Usage").WithColor(embed.ColorInfo),
		"status":  embed.New().WithTitle("📊 Status").WithColor(embed.ColorDefault),
	} {
		if isErrorResponse([]discord.Embed{e}) {
			t.Errorf("%s embed flagged as error — only red (embed.Error) should be", name)
		}
	}

	// Empty response: must not be treated as an error (no color to read),
	// so it stays on screen too.
	if isErrorResponse(nil) {
		t.Error("nil embeds flagged as error")
	}
	if isErrorResponse([]discord.Embed{}) {
		t.Error("empty embed slice flagged as error")
	}
}
