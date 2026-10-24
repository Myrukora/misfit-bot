package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

// moderation.go — dashboard integration for the moderation builtin. Like the
// welcome page, the dashboard never imports the module: it resolves
// modules.ModerationAdmin through the manager.
//
//	GET    /g/<gid>/moderation                     settings + audit browser
//	GET    /api/guilds/<gid>/moderation            config + last delivery error
//	POST   /api/guilds/<gid>/moderation            save config + rules
//	GET    /api/guilds/<gid>/moderation/cases      filtered audit cases
//	DELETE /api/guilds/<gid>/moderation/cases/<id> remove one warning

func (m *DashboardModule) moderationAdmin() (modules.ModerationAdmin, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("moderation")
	if !ok {
		return nil, false
	}
	adm, ok := mod.(modules.ModerationAdmin)
	return adm, ok
}

// moderationChannelTypes are destinations a moderation post can use: text,
// announcement and the three thread kinds. Forums are excluded (a forum
// requires creating a thread first); voice/category cannot receive messages.
var moderationChannelTypes = map[discord.ChannelType]bool{
	discord.ChannelTypeGuildText:          true,
	discord.ChannelTypeGuildNews:          true,
	discord.ChannelTypeGuildPublicThread:  true,
	discord.ChannelTypeGuildPrivateThread: true,
	discord.ChannelTypeGuildNewsThread:    true,
}

func moderationChannelOptions(chs []entityOpt) []entityOpt {
	out := make([]entityOpt, 0, len(chs))
	for _, ch := range chs {
		if moderationChannelTypes[channelTypeByName(ch.Type)] {
			out = append(out, ch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type moderationCaseView struct {
	ID          int
	Type        string
	TypeLabel   string
	Target      string
	TargetID    string
	Moderator   string
	ModeratorID string
	Reason      string
	Created     string
	Status      string
	CanRemove   bool
	Automated   bool
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func moderationCaseViewFrom(c modules.ModerationCase, now time.Time) moderationCaseView {
	label := c.Type
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	if c.Type == modules.ModerationTypeWarn {
		label = "Warning"
	}
	moderator := c.ModeratorTag
	if c.Automated {
		moderator = "Automatic · " + c.ModeratorTag
	}
	status := "Logged"
	canRemove := false
	switch c.Type {
	case modules.ModerationTypeWarn:
		switch {
		case c.Removed:
			status = "Removed"
		case c.ExpiresAt != nil && !c.ExpiresAt.After(now):
			status = "Expired"
		default:
			status = "Active"
			canRemove = true
		}
	default:
		if c.ExpiresAt != nil && c.ExpiresAt.After(now) {
			status = "Until " + c.ExpiresAt.UTC().Format("2006-01-02 15:04")
		}
	}
	return moderationCaseView{
		ID:          c.ID,
		Type:        c.Type,
		TypeLabel:   label,
		Target:      c.TargetTag,
		TargetID:    c.TargetID,
		Moderator:   moderator,
		ModeratorID: c.ModeratorID,
		Reason:      clip(reasonOrDash(c.Reason), 110),
		Created:     c.CreatedAt.UTC().Format("2006-01-02 15:04"),
		Status:      status,
		CanRemove:   canRemove,
		Automated:   c.Automated,
	}
}

func reasonOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func (m *DashboardModule) renderGuildModeration(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !m.canManageGuild(us, guildID) {
		http.Redirect(w, r, "/g/"+guildID+"/commands", http.StatusSeeOther)
		return
	}
	filterType := strings.TrimSpace(r.URL.Query().Get("type"))
	switch filterType {
	case modules.ModerationTypeWarn, modules.ModerationTypeBan, modules.ModerationTypeKick,
		modules.ModerationTypeTimeout, modules.ModerationTypeUnban:
	default:
		filterType = ""
	}
	filterUser := strings.TrimSpace(r.URL.Query().Get("user"))
	filterUser = util.ExtractID(filterUser)
	if !validGuildID(filterUser) {
		filterUser = ""
	}

	d := m.baseData(us)
	d.Page = "gmoderation"
	d.GuildID = guildID
	d.GuildName = m.guildDisplayName(guildID, us)

	content := map[string]any{
		"guild":      guildID,
		"admin":      false,
		"config":     modules.ModerationConfig{},
		"cases":      []moderationCaseView{},
		"channels":   []entityOpt{},
		"filterType": filterType,
		"filterUser": filterUser,
	}
	var cfg modules.ModerationConfig
	adm, adminOK := m.moderationAdmin()
	if adminOK {
		content["admin"] = true
		if loaded, err := adm.GetGuildConfig(guildID); err != nil {
			content["configError"] = err.Error()
		} else {
			cfg = loaded
		}
		content["lastError"] = adm.LastError(guildID)
		if cases, err := adm.ListCases(guildID, modules.ModerationCaseFilter{
			Type: filterType, UserID: filterUser, Limit: 100,
		}); err != nil {
			content["casesError"] = err.Error()
		} else {
			now := time.Now().UTC()
			views := make([]moderationCaseView, 0, len(cases))
			for _, c := range cases {
				views = append(views, moderationCaseViewFrom(c, now))
			}
			content["cases"] = views
			content["count"] = len(views)
		}
	}
	content["config"] = cfg

	var channels []entityOpt
	if detail, err := m.buildGuildDetail(guildID); err == nil {
		channels = moderationChannelOptions(detail.Channels)
	}
	content["channels"] = channels
	content["staleAudit"] = staleLogChannelOption(cfg.AuditChannel, channels)
	content["staleWarn"] = staleLogChannelOption(cfg.WarnChannel, channels)

	d.Content = content
	m.tmpl.render(w, "rd_moderation", d)
}

func (m *DashboardModule) routeModerationAPI(w http.ResponseWriter, r *http.Request, meth string, parts []string) {
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
	adm, ok := m.moderationAdmin()
	if !ok {
		writeError(w, http.StatusNotFound, "moderation module not loaded")
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
		writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "last_error": adm.LastError(gid)})

	case meth == "POST" && sub == "":
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		var cfg modules.ModerationConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := adm.SetGuildConfig(gid, cfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case meth == "GET" && sub == "cases":
		q := r.URL.Query()
		typ := strings.TrimSpace(q.Get("type"))
		userID := util.ExtractID(strings.TrimSpace(q.Get("user")))
		if !validGuildID(userID) {
			userID = ""
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		cases, err := adm.ListCases(gid, modules.ModerationCaseFilter{Type: typ, UserID: userID, Limit: limit})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cases": cases})

	case meth == "DELETE" && sub == "cases" && len(parts) == 5:
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		caseID, err := strconv.Atoi(parts[4])
		if err != nil || caseID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid case number")
			return
		}
		if err := adm.RemoveWarning(gid, caseID, us.userID.String(), us.username, "Removed from dashboard"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}
