package dashboard

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/misfit/bot/modules"
)

// welcome.go — dashboard integration for the welcome builtin. The dashboard
// never imports the module package; it resolves modules.WelcomeAdmin through
// the manager (same pattern as imagefilter/tickets).
//
//	GET  /g/<gid>/welcome                 editor page (staff+ for that guild)
//	GET  /api/guilds/<gid>/welcome        config + last delivery errors
//	POST /api/guilds/<gid>/welcome        save the full config
//	POST /api/guilds/<gid>/welcome/test   send a sample join/leave message

func (m *DashboardModule) welcomeAdmin() (modules.WelcomeAdmin, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("welcome")
	if !ok {
		return nil, false
	}
	adm, ok := mod.(modules.WelcomeAdmin)
	return adm, ok
}

// welcomePanel is the template view for one event tab.
type welcomePanel struct {
	Event     string // "join" / "leave"
	Label     string // "Welcome" / "Leave"
	Channels  []entityOpt
	Stale     *entityOpt // saved channel no longer in the list
	LastError string
}

// renderGuildWelcome renders the per-server welcome editor. The full config is
// embedded as JSON so app.js can hydrate the builder without a second request;
// channel options are rendered server-side (and a deleted saved channel stays
// visible as a stale option so saving other fields cannot silently clear it).
func (m *DashboardModule) renderGuildWelcome(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !m.canManageGuild(us, guildID) {
		http.Redirect(w, r, "/g/"+guildID+"/commands", http.StatusSeeOther)
		return
	}
	d := m.baseData(us)
	d.Page = "gwelcome"
	d.GuildID = guildID
	d.GuildName = m.guildDisplayName(guildID, us)

	content := map[string]any{
		"guild":    guildID,
		"admin":    false,
		"channels": []entityOpt{},
		"config":   modules.WelcomeConfig{},
	}
	var cfg modules.WelcomeConfig
	if adm, ok := m.welcomeAdmin(); ok {
		content["admin"] = true
		if loaded, err := adm.GetGuildConfig(guildID); err != nil {
			content["configError"] = err.Error()
		} else {
			cfg = loaded
		}
		content["joinError"] = adm.LastError(guildID, modules.WelcomeEventJoin)
		content["leaveError"] = adm.LastError(guildID, modules.WelcomeEventLeave)
	}
	content["config"] = cfg

	var channels []entityOpt
	if detail, err := m.buildGuildDetail(guildID); err == nil {
		channels = logChannelOptions(detail.Channels)
	}
	staleJoin := staleLogChannelOption(cfg.Join.ChannelID, channels)
	staleLeave := staleLogChannelOption(cfg.Leave.ChannelID, channels)
	content["channels"] = channels
	content["staleJoin"] = staleJoin
	content["staleLeave"] = staleLeave

	panels := make([]welcomePanel, 0, 2)
	for _, p := range []struct {
		event string
		label string
		stale *entityOpt
	}{
		{modules.WelcomeEventJoin, "Welcome", staleJoin},
		{modules.WelcomeEventLeave, "Leave", staleLeave},
	} {
		panel := welcomePanel{
			Event:    p.event,
			Label:    p.label,
			Channels: channels,
			Stale:    p.stale,
		}
		if adm, ok := m.welcomeAdmin(); ok {
			panel.LastError = adm.LastError(guildID, p.event)
		}
		panels = append(panels, panel)
	}
	content["panels"] = panels

	// encoding/json escapes <, > and &, so user-edited message text cannot
	// break out of the <script type="application/json"> block.
	payload, err := json.Marshal(map[string]any{"config": cfg})
	if err != nil {
		payload = []byte(`{"config":{}}`)
	}
	content["configJSON"] = template.JS(payload)
	d.Content = content
	m.tmpl.render(w, "rd_welcome", d)
}

// routeWelcomeAPI dispatches /api/guilds/<gid>/welcome… (guards + CSRF live
// here so the thin handlers stay unmissable).
func (m *DashboardModule) routeWelcomeAPI(w http.ResponseWriter, r *http.Request, meth string, parts []string) {
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
	adm, ok := m.welcomeAdmin()
	if !ok {
		writeError(w, http.StatusNotFound, "welcome module not loaded")
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
		writeJSON(w, http.StatusOK, map[string]any{
			"config":      cfg,
			"join_error":  adm.LastError(gid, modules.WelcomeEventJoin),
			"leave_error": adm.LastError(gid, modules.WelcomeEventLeave),
		})

	case meth == "POST" && sub == "":
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		var cfg modules.WelcomeConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := m.checkChannelInGuild(gid, cfg.Join.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, "join channel: "+err.Error())
			return
		}
		if err := m.checkChannelInGuild(gid, cfg.Leave.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, "leave channel: "+err.Error())
			return
		}
		if err := adm.SetGuildConfig(gid, cfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case meth == "POST" && sub == "test":
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		var body struct {
			Event string `json:"event"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		event := strings.ToLower(strings.TrimSpace(body.Event))
		if event != modules.WelcomeEventJoin && event != modules.WelcomeEventLeave {
			writeError(w, http.StatusBadRequest, "event must be join or leave")
			return
		}
		cfg, err := adm.GetGuildConfig(gid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ev := cfg.Join
		if event == modules.WelcomeEventLeave {
			ev = cfg.Leave
		}
		if err := m.checkChannelInGuild(gid, ev.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, "test channel: "+err.Error())
			return
		}
		if err := adm.SendTest(gid, event, us.userID.String()); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}
