package dashboard

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/misfit/bot/modules"
)

// api3.go — dashboard ↔ image filter module integration.
//
// Provider resolution mirrors ticketProvider(): the dashboard never imports
// the builtin; it resolves modules.ImageFilterAdmin through the manager.
//
// Routes (all JSON):
//
//	GET    /api/guilds/<gid>/imagefilter              config + images + model status
//	POST   /api/guilds/<gid>/imagefilter              partial config update
//	POST   /api/guilds/<gid>/imagefilter/enable       toggle the filter for one guild
//	POST   /api/guilds/<gid>/imagefilter/images       add (multipart upload or CDN url)
//	DELETE /api/guilds/<gid>/imagefilter/images/<name> remove one reference image
//	GET    /api/imagefilter/status                    owner-only: model panel
//	POST   /api/imagefilter/variant                   owner-only: switch CLIP variant
//
// Guards: guild endpoints need staff+ who manages THAT guild (canManageGuild),
// bot-wide endpoints are lvlOwner only (same gate as /api/updater), mutating
// calls check CSRF like every other mutating endpoint. Enforcement lives in
// the dispatcher (routeImageFilterAPI), handlers stay thin.

func (m *DashboardModule) imageFilterAdmin() (modules.ImageFilterAdmin, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("imagefilter")
	if !ok {
		return nil, false
	}
	adm, ok := mod.(modules.ImageFilterAdmin)
	return adm, ok
}

