package reactionroles

import (
	"strings"
	"testing"

	"github.com/misfit/bot/modules"
)

func TestCreateAndPostPanel(t *testing.T) {
	m, _, fr := newRRTestModule(t)
	panel, err := m.CreatePanel("1", validPanel())
	if err != nil {
		t.Fatal(err)
	}
	if panel.ID != 1 {
		t.Fatalf("panel id = %d", panel.ID)
	}
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatalf("PostPanel: %v", err)
	}
	sent := fr.sentMessages()
	if len(sent) != 1 || sent[0].channelID != 2 {
		t.Fatalf("sent = %+v", sent)
	}
	if len(sent[0].message.Embeds) != 1 || sent[0].message.Embeds[0].Title != "Pick your roles" {
		t.Fatalf("embed = %+v", sent[0].message.Embeds)
	}
	reactions := fr.reactions()
	if len(reactions) != 1 || reactions[0].emoji != "👍" {
		t.Fatalf("reactions = %+v", reactions)
	}
	cfg, _ := m.GetGuildConfig("1")
	if cfg.Panels[0].MessageID != "500" || cfg.Panels[0].PostedChannelID != "2" {
		t.Fatalf("posted state = %+v", cfg.Panels[0])
	}
	if len(cfg.Panels[0].PostedEmojis) != 1 || cfg.Panels[0].PostedEmojis[0] != "👍" {
		t.Fatalf("posted emojis = %+v", cfg.Panels[0].PostedEmojis)
	}
}

func TestRepostSyncsReactions(t *testing.T) {
	m, _, fr := newRRTestModule(t)
	panel, _ := m.CreatePanel("1", validPanel())
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := m.GetGuildConfig("1")
	edit := cfg.Panels[0]
	edit.Mappings = []modules.ReactionRoleMapping{{Emoji: "✅", RoleID: "20"}}
	if _, err := m.UpdatePanel("1", edit); err != nil {
		t.Fatal(err)
	}
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	updated := len(fr.updated)
	removed := append([]rrReaction(nil), fr.removedRct...)
	fr.mu.Unlock()
	if updated != 1 {
		t.Fatalf("message not edited (updated=%d)", updated)
	}
	if len(removed) != 1 || removed[0].emoji != "👍" {
		t.Fatalf("stale reaction not removed: %+v", removed)
	}
	cfg, _ = m.GetGuildConfig("1")
	if len(cfg.Panels[0].PostedEmojis) != 1 || cfg.Panels[0].PostedEmojis[0] != "✅" {
		t.Fatalf("posted emojis = %+v", cfg.Panels[0].PostedEmojis)
	}
	if cfg.Panels[0].MessageID != "500" {
		t.Fatalf("message id changed on edit: %s", cfg.Panels[0].MessageID)
	}
}

func TestRepostChannelChange(t *testing.T) {
	m, _, fr := newRRTestModule(t)
	panel, _ := m.CreatePanel("1", validPanel())
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := m.GetGuildConfig("1")
	edit := cfg.Panels[0]
	edit.ChannelID = "3"
	if _, err := m.UpdatePanel("1", edit); err != nil {
		t.Fatal(err)
	}
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	deleted := append([]rrReaction(nil), fr.deleted...)
	fr.mu.Unlock()
	if len(deleted) != 1 || deleted[0].messageID != 500 {
		t.Fatalf("old message not deleted: %+v", deleted)
	}
	sent := fr.sentMessages()
	if len(sent) != 2 || sent[1].channelID != 3 {
		t.Fatalf("sent = %+v", sent)
	}
	cfg, _ = m.GetGuildConfig("1")
	if cfg.Panels[0].MessageID != "501" || cfg.Panels[0].PostedChannelID != "3" {
		t.Fatalf("posted state = %+v", cfg.Panels[0])
	}
}

func TestPostRejectsAboveBotRole(t *testing.T) {
	m, _, fr := newRRTestModule(t)
	p := validPanel()
	p.Mappings[0].RoleID = "60"
	panel, err := m.CreatePanel("1", p)
	if err != nil {
		t.Fatal(err)
	}
	err = m.PostPanel("1", panel.ID)
	if err == nil || !strings.Contains(err.Error(), "above my highest role") {
		t.Fatalf("err = %v", err)
	}
	if got := len(fr.sentMessages()); got != 0 {
		t.Fatalf("posted despite failure: %d", got)
	}
}

func TestPostRequiresEnabledAndMappings(t *testing.T) {
	m, _, _ := newRRTestModule(t)
	p := validPanel()
	p.Enabled = false
	panel, _ := m.CreatePanel("1", p)
	if err := m.PostPanel("1", panel.ID); err == nil {
		t.Fatal("disabled panel should not post")
	}

	p = validPanel()
	p.Mappings = nil
	panel, _ = m.CreatePanel("1", p)
	if err := m.PostPanel("1", panel.ID); err == nil {
		t.Fatal("panel without mappings should not post")
	}
}

func TestDeletePanelRemovesMessage(t *testing.T) {
	m, _, fr := newRRTestModule(t)
	panel, _ := m.CreatePanel("1", validPanel())
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.DeletePanel("1", panel.ID, true); err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	deleted := append([]rrReaction(nil), fr.deleted...)
	fr.mu.Unlock()
	if len(deleted) != 1 || deleted[0].messageID != 500 {
		t.Fatalf("deleted = %+v", deleted)
	}
	cfg, _ := m.GetGuildConfig("1")
	if len(cfg.Panels) != 0 {
		t.Fatalf("panel not removed: %+v", cfg.Panels)
	}
}

func TestUpdatePanelPreservesPostedState(t *testing.T) {
	m, _, _ := newRRTestModule(t)
	panel, _ := m.CreatePanel("1", validPanel())
	if err := m.PostPanel("1", panel.ID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := m.GetGuildConfig("1")
	edit := cfg.Panels[0]
	edit.Name = "Renamed"
	updated, err := m.UpdatePanel("1", edit)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MessageID != "500" || updated.PostedChannelID != "2" {
		t.Fatalf("posted state lost: %+v", updated)
	}
}
