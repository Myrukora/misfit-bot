package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"strings"
)

//go:embed web/templates/*.html
var templateFiles embed.FS

//go:embed web/static
var staticFiles embed.FS

type templateBundle struct {
	tmpl *template.Template
}

// renderData is the shared context passed to every page template.
type renderData struct {
	Bot         string // dynamic bot name (gateway self-user → Dev Portal app name → config)
	BotAvatar   string // bot avatar URL (may be empty)
	User        *userJSON
	Level       string
	Guilds      []guildOpt
	CSRF        string
	Page        string // page title
	Content     any    // page-specific payload
	ShowConfig  bool   // owner/elevated: show core config nav
	ShowStaff   bool   // staff+: show guild nav
	IsOwner     bool
	IsElevated  bool
	IsStaff     bool
	IsRegular   bool
	ShowSidebar bool // false = standalone page (login/setup): no sidebar/topbar
	Raw         bool
}

var tmplFuncs = template.FuncMap{
	"join":        strings.Join,
	"joinOr":      joinOr,
	"yesno":       yesno,
	"csvContains": csvContains,
	"pageTitle":   pageTitle,
	"initial":     initial,
}

// csvContains reports whether opt appears in the comma-separated csv value
// (used to render multi-select checkboxes).
func csvContains(csv, opt string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == opt {
			return true
		}
	}
	return false
}

// joinOr joins s with sep, or returns or when s is empty.
func joinOr(s []string, sep, or string) string {
	if len(s) == 0 {
		return or
	}
	return strings.Join(s, sep)
}

// yesno renders a boolean as ✅ or ⛔.
func yesno(b bool) string {
	if b {
		return "✅"
	}
	return "⛔"
}

// pageTitle maps a page name to its display title for the topbar and <title>.
func pageTitle(page string) string {
	switch page {
	case "index":
		return "Overview"
	case "login":
		return "Login"
	case "setup":
		return "Setup"
	case "commands":
		return "Commands"
	case "guild":
		return "Server"
	case "modules":
		return "Modules"
	case "settings":
		return "Settings"
	case "permissions":
		return "Permissions"
	case "logs":
		return "Logs"
	}
	if page == "" {
		return "Dashboard"
	}
	return strings.ToUpper(page[:1]) + page[1:]
}

// initial returns the first rune of s for avatar fallbacks ("✦" when empty).
func initial(s string) string {
	if s == "" {
		return "✦"
	}
	return string([]rune(s)[0])
}

// loadTemplates parses all embedded page templates with the shared FuncMap.
func loadTemplates() (*templateBundle, error) {
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(templateFiles, "web/templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &templateBundle{tmpl: tmpl}, nil
}

// render executes the named page template with the shared renderData.
func (b *templateBundle) render(w io.Writer, page string, data renderData) error {
	if data.Page == "" {
		data.Page = page
	}
	return b.tmpl.ExecuteTemplate(w, page, data)
}

// staticFS returns the embedded static subtree rooted at "web/static".
func staticSubFS() fs.FS {
	sub, err := fs.Sub(staticFiles, "web/static")
	if err != nil {
		panic(err)
	}
	return sub
}