// routeImageFilterAPI dispatches /api/imagefilter* (parts[0] == "imagefilter")
// and the guild-scoped /api/guilds/<gid>/imagefilter... subtree. Called from
// routeAPI; meth is the upper-cased request method, parts the path after /api.
func (m *DashboardModule) routeImageFilterAPI(w http.ResponseWriter, r *http.Request, meth string, parts []string) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch {

	// ── Bot-wide (owner only): /api/imagefilter[/...] ────────────────────────
	case parts[0] == "imagefilter":
		if us == nil || m.resolveLevel(us) != lvlOwner {
			writeError(w, http.StatusForbidden, "owner only")
			return
		}
		adm, ok := m.imageFilterAdmin()
		if !ok {
			writeError(w, http.StatusNotFound, "imagefilter module not loaded")
			return
		}
		switch {
		case meth == "GET" && len(parts) == 2 && parts[1] == "status":
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  adm.Status(),
				"variant": adm.Variant(),
			})
		case meth == "POST" && len(parts) == 2 && parts[1] == "variant":
			if !m.checkCSRF(r) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			var body struct {
				Variant string `json:"variant"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if err := adm.SetVariant(strings.TrimSpace(body.Variant)); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusNotFound, "not found")
		}

	// ── Guild-scoped: /api/guilds/<gid>/imagefilter[/...] ────────────────────
	case parts[0] == "guilds" && len(parts) >= 3 && parts[2] == "imagefilter":
		gid := parts[1]
		// Validate the guild id BEFORE any filesystem access: the module
		// builds <dataDir>/spam_images/<gid>/ from it, so ".", "..", "null"
		// (which Parse accepts as 0) or anything with a separator must be
		// refused here rather than reaching the module.
		if !validGuildID(gid) {
			writeError(w, http.StatusBadRequest, "invalid guild id")
			return
		}
		adm, ok := m.imageFilterAdmin()
		if !ok {
			writeError(w, http.StatusNotFound, "imagefilter module not loaded")
			return
		}
		// Staff+ managing THIS guild (same rule as guild-scoped module config).
		if us == nil || !m.canManageGuild(us, gid) {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}

		sub := ""
		if len(parts) >= 4 {
			sub = parts[3]
		}
		switch {
		case meth == "GET" && sub == "":
			writeImageFilterOverview(w, adm, gid)

		case meth == "POST" && sub == "":
			if !m.checkCSRF(r) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			var body map[string]string
			dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
			if err := dec.Decode(&body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if err := adm.SetGuildConfig(gid, body); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		case meth == "POST" && sub == "enable":
			if !m.checkCSRF(r) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			enabled, err := parseImageFilterEnableBody(readAll(r))
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := adm.SetGuildEnabled(gid, enabled); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		case meth == "POST" && sub == "images":
			if !m.checkCSRF(r) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			m.apiImageFilterAddImages(w, r, adm, gid)

		case meth == "GET" && sub == "raw" && len(parts) == 4:
			// Gallery thumbnails: /api/guilds/<gid>/imagefilter/raw?name=<file>
			// (len 4: guilds, gid, imagefilter, raw). The name travels in the
			// query string, not as a path segment.
			// Same-orientation guard as DELETE: staff managing this guild
			// (checked above), name via filepath.Base.
			name := filepath.Base(r.URL.Query().Get("name"))
			if name == "" || name == "." || name == ".." {
				writeError(w, http.StatusBadRequest, "invalid image name")
				return
			}
			data, err := adm.ReadImage(gid, name)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			// Content type from a sniff (all stored files passed image decode).
			w.Header().Set("Content-Type", http.DetectContentType(data))
			w.Header().Set("Cache-Control", "private, max-age=60")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)

		case meth == "DELETE" && sub == "images" && len(parts) == 5:
			if !m.checkCSRF(r) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			name := filepath.Base(parts[4])
			if name == "" || name == "." || name == ".." {
				writeError(w, http.StatusBadRequest, "invalid image name")
				return
			}
			if err := adm.RemoveImage(gid, name); err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		default:
			writeError(w, http.StatusNotFound, "not found")
		}

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// writeImageFilterOverview answers GET with the full guild panel payload.
func writeImageFilterOverview(w http.ResponseWriter, adm modules.ImageFilterAdmin, gid string) {
	cfg, err := adm.GetGuildConfig(gid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	images, err := adm.ListImages(gid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config":  cfg,
		"images":  images,
		"status":  adm.Status(),
		"enabled": cfg["enabled"] == "true",
	})
}

// apiImageFilterAddImages handles the two add paths: multipart upload (field
// "file", one or more) or JSON {"url": "..."}.
func (m *DashboardModule) apiImageFilterAddImages(w http.ResponseWriter, r *http.Request, adm modules.ImageFilterAdmin, gid string) {
	ct := r.Header.Get("Content-Type")
	ctBase := strings.TrimSpace(strings.Split(ct, ";")[0])
	switch ctBase {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(55 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid multipart form")
			return
		}
		form := r.MultipartForm
		if form == nil || len(form.File["file"]) == 0 {
			writeError(w, http.StatusBadRequest, "no files uploaded (field \"file\")")
			return
		}
		var added []string
		for _, fh := range form.File["file"] {
			f, err := fh.Open()
			if err != nil {
				writeError(w, http.StatusBadRequest, "cannot read upload "+fh.Filename)
				return
			}
			data, err := io.ReadAll(io.LimitReader(f, maxImageFilterBytes+1))
			f.Close()
			if err != nil {
				writeError(w, http.StatusBadRequest, "cannot read upload "+fh.Filename)
				return
			}
			if len(data) > maxImageFilterBytes {
				writeError(w, http.StatusRequestEntityTooLarge, fh.Filename+" exceeds 50 MiB")
				return
			}
			name, err := adm.AddImageFromBytes(gid, data, fh.Filename)
			if err != nil {
				writeError(w, http.StatusBadRequest, fh.Filename+": "+err.Error())
				return
			}
			added = append(added, name)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})

	default:
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
			writeError(w, http.StatusBadRequest, "expected multipart upload or JSON {\"url\": \"...\"}")
			return
		}
		name, err := adm.AddImageFromURL(gid, body.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": []string{name}})
	}
}

// maxImageFilterBytes is the per-file read cap (mirrors the module's 50 MiB).
const maxImageFilterBytes = 50 * 1024 * 1024

// validGuildID mirrors the module's own directory-name rule: a Discord
// snowflake is digits only and non-zero. "null" must be rejected explicitly
// because snowflake.Parse maps it to (0, nil).
func validGuildID(gid string) bool {
	if gid == "" || gid == "0" {
		return false
	}
	for _, r := range gid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// readAll reads the request body with a 1 MiB cap (config/enable JSON bodies).
func readAll(r *http.Request) []byte {
	data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return data
}

// parseImageFilterEnableBody decodes {"enabled": bool} — extracted pure helper
// (table-tested in api3_test.go).
func parseImageFilterEnableBody(body []byte) (bool, error) {
	var v struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false, errors.New("invalid JSON body")
	}
	if v.Enabled == nil {
		return false, errors.New("missing \"enabled\" field")
	}
	return *v.Enabled, nil
}
