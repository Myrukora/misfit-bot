package dashboard

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// loginRender renders the standalone login page the way handleLoginPage does
// (baseData → ShowSidebar=false → rd_login).
func loginRender(t *testing.T, m *DashboardModule) string {
	t.Helper()
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	m.tmpl = tmpl
	d := m.baseData(nil)
	d.ShowSidebar = false
	var sb strings.Builder
	if err := m.tmpl.render(&sb, "rd_login", d); err != nil {
		t.Fatalf("render rd_login: %v", err)
	}
	return sb.String()
}

// TestLoginPageLiveIdentity pins the login card on the live bot identity: the
// brand image is the gateway self user's Discord avatar CDN URL (not a static
// png), and the title is the live name — a stale config name must not win when
// the gateway knows the bot's real name.
func TestLoginPageLiveIdentity(t *testing.T) {
	m := &DashboardModule{
		botName: "ConfigFallbackName",
		client:  &bot.Client{Caches: cache.New()},
	}
	avatar := "abcdef1234567890"
	m.client.Caches.SetSelfUser(discord.OAuth2User{User: discord.User{
		ID:       snowflake.ID(123),
		Username: "TestBot",
		Avatar:   &avatar,
	}})

	out := loginRender(t, m)

	const wantAvatar = "https://cdn.discordapp.com/avatars/123/abcdef1234567890.png?size=512"
	if !strings.Contains(out, `src="`+wantAvatar+`"`) {
		t.Errorf("login page does not render the live bot avatar URL %q", wantAvatar)
	}
	if !strings.Contains(out, `class="login-title">TestBot<`) {
		t.Error("login title does not render the live bot name")
	}
	if strings.Contains(out, "logo.png") || strings.Contains(out, "/static/redesign/") {
		t.Error("login page still references the old static logo/redesign paths")
	}
	if strings.Contains(out, "ConfigFallbackName") {
		t.Error("login page falls back to the config name even though the gateway self user is cached")
	}
}

// TestLoginPageAvatarFallback pins the pre-gateway login render: with no
// self user in the cache and no bot/client, the brand must degrade to the
// letter fallback — never a broken empty <img src="">. botIdentity() falls to
// the literal "Bot" in that state, so the fallback letter is "B".
func TestLoginPageAvatarFallback(t *testing.T) {
	m := &DashboardModule{}
	out := loginRender(t, m)
	if strings.Contains(out, `<img class="login-avatar" src="">`) {
		t.Error("login page renders an empty-src avatar img instead of the letter fallback")
	}
	if !strings.Contains(out, `login-avatar-fallback">B<`) {
		t.Errorf("login page does not render the letter fallback for a missing avatar\n---\n%s", out)
	}
}

// TestStaticAssetsServedAtNewPaths pins the asset relocation: the embedded
// static subtree serves style.css/app.js/discord.svg at the /static/ root, the
// redesign subdirectory is gone, and logo.png is gone too.
func TestStaticAssetsServedAtNewPaths(t *testing.T) {
	sub := staticSubFS()
	for _, name := range []string{"style.css", "app.js", "discord.svg"} {
		b, err := fs.ReadFile(sub, name)
		if err != nil || len(b) == 0 {
			t.Errorf("static asset %q missing at web/static root: %v", name, err)
		}
	}
	if _, err := fs.Stat(sub, "redesign"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("web/static/redesign still present: %v", err)
	}
	if _, err := fs.Stat(sub, "logo.png"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("logo.png still present in web/static: %v", err)
	}
}

// TestTemplatesReferenceStaticRoot pins that no template references the old
// /static/redesign/ prefix or logo.png.
func TestTemplatesReferenceStaticRoot(t *testing.T) {
	paths, err := templateFiles.ReadDir("web/templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	for _, p := range paths {
		b, err := templateFiles.ReadFile("web/templates/" + p.Name())
		if err != nil {
			t.Fatalf("read %s: %v", p.Name(), err)
		}
		src := string(b)
		if strings.Contains(src, "/static/redesign/") {
			t.Errorf("%s still references /static/redesign/", p.Name())
		}
		if strings.Contains(src, "logo.png") {
			t.Errorf("%s still references logo.png", p.Name())
		}
	}
}
