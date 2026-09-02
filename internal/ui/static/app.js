/* uitester web UI — vanilla JS, no build step, no dependencies.
   Structure:
     api        — fetch wrappers for the JSON API
     router     — hash-based view switching
     scenarios  — list + form editor (steps table, add/remove/reorder)
     run        — live run monitor (SSE with polling fallback)
     results    — history list + detail with inline screenshots
     config     — app config viewer/editor
   Sensitive values are never rendered: the editor shows a mask and only
   sends the real value back when the user typed a new one. */
(function () {
  'use strict';

  // ------------------------------------------------------------------ api --

  async function api(method, path, body) {
    const opts = { method, headers: {} };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(path, opts);
    if (res.status === 204) return null;
    let data = null;
    try { data = await res.json(); } catch (e) { /* non-JSON */ }
    if (!res.ok) {
      const msg = data && data.error ? data.error : res.status + ' ' + res.statusText;
      throw new Error(msg);
    }
    return data;
  }
  const API = {
    scenarios: () => api('GET', '/api/scenarios'),
    scenario: (id) => api('GET', '/api/scenarios/' + encodeURIComponent(id)),
    createScenario: (sc) => api('POST', '/api/scenarios', sc),
    updateScenario: (id, sc) => api('PUT', '/api/scenarios/' + encodeURIComponent(id), sc),
    deleteScenario: (id) => api('DELETE', '/api/scenarios/' + encodeURIComponent(id)),
    tools: () => api('GET', '/api/tools'),
    startRun: (scenario, tool) => api('POST', '/api/run', { scenario, tool: tool || '' }),
    run: (id) => api('GET', '/api/run/' + encodeURIComponent(id)),
    stopRun: (id) => api('POST', '/api/run/' + encodeURIComponent(id) + '/stop'),
    results: () => api('GET', '/api/results'),
    result: (id) => api('GET', '/api/results/' + encodeURIComponent(id)),
    config: () => api('GET', '/api/config'),
    saveConfig: (cfg) => api('PUT', '/api/config', cfg),
  };

  // --------------------------------------------------------------- helpers --

  const el = (id) => document.getElementById(id);

  // ESC_RE matches the five HTML-significant characters and ESCAPES maps
  // each to its entity. Both are written with \xNN escapes so the entity
  // text never appears literally in this source file: editors and
  // formatters that decode HTML entities cannot silently break escaping.
  var ESC_RE = /[\x26\x3C\x3E\x22\x27]/g;
  var ESCAPES = {
    '\x26': '\x26amp;',
    '\x3C': '\x26lt;',
    '\x3E': '\x26gt;',
    '\x22': '\x26quot;',
    '\x27': '\x26#39;',
  };
  function esc(s) {
    return String(s == null ? '' : s).replace(ESC_RE, function (c) {
      return ESCAPES[c];
    });
  }

  function fmtMS(ms) {
    if (ms == null) return '';
    if (ms < 1000) return ms + 'ms';
    if (ms < 60000) return (ms / 1000).toFixed(1) + 's';
    const m = Math.floor(ms / 60000), s = Math.round((ms % 60000) / 1000);
    return m + 'm' + String(s).padStart(2, '0') + 's';
  }

  function fmtTime(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d)) return iso;
    return d.toLocaleString();
  }

  const STEP_TYPES = ['navigate', 'click', 'input', 'wait_for', 'assert_text', 'assert_exist', 'screenshot', 'sleep'];

  let toastTimer = null;
  function toast(msg, kind) {
    const t = el('toast');
    t.textContent = msg;
    t.className = 'toast ' + (kind || '');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.add('hidden'), 3200);
  }

  function statusPill(status) {
    const cls = status === 'passed' ? 'passed' : status === 'failed' ? 'failed' :
      status === 'running' ? 'running' : 'pending';
    const icon = cls === 'passed' ? '✓' : cls === 'failed' ? '✕' :
      cls === 'running' ? '◉' : '…';
    return '<span class="pill ' + cls + '">' + icon + ' ' + esc(status) + '</span>';
  }

  // ---------------------------------------------------------------- theme --

  function initTheme() {
    const saved = localStorage.getItem('uitester-theme');
    if (saved === 'dark' || (!saved && matchMedia('(prefers-color-scheme: dark)').matches)) {
      document.documentElement.classList.add('dark');
    }
    el('themeToggle').addEventListener('click', () => {
      const dark = document.documentElement.classList.toggle('dark');
      localStorage.setItem('uitester-theme', dark ? 'dark' : 'light');
    });
  }

  // --------------------------------------------------------------- router --

  const VIEWS = ['scenarios', 'run', 'results', 'config'];

  function currentRoute() {
    const h = location.hash.replace(/^#\/?/, '');
    const parts = h.split('/');
    const view = VIEWS.includes(parts[0]) ? parts[0] : 'scenarios';
    return { view: view, param: parts[1] || '' };
  }

  function navigate(hash) { location.hash = hash; }

  let activeView = null;
  function renderRoute() {
    const route = currentRoute();
    // Leaving the run view: stop its SSE stream, polling and timers.
    if (activeView === 'run' && route.view !== 'run') Run.leave();
    activeView = route.view;
    VIEWS.forEach(v => el('view-' + v).classList.toggle('hidden', v !== route.view));
    document.querySelectorAll('.nav-link').forEach(a => {
      a.classList.toggle('active', a.dataset.view === route.view);
    });
    if (route.view === 'scenarios') Scenarios.load();
    else if (route.view === 'run') Run.load(route.param);
    else if (route.view === 'results') Results.load(route.param);
    else if (route.view === 'config') Config.load();
  }

  // ------------------------------------------------------------ scenarios --

  const Scenarios = (function () {
    let editingId = null;   // scenario file id being edited, null = new
    let steps = [];          // editor step rows
    let toolNames = [];

    async function load() {
      try {
        const listAndTools = await Promise.all([API.scenarios(), API.tools()]);
        toolNames = listAndTools[1].tools || [];
        renderList(listAndTools[0]);
        fillToolSelect();
      } catch (e) {
        toast('Failed to load scenarios: ' + e.message, 'err');
        renderList([]);
      }
    }

    function fillToolSelect() {
      const sel = el('scTool');
      const prev = sel.value;
      sel.innerHTML = toolNames.map(t => '<option value="' + esc(t) + '">' + esc(t) + '</option>').join('');
      if (toolNames.indexOf(prev) !== -1) sel.value = prev;
    }

    function renderList(list) {
      const wrap = el('scenarioList');
      if (!list.length) {
        wrap.innerHTML = '<div class="empty">No scenarios yet. Click “New Scenario” to create one.</div>';
        return;
      }
      wrap.innerHTML = list.map(function (s) {
        return '<div class="row-card" data-id="' + esc(s.id) + '">' +
          '<div class="row-main">' +
            '<div class="row-title">' + esc(s.name) + '</div>' +
            '<div class="row-sub"><span class="muted">' + esc(s.tool) + '</span>' +
              (s.base_url ? ' · ' + esc(s.base_url) : '') +
              ' · ' + s.steps + ' step' + (s.steps === 1 ? '' : 's') + '</div>' +
          '</div>' +
          (s.last_run_status ? statusPill(s.last_run_status) : '') +
          '<button class="btn btn-accent btn-sm" data-run="' + esc(s.id) + '">▶ Run</button>' +
        '</div>';
      }).join('');

      wrap.querySelectorAll('.row-card').forEach(function (card) {
        card.addEventListener('click', function (ev) {
          if (ev.target.closest('[data-run]')) return;
          openEditor(card.dataset.id);
        });
      });
      wrap.querySelectorAll('[data-run]').forEach(function (btn) {
        btn.addEventListener('click', function (ev) {
          ev.stopPropagation();
          startRun(btn.dataset.run, '');
        });
      });
    }

    // ---- editor ----

    async function openEditor(id) {
      try {
        const sc = await API.scenario(id);
        editingId = id;
        el('editorTitle').textContent = 'Edit scenario';
        el('scName').value = sc.name;
        el('scBaseURL').value = sc.base_url || '';
        fillToolSelect();
        el('scTool').value = sc.tool;
        steps = (sc.steps || []).map(function (s) { return Object.assign({}, s); });
        renderSteps();
        showEditor(true);
        el('scenarioList').querySelectorAll('.row-card').forEach(function (c) {
          c.classList.toggle('selected', c.dataset.id === id);
        });
      } catch (e) {
        toast('Failed to open scenario: ' + e.message, 'err');
      }
    }

    function openNew() {
      editingId = null;
      el('editorTitle').textContent = 'New scenario';
      el('scName').value = '';
      el('scBaseURL').value = '';
      fillToolSelect();
      steps = [{ type: 'wait_for', selector: '', value: '', timeout: '10s', sensitive: false }];
      renderSteps();
      showEditor(true);
      el('scenarioList').querySelectorAll('.row-card').forEach(function (c) {
        c.classList.remove('selected');
      });
      el('scName').focus();
    }

    function showEditor(on) {
      el('scenarioEditor').classList.toggle('hidden', !on);
      if (!on) el('scenarioList').querySelectorAll('.row-card').forEach(function (c) {
        c.classList.remove('selected');
      });
    }

    function renderSteps() {
      const body = el('stepsBody');
      if (!steps.length) {
        body.innerHTML = '<tr><td colspan="7" style="border:none"><div class="empty">No steps — add one below.</div></td></tr>';
        return;
      }
      body.innerHTML = steps.map(function (s, i) {
        const masked = !!s.sensitive;
        const valueShown = masked ? '••••••' : (s.value || '');
        return '<tr data-i="' + i + '">' +
            '<td class="col-num">' + (i + 1) + '</td>' +
            '<td><select data-f="type">' + STEP_TYPES.map(function (t) {
              return '<option' + (t === s.type ? ' selected' : '') + '>' + t + '</option>';
            }).join('') + '</select></td>' +
            '<td><input type="text" data-f="selector" placeholder="#login-button" value="' + esc(s.selector) + '"></td>' +
            '<td><input type="text" data-f="value" placeholder="text" value="' + esc(valueShown) + '"' +
              (masked ? ' data-masked="1"' : '') + '></td>' +
            '<td><input type="text" data-f="timeout" placeholder="10s" value="' + esc(s.timeout || '') + '"></td>' +
            '<td class="col-sens"><input type="checkbox" data-f="sensitive"' + (masked ? ' checked' : '') + '></td>' +
            '<td class="col-ops"><span class="step-actions">' +
              '<button class="btn btn-sm" data-op="up"' + (i === 0 ? ' disabled' : '') + '>↑</button>' +
              '<button class="btn btn-sm" data-op="down"' + (i === steps.length - 1 ? ' disabled' : '') + '>↓</button>' +
              '<button class="btn btn-sm btn-danger" data-op="del">×</button>' +
            '</span></td>' +
          '</tr>';
      }).join('');

      body.querySelectorAll('tr').forEach(function (tr) {
        const i = +tr.dataset.i;
        tr.querySelectorAll('[data-f]').forEach(function (input) {
          input.addEventListener('input', function () {
            const f = input.dataset.f;
            if (f === 'sensitive') {
              steps[i].sensitive = input.checked;
              renderSteps();
              return;
            }
            if (input.dataset.masked) {
              if (input.value === '••••••') return; // untouched mask: keep stored value
              delete input.dataset.masked;          // user typed a new secret
              steps[i].value = input.value;
              return;
            }
            steps[i][f] = input.value;
          });
        });
        tr.querySelectorAll('[data-op]').forEach(function (btn) {
          btn.addEventListener('click', function () {
            const op = btn.dataset.op;
            if (op === 'del') steps.splice(i, 1);
            else if (op === 'up' && i > 0) { const t = steps[i - 1]; steps[i - 1] = steps[i]; steps[i] = t; }
            else if (op === 'down' && i < steps.length - 1) { const t = steps[i + 1]; steps[i + 1] = steps[i]; steps[i] = t; }
            renderSteps();
          });
        });
      });
    }

    function collect() {
      const name = el('scName').value.trim();
      const tool = el('scTool').value;
      const baseURL = el('scBaseURL').value.trim();
      const out = { name: name, tool: tool, steps: [] };
      if (baseURL) out.base_url = baseURL;
      steps.forEach(function (s) {
        const step = { type: s.type };
        if (s.selector) step.selector = s.selector;
        if (s.value) step.value = s.value;
        if (s.timeout) step.timeout = s.timeout;
        if (s.sensitive) step.sensitive = true;
        out.steps.push(step);
      });
      return out;
    }

    async function save() {
      const sc = collect();
      if (!sc.name) { toast('Scenario name is required', 'err'); return; }
      if (!sc.steps.length) { toast('At least one step is required', 'err'); return; }
      try {
        const res = editingId
          ? await API.updateScenario(editingId, sc)
          : await API.createScenario(sc);
        toast('Scenario saved', 'ok');
        editingId = res.id;
        showEditor(false);
        load();
      } catch (e) {
        toast('Save failed: ' + e.message, 'err');
      }
    }

    async function remove() {
      if (!editingId) { showEditor(false); return; }
      let label = editingId;
      try { const sc = await API.scenario(editingId); label = sc.name; } catch (e) { /* use id */ }
      if (!confirm('Delete scenario "' + label + '"? This removes its file.')) return;
      try {
        await API.deleteScenario(editingId);
        toast('Scenario deleted', 'ok');
        editingId = null;
        showEditor(false);
        load();
      } catch (e) {
        toast('Delete failed: ' + e.message, 'err');
      }
    }

    function bind() {
      el('btnNewScenario').addEventListener('click', openNew);
      el('btnAddStep').addEventListener('click', function () {
        steps.push({ type: 'click', selector: '', value: '', timeout: '', sensitive: false });
        renderSteps();
      });
      el('btnSaveScenario').addEventListener('click', save);
      el('btnDeleteScenario').addEventListener('click', remove);
      el('btnCancelEdit').addEventListener('click', function () { showEditor(false); });
      el('btnRunScenario').addEventListener('click', function () {
        const sc = collect();
        startRun(sc.name, '');
      });
    }

    return { load: load, bind: bind, openEditor: openEditor };
  })();

  // ------------------------------------------------------------------- run --

  const Run = (function () {
    let runId = null;
    let es = null;               // EventSource
    let pollTimer = null;
    let tickTimer = null;        // elapsed ticker
    let logs = [];

    async function load(id) {
      teardown();
      logs = [];
      if (!id) {
        el('runTitle').textContent = 'Run';
        el('runOverview').innerHTML = '<div class="empty">No run selected. Start one from the Scenarios view.</div>';
        el('runStepsBody').innerHTML = '';
        return;
      }
      runId = id;
      try {
        const run = await API.run(id);
        el('runTitle').textContent = 'Run: ' + (run.scenario ? run.scenario.name : id);
        render(run);
        if (run.status === 'running') {
          connectSSE(id);
          startTicker(run);
        }
      } catch (e) {
        toast('Run not found: ' + e.message, 'err');
        navigate('#/scenarios');
      }
    }

    function connectSSE(id) {
      if (es) es.close();
      es = new EventSource('/api/run/' + encodeURIComponent(id) + '/events');
      es.addEventListener('step', function (ev) {
        const data = JSON.parse(ev.data);
        if (data.step) applyStep(data.step_index, data.step);
      });
      es.addEventListener('done', function () {
        teardown(); // also stops the elapsed ticker
        refresh();
      });
      es.onerror = function () {
        // SSE dropped (proxy timeout, server restart): fall back to polling.
        teardownStream();
        startPolling();
      };
    }

    function startPolling() {
      if (pollTimer) return;
      pollTimer = setInterval(async function () {
        try {
          const run = await API.run(runId);
          render(run);
          if (run.status !== 'running') teardown();
        } catch (e) { teardown(); }
      }, 700);
    }

    function startTicker(run) {
      const started = Date.now() - (run.elapsed_ms || 0);
      tickTimer = setInterval(function () {
        const pill = el('runOverview').querySelector('.run-elapsed');
        if (pill) pill.textContent = 'elapsed ' + fmtMS(Date.now() - started);
      }, 200);
    }

    function applyStep(index, step) {
      const row = el('runStepsBody').querySelector('tr[data-i="' + index + '"]');
      const html = stepRow(step, index, false);
      if (row) row.outerHTML = html;
      else el('runStepsBody').insertAdjacentHTML('beforeend', html);
      if (step.log) {
        logs.push(step.log);
        const body = el('runLogBody');
        body.textContent = logs.join('\n');
        el('runLog').classList.remove('hidden');
        body.scrollTop = body.scrollHeight;
      }
      const shot = latestShot();
      if (shot) showShot(shot);
    }

    function latestShot() {
      let found = null;
      el('runStepsBody').querySelectorAll('tr').forEach(function (r) {
        let atts = [];
        try { atts = JSON.parse(r.dataset.atts || '[]'); } catch (e) { /* ignore */ }
        if (atts.length) found = atts[atts.length - 1].url;
      });
      return found;
    }

    function showShot(url) {
      const img = el('runShotImg');
      if (!img.src.endsWith(url)) img.src = url;
      el('runShotWrap').classList.remove('hidden');
      el('runShotTitle').textContent = 'Screenshot (latest step)';
      el('btnOpenShot').onclick = function () { window.open(url, '_blank'); };
    }

    function stepRow(step, index, pending) {
      const st = pending ? 'pending' : step.status;
      const atts = step.attachments || [];
      const shotLink = atts.length
        ? '<span class="shot-link" data-shot="' + esc(atts[atts.length - 1].url) + '">view</span>'
        : '';
      const action = step.action || {};
      return '<tr data-i="' + index + '" class="step-' + st + '" data-atts="' + esc(JSON.stringify(atts)) + '">' +
          '<td class="col-num">' + (index + 1) + '</td>' +
          '<td>' + esc(action.type || '') + '</td>' +
          '<td class="sel">' + esc(action.selector || '') + '</td>' +
          '<td class="col-status">' + statusPill(st) +
            (step.error ? '<div class="row-sub" style="color:var(--err)">' + esc(step.error) + '</div>' : '') + '</td>' +
          '<td class="col-time">' + (step.duration_ms != null ? fmtMS(step.duration_ms) : '') + '</td>' +
          '<td class="col-shot">' + shotLink + '</td>' +
        '</tr>';
    }

    function render(run) {
      // overview
      const parts = [statusPill(run.status)];
      parts.push('<span class="run-elapsed">elapsed ' + fmtMS(run.elapsed_ms) + '</span>');
      parts.push('<span class="row-sub">' + run.current_step + ' / ' + run.step_count + ' steps</span>');
      if (run.error) parts.push('<div class="run-err">' + esc(run.error) + '</div>');
      el('runOverview').innerHTML = parts.join('');
      el('btnStopRun').disabled = run.status !== 'running';

      // steps table: completed steps first, pending from the plan
      const done = run.steps || [];
      let html = done.map(function (s, i) { return stepRow(s, i, false); }).join('');
      const lastDone = done.length ? done[done.length - 1] : null;
      const failed = lastDone && lastDone.status === 'failed';
      for (let i = done.length; i < run.step_count && !failed; i++) {
        const plan = (run.scenario && run.scenario.steps && run.scenario.steps[i]) || {};
        html += stepRow({ action: plan, status: 'pending' }, i, true);
      }
      el('runStepsBody').innerHTML = html;
      el('runStepsBody').querySelectorAll('[data-shot]').forEach(function (lnk) {
        lnk.addEventListener('click', function () { showShot(lnk.dataset.shot); });
      });

      // logs from completed steps
      logs = done.filter(function (s) { return s.log; }).map(function (s) { return s.log; });
      if (logs.length) {
        el('runLogBody').textContent = logs.join('\n');
        el('runLog').classList.remove('hidden');
      }

      // latest screenshot
      const shot = latestShot();
      if (shot) showShot(shot); else el('runShotWrap').classList.add('hidden');
    }

    async function refresh() {
      try {
        const run = await API.run(runId);
        render(run);
      } catch (e) { /* run evicted; leave UI as-is */ }
    }

    function teardownStream() {
      if (es) { es.close(); es = null; }
    }

    function teardown() {
      teardownStream();
      if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
      if (tickTimer) { clearInterval(tickTimer); tickTimer = null; }
    }

    function bind() {
      el('btnStopRun').addEventListener('click', async function () {
        try {
          await API.stopRun(runId);
          toast('Stopping run…');
        } catch (e) { toast(e.message, 'err'); }
      });
      el('btnBackFromRun').addEventListener('click', function () {
        teardown();
        navigate('#/scenarios');
      });
    }

    return { load: load, bind: bind, leave: teardown };
  })();

  // --------------------------------------------------------------- results --

  const Results = (function () {
    async function load(selectedId) {
      try {
        const list = await API.results();
        renderList(list, selectedId);
      } catch (e) {
        toast('Failed to load results: ' + e.message, 'err');
      }
    }

    function renderList(list, selectedId) {
      const wrap = el('resultsList');
      if (!list.length) {
        wrap.innerHTML = '<div class="empty">No runs yet in this session. Start one from the Scenarios view.</div>';
        el('resultDetail').classList.add('hidden');
        el('resultShotWrap').classList.add('hidden');
        return;
      }
      wrap.innerHTML = list.map(function (r) {
        return '<div class="row-card' + (r.run_id === selectedId ? ' selected' : '') + '" data-id="' + esc(r.run_id) + '">' +
          '<div class="row-main">' +
            '<div class="row-title">' + esc(r.scenario_name) + '</div>' +
            '<div class="row-sub">' + fmtTime(r.started_at) + ' · <span class="muted">' + esc(r.tool) + '</span> · ' + fmtMS(r.duration_ms) + '</div>' +
          '</div>' +
          statusPill(r.status) +
        '</div>';
      }).join('');
      wrap.querySelectorAll('.row-card').forEach(function (card) {
        card.addEventListener('click', function () { openDetail(card.dataset.id); });
      });
      if (selectedId) openDetail(selectedId);
    }

    async function openDetail(id) {
      el('resultShotWrap').classList.add('hidden');
      try {
        const res = await API.result(id);
        el('resultsList').querySelectorAll('.row-card').forEach(function (c) {
          c.classList.toggle('selected', c.dataset.id === id);
        });
        el('resultTitle').textContent = res.scenario_name;
        el('resultMeta').innerHTML = [
          ['Status', statusPill(res.status)],
          ['Tool', esc(res.tool)],
          ['Started', fmtTime(res.started_at)],
          ['Duration', fmtMS(res.duration_ms)],
          ['Run ID', '<span class="mono">' + esc(String(res.run_id).slice(0, 12)) + '…</span>'],
        ].map(function (m) {
          return '<div class="meta-item"><div class="k">' + m[0] + '</div><div class="v">' + m[1] + '</div></div>';
        }).join('');

        el('resultStepsBody').innerHTML = res.steps.map(function (s) {
          const atts = s.attachments || [];
          return '<tr>' +
            '<td class="col-num">' + (s.index + 1) + '</td>' +
            '<td>' + esc(s.action.type) + '</td>' +
            '<td class="sel">' + esc(s.action.selector || '') +
              (s.error ? '<div class="row-sub" style="color:var(--err)">' + esc(s.error) + '</div>' : '') + '</td>' +
            '<td class="col-status">' + statusPill(s.status) + '</td>' +
            '<td class="col-time">' + fmtMS(s.duration_ms) + '</td>' +
            '<td class="col-shot">' + (atts.length
              ? '<span class="shot-link" data-shot="' + esc(atts[atts.length - 1].url) + '">view</span>'
              : '') + '</td>' +
          '</tr>';
        }).join('');
        el('resultStepsBody').querySelectorAll('[data-shot]').forEach(function (lnk) {
          lnk.addEventListener('click', function () { showShot(lnk.dataset.shot); });
        });
        el('resultDetail').classList.remove('hidden');
        el('resultDetail').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      } catch (e) {
        toast('Failed to open result: ' + e.message, 'err');
      }
    }

    function showShot(url) {
      el('resultShotImg').src = url;
      el('resultShotWrap').classList.remove('hidden');
      el('btnOpenResultShot').onclick = function () { window.open(url, '_blank'); };
    }

    function bind() {
      el('btnRefreshResults').addEventListener('click', function () { load(); });
      el('btnCloseResult').addEventListener('click', function () {
        el('resultDetail').classList.add('hidden');
        el('resultShotWrap').classList.add('hidden');
      });
    }

    return { load: load, bind: bind };
  })();

  // ---------------------------------------------------------------- config --

  const Config = (function () {
    async function load() {
      try {
        const cfg = await API.config();
        el('configText').value = JSON.stringify(cfg, null, 2);
        el('configStatus').classList.add('hidden');
      } catch (e) {
        el('configText').value = '';
        toast('Failed to load config: ' + e.message, 'err');
      }
    }

    async function save() {
      let cfg;
      try {
        cfg = JSON.parse(el('configText').value);
      } catch (e) {
        showStatus('err', 'Invalid JSON: ' + e.message);
        return;
      }
      try {
        const saved = await API.saveConfig(cfg);
        el('configText').value = JSON.stringify(saved, null, 2);
        showStatus('ok', 'Configuration saved. New runs will use it.');
        toast('Config saved', 'ok');
      } catch (e) {
        showStatus('err', e.message);
      }
    }

    function showStatus(kind, msg) {
      const s = el('configStatus');
      s.textContent = msg;
      s.className = 'form-status ' + kind;
    }

    function bind() {
      el('btnSaveConfig').addEventListener('click', save);
    }

    return { load: load, bind: bind };
  })();

  // ------------------------------------------------------------ run start --

  async function startRun(scenarioRef, toolOverride) {
    try {
      const res = await API.startRun(scenarioRef, toolOverride);
      navigate('#/run/' + res.run_id);
    } catch (e) {
      toast('Failed to start run: ' + e.message, 'err');
    }
  }

  // ------------------------------------------------------------------ boot --

  document.addEventListener('DOMContentLoaded', function () {
    initTheme();
    Scenarios.bind();
    Run.bind();
    Results.bind();
    Config.bind();
    window.addEventListener('hashchange', renderRoute);
    renderRoute();
  });
})();