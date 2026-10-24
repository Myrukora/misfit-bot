package moderation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

func savedConfig(t *testing.T, m *ModerationModule, cfg modules.ModerationConfig) modules.ModerationConfig {
	t.Helper()
	if err := m.SetGuildConfig("1", cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	got, err := m.GetGuildConfig("1")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return got
}

func TestWarnPostsNotificationAndAudit(t *testing.T) {
	m, _, fr := newModTestModule(t)
	cfg := savedConfig(t, m, testModConfig())

	out, err := m.warn(cfg, 1, 10, "Mod", 20, "spam")
	if err != nil {
		t.Fatalf("warn: %v", err)
	}
	if out.Count != 1 || out.Case.ID != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	sent := fr.messages()
	if len(sent) != 2 {
		t.Fatalf("messages = %d, want 2", len(sent))
	}
	if sent[0].channelID != 2 || sent[0].message.Embeds[0].Title != "Warning issued" {
		t.Fatalf("notification = %+v", sent[0])
	}
	if got := sent[0].message.Embeds[0].Fields[2].Value; got != "1" {
		t.Fatalf("total warnings field = %q", got)
	}
	if sent[1].channelID != 3 || !strings.Contains(sent[1].message.Embeds[0].Title, "Warning") {
		t.Fatalf("audit = %+v", sent[1])
	}
	cases, err := m.ListCases("1", modules.ModerationCaseFilter{})
	if err != nil || len(cases) != 1 || cases[0].Type != modules.ModerationTypeWarn {
		t.Fatalf("cases = %+v, err %v", cases, err)
	}
}

func TestWarnSameChannelDoesNotDoublePost(t *testing.T) {
	m, _, fr := newModTestModule(t)
	cfg := testModConfig()
	cfg.WarnChannel = "3" // same as audit
	cfg = savedConfig(t, m, cfg)
	if _, err := m.warn(cfg, 1, 10, "Mod", 20, "spam"); err != nil {
		t.Fatal(err)
	}
	if got := len(fr.messages()); got != 1 {
		t.Fatalf("messages = %d, want 1", got)
	}
}

func TestWarnRuleAppliesTimeout(t *testing.T) {
	m, _, fr := newModTestModule(t)
	cfg := testModConfig()
	cfg.Rules = []modules.ModerationRule{{Threshold: 2, Action: modules.ModerationActionTimeout, DurationMinutes: 60}}
	cfg = savedConfig(t, m, cfg)

	if _, err := m.warn(cfg, 1, 10, "Mod", 20, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.warn(cfg, 1, 10, "Mod", 20, "second"); err != nil {
		t.Fatal(err)
	}
	if len(fr.updates) != 1 {
		t.Fatalf("timeouts = %d, want 1", len(fr.updates))
	}
	until := fr.updates[0].disabled
	if until == nil || time.Until(*until) < 55*time.Minute {
		t.Fatalf("timeout until = %v", until)
	}
	cases, _ := m.ListCases("1", modules.ModerationCaseFilter{Type: modules.ModerationTypeTimeout})
	if len(cases) != 1 || !cases[0].Automated || cases[0].RuleThreshold != 2 || cases[0].WarningID != 2 {
		t.Fatalf("auto case = %+v", cases)
	}
	// Two warns: (notification + audit) + (auto-action audit + notification + audit).
	if got := len(fr.messages()); got != 5 {
		t.Fatalf("messages = %d, want 5", got)
	}
}

func TestWarnRequiresReason(t *testing.T) {
	m, _, _ := newModTestModule(t)
	cfg := savedConfig(t, m, testModConfig())
	if _, err := m.warn(cfg, 1, 10, "Mod", 20, "   "); err == nil {
		t.Fatal("empty reason should be rejected")
	}
}

func TestBanKickTimeoutUnbanRecordCases(t *testing.T) {
	m, _, fr := newModTestModule(t)
	cfg := savedConfig(t, m, testModConfig())

	if _, err := m.ban(cfg, 1, 10, "Mod", 20, "raid"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if len(fr.bans) != 1 || fr.bans[0] != 20 {
		t.Fatalf("bans = %+v", fr.bans)
	}
	if _, err := m.kick(cfg, 1, 10, "Mod", 20, "rule 1"); err != nil {
		t.Fatalf("kick: %v", err)
	}
	if len(fr.removes) != 1 || fr.removes[0] != 20 {
		t.Fatalf("removes = %+v", fr.removes)
	}
	if _, err := m.timeout(cfg, 1, 10, "Mod", 20, 30, "cool off"); err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if len(fr.updates) != 1 || fr.updates[0].disabled == nil {
		t.Fatalf("updates = %+v", fr.updates)
	}
	if _, err := m.unban(cfg, 1, 10, "Mod", 20, "appeal"); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if len(fr.unbans) != 1 {
		t.Fatalf("unbans = %+v", fr.unbans)
	}
	cases, _ := m.ListCases("1", modules.ModerationCaseFilter{})
	if len(cases) != 4 {
		t.Fatalf("cases = %d, want 4", len(cases))
	}
}

func TestRemoveWarningAudits(t *testing.T) {
	m, _, fr := newModTestModule(t)
	cfg := savedConfig(t, m, testModConfig())
	out, err := m.warn(cfg, 1, 10, "Mod", 20, "spam")
	if err != nil {
		t.Fatal(err)
	}
	before := len(fr.messages())
	c, err := m.removeWarning("1", out.Case.ID, 10, "Mod", "appeal accepted")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !c.Removed {
		t.Fatal("case not marked removed")
	}
	sent := fr.messages()
	if len(sent) != before+1 {
		t.Fatalf("messages = %d, want %d", len(sent), before+1)
	}
	if title := sent[len(sent)-1].message.Embeds[0].Title; !strings.Contains(title, "Warning removed") {
		t.Fatalf("audit title = %q", title)
	}
}

func TestAuditFailureRecorded(t *testing.T) {
	m, _, fr := newModTestModule(t)
	cfg := testModConfig()
	cfg.WarnNotify = false
	cfg = savedConfig(t, m, cfg)
	fr.sendErr = errors.New("missing access")
	if _, err := m.warn(cfg, 1, 10, "Mod", 20, "spam"); err != nil {
		t.Fatalf("warn should still succeed: %v", err)
	}
	if got := m.LastError("1"); !strings.Contains(got, "missing access") {
		t.Fatalf("LastError = %q", got)
	}
}

func TestFormatWarnings(t *testing.T) {
	if got := formatWarnings(nil); got != "No active warnings." {
		t.Fatalf("empty = %q", got)
	}
	now := time.Now()
	got := formatWarnings([]modules.ModerationCase{{
		ID: 3, CreatedAt: now, ModeratorTag: "Mod", Reason: "spam", ExpiresAt: &now,
	}})
	for _, want := range []string{"#3", "Mod", "spam", "expires"} {
		if !strings.Contains(got, want) {
			t.Errorf("format missing %q: %s", want, got)
		}
	}
}
