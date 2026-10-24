package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// reactionroles.go — dashboard integration for the reaction-roles builtin.
//
//	GET    /g/<gid>/reactionroles                                  panel manager
//	GET    /api/guilds/<gid>/reactionroles                         full config
//	POST   /api/guilds/<gid>/reactionroles/panels                  create
//	POST   /api/guilds/<gid>/reactionroles/panels/<id>             update
//	POST   /api/guilds/<gid>/reactionroles/panels/<id>/post        post/update message
//	DELETE /api/guilds/<gid>/reactionroles/panels/<id>             delete (+message)

func (m *DashboardModule) reactionRolesAdmin() (modules.ReactionRolesAdmin, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("reactionroles")
	if !ok {
		return nil, false
	}
	adm, ok := mod.(modules.ReactionRolesAdmin)
	return adm, ok
}

type emojiOpt struct {
	Key   string
	Label string
}

type rrPanelView struct {
	ID       int
	Name     string
	Enabled  bool
	Posted   bool
	Mappings int
	Channel  string
	Active   bool
}

func (m *DashboardModule) renderGuildReactionRoles(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !m.canManageGuild(us, guildID) {
		http.Redirect(w, r, "/g/"+guildID+"/commands", http.StatusSeeOther)
		return
	}
	d := m.baseData(us)
	d.Page = "greactionroles"
	d.GuildID = guildID
	d.GuildName = m.guildDisplayName(guildID, us)

	content := map[string]any{
		"guild":    guildID,
		"admin":    false,
		"panels":   []rrPanelView{},
		"panel":    modules.ReactionPanel{},
		"isNew":    true,
		"channels": []entityOpt{},
		"roles":    []roleOpt{},
		"emojis":   []emojiOpt{},
	}
	var cfg modules.ReactionRolesConfig
	adm, adminOK := m.reactionRolesAdmin()
	content["admin"] = adminOK
	if adminOK {
		if loaded, err := adm.GetGuildConfig(guildID); err != nil {
			content["configError"] = err.Error()
		} else {
			cfg = loaded
		}
		content["lastError"] = adm.LastError(guildID)
	}

	var channels []entityOpt
	var roles []roleOpt
	if detail, err := m.buildGuildDetail(guildID); err == nil {
		channels = moderationChannelOptions(detail.Channels)
		for _, role := range detail.Roles {
			// @everyone (role id == guild id) and integration-managed roles
			// cannot be assigned by the bot; keep them out of the picker.
			if role.ID == guildID || role.Managed {
				continue
			}
			roles = append(roles, role)
		}
	}
	content["channels"] = channels
	content["roles"] = roles

	channelName := map[string]string{}
	for _, ch := range channels {
		channelName[ch.ID] = ch.Name
	}

	selectedID := 0
	raw := strings.TrimSpace(r.URL.Query().Get("panel"))
	if raw != "" && raw != "new" {
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			selectedID = id
		}
	}
	panel := modules.ReactionPanel{
		Enabled:         false,
		RemoveOnUnreact: true,
		Embed:           modules.ReactionRoleEmbed{Color: "#5865f2"},
	}
	found := false
	for _, p := range cfg.Panels {
		if p.ID == selectedID {
			panel = p
			found = true
			break
		}
	}
	if !found && raw != "new" && len(cfg.Panels) > 0 {
		panel = cfg.Panels[0]
		found = true
	}
	if !found {
		panel.ID = 0
	}
	content["panel"] = panel
	content["isNew"] = !found
	content["selectedID"] = panel.ID
	if !found {
		content["selectedID"] = 0
	}
	content["staleChannel"] = staleLogChannelOption(panel.ChannelID, channels)

	views := make([]rrPanelView, 0, len(cfg.Panels))
	for _, p := range cfg.Panels {
		ch := channelName[p.ChannelID]
		if ch == "" {
			ch = p.ChannelID
		}
		views = append(views, rrPanelView{
			ID: p.ID, Name: p.Name, Enabled: p.Enabled, Posted: p.MessageID != "",
			Mappings: len(p.Mappings), Channel: ch, Active: found && p.ID == panel.ID,
		})
	}
	content["panels"] = views

	// Server custom emojis for the picker (Unicode emojis are typed/pasted).
	var emojiOpts []emojiOpt
	if m.client != nil {
		if gid, err := snowflake.Parse(guildID); err == nil {
			for emoji := range m.client.Caches.Emojis(gid) {
				if emoji.ID == 0 || emoji.Name == "" {
					continue
				}
				emojiOpts = append(emojiOpts, emojiOpt{Key: emoji.Reaction(), Label: ":" + emoji.Name + ":"})
			}
		}
	}
	sort.Slice(emojiOpts, func(i, j int) bool { return emojiOpts[i].Label < emojiOpts[j].Label })
	content["emojis"] = emojiOpts

	d.Content = content
	m.tmpl.render(w, "rd_reactionroles", d)
}

func (m *DashboardModule) routeReactionRolesAPI(w http.ResponseWriter, r *http.Request, meth string, parts []string) {
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
	adm, ok := m.reactionRolesAdmin()
	if !ok {
		writeError(w, http.StatusNotFound, "reaction roles module not loaded")
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

	case meth == "POST" && sub == "panels" && len(parts) == 4:
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		var panel modules.ReactionPanel
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&panel); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := m.checkChannelInGuild(gid, panel.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, "panel channel: "+err.Error())
			return
		}
		for _, mapping := range panel.Mappings {
			if err := m.checkRoleGrant(us, gid, mapping.RoleID); err != nil {
				writeError(w, http.StatusBadRequest, "mapping role: "+err.Error())
				return
			}
		}
		created, err := adm.CreatePanel(gid, panel)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"panel": created})

	case meth == "POST" && sub == "panels" && len(parts) >= 5:
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		panelID, err := strconv.Atoi(parts[4])
		if err != nil || panelID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid panel id")
			return
		}
		if len(parts) == 6 && parts[5] == "post" {
			cfg, err := adm.GetGuildConfig(gid)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			found := false
			for _, p := range cfg.Panels {
				if p.ID != panelID {
					continue
				}
				found = true
				if err := m.checkChannelInGuild(gid, p.ChannelID); err != nil {
					writeError(w, http.StatusBadRequest, "panel channel: "+err.Error())
					return
				}
				for _, mapping := range p.Mappings {
					if err := m.checkRoleGrant(us, gid, mapping.RoleID); err != nil {
						writeError(w, http.StatusBadRequest, "mapping role: "+err.Error())
						return
					}
				}
				break
			}
			if !found {
				writeError(w, http.StatusNotFound, "panel not found")
				return
			}
			if err := adm.PostPanel(gid, panelID); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if len(parts) != 5 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		var panel modules.ReactionPanel
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&panel); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := m.checkChannelInGuild(gid, panel.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, "panel channel: "+err.Error())
			return
		}
		for _, mapping := range panel.Mappings {
			if err := m.checkRoleGrant(us, gid, mapping.RoleID); err != nil {
				writeError(w, http.StatusBadRequest, "mapping role: "+err.Error())
				return
			}
		}
		panel.ID = panelID
		updated, err := adm.UpdatePanel(gid, panel)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"panel": updated})

	case meth == "DELETE" && sub == "panels" && len(parts) == 5:
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		panelID, err := strconv.Atoi(parts[4])
		if err != nil || panelID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid panel id")
			return
		}
		deleteMessage := r.URL.Query().Get("delete_message") != "0"
		if err := adm.DeletePanel(gid, panelID, deleteMessage); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}
