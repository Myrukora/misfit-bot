package dashboard

import (
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/updater"
)

type metricsSnapshot struct {
	Guilds        int            `json:"guilds"`
	Members       int            `json:"members_cached"`
	Channels      int            `json:"channels_cached"`
	Roles         int            `json:"roles_cached"`
	Latency       string         `json:"gateway_latency"`
	Uptime        string         `json:"uptime"`
	ModulesLoaded int            `json:"modules_loaded"`
	ModulesAvail  int            `json:"modules_available"`
	Commands      int            `json:"commands"`
	Modules       []string       `json:"loaded_module_names"`
	Runtime       map[string]any `json:"runtime"`

	// Version is this build's release version (the VERSION file, stamped in at
	// build time); LatestVersion is the newest tag seen on the tracked branch.
	// UpdateAvailable is precomputed in Go so the template and the browser
	// never compare versions themselves.
	Version         string `json:"version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	// VersionLabel is the card text ("v0.1.0", or "v0.1.0 → v0.2.0" when a
	// release is pending) so the browser never repeats the formatting rules.
	VersionLabel string `json:"version_label"`
}

// metrics captures a live snapshot of bot + runtime stats.
func (m *DashboardModule) metrics() metricsSnapshot {
	s := metricsSnapshot{Runtime: map[string]any{}, Modules: []string{}}

	// Version first: it does not depend on Discord and stays useful even before
	// the gateway cache exists.
	s.Version = m.bot.GetVersion()
	if upd := m.updaterMgr(); upd != nil {
		s.LatestVersion = upd.LatestVersion()
		s.UpdateAvailable = updater.Ahead(s.Version, s.LatestVersion)
	}
	s.VersionLabel = versionLabel(s.Version, s.LatestVersion, s.UpdateAvailable)

	if m.client == nil {
		return s
	}
	s.Guilds = m.client.Caches.GuildsLen()
	s.Members = m.client.Caches.MembersAllLen()
	s.Channels = m.client.Caches.ChannelsLen()
	s.Roles = m.client.Caches.RolesAllLen()
	s.Latency = m.bot.GetLatency()
	s.Uptime = time.Since(m.bot.GetStartTime()).Round(time.Second).String()

	loaded := m.bot.GetLoadedModuleNames()
	s.ModulesLoaded = len(loaded)
	s.ModulesAvail = len(m.bot.GetAvailableModuleNames())
	s.Modules = append(s.Modules, loaded...)
	sort.Strings(s.Modules)

	// total command count = core prefix + core slash + all loaded-module commands
	s.Commands = len(commands.CoreCommands) + len(commands.CoreSlashCommands) + len(m.bot.GetAllModuleCommands())

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.Runtime["alloc_mb"] = ms.Alloc / 1024 / 1024
	s.Runtime["sys_mb"] = ms.Sys / 1024 / 1024
	s.Runtime["goroutines"] = runtime.NumGoroutine()
	s.Runtime["gc_cycles"] = ms.NumGC
	s.Runtime["go_version"] = runtime.Version()
	s.Runtime["uptime_seconds"] = int64(time.Since(m.bot.GetStartTime()).Seconds())
	return s
}

// releaseLabel canonicalises a version to "v0.1.0" using the updater's SemVer
// rules — so the dashboard and [p]info can never disagree — and reports when
// the value is not a version at all (an unstamped "dev" build, a corrupt tag).
func releaseLabel(v string) (string, bool) {
	if canonical := updater.NormalizeVersion(v); canonical != "" {
		return "v" + canonical, true
	}
	return "", false
}

// versionDisplay renders a version for the UI: a stamped release becomes
// "v0.1.0", an unstamped build says "dev", and anything else is an em-dash
// rather than a version-shaped guess.
func versionDisplay(v string) string {
	if l, ok := releaseLabel(v); ok {
		return l
	}
	switch strings.TrimSpace(v) {
	case "", "dev":
		return "dev"
	}
	return "—"
}

// versionLabel renders the Overview version card: "v0.1.0", or
// "v0.1.0 → v0.2.0" while a newer release tag is known. The pending side must
// itself be a version, so a missing/odd cached tag never renders as an arrow to
// nowhere.
func versionLabel(current, latest string, updateAvailable bool) string {
	c := versionDisplay(current)
	if !updateAvailable {
		return c
	}
	if l, ok := releaseLabel(latest); ok && l != c {
		return c + " → " + l
	}
	return c
}
