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

  /* ---------- admin: bot lifecycle (owner + elevated) ---------- */
  var botRestart = byId("bot-restart");
  if (botRestart) botRestart.addEventListener("click", function () {
    if (!confirm("Restart the bot now?")) return;
    api("POST", "/api/restart")
      .then(function () { toast("Restarting…"); })
      .catch(function (e) { toast(e.message, true); });
  });
  var botShutdown = byId("bot-shutdown");
  if (botShutdown) botShutdown.addEventListener("click", function () {
    if (!confirm("Shut the bot down? It stays offline until started again.")) return;
    api("POST", "/api/shutdown")
      .then(function () { toast("Shutting down…"); })
      .catch(function (e) { toast(e.message, true); });
  });

  /* ---------- admin: rate limit status/reset (owner) ---------- */
  var rlCheck = byId("rl-check");
  if (rlCheck) {
    var rlUser = byId("rl-user");
    var rlResult = byId("rl-result");
    var rlUserID = function () {
      var id = ((rlUser || {}).value || "").trim();
      if (!id) { toast("Enter a user ID", true); return ""; }
      return id;
    };
    rlCheck.addEventListener("click", function () {
      var id = rlUserID();
      if (!id) return;
      api("GET", "/api/ratelimit/status?user_id=" + encodeURIComponent(id))
        .then(function (r) {
          rlResult.textContent = r.allowed
            ? "User " + id + " is allowed to use commands."
            : "User " + id + " is rate limited — wait " + r.wait_seconds + "s.";
        })
        .catch(function (e) { toast(e.message, true); });
    });
    var rlReset = byId("rl-reset");
    if (rlReset) rlReset.addEventListener("click", function () {
      var id = rlUserID();
      if (!id) return;
      api("POST", "/api/ratelimit/reset", { user_id: id })
        .then(function () { rlResult.textContent = "Rate limit for " + id + " reset."; })
        .catch(function (e) { toast(e.message, true); });
    });
  }

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

    /* while the runtime/model is being provisioned in the background, poll the
       overview endpoint and update the chip/error in place — reloading would
       discard unsaved detection settings or gallery edits */
    var modelState = byId("if-model-state");
    if (modelState && modelState.dataset.provisioning === "true") {
      setTimeout(pollProvisioning, 10000);
    }

    function applyModelStatus(status) {
      if (!modelState) return false;
      modelState.dataset.provisioning = String(!!status.provisioning);
      if (status.provisioning) {
        modelState.textContent = "🟡 preparing model…";
      } else if (status.warm) {
        modelState.textContent = "🟢 model warm";
      } else {
        modelState.textContent = "⚪ model cold";
      }
      var err = byId("if-model-error");
      if (status.provision_error) {
        if (!err) {
          err = document.createElement("p");
          err.id = "if-model-error";
          err.className = "muted";
          err.style.margin = "8px 0 0";
          err.style.borderLeft = "2px solid rgba(237,66,69,.6)";
          err.style.paddingLeft = "8px";
          var anchor = modelState.closest(".if-model-row") || modelState;
          anchor.insertAdjacentElement("afterend", err);
        }
        err.textContent = "Model setup failed: " + status.provision_error;
      } else if (err) {
        err.remove();
      }
      return !!status.provisioning;
    }

    function pollProvisioning() {
      api("GET", "/api/guilds/" + guildID + "/imagefilter")
        .then(function (r) {
          if (applyModelStatus(r.status || {})) setTimeout(pollProvisioning, 10000);
        })
        .catch(function () { setTimeout(pollProvisioning, 15000); });
    }

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

  /* Live updates: the dashboard re-renders the list fragments server-side and
     pushes them over SSE, so cards appear/disappear without a reload. */
  if (tkGuild && byId("tk-open-list")) {
    var tkEvents = new EventSource(tkBase + "/stream");
    tkEvents.addEventListener("tickets", function (ev) {
      var payload = JSON.parse(ev.data);
      var open = byId("tk-open-list");
      if (open && typeof payload.open === "string") open.innerHTML = payload.open;
      var closed = byId("tk-closed-list");
      if (closed && typeof payload.closed === "string") closed.innerHTML = payload.closed;
    });
  }

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
      typeClose = byId("tk-type-close"),
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

  /* ---------- transcript page: live messages (SSE) ---------- */
  var tkConv = byId("tk-conversation");
  if (tkConv && tkConv.dataset.guild && tkConv.dataset.ticket) {
    var tkConvEvents = new EventSource(
      "/api/tickets/" + encodeURIComponent(tkConv.dataset.guild) +
      "/" + encodeURIComponent(tkConv.dataset.ticket) + "/stream");
    tkConvEvents.addEventListener("messages", function (ev) {
      var p = JSON.parse(ev.data);
      var nearBottom = (window.innerHeight + window.scrollY) >= document.documentElement.scrollHeight - 120;
      tkConv.innerHTML = p.html;
      tkInitPlayers(tkConv);
      var title = byId("tk-conv-title");
      if (title) title.textContent = "Conversation (" + p.count + " entries)";
      var status = byId("tk-status");
      if (status) status.textContent = p.status;
      if (nearBottom) window.scrollTo(0, document.body.scrollHeight);
    });
  }

  /* ---------- custom media players ---------- */
  var tkInitPlayers = function (root) {
    (root || document).querySelectorAll("[data-player]").forEach(function (p) {
      if (p.dataset.playerReady) return;
      p.dataset.playerReady = "1";
      var media = p.querySelector("video, audio");
      var play = p.querySelector(".player-play");
      var seek = p.querySelector(".player-seek");
      var time = p.querySelector(".player-time");
      var mute = p.querySelector(".player-mute");
      var vol = p.querySelector(".player-vol");
      var full = p.querySelector(".player-full");
      if (!media || !play || !seek || !time || !mute) return;
      var fmt = function (s) {
        if (!isFinite(s) || s < 0) s = 0;
        var m = Math.floor(s / 60), sec = Math.floor(s % 60);
        return m + ":" + (sec < 10 ? "0" : "") + sec;
      };
      var sync = function () {
        var dur = media.duration || 0, cur = media.currentTime || 0;
        seek.value = dur ? Math.round((cur / dur) * 1000) : 0;
        time.textContent = fmt(cur) + " / " + fmt(dur);
        play.textContent = media.paused ? "▶" : "❚❚";
        if (vol) vol.value = Math.round((media.muted ? 0 : media.volume) * 100);
      };
      var toggle = function () {
        if (media.paused) {
          var pr = media.play();
          if (pr && pr.catch) pr.catch(function () {});
        } else {
          media.pause();
        }
      };
      play.addEventListener("click", toggle);
      media.addEventListener("click", toggle);
      media.addEventListener("play", sync);
      media.addEventListener("pause", sync);
      media.addEventListener("timeupdate", sync);
      media.addEventListener("loadedmetadata", sync);
      seek.addEventListener("input", function () {
        if (media.duration) media.currentTime = (seek.value / 1000) * media.duration;
      });
      mute.addEventListener("click", function () {
        media.muted = !media.muted;
        mute.textContent = media.muted ? "🔇" : "🔊";
        sync();
      });
      if (vol) {
        vol.addEventListener("input", function () {
          media.volume = vol.value / 100;
          media.muted = vol.value === "0";
          mute.textContent = media.muted ? "🔇" : "🔊";
        });
      }
      if (full) {
        full.addEventListener("click", function () {
          if (document.fullscreenElement) {
            document.exitFullscreen();
          } else if (p.requestFullscreen) {
            var fr = p.requestFullscreen();
            if (fr && fr.catch) fr.catch(function () {});
          }
        });
      }
      sync();
    });
  };
  tkInitPlayers();

  /* ---------- media lightbox (zoom, pan, download) ---------- */
  var lightbox = byId("lightbox");
  if (lightbox) {
    var lightboxImg = byId("lightbox-img");
    var lightboxVideo = byId("lightbox-video");
    var lightboxPlayer = byId("lightbox-player");
    var lightboxDl = byId("lightbox-download");
    var lbScale = 1, lbX = 0, lbY = 0, lbDrag = null;
    var lbApply = function () {
      lightboxImg.style.transform = "translate(" + lbX + "px," + lbY + "px) scale(" + lbScale + ")";
    };
    var lbReset = function () {
      lbScale = 1; lbX = 0; lbY = 0;
      lightboxImg.classList.remove("dragging");
      lbApply();
    };
    var closeLightbox = function () {
      lightbox.classList.remove("open");
      lightboxImg.hidden = false;
      lightboxImg.removeAttribute("src");
      lbReset();
      if (lightboxVideo) {
        lightboxVideo.pause();
        lightboxVideo.removeAttribute("src");
      }
      if (lightboxPlayer) lightboxPlayer.hidden = true;
      if (lightboxDl) lightboxDl.removeAttribute("href");
    };
    document.addEventListener("click", function (ev) {
      var img = ev.target.closest("img.zoomable");
      if (img) {
        if (lightboxVideo) {
          lightboxVideo.pause();
          lightboxVideo.removeAttribute("src");
        }
        if (lightboxPlayer) lightboxPlayer.hidden = true;
        lightboxImg.hidden = false;
        lightboxImg.src = img.src;
        lbReset();
        if (lightboxDl) lightboxDl.href = img.src;
        lightbox.classList.add("open");
        lightbox.focus();
        return;
      }
      var vid = ev.target.closest("video.video-thumb");
      if (vid) {
        lightboxImg.hidden = true;
        lightboxImg.removeAttribute("src");
        if (lightboxPlayer) lightboxPlayer.hidden = false;
        lightboxVideo.src = vid.currentSrc || vid.src;
        if (lightboxDl) lightboxDl.href = lightboxVideo.src;
        lightbox.classList.add("open");
        var pr = lightboxVideo.play();
        if (pr && pr.catch) pr.catch(function () {});
        lightbox.focus();
      }
    });
    lightbox.addEventListener("click", function (ev) {
      if (ev.target === lightbox) closeLightbox();
    });
    var lightboxClose = byId("lightbox-close");
    if (lightboxClose) lightboxClose.addEventListener("click", closeLightbox);
    lightboxImg.addEventListener("wheel", function (ev) {
      ev.preventDefault();
      lbScale = Math.min(6, Math.max(1, lbScale * (ev.deltaY < 0 ? 1.15 : 1 / 1.15)));
      if (lbScale === 1) { lbX = 0; lbY = 0; }
      lbApply();
    }, { passive: false });
    lightboxImg.addEventListener("mousedown", function (ev) {
      if (lbScale <= 1) return;
      ev.preventDefault();
      lbDrag = { x: ev.clientX, y: ev.clientY, tx: lbX, ty: lbY };
      lightboxImg.classList.add("dragging");
    });
    document.addEventListener("mousemove", function (ev) {
      if (!lbDrag) return;
      lbX = lbDrag.tx + (ev.clientX - lbDrag.x);
      lbY = lbDrag.ty + (ev.clientY - lbDrag.y);
      lbApply();
    });
    document.addEventListener("mouseup", function () {
      lbDrag = null;
      lightboxImg.classList.remove("dragging");
    });
    lightboxImg.addEventListener("dblclick", function (ev) {
      ev.preventDefault();
      if (lbScale > 1) { lbReset(); } else { lbScale = 2.5; lbApply(); }
    });
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

  /* ---------- welcome message editor (/g/<id>/welcome) ---------- */
  var welcomeData = byId("welcome-data");
  if (welcomeData && document.querySelector("[data-welcome-panel]")) {
    var welcomeGuild = (byId("welcome-guild") || {}).value || "";
    var welcomeFieldTemplate = byId("welcome-field-template");
    var welcomeCfg = {};
    try {
      welcomeCfg = (JSON.parse(welcomeData.textContent || "{}").config) || {};
    } catch (err) {
      welcomeCfg = {};
    }
    var welcomeEvent = "join";
    var welcomeDirty = false;

    function welcomePanel(event) {
      return document.querySelector('[data-welcome-panel="' + event + '"]');
    }
    function welcomeEl(event, name) {
      var panel = welcomePanel(event);
      return panel ? panel.querySelector("[data-welcome-" + name + "]") : null;
    }
    function welcomeStatus(msg, isErr) {
      var el = document.querySelector("[data-welcome-status]");
      if (!el) return;
      el.textContent = msg || "";
      el.classList.toggle("welcome-status-err", !!isErr);
    }
    function welcomeDirtyMark() {
      welcomeDirty = true;
      welcomeStatus("");
    }
    function welcomeCounter(input, counter) {
      if (!input || !counter) return;
      var max = input.getAttribute("maxlength") || "";
      counter.textContent = input.value.length + (max ? "/" + max : "");
    }
    function welcomeBindCounter(event, inputName, counterName) {
      var input = welcomeEl(event, inputName);
      var counter = welcomeEl(event, counterName);
      if (!input || !counter) return;
      input.addEventListener("input", function () { welcomeCounter(input, counter); });
      welcomeCounter(input, counter);
    }
    function welcomeFieldCount(event) {
      var panel = welcomePanel(event);
      if (!panel) return;
      var box = panel.querySelector("[data-welcome-fields]");
      var counter = panel.querySelector("[data-welcome-field-count]");
      if (box && counter) counter.textContent = box.children.length + "/25";
    }
    function welcomeFieldRow(field) {
      var row = welcomeFieldTemplate.content.firstElementChild.cloneNode(true);
      row.querySelector("[data-field-name]").value = field.name || "";
      row.querySelector("[data-field-value]").value = field.value || "";
      row.querySelector("[data-field-inline]").checked = !!field.inline;
      return row;
    }
    function welcomeAddField(event, field) {
      var panel = welcomePanel(event);
      if (!panel) return;
      var box = panel.querySelector("[data-welcome-fields]");
      if (!box) return;
      if (box.children.length >= 25) {
        toast("An embed can hold at most 25 fields", true);
        return;
      }
      box.appendChild(welcomeFieldRow(field || {}));
      welcomeFieldCount(event);
    }
    function welcomeSetColor(event, value) {
      var panel = welcomePanel(event);
      if (!panel) return;
      var swatch = panel.querySelector("[data-welcome-color]");
      var hex = panel.querySelector("[data-welcome-color-hex]");
      value = (value || "").trim();
      if (/^#[0-9a-fA-F]{3}$/.test(value)) {
        value = "#" + value[1] + value[1] + value[2] + value[2] + value[3] + value[3];
      }
      value = value.toLowerCase();
      if (/^#[0-9a-f]{6}$/.test(value)) {
        if (swatch) swatch.value = value;
        if (hex) hex.value = value;
      } else if (value === "" && hex) {
        hex.value = "";
      }
    }
    function isoLocal(iso) {
      if (!iso) return "";
      var d = new Date(iso);
      if (isNaN(d.getTime())) return "";
      var pad = function (n) { return (n < 10 ? "0" : "") + n; };
      return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + "T" + pad(d.getHours()) + ":" + pad(d.getMinutes());
    }
    function localISO(value) {
      if (!value) return "";
      var d = new Date(value);
      return isNaN(d.getTime()) ? "" : d.toISOString();
    }
    function welcomeClearEmbed(event) {
      ["author-icon", "author-name", "author-url", "title", "title-url", "description", "image", "thumbnail", "footer", "footer-icon", "timestamp"].forEach(function (name) {
        var el = welcomeEl(event, name);
        if (el) el.value = "";
      });
      var embedEnabled = welcomeEl(event, "embed-enabled");
      if (embedEnabled) embedEnabled.checked = false;
      var box = welcomePanel(event).querySelector("[data-welcome-fields]");
      if (box) box.innerHTML = "";
      welcomeSetColor(event, "");
      var swatch = welcomeEl(event, "color");
      if (swatch) swatch.value = "#5865f2";
      welcomeFieldCount(event);
      welcomeCounter(welcomeEl(event, "title"), welcomeEl(event, "title-count"));
      welcomeCounter(welcomeEl(event, "description"), welcomeEl(event, "desc-count"));
      welcomeCounter(welcomeEl(event, "footer"), welcomeEl(event, "footer-count"));
    }
    function welcomeHydrate(event) {
      var cfg = welcomeCfg[event] || {};
      var embed = cfg.embed || {};
      var author = embed.author || {};
      welcomeEl(event, "enabled").checked = !!cfg.enabled;
      welcomeEl(event, "channel").value = cfg.channel_id || "";
      welcomeEl(event, "autodelete").value = cfg.auto_delete || 0;
      welcomeEl(event, "message").value = cfg.message || "";
      welcomeEl(event, "embed-enabled").checked = !!embed.enabled;
      welcomeEl(event, "author-icon").value = author.icon_url || "";
      welcomeEl(event, "author-name").value = author.name || "";
      welcomeEl(event, "author-url").value = author.url || "";
      welcomeEl(event, "title").value = embed.title || "";
      welcomeEl(event, "title-url").value = embed.title_url || "";
      welcomeEl(event, "description").value = embed.description || "";
      welcomeEl(event, "image").value = embed.image_url || "";
      welcomeEl(event, "thumbnail").value = embed.thumbnail_url || "";
      welcomeEl(event, "footer").value = embed.footer || "";
      welcomeEl(event, "footer-icon").value = embed.footer_icon || "";
      welcomeEl(event, "timestamp").value = isoLocal(embed.timestamp || "");
      var hex = welcomeEl(event, "color-hex");
      var swatch = welcomeEl(event, "color");
      var color = (embed.color || "").toLowerCase();
      if (hex) hex.value = color;
      if (swatch) swatch.value = /^#[0-9a-f]{6}$/.test(color) ? color : "#5865f2";
      var box = welcomePanel(event).querySelector("[data-welcome-fields]");
      box.innerHTML = "";
      (embed.fields || []).forEach(function (f) { box.appendChild(welcomeFieldRow(f)); });
      welcomeCounter(welcomeEl(event, "message"), welcomeEl(event, "count"));
      welcomeCounter(welcomeEl(event, "title"), welcomeEl(event, "title-count"));
      welcomeCounter(welcomeEl(event, "description"), welcomeEl(event, "desc-count"));
      welcomeCounter(welcomeEl(event, "footer"), welcomeEl(event, "footer-count"));
      welcomeFieldCount(event);
    }

    ["join", "leave"].forEach(function (event) {
      var panel = welcomePanel(event);
      if (!panel) return;
      panel.querySelectorAll("input, textarea, select").forEach(function (el) {
        var evt = (el.tagName === "SELECT" || el.type === "checkbox" || el.type === "color" || el.type === "datetime-local") ? "change" : "input";
        el.addEventListener(evt, welcomeDirtyMark);
      });
      welcomeBindCounter(event, "message", "count");
      welcomeBindCounter(event, "title", "title-count");
      welcomeBindCounter(event, "description", "desc-count");
      welcomeBindCounter(event, "footer", "footer-count");
      var fieldsBox = panel.querySelector("[data-welcome-fields]");
      if (fieldsBox) {
        fieldsBox.addEventListener("input", welcomeDirtyMark);
        fieldsBox.addEventListener("change", welcomeDirtyMark);
      }

      panel.querySelector("[data-welcome-add-field]").addEventListener("click", function () {
        welcomeAddField(event, {});
        welcomeDirtyMark();
      });
      panel.querySelector("[data-welcome-fields]").addEventListener("click", function (e) {
        var btn = e.target.closest("button");
        if (!btn) return;
        var row = btn.closest("[data-field-row]");
        if (!row) return;
        if (btn.hasAttribute("data-field-up")) {
          if (row.previousElementSibling) row.parentNode.insertBefore(row, row.previousElementSibling);
        } else if (btn.hasAttribute("data-field-down")) {
          if (row.nextElementSibling) row.parentNode.insertBefore(row.nextElementSibling, row);
        } else if (btn.hasAttribute("data-field-remove")) {
          row.remove();
        } else {
          return;
        }
        welcomeFieldCount(event);
        welcomeDirtyMark();
      });

      panel.querySelector("[data-welcome-color]").addEventListener("input", function () {
        var hex = welcomeEl(event, "color-hex");
        if (hex) hex.value = this.value;
        welcomeDirtyMark();
      });
      panel.querySelector("[data-welcome-color-hex]").addEventListener("change", function () {
        var value = this.value.trim();
        if (value && value.charAt(0) !== "#") value = "#" + value;
        welcomeSetColor(event, value);
        welcomeDirtyMark();
      });
      var colorToggle = panel.querySelector("[data-welcome-color-toggle]");
      var colorRow = panel.querySelector("[data-welcome-color-row]");
      colorToggle.addEventListener("click", function () { colorRow.hidden = !colorRow.hidden; });
      panel.querySelector("[data-welcome-color-clear]").addEventListener("click", function () {
        welcomeSetColor(event, "");
        var swatch = welcomeEl(event, "color");
        if (swatch) swatch.value = "#5865f2";
        welcomeDirtyMark();
      });

      var advToggle = panel.querySelector("[data-welcome-advanced]");
      var advBody = panel.querySelector("[data-welcome-advanced-body]");
      advToggle.addEventListener("click", function () {
        advBody.hidden = !advBody.hidden;
        advToggle.textContent = advBody.hidden ? "Advanced" : "Hide";
        advToggle.setAttribute("aria-expanded", advBody.hidden ? "false" : "true");
      });

      panel.querySelector("[data-welcome-embed-clear]").addEventListener("click", function () {
        welcomeClearEmbed(event);
        welcomeDirtyMark();
      });
      panel.querySelector("[data-welcome-timestamp-now]").addEventListener("click", function () {
        welcomeEl(event, "timestamp").value = isoLocal(new Date().toISOString());
        welcomeDirtyMark();
      });
      panel.querySelector("[data-welcome-timestamp-clear]").addEventListener("click", function () {
        welcomeEl(event, "timestamp").value = "";
        welcomeDirtyMark();
      });

      panel.querySelectorAll("[data-welcome-var]").forEach(function (btn) {
        btn.addEventListener("click", function () {
          var ta = welcomeEl(event, "message");
          var token = btn.getAttribute("data-welcome-var") || "";
          var start = typeof ta.selectionStart === "number" ? ta.selectionStart : ta.value.length;
          var end = typeof ta.selectionEnd === "number" ? ta.selectionEnd : ta.value.length;
          ta.value = ta.value.slice(0, start) + token + ta.value.slice(end);
          ta.focus();
          ta.selectionStart = ta.selectionEnd = start + token.length;
          welcomeCounter(ta, welcomeEl(event, "count"));
          welcomeDirtyMark();
        });
      });

      welcomeHydrate(event);
    });

    document.querySelectorAll("[data-welcome-tab]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        welcomeEvent = btn.getAttribute("data-welcome-tab");
        document.querySelectorAll("[data-welcome-tab]").forEach(function (b) {
          var on = b === btn;
          b.classList.toggle("cmd-tab-active", on);
          b.setAttribute("aria-selected", on ? "true" : "false");
        });
        document.querySelectorAll("[data-welcome-panel]").forEach(function (p) {
          p.hidden = p.getAttribute("data-welcome-panel") !== welcomeEvent;
        });
        welcomeStatus("");
      });
    });

    function welcomeCollectEvent(event) {
      var fields = [];
      welcomePanel(event).querySelectorAll("[data-field-row]").forEach(function (row) {
        fields.push({
          name: row.querySelector("[data-field-name]").value,
          value: row.querySelector("[data-field-value]").value,
          inline: row.querySelector("[data-field-inline]").checked
        });
      });
      return {
        enabled: welcomeEl(event, "enabled").checked,
        channel_id: welcomeEl(event, "channel").value,
        message: welcomeEl(event, "message").value,
        auto_delete: parseInt(welcomeEl(event, "autodelete").value, 10) || 0,
        embed: {
          enabled: welcomeEl(event, "embed-enabled").checked,
          color: welcomeEl(event, "color-hex").value.trim(),
          author: {
            name: welcomeEl(event, "author-name").value,
            url: welcomeEl(event, "author-url").value,
            icon_url: welcomeEl(event, "author-icon").value
          },
          title: welcomeEl(event, "title").value,
          title_url: welcomeEl(event, "title-url").value,
          description: welcomeEl(event, "description").value,
          fields: fields,
          image_url: welcomeEl(event, "image").value,
          thumbnail_url: welcomeEl(event, "thumbnail").value,
          footer: welcomeEl(event, "footer").value,
          footer_icon: welcomeEl(event, "footer-icon").value,
          timestamp: localISO(welcomeEl(event, "timestamp").value)
        }
      };
    }
    function welcomeSave() {
      return api("POST", "/api/guilds/" + welcomeGuild + "/welcome", {
        join: welcomeCollectEvent("join"),
        leave: welcomeCollectEvent("leave")
      });
    }

    var welcomeSaveBtn = document.querySelector("[data-welcome-save]");
    if (welcomeSaveBtn) {
      welcomeSaveBtn.addEventListener("click", function () {
        welcomeSaveBtn.disabled = true;
        welcomeStatus("Saving…");
        welcomeSave().then(function () {
          welcomeDirty = false;
          welcomeStatus("Saved.");
          toast("Welcome settings saved");
        }).catch(function (e) {
          welcomeStatus(e.message, true);
          toast("Save failed: " + e.message, true);
        }).then(function () { welcomeSaveBtn.disabled = false; });
      });
    }
    var welcomeTestBtn = document.querySelector("[data-welcome-test]");
    if (welcomeTestBtn) {
      welcomeTestBtn.addEventListener("click", function () {
        welcomeTestBtn.disabled = true;
        welcomeStatus("Saving and sending a test…");
        welcomeSave().then(function () {
          welcomeDirty = false;
          return api("POST", "/api/guilds/" + welcomeGuild + "/welcome/test", { event: welcomeEvent });
        }).then(function () {
          welcomeStatus("Test sent to the configured channel.");
          toast("Test message sent");
        }).catch(function (e) {
          welcomeStatus(e.message, true);
          toast("Test failed: " + e.message, true);
        }).then(function () { welcomeTestBtn.disabled = false; });
      });
    }
    var welcomeClearBtn = document.querySelector("[data-welcome-clear]");
    if (welcomeClearBtn) {
      welcomeClearBtn.addEventListener("click", function () {
        if (!confirm("Clear the " + welcomeEvent + " message and embed? The change is only applied when you save.")) return;
        welcomeEl(welcomeEvent, "message").value = "";
        welcomeClearEmbed(welcomeEvent);
        welcomeCounter(welcomeEl(welcomeEvent, "message"), welcomeEl(welcomeEvent, "count"));
        welcomeDirtyMark();
      });
    }
    window.addEventListener("beforeunload", function (e) {
      if (!welcomeDirty) return;
      e.preventDefault();
      e.returnValue = "";
    });
  }

  /* ---------- moderation editor (/g/<id>/moderation) ---------- */
  var modSave = byId("mod-save");
  if (modSave) {
    var modGuild = (byId("mod-guild") || {}).value || "";
    var modRules = byId("mod-rules");
    var modTemplate = byId("mod-rule-template");
    var modDirty = false;

    function modStatus(msg, isErr) {
      var el = byId("mod-status");
      if (!el) return;
      el.textContent = msg || "";
      el.classList.toggle("mod-status-err", !!isErr);
    }
    function modDirtyMark() {
      modDirty = true;
      modStatus("");
    }
    function modSyncRule(row) {
      var action = row.querySelector("[data-rule-action]");
      var duration = row.querySelector("[data-rule-duration]");
      if (!action || !duration) return;
      var isTimeout = action.value === "timeout";
      duration.disabled = !isTimeout;
      if (!isTimeout) duration.value = "0";
    }
    function modBindRule(row) {
      var action = row.querySelector("[data-rule-action]");
      if (action) {
        action.addEventListener("change", function () {
          modSyncRule(row);
          modDirtyMark();
        });
      }
      row.querySelectorAll("input, select").forEach(function (el) {
        el.addEventListener("input", modDirtyMark);
        el.addEventListener("change", modDirtyMark);
      });
      modSyncRule(row);
    }
    modRules.querySelectorAll("[data-rule]").forEach(modBindRule);

    byId("mod-add-rule").addEventListener("click", function () {
      var row = modTemplate.content.firstElementChild.cloneNode(true);
      modRules.appendChild(row);
      modBindRule(row);
      var empty = byId("mod-rules-empty");
      if (empty) empty.hidden = true;
      modDirtyMark();
    });
    modRules.addEventListener("click", function (e) {
      var btn = e.target.closest("[data-rule-remove]");
      if (!btn) return;
      var row = btn.closest("[data-rule]");
      if (row) row.remove();
      if (!modRules.querySelector("[data-rule]")) {
        var empty = byId("mod-rules-empty");
        if (empty) empty.hidden = false;
      }
      modDirtyMark();
    });

    ["mod-enabled", "mod-audit-channel", "mod-warn-channel", "mod-warn-notify", "mod-warn-expiry", "mod-ban-delete"].forEach(function (id) {
      var el = byId(id);
      if (!el) return;
      var evt = (el.type === "checkbox" || el.tagName === "SELECT") ? "change" : "input";
      el.addEventListener(evt, modDirtyMark);
    });

    function modCollect() {
      var rules = [];
      modRules.querySelectorAll("[data-rule]").forEach(function (row) {
        var threshold = parseInt(row.querySelector("[data-rule-threshold]").value, 10) || 0;
        if (threshold < 1) return;
        var action = row.querySelector("[data-rule-action]").value;
        var duration = parseInt(row.querySelector("[data-rule-duration]").value, 10) || 0;
        if (action !== "timeout") duration = 0;
        rules.push({ threshold: threshold, action: action, duration_minutes: duration });
      });
      return {
        enabled: byId("mod-enabled").checked,
        audit_channel: byId("mod-audit-channel").value,
        warn_channel: byId("mod-warn-channel").value,
        warn_notify: byId("mod-warn-notify").checked,
        warn_expiry_days: parseInt(byId("mod-warn-expiry").value, 10) || 0,
        ban_delete_seconds: parseInt(byId("mod-ban-delete").value, 10) || 0,
        rules: rules
      };
    }

    modSave.addEventListener("click", function () {
      modSave.disabled = true;
      modStatus("Saving…");
      api("POST", "/api/guilds/" + modGuild + "/moderation", modCollect())
        .then(function () {
          modDirty = false;
          modStatus("Saved.");
          toast("Moderation settings saved");
        })
        .catch(function (e) {
          modStatus(e.message, true);
          toast("Save failed: " + e.message, true);
        })
        .then(function () { modSave.disabled = false; });
    });

    document.querySelectorAll("[data-mod-unwarn]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-mod-unwarn");
        if (!confirm("Remove warning #" + id + "? This is recorded in the audit log.")) return;
        btn.disabled = true;
        api("DELETE", "/api/guilds/" + modGuild + "/moderation/cases/" + id)
          .then(function () { location.reload(); })
          .catch(function (e) {
            btn.disabled = false;
            toast("Remove failed: " + e.message, true);
          });
      });
    });

    window.addEventListener("beforeunload", function (e) {
      if (!modDirty) return;
      e.preventDefault();
      e.returnValue = "";
    });
  }

  /* ---------- leveling editor (/g/<id>/leveling) ---------- */
  var lvlSave = byId("lvl-save");
  if (lvlSave) {
    var lvlGuild = (byId("lvl-guild") || {}).value || "";
    var lvlBase = "/api/guilds/" + lvlGuild + "/leveling";
    var lvlRewards = byId("lvl-rewards");
    var lvlTemplate = byId("lvl-reward-template");
    var lvlDirty = false;
    var lvlPoll = null;

    function lvlStatus(msg, isErr) {
      var el = byId("lvl-status");
      if (!el) return;
      el.textContent = msg || "";
      el.classList.toggle("mod-status-err", !!isErr);
    }
    function lvlDirtyMark() {
      lvlDirty = true;
      lvlStatus("");
    }
    function lvlSelected(id) {
      var el = byId(id);
      if (!el) return [];
      return Array.prototype.map.call(el.selectedOptions || [], function (o) { return o.value; });
    }
    function lvlBindReward(row) {
      row.querySelectorAll("input, select").forEach(function (el) {
        el.addEventListener("input", lvlDirtyMark);
        el.addEventListener("change", lvlDirtyMark);
      });
    }
    if (lvlRewards) {
      lvlRewards.querySelectorAll("[data-reward]").forEach(lvlBindReward);
    }

    var lvlAddReward = byId("lvl-add-reward");
    if (lvlAddReward && lvlRewards && lvlTemplate) {
      lvlAddReward.addEventListener("click", function () {
        var row = lvlTemplate.content.firstElementChild.cloneNode(true);
        lvlRewards.appendChild(row);
        lvlBindReward(row);
        var empty = byId("lvl-rewards-empty");
        if (empty) empty.hidden = true;
        lvlDirtyMark();
      });
      lvlRewards.addEventListener("click", function (e) {
        var btn = e.target.closest("[data-reward-remove]");
        if (!btn) return;
        var row = btn.closest("[data-reward]");
        if (row) row.remove();
        if (!lvlRewards.querySelector("[data-reward]")) {
          var empty = byId("lvl-rewards-empty");
          if (empty) empty.hidden = false;
        }
        lvlDirtyMark();
      });
    }

    ["lvl-enabled", "lvl-msg-enabled", "lvl-msg-min", "lvl-msg-max", "lvl-cooldown",
      "lvl-voice-enabled", "lvl-voice-xp", "lvl-voice-others", "lvl-max-level",
      "lvl-catchup-enabled", "lvl-catchup-max", "lvl-catchup-channels",
      "lvl-announce-enabled", "lvl-announce-channel", "lvl-announce-message",
      "lvl-ignore-channels", "lvl-ignore-roles"].forEach(function (id) {
        var el = byId(id);
        if (!el) return;
        var evt = (el.type === "checkbox" || el.tagName === "SELECT") ? "change" : "input";
        el.addEventListener(evt, lvlDirtyMark);
      });

    function lvlCollect() {
      var rewards = [];
      if (lvlRewards) {
        lvlRewards.querySelectorAll("[data-reward]").forEach(function (row) {
          var level = parseInt((row.querySelector("[data-reward-level]") || {}).value, 10) || 0;
          var role = (row.querySelector("[data-reward-role]") || {}).value || "";
          if (level < 1 || !role) return;
          rewards.push({ level: level, role_id: role });
        });
      }
      return {
        enabled: byId("lvl-enabled").checked,
        message_enabled: byId("lvl-msg-enabled").checked,
        message_xp_min: parseInt(byId("lvl-msg-min").value, 10) || 0,
        message_xp_max: parseInt(byId("lvl-msg-max").value, 10) || 0,
        message_cooldown_seconds: parseInt(byId("lvl-cooldown").value, 10) || 0,
        voice_enabled: byId("lvl-voice-enabled").checked,
        voice_xp_per_minute: parseInt(byId("lvl-voice-xp").value, 10) || 0,
        voice_require_others: byId("lvl-voice-others").checked,
        max_level: parseInt(byId("lvl-max-level").value, 10) || 1,
        announce_enabled: byId("lvl-announce-enabled").checked,
        announce_channel: byId("lvl-announce-channel").value,
        announce_message: byId("lvl-announce-message").value,
        catchup_enabled: byId("lvl-catchup-enabled").checked,
        catchup_max_messages: parseInt(byId("lvl-catchup-max").value, 10) || 1,
        catchup_max_channels: parseInt(byId("lvl-catchup-channels").value, 10) || 1,
        ignored_channels: lvlSelected("lvl-ignore-channels"),
        ignored_roles: lvlSelected("lvl-ignore-roles"),
        rewards: rewards
      };
    }

    lvlSave.addEventListener("click", function () {
      lvlSave.disabled = true;
      lvlStatus("Saving…");
      api("POST", lvlBase, lvlCollect())
        .then(function () {
          lvlDirty = false;
          lvlStatus("Saved.");
          toast("Leveling settings saved");
        })
        .catch(function (e) {
          lvlStatus(e.message, true);
          toast("Save failed: " + e.message, true);
        })
        .then(function () { lvlSave.disabled = false; });
    });

    var lvlToggle = byId("lvl-toggle");
    if (lvlToggle) {
      lvlToggle.addEventListener("click", function () {
        var enabled = lvlToggle.dataset.enabled !== "true";
        var label = enabled ? "Enable leveling in this server?" : "Disable leveling? XP stops immediately and nothing is deleted.";
        if (!confirm(label)) return;
        lvlToggle.disabled = true;
        api("POST", lvlBase + "/enabled", { enabled: enabled })
          .then(function () { location.reload(); })
          .catch(function (e) {
            lvlToggle.disabled = false;
            toast("Failed: " + e.message, true);
          });
      });
    }

    function lvlStatsText(s) {
      var parts = [];
      if (s.catchup_running) {
        parts.push("Catch-up running: " + s.catchup_processed + " messages scanned, " + s.catchup_awarded + " awarded");
      } else if (s.catchup_truncated) {
        parts.push("Catch-up incomplete (hit a cap); it retries on the next restart.");
      }
      if (s.sync_running) {
        parts.push("Reward sync: " + s.sync_processed + " / " + s.sync_total);
      }
      if (s.catchup_error) parts.push("Catch-up error: " + s.catchup_error);
      if (s.sync_error) parts.push("Sync error: " + s.sync_error);
      return parts.join(" · ");
    }

    function lvlApplyStats(s) {
      var catchupEl = byId("lvl-catchup-status");
      if (catchupEl) catchupEl.textContent = lvlStatsText(s);
      var syncEl = byId("lvl-sync-status");
      if (syncEl) {
        syncEl.textContent = s.sync_running
          ? "Syncing " + s.sync_processed + " / " + s.sync_total + (s.sync_failed ? " (" + s.sync_failed + " failed)" : "")
          : (s.sync_total ? "Last sync: " + s.sync_processed + " checked, " + s.sync_failed + " failed" : "");
      }
      return !s.sync_running && !s.catchup_running;
    }

    function lvlPollStats() {
      if (lvlPoll) return;
      var tick = function () {
        api("GET", lvlBase)
          .then(function (res) {
            if (lvlApplyStats(res.stats || {})) {
              clearInterval(lvlPoll);
              lvlPoll = null;
            }
          })
          .catch(function () {
            clearInterval(lvlPoll);
            lvlPoll = null;
          });
      };
      tick();
      lvlPoll = setInterval(tick, 2000);
    }

    var lvlSync = byId("lvl-sync");
    if (lvlSync) {
      lvlSync.addEventListener("click", function () {
        lvlSync.disabled = true;
        api("POST", lvlBase + "/sync")
          .then(function (res) {
            toast(res.queued + " member(s) queued for reward sync");
            lvlPollStats();
          })
          .catch(function (e) { toast("Sync failed: " + e.message, true); })
          .then(function () { lvlSync.disabled = false; });
      });
    }

    document.querySelectorAll("[data-lvl-edit]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var userID = btn.getAttribute("data-lvl-edit");
        var current = btn.getAttribute("data-xp") || "0";
        var value = prompt("Set total XP for " + userID, current);
        if (value === null) return;
        var xp = parseInt(value, 10);
        if (isNaN(xp) || xp < 0) {
          toast("Enter a non-negative integer", true);
          return;
        }
        btn.disabled = true;
        api("POST", lvlBase + "/members/" + userID, { xp: xp })
          .then(function () { location.reload(); })
          .catch(function (e) {
            btn.disabled = false;
            toast("Update failed: " + e.message, true);
          });
      });
    });

    document.querySelectorAll("[data-lvl-reset]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var userID = btn.getAttribute("data-lvl-reset");
        if (!confirm("Reset " + userID + "? Their XP is deleted and every leveling reward role is removed.")) return;
        btn.disabled = true;
        api("DELETE", lvlBase + "/members/" + userID)
          .then(function () { location.reload(); })
          .catch(function (e) {
            btn.disabled = false;
            toast("Reset failed: " + e.message, true);
          });
      });
    });

    lvlPollStats();

    window.addEventListener("beforeunload", function (e) {
      if (!lvlDirty) return;
      e.preventDefault();
      e.returnValue = "";
    });
  }

  /* ---------- reaction roles (/g/<id>/reactionroles) ---------- */
  var rrSave = byId("rr-save");
  if (rrSave) {
    var rrGuild = (byId("rr-guild") || {}).value || "";
    var rrPanelID = parseInt((byId("rr-panel") || {}).value, 10) || 0;
    var rrMappings = byId("rr-mappings");
    var rrTemplate = byId("rr-mapping-template");
    var rrBase = "/api/guilds/" + rrGuild + "/reactionroles";
    var rrDirty = false;

    function rrStatus(msg, isErr) {
      var el = byId("rr-status");
      if (!el) return;
      el.textContent = msg || "";
      el.classList.toggle("rr-status-err", !!isErr);
    }
    function rrDirtyMark() {
      rrDirty = true;
      rrStatus("");
    }
    function rrBindRow(row) {
      row.querySelectorAll("input, select").forEach(function (el) {
        el.addEventListener("input", rrDirtyMark);
        el.addEventListener("change", rrDirtyMark);
      });
    }
    rrMappings.querySelectorAll("[data-rr-mapping]").forEach(rrBindRow);

    byId("rr-add-mapping").addEventListener("click", function () {
      var row = rrTemplate.content.firstElementChild.cloneNode(true);
      rrMappings.appendChild(row);
      rrBindRow(row);
      rrDirtyMark();
    });
    rrMappings.addEventListener("click", function (e) {
      var btn = e.target.closest("[data-rr-remove]");
      if (!btn) return;
      var row = btn.closest("[data-rr-mapping]");
      if (row) row.remove();
      rrDirtyMark();
    });

    ["rr-name", "rr-enabled", "rr-remove-on-unreact", "rr-channel", "rr-title", "rr-description", "rr-footer", "rr-image", "rr-thumbnail"].forEach(function (id) {
      var el = byId(id);
      if (!el) return;
      var evt = (el.type === "checkbox" || el.tagName === "SELECT") ? "change" : "input";
      el.addEventListener(evt, rrDirtyMark);
    });

    var rrColor = byId("rr-color");
    var rrColorHex = byId("rr-color-hex");
    rrColor.addEventListener("input", function () {
      rrColorHex.value = rrColor.value;
      rrDirtyMark();
    });
    rrColorHex.addEventListener("change", function () {
      var value = this.value.trim();
      if (value && value.charAt(0) !== "#") value = "#" + value;
      if (/^#[0-9a-fA-F]{6}$/.test(value)) {
        value = value.toLowerCase();
        rrColor.value = value;
        rrColorHex.value = value;
      } else if (value === "") {
        rrColorHex.value = "";
      }
      rrDirtyMark();
    });
    byId("rr-color-clear").addEventListener("click", function () {
      rrColorHex.value = "";
      rrColor.value = "#5865f2";
      rrDirtyMark();
    });

    function rrCollect() {
      var mappings = [];
      rrMappings.querySelectorAll("[data-rr-mapping]").forEach(function (row) {
        var emoji = row.querySelector("[data-rr-emoji]").value.trim();
        var role = row.querySelector("[data-rr-role]").value;
        if (!emoji && !role) return;
        mappings.push({ emoji: emoji, role_id: role });
      });
      return {
        id: rrPanelID,
        name: byId("rr-name").value,
        enabled: byId("rr-enabled").checked,
        channel_id: byId("rr-channel").value,
        remove_on_unreact: byId("rr-remove-on-unreact").checked,
        embed: {
          title: byId("rr-title").value,
          description: byId("rr-description").value,
          color: rrColorHex.value.trim(),
          footer: byId("rr-footer").value,
          image_url: byId("rr-image").value,
          thumbnail_url: byId("rr-thumbnail").value
        },
        mappings: mappings
      };
    }
    function rrSavePanel() {
      var body = rrCollect();
      if (rrPanelID === 0) {
        return api("POST", rrBase + "/panels", body).then(function (res) {
          rrPanelID = res.panel.id;
          return res.panel;
        });
      }
      return api("POST", rrBase + "/panels/" + rrPanelID, body).then(function (res) {
        return res.panel;
      });
    }
    function rrRun(post) {
      var saveBtn = rrSave;
      var postBtn = byId("rr-save-post");
      saveBtn.disabled = true;
      postBtn.disabled = true;
      rrStatus(post ? "Saving and posting…" : "Saving…");
      rrSavePanel().then(function () {
        if (post) return api("POST", rrBase + "/panels/" + rrPanelID + "/post", {});
      }).then(function () {
        rrDirty = false;
        toast(post ? "Panel posted" : "Panel saved");
        window.location.href = "/g/" + rrGuild + "/reactionroles?panel=" + rrPanelID;
      }).catch(function (e) {
        rrStatus(e.message, true);
        toast("Save failed: " + e.message, true);
        saveBtn.disabled = false;
        postBtn.disabled = false;
      });
    }
    rrSave.addEventListener("click", function () { rrRun(false); });
    byId("rr-save-post").addEventListener("click", function () {
      if (!byId("rr-enabled").checked) {
        rrStatus("Enable the panel before posting it.", true);
        return;
      }
      rrRun(true);
    });

    var rrDelete = byId("rr-delete");
    if (rrDelete) {
      rrDelete.addEventListener("click", function () {
        if (!confirm("Delete this panel and its posted message? This cannot be undone.")) return;
        rrDelete.disabled = true;
        api("DELETE", rrBase + "/panels/" + rrPanelID + "?delete_message=1")
          .then(function () {
            rrDirty = false;
            window.location.href = "/g/" + rrGuild + "/reactionroles";
          })
          .catch(function (e) {
            rrDelete.disabled = false;
            rrStatus(e.message, true);
            toast("Delete failed: " + e.message, true);
          });
      });
    }

    window.addEventListener("beforeunload", function (e) {
      if (!rrDirty) return;
      e.preventDefault();
      e.returnValue = "";
    });
  }
})();
