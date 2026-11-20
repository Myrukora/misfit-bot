package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/disgoorg/snowflake/v2"

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
			if err := readJSON(r.Body, &body); err != nil {
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
			if err := readJSON(r.Body, &body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if err := m.checkChannelInGuild(gid, body["log_channel"]); err != nil {
				writeError(w, http.StatusBadRequest, "log channel: "+err.Error())
				return
			}
			current, err := adm.GetGuildConfig(gid)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			effectivePunishment := current["punishment"]
			if v, ok := body["punishment"]; ok {
				effectivePunishment = v
			}
			if !m.punishmentAllowed(us, gid, effectivePunishment) {
				writeError(w, http.StatusForbidden, "you lack the Discord permission for this punishment")
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
			if enabled {
				current, err := adm.GetGuildConfig(gid)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				if !m.punishmentAllowed(us, gid, current["punishment"]) {
					writeError(w, http.StatusForbidden, "you lack the Discord permission for the configured punishment")
					return
				}
				if err := m.checkChannelInGuild(gid, current["log_channel"]); err != nil {
					writeError(w, http.StatusBadRequest, "log channel: "+err.Error())
					return
				}
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
// "file", one or more) or JSON {"url": "..."} (a comma/whitespace separated
// list is accepted too, so a pasted column of CDN links is one request).
//
// A batch is PARTIAL-SUCCESS aware: one undecodable file must not discard the
// files that already landed, so per-file failures are collected into "failed"
// instead of aborting the request. Only a structurally unusable request (no
// files at all) is a 400.
func (m *DashboardModule) apiImageFilterAddImages(w http.ResponseWriter, r *http.Request, adm modules.ImageFilterAdmin, gid string) {
	ct := r.Header.Get("Content-Type")
	ctBase := strings.TrimSpace(strings.Split(ct, ";")[0])
	switch ctBase {
	case "multipart/form-data":
		// A batch is many files in one request, so the body cap has to cover a
		// whole guild budget rather than one image. Without it a single request
		// could stream unbounded data while ParseMultipartForm spills to disk.
		r.Body = http.MaxBytesReader(w, r.Body, maxImageFilterBatchBytes)
		if err := r.ParseMultipartForm(55 << 20); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("batch exceeds %d MiB", maxImageFilterBatchBytes>>20))
				return
			}
			writeError(w, http.StatusBadRequest, "invalid multipart form")
			return
		}
		form := r.MultipartForm
		if form == nil || len(form.File["file"]) == 0 {
			writeError(w, http.StatusBadRequest, "no files uploaded (field \"file\")")
			return
		}
		added := []string{}
		failed := []string{}
		for _, fh := range form.File["file"] {
			name, err := storeUploadedImage(adm, gid, fh)
			if err != nil {
				// Keep going: the rest of the batch is still worth storing.
				failed = append(failed, fh.Filename+": "+err.Error())
				continue
			}
			added = append(added, name)
		}
		// 200 whenever at least one file landed, so the dashboard can report
		// "N added, M failed" instead of an error for a usable batch.
		status := http.StatusOK
		if len(added) == 0 {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{
			"ok":     len(added) > 0,
			"added":  added,
			"failed": failed,
		})

	default:
		var body struct {
			URL string `json:"url"`
		}
		if err := readJSON(r.Body, &body); err != nil || strings.TrimSpace(body.URL) == "" {
			writeError(w, http.StatusBadRequest, "expected multipart upload or JSON {\"url\": \"...\"}")
			return
		}
		urls := splitURLList(body.URL)
		if len(urls) == 0 {
			writeError(w, http.StatusBadRequest, "no URLs given")
			return
		}
		added := []string{}
		failed := []string{}
		for _, u := range urls {
			name, err := adm.AddImageFromURL(gid, u)
			if err != nil {
				failed = append(failed, u+": "+err.Error())
				continue
			}
			added = append(added, name)
		}
		status := http.StatusOK
		if len(added) == 0 {
			// A single URL keeps the historical shape: the error IS the body.
			if len(urls) == 1 {
				writeError(w, http.StatusBadRequest, failed[0])
				return
			}
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{
			"ok":     len(added) > 0,
			"added":  added,
			"failed": failed,
		})
	}
}

// storeUploadedImage reads, size-checks and stores one multipart file part.
func storeUploadedImage(adm modules.ImageFilterAdmin, gid string, fh *multipart.FileHeader) (string, error) {
	f, err := fh.Open()
	if err != nil {
		return "", errors.New("cannot read upload")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageFilterBytes+1))
	if err != nil {
		return "", errors.New("cannot read upload")
	}
	if len(data) > maxImageFilterBytes {
		return "", fmt.Errorf("exceeds %d MiB", maxImageFilterBytes>>20)
	}
	return adm.AddImageFromBytes(gid, data, fh.Filename)
}

// splitURLList accepts one URL, or a pasted list separated by commas and/or
// whitespace (a column of CDN links is the common case).
func splitURLList(raw string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if u := strings.TrimSpace(f); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// maxImageFilterBytes is the per-file read cap (mirrors the module's 50 MiB).
const maxImageFilterBytes = 50 * 1024 * 1024

// maxImageFilterBatchBytes caps one upload request. It is the module's
// per-guild byte budget (250 MiB) plus multipart framing slack, so a batch that
// could never be accepted is refused before it is read.
const maxImageFilterBatchBytes = 250*1024*1024 + 8*1024*1024

// validGuildID mirrors the module's own directory-name rule: a canonical,
// non-zero Discord snowflake. "null" must be rejected explicitly because
// snowflake.Parse maps it to (0, nil); the String() round-trip rejects leading
// zeros and values that overflow the snowflake range.
func validGuildID(gid string) bool {
	if gid == "" || gid == "0" {
		return false
	}
	id, err := snowflake.Parse(gid)
	return err == nil && id != 0 && id.String() == gid
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
