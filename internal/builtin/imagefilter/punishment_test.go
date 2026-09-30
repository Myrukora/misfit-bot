package imagefilter

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/modules"
)

// recordingRest captures every REST action executePunishment performs. Only
// the methods the punishment path uses are implemented; the embedded nil
// rest.Rest panics if the code ever reaches for anything else (which is
// exactly the kind of surprise these tests should surface).
type recordingRest struct {
	rest.Rest

	mu        sync.Mutex
	deleted   []snowflake.ID
	loggedTo  []snowflake.ID
	embeds    []discord.Embed
	kicked    []snowflake.ID
	banned    []snowflake.ID
	muted     []snowflake.ID
	muteUntil []time.Time

	// REST fallback (uncached member) answers, when set.
	fallbackRoles   []discord.Role
	fallbackMembers map[snowflake.ID]discord.Member
	fallbackCalls   int
	nilMember       bool // model a 2xx body decoding to JSON null
}

func (r *recordingRest) DeleteMessage(channelID, messageID snowflake.ID, opts ...rest.RequestOpt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, messageID)
	return nil
}

func (r *recordingRest) CreateMessage(channelID snowflake.ID, mc discord.MessageCreate, opts ...rest.RequestOpt) (*discord.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loggedTo = append(r.loggedTo, channelID)
	r.embeds = append(r.embeds, mc.Embeds...)
	return nil, nil
}

func (r *recordingRest) RemoveMember(guildID, userID snowflake.ID, opts ...rest.RequestOpt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kicked = append(r.kicked, userID)
	return nil
}

func (r *recordingRest) AddBan(guildID, userID snowflake.ID, deleteMessageDuration time.Duration, opts ...rest.RequestOpt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.banned = append(r.banned, userID)
	return nil
}

func (r *recordingRest) UpdateMember(guildID, userID snowflake.ID, update discord.MemberUpdate, opts ...rest.RequestOpt) (*discord.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.muted = append(r.muted, userID)
	if update.CommunicationDisabledUntil.OK && update.CommunicationDisabledUntil.Value != nil {
		r.muteUntil = append(r.muteUntil, *update.CommunicationDisabledUntil.Value)
	}
	return nil, nil
}

func (r *recordingRest) GetRoles(guildID snowflake.ID, opts ...rest.RequestOpt) ([]discord.Role, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallbackCalls++
	return r.fallbackRoles, nil
}

func (r *recordingRest) GetMember(guildID, userID snowflake.ID, opts ...rest.RequestOpt) (*discord.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallbackCalls++
	if r.nilMember {
		return nil, nil
	}
	if m, ok := r.fallbackMembers[userID]; ok {
		return &m, nil
	}
	return nil, errNotFoundTest
}

var errNotFoundTest = errors.New("test: member not found")

// stubBot answers the cache/identity questions isImmune asks.
type stubBot struct {
	commands.Interface
	ownerID string
	selfID  string
	members map[string]*discord.Member
	roles   map[string]*discord.Role
}

func (b *stubBot) GetGuildOwnerID(string) string { return b.ownerID }
func (b *stubBot) GetSelfUserID() string         { return b.selfID }
func (b *stubBot) GetCachedMember(guildID, userID string) *discord.Member {
	return b.members[guildID+"/"+userID]
}
func (b *stubBot) GetCachedRole(guildID, roleID string) *discord.Role {
	return b.roles[guildID+"/"+roleID]
}

// newPunishHarness wires a module whose context carries only the pieces
// executePunishment/isImmune touch (REST + Bot + logger).
func newPunishHarness(t *testing.T) (*ImageFilterModule, *recordingRest, *stubBot) {
	t.Helper()
	r := &recordingRest{}
	b := &stubBot{selfID: "42", members: map[string]*discord.Member{}, roles: map[string]*discord.Role{}}
	mod := &ImageFilterModule{ok: true}
	mod.ctx = &modules.Context{Logger: nopLogger{}, Rest: r, Bot: b}
	return mod, r, b
}

func testJob() job {
	return job{guildID: "1", channelID: "10", messageID: "100", authorID: "7", imageURL: "https://cdn/x.png"}
}

