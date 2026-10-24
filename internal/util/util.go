package util

// PtrBool returns a pointer to the given bool value.
// Useful for discord.EmbedField.Inline which requires *bool.
func PtrBool(b bool) *bool {
	return &b
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
