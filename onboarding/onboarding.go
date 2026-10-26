package onboarding

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/misfit/bot/config"
)

func Run(dir string) (*config.Config, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║       Bot Setup Wizard               ║")
	fmt.Println("║       First-time configuration       ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()

	base := *config.DefaultConfig
	cfg := &base
	cfg.Bot.Token = ""
	cfg.Bot.OwnerID = ""

	fmt.Print("Enter bot token: ")
	token, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read token: %w", err)
	}
	cfg.Bot.Token = strings.TrimSpace(token)

	fmt.Print("Enter your Discord user ID (owner): ")
	ownerID, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read owner ID: %w", err)
	}
	cfg.Bot.OwnerID = strings.TrimSpace(ownerID)

	// The bot's name is not configured: it is resolved live from the
	// app's Developer Portal identity at runtime.

	cfg.Modules.AutoLoad = true
	cfg.Modules.Path = "modules"
	cfg.Modules.Disabled = []string{}

	cfg.Logging.Enabled = true
	cfg.Logging.FilePath = "logs/bot.log"
	cfg.Logging.Level = "info"

	if err := config.WriteEnvToken(dir, cfg.Bot.Token); err != nil {
		return nil, fmt.Errorf("failed to save the bot token to .env: %w", err)
	}
	if err := config.Save(cfg, dir); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Println("✅ Configuration saved to config.yml")
	fmt.Println("   The bot token was written to .env (DISCORD_BOT_TOKEN, mode 0600).")
	fmt.Println("   Make sure to enable these Privileged Intents in Discord Developer Portal:")
	fmt.Println("   - MESSAGE_CONTENT")
	fmt.Println("   - GUILD_MEMBERS")
	fmt.Println("   - PRESENCES")
	fmt.Println()

	return cfg, nil
}