// TestPunishmentDeleteRule pins the delete rule: the message is deleted unless
// (punishment == none AND delete_on_none == false). Delete happens BEFORE the
// punishment action and is independent of immunity.
func TestPunishmentDeleteRule(t *testing.T) {
	for _, tc := range []struct {
		name         string
		punishment   string
		deleteOnNone bool
		wantDeleted  bool
	}{
		{"none without delete_on_none keeps the message", PunishNone, false, false},
		{"none with delete_on_none deletes", PunishNone, true, true},
		{"mute always deletes", PunishMute, false, true},
		{"kick always deletes", PunishKick, false, true},
		{"ban always deletes", PunishBan, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod, r, b := newPunishHarness(t)
			b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
			b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}

			mod.executePunishment(testJob(), 0.99, GuildConfig{
				Punishment:   tc.punishment,
				MuteDuration: 600,
				DeleteOnNone: tc.deleteOnNone,
			})
			if got := len(r.deleted) == 1; got != tc.wantDeleted {
				t.Errorf("deleted = %v, want %v", got, tc.wantDeleted)
			}
		})
	}
}

// TestPunishmentActions pins the action each punishment performs, and that a
// guild owner / higher-ranked author is never acted against (immunity is
// checked before acting, while the delete still stands).
func TestPunishmentActions(t *testing.T) {
	t.Run("mute sets a timeout of the configured duration", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}

		before := time.Now().UTC()
		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishMute, MuteDuration: 120})
		if len(r.muted) != 1 {
			t.Fatalf("mute calls = %d, want 1", len(r.muted))
		}
		if len(r.muteUntil) != 1 {
			t.Fatalf("timeout deadlines = %d, want 1", len(r.muteUntil))
		}
		got := r.muteUntil[0].Sub(before)
		if got < 119*time.Second || got > 121*time.Second {
			t.Errorf("timeout deadline offset = %v, want ~120s", got)
		}
		if len(r.kicked) != 0 || len(r.banned) != 0 {
			t.Error("mute must not kick or ban")
		}
	})

	t.Run("kick removes the member", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishKick})
		if len(r.kicked) != 1 {
			t.Errorf("kick calls = %d, want 1", len(r.kicked))
		}
	})

	t.Run("ban adds a ban", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishBan})
		if len(r.banned) != 1 {
			t.Errorf("ban calls = %d, want 1", len(r.banned))
		}
	})

	t.Run("guild owner is immune but the message is still deleted", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.ownerID = "7" // the author owns the guild

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishKick})
		if len(r.kicked) != 0 {
			t.Error("the guild owner must never be kicked")
		}
		if len(r.deleted) != 1 {
			t.Error("the delete is independent of immunity")
		}
	})

	t.Run("equal-or-higher role author is immune", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.selfID = "42"
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 5}
		b.members["1/42"] = &discord.Member{User: discord.User{ID: 42}, RoleIDs: []snowflake.ID{60}}
		b.roles["1/60"] = &discord.Role{ID: 60, Position: 5}

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishBan})
		if len(r.banned) != 0 {
			t.Error("an author at or above the bot's top role must be immune")
		}
	})

	t.Run("uncached author falls back to REST", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.selfID = "42" // neither member is cached: both resolved over REST
		r.fallbackRoles = []discord.Role{
			{ID: 50, Position: 9}, // author's top role
			{ID: 60, Position: 3}, // bot's top role
		}
		r.fallbackMembers = map[snowflake.ID]discord.Member{
			7:  {User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}},
			42: {User: discord.User{ID: 42}, RoleIDs: []snowflake.ID{60}},
		}

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishKick})
		if len(r.kicked) != 0 {
			t.Error("a higher-ranked author must be immune even when resolved via REST")
		}
		if r.fallbackCalls == 0 {
			t.Error("the REST fallback was never exercised")
		}
	})

	t.Run("unresolvable hierarchy is not immune", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.selfID = "42" // no cache, no REST answers → both positions -1
		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishKick})
		if len(r.kicked) != 1 {
			t.Errorf("kick calls = %d, want 1 when the hierarchy is unknown", len(r.kicked))
		}
	})

	t.Run("a nil member from REST does not panic", func(t *testing.T) {
		// disgo returns (nil, nil) when a 2xx body decodes to JSON null.
		mod, r, b := newPunishHarness(t)
		b.selfID = "42"
		r.nilMember = true

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishKick})
		if len(r.kicked) != 1 {
			t.Errorf("kick calls = %d, want 1 for an unknown member", len(r.kicked))
		}
	})
}

