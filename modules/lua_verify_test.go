package modules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/logger"
)

// helloFixture is the minimal hello module previously living at
// modules/Lua/hello/hello.lua (placeholder removed from the repo). The test
// writes it to a temp dir so the Lua loader still has a real file to load.
const helloFixture = `-- minimal test module for the Lua loader
M = {}

M.name = "hello"
M.version = "1.0.0"
M.description = "A simple hello module (test fixture)."
M.author = "sam"

function M.on_load(M, name)
    ctx.log("Hello Lua module loaded!")
end

function M.on_unload()
end

function M.commands()
    return {
        {
            name = "hello",
            description = "Say hello from the Lua example module.",
            usage = "hello",
            category = "fun",
            execute = function(M)
                ctx.respond("Hello from Lua!", "This command was written in Lua!")
            end
        },
        {
            name = "luainfo",
            description = "Show info about the Lua example module.",
            usage = "luainfo",
            category = "fun",
            execute = function(M)
                local info = "Lua Module: " .. M.name .. "\n"
                info = info .. "Version: " .. M.version .. "\n"
                info = info .. "Description: " .. M.description
                ctx.respond("Lua Module Info", info)
            end
        }
    }
end

function M.slash_commands()
    return {}
end
`

func TestHelloLuaLoadsAndRuns(t *testing.T) {
	log, err := logger.New(t.TempDir(), "error", false)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer log.Close()
	loader := NewLuaLoader(nil, log, "", nil)
	path := filepath.Join(t.TempDir(), "hello.lua")
	if err := os.WriteFile(path, []byte(helloFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	mod, err := loader.Load(path)
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
