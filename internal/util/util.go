package util

import "strings"

// PtrBool returns a pointer to the given bool value.
// Useful for discord.EmbedField.Inline which requires *bool.
func PtrBool(b bool) *bool {
	return &b
}

// ContainsStr checks if a string slice contains the given item.
func ContainsStr(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// TokenizeArgs splits a string into args, respecting double-quoted strings.
// Unlike strings.Fields, quoted substrings are preserved as single args.
//
// Examples:
//
//	TokenizeArgs(`hello world`) → ["hello", "world"]
//	TokenizeArgs(`cleanup text "hello world" 5`) → ["cleanup", "text", "hello world", "5"]
//	TokenizeArgs(`say "he said \"hi\""`) → ["say", "he said \"hi\""]
func TokenizeArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var args []string
	var current strings.Builder
	inQuotes := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			current.WriteByte(ch)
			continue
		}

		if ch == '"' {
			inQuotes = !inQuotes
			current.WriteByte(ch)
			continue
		}

		if ch == ' ' || ch == '\t' {
			if inQuotes {
				current.WriteByte(ch)
			} else if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteByte(ch)
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// ExtractID extracts a Discord user ID from mention formats.
// Handles @User, <@ID>, and <@!ID> formats.
// If the input is already a plain ID, it returns it as-is.
func ExtractID(raw string) string {
	// Handle <@!ID> format (nickname mention)
	if len(raw) > 3 && raw[:3] == "<@!" && raw[len(raw)-1] == '>' {
		return raw[3 : len(raw)-1]
	}
	// Handle <@ID> format
	if len(raw) > 2 && raw[:2] == "<@" && raw[len(raw)-1] == '>' {
		return raw[2 : len(raw)-1]
	}
	// Already a plain ID or @User format
	if len(raw) > 1 && raw[0] == '@' {
		return raw[1:]
	}
	return raw
}
