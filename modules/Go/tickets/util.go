package main

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
	if len(s) > 120 {
		s = s[:117] + "…"
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
