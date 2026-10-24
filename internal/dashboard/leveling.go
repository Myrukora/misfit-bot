package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

// leveling.go — dashboard integration for the leveling builtin. Like the
// moderation page, the dashboard never imports the module: it resolves
// modules.LevelingAdmin through the manager.
//
//	GET    /g/<gid>/leveling                       settings + leaderboard
//	GET    /api/guilds/<gid>/leveling              config + stats + last error
//	POST   /api/guilds/<gid>/leveling              save config
//	POST   /api/guilds/<gid>/leveling/enabled      per-server on/off switch
//	POST   /api/guilds/<gid>/leveling/members/<id> set a member's XP
//	DELETE /api/guilds/<gid>/leveling/members/<id> reset a member
//	POST   /api/guilds/<gid>/leveling/sync         backfill reward roles

func (m *DashboardModule) levelingAdmin() (modules.LevelingAdmin, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("leveling")
	if !ok {
		return nil, false
	}
	adm, ok := mod.(modules.LevelingAdmin)
	return adm, ok
}

// levelingEntryView decorates a leaderboard row with the member's display name
// and avatar for the table.
type levelingEntryView struct {
	modules.LevelingEntry
	Name    string
	Avatar  string
	Voice   string
	Updated string
}

// levelingVoice renders a voice duration for the table (3h 20m).
func levelingVoice(seconds int64) string {
	if seconds <= 0 {
		return "—"
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	switch {
	case hours > 0 && minutes > 0:
		return strconv.FormatInt(hours, 10) + "h " + strconv.FormatInt(minutes, 10) + "m"
	case hours > 0:
		return strconv.FormatInt(hours, 10) + "h"
	default:
		return strconv.FormatInt(minutes, 10) + "m"
	}
}

type levelingPageData struct {
	Guild              string
	Admin              bool
	Config             modules.LevelingConfig
	ConfigError        string
	LastError          string
	Stats              modules.LevelingStats
	StatsText          string
	LastSeen           string
	Channels           []entityOpt
	AnnounceChannels   []entityOpt
	StaleAnnounce      *entityOpt
	Roles              []entityOpt
	IgnoredChannelsCSV string
	IgnoredRolesCSV    string
	StaleChannels      []string
	StaleRoles         []string
	Rows               []levelingEntryView
	FilterUser         string
}

// levelingChannelTypes are destinations an announcement can use: text,
// announcement and the three thread kinds.
var levelingChannelTypes = map[discord.ChannelType]bool{
	discord.ChannelTypeGuildText:          true,
	discord.ChannelTypeGuildNews:          true,
	discord.ChannelTypeGuildPublicThread:  true,
	discord.ChannelTypeGuildPrivateThread: true,
	discord.ChannelTypeGuildNewsThread:    true,
}

func (m *DashboardModule) renderGuildLeveling(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !m.canManageGuild(us, guildID) {
		http.Redirect(w, r, "/g/"+guildID+"/commands", http.StatusSeeOther)
		return
	}
	filterUser := util.ExtractID(strings.TrimSpace(r.URL.Query().Get("user")))
	if !validGuildID(filterUser) {
		filterUser = ""
	}

	d := m.baseData(us)
	d.Page = "gleveling"
	d.GuildID = guildID
	d.GuildName = m.guildDisplayName(guildID, us)

	page := levelingPageData{Guild: guildID, FilterUser: filterUser}
	adm, adminOK := m.levelingAdmin()
	page.Admin = adminOK
	if adminOK {
		if cfg, err := adm.GetGuildConfig(guildID); err != nil {
			page.ConfigError = err.Error()
			page.Config = levelingDefaultConfig()
		} else {
			page.Config = cfg
		}
		page.LastError = adm.LastError(guildID)
		if stats, err := adm.Stats(guildID); err == nil {
			page.Stats = stats
			page.StatsText = levelingStatsSummary(stats)
			page.LastSeen = levelingLastSeen(stats)
		} else {
			page.ConfigError = err.Error()
		}
		page.Rows = m.levelingRows(guildID, adm)
		if filterUser != "" {
			page.Rows = m.levelingFilteredRows(guildID, adm, filterUser)
		}
	} else {
		page.Config = levelingDefaultConfig()
	}

	// Channel and role pickers come from the guild cache; a module that is not
	// loaded still renders the notice without them.
	if detail, err := m.buildGuildDetail(guildID); err == nil {
		page.Channels = detail.Channels
		for _, ch := range detail.Channels {
			if levelingChannelTypes[channelTypeByName(ch.Type)] {
				page.AnnounceChannels = append(page.AnnounceChannels, ch)
			}
		}
		for _, role := range detail.Roles {
			if role.Managed || role.ID == guildID || role.Name == "@everyone" {
				continue
			}
			page.Roles = append(page.Roles, entityOpt{ID: role.ID, Name: role.Name})
		}
	}
	page.IgnoredChannelsCSV = strings.Join(page.Config.IgnoredChannels, ",")
	page.IgnoredRolesCSV = strings.Join(page.Config.IgnoredRoles, ",")
	page.StaleChannels = staleIDs(page.Config.IgnoredChannels, page.Channels)
	page.StaleRoles = staleIDs(page.Config.IgnoredRoles, page.Roles)
	page.StaleAnnounce = staleLogChannelOption(page.Config.AnnounceChannel, page.AnnounceChannels)

	d.Content = page
	m.tmpl.render(w, "rd_leveling", d)
}

// levelingDefaultConfig mirrors the module defaults so the page renders before
// the module is loaded.
func levelingDefaultConfig() modules.LevelingConfig {
	return modules.LevelingConfig{
		MessageEnabled:         true,
		MessageXPMin:           15,
		MessageXPMax:           25,
		MessageCooldownSeconds: 60,
		VoiceEnabled:           true,
		VoiceXPPerMinute:       10,
		VoiceRequireOthers:     true,
		MaxLevel:               500,
		AnnounceEnabled:        true,
		CatchupEnabled:         true,
		CatchupMaxMessages:     2000,
		CatchupMaxChannels:     50,
	}
}

// staleIDs returns configured IDs that no longer appear in the guild's current
// channels/roles, so the page can show them instead of silently dropping them.
func staleIDs(configured []string, options []entityOpt) []string {
	var out []string
	for _, id := range configured {
		found := false
		for _, opt := range options {
			if opt.ID == id {
				found = true
				break
			}
		}
		if !found {
			out = append(out, id)
		}
	}
	return out
}

// levelingRows renders the top of the leaderboard.
func (m *DashboardModule) levelingRows(guildID string, adm modules.LevelingAdmin) []levelingEntryView {
	entries, _, err := adm.Leaderboard(guildID, 0, 100)
	if err != nil {
		return nil
	}
	return m.levelingDecorate(guildID, entries)
}

// levelingFilteredRows renders exactly one member (filtered view).
func (m *DashboardModule) levelingFilteredRows(guildID string, adm modules.LevelingAdmin, userID string) []levelingEntryView {
	entry, ok, err := adm.GetEntry(guildID, userID)
	if err != nil || !ok {
		return nil
	}
	return m.levelingDecorate(guildID, []modules.LevelingEntry{entry})
}

func (m *DashboardModule) levelingDecorate(guildID string, entries []modules.LevelingEntry) []levelingEntryView {
	gid, gidErr := snowflake.Parse(guildID)
	out := make([]levelingEntryView, 0, len(entries))
	for _, entry := range entries {
		view := levelingEntryView{
			LevelingEntry: entry,
			Name:          "User " + entry.UserID,
			Voice:         levelingVoice(entry.VoiceSeconds),
			Updated:       "—",
		}
		if !entry.UpdatedAt.IsZero() {
			view.Updated = entry.UpdatedAt.UTC().Format("2006-01-02 15:04")
		}
		if gidErr == nil && m.client != nil {
			if uid, err := snowflake.Parse(entry.UserID); err == nil {
				if member, ok := m.client.Caches.Member(gid, uid); ok {
					view.Name = member.EffectiveName()
					view.Avatar = member.User.EffectiveAvatarURL(discord.WithSize(64))
				}
			}
		}
		out = append(out, view)
	}
	return out
}

func (m *DashboardModule) routeLevelingAPI(w http.ResponseWriter, r *http.Request, meth string, parts []string) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if len(parts) < 3 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	gid := parts[1]
	if !validGuildID(gid) {
		writeError(w, http.StatusBadRequest, "invalid guild id")
		return
	}
	adm, ok := m.levelingAdmin()
	if !ok {
		writeError(w, http.StatusNotFound, "leveling module not loaded")
		return
	}
	if !m.canManageGuild(us, gid) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	sub := ""
	if len(parts) >= 4 {
		sub = parts[3]
	}
	switch {
	case meth == "GET" && sub == "":
		cfg, err := adm.GetGuildConfig(gid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		stats, err := adm.Stats(gid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"config": cfg, "stats": stats, "last_error": adm.LastError(gid),
		})

	case meth == "POST" && sub == "":
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		var cfg modules.LevelingConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := m.checkChannelInGuild(gid, cfg.AnnounceChannel); err != nil {
			writeError(w, http.StatusBadRequest, "announcement channel: "+err.Error())
			return
		}
		for _, reward := range cfg.Rewards {
			if err := m.checkRoleGrant(us, gid, reward.RoleID); err != nil {
				writeError(w, http.StatusBadRequest, "reward role: "+err.Error())
				return
			}
		}
		if err := adm.SetGuildConfig(gid, cfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case meth == "POST" && sub == "enabled":
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		cfg, err := adm.GetGuildConfig(gid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if body.Enabled {
			for _, reward := range cfg.Rewards {
				if err := m.checkRoleGrant(us, gid, reward.RoleID); err != nil {
					writeError(w, http.StatusForbidden, "reward role: "+err.Error())
					return
				}
			}
		}
		cfg.Enabled = body.Enabled
		if err := adm.SetGuildConfig(gid, cfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})

	case (meth == "POST" || meth == "DELETE") && sub == "members" && len(parts) >= 5:
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		userID := parts[4]
		if !validGuildID(userID) {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		if meth == "DELETE" {
			if err := adm.ResetEntry(gid, userID); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		var body struct {
			XP int64 `json:"xp"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		entry, err := adm.SetEntryXP(gid, userID, body.XP)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entry": entry})

	case meth == "POST" && sub == "sync":
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		// A pre-existing config may predate the role-hierarchy guard; re-check
		// the stored rewards before a sync grants them.
		storedCfg, err := adm.GetGuildConfig(gid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, reward := range storedCfg.Rewards {
			if err := m.checkRoleGrant(us, gid, reward.RoleID); err != nil {
				writeError(w, http.StatusForbidden, "reward role: "+err.Error())
				return
			}
		}
		queued, err := adm.StartRewardSync(gid)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		stats, err := adm.Stats(gid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "stats": stats})

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// levelingStatsSummary is the polled status line's text.
func levelingStatsSummary(stats modules.LevelingStats) string {
	var parts []string
	if stats.CatchupRunning {
		parts = append(parts, "Catch-up running: "+strconv.FormatInt(stats.CatchupProcessed, 10)+" messages scanned, "+
			strconv.FormatInt(stats.CatchupAwarded, 10)+" awarded")
	} else if stats.CatchupTruncated {
		parts = append(parts, "Catch-up incomplete (hit a cap); it retries on the next restart.")
	}
	if stats.SyncRunning {
		parts = append(parts, "Reward sync: "+strconv.Itoa(stats.SyncProcessed)+" / "+strconv.Itoa(stats.SyncTotal))
	}
	if stats.CatchupError != "" {
		parts = append(parts, "Catch-up error: "+stats.CatchupError)
	}
	if stats.SyncError != "" {
		parts = append(parts, "Sync error: "+stats.SyncError)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// levelingLastSeen formats the watermark for the page.
func levelingLastSeen(stats modules.LevelingStats) string {
	if stats.LastSeenAt.IsZero() {
		return "never"
	}
	return stats.LastSeenAt.UTC().Format("2006-01-02 15:04") + " UTC"
}
