package commands

import (
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// TestValidateSlashOptionsOk asserts every currently-registered core slash
// tree is structurally valid (Discord would otherwise reject the whole batch
// with 50035 and silently unregister every slash command).
func TestValidateSlashOptionsOk(t *testing.T) {
	if len(CoreSlashCommands) == 0 {
		t.Fatal("no core slash commands registered")
	}
	for _, sc := range CoreSlashCommands {
		if errs := ValidateSlashOptions(sc.Options); len(errs) > 0 {
			t.Errorf("core slash /%s has invalid schema:\n  %s", sc.Name, strings.Join(errs, "\n  "))
		}
	}
}

// TestValidateSlashOptionsRequiredAfterOptional pins the exact bug that hit
// the live bot: a required option placed after an optional one inside a
// subcommand. Discord rejects the ENTIRE SetGlobalCommands batch with 50035
// for this, so no slash command registers.
func TestValidateSlashOptionsRequiredAfterOptional(t *testing.T) {
	bad := []discord.ApplicationCommandOption{
		discord.ApplicationCommandOptionSubCommand{
			Name: "create", Description: "Create a panel",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "name", Description: "Panel name", Required: true},
				discord.ApplicationCommandOptionChannel{Name: "channel", Description: "Channel (default: current)", Required: false},
				discord.ApplicationCommandOptionString{Name: "type", Description: "Type key", Required: true},
			},
		},
	}
	errs := ValidateSlashOptions(bad)
	if len(errs) == 0 {
		t.Fatal("expected a violation for required-after-optional, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "type") && strings.Contains(e, "channel") {
			found = true
		}
	}
	if !found {
		t.Errorf("violations %v do not name the offending options (type after channel)", errs)
	}
}

// TestValidateSlashOptionsValidOrder asserts a correctly-ordered tree passes.
func TestValidateSlashOptionsValidOrder(t *testing.T) {
	good := []discord.ApplicationCommandOption{
		discord.ApplicationCommandOptionSubCommand{
			Name: "create", Description: "Create a panel",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "name", Description: "Panel name", Required: true},
				discord.ApplicationCommandOptionString{Name: "type", Description: "Type key", Required: true},
				discord.ApplicationCommandOptionChannel{Name: "channel", Description: "Channel (default: current)", Required: false},
			},
		},
	}
	if errs := ValidateSlashOptions(good); len(errs) > 0 {
		t.Errorf("valid tree reported invalid:\n  %s", strings.Join(errs, "\n  "))
	}
}
