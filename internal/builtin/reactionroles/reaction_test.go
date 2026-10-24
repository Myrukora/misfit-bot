package reactionroles

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

func unicodeEmoji(name string) discord.PartialEmoji {
	return discord.PartialEmoji{Name: &name}
}

func customEmoji(name string, id snowflake.ID) discord.PartialEmoji {
	return discord.PartialEmoji{Name: &name, ID: &id}
}

func reactionAdd(messageID, userID snowflake.ID, emoji discord.PartialEmoji, member *discord.Member) *events.MessageReactionAdd {
	gid := snowflake.ID(1)
	return &events.MessageReactionAdd{
		GenericReaction: &events.GenericReaction{
			GuildID:   &gid,
			ChannelID: 2,
			MessageID: messageID,
			UserID:    userID,
			Emoji:     emoji,
		},
		Member: member,
	}
}

func reactionRemove(messageID, userID snowflake.ID, emoji discord.PartialEmoji) *events.MessageReactionRemove {
	gid := snowflake.ID(1)
	return &events.MessageReactionRemove{
		GenericReaction: &events.GenericReaction{
			GuildID:   &gid,
			ChannelID: 2,
			MessageID: messageID,
			UserID:    userID,
			Emoji:     emoji,
		},
	}
}

func postTestPanel(t *testing.T, m *ReactionRolesModule, p modules.ReactionPanel) {
	t.Helper()
	created, err := m.CreatePanel("1", p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PostPanel("1", created.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReactionGrantsRole(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	postTestPanel(t, m, validPanel())
	m.onReactionAdd(reactionAdd(500, 10, unicodeEmoji("👍"), bot.members["10"]))
	grants := fr.roleGrants()
	if len(grants) != 1 || grants[0].userID != 10 || grants[0].roleID != 20 {
		t.Fatalf("grants = %+v", grants)
	}
}

func TestReactionIgnoresUnknownEmojiAndActors(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	postTestPanel(t, m, validPanel())

	m.onReactionAdd(reactionAdd(500, 10, unicodeEmoji("👎"), bot.members["10"]))
	m.onReactionAdd(reactionAdd(500, 99, unicodeEmoji("👍"), bot.members["99"]))
	bot.members["11"] = &discord.Member{User: discord.User{ID: 11, Bot: true}}
	m.onReactionAdd(reactionAdd(500, 11, unicodeEmoji("👍"), bot.members["11"]))
	m.onReactionAdd(reactionAdd(999, 10, unicodeEmoji("👍"), bot.members["10"]))
	if got := len(fr.roleGrants()); got != 0 {
		t.Fatalf("grants = %d, want 0", got)
	}
}

func TestReactionIgnoresDisabledPanel(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	postTestPanel(t, m, validPanel())
	cfg, _ := m.GetGuildConfig("1")
	edit := cfg.Panels[0]
	edit.Enabled = false
	if _, err := m.UpdatePanel("1", edit); err != nil {
		t.Fatal(err)
	}
	m.onReactionAdd(reactionAdd(500, 10, unicodeEmoji("👍"), bot.members["10"]))
	if got := len(fr.roleGrants()); got != 0 {
		t.Fatalf("grants = %d, want 0", got)
	}
}

func TestUnreactRemovesRole(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	postTestPanel(t, m, validPanel())
	bot.members["10"].RoleIDs = []snowflake.ID{10, 20}
	m.onReactionRemove(reactionRemove(500, 10, unicodeEmoji("👍")))
	removals := fr.roleRemovals()
	if len(removals) != 1 || removals[0].roleID != 20 {
		t.Fatalf("removals = %+v", removals)
	}
}

func TestUnreactKeepsRoleWhenDisabled(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	p := validPanel()
	p.RemoveOnUnreact = false
	postTestPanel(t, m, p)
	bot.members["10"].RoleIDs = []snowflake.ID{10, 20}
	m.onReactionRemove(reactionRemove(500, 10, unicodeEmoji("👍")))
	if got := len(fr.roleRemovals()); got != 0 {
		t.Fatalf("removals = %d, want 0", got)
	}
}

func TestReactionRoleHierarchyFailure(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	postTestPanel(t, m, validPanel())
	bot.roles["20"].Position = 60 // above the bot's top role (50)
	m.onReactionAdd(reactionAdd(500, 10, unicodeEmoji("👍"), bot.members["10"]))
	if got := len(fr.roleGrants()); got != 0 {
		t.Fatalf("granted despite hierarchy: %d", got)
	}
	if m.LastError("1") == "" {
		t.Fatal("hierarchy failure should be recorded")
	}
}

func TestCustomEmojiMatchesByID(t *testing.T) {
	m, bot, fr := newRRTestModule(t)
	p := validPanel()
	p.Mappings = []modules.ReactionRoleMapping{{Emoji: "wave:123456789012345678", RoleID: "20"}}
	postTestPanel(t, m, p)
	renamed := "renamed"
	m.onReactionAdd(reactionAdd(500, 10, customEmoji(renamed, 123456789012345678), bot.members["10"]))
	grants := fr.roleGrants()
	if len(grants) != 1 || grants[0].roleID != 20 {
		t.Fatalf("grants = %+v", grants)
	}
}
