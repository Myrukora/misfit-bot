package main

import (
	"time"
)

// appNameTTL caps how often the Dev Portal application name is re-fetched via
// REST. The gateway-cached self user is preferred anyway (see botIdentity), so
// this is only a fallback for the brief window before/without a gateway.
const appNameTTL = 10 * time.Minute

// botIdentity is the bot's live display identity resolved for the UI: the
// name set on the Discord Developer Portal (bot username / global name) and
// its avatar. The static config name (ctx.BotName) is only the last resort.
type botIdentity struct {
	Name   string
	Avatar string // may be empty when no avatar is resolvable
}

// botIdentity resolves the bot's live display name + avatar with this
// priority:
//
//  1. The gateway-cached self user (disgo populates it from the READY event,
//     so it's live and costs zero network). EffectiveName() prefers the global
//     name, falling back to the username — both are set from the application
//     name on the Developer Portal.
//  2. The application name via REST (GetCurrentApplication) — a network call,
//     so it's cached in-memory for appNameTTL and guarded against nil client
//     and transient errors (keeps the last known value).
//  3. The static bot.name from config.yml (ctx.BotName).
func (m *DashboardModule) botIdentity() botIdentity {
	if m.client != nil {
		if su, ok := m.client.Caches.SelfUser(); ok {
			return botIdentity{
				Name:   su.EffectiveName(),
				Avatar: su.EffectiveAvatarURL(),
			}
		}
		if name := m.applicationName(); name != "" {
			return botIdentity{Name: name}
		}
	}
	if m.ctx != nil {
		return botIdentity{Name: m.ctx.BotName}
	}
	return botIdentity{Name: "Bot"}
}

// applicationName returns the Discord application (Developer Portal) name,
// caching the REST result for appNameTTL so page renders never hammer the API.
// The attempt time is stamped on EVERY attempt (success or failure) so a
// failing/empty lookup also respects the TTL, and the lock is not held across
// the network call (concurrent renders must not serialize behind one Discord
// request).
func (m *DashboardModule) applicationName() string {
	m.appNameMu.Lock()
	cached, at, client := m.appName, m.appNameAt, m.client
	m.appNameMu.Unlock()
	if time.Since(at) < appNameTTL {
		return cached
	}
	if client == nil {
		return ""
	}
	app, err := client.Rest.GetCurrentApplication()
	m.appNameMu.Lock()
	defer m.appNameMu.Unlock()
	// Stamp every attempt so failures also respect the TTL.
	m.appNameAt = time.Now()
	if err == nil && app.Name != "" {
		m.appName = app.Name
	}
	return m.appName
}
