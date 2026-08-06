package main

import (
	"runtime"
	"sort"
	"time"

	"github.com/custombot/bot/commands"
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
}

// metrics captures a live snapshot of bot + runtime stats.
func (m *DashboardModule) metrics() metricsSnapshot {
	s := metricsSnapshot{Runtime: map[string]any{}, Modules: []string{}}
	if m.client == nil {
		return s
	}
	s.Guilds = m.client.Caches.GuildsLen()
	s.Members = m.client.Caches.MembersAllLen()
	s.Channels = m.client.Caches.ChannelsLen()
	s.Roles = m.client.Caches.RolesAllLen()
	s.Latency = m.ctx.Bot.GetLatency()
	s.Uptime = time.Since(m.ctx.Bot.GetStartTime()).Round(time.Second).String()

	loaded := m.ctx.Bot.GetLoadedModuleNames()
	s.ModulesLoaded = len(loaded)
	s.ModulesAvail = len(m.ctx.Bot.GetAvailableModuleNames())
	s.Modules = append(s.Modules, loaded...)
	sort.Strings(s.Modules)

	// total command count = core prefix + core slash + all loaded-module commands
	s.Commands = len(commands.CoreCommands) + len(commands.CoreSlashCommands) + len(m.ctx.Bot.GetAllModuleCommands())

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.Runtime["alloc_mb"] = ms.Alloc / 1024 / 1024
	s.Runtime["sys_mb"] = ms.Sys / 1024 / 1024
	s.Runtime["goroutines"] = runtime.NumGoroutine()
	s.Runtime["gc_cycles"] = ms.NumGC
	s.Runtime["uptime_seconds"] = int64(time.Since(m.ctx.Bot.GetStartTime()).Seconds())
	return s
}
