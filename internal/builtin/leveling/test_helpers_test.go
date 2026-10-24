package leveling

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
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

// fakeBot serves the cache lookups the module makes.
type fakeBot struct {
	commands.Interface
	guild   *discord.Guild
	members map[string]*discord.Member
}

func (b *fakeBot) GetCachedGuild(string) *discord.Guild { return b.guild }

func (b *fakeBot) GetCachedMember(guildID, userID string) *discord.Member {
	if b.members == nil {
		return nil
	}
	return b.members[guildID+"/"+userID]
}

type roleGrant struct {
	GuildID string
	UserID  string
	RoleID  string
}

// fakeRest implements the REST surface the module touches. Messages are held
// newest-first per channel, matching Discord's ordering.
type fakeRest struct {
	rest.Rest

	mu       sync.Mutex
	msgs     map[snowflake.ID][]discord.Message
	channels []discord.GuildChannel
	threads  []discord.GuildThread
	members  map[snowflake.ID]*discord.Member
	users    map[snowflake.ID]*discord.User
	added    []roleGrant
	removed  []roleGrant
	calls    int
	// failChannels makes GetMessages fail for the given channel IDs.
	failChannels map[snowflake.ID]error
}

func newFakeRest() *fakeRest {
	return &fakeRest{
		msgs:    map[snowflake.ID][]discord.Message{},
		members: map[snowflake.ID]*discord.Member{},
		users:   map[snowflake.ID]*discord.User{},
	}
}

// setMessages replaces a channel's history. The slice is sorted newest-first.
func (f *fakeRest) setMessages(channelID snowflake.ID, msgs []discord.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[channelID] = msgs
}

func (f *fakeRest) counts() (added, removed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.added), len(f.removed)
}

func (f *fakeRest) addedRoles() []roleGrant {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]roleGrant(nil), f.added...)
}

func (f *fakeRest) removedRoles() []roleGrant {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]roleGrant(nil), f.removed...)
}

func (f *fakeRest) GetMessages(channelID, _, before, after snowflake.ID, limit int, _ ...rest.RequestOpt) ([]discord.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err, ok := f.failChannels[channelID]; ok {
		return nil, err
	}
	history := f.msgs[channelID]
	// Discord returns the batch immediately after `after` (or before
	// `before`), newest first within that batch.
	var selected []discord.Message
	for _, msg := range history {
		if after != 0 && msg.ID <= after {
			continue
		}
		if before != 0 && msg.ID >= before {
			continue
		}
		selected = append(selected, msg)
	}
	// history is newest-first, so the tail of selected holds the oldest.
	if len(selected) > limit {
		selected = selected[len(selected)-limit:]
	}
	return selected, nil
}

func (f *fakeRest) GetGuildChannels(snowflake.ID, ...rest.RequestOpt) ([]discord.GuildChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]discord.GuildChannel(nil), f.channels...), nil
}

func (f *fakeRest) GetActiveGuildThreads(snowflake.ID, ...rest.RequestOpt) (*discord.GuildActiveThreads, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &discord.GuildActiveThreads{Threads: append([]discord.GuildThread(nil), f.threads...)}, nil
}

func (f *fakeRest) GetMember(_, userID snowflake.ID, _ ...rest.RequestOpt) (*discord.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if member, ok := f.members[userID]; ok {
		return member, nil
	}
	return nil, &rest.Error{Response: &http.Response{StatusCode: http.StatusNotFound}}
}

func (f *fakeRest) GetUser(userID snowflake.ID, _ ...rest.RequestOpt) (*discord.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if user, ok := f.users[userID]; ok {
		return user, nil
	}
	return &discord.User{ID: userID, Username: "user" + userID.String()}, nil
}

func (f *fakeRest) AddMemberRole(guildID, userID, roleID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, roleGrant{GuildID: guildID.String(), UserID: userID.String(), RoleID: roleID.String()})
	return nil
}

func (f *fakeRest) RemoveMemberRole(guildID, userID, roleID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, roleGrant{GuildID: guildID.String(), UserID: userID.String(), RoleID: roleID.String()})
	return nil
}

// newHarness builds a module with a real SQLite store in a temp dir and the
// fakes wired into its context. randInt always returns the maximum award.
func newHarness(t *testing.T) (*LevelingModule, *fakeRest, *fakeBot) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "leveling.db"), nil)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := newFakeRest()
	b := &fakeBot{members: map[string]*discord.Member{}}
	m := &LevelingModule{
		store:     store,
		randInt:   func(n int) int { return n - 1 },
		lastErr:   map[string]string{},
		cooldown:  map[string]map[string]time.Time{},
		catching:  map[string]bool{},
		catchupOK: map[string]bool{},
		liveBuf:   map[string][]bufferedMessage{},
		sync:      map[string]*syncState{},
		catchup:   map[string]*catchupState{},
	}
	m.ctx = &modules.Context{Logger: testLogger{}, Rest: r, Bot: b}
	// The rank card never touches the network in tests.
	previous := avatarFetch
	avatarFetch = func(string) []byte { return nil }
	t.Cleanup(func() { avatarFetch = previous })
	return m, r, b
}

// enabledConfig is a deterministic config: 10 XP per message, 60s cooldown,
// voice off (voice tests opt in).
func enabledConfig() modules.LevelingConfig {
	cfg := defaultConfig()
	cfg.Enabled = true
	cfg.MessageXPMin = 10
	cfg.MessageXPMax = 10
	cfg.MessageCooldownSeconds = 60
	cfg.VoiceEnabled = false
	cfg.CatchupEnabled = true
	cfg.CatchupMaxMessages = 2000
	cfg.CatchupMaxChannels = 50
	return cfg
}

// saveConfig persists cfg for the guild.
func saveConfig(t *testing.T, m *LevelingModule, guildID string, cfg modules.LevelingConfig) {
	t.Helper()
	if err := m.store.SaveConfig(guildID, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

func textChannel(t *testing.T, id, name string, position int) discord.GuildChannel {
	t.Helper()
	var ch discord.GuildTextChannel
	raw := fmt.Sprintf(`{"id":%q,"type":0,"name":%q,"position":%d}`, id, name, position)
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatalf("build text channel: %v", err)
	}
	return ch
}

func testMessage(id, channelID, userID snowflake.ID, at time.Time) discord.Message {
	return discord.Message{
		ID:        id,
		ChannelID: channelID,
		Author:    discord.User{ID: userID, Username: "member"},
		CreatedAt: at,
	}
}
