package tickets

import (
	"regexp"
	"strings"
)

// util.go — small parse helpers shared by the in-channel commands and the
// dashboard admin surface.

var snowflakeRe = regexp.MustCompile(`\b\d{15,21}\b`)

// extractSnowflake pulls the first 15–21 digit number out of a mention/ID.
func extractSnowflake(s string) string {
	return snowflakeRe.FindString(strings.TrimSpace(s))
}
