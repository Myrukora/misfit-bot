package modules

import (
	"testing"

	"github.com/misfit/bot/commands"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/yuin/gopher-lua"
)

// TestLuaRespondRoutesToVirtualContext proves a Lua command's respond /
// reply_text output lands in a virtual (web) Context — the mechanism that
// makes Lua commands dashboard-runnable with zero bridge changes.
func TestLuaRespondRoutesToVirtualContext(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	var title, desc, text string
	ctx := &commands.Context{
		Author: discord.User{ID: snowflake.MustParse("123")},
		Respond: func(embeds ...discord.Embed) error {
			if len(embeds) > 0 {
				title, desc = embeds[0].Title, embeds[0].Description
			}
			return nil
		},
		ReplyText: func(s string) error {
			text = s
			return nil
		},
	}

	b := &LuaBridge{}
	b.RegisterCommandContext(L, ctx)

	if err := L.DoString(`ctx.respond("Hello", "World")`); err != nil {
		t.Fatalf("ctx.respond: %v", err)
	}
	if title != "Hello" || desc != "World" {
		t.Errorf("respond captured %q / %q, want Hello / World", title, desc)
	}

	if err := L.DoString(`ctx.reply_text("plain text")`); err != nil {
		t.Fatalf("ctx.reply_text: %v", err)
	}
	if text != "plain text" {
		t.Errorf("reply_text captured %q, want %q", text, "plain text")
	}
}