// TestPunishmentLogTargets pins where the log embed goes: only the guild's
// configured log channel, never the detection channel; nothing at all when no
// channel is configured. Detection and failure both use the log channel.
func TestPunishmentLogTargets(t *testing.T) {
	t.Run("no log channel means no embed", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishKick})
		if len(r.loggedTo) != 0 {
			t.Errorf("log embeds = %d, want 0 without a log channel", len(r.loggedTo))
		}
	})

	t.Run("detection embed goes to the log channel in red", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}

		mod.executePunishment(testJob(), 0.987654, GuildConfig{Punishment: PunishKick, LogChannel: "555"})
		if len(r.loggedTo) != 1 || r.loggedTo[0] != snowflake.ID(555) {
			t.Fatalf("log target = %v, want [555]", r.loggedTo)
		}
		if len(r.embeds) != 1 {
			t.Fatalf("embeds = %d, want 1", len(r.embeds))
		}
		e := r.embeds[0]
		if e.Color != embed.ColorError {
			t.Errorf("log embed color = %#x, want %#x (red)", e.Color, embed.ColorError)
		}
		if !strings.Contains(e.Title, "detected") {
			t.Errorf("log embed title = %q, want the detection title", e.Title)
		}
		for _, want := range []string{"<@7>", "`0.9877`", "<#10>", "Kicked"} {
			if !strings.Contains(e.Description, want) {
				t.Errorf("log embed description missing %q:\n%s", want, e.Description)
			}
		}
		if e.Image == nil || e.Image.URL != "https://cdn/x.png" {
			t.Error("log embed must carry the detected image")
		}
	})

	t.Run("failure log goes to the log channel too, never the message channel", func(t *testing.T) {
		mod, r, b := newPunishHarness(t)
		b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
		b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}
		mod.ctx.Rest = &failingBanRest{recordingRest: r}

		mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishBan, LogChannel: "555"})
		for _, target := range r.loggedTo {
			if target == snowflake.ID(10) {
				t.Errorf("log embed must never go to the detection channel (10)")
			}
			if target != snowflake.ID(555) {
				t.Errorf("failure log target = %s, want 555", target)
			}
		}
	})
}

// TestPunishmentFailureLog pins the failure branch: a REST error on the action
// produces the failure embed (not the detection one) and still no
// message-channel write.
func TestPunishmentFailureLog(t *testing.T) {
	mod, r, b := newPunishHarness(t)
	b.members["1/7"] = &discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{50}}
	b.roles["1/50"] = &discord.Role{ID: 50, Position: 1}
	// Ban through a REST double whose AddBan fails.
	failing := &failingBanRest{recordingRest: r}
	mod.ctx.Rest = failing

	mod.executePunishment(testJob(), 0.99, GuildConfig{Punishment: PunishBan, LogChannel: "555"})
	if len(r.embeds) != 1 {
		t.Fatalf("embeds = %d, want 1 (the failure embed)", len(r.embeds))
	}
	e := r.embeds[0]
	if !strings.Contains(e.Title, "failed") {
		t.Errorf("failure embed title = %q, want a failed title", e.Title)
	}
	if !strings.Contains(e.Description, "FAILED") {
		t.Errorf("failure embed description missing the FAILED marker:\n%s", e.Description)
	}
	if len(r.loggedTo) != 1 || r.loggedTo[0] != snowflake.ID(555) {
		t.Errorf("failure log target = %v, want [555]", r.loggedTo)
	}
}

// failingBanRest makes the ban action fail so the failure branch runs.
type failingBanRest struct {
	*recordingRest
}

func (f *failingBanRest) AddBan(guildID, userID snowflake.ID, d time.Duration, opts ...rest.RequestOpt) error {
	return errTestBan
}

var errTestBan = errors.New("test: ban refused by Discord")
