package tickets

import (
	"regexp"
	"sort"
	"strings"
)

// util.go — small parse/format helpers shared by the CLI and dashboard.

var snowflakeRe = regexp.MustCompile(`\b\d{15,21}\b`)
var channelRefRe = regexp.MustCompile(`^<?#?(\d{15,21})>?$`)

// extractSnowflake pulls the first 15–21 digit number out of a mention/ID.
func extractSnowflake(s string) string {
	m := snowflakeRe.FindString(strings.TrimSpace(s))
	return m
}

// extractSnowflakes pulls every snowflake from a (possibly multi-mention) list.
func extractSnowflakes(s string) []string {
	found := snowflakeRe.FindAllString(s, -1)
	out := make([]string, 0, len(found))
	seen := map[string]bool{}
	for _, f := range found {
		if !seen[f] {
			out = append(out, f)
			seen[f] = true
		}
	}
	return out
}

// parseChannelRef resolves "#mention", bare ID, or "<#id>" — falling back to
// fallbackID when the arg is empty/"here".
func parseChannelRef(arg, fallbackID string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "here" || arg == "this" {
		return fallbackID
	}
	if m := channelRefRe.FindStringSubmatch(arg); m != nil {
		return m[1]
	}
	return ""
}

// splitRoleList parses "a, b c" / mention soup into a deduped ID list; empty
// input clears ("none").
func splitRoleList(val string) []string {
	val = strings.TrimSpace(val)
	if val == "" || strings.EqualFold(val, "none") || val == "-" {
		return nil
	}
	return extractSnowflakes(val)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func codeOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "*not set*"
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:117]) + "…"
	}
	return "`" + s + "`"
}

func rolesOrNone(ids []string) string {
	if len(ids) == 0 {
		return "*none*"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "<@&" + id + ">"
	}
	return strings.Join(parts, ", ")
}

func mapKeysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// titleWord upper-cases the first rune of a word ("suspend" → "Suspend").
func titleWord(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// titleCase converts an identifier ("contact_staff" / "bug-reports") into a
// display label ("Contact Staff") — replaces the deprecated strings.Title.
func titleCase(key string) string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	if len(parts) == 0 {
		return key
	}
	var b strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(titleWord(p))
	}
	return b.String()
}
