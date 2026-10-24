package welcome

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// WelcomeModule sends configurable welcome (member join) and leave (member
// leave) messages. Every setting lives on the dashboard; the module itself has
// no Discord commands.
type WelcomeModule struct {
	ctx   *modules.Context
	store *store

	mu      sync.Mutex
	lastErr map[string]string // "guild|join" → last delivery error
}

// New constructs the module (registered as a compiled-in builtin).
func New() modules.Module { return &WelcomeModule{} }

func (m *WelcomeModule) Name() string    { return "welcome" }
func (m *WelcomeModule) Version() string { return "1.0.0" }
func (m *WelcomeModule) Description() string {
	return "Welcome and leave messages with a dashboard embed builder"
}
func (m *WelcomeModule) Author() string { return "misfit" }

func (m *WelcomeModule) OnLoad(ctx *modules.Context) error {
	m.ctx = ctx
	m.lastErr = map[string]string{}
	st, err := openStore(filepath.Join(ctx.DataDir, "guilds"))
	if err != nil {
		return err
	}
	m.store = st
	if ctx.Events != nil {
		ctx.Events.AddGuildMemberJoin(m.onJoin)
		ctx.Events.AddGuildMemberLeave(m.onLeave)
	}
	ctx.Logger.Info("Welcome module loaded")
	return nil
}

func (m *WelcomeModule) OnUnload() error { return nil }

func (m *WelcomeModule) SlashCommands() []commands.SlashCommand { return nil }

func (m *WelcomeModule) Dependencies() []string { return nil }

// OnDisable implements modules.Disablable: hooks are removed by the manager,
// nothing else needs tearing down.
func (m *WelcomeModule) OnDisable() error { return nil }

func (m *WelcomeModule) onJoin(e *events.GuildMemberJoin) {
	if m.ctx == nil || m.store == nil {
		return
	}
	guildID := e.GuildID.String()
	cfg, err := m.store.Get(guildID)
	if err != nil {
		m.ctx.Logger.Error("Welcome: %v", err)
		return
	}
	if !cfg.Join.Enabled {
		return
	}
	d := m.templateData(e.GuildID, e.Member.User, &e.Member)
	m.deliver(guildID, modules.WelcomeEventJoin, cfg.Join, d)
}

func (m *WelcomeModule) onLeave(e *events.GuildMemberLeave) {
	if m.ctx == nil || m.store == nil {
		return
	}
	guildID := e.GuildID.String()
	cfg, err := m.store.Get(guildID)
	if err != nil {
		m.ctx.Logger.Error("Welcome: %v", err)
		return
	}
	if !cfg.Leave.Enabled {
		return
	}
	var member *discord.Member
	if e.Member.User.ID != 0 {
		member = &e.Member
	}
	d := m.templateData(e.GuildID, e.User, member)
	m.deliver(guildID, modules.WelcomeEventLeave, cfg.Leave, d)
}

// templateData builds the variable snapshot for one event. Member may be nil
// (a leave event for a member no longer in cache).
func (m *WelcomeModule) templateData(guildID snowflake.ID, user discord.User, member *discord.Member) templateData {
	d := templateData{User: user, Member: member, GuildID: guildID}
	if member != nil && member.JoinedAt != nil {
		d.JoinedAt = *member.JoinedAt
	}
	if g := m.ctx.Bot.GetCachedGuild(guildID.String()); g != nil {
		d.GuildName = g.Name
		d.MemberCount = g.MemberCount
		if icon := g.IconURL(discord.WithSize(256)); icon != nil {
			d.GuildIcon = *icon
		}
		return d
	}
	// Cache miss: fetch counts from Discord (rare — guilds are cached on
	// startup). A failure leaves the guild variables empty rather than
	// dropping the welcome message.
	if m.ctx.Rest != nil {
		if g, err := m.ctx.Rest.GetGuild(guildID, true); err == nil && g != nil {
			d.GuildName = g.Name
			d.MemberCount = g.MemberCount
			if icon := g.IconURL(discord.WithSize(256)); icon != nil {
				d.GuildIcon = *icon
			}
		}
	}
	return d
}

