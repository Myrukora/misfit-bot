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
      api("POST", "/api/permissions/elevated", { user_id: id.trim() })
        .then(function () { location.reload(); })
        .catch(function (e) { toast(e.message, true); });
    });
    document.querySelectorAll("[data-remove-elevated]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!confirm("Remove elevated access for this user?")) return;
        api("DELETE", "/api/permissions/elevated/" + encodeURIComponent(btn.dataset.removeElevated))
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

  /* ---------- admin (config) page: per-section save ---------- */
  document.querySelectorAll("[data-save-section]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var section = btn.closest(".config-section");
      if (!section) return;
      var body = {};
      section.querySelectorAll("[data-set-key]").forEach(function (input) {
        var key = input.dataset.setKey;
        body[key] = input.type === "checkbox" ? String(input.checked) : input.value;
      });
      api("POST", "/api/settings", body)
        .then(function () { toast("Settings saved"); })
        .catch(function (e) { toast(e.message, true); });
    });
  });

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
})();
