package onboarding

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/custombot/bot/config"
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
	cfg.Bot.Name = ""

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

	fmt.Print("Enter command prefix (default: [p]): ")
	prefix, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read prefix: %w", err)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "[p]"
	}
	cfg.Bot.Prefix = prefix

	fmt.Print("Enter bot name (default: Bot): ")
	name, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read name: %w", err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Bot"
	}
	cfg.Bot.Name = name

	fmt.Print("Enter Terms of Service URL (optional): ")
	tos, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read ToS URL: %w", err)
	}
	cfg.Bot.ToS = strings.TrimSpace(tos)

	fmt.Print("Enter Privacy Policy URL (optional): ")
	privacy, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read privacy URL: %w", err)
	}
	cfg.Bot.Privacy = strings.TrimSpace(privacy)

	cfg.Modules.AutoLoad = true
	cfg.Modules.Path = "modules"
	cfg.Modules.Disabled = []string{}

	cfg.Logging.Enabled = true
	cfg.Logging.FilePath = "logs/bot.log"
	cfg.Logging.Level = "info"
	cfg.Logging.Channel = ""

	if err := config.Save(cfg, dir); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Println("✅ Configuration saved to config.yml")
	fmt.Println("   Make sure to enable these Privileged Intents in Discord Developer Portal:")
	fmt.Println("   - MESSAGE_CONTENT")
	fmt.Println("   - GUILD_MEMBERS")
	fmt.Println("   - PRESENCES")
	fmt.Println()

	return cfg, nil
}
