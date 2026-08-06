package embed

import (
	"time"

	"github.com/disgoorg/disgo/discord"
)

func New() discord.Embed {
	return discord.NewEmbed()
}

const (
	ColorDefault = 0x5865F2
	ColorSuccess = 0x57F287
	ColorWarning = 0xFEE75C
	ColorError   = 0xED4245
	ColorInfo    = 0x5865F2
	ColorPurple  = 0x9B59B6
	ColorDark    = 0x2C2F33
)

func Success(title, desc string) discord.Embed {
	return New().
		WithTitle(title).
		WithDescription(desc).
		WithColor(ColorSuccess).
		WithTimestamp(time.Now())
}

func Error(title, desc string) discord.Embed {
	return New().
		WithTitle(title).
		WithDescription(desc).
		WithColor(ColorError).
		WithTimestamp(time.Now())
}

func Info(title, desc string) discord.Embed {
	return New().
		WithTitle(title).
		WithDescription(desc).
		WithColor(ColorInfo).
		WithTimestamp(time.Now())
}

func Warning(title, desc string) discord.Embed {
	return New().
		WithTitle(title).
		WithDescription(desc).
		WithColor(ColorWarning).
		WithTimestamp(time.Now())
}
