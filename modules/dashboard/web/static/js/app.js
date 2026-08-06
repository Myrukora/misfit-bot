// Dashboard client: vanilla JS, no dependencies. CSRF via <meta name="csrf-token">.
(function () {
  const csrf = document.querySelector('meta[name="csrf-token"]')?.content || '';
  const apiHeaders = { 'Content-Type': 'application/json' };
  if (csrf) apiHeaders['X-CSRF-Token'] = csrf;

  async function req(method, url, body) {
    const opts = { method, headers: { ...apiHeaders } };
    if (body !== undefined) opts.body = JSON.stringify(body);
    const r = await fetch(url, opts);
    let data = null;
    try { data = await r.json(); } catch (_) {}
    if (!r.ok) throw new Error((data && data.error) || ('HTTP ' + r.status));
    return data;
  }

  // ── Metrics auto-refresh on the overview page ──────────────────────────
  const m = document.getElementById('m-guilds');
  if (m) {
    async function refresh() {
      try {
        const s = await req('GET', '/api/metrics');
        const set = (id, v) => { const e = document.getElementById(id); if (e) e.textContent = v; };
        set('m-guilds', s.guilds);
        set('m-members', s.members_cached);
        set('m-channels', s.channels_cached);
        set('m-roles', s.roles_cached);
        set('m-latency', s.gateway_latency);
        set('m-uptime', s.uptime);
        set('m-mods', s.modules_loaded + '/' + s.modules_available);
        set('m-cmds', s.commands);
        if (s.runtime) { set('m-mem', s.runtime.alloc_mb); set('m-goros', s.runtime.goroutines); }
      } catch (_) {}
    }
    refresh();
    setInterval(refresh, 5000);
  }

  // ── Command filter ───────────────────────────────────────────────────────
  const search = document.getElementById('cmd-search');
  if (search) {
    search.addEventListener('input', () => {
      const q = search.value.toLowerCase();
      document.querySelectorAll('details.cmd').forEach(d => {
        const t = d.textContent.toLowerCase();
        d.style.display = (!q || t.includes(q)) ? '' : 'none';
      });
    });
  }
  const rawBox = document.getElementById('cmd-raw');
  if (rawBox) {
    rawBox.addEventListener('change', () => {
      const u = new URL(location.href);
      u.searchParams.set('raw', rawBox.checked ? 'true' : 'false');
      location.href = u;
    });
  }

  // ── Modules load/unload/reload ──────────────────────────────────────────
  document.querySelectorAll('.act[data-action]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const tr = btn.closest('tr');
      const name = tr.dataset.module;
      const action = btn.dataset.action;
      btn.disabled = true;
      try {
        await req('POST', '/api/modules/' + encodeURIComponent(name) + '/' + action);
        location.reload();
      } catch (e) {
        alert(e.message); btn.disabled = false;
      }
    });
  });

  // ── Core settings save ──────────────────────────────────────────────────
  const coreSave = document.getElementById('core-save');
  if (coreSave) {
    coreSave.addEventListener('click', async () => {
      const body = {};
      document.querySelectorAll('[data-corekey]').forEach(el => { body[el.dataset.corekey] = el.value; });
      const st = document.querySelector('#core-form .save-state');
      try {
        await req('POST', '/api/settings/core', body);
        if (st) { st.textContent = 'saved'; st.className = 'save-state ok'; }
      } catch (e) {
        if (st) { st.textContent = e.message; st.className = 'save-state err'; }
      }
    });
  }

  // ── Module config save (WebConfigurable) ───────────────────────────────
  document.querySelectorAll('.save-module').forEach(btn => {
    btn.addEventListener('click', async () => {
      const form = btn.closest('form');
      const mod = form.dataset.module;
      const guild = form.dataset.guild || '';
      const st = form.querySelector('.save-state');
      const fields = form.querySelectorAll('.cfg-field');
      const tasks = [];
      fields.forEach(field => {
        const wrap = field.closest('.field');
        if (!wrap) return;
        const key = wrap.dataset.key;
        const type = wrap.dataset.type;
        let value;
        if (type === 'toggle') {
          value = field.checked ? 'true' : 'false';
        } else if (type === 'multi') {
          const checked = Array.from(wrap.querySelectorAll('.multi input:checked')).map(c => c.value);
          value = checked.join(',');
        } else if (type === 'secret' || type === 'channel' || type === 'role') {
          if (field.tagName === 'SELECT') value = field.value;
          else value = field.value;
        } else {
          value = field.value;
        }
        // Skip secrets that were left blank (keep existing value).
        if (type === 'secret' && value === '') return;
        tasks.push({ guildID: guild, key, value });
      });
      try {
        for (const t of tasks) {
          await req('POST', '/api/settings/module/' + encodeURIComponent(mod), t);
        }
        if (st) { st.textContent = 'saved'; st.className = 'save-state ok'; }
      } catch (e) {
        if (st) { st.textContent = e.message; st.className = 'save-state err'; }
      }
    });
  });

  // Range value display
  document.querySelectorAll('input[type=range].cfg-field').forEach(r => {
    const out = r.parentElement.querySelector('.rangeval');
    if (out) r.addEventListener('input', () => { out.textContent = r.value; });
  });

  // ── Permissions: add/remove elevated ───────────────────────────────────
  const addBtn = document.getElementById('elevated-add-btn');
  if (addBtn) {
    addBtn.addEventListener('click', async () => {
      const id = document.getElementById('elevated-id').value.trim();
      if (!id) return;
      try { await req('POST', '/api/permissions/elevated/add', { id }); location.reload(); }
      catch (e) { alert(e.message); }
    });
  }
  document.querySelectorAll('.elrm').forEach(b => {
    b.addEventListener('click', async () => {
      try { await req('POST', '/api/permissions/elevated/remove', { id: b.dataset.id }); location.reload(); }
      catch (e) { alert(e.message); }
    });
  });

  // ── Logs ───────────────────────────────────────────────────────────────
  const logBox = document.getElementById('log-box');
  if (logBox) {
    const refreshBtn = document.getElementById('logs-refresh');
    const moreBtn = document.getElementById('logs-more');
    async function load(n) {
      try { const d = await req('GET', '/api/logs?tail=' + n); logBox.textContent = (d.lines || []).join('\n'); }
      catch (e) { logBox.textContent = 'error: ' + e.message; }
    }
    if (refreshBtn) refreshBtn.addEventListener('click', () => load(200));
    if (moreBtn) moreBtn.addEventListener('click', () => load(500));
  }
})();