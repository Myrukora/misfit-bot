package dashboard

import (
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// permRestStub implements only GetUser; the resolver never calls the rest of
// the REST surface.
type permRestStub struct {
	rest.Rest
	user *discord.User
	err  error
}

func (s permRestStub) GetUser(snowflake.ID, ...rest.RequestOpt) (*discord.User, error) {
	return s.user, s.err
}

func TestResolveUsernamesFallsBackToREST(t *testing.T) {
	global := "Sam"
	m := &DashboardModule{client: &bot.Client{
		Caches: cache.New(),
		Rest:   permRestStub{user: &discord.User{ID: 42, Username: "sam", GlobalName: &global}},
	}}
	names := m.resolveUsernames([]string{"42", "not-an-id"})
	if names["42"] != "Sam" {
		t.Fatalf("resolved name = %q, want Sam", names["42"])
	}
	if _, ok := names["not-an-id"]; ok {
		t.Fatalf("invalid ID unexpectedly resolved: %+v", names)
	}

	m.client.Rest = permRestStub{err: errors.New("discord unavailable")}
	if got := m.resolveUsernames([]string{"42"}); len(got) != 0 {
		t.Fatalf("failed REST lookup should stay unmapped: %+v", got)
	}
}

func TestResolveUsernamesWithoutClient(t *testing.T) {
	m := &DashboardModule{}
	if got := m.resolveUsernames([]string{"42"}); len(got) != 0 {
		t.Fatalf("nil client should yield no names: %+v", got)
	}
}
