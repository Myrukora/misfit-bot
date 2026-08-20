package modules

import (
	"path/filepath"
	"testing"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/logger"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestHelloLuaLoadsAndRuns(t *testing.T) {
	log, err := logger.New(t.TempDir(), "error", false)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer log.Close()
	loader := NewLuaLoader(nil, log, "", nil)
	// hello lives at modules/Lua/hello/hello.lua (test cwd = modules/).
	mod, err := loader.Load(filepath.Join("Lua", "hello", "hello.lua"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := mod.OnLoad(&Context{}); err != nil {
		t.Fatalf("onload: %v", err)
	}
	if mod.Name() != "hello" {
		t.Fatalf("name = %q", mod.Name())
	}
	var title, desc, text string
	ctx := &commands.Context{
		Author: discord.User{ID: snowflake.MustParse("1")},
		Web:    true,
		Args:   []string{},
		Respond: func(embeds ...discord.Embed) error {
			if len(embeds) > 0 {
				title, desc = embeds[0].Title, embeds[0].Description
			}
			return nil
		},
		ReplyText: func(s string) error { text = s; return nil },
	}
	cmds := mod.Commands()
	if len(cmds) != 2 {
		t.Fatalf("want 2 commands, got %d", len(cmds))
	}
	if err := cmds[0].Execute(ctx); err != nil {
		t.Fatalf("execute hello: %v", err)
	}
	if title != "Hello from Lua!" {
		t.Errorf("title = %q", title)
	}
	if err := cmds[1].Execute(ctx); err != nil {
		t.Fatalf("execute luainfo: %v", err)
	}
	if desc == "" || title != "Lua Module Info" {
		t.Errorf("luainfo captured %q / %q", title, desc)
	}
	_ = text
}
