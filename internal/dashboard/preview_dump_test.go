package dashboard

// preview_dump_test.go — renders every page template with representative data
// and writes the HTML to /tmp/dash-preview/ so a human (or Camofox) can open
// the pages without running the bot. Skipped unless PREVIEW_DUMP=1.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

func previewData(level string) renderData {
	d := mkData(level)
	d.ShowSidebar = true
	d.ShowConfig = level == lvlOwner || level == lvlElevated
	d.ShowStaff = true
	d.ShowImageFilter = true
	d.ShowWelcome = true
	d.ShowModeration = true
	d.ShowReactionRoles = true
	d.ShowLeveling = true
	return d
}

func TestPreviewDumpRedesign(t *testing.T) {
	if os.Getenv("PREVIEW_DUMP") != "1" {
		t.Skip("set PREVIEW_DUMP=1 to dump preview HTML")
	}
	b, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	out := "/tmp/dash-preview"
	_ = os.MkdirAll(out, 0o755)

	var legalBody template.HTML
	{
		var buf bytes.Buffer
		if err := legalMarkdown.Convert([]byte(defaultTermsMD), &buf); err != nil {
			t.Fatal(err)
		}
		legalBody = template.HTML(buf.String())
	}

	welcomePreviewCfg := modules.WelcomeConfig{
		Join: modules.WelcomeEvent{
			Enabled:   true,
			ChannelID: "c1",
			Message:   "Hello {mention}, welcome to {server}! You are member #{ord:{server(members)}}.",
			Embed: modules.WelcomeEmbed{
				Enabled:     true,
				Color:       "#5865f2",
				Title:       "Welcome to {server}",
				Description: "Glad to have you here, {user}.",
				Fields:      []modules.WelcomeEmbedField{{Name: "Member", Value: "#{ord:{server(members)}}", Inline: true}},
				Footer:      "Enjoy your stay",
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
			},
		},
		Leave: modules.WelcomeEvent{
			Enabled:   true,
			ChannelID: "c2",
			Message:   "Goodbye {user}, we'll miss you.",
		},
	}
	welcomePreviewJSON, _ := json.Marshal(map[string]any{"config": welcomePreviewCfg})
	welcomePreviewChannels := []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "farewell"}}
	welcomePreviewPanels := []welcomePanel{
		{Event: "join", Label: "Welcome", Channels: welcomePreviewChannels},
		{Event: "leave", Label: "Leave", Channels: welcomePreviewChannels},
	}
	modPreviewCfg := modules.ModerationConfig{
		Enabled:          true,
		AuditChannel:     "c1",
		WarnChannel:      "c2",
		WarnNotify:       true,
		WarnExpiryDays:   30,
		BanDeleteSeconds: 60,
		Rules: []modules.ModerationRule{
			{Threshold: 2, Action: modules.ModerationActionTimeout, DurationMinutes: 60},
			{Threshold: 4, Action: modules.ModerationActionBan},
		},
	}
	modPreviewChannels := []entityOpt{{ID: "c1", Name: "mod-log"}, {ID: "c2", Name: "warn-log"}}
	modNow := time.Now().UTC()
	modFuture := modNow.Add(48 * time.Hour)
	modPreviewCases := []moderationCaseView{
		moderationCaseViewFrom(modules.ModerationCase{ID: 1, Type: modules.ModerationTypeWarn, TargetID: "111", TargetTag: "Vixen", ModeratorID: "9", ModeratorTag: "Sam", Reason: "Spamming invites", CreatedAt: modNow, ExpiresAt: &modFuture}, modNow),
		moderationCaseViewFrom(modules.ModerationCase{ID: 2, Type: modules.ModerationTypeBan, TargetID: "222", TargetTag: "Nova", ModeratorID: "9", ModeratorTag: "Sam", Reason: "Raid participation", CreatedAt: modNow.Add(-time.Hour)}, modNow),
		moderationCaseViewFrom(modules.ModerationCase{ID: 3, Type: modules.ModerationTypeTimeout, TargetID: "333", TargetTag: "Echo", ModeratorID: "99", ModeratorTag: "TestBot", Reason: "Automatic: 2 active warnings", CreatedAt: modNow.Add(-2 * time.Hour), ExpiresAt: &modFuture, Automated: true, RuleThreshold: 2, WarningID: 1}, modNow),
		moderationCaseViewFrom(modules.ModerationCase{ID: 4, Type: modules.ModerationTypeWarn, TargetID: "444", TargetTag: "Reformed", ModeratorID: "9", ModeratorTag: "Sam", Reason: "Old warning", CreatedAt: modNow.Add(-48 * time.Hour), Removed: true, RemovedByTag: "Sam", RemovedReason: "appeal accepted"}, modNow),
	}
	rrPreviewPanel := modules.ReactionPanel{
		ID:              1,
		Name:            "Pick roles",
		Enabled:         true,
		ChannelID:       "c1",
		MessageID:       "500",
		PostedChannelID: "c1",
		PostedEmojis:    []string{"👍", "wave:123"},
		RemoveOnUnreact: true,
		Embed: modules.ReactionRoleEmbed{
			Title:        "Choose your roles",
			Description:  "React below to receive a role. Remove the reaction to drop it.",
			Color:        "#5865f2",
			Footer:       "Roles",
			ThumbnailURL: "https://cdn.example/icon.png",
		},
		Mappings: []modules.ReactionRoleMapping{
			{Emoji: "👍", RoleID: "r2"},
			{Emoji: "wave:123", RoleID: "r3"},
		},
	}
	rrPreviewChannels := []entityOpt{{ID: "c1", Name: "roles"}, {ID: "c2", Name: "general"}}
	rrPreviewRoles := []roleOpt{{ID: "r2", Name: "Verified"}, {ID: "r3", Name: "Artist"}}
	rrPreviewEmojis := []emojiOpt{{Key: "wave:123", Label: ":wave:"}, {Key: "🔥", Label: "🔥"}}
	rrPreviewPanels := []rrPanelView{{ID: 1, Name: "Pick roles", Enabled: true, Posted: true, Mappings: 2, Channel: "roles", Active: true}}
	auditPreviewNow := time.Now().UTC()
	auditPreviewEntries := []auditEntry{
		{Time: auditPreviewNow, Event: "page", Success: true, UserID: "9", Username: "Sam", Level: "owner", IP: "203.0.113.10", Action: "GET /audit", Detail: "Dashboard audit", Status: 200},
		{Time: auditPreviewNow.Add(-2 * time.Minute), Event: "mutation", Success: true, UserID: "9", Username: "Sam", Level: "owner", IP: "203.0.113.10", Action: "POST /api/settings/core", Detail: "Core settings update", Status: 200},
		{Time: auditPreviewNow.Add(-5 * time.Minute), Event: "page", Success: true, UserID: "9", Username: "Sam", Level: "owner", IP: "203.0.113.10", Action: "GET /tos", Detail: "Terms of Service", Status: 200},
		{Time: auditPreviewNow.Add(-9 * time.Minute), Event: "login", Success: true, UserID: "9", Username: "Sam", Level: "owner", IP: "203.0.113.10", Action: "Discord OAuth", Detail: "signed in", Status: 303},
		{Time: auditPreviewNow.Add(-12 * time.Minute), Event: "page", Success: true, IP: "198.51.100.7", Action: "GET /policy", Detail: "Privacy Policy", Status: 200},
		{Time: auditPreviewNow.Add(-20 * time.Minute), Event: "login", Success: false, UserID: "77", Username: "intruder", IP: "198.51.100.8", Action: "Discord OAuth", Detail: "no shared server", Status: 403},
	}
	pages := []struct {
		name    string
		page    string // renderData.Page (what the real handler sets)
		tpl     string // template to render (rd_*)
		content any
		scoped  bool
	}{
		{"login.html", "", "rd_login", nil, false},
		{"legal.html", "", "rd_legal", map[string]any{
			"title": "Terms of Service",
			"body":  legalBody,
		}, false},
		{"welcome.html", "gwelcome", "rd_welcome", map[string]any{
			"guild":      "1",
			"admin":      true,
			"config":     welcomePreviewCfg,
			"configJSON": template.JS(string(welcomePreviewJSON)),
			"panels":     welcomePreviewPanels,
			"channels":   welcomePreviewChannels,
		}, true},
		{"moderation.html", "gmoderation", "rd_moderation", map[string]any{
			"guild":      "1",
			"admin":      true,
			"config":     modPreviewCfg,
			"cases":      modPreviewCases,
			"count":      len(modPreviewCases),
			"channels":   modPreviewChannels,
			"filterType": "",
			"filterUser": "",
			"lastError":  "",
		}, true},
		{"leveling.html", "gleveling", "rd_leveling", levelingPageData{
			Guild: "1",
			Admin: true,
			Config: modules.LevelingConfig{
				Enabled: true, MessageEnabled: true, MessageXPMin: 15, MessageXPMax: 25,
				MessageCooldownSeconds: 60, VoiceEnabled: true, VoiceXPPerMinute: 10,
				VoiceRequireOthers: true, MaxLevel: 500, AnnounceEnabled: true,
				AnnounceChannel: "c1", AnnounceMessage: "{user} reached **level {level}**!",
				Rewards: []modules.LevelingReward{
					{Level: 5, RoleID: "r2"}, {Level: 10, RoleID: "r3"},
				},
				IgnoredChannels: []string{"c2"}, IgnoredRoles: []string{"r1"},
				CatchupEnabled: true, CatchupMaxMessages: 2000, CatchupMaxChannels: 50,
			},
			Stats:     modules.LevelingStats{Tracked: 42, TotalXP: 128000},
			StatsText: "",
			LastSeen:  "2026-10-24 14:30 UTC",
			Channels: []entityOpt{
				{ID: "c1", Name: "general"}, {ID: "c2", Name: "staff"},
			},
			AnnounceChannels:   []entityOpt{{ID: "c1", Name: "general"}},
			Roles:              []entityOpt{{ID: "r2", Name: "Level 5"}, {ID: "r3", Name: "Level 10"}},
			IgnoredChannelsCSV: "c2",
			IgnoredRolesCSV:    "r1",
			Rows: []levelingEntryView{
				{
					LevelingEntry: modules.LevelingEntry{
						UserID: "111", XP: 12400, Level: 12, Rank: 1,
						Progress: 900, NextLevelXP: 1780, Messages: 320, VoiceSeconds: 8000,
						UpdatedAt: time.Now(),
					},
					Name: "Vixen", Voice: "2h 13m", Updated: "2026-10-24 14:20",
				},
			},
		}, true},
		{"reactionroles.html", "greactionroles", "rd_reactionroles", map[string]any{
			"guild":    "1",
			"admin":    true,
			"panels":   rrPreviewPanels,
			"panel":    rrPreviewPanel,
			"isNew":    false,
			"channels": rrPreviewChannels,
			"roles":    rrPreviewRoles,
			"emojis":   rrPreviewEmojis,
		}, true},
		{"audit.html", "audit", "rd_audit", map[string]any{
			"entries":       auditPreviewEntries,
			"total":         len(auditPreviewEntries),
			"path":          "logs/dashboard-audit.jsonl",
			"retentionDays": 30,
			"filterEvent":   "",
			"filterUser":    "",
			"limit":         100,
		}, false},
		{"setup.html", "", "rd_setup", map[string]string{
			"public_url": "https://x.com", "lan_url": "http://192.168.1.5:8080", "client_id": "111",
			"redirect_uri": "https://x.com/callback", "listen": "127.0.0.1:8080",
		}, false},
		{"servers.html", "servers", "rd_servers", map[string]any{"guilds": []guildPickerRow{
			{ID: "1", Name: "Gaming HQ", Icon: ""},
			{ID: "2", Name: "Art Server", Icon: ""},
		}}, false},
		{"overview.html", "index", "rd_overview", metricsSnapshot{Runtime: map[string]any{
			"alloc_mb": uint64(128), "sys_mb": uint64(512), "goroutines": 14, "go_version": "go1.26.4",
		}, Modules: []string{"cleanup", "tickets", "imagefilter"}}, false},
		{"config.html", "config", "rd_admin", map[string]any{
			"isOwner": true,
			"sections": []settingsSection{{Title: "Bot", Help: "Presence", Fields: []fieldRender{
				{Key: "status", Label: "Presence status", Type: "select", Value: "", Options: []string{"", "online"}},
				{Key: "log_level", Label: "Log level", Type: "select", Value: "info", Options: []string{"debug", "info", "warn", "error"}},
				{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"},
			}}},
			"variants": []string{"b32", "b16", "l14", "l14-336"}, "variant": "b32",
		}, false},
		{"permissions.html", "permissions", "rd_permissions", map[string]any{
			"elevated": []string{"111", "222"}, "owner_id": "9",
			"names": map[string]string{"111": "Helper", "222": "Mod", "9": "Sam"},
		}, false},
		{"logs.html", "logs", "rd_logs", map[string]any{
			"path": "logs/bot.log", "note": "",
			"lines": []string{`{"time":"2026-09-11T12:00:00Z","level":"INFO","msg":"bot started"}`, `{"time":"2026-09-11T12:01:00Z","level":"ERROR","msg":"rest failed"}`},
		}, false},
		{"imagefilter.html", "imagefilter", "rd_imagefilter", map[string]any{
			"admin": true, "guild": "1",
			"config":   map[string]string{"enabled": "true", "threshold": "0.95", "punishment": "mute", "mute_duration": "600", "log_channel": "c1", "delete_on_none": "false"},
			"images":   []string{"ref_a.png", "ref_b.png"},
			"status":   modules.ImageFilterStatus{Warm: true, Variant: "b32", EnabledGuilds: 1},
			"enabled":  true,
			"channels": []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}},
		}, true},
		{"commands.html", "gcommands", "rd_commands", map[string]any{
			"groups": []moduleGroup{
				{Module: "core", Categories: []catGroup{{Name: "general", Commands: []cmdView{
					{Name: "ping", Description: "pong", Category: "general", ModuleOwner: "core", Usage: "ping", Usable: true, CanExec: true},
					{Name: "ban", Description: "ban a member", Category: "moderation", ModuleOwner: "core", Usage: "ban <user> [reason]", Usable: true, CanExec: true, RequiredPerm: "Ban Members"},
					{Name: "owner-only-cmd", Description: "owner only", Category: "moderation", ModuleOwner: "core", OwnerOnly: true, Usable: false},
				}}}},
				{Module: "cleanup", Categories: []catGroup{{Name: "cleanup", Commands: []cmdView{
					{Name: "cleanup", Description: "clean messages", Category: "cleanup", ModuleOwner: "cleanup", Usage: "cleanup <n>", Usable: true, CanExec: true},
				}}}},
			},
			"guild": "1", "selectedTab": "core", "count": 4,
			"canRaw": false, "canManage": true, "level": lvlOwner,
			"guilds":   []guildOpt{{ID: "1", Name: "Gaming HQ"}},
			"channels": []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}},
			"roles":    []entityOpt{{ID: "r1", Name: "@everyone"}, {ID: "r2", Name: "Staff"}},
		}, true},
		{"tickets.html", "gtickets", "rd_tickets", ticketsPageData{
			GuildID: "1",
			TypeDetails: []modules.TypeDetail{
				{Key: "support", Label: "Support", Enabled: true, Category: "c3", PingRoles: []string{"r2"}, HelperRoles: []string{"r2"}, Color: 0x5865F2, AllowClose: true, ButtonLabel: "Support"},
				{Key: "report", Label: "Report a member", Enabled: false, AllowClose: true},
			},
			Panels: []modules.PanelSummary{{
				Name: "support_panel", TypeKey: "support", ChannelID: "c1",
				Title: "Need help?", Description: "Open a ticket below.",
				Questions: []modules.PanelQuestion{{Label: "What happened?", Style: "paragraph", Required: true}},
			}},
			Settings:     modules.TicketGuildSettings{LogChannel: "c2"},
			Channels:     []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}},
			Categories:   []entityOpt{{ID: "c3", Name: "Tickets"}},
			Roles:        []entityOpt{{ID: "r1", Name: "@everyone"}, {ID: "r2", Name: "Staff"}},
			ChannelNames: map[string]string{"c1": "general", "c2": "mod-log", "c3": "Tickets"},
			People: map[string]ticketPerson{
				"111": {Name: "Vixen"}, "222": {Name: "Nova"},
			},
			Open: []modules.TicketSummary{
				{ID: "support-0012", Type: "support", OpenerID: "111", OpenedAt: time.Now().Add(-2 * time.Hour), Preview: []modules.Media{{
					URL: "/static/discord.svg", Kind: "image", Filename: "screenshot.png",
				}}},
			},
			Closed: []modules.TicketSummary{
				{ID: "report-0011", Type: "report", OpenerID: "222", ClosedAt: time.Now().Add(-26 * time.Hour), Preview: []modules.Media{{
					URL: "/static/discord.svg", Kind: "image", Filename: "evidence.png",
				}}},
			},
		}, true},
		{"tickets-empty.html", "gtickets", "rd_tickets", ticketsPageData{GuildID: "1"}, true},
		{"tickets-error.html", "gtickets", "rd_tickets", ticketsPageData{GuildID: "", Error: "tickets module is not loaded"}, true},
		{"modules.html", "modules", "rd_modules", settingsPageData{Manage: true, MgmtRows: []moduleView{
			{Name: "cleanup", Loaded: true, Description: "Bulk message cleanup"},
			{Name: "tickets", Loaded: true},
			{Name: "hello", Loaded: false, Description: "Demo module"},
		}}, false},
		{"gmodules.html", "gmodules", "rd_modules", settingsPageData{
			GuildID: "1", GuildName: "Gaming HQ",
			ModulesView: []moduleConfigView{
				{Name: "dashboard", Label: "Update notifications", Fields: []fieldRender{
					{Key: "notify_channel", Label: "Update notification channel", Help: "Channel in this server that receives the bot's PR, commit and release embeds. Empty = this server gets no update notifications.", Type: "channel", Value: "c2", GuildScoped: true, GuildID: "1", Entities: []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}}},
				}},
				{Name: "tickets", Fields: []fieldRender{
					{Key: "transcript_channel", Label: "Transcript channel", Type: "channel", Value: "c1", GuildScoped: true, GuildID: "1", Entities: []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}}},
					{Key: "welcome_message", Label: "Welcome message", Type: "textarea", Value: "hey {user}", GuildScoped: true, GuildID: "1"},
					{Key: "max_open", Label: "Max open tickets", Type: "number", Value: "3", Min: "1", Max: "20", Step: "1", GuildScoped: true, GuildID: "1"},
				}},
			},
		}, true},
		{"transcript-closed.html", "transcript", "rd_transcript", struct {
			Ticket  *modules.Ticket
			GuildID string
		}{
			Ticket: &modules.Ticket{
				ID: "support-0011", Type: "support", GuildID: "1", OpenerID: "111", Status: "closed",
				OpenedAt: time.Now().Add(-3 * time.Hour), ClosedAt: time.Now().Add(-time.Hour),
				ArchiveAudit: []modules.TicketArchiveEvent{{
					Time: time.Now().Add(-30 * time.Minute), ActorID: "9", ActorName: "Sam",
					Action: "messages_deleted", MessageIDs: []string{"m1"}, Count: 1, Reason: "privacy request",
				}},
				Log: []modules.LogEntry{
					{MsgID: "m2", AuthorID: "9", AuthorName: "Sam", Timestamp: time.Now().Add(-90 * time.Minute), Content: "Archived conversation entry."},
				},
			},
			GuildID: "1",
		}, false},
		{"transcript.html", "transcript", "rd_transcript", struct {
			Ticket  *modules.Ticket
			GuildID string
		}{
			Ticket: &modules.Ticket{
				ID: "support-0012", Type: "support", GuildID: "1", OpenerID: "111", Status: "open",
				MessageID: "welcome",
				OpenedAt:  time.Now().Add(-2 * time.Hour), Members: []string{"333"},
				Log: []modules.LogEntry{
					{MsgID: "welcome", AuthorID: "7", AuthorName: "Test Bot 2", IsBot: true, Timestamp: time.Now().Add(-2 * time.Hour), Content: "<@111> Welcome!"},
					{MsgID: "m1", AuthorID: "111", AuthorName: "Helper", Timestamp: time.Now().Add(-2 * time.Hour), Content: "My game **crashes**.",
						Attachments: []modules.Media{{URL: "https://cdn.discordapp.com/a.png", Kind: "image", Filename: "a.png"}}},
					{MsgID: "m2", AuthorID: "9", AuthorName: "Sam", IsBot: false, Timestamp: time.Now().Add(-1 * time.Hour), Content: "Try a clean install.", Edited: true},
					{MsgID: "m3", AuthorID: "111", AuthorName: "Helper", Timestamp: time.Now().Add(-50 * time.Minute), Attachments: []modules.Media{
						{URL: "https://cdn.discordapp.com/clip.mp4", Kind: "video", Filename: "clip.mp4"},
						{URL: "https://cdn.discordapp.com/song.mp3", Kind: "audio", Filename: "song.mp3"},
						{URL: "https://cdn.discordapp.com/doc.pdf", Kind: "file", Filename: "doc.pdf"},
					}},
					{MsgID: "m4", AuthorID: "111", AuthorName: "Helper", Timestamp: time.Now(), Content: "deleted msg", Deleted: true},
					{MsgID: "system-close-support-0012", AuthorName: "Test Bot 2", IsBot: true, Timestamp: time.Now(), Content: "_Ticket closed._"},
				},
			},
			GuildID: "1",
		}, false},
	}

	for _, p := range pages {
		d := previewData(lvlOwner)
		d.Page = p.page
		if p.scoped {
			d.GuildID = "1"
			d.GuildName = "Gaming HQ"
		}
		d.Content = p.content
		var sb strings.Builder
		if err := b.render(&sb, p.tpl, d); err != nil {
			t.Errorf("render %s: %v", p.tpl, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(out, p.name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("dumped %d previews to %s", len(pages), out)
}
