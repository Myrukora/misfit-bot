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
	Bot        string
	User       *userJSON
	Level      string
	Guilds     []guildOpt
	CSRF       string
	Page       string // page title
	Content    any    // page-specific payload
	ShowConfig bool   // owner/elevated: show core config nav
	ShowStaff  bool   // staff+: show guild nav
	IsOwner    bool
	IsElevated bool
	IsStaff    bool
	IsRegular  bool
	Raw        bool
}

var tmplFuncs = template.FuncMap{
	"join":        strings.Join,
	"joinOr":      joinOr,
	"yesno":       yesno,
	"csvContains": csvContains,
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

func joinOr(s []string, sep, or string) string {
	if len(s) == 0 {
		return or
	}
	return strings.Join(s, sep)
}

func yesno(b bool) string {
	if b {
		return "✅"
	}
	return "⛔"
}

func loadTemplates() (*templateBundle, error) {
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(templateFiles, "web/templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &templateBundle{tmpl: tmpl}, nil
}

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
