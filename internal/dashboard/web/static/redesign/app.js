/* redesign/app.js — vanilla JS for the redesigned dashboard.
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

  /* ---------- overview: live metrics poll ---------- */
  var latencyEl = byId("m-latency");
  if (latencyEl) {
    var memEl = byId("m-mem"), gorosEl = byId("m-goros"), gcEl = byId("m-gc"), upEl = byId("m-uptime");
    setInterval(function () {
      api("GET", "/api/metrics").then(function (s) {
        if (latencyEl) latencyEl.textContent = s.gateway_latency || latencyEl.textContent;
        if (upEl) upEl.textContent = s.uptime || upEl.textContent;
        if (memEl && s.runtime) memEl.textContent = s.runtime.alloc_mb + " MB";
        if (gorosEl && s.runtime) gorosEl.textContent = s.runtime.goroutines;
        if (gcEl && s.runtime) gcEl.textContent = s.runtime.gc_cycles;
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
          statusRow("interval " + s.interval + " · auto_pull " + s.auto_pull + " · notify " + (s.notify_channel || "—")),
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

    /* add images (upload and/or URL) */
    var addBtn = byId("if-add");
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

  /* ---------- commands page: filter + tabs + run + gear ---------- */
  var cmdSearch = byId("cmd-search");
  if (cmdSearch) {
    cmdSearch.addEventListener("input", function () {
      var q = cmdSearch.value.toLowerCase();
      document.querySelectorAll("details.cmd-card").forEach(function (d) {
        var t = d.textContent.toLowerCase();
        d.style.display = (!q || t.indexOf(q) !== -1) ? "" : "none";
      });
    });
  }

  var rawBox = byId("cmd-raw");
  if (rawBox) {
    rawBox.addEventListener("change", function () {
      var u = new URL(location.href);
      u.searchParams.set("raw", rawBox.checked ? "true" : "false");
      location.href = u;
    });
  }

  /* Guild selector navigation (commands/tickets toolbars, global scope) */
  document.querySelectorAll(".guild-nav").forEach(function (sel) {
    sel.addEventListener("change", function () {
      location.href = sel.dataset.path + "?guild=" + encodeURIComponent(sel.value);
    });
  });

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

  /* Per-command config (gear) modal */
  var gearModal = byId("cmd-gear-modal");
  if (gearModal) {
    var gearName = byId("gear-cmd-name");
    var gearGlobalScope = byId("gear-global-scope");
    var gearGlobalToggle = byId("gear-global-toggle");
    var gearModOnly = byId("gear-modonly-toggle");
    var gearLocalScope = byId("gear-local-scope");
    var gearGuild = byId("gear-guild");
    var gearLocalToggle = byId("gear-local-toggle");
    var gearChannels = byId("gear-channels");
    var gearRoles = byId("gear-roles");
    var gearHint = byId("gear-hint");
    var gearFields = byId("gear-fields");
    var gearCurrent = { name: "", guild: "", globalDisabled: false };

    var splitList = function (s) {
      return (s || "").split(",").filter(function (x) { return x.length > 0; });
    };
    var syncPicker = function (box, ids) {
      var map = {};
      (ids || []).forEach(function (id) { map[id] = true; });
      box.querySelectorAll("input").forEach(function (inp) { inp.checked = !!map[inp.value]; });
    };
    var selectedIDs = function (box) {
      return Array.prototype.slice.call(box.querySelectorAll("input:checked")).map(function (c) { return c.value; });
    };
    var gearScopeVisibility = function () {
      // Level comes from the template-rendered body attribute, never from the
      // scope blocks' current hidden state (staff's global block starts hidden).
      var level = document.body.dataset.level || "regular";
      var isOwner = level === "owner" || level === "elevated";
      var isStaff = isOwner || level === "staff";
      gearGlobalScope.hidden = !isOwner;
      gearLocalScope.hidden = !isStaff;
      gearFields.hidden = !(isStaff && gearGuild.value);
    };
    var gearRefreshHint = function () {
      if (gearGlobalToggle.checked) {
        gearHint.textContent = "This command is disabled everywhere. Uncheck to re-enable it across all servers.";
      } else if (gearLocalToggle.checked) {
        gearHint.textContent = "Disabled in this server only. Other servers keep it enabled.";
      } else {
        gearHint.textContent = "This command is enabled. Toggle a switch above to restrict it.";
      }
    };
    var gearClose = function () {
      gearModal.hidden = true;
      document.body.style.overflow = "";
    };
    var loadGearEntities = function (guildID, checkedCh, checkedRo) {
      gearChannels.replaceChildren();
      gearRoles.replaceChildren();
      if (!guildID) return;
      api("GET", "/api/guild/" + encodeURIComponent(guildID)).then(function (d) {
        var mkItem = function (id, name) {
          var label = document.createElement("label");
          label.className = "multi-item";
          var inp = document.createElement("input");
          inp.type = "checkbox";
          inp.value = id;
          label.appendChild(inp);
          var span = document.createElement("span");
          span.textContent = name;
          label.appendChild(span);
          return label;
        };
        gearChannels.replaceChildren.apply(gearChannels, (d.channels || []).map(function (it) { return mkItem(it.ID, it.Name); }));
        gearRoles.replaceChildren.apply(gearRoles, (d.roles || []).map(function (it) { return mkItem(it.ID, it.Name); }));
        syncPicker(gearChannels, checkedCh);
        syncPicker(gearRoles, checkedRo);
      }).catch(function (e) { toast("Failed to load channels/roles: " + e.message, true); });
    };

    document.querySelectorAll(".gear-btn").forEach(function (btn) {
      btn.addEventListener("click", function () {
        gearCurrent = {
          name: btn.dataset.name,
          guild: btn.dataset.guild || "",
          globalDisabled: btn.dataset.globalDisabled === "true"
        };
        gearName.textContent = gearCurrent.name;
        gearGlobalToggle.checked = gearCurrent.globalDisabled;
        gearModOnly.checked = btn.dataset.modonly === "true";
        gearGuild.value = gearCurrent.guild;
        gearLocalToggle.checked = btn.dataset.guildDisabled === "true";
        syncPicker(gearChannels, splitList(btn.dataset.channels));
        syncPicker(gearRoles, splitList(btn.dataset.roles));
        gearScopeVisibility();
        gearRefreshHint();
        gearModal.hidden = false;
        document.body.style.overflow = "hidden";
      });
    });
    gearModal.querySelector(".gear-backdrop").addEventListener("click", gearClose);
    byId("gear-close").addEventListener("click", gearClose);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !gearModal.hidden) gearClose();
    });
    gearGuild.addEventListener("change", function () {
      gearFields.hidden = gearGuild.value === "";
      loadGearEntities(gearGuild.value, selectedIDs(gearChannels), selectedIDs(gearRoles));
      gearRefreshHint();
    });
    [gearGlobalToggle, gearModOnly, gearLocalToggle].forEach(function (t) {
      t.addEventListener("change", gearRefreshHint);
    });

    byId("gear-save").addEventListener("click", function () {
      var btn = byId("gear-save");
      btn.disabled = true;
      var name = gearCurrent.name;
      var channels = selectedIDs(gearChannels);
      var roles = selectedIDs(gearRoles);
      var modOnly = gearModOnly.checked;
      var work = Promise.resolve();
      if (gearGlobalToggle.checked) {
        // Owner: disable everywhere + mod-only. Clear any local override so
        // the global state is the single source of truth.
        work = work.then(function () {
          return api("POST", "/api/cmdcfg/toggle", { name: name, disabled: true, guildID: "", modOnly: modOnly, channels: [], roles: [] });
        });
        if (gearGuild.value) {
          work = work.then(function () {
            return api("POST", "/api/cmdcfg/toggle", { name: name, disabled: false, guildID: gearGuild.value, channels: [], roles: [], modOnly: false });
          });
        }
      } else if (gearGuild.value && gearLocalToggle.checked) {
        // Staff: disable in this guild, narrowing channels/roles.
        work = work.then(function () {
          return api("POST", "/api/cmdcfg/toggle", { name: name, disabled: true, guildID: gearGuild.value, channels: channels, roles: roles, modOnly: modOnly });
        });
      } else if (gearGuild.value) {
        // No disable toggled: persist channel/role/mod-only narrowing only.
        work = work.then(function () {
          return api("POST", "/api/cmdcfg/toggle", { name: name, disabled: false, guildID: gearGuild.value, channels: channels, roles: roles, modOnly: modOnly });
        });
      }
      work.then(function () {
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
      var work = Promise.resolve();
      if (gearGlobalToggle.checked || gearCurrent.globalDisabled) {
        work = work.then(function () {
          return api("POST", "/api/cmdcfg/toggle", { name: gearCurrent.name, disabled: false, guildID: "", channels: [], roles: [], modOnly: false });
        });
      }
      if (gearGuild.value) {
        work = work.then(function () {
          return api("POST", "/api/cmdcfg/toggle", { name: gearCurrent.name, disabled: false, guildID: gearGuild.value, channels: [], roles: [], modOnly: false });
        });
      }
      work.then(function () {
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
