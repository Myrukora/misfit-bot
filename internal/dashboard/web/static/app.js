/* app.js — vanilla JS for the dashboard.
 * Fetch-based, no framework. CSRF token comes from <meta name="csrf-token">.
 * Each page's script block activates only when its elements exist.
 */
(function () {
  "use strict";

  var CSRF = (document.querySelector('meta[name="csrf-token"]') || {}).content || "";

  function api(method, url, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined && !(body instanceof FormData)) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    } else if (body instanceof FormData) {
      opts.body = body;
    }
    if (method !== "GET") opts.headers["X-CSRF-Token"] = CSRF;
    return fetch(url, opts).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) throw new Error(data.error || ("HTTP " + res.status));
        return data;
      });
    });
  }

  function toast(msg, isErr) {
    var el = document.createElement("div");
    el.className = "toast" + (isErr ? " toast-err" : "");
    el.textContent = msg;
    document.body.appendChild(el);
    setTimeout(function () { el.classList.add("show"); }, 10);
    setTimeout(function () { el.classList.remove("show"); el.remove(); }, 3200);
  }

  function byId(id) { return document.getElementById(id); }

  /* ---------- user dropdown (hover handled in CSS; click toggles too) ---------- */
  var avatar = document.querySelector(".user-avatar");
  if (avatar) {
    avatar.addEventListener("click", function () {
      var menu = avatar.parentElement.querySelector(".user-menu");
      if (menu) menu.style.display = menu.style.display === "block" ? "none" : "block";
    });
  }

  /* ---------- mobile: off-canvas sidebar drawer + touch dropdown ---------- */
  var sidebar = document.getElementById("sidebar");
  var backdrop = document.querySelector(".sidebar-backdrop");
  var menuToggle = document.querySelector(".menu-toggle");

  function setDrawer(open) {
    if (!sidebar) return;
    sidebar.classList.toggle("open", open);
    if (backdrop) backdrop.classList.toggle("show", open);
    document.body.style.overflow = open ? "hidden" : "";
  }

  if (menuToggle && sidebar) {
    menuToggle.addEventListener("click", function () {
      setDrawer(!sidebar.classList.contains("open"));
    });
  }
  if (backdrop) {
    backdrop.addEventListener("click", function () { setDrawer(false); });
  }
  // Tapping a nav link closes the drawer (mobile only).
  if (sidebar) {
    sidebar.addEventListener("click", function (e) {
      if (e.target.closest("a")) setDrawer(false);
    });
  }
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && sidebar && sidebar.classList.contains("open")) setDrawer(false);
  });

  // Touch: the avatar dropdown is hover-driven on desktop (no hover on touch),
  // so a tap toggles the .open class that the (hover:none) CSS reveals.
  if (avatar) {
    avatar.addEventListener("click", function () {
      var wrap = avatar.parentElement;
      if (wrap) wrap.classList.toggle("open");
    });
    // Tapping elsewhere closes the touch dropdown.
    document.addEventListener("click", function (e) {
      var wrap = avatar.parentElement;
      if (wrap && wrap.classList.contains("open") && !wrap.contains(e.target)) {
        wrap.classList.remove("open");
      }
    });
  }

  /* ---------- overview: live metrics poll ---------- */
  var latencyEl = byId("m-latency");
  if (latencyEl) {
    var memEl = byId("m-mem"), sysEl = byId("m-sys"), gorosEl = byId("m-goros"), upEl = byId("m-uptime");
    setInterval(function () {
      api("GET", "/api/metrics").then(function (s) {
        if (latencyEl) latencyEl.textContent = s.gateway_latency || latencyEl.textContent;
        if (upEl) upEl.textContent = s.uptime || upEl.textContent;
        if (memEl && s.runtime) memEl.textContent = s.runtime.alloc_mb + " MB";
        if (sysEl && s.runtime) sysEl.textContent = s.runtime.sys_mb + " MB";
        if (gorosEl && s.runtime) gorosEl.textContent = s.runtime.goroutines;
      }).catch(function () { /* transient poll errors stay silent */ });
    }, 5000);
  }

  /* ---------- permissions page ---------- */
  var addEl = byId("add-elevated");
  if (addEl) {
    addEl.addEventListener("click", function () {
      var id = (byId("add-elevated-id") || {}).value || "";
      if (!id.trim()) return;
      api("POST", "/api/permissions/elevated/add", { id: id.trim() })
        .then(function () { location.reload(); })
        .catch(function (e) { toast(e.message, true); });
    });
    document.querySelectorAll("[data-remove-elevated]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!confirm("Remove elevated access for this user?")) return;
        api("POST", "/api/permissions/elevated/remove", { id: btn.dataset.removeElevated })
          .then(function () { location.reload(); })
          .catch(function (e) { toast(e.message, true); });
      });
    });
  }

  /* ---------- logs page: client-side filter ---------- */
  var logLevel = byId("log-level");
  if (logLevel) {
    var logSearch = byId("log-search");
    var applyLogFilter = function () {
      var lvl = logLevel.value, q = (logSearch ? logSearch.value : "").toLowerCase();
      document.querySelectorAll("#log-viewer .log-line").forEach(function (line) {
        var text = line.textContent.toLowerCase();
        var okLvl = lvl === "all" || text.indexOf("\"level\":\"" + lvl + "\"") !== -1 ||
                    text.indexOf(" " + lvl.toUpperCase() + " ") !== -1;
        var okQ = !q || text.indexOf(q) !== -1;
        line.style.display = okLvl && okQ ? "" : "none";
      });
    };
    logLevel.addEventListener("change", applyLogFilter);
    if (logSearch) logSearch.addEventListener("input", applyLogFilter);
  }

  /* ---------- config field collection (shared by section + module saves) ---
   * Mirrors the field partial's contract: .field[data-key] wrappers carry the
   * key/type/guild context, .cfg-field controls carry the value. Toggles read
   * checked, multi groups join checked boxes with newlines, blank secrets are
   * skipped (keep existing value), owner-only (locked) fields never submit. */
  function collectFields(scope) {
    var tasks = [];
    scope.querySelectorAll(".cfg-field").forEach(function (field) {
      var wrap = field.closest(".field");
      if (!wrap) return;
      if (wrap.dataset.owneronly === "true") return;
      var key = wrap.dataset.key;
      var type = wrap.dataset.type;
      var guild = wrap.dataset.guild != null ? wrap.dataset.guild : "";
      var value;
      if (type === "toggle") {
        value = field.checked ? "true" : "false";
      } else if (type === "multi") {
        value = Array.prototype.slice.call(wrap.querySelectorAll(".multi input:checked")).map(function (c) { return c.value; }).join("\n");
      } else {
        value = field.value;
      }
      if (type === "secret" && value === "") return;
      tasks.push({ key: key, value: value, guildID: guild });
    });
    return tasks;
  }

  /* ---------- admin (config) page: per-section save ---------- */
  document.querySelectorAll(".save-section").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var section = btn.closest(".config-section");
      if (!section) return;
      var body = {};
      collectFields(section).forEach(function (t) { body[t.key] = t.value; });
      var label = btn.textContent.replace("Save ", "");
      api("POST", "/api/settings/core", body)
        .then(function () { toast(label + " saved"); })
        .catch(function (e) { toast("Save failed: " + e.message, true); });
    });
  });

  /* ---------- admin: updater status panel (owner) ---------- */
  var updPanel = byId("updater-status");
  if (updPanel) {
    var statusRow = function (text, cls) {
      var d = document.createElement("div");
      if (cls) d.className = cls;
      d.textContent = text;
      return d;
    };
    var loadStatus = function () {
      api("GET", "/api/updater/status").then(function (s) {
        var rows = [
          statusRow((s.enabled === "true" ? "enabled" : "disabled") + " · " + (s.repo || "no repo") + "@" + s.branch,
            s.enabled === "true" ? "upd-ok" : "upd-err"),
          statusRow("interval " + s.interval + " · auto_pull " + s.auto_pull + " · notify " + (s.notify_channels || "—")),
          statusRow("last check " + s.last_check + " · last seen " + (s.last_sha || "—"))
        ];
        if (s.last_error) rows.push(statusRow("last error: " + s.last_error, "upd-err"));
        updPanel.replaceChildren.apply(updPanel, rows);
      }).catch(function (e) {
        updPanel.textContent = "updater unavailable: " + e.message;
      });
    };
    loadStatus();
    var updCheck = byId("upd-check");
    if (updCheck) updCheck.addEventListener("click", function () {
      api("POST", "/api/updater/check").then(function (r) {
        if (r.up_to_date) toast("Up to date (" + r.local_sha.slice(0, 7) + ")");
        else toast(r.behind + " new commit(s) available");
        loadStatus();
      }).catch(function (e) { toast(e.message, true); });
    });
    var updApply = byId("upd-apply");
    if (updApply) updApply.addEventListener("click", function () {
      if (!confirm("Pull, rebuild and restart the bot now?")) return;
      api("POST", "/api/updater/apply")
        .then(function () { toast("Update started — the bot will rebuild and restart"); })
        .catch(function (e) { toast(e.message, true); });
    });
    var updTest = byId("upd-test");
    if (updTest) updTest.addEventListener("click", function () {
      api("POST", "/api/updater/test")
        .then(function () { toast("Sample PR + commit embeds sent"); })
        .catch(function (e) { toast(e.message, true); });
    });
  }

  /* ---------- admin: CLIP variant switch ---------- */
  var variantSave = byId("clip-variant-save");
  if (variantSave) {
    variantSave.addEventListener("click", function () {
      var v = (byId("clip-variant") || {}).value;
      if (!v) return;
      if (!confirm("Switch CLIP model to '" + v + "'? The model unloads and reloads if any server uses the filter.")) return;
      api("POST", "/api/imagefilter/variant", { variant: v })
        .then(function () { toast("Model switched to " + v); })
        .catch(function (e) { toast(e.message, true); });
    });
  }

  /* ---------- image filter page ---------- */
  var ifEnabled = byId("if-enabled");
  if (ifEnabled) {
    var guildID = document.body.dataset.guild || (location.pathname.split("/")[2] || "");

    /* enable toggle → immediate API call */
    ifEnabled.addEventListener("change", function () {
      api("POST", "/api/guilds/" + guildID + "/imagefilter/enable", { enabled: ifEnabled.checked })
        .then(function () {
          toast(ifEnabled.checked ? "Filter enabled — model loading" : "Filter disabled");
          setTimeout(function () { location.reload(); }, 800);
        })
        .catch(function (e) { toast(e.message, true); ifEnabled.checked = !ifEnabled.checked; });
    });

    /* punishment select shows/hides the duration field */
    var punishSel = byId("if-punishment");
    if (punishSel) {
      punishSel.addEventListener("change", function () {
        var row = byId("if-mute-row");
        if (row) row.hidden = punishSel.value !== "mute";
      });
    }

    /* save settings */
    var saveBtn = byId("if-save");
    if (saveBtn) {
      saveBtn.addEventListener("click", function () {
        var body = {
          threshold: (byId("if-threshold") || {}).value || "",
          punishment: (byId("if-punishment") || {}).value || "none",
          mute_duration: (byId("if-mute-duration") || {}).value || "600",
          log_channel: (byId("if-log-channel") || {}).value || "",
          delete_on_none: String(!!(byId("if-delete-on-none") || {}).checked)
        };
        api("POST", "/api/guilds/" + guildID + "/imagefilter", body)
          .then(function () { toast("Detection settings saved"); })
          .catch(function (e) { toast(e.message, true); });
      });
    }

    /* add images (dropzone upload and/or URL) */
    var uploadEl = byId("if-upload");
    var dropEl = byId("if-drop");
    var chipsEl = byId("if-files");
    var addBtn = byId("if-add");

    function fmtSize(n) {
      if (n < 1024) return n + " B";
      if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
      return (n / 1024 / 1024).toFixed(1) + " MB";
    }

    function refreshAddState() {
      if (!addBtn) return;
      var urlEl = byId("if-url");
      var hasUrl = !!(urlEl && urlEl.value.trim());
      addBtn.disabled = !uploadEl || (uploadEl.files.length === 0 && !hasUrl);
    }

    function renderChips() {
      if (!uploadEl || !chipsEl) return;
      chipsEl.innerHTML = "";
      var files = uploadEl.files;
      if (!files || !files.length) { chipsEl.hidden = true; return; }
      chipsEl.hidden = false;
      for (var i = 0; i < files.length; i++) {
        var li = document.createElement("li");
        li.className = "file-chip";
        var name = document.createElement("span");
        name.className = "file-chip-name";
        name.textContent = files[i].name;
        name.title = files[i].name;
        var size = document.createElement("span");
        size.className = "file-chip-size";
        size.textContent = fmtSize(files[i].size);
        var rm = document.createElement("button");
        rm.type = "button";
        rm.className = "file-chip-remove";
        rm.setAttribute("aria-label", "Remove " + files[i].name);
        rm.textContent = "×";
        rm.addEventListener("click", function (idx) {
          return function () {
            var dl = uploadEl;
            var dt = dl instanceof DataTransfer ? dl : new DataTransfer();
            for (var k = 0; k < dl.files.length; k++) if (k !== idx) dt.items.add(dl.files[k]);
            uploadEl.files = dt.files;
            renderChips();
            refreshAddState();
          };
        }(i));
        li.appendChild(name);
        li.appendChild(size);
        li.appendChild(rm);
        chipsEl.appendChild(li);
      }
    }

    if (uploadEl) {
      uploadEl.addEventListener("change", function () {
        renderChips();
        refreshAddState();
      });
    }
    if (dropEl && uploadEl) {
      // Keyboard activation (the hidden input cannot be focused directly).
      dropEl.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          uploadEl.click();
        }
      });
      ["dragenter", "dragover"].forEach(function (evt) {
        dropEl.addEventListener(evt, function (e) {
          e.preventDefault();
          dropEl.classList.add("drag-over");
        });
      });
      ["dragleave", "drop"].forEach(function (evt) {
        dropEl.addEventListener(evt, function (e) {
          e.preventDefault();
          dropEl.classList.remove("drag-over");
        });
      });
      dropEl.addEventListener("drop", function (e) {
        if (!e.dataTransfer || !e.dataTransfer.files || !e.dataTransfer.files.length) return;
        try { uploadEl.files = e.dataTransfer.files; } catch (err) { /* older browsers */ }
        renderChips();
        refreshAddState();
      });
    }
    var urlInput = byId("if-url");
    if (urlInput) {
      urlInput.addEventListener("input", refreshAddState);
    }
    refreshAddState();

    if (addBtn) {
      addBtn.addEventListener("click", function () {
        var files = (byId("if-upload") || {}).files;
        var urlEl = byId("if-url");
        var work = Promise.resolve();

        if (files && files.length) {
          var fd = new FormData();
          for (var i = 0; i < files.length; i++) fd.append("file", files[i]);
          work = work.then(function () {
            return api("POST", "/api/guilds/" + guildID + "/imagefilter/images", fd);
          });
        }
        if (urlEl && urlEl.value.trim()) {
          var u = urlEl.value.trim();
          work = work.then(function () {
            return api("POST", "/api/guilds/" + guildID + "/imagefilter/images", { url: u });
          });
        }
        work.then(function () { location.reload(); })
          .catch(function (e) { toast(e.message, true); });
      });
    }

    /* remove image */
    document.querySelectorAll(".image-remove").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var name = btn.dataset.remove;
        if (!confirm("Remove this image from the blacklist?")) return;
        api("DELETE", "/api/guilds/" + guildID + "/imagefilter/images/" + encodeURIComponent(name))
          .then(function () {
            var tile = btn.closest(".image-tile");
            if (tile) tile.remove();
            if (!document.querySelector(".image-tile")) {
              var empty = byId("if-empty");
              if (empty) empty.hidden = false;
            }
            toast("Image removed");
          })
          .catch(function (e) { toast(e.message, true); });
      });
    });
  }

  /* ---------- modules page: load/unload/reload ---------- */
  document.querySelectorAll(".act[data-action]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var tr = btn.closest("tr");
      var name = tr.dataset.module;
      var action = btn.dataset.action;
      btn.disabled = true;
      api("POST", "/api/modules/" + encodeURIComponent(name) + "/" + action)
        .then(function () {
          toast(action + "ed " + name);
          setTimeout(function () { location.reload(); }, 700);
        })
        .catch(function (e) {
          toast(e.message, true);
          btn.disabled = false;
        });
    });
  });

  /* ---------- module config save (WebConfigurable) ---------- */
  document.querySelectorAll(".save-module").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var form = btn.closest("form");
      if (!form) return;
      var mod = form.dataset.module;
      var tasks = collectFields(form);
      btn.disabled = true;
      var done = 0, failed = [];
      var next = function (i) {
        if (i >= tasks.length) {
          btn.disabled = false;
          if (failed.length === 0) {
            toast(mod + " settings saved (" + done + ")" + (done < tasks.length ? " — " + (tasks.length - done) + " unchanged" : ""));
          } else {
            toast(mod + ": saved " + done + "/" + tasks.length + ", failed: " + failed.join(", "), true);
          }
          return;
        }
        api("POST", "/api/settings/module/" + encodeURIComponent(mod), tasks[i])
          .then(function () { done++; next(i + 1); })
          .catch(function () { failed.push(tasks[i].key); next(i + 1); });
      };
      next(0);
    });
  });

  /* ---------- tickets: dashboard-managed configuration ---------- */
  var tkTypes = byId("tickets-types");
  var tkPanels = byId("tickets-panels");
  var tkSettings = byId("tickets-settings");
  var tkGuild = (tkTypes && tkTypes.dataset.guild) ||
    (tkPanels && tkPanels.dataset.guild) ||
    (tkSettings && tkSettings.dataset.guild) || "";
  var tkBase = "/api/tickets/" + encodeURIComponent(tkGuild);

  var tkSetChecked = function (box, ids) {
    var map = {};
    (ids || []).forEach(function (id) { map[id] = true; });
    box.querySelectorAll("input[type=checkbox]").forEach(function (inp) { inp.checked = !!map[inp.value]; });
    if (box.__tkRender) box.__tkRender();
  };
  var tkChecked = function (box) {
    return Array.prototype.slice.call(box.querySelectorAll("input[type=checkbox]:checked"))
      .map(function (inp) { return inp.value; });
  };
  var tkShow = function (modal, show) { if (modal) modal.hidden = !show; };
  var tkFail = function (e) { toast(e.message, true); };
  var tkHasOption = function (sel, value) {
    return Array.prototype.some.call(sel.options, function (o) { return o.value === value; });
  };

  /* Role picker: a searchable dropdown over the guild's roles. The menu
     expands inline (the modal scrolls, so a floating popover would clip). */
  var tkRenderPicker = function (box) {
    var list = box.querySelector(".picker-list");
    var value = box.querySelector(".picker-value");
    var chips = box.querySelector(".picker-chips");
    if (!list || !value || !chips) return;
    var selected = Array.prototype.slice.call(list.querySelectorAll("input[type=checkbox]:checked"));
    var names = selected.map(function (inp) {
      var row = inp.closest(".picker-option");
      return row ? row.querySelector("span").textContent : inp.value;
    });
    value.textContent = names.length === 0 ? "No roles selected"
      : names.length === 1 ? names[0] : names.length + " roles selected";
    chips.innerHTML = "";
    names.forEach(function (name, i) {
      var chip = document.createElement("span");
      chip.className = "picker-chip";
      chip.appendChild(document.createTextNode(name));
      var x = document.createElement("button");
      x.type = "button";
      x.className = "picker-chip-x";
      x.setAttribute("aria-label", "Remove " + name);
      x.textContent = "×";
      x.addEventListener("click", function (e) {
        e.stopPropagation();
        selected[i].checked = false;
        tkRenderPicker(box);
      });
      chip.appendChild(x);
      chips.appendChild(chip);
    });
    chips.hidden = names.length === 0;
  };

  var tkInitPicker = function (box) {
    if (box.dataset.pickerReady) return;
    box.dataset.pickerReady = "1";
    var trigger = box.querySelector(".picker-trigger");
    var panel = box.querySelector(".picker-panel");
    var search = box.querySelector(".picker-search");
    var list = box.querySelector(".picker-list");
    if (!trigger || !panel || !search || !list) return;
    var empty = box.querySelector(".picker-empty");
    var filter = function () {
      var q = search.value.trim().toLowerCase(), any = false;
      list.querySelectorAll(".picker-option").forEach(function (row) {
        var hit = row.textContent.toLowerCase().indexOf(q) !== -1;
        row.hidden = !hit;
        if (hit) any = true;
      });
      if (empty) empty.hidden = any;
    };
    var setOpen = function (open) {
      panel.hidden = !open;
      trigger.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) {
        search.value = "";
        filter();
        search.focus();
      }
    };
    trigger.addEventListener("click", function () { setOpen(panel.hidden); });
    search.addEventListener("input", filter);
    list.addEventListener("change", function () { tkRenderPicker(box); });
    document.addEventListener("click", function (e) {
      if (!box.contains(e.target)) setOpen(false);
    });
    box.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { setOpen(false); trigger.focus(); }
    });
    box.__tkRender = function () { tkRenderPicker(box); };
    tkRenderPicker(box);
  };

  document.querySelectorAll("[data-picker]").forEach(tkInitPicker);

  /* ---------- tickets: close from list or transcript ---------- */
  document.querySelectorAll(".tk-close").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var guild = btn.dataset.guild;
      var id = btn.dataset.id;
      if (!confirm("Close ticket " + id + "? This cannot be undone.")) return;
      var url = btn.dataset.closeurl || ("/api/tickets/" + encodeURIComponent(guild) + "/" + encodeURIComponent(id) + "/close");
      btn.disabled = true;
      api("POST", url, {})
        .then(function () {
          toast("Ticket " + id + " closed");
          setTimeout(function () { location.reload(); }, 700);
        })
        .catch(function (e) {
          toast(e.message, true);
          btn.disabled = false;
        });
    });
  });

  /* ---------- tickets: settings (log channel) ---------- */
  if (tkSettings) {
    var tkLog = byId("tk-log-channel");
    var tkSettingsSave = byId("tk-settings-save");
    if (tkLog && tkSettingsSave) {
      tkSettingsSave.addEventListener("click", function () {
        tkSettingsSave.disabled = true;
        api("POST", tkBase + "/settings", { log_channel: tkLog.value })
          .then(function () { toast("Ticket settings saved"); })
          .catch(tkFail)
          .finally(function () { tkSettingsSave.disabled = false; });
      });
    }
  }

  /* ---------- tickets: type editor (shared modal) ---------- */
  var typeModal = byId("tk-type-modal");
  if (typeModal && tkTypes) {
    var typeKey = byId("tk-type-key"), typeLabel = byId("tk-type-label"),
      typeCategory = byId("tk-type-category"), typeChannel = byId("tk-type-channel"),
      typePing = byId("tk-type-ping"),
      typeHelper = byId("tk-type-helper"), typeAccess = byId("tk-type-access"),
      typeWelcome = byId("tk-type-welcome"), typeBody = byId("tk-type-body"),
      typeButton = byId("tk-type-button"), typeEmoji = byId("tk-type-emoji"),
      typeColor = byId("tk-type-color"), typeEnabled = byId("tk-type-enabled"),
      typeClaim = byId("tk-type-claim"), typeClose = byId("tk-type-close"),
      typeHeading = byId("tk-type-heading"), typeSave = byId("tk-type-save");
    var typeMode = "create";

    var typeColorHex = function (n) {
      var h = (n || 0).toString(16);
      while (h.length < 6) h = "0" + h;
      return "#" + h;
    };
    var typeColorInt = function (hex) {
      var h = (hex || "").replace("#", "");
      return /^[0-9a-fA-F]{6}$/.test(h) ? parseInt(h, 16) : 0;
    };

    var fillType = function (t) {
      typeMode = t ? "edit" : "create";
      typeHeading.textContent = t ? "Edit ticket type" : "New ticket type";
      typeKey.value = t ? t.key : "";
      typeKey.disabled = !!t;
      typeLabel.value = t ? t.label : "";
      if (t && t.category && !tkHasOption(typeCategory, t.category)) {
        var opt = document.createElement("option");
        opt.value = t.category;
        opt.textContent = t.category + " (unavailable)";
        typeCategory.appendChild(opt);
      }
      typeCategory.value = t && t.category ? t.category : "";
      typeChannel.value = t ? t.channel_name || "" : "";
      tkSetChecked(typePing, t ? t.ping_roles : []);
      tkSetChecked(typeHelper, t ? t.helper_roles : []);
      tkSetChecked(typeAccess, t ? t.access_roles : []);
      typeWelcome.value = t ? t.welcome_message || "" : "";
      typeBody.value = t ? t.embed_body || "" : "";
      typeButton.value = t ? t.button_label || "" : "";
      typeEmoji.value = t ? t.button_emoji || "" : "";
      typeColor.value = typeColorHex(t ? t.color : 0x5865f2);
      typeEnabled.checked = t ? !!t.enabled : true;
      typeClaim.checked = !t || t.allow_claim;
      typeClose.checked = !t || t.allow_close;
    };

    document.querySelectorAll(".tk-type-edit").forEach(function (btn) {
      btn.addEventListener("click", function () {
        btn.disabled = true;
        api("GET", tkBase + "/types/" + encodeURIComponent(btn.dataset.key))
          .then(function (t) { fillType(t); tkShow(typeModal, true); })
          .catch(tkFail)
          .finally(function () { btn.disabled = false; });
      });
    });
    document.querySelectorAll(".tk-type-delete").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var key = btn.dataset.key;
        if (!confirm("Delete type '" + key + "' and its panels? This cannot be undone.")) return;
        btn.disabled = true;
        api("POST", tkBase + "/types/" + encodeURIComponent(key) + "/delete", {})
          .then(function () { toast("Type " + key + " deleted"); location.reload(); })
          .catch(function (e) { tkFail(e); btn.disabled = false; });
      });
    });
    var typeAddBtn = byId("tk-type-add");
    if (typeAddBtn) typeAddBtn.addEventListener("click", function () { fillType(null); tkShow(typeModal, true); });
    var typeCloseBtn = byId("tk-type-close");
    if (typeCloseBtn) typeCloseBtn.addEventListener("click", function () { tkShow(typeModal, false); });
    var typeBackdrop = typeModal.querySelector(".gear-backdrop");
    if (typeBackdrop) typeBackdrop.addEventListener("click", function () { tkShow(typeModal, false); });

    if (typeSave) {
      typeSave.addEventListener("click", function () {
        var payload = {
          label: typeLabel.value.trim(),
          enabled: typeEnabled.checked,
          category: typeCategory.value,
          channel_name: typeChannel.value.trim(),
          ping_roles: tkChecked(typePing),
          helper_roles: tkChecked(typeHelper),
          access_roles: tkChecked(typeAccess),
          welcome_message: typeWelcome.value,
          embed_body: typeBody.value,
          button_label: typeButton.value.trim(),
          button_emoji: typeEmoji.value.trim(),
          color: typeColorInt(typeColor.value),
          allow_claim: typeClaim.checked,
          allow_close: typeClose.checked
        };
        var url = tkBase + "/types";
        if (typeMode === "edit") {
          payload.key = typeKey.value;
          url += "/" + encodeURIComponent(typeKey.value);
        } else {
          payload.key = typeKey.value.trim().toLowerCase();
        }
        typeSave.disabled = true;
        api("POST", url, payload)
          .then(function () { toast("Type saved"); location.reload(); })
          .catch(function (e) { tkFail(e); typeSave.disabled = false; });
      });
    }
  }

  /* ---------- tickets: panel editor (shared modal) ---------- */
  var panelModal = byId("tk-panel-modal");
  if (panelModal && tkPanels) {
    var pName = byId("tk-panel-name"), pType = byId("tk-panel-type"),
      pChannel = byId("tk-panel-channel"), pTitle = byId("tk-panel-title"),
      pDesc = byId("tk-panel-description"), pModalTitle = byId("tk-panel-modal-title"),
      pHeading = byId("tk-panel-heading"), pSave = byId("tk-panel-save"),
      pQuestions = byId("tk-panel-questions"), pQAdd = byId("tk-panel-qadd");
    var panelMode = "create";
    var qTpl = byId("tk-qrow-tpl");

    var syncQAdd = function () {
      if (pQAdd) pQAdd.disabled = pQuestions.querySelectorAll(".tk-qrow").length >= 5;
    };
    var bindQRow = function (row) {
      var rm = row.querySelector(".tk-qremove");
      if (rm) rm.addEventListener("click", function () { row.remove(); syncQAdd(); });
    };
    var addQRow = function (q) {
      if (!qTpl || pQuestions.querySelectorAll(".tk-qrow").length >= 5) return;
      var row = qTpl.content.firstElementChild.cloneNode(true);
      if (q) {
        row.querySelector(".tk-q-label").value = q.label || "";
        row.querySelector(".tk-q-style").value = q.style === "paragraph" ? "paragraph" : "short";
        row.querySelector(".tk-q-required").checked = !!q.required;
        row.querySelector(".tk-q-placeholder").value = q.placeholder || "";
        row.querySelector(".tk-q-value").value = q.value || "";
      }
      pQuestions.appendChild(row);
      bindQRow(row);
      syncQAdd();
    };

    var fillPanel = function (p) {
      panelMode = p ? "edit" : "create";
      pHeading.textContent = p ? "Edit panel" : "New panel";
      pName.value = p ? p.name : "";
      pName.disabled = !!p;
      if (p && p.type && !tkHasOption(pType, p.type)) {
        var opt = document.createElement("option");
        opt.value = p.type;
        opt.textContent = p.type + " (missing type)";
        pType.appendChild(opt);
      }
      pType.value = p ? p.type || "" : (pType.options[0] || {}).value || "";
      if (p && p.channel_id && !tkHasOption(pChannel, p.channel_id)) {
        var chOpt = document.createElement("option");
        chOpt.value = p.channel_id;
        chOpt.textContent = p.channel_id + " (unavailable)";
        pChannel.appendChild(chOpt);
      }
      pChannel.value = p && p.channel_id ? p.channel_id : (pChannel.options[0] || {}).value || "";
      pTitle.value = p ? p.title || "" : "";
      pDesc.value = p ? p.description || "" : "";
      pModalTitle.value = p ? p.modal_title || "" : "";
      pQuestions.innerHTML = "";
      (p && p.questions ? p.questions : []).forEach(addQRow);
      syncQAdd();
    };

    document.querySelectorAll(".tk-panel-edit").forEach(function (btn) {
      btn.addEventListener("click", function () {
        btn.disabled = true;
        api("GET", tkBase + "/panels/" + encodeURIComponent(btn.dataset.name))
          .then(function (p) { fillPanel(p); tkShow(panelModal, true); })
          .catch(tkFail)
          .finally(function () { btn.disabled = false; });
      });
    });
    document.querySelectorAll(".tk-panel-delete").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var name = btn.dataset.name;
        if (!confirm("Delete panel '" + name + "'? The posted message is removed too.")) return;
        btn.disabled = true;
        api("POST", tkBase + "/panels/" + encodeURIComponent(name) + "/delete", {})
          .then(function () { toast("Panel " + name + " deleted"); location.reload(); })
          .catch(function (e) { tkFail(e); btn.disabled = false; });
      });
    });
    var panelAddBtn = byId("tk-panel-add");
    if (panelAddBtn) panelAddBtn.addEventListener("click", function () { fillPanel(null); tkShow(panelModal, true); });
    var panelCloseBtn = byId("tk-panel-close");
    if (panelCloseBtn) panelCloseBtn.addEventListener("click", function () { tkShow(panelModal, false); });
    var panelBackdrop = panelModal.querySelector(".gear-backdrop");
    if (panelBackdrop) panelBackdrop.addEventListener("click", function () { tkShow(panelModal, false); });
    if (pQAdd) pQAdd.addEventListener("click", function () { addQRow(null); });

    if (pSave) {
      pSave.addEventListener("click", function () {
        var questions = [];
        pQuestions.querySelectorAll(".tk-qrow").forEach(function (row) {
          var label = (row.querySelector(".tk-q-label") || {}).value || "";
          label = label.trim();
          if (!label) return;
          questions.push({
            label: label,
            style: (row.querySelector(".tk-q-style") || {}).value || "short",
            required: !!(row.querySelector(".tk-q-required") || {}).checked,
            placeholder: ((row.querySelector(".tk-q-placeholder") || {}).value || "").trim(),
            value: ((row.querySelector(".tk-q-value") || {}).value || "").trim()
          });
        });
        var payload = {
          name: pName.value.trim(),
          channel_id: pChannel.value,
          type: pType.value,
          title: pTitle.value,
          description: pDesc.value,
          modal_title: pModalTitle.value.trim(),
          questions: questions
        };
        var url = tkBase + "/panels";
        if (panelMode === "edit") url += "/" + encodeURIComponent(pName.value);
        pSave.disabled = true;
        api("POST", url, payload)
          .then(function () { toast("Panel saved"); location.reload(); })
          .catch(function (e) { tkFail(e); pSave.disabled = false; });
      });
    }
  }

  /* ---------- tickets: panel actions (suspend/resume/resend) ---------- */
  document.querySelectorAll(".tk-panel-action").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var name = btn.dataset.name, action = btn.dataset.action;
      if (action === "resend" && !confirm("Post panel '" + name + "' again?")) return;
      btn.disabled = true;
      api("POST", tkBase + "/panels/" + encodeURIComponent(name) + "/" + action, {})
        .then(function () { toast("Panel " + name + " " + action + "ed"); location.reload(); })
        .catch(function (e) { tkFail(e); btn.disabled = false; });
    });
  });

  /* ---------- transcript page: image lightbox ---------- */
  var lightbox = byId("lightbox");
  if (lightbox && document.querySelector("img.zoomable")) {
    var lightboxImg = byId("lightbox-img");
    var closeLightbox = function () {
      lightbox.classList.remove("open");
      lightboxImg.removeAttribute("src");
    };
    document.querySelectorAll("img.zoomable").forEach(function (img) {
      img.addEventListener("click", function () {
        lightboxImg.src = img.src;
        lightbox.classList.add("open");
        lightbox.focus();
      });
    });
    lightbox.addEventListener("click", closeLightbox);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && lightbox.classList.contains("open")) closeLightbox();
    });
  }

  /* ---------- commands page: filter + tabs + run + gear ---------- */
  var splitList = function (s) {
    return (s || "").split(",").filter(function (x) { return x.length > 0; });
  };

  var cmdSearch = byId("cmd-search");
  if (cmdSearch) {
    cmdSearch.addEventListener("input", function () {
      var q = cmdSearch.value.toLowerCase();
      document.querySelectorAll(".cmd-row").forEach(function (row) {
        var t = row.textContent.toLowerCase();
        row.style.display = (!q || t.indexOf(q) !== -1) ? "" : "none";
      });
    });
  }

  var cmdTabs = document.querySelectorAll(".cmd-tab");
  if (cmdTabs.length) {
    var showTab = function (name) {
      cmdTabs.forEach(function (t) {
        var on = t.dataset.tab === name;
        t.classList.toggle("cmd-tab-active", on);
        t.setAttribute("aria-selected", on ? "true" : "false");
      });
      document.querySelectorAll(".cmd-grid").forEach(function (g) {
        g.hidden = g.dataset.tab !== name;
      });
    };
    cmdTabs.forEach(function (tab) {
      tab.addEventListener("click", function () { showTab(tab.dataset.tab); });
    });
  }

  /* Run button: executes via the universal web exec endpoint */
  document.querySelectorAll(".run-cmd").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var name = btn.dataset.name;
      var guild = btn.dataset.guild || "";
      var label = btn.textContent;
      btn.disabled = true;
      btn.textContent = "Running…";
      api("POST", "/api/exec", { command: name, args: [], guild: guild })
        .then(function (r) {
          toast((r.title ? "[" + r.title + "] " : "") + (r.text || r.description || "ok"));
        })
        .catch(function (e) { toast(name + ": " + e.message, true); })
        .finally(function () { btn.textContent = label; btn.disabled = false; });
    });
  });

  /* Row enable/disable toggle: flips the per-guild disabled override,
     preserving the command's mod-only + channel/role allowlists. */
  document.querySelectorAll("input[data-rowtoggle]").forEach(function (inp) {
    inp.addEventListener("change", function () {
      var name = inp.dataset.rowtoggle;
      var gearBtn = inp.closest(".cmd-row").querySelector(".gear-btn");
      if (!gearBtn) return;
      var payload = {
        name: name,
        disabled: !inp.checked,
        guildID: gearBtn.dataset.guild || "",
        channels: splitList(gearBtn.dataset.channels),
        roles: splitList(gearBtn.dataset.roles),
        modOnly: gearBtn.dataset.modonly === "true"
      };
      api("POST", "/api/cmdcfg/toggle", payload)
        .then(function () { location.reload(); })
        .catch(function (e) { toast(name + ": " + e.message, true); });
    });
  });

  /* Per-command config (gear) modal — per-guild mod-only + channel/role
     allowlists for this server. Enable/disable is the row toggle; the modal
     preserves it (it never changes the disabled flag). */
  var gearModal = byId("cmd-gear-modal");
  if (gearModal) {
    var gearName = byId("gear-cmd-name");
    var gearModOnly = byId("gear-modonly-toggle");
    var gearChannels = byId("gear-channels");
    var gearRoles = byId("gear-roles");
    var gearCurrent = { name: "", guild: "", disabled: false };

    var syncPicker = function (box, ids) {
      var map = {};
      (ids || []).forEach(function (id) { map[id] = true; });
      box.querySelectorAll("input").forEach(function (inp) { inp.checked = !!map[inp.value]; });
    };
    var selectedIDs = function (box) {
      return Array.prototype.slice.call(box.querySelectorAll("input:checked")).map(function (c) { return c.value; });
    };
    var gearClose = function () {
      gearModal.hidden = true;
      document.body.style.overflow = "";
    };

    document.querySelectorAll(".gear-btn").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var row = btn.closest(".cmd-row");
        var rowToggle = row ? row.querySelector("input[data-rowtoggle]") : null;
        gearCurrent = {
          name: btn.dataset.name,
          guild: btn.dataset.guild || "",
          disabled: rowToggle ? !rowToggle.checked : false
        };
        gearName.textContent = gearCurrent.name;
        gearModOnly.checked = btn.dataset.modonly === "true";
        syncPicker(gearChannels, splitList(btn.dataset.channels));
        syncPicker(gearRoles, splitList(btn.dataset.roles));
        gearModal.hidden = false;
        document.body.style.overflow = "hidden";
      });
    });
    gearModal.querySelector(".gear-backdrop").addEventListener("click", gearClose);
    byId("gear-close").addEventListener("click", gearClose);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !gearModal.hidden) gearClose();
    });

    byId("gear-save").addEventListener("click", function () {
      var btn = byId("gear-save");
      btn.disabled = true;
      var name = gearCurrent.name;
      api("POST", "/api/cmdcfg/toggle", {
        name: name,
        disabled: gearCurrent.disabled,
        guildID: gearCurrent.guild,
        channels: selectedIDs(gearChannels),
        roles: selectedIDs(gearRoles),
        modOnly: gearModOnly.checked
      }).then(function () {
        gearClose();
        location.reload();
      }).catch(function (e) {
        toast(name + ": " + e.message, true);
        btn.disabled = false;
      });
    });

    byId("gear-clear").addEventListener("click", function () {
      var btn = byId("gear-clear");
      btn.disabled = true;
      api("POST", "/api/cmdcfg/toggle", {
        name: gearCurrent.name,
        disabled: false,
        guildID: gearCurrent.guild,
        channels: [],
        roles: [],
        modOnly: false
      }).then(function () {
        toast("Overrides cleared for " + gearCurrent.name);
        gearClose();
        location.reload();
      }).catch(function (e) {
        toast(gearCurrent.name + ": " + e.message, true);
        btn.disabled = false;
      });
    });
  }
})();
