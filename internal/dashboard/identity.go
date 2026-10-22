package dashboard

import (
	"time"

	"github.com/disgoorg/disgo/discord"
)

// appNameTTL caps how often the Dev Portal application identity is re-fetched
// via REST. The gateway-cached self user is preferred anyway (see
// botIdentity), so this is only a fallback for the brief window before/without
// a gateway.
const appNameTTL = 10 * time.Minute

// botAvatarSize is the CDN size requested for the bot's avatar image. The
// dashboard shows it at 150px (login/setup card) and 34px (topbar); 512 stays
// crisp on HiDPI screens without pulling the 4096px original.
const botAvatarSize = 512

// botIdentity is the bot's live display identity resolved for the UI: the
// name set on the Discord Developer Portal (bot username / global name) and
// its avatar. The static config name (ctx.BotName) is only the last resort.
type botIdentity struct {
	Name   string
	Avatar string // may be empty when no avatar is resolvable
}

// appIdentity is the Dev Portal application identity (name + icon) as seen
// over REST. It is the fallback identity source used while the gateway
// self-user cache is still empty.
type appIdentity struct {
	Name string
	Icon string // app icon URL, empty when the application has no icon
}

// botIdentity resolves the bot's live display name + avatar with this
// priority:
//
//  1. The gateway-cached self user (disgo populates it from the READY event,
//     so it's live and costs zero network). EffectiveName() prefers the global
//     name, falling back to the username — both are set from the application
//     name on the Developer Portal.
//  2. The application identity via REST (GetCurrentApplication) — a network
//     call, so it's cached in-memory for appNameTTL and guarded against nil
//     client and transient errors (keeps the last known value).
//  3. The static bot.name from config.yml (ctx.BotName).
func (m *DashboardModule) botIdentity() botIdentity {
	if m.client != nil {
		if su, ok := m.client.Caches.SelfUser(); ok {
			return botIdentity{
				Name:   su.EffectiveName(),
				Avatar: su.EffectiveAvatarURL(discord.WithSize(botAvatarSize)),
			}
		}
		if app := m.application(); app.Name != "" {
			return botIdentity{Name: app.Name, Avatar: app.Icon}
		}
	}
	if m.bot != nil {
		return botIdentity{Name: m.botName}
	}
	return botIdentity{Name: "Bot"}
}

// application returns the Discord application (Developer Portal) identity,
// caching the REST result for appNameTTL so page renders never hammer the API.
// The attempt time is stamped on EVERY attempt (success or failure) so a
// failing/empty lookup also respects the TTL, and the lock is not held across
// the network call (concurrent renders must not serialize behind one Discord
// request).
func (m *DashboardModule) application() appIdentity {
	m.appNameMu.Lock()
	cached, at, client := m.appInfo, m.appNameAt, m.client
	m.appNameMu.Unlock()
	if time.Since(at) < appNameTTL {
		return cached
	}
	if client == nil || client.Rest == nil {
		return appIdentity{}
	}
	app, err := client.Rest.GetCurrentApplication()
	m.appNameMu.Lock()
	defer m.appNameMu.Unlock()
	// Stamp every attempt so failures also respect the TTL.
	m.appNameAt = time.Now()
	if err == nil && app.Name != "" {
		m.appInfo = appIdentity{Name: app.Name, Icon: applicationIcon(app)}
	}
	return m.appInfo
}

// applicationIcon returns the application icon URL (the square icon shown on
// the Dev Portal / OAuth consent screen), or "" when the application has none.
func applicationIcon(app *discord.Application) string {
	if app == nil {
		return ""
	}
	if url := app.IconURL(discord.WithSize(botAvatarSize)); url != nil {
		return *url
	}
	return ""
}
