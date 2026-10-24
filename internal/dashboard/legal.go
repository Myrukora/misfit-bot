package dashboard

import (
	"bytes"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// legalDirName is the folder (inside the bot directory, next to config.yml)
// that holds the editable legal pages served at /tos and /policy.
const legalDirName = "legal"

// legalMarkdown converts GitHub-flavoured Markdown (tables, strikethrough,
// task lists, autolinks) to HTML. Raw HTML in the source is escaped on purpose:
// these files live on the server, but the pages are public, so a stray tag must
// never become executable markup.
var legalMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// defaultTermsMD is written to legal/terms.md on first render when the file
// does not exist, so a fresh install shows an editable page instead of a 404.
const defaultTermsMD = `# Terms of Service

This page is generated from **legal/terms.md** in the bot's directory. Edit
that file to replace this starter text with your own terms — the dashboard
picks the change up on the next request.

## 1. Acceptance

By using this bot or its dashboard you agree to these terms. If you do not
agree, do not use the bot.

## 2. Use of the service

- Do not use the bot to break Discord's Terms of Service or applicable law.
- Do not attempt to disrupt, overload, or gain unauthorised access to the bot,
  its host, or other users' data.
- The bot may be changed, suspended, or shut down at any time without notice.

## 3. Content

You are responsible for the content you submit through the bot. The operator
may remove content and restrict access when these terms are violated.

## 4. Liability

The bot is provided **as is**, without warranty of any kind. To the maximum
extent permitted by law, the operator is not liable for any damages arising
from its use.

## 5. Changes

These terms may be updated at any time. Continued use after a change means you
accept the updated terms.
`

// defaultPrivacyMD mirrors defaultTermsMD for /policy.
const defaultPrivacyMD = `# Privacy Policy

This page is generated from **legal/privacy.md** in the bot's directory. Edit
that file to replace this starter text with your own policy — the dashboard
picks the change up on the next request.

## What the bot stores

- Discord identifiers (user, guild, channel and role IDs) needed to run the
  bot's features.
- Configuration for the features you enable, including per-server settings.
- Messages and attachments only where a feature requires them (for example,
  ticket transcripts kept on the bot's host).

## Where it is stored

Data lives on the bot operator's host. It is not sold or shared with third
parties beyond what is needed to operate the bot and its Discord connection.

## Retention

Data is kept only as long as it is needed for the feature that created it.
Ticket transcripts, logs and caches are pruned according to the configured
retention settings; you can ask the operator to delete your data at any time.

## Your choices

You can ask the operator to access, correct, or delete the data the bot holds
about you. Removing the bot from a server stops it from processing new
messages there.

## Contact

Replace this section with the operator's contact details.
`

// defaultLegal returns the starter document for a legal page name.
func defaultLegal(name string) string {
	if name == "privacy" {
		return defaultPrivacyMD
	}
	return defaultTermsMD
}

// legalTitle is the human-readable document name.
func legalTitle(name string) string {
	if name == "privacy" {
		return "Privacy Policy"
	}
	return "Terms of Service"
}

// legalFile is the on-disk markdown path for a legal page.
func (m *DashboardModule) legalFile(name string) string {
	dir := "."
	if m.bot != nil && m.bot.GetConfigDir() != "" {
		dir = m.bot.GetConfigDir()
	}
	return filepath.Join(dir, legalDirName, name+".md")
}

// readLegal loads a legal page's markdown, creating the starter document when
// the file (or its folder) does not exist yet. The file is read on every
// request, so edits apply without a restart.
func (m *DashboardModule) readLegal(name string) (string, error) {
	path := m.legalFile(name)
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	def := defaultLegal(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(def), 0o644); err != nil {
		return "", err
	}
	return def, nil
}

// handleLegalPage renders /tos and /policy from their markdown files.
func (m *DashboardModule) handleLegalPage(w http.ResponseWriter, r *http.Request, name string) {
	src, err := m.readLegal(name)
	if err != nil {
		m.logger.Error("Legal page %q is unreadable: %v", name, err)
		http.Error(w, "legal page unavailable", http.StatusInternalServerError)
		return
	}
	var body bytes.Buffer
	if err := legalMarkdown.Convert([]byte(src), &body); err != nil {
		m.logger.Error("Legal page %q failed to render: %v", name, err)
		http.Error(w, "legal page unavailable", http.StatusInternalServerError)
		return
	}
	html := body.String()
	if strings.TrimSpace(src) == "" {
		html = `<p class="muted">This document is empty — edit <code>` +
			template.HTMLEscapeString(legalDirName+"/"+name+".md") +
			`</code> in the bot directory.</p>`
	}
	d := m.baseData(sessionOf(r))
	d.ShowSidebar = false // standalone document page
	d.Page = name
	d.Content = map[string]any{
		"title": legalTitle(name),
		"body":  template.HTML(html), //nolint:gosec // goldmark escapes raw HTML
	}
	m.tmpl.render(w, "rd_legal", d)
}
