package commands

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// findSlashOpts returns the options of the core slash command with the given
// name (auto-generated twins from registerCoreSlashCommands).
func findSlashOpts(t *testing.T, name string) []discord.ApplicationCommandOption {
	t.Helper()
	for _, sc := range CoreSlashCommands {
		if sc.Name == name {
			return sc.Options
		}
	}
	t.Fatalf("core slash command %q not registered", name)
	return nil
}

// subNames returns the names of the subcommand options in opts.
func subNames(opts []discord.ApplicationCommandOption) []string {
	var out []string
	for _, o := range opts {
		if s, ok := o.(discord.ApplicationCommandOptionSubCommand); ok {
			out = append(out, s.Name)
		}
	}
	return out
}

// subOptByName returns the subcommand option with the given name.
func subOptByName(t *testing.T, opts []discord.ApplicationCommandOption, name string) discord.ApplicationCommandOptionSubCommand {
	t.Helper()
	for _, o := range opts {
		if s, ok := o.(discord.ApplicationCommandOptionSubCommand); ok && s.Name == name {
			return s
		}
	}
	t.Fatalf("subcommand %q not found", name)
	return discord.ApplicationCommandOptionSubCommand{}
}

// TestCoreSlashNestedSubcommands pins the handler-parity contract of the
// nested slash schemas: the slash dispatcher prepends the subcommand name and
// appends the provided options in declaration order, so each subcommand's
// options must line up exactly with the positional args the prefix handler
// reads (e.g. backup restore <filename> [--confirm]).
func TestCoreSlashNestedSubcommands(t *testing.T) {
	// backup: create | verify <filename> | restore <filename> [confirm] | list
	backup := findSlashOpts(t, "backup")
	if got := subNames(backup); !equalStrings(got, []string{"create", "verify", "restore", "list"}) {
		t.Fatalf("backup subcommands = %v, want [create verify restore list]", got)
	}
	if s := subOptByName(t, backup, "create"); len(s.Options) != 0 {
		t.Fatalf("backup create should take no options, got %v", s.Options)
	}
	verify := subOptByName(t, backup, "verify")
	if len(verify.Options) != 1 || verify.Options[0].OptionName() != "filename" {
		t.Fatalf("backup verify should take one required filename, got %v", verify.Options)
	}
	if !optReq(verify.Options[0]) {
		t.Fatal("backup verify filename must be required")
	}
	restore := subOptByName(t, backup, "restore")
	if len(restore.Options) != 2 || restore.Options[0].OptionName() != "filename" || restore.Options[1].OptionName() != "confirm" {
		t.Fatalf("backup restore should take filename + confirm, got %v", restore.Options)
	}
	if _, ok := restore.Options[1].(discord.ApplicationCommandOptionBool); !ok {
		t.Fatalf("backup restore confirm must be a bool, got %T", restore.Options[1])
	}
	if s := subOptByName(t, backup, "list"); len(s.Options) != 0 {
		t.Fatalf("backup list should take no options, got %v", s.Options)
	}

	// ratelimit: status <user> | reset <user>
	rl := findSlashOpts(t, "ratelimit")
	if got := subNames(rl); !equalStrings(got, []string{"status", "reset"}) {
		t.Fatalf("ratelimit subcommands = %v, want [status reset]", got)
	}
	for _, sub := range []string{"status", "reset"} {
		s := subOptByName(t, rl, sub)
		if len(s.Options) != 1 || s.Options[0].OptionName() != "user" {
			t.Fatalf("ratelimit %s should take one required user, got %v", sub, s.Options)
		}
		if _, ok := s.Options[0].(discord.ApplicationCommandOptionUser); !ok {
			t.Fatalf("ratelimit %s user must be a USER option, got %T", sub, s.Options[0])
		}
		if !optReq(s.Options[0]) {
			t.Fatalf("ratelimit %s user must be required", sub)
		}
	}

	// permissions: add <user> | remove <user> | list
	perm := findSlashOpts(t, "permissions")
	if got := subNames(perm); !equalStrings(got, []string{"add", "remove", "list"}) {
		t.Fatalf("permissions subcommands = %v, want [add remove list]", got)
	}
	for _, sub := range []string{"add", "remove"} {
		s := subOptByName(t, perm, sub)
		if len(s.Options) != 1 || s.Options[0].OptionName() != "user" || !optReq(s.Options[0]) {
			t.Fatalf("permissions %s should take one required user, got %v", sub, s.Options)
		}
	}
	if s := subOptByName(t, perm, "list"); len(s.Options) != 0 {
		t.Fatalf("permissions list should take no options, got %v", s.Options)
	}

	// update: check | now | status | test | set <key> <value>
	upd := findSlashOpts(t, "update")
	if got := subNames(upd); !equalStrings(got, []string{"check", "now", "status", "test", "set"}) {
		t.Fatalf("update subcommands = %v, want [check now status test set]", got)
	}
	for _, sub := range []string{"check", "now", "status", "test"} {
		if s := subOptByName(t, upd, sub); len(s.Options) != 0 {
			t.Fatalf("update %s should take no options, got %v", sub, s.Options)
		}
	}
	setSub := subOptByName(t, upd, "set")
	if len(setSub.Options) != 2 || setSub.Options[0].OptionName() != "key" || setSub.Options[1].OptionName() != "value" {
		t.Fatalf("update set should take key + value, got %v", setSub.Options)
	}
	for _, o := range setSub.Options {
		if !optReq(o) {
			t.Fatalf("update set option %s must be required", o.OptionName())
		}
	}
}

