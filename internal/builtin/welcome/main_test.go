package welcome

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

type sentMessage struct {
	channelID snowflake.ID
	message   discord.MessageCreate
}

type fakeRest struct {
	rest.Rest
	mu   sync.Mutex
	sent []sentMessage
	err  error
}

func (f *fakeRest) CreateMessage(channelID snowflake.ID, msg discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.sent = append(f.sent, sentMessage{channelID: channelID, message: msg})
	return &discord.Message{ID: snowflake.ID(999)}, nil
}

func (f *fakeRest) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

type fakeBot struct {
	commands.Interface
	guild  *discord.Guild
	member *discord.Member
}

func (f *fakeBot) GetCachedGuild(string) *discord.Guild           { return f.guild }
func (f *fakeBot) GetCachedMember(string, string) *discord.Member { return f.member }

func newTestModule(t *testing.T) (*WelcomeModule, *fakeRest, *fakeBot) {
	t.Helper()
	fr := &fakeRest{}
	fb := &fakeBot{guild: &discord.Guild{ID: 1, Name: "Test Guild", MemberCount: 10}}
	m := New().(*WelcomeModule)
	ctx := &modules.Context{
		DataDir: t.TempDir(),
		Logger:  testLogger{},
		Rest:    fr,
		Bot:     fb,
		Events:  modules.NewEventHooks(),
	}
	if err := m.OnLoad(ctx); err != nil {
		t.Fatalf("OnLoad: %v", err)
	}
	return m, fr, fb
}

func joinEvent(user discord.User) *events.GuildMemberJoin {
	now := time.Now()
	return &events.GuildMemberJoin{
		GenericGuildMember: &events.GenericGuildMember{
			GuildID: 1,
			Member:  discord.Member{User: user, JoinedAt: &now},
		},
	}
}

func TestJoinDeliversRenderedMessage(t *testing.T) {
	m, fr, _ := newTestModule(t)
	cfg := modules.WelcomeConfig{Join: modules.WelcomeEvent{
		Enabled:   true,
		ChannelID: "2",
		Message:   "Hi {mention}, welcome to {server} (#{ord:{server(members)}})",
	}}
	if err := m.SetGuildConfig("1", cfg); err != nil {
		t.Fatal(err)
	}
	m.onJoin(joinEvent(discord.User{ID: 42, Username: "newbie"}))

	sent := fr.messages()
	if len(sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(sent))
	}
	if sent[0].channelID != snowflake.ID(2) {
		t.Errorf("channel = %s, want 2", sent[0].channelID)
	}
	if sent[0].message.Content != "Hi <@42>, welcome to Test Guild (#10th)" {
		t.Errorf("content = %q", sent[0].message.Content)
	}
}

func TestDisabledEventDoesNotSend(t *testing.T) {
	m, fr, _ := newTestModule(t)
	if err := m.SetGuildConfig("1", modules.WelcomeConfig{
		Join: modules.WelcomeEvent{Enabled: false, ChannelID: "2", Message: "hi"},
	}); err != nil {
		t.Fatal(err)
	}
	m.onJoin(joinEvent(discord.User{ID: 42, Username: "newbie"}))
	if got := len(fr.messages()); got != 0 {
		t.Fatalf("sent = %d, want 0", got)
	}
}

func TestLeaveUsesCachedNickname(t *testing.T) {
	m, fr, _ := newTestModule(t)
	if err := m.SetGuildConfig("1", modules.WelcomeConfig{
		Leave: modules.WelcomeEvent{Enabled: true, ChannelID: "3", Message: "Bye {user}"},
	}); err != nil {
		t.Fatal(err)
	}
	user := discord.User{ID: 42, Username: "leaver"}
	nick := "Nickname"
	m.onLeave(&events.GuildMemberLeave{
		GuildID: 1,
		User:    user,
		Member:  discord.Member{User: user, Nick: &nick},
	})
	sent := fr.messages()
	if len(sent) != 1 || sent[0].message.Content != "Bye Nickname" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestDeliveryFailureRecordedAndCleared(t *testing.T) {
	m, fr, _ := newTestModule(t)
	if err := m.SetGuildConfig("1", modules.WelcomeConfig{
		Join: modules.WelcomeEvent{Enabled: true, ChannelID: "2", Message: "hi"},
	}); err != nil {
		t.Fatal(err)
	}
	fr.err = errors.New("missing access")
	m.onJoin(joinEvent(discord.User{ID: 42, Username: "newbie"}))
	if got := m.LastError("1", modules.WelcomeEventJoin); !strings.Contains(got, "missing access") {
		t.Fatalf("LastError = %q", got)
	}
	fr.err = nil
	m.onJoin(joinEvent(discord.User{ID: 42, Username: "newbie"}))
	if got := m.LastError("1", modules.WelcomeEventJoin); got != "" {
		t.Fatalf("LastError after success = %q, want empty", got)
	}
}

func TestSendTest(t *testing.T) {
	m, fr, fb := newTestModule(t)
	fb.member = &discord.Member{User: discord.User{ID: 42, Username: "tester", GlobalName: strPtr("Tester")}}
	if err := m.SetGuildConfig("1", modules.WelcomeConfig{
		Join: modules.WelcomeEvent{Enabled: true, ChannelID: "2", Message: "Hi {user}"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.SendTest("1", modules.WelcomeEventJoin, "42"); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	sent := fr.messages()
	if len(sent) != 1 || sent[0].message.Content != "Hi Tester" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestSendTestRejectsDisabledEvent(t *testing.T) {
	m, _, _ := newTestModule(t)
	if err := m.SetGuildConfig("1", modules.WelcomeConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := m.SendTest("1", modules.WelcomeEventJoin, "42"); err == nil {
		t.Fatal("disabled event should be rejected")
	}
}