// deliver renders and posts one event's message, recording the outcome for the
// dashboard's "last error" note.
func (m *WelcomeModule) deliver(guildID, event string, ev modules.WelcomeEvent, d templateData) {
	chID, err := snowflake.Parse(ev.ChannelID)
	if err != nil {
		m.setLastError(guildID, event, "invalid channel id")
		m.ctx.Logger.Error("Welcome: invalid %s channel for guild %s: %v", event, guildID, err)
		return
	}
	msg, warning := buildMessage(ev, d)
	if messageEmpty(msg) {
		return
	}
	sent, err := m.ctx.Rest.CreateMessage(chID, msg)
	if err != nil {
		m.setLastError(guildID, event, err.Error())
		m.ctx.Logger.Error("Welcome: failed to post %s message to %s (guild %s): %v", event, chID, guildID, err)
		return
	}
	if warning != "" {
		m.setLastError(guildID, event, warning)
		m.ctx.Logger.Warn("Welcome: %s message for guild %s: %s", event, guildID, warning)
	} else {
		m.setLastError(guildID, event, "")
	}
	if ev.AutoDelete > 0 && sent != nil {
		msgID := sent.ID
		time.AfterFunc(time.Duration(ev.AutoDelete)*time.Second, func() {
			if err := m.ctx.Rest.DeleteMessage(chID, msgID); err != nil {
				m.ctx.Logger.Debug("Welcome: auto-delete failed in %s (guild %s): %v", chID, guildID, err)
			}
		})
	}
}

// SendTest posts the event's current message to its configured channel using
// actorID (the dashboard user clicking Test) as the sample member.
func (m *WelcomeModule) SendTest(guildID, event, actorID string) error {
	if m.ctx == nil || m.store == nil {
		return fmt.Errorf("welcome module not loaded")
	}
	cfg, err := m.store.Get(guildID)
	if err != nil {
		return err
	}
	var ev modules.WelcomeEvent
	switch event {
	case modules.WelcomeEventJoin:
		ev = cfg.Join
	case modules.WelcomeEventLeave:
		ev = cfg.Leave
	default:
		return fmt.Errorf("unknown event %q", event)
	}
	if !ev.Enabled {
		return fmt.Errorf("%s messages are disabled — enable and save them first", event)
	}
	if ev.ChannelID == "" {
		return fmt.Errorf("choose a channel first")
	}
	uid, err := snowflake.Parse(actorID)
	if err != nil {
		return fmt.Errorf("invalid user id")
	}
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild id")
	}
	user, member := m.sampleActor(guildID, uid)
	msg, warning := buildMessage(ev, m.templateData(gid, user, member))
	if messageEmpty(msg) {
		return fmt.Errorf("the message and embed are both empty")
	}
	chID, err := snowflake.Parse(ev.ChannelID)
	if err != nil {
		return fmt.Errorf("invalid channel id")
	}
	if _, err := m.ctx.Rest.CreateMessage(chID, msg); err != nil {
		m.setLastError(guildID, event, err.Error())
		return fmt.Errorf("Discord rejected the message: %w", err)
	}
	m.setLastError(guildID, event, warning)
	return nil
}

// sampleActor resolves a member for the test message: cached member first,
// then a REST user lookup, then a placeholder so the test still works for a
// user the bot cannot see.
func (m *WelcomeModule) sampleActor(guildID string, uid snowflake.ID) (discord.User, *discord.Member) {
	if member := m.ctx.Bot.GetCachedMember(guildID, uid.String()); member != nil {
		return member.User, member
	}
	if m.ctx.Rest != nil {
		if user, err := m.ctx.Rest.GetUser(uid); err == nil && user != nil {
			return *user, nil
		}
	}
	return discord.User{ID: uid, Username: "test-user"}, nil
}

func (m *WelcomeModule) lastErrorKey(guildID, event string) string {
	return guildID + "|" + event
}

func (m *WelcomeModule) setLastError(guildID, event, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.lastErrorKey(guildID, event)
	if strings.TrimSpace(msg) == "" {
		delete(m.lastErr, key)
		return
	}
	m.lastErr[key] = msg
}

// LastError implements modules.WelcomeAdmin.
func (m *WelcomeModule) LastError(guildID, event string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[m.lastErrorKey(guildID, event)]
}

// GetGuildConfig implements modules.WelcomeAdmin.
func (m *WelcomeModule) GetGuildConfig(guildID string) (modules.WelcomeConfig, error) {
	if m.store == nil {
		return modules.WelcomeConfig{}, fmt.Errorf("welcome module not loaded")
	}
	return m.store.Get(guildID)
}

// SetGuildConfig implements modules.WelcomeAdmin.
func (m *WelcomeModule) SetGuildConfig(guildID string, cfg modules.WelcomeConfig) error {
	if m.store == nil {
		return fmt.Errorf("welcome module not loaded")
	}
	return m.store.Save(guildID, cfg)
}