// TestCoreSlashSetKeyChoices pins the known config keys as choices so the
// dashboard renders a dropdown instead of a free-text key field.
func TestCoreSlashSetKeyChoices(t *testing.T) {
	opts := findSlashOpts(t, "set")
	if len(opts) != 2 || opts[0].OptionName() != "key" || opts[1].OptionName() != "value" {
		t.Fatalf("set options = %v, want [key value]", opts)
	}
	s, ok := opts[0].(discord.ApplicationCommandOptionString)
	if !ok {
		t.Fatalf("set key must be a string option, got %T", opts[0])
	}
	if len(s.Choices) == 0 {
		t.Fatal("set key must expose known config keys as choices")
	}
	found := map[string]bool{}
	for _, c := range s.Choices {
		found[c.Value] = true
	}
	// Full config.Set key list (config/config.go Set) — the dropdown must
	// not omit valid keys (Lemma review: curated subsets mislead owners).
	for _, want := range []string{"prefix", "token", "owner_id", "log_level", "dashboard_listen", "oauth_client_secret",
		"log_file_path", "modules_auto_load", "updater_enabled", "updater_repo", "updater_branch",
		"updater_token", "updater_interval", "updater_auto_pull", "updater_notify_channel"} {
		if !found[want] {
			t.Errorf("set key choices missing %q", want)
		}
	}
}

// TestCoreSlashHelpOptional pins /help's optional command argument.
func TestCoreSlashHelpOptional(t *testing.T) {
	opts := findSlashOpts(t, "help")
	if len(opts) != 1 || opts[0].OptionName() != "command" {
		t.Fatalf("help options = %v, want [command]", opts)
	}
	if optReq(opts[0]) {
		t.Fatal("help command must be optional")
	}
}

// optReq extracts the Required flag from a typed option (mirrors the
// dashboard's optionRequired helper; kept local to avoid a cross-package dep).
func optReq(o discord.ApplicationCommandOption) bool {
	switch t := o.(type) {
	case discord.ApplicationCommandOptionString:
		return t.Required
	case discord.ApplicationCommandOptionInt:
		return t.Required
	case discord.ApplicationCommandOptionFloat:
		return t.Required
	case discord.ApplicationCommandOptionBool:
		return t.Required
	case discord.ApplicationCommandOptionChannel:
		return t.Required
	case discord.ApplicationCommandOptionRole:
		return t.Required
	case discord.ApplicationCommandOptionUser:
		return t.Required
	case discord.ApplicationCommandOptionMentionable:
		return t.Required
	default:
		return false
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
