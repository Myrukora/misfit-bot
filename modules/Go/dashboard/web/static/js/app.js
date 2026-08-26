// Dashboard client: vanilla JS, no dependencies. CSRF via <meta name="csrf-token">.
(function () {
  'use strict';

  const csrf = document.querySelector('meta[name="csrf-token"]')?.content || '';
  const apiHeaders = { 'Content-Type': 'application/json' };
  if (csrf) apiHeaders['X-CSRF-Token'] = csrf;
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // Bounded fetch: abort slow/hung requests instead of leaving them pending.
  async function req(method, url, body, timeoutMs) {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), timeoutMs || 15000);
    try {
      const opts = { method, headers: { ...apiHeaders }, signal: ctl.signal };
      if (body !== undefined) opts.body = JSON.stringify(body);
      const r = await fetch(url, opts);
      let data = null;
      try { data = await r.json(); } catch (_) {}
      if (!r.ok) throw new Error((data && data.error) || ('HTTP ' + r.status));
      return data;
    } catch (e) {
      if (e.name === 'AbortError') throw new Error('request timed out');
      throw e;
    } finally {
      clearTimeout(timer);
    }
  }

  // ── Toast notifications ────────────────────────────────────────────────
  function toast(msg, kind) {
    const box = document.getElementById('toasts');
    if (!box) { window.alert(msg); return; } // no toasts container (e.g. denial page)
    const t = document.createElement('div');
    t.className = 'toast ' + (kind || 'info');
    t.textContent = msg;
    box.appendChild(t);
    setTimeout(() => {
      t.classList.add('out');
      setTimeout(() => t.remove(), 300);
    }, 3400);
  }

  // Replaces a button's content with a spinner while an async task runs.
  // Returns a function that restores the button.
  function spin(btn) {
    const old = btn.innerHTML;
    btn.disabled = true;
    btn.innerHTML = '<span class="spinner"></span>';
    return () => { btn.innerHTML = old; btn.disabled = false; };
  }

  // ── Scroll reveal (skipped entirely for reduced motion) ────────────────
  const revealEls = document.querySelectorAll('.reveal:not(.in)');
  if ('IntersectionObserver' in window && !reduceMotion) {
    const io = new IntersectionObserver(entries => {
      entries.forEach(e => {
        if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); }
      });
    }, { threshold: 0.12 });
    revealEls.forEach(el => io.observe(el));
  } else {
    revealEls.forEach(el => el.classList.add('in'));
  }

  // ── Sidebar: desktop collapse + mobile drawer ──────────────────────────
  // Desktop: the sidebar slides away (collapsed) and the topbar hamburger
  // appears to bring it back; the choice persists in localStorage. Mobile:
  // the sidebar becomes an off-canvas drawer that pops in over an overlay.
  const navToggle = document.getElementById('nav-toggle');
  const sidebar = document.getElementById('sidebar');
  const overlay = document.getElementById('drawer-overlay');
  const collapseBtn = document.getElementById('sidebar-collapse');
  const mqMobile = window.matchMedia('(max-width: 900px)');

  if (navToggle && sidebar) {
    const closeDrawer = () => {
      sidebar.classList.remove('open');
      navToggle.setAttribute('aria-expanded', 'false');
      if (overlay) { overlay.hidden = true; overlay.classList.remove('show'); }
    };

    const setCollapsed = (collapsed) => {
      sidebar.classList.toggle('collapsed', collapsed);
      navToggle.setAttribute('aria-expanded', String(!collapsed));
      if (collapseBtn) {
        collapseBtn.setAttribute('aria-expanded', String(!collapsed));
        collapseBtn.setAttribute('aria-label', collapsed ? 'Expand sidebar' : 'Collapse sidebar');
        collapseBtn.title = collapsed ? 'Expand sidebar' : 'Collapse sidebar';
      }
      try { localStorage.setItem('dash_sidebar_collapsed', collapsed ? '1' : '0'); } catch (_) {}
    };

    // Restore the desktop preference on load (never applied on mobile).
    // Go through setCollapsed so the aria-expanded/aria-label state on the
    // toggle and collapse buttons stays in sync with the visual state.
    try {
      if (!mqMobile.matches && localStorage.getItem('dash_sidebar_collapsed') === '1') {
        setCollapsed(true);
      }
    } catch (_) {}

    navToggle.addEventListener('click', () => {
      if (mqMobile.matches) {
        // drawer mode
        const open = sidebar.classList.toggle('open');
        navToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
        sidebar.classList.remove('collapsed');
        if (overlay) {
          overlay.hidden = false;
          requestAnimationFrame(() => overlay.classList.toggle('show', open));
        }
      } else {
        // desktop collapse toggle
        setCollapsed(!sidebar.classList.contains('collapsed'));
      }
    });

    if (collapseBtn) {
      collapseBtn.addEventListener('click', () => setCollapsed(!sidebar.classList.contains('collapsed')));
    }

    if (overlay) overlay.addEventListener('click', closeDrawer);
    // Escape closes the drawer only while it is actually open — on desktop it
    // must not touch the sidebar's exposed state.
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape' && sidebar.classList.contains('open')) closeDrawer();
    });

    // Crossing the breakpoint: forget the collapsed state on mobile, close any
    // open drawer, and re-apply the saved preference when returning to desktop.
    mqMobile.addEventListener('change', (e) => {
      if (e.matches) {
        sidebar.classList.remove('collapsed');
      } else {
        closeDrawer();
        try {
          if (localStorage.getItem('dash_sidebar_collapsed') === '1') sidebar.classList.add('collapsed');
        } catch (_) {}
      }
    });
  }

  // ── Metrics auto-refresh + count-up on the overview page ───────────────
  const m = document.getElementById('m-guilds');
  if (m) {
    // Animate pure-numeric values from their current to the new value.
    // Non-numeric strings (latency, uptime, "3/5" formats) set directly.
    function setMetric(id, v) {
      const el = document.getElementById(id);
      if (!el) return;
      const target = String(v);
      if (el.textContent === target) return;
      if (reduceMotion || !/^\d+$/.test(target) || !/^\d+$/.test(el.textContent)) {
        el.textContent = target;
        return;
      }
      const from = parseInt(el.textContent, 10);
      const to = parseInt(target, 10);
      const dur = 650;
      const t0 = performance.now();
      function tick(now) {
        const p = Math.min(1, (now - t0) / dur);
        const eased = 1 - Math.pow(1 - p, 3);
        el.textContent = Math.round(from + (to - from) * eased);
        if (p < 1) requestAnimationFrame(tick);
      }
      requestAnimationFrame(tick);
    }
    async function refresh() {
      try {
        const s = await req('GET', '/api/metrics');
        setMetric('m-guilds', s.guilds);
        setMetric('m-members', s.members_cached);
        setMetric('m-channels', s.channels_cached);
        setMetric('m-roles', s.roles_cached);
        setMetric('m-latency', s.gateway_latency);
        setMetric('m-uptime', s.uptime);
        // m-mods renders as "n<small>/total</small>" — update the numerator text
        // node and the denominator separately so the muted style survives.
        const mods = document.getElementById('m-mods');
        const modsDen = mods && mods.querySelector('small');
        if (modsDen) {
          if (mods.firstChild && mods.firstChild.nodeType === Node.TEXT_NODE) {
            mods.firstChild.nodeValue = String(s.modules_loaded);
          }
          modsDen.textContent = '/' + s.modules_available;
        } else {
          setMetric('m-mods', s.modules_loaded + '/' + s.modules_available);
        }
        setMetric('m-cmds', s.commands);
        if (s.runtime) { setMetric('m-mem', s.runtime.alloc_mb); setMetric('m-goros', s.runtime.goroutines); }
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

  // ── Guild selector navigation (commands + settings toolbars) ───────────
  // No inline handlers in the templates — the selects carry data-path and
  // this listener owns the navigation (also keeps CSP script-src 'self'
  // compatible).
  document.querySelectorAll('.guild-nav').forEach(sel => {
    sel.addEventListener('change', () => {
      location.href = sel.dataset.path + '?guild=' + encodeURIComponent(sel.value);
    });
  });

  // ── Tickets: close from list or transcript ──────────────────────────────
  document.querySelectorAll('.tk-close').forEach(btn => {
    btn.addEventListener('click', async () => {
      if (!window.confirm('Close ticket ' + btn.dataset.id + '? This cannot be undone.')) return;
      const guild = btn.dataset.guild;
      const id = btn.dataset.id;
      const url = btn.dataset.closeurl || ('/api/tickets/' + encodeURIComponent(guild) + '/' + encodeURIComponent(id) + '/close');
      const restore = spin(btn);
      try {
        await req('POST', url, {});
        toast('Ticket ' + id + ' closed', 'ok');
        setTimeout(() => location.reload(), 700);
      } catch (e) {
        restore();
        toast(e.message, 'err');
      }
    });
  });

  // ── Modules load/unload/reload ──────────────────────────────────────────
  document.querySelectorAll('.act[data-action]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const tr = btn.closest('tr');
      const name = tr.dataset.module;
      const action = btn.dataset.action;
      const restore = spin(btn);
      try {
        await req('POST', '/api/modules/' + encodeURIComponent(name) + '/' + action);
        toast(action + 'ed ' + name, 'ok');
        setTimeout(() => location.reload(), 700);
      } catch (e) {
        restore();
        toast(e.message, 'err');
      }
    });
  });

  // ── Config field collection (shared by core + module saves) ────────────
  // Mirrors the field partial's data-key/data-type contract: toggles read
  // checked, selects/inputs read value, blank secrets are skipped (keep
  // existing value).
  function collectFields(form) {
    const tasks = [];
    form.querySelectorAll('.cfg-field').forEach(field => {
      const wrap = field.closest('.field');
      if (!wrap) return;
      // Owner-only fields render disabled for elevated viewers — never submit them.
      if (wrap.dataset.owneronly === 'true') return;
      const key = wrap.dataset.key;
      const type = wrap.dataset.type;
      // Per-field guild context: guild-scoped fields carry their server,
      // global fields render data-guild="" (empty string) — nullish
      // fallback keeps that explicit empty, so global fields always submit
      // an empty guildID (the API rejects global keys posted with a guild).
      const guild = wrap.dataset.guild ?? form.dataset.guild ?? '';
      let value;
      if (type === 'toggle') {
        value = field.checked ? 'true' : 'false';
      } else if (type === 'multi') {
        value = Array.from(wrap.querySelectorAll('.multi input:checked')).map(c => c.value).join('\n');
      } else {
        value = field.value;
      }
      // Skip secrets that were left blank (keep existing value).
      if (type === 'secret' && value === '') return;
      tasks.push({ key, value, guildID: guild });
    });
    return tasks;
  }

  // ── Core settings save (per section) ───────────────────────────────────
  document.querySelectorAll('.save-section').forEach(btn => {
    btn.addEventListener('click', async () => {
      const form = btn.closest('form');
      if (!form) return;
      const body = {};
      collectFields(form).forEach(t => { body[t.key] = t.value; });
      // spin() replaces the label with a spinner — capture it first so the
      // success toast names the section.
      const label = btn.textContent.replace('Save ', '');
      const restore = spin(btn);
      try {
        await req('POST', '/api/settings/core', body);
        toast(label + ' saved', 'ok');
      } catch (e) {
        toast('Save failed: ' + e.message, 'err');
      } finally {
        restore();
      }
    });
  });

  // ── Updater panel (owner only) ─────────────────────────────────────────
  const updPanel = document.getElementById('updater-status');
  if (updPanel) {
    const statusEl = updPanel;
    // Build status rows with DOM nodes + textContent: repo/branch/notify/
    // last_error are user- or git-controlled strings and must never be parsed
    // as HTML in the owner's session.
    const statusRow = (text, cls) => {
      const d = document.createElement('div');
      if (cls) d.className = cls;
      d.textContent = text;
      return d;
    };
    async function loadStatus() {
      try {
        const s = await req('GET', '/api/updater/status');
        const rows = [
          statusRow((s.enabled === 'true' ? 'enabled' : 'disabled') + ' · ' + (s.repo || 'no repo') + '@' + s.branch,
            s.enabled === 'true' ? 'upd-ok' : 'upd-err'),
          statusRow('interval ' + s.interval + ' · auto_pull ' + s.auto_pull + ' · notify ' + (s.notify_channel || '—')),
          statusRow('last check ' + s.last_check + ' · last seen ' + (s.last_sha || '—')),
        ];
        if (s.last_error) rows.push(statusRow('last error: ' + s.last_error, 'upd-err'));
        statusEl.replaceChildren(...rows);
      } catch (e) {
        statusEl.textContent = 'updater unavailable: ' + e.message;
      }
    }
    loadStatus();
    const checkBtn = document.getElementById('upd-check');
    if (checkBtn) checkBtn.addEventListener('click', async () => {
      const restore = spin(checkBtn);
      try {
        const r = await req('POST', '/api/updater/check');
        if (r.up_to_date) toast('Up to date (' + r.local_sha.slice(0, 7) + ')', 'ok');
        else toast(r.behind + ' new commit(s) available', 'info');
        loadStatus();
      } catch (e) {
        toast(e.message, 'err');
      } finally { restore(); }
    });
    const applyBtn = document.getElementById('upd-apply');
    if (applyBtn) applyBtn.addEventListener('click', async () => {
      if (!confirm('Pull, rebuild and restart the bot now?')) return;
      const restore = spin(applyBtn);
      try {
        await req('POST', '/api/updater/apply');
        toast('Update started — the bot will rebuild and restart', 'ok');
      } catch (e) {
        toast(e.message, 'err');
      } finally { restore(); }
    });
    const testBtn = document.getElementById('upd-test');
    if (testBtn) testBtn.addEventListener('click', async () => {
      const restore = spin(testBtn);
      try {
        await req('POST', '/api/updater/test');
        toast('Sample PR + commit embeds sent', 'ok');
      } catch (e) {
        toast(e.message, 'err');
      } finally { restore(); }
    });
  }

  // ── Module config save (WebConfigurable) ───────────────────────────────
  document.querySelectorAll('.save-module').forEach(btn => {
    btn.addEventListener('click', async () => {
      const form = btn.closest('form');
      const mod = form.dataset.module;
      const tasks = collectFields(form).map(t => ({ guildID: t.guildID, key: t.key, value: t.value }));
      const restore = spin(btn);
      try {
        let saved = 0;
        const failed = [];
        for (const t of tasks) {
          try {
            await req('POST', '/api/settings/module/' + encodeURIComponent(mod), t);
            saved++;
          } catch (e) {
            failed.push(t.key);
          }
        }
        if (failed.length === 0) {
          toast(mod + ' settings saved (' + saved + ')' + (saved < tasks.length ? ' — ' + (tasks.length - saved) + ' unchanged' : ''), 'ok');
        } else {
          toast(mod + ': saved ' + saved + '/' + tasks.length + ', failed: ' + failed.join(', '), 'err');
        }
      } catch (e) {
        toast('Save failed: ' + e.message, 'err');
      } finally {
        restore();
      }
    });
  });

  // Range value bubble + filled track
  document.querySelectorAll('input[type=range].cfg-field').forEach(r => {
    const out = r.parentElement.querySelector('.rangeval');
    const paint = () => {
      const min = Number.isFinite(parseFloat(r.min)) ? parseFloat(r.min) : 0;
      const rawMax = parseFloat(r.max);
      const max = Number.isFinite(rawMax) ? rawMax : 100;
      const val = Number.isFinite(parseFloat(r.value)) ? parseFloat(r.value) : min;
      const span = max - min;
      const pct = span > 0 ? Math.min(100, Math.max(0, ((val - min) / span) * 100)) : 0;
      r.style.setProperty('--fill', pct + '%');
      if (out) out.textContent = r.value;
    };
    r.addEventListener('input', paint);
    paint();
  });

  // ── Permissions: add/remove elevated ───────────────────────────────────
  const addBtn = document.getElementById('elevated-add-btn');
  if (addBtn) {
    const idInput = document.getElementById('elevated-id');
    // Enter in the ID field submits, same as clicking Add.
    if (idInput) {
      idInput.addEventListener('keydown', e => {
        if (e.key === 'Enter') {
          e.preventDefault();
          addBtn.click();
        }
      });
    }
    addBtn.addEventListener('click', async () => {
      const id = idInput.value.trim();
      if (!id) return;
      const restore = spin(addBtn);
      try {
        await req('POST', '/api/permissions/elevated/add', { id });
        toast('Elevated user added', 'ok');
        setTimeout(() => location.reload(), 700);
      } catch (e) {
        restore();
        toast(e.message, 'err');
      }
    });
  }
  document.querySelectorAll('.elrm').forEach(b => {
    b.addEventListener('click', async () => {
      const restore = spin(b);
      try {
        await req('POST', '/api/permissions/elevated/remove', { id: b.dataset.id });
        toast('Elevated user removed', 'ok');
        setTimeout(() => location.reload(), 700);
      } catch (e) {
        restore();
        toast(e.message, 'err');
      }
    });
  });

  // ── Logs ───────────────────────────────────────────────────────────────
  const logBox = document.getElementById('log-box');
  if (logBox) {
    const refreshBtn = document.getElementById('logs-refresh');
    const moreBtn = document.getElementById('logs-more');
    async function load(n, btn) {
      const restore = spin(btn);
      try {
        const d = await req('GET', '/api/logs?tail=' + n);
        logBox.textContent = d.note || (d.lines || []).join('\n');
        logBox.scrollTop = logBox.scrollHeight;
        toast('Logs refreshed (' + (d.lines || []).length + ' lines)', 'info');
        restore();
      } catch (e) {
        restore();
        logBox.textContent = 'error: ' + e.message;
        toast(e.message, 'err');
      }
    }
    if (refreshBtn) refreshBtn.addEventListener('click', () => load(200, refreshBtn));
    if (moreBtn) moreBtn.addEventListener('click', () => load(500, moreBtn));
  }
})();
