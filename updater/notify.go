package updater

import (
	"fmt"
	"strings"
	"time"

	"github.com/custombot/bot/config"
	"github.com/disgoorg/disgo/discord"
)

// GitHub-flavored embed colors.
const (
	colorGitHubGreen = 0x2EA043 // PR opened
	colorGitHubBlue  = 0x0969DA // new commit
)

// maxDescription is Discord's embed description limit (4096) minus slack.
const maxDescription = 4000

// truncate cuts s to at most n runes (Discord counts characters) without
// splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// shortSHA returns the 7-character abbreviation of a commit SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// isMergeCommit reports whether a commit message is a GitHub/merge commit that
// should not produce a notification (noise: "Merge pull request #123 from …").
func isMergeCommit(msg string) bool {
	m := strings.TrimSpace(msg)
	return strings.HasPrefix(m, "Merge pull request") ||
		strings.HasPrefix(m, "Merge branch") ||
		strings.HasPrefix(m, "Merge commit")
}

// authorRow builds the "user on top" part of every notification embed: the
// author's avatar (pfp) with their username as a hyperlink to their GitHub
// profile.
func authorRow(u ghUser) (name, url, iconURL string) {
	login := strings.TrimSpace(u.Login)
	if login == "" {
		login = "unknown"
	}
	profile := u.HTMLURL
	if profile == "" {
		profile = "https://github.com/" + login
	}
	return login, profile, u.AvatarURL
}

func repoAtBranch(cfg *config.UpdaterConfig) string {
	repo := cfg.Repo
	if repo == "" {
		repo = "owner/repo"
	}
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	return repo + " @ " + branch
}

// buildPREmbed formats a pull-request notification:
//
//	[avatar] login (hyperlink → github profile)
//	Pull request opened: #3604 Bipkibipki        ← bold title, links to the PR
//	<PR body, markdown rendered>
func buildPREmbed(cfg *config.UpdaterConfig, pr ghPR) discord.Embed {
	body := strings.TrimSpace(pr.Body)
	if body == "" {
		body = "No description provided."
	}
	return discord.NewEmbed().
		WithAuthor(authorRow(pr.User)).
		WithTitle(fmt.Sprintf("Pull request opened: #%d %s", pr.Number, pr.Title)).
		WithURL(pr.HTMLURL).
		WithDescription(truncate(body, maxDescription)).
		WithColor(colorGitHubGreen).
		WithFooter(repoAtBranch(cfg), "").
		WithTimestamp(time.Now())
}

// buildCommitEmbed formats a commit notification:
//
//	[avatar] login (hyperlink → github profile)
//	1 new commit #1d84cc2                        ← bold title, links to the commit
//	<commit message, markdown rendered>
func buildCommitEmbed(cfg *config.UpdaterConfig, c ghCommit) discord.Embed {
	msg := strings.TrimSpace(c.Commit.Message)
	if msg == "" {
		msg = "No description provided."
	}
	u := ghUser{}
	if c.Author != nil {
		u = *c.Author
	}
	if u.Login == "" {
		// No linked GitHub account: fall back to the git author name.
		u.Login = strings.TrimSpace(c.Commit.Author.Name)
	}
	return discord.NewEmbed().
		WithAuthor(authorRow(u)).
		WithTitle(fmt.Sprintf("1 new commit #%s", shortSHA(c.SHA))).
		WithURL(c.HTMLURL).
		WithDescription(truncate(msg, maxDescription)).
		WithColor(colorGitHubBlue).
		WithFooter(repoAtBranch(cfg), "").
		WithTimestamp(time.Now())
}

// ── Temporary embed tester samples ───────────────────────────────────────
// Markdown-rich on purpose: Discord renders markdown in embed descriptions, so
// the owner can see bold/italic/code/lists/links all render before real events
// start flowing.

const samplePRBody = "**Bold text**, *italic text*, and `inline code` all render in embed descriptions.\n\n" +
	"> Blockquotes work too.\n\n" +
	"```go\nfunc hello() {\n\tfmt.Println(\"markdown works\")\n}\n```\n\n" +
	"- list item one\n- list item two\n\n" +
	"And a [link to GitHub](https://github.com) for good measure."

const sampleCommitMsg = "Add markdown rendering to notifications\n\n**Bold**, *italic*, `inline code`, a [link](https://github.com), and:\n\n- a\n- list"
