'use strict';
// Wasabi Backup Dashboard: a dependency-free SPA. All dynamic text goes through
// textContent (never innerHTML), and there are no inline scripts/styles, so the
// strict CSP (default-src 'self') holds.

const app = document.getElementById('app');

// ---------- helpers ----------------------------------------------------------
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === false || v == null) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'value') el.value = v;
    else if (k === 'checked' || k === 'disabled' || k === 'selected') el[k] = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    el.append(k.nodeType ? k : document.createTextNode(String(k)));
  }
  return el;
}
const clear = (el) => { while (el.firstChild) el.removeChild(el.firstChild); return el; };

async function api(method, url, body) {
  const r = await fetch(url, {
    method, credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'dashboard' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (r.status === 401 && !url.endsWith('/login')) { renderLogin(); throw new Error('Please sign in'); }
  const text = await r.text();
  let data = null; try { data = text ? JSON.parse(text) : null; } catch { /* not JSON */ }
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}

function toast(msg, bad) {
  const t = h('div', { class: 'rounded-md px-4 py-2 text-sm shadow-lg ' + (bad ? 'bg-rose-700 text-white' : 'bg-emerald-700 text-white') }, msg);
  document.getElementById('toasts').append(t);
  setTimeout(() => t.remove(), bad ? 7000 : 3500);
}
const guard = (fn) => async (...a) => { try { return await fn(...a); } catch (e) { toast(e.message, true); } };

const fmtTime = (s) => s ? new Date(s * 1000).toLocaleString() : '-';
function ago(s) {
  if (!s) return 'never';
  const d = Math.max(0, Date.now() / 1000 - s);
  if (d < 60) return 'just now';
  if (d < 3600) return Math.floor(d / 60) + ' min ago';
  if (d < 86400) return Math.floor(d / 3600) + ' h ago';
  return Math.floor(d / 86400) + ' d ago';
}
// Decimal units (1 GB = 1,000,000,000 bytes), as storage providers bill.
function fmtBytes(n) {
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']; let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i ? String(Math.round(n * 10) / 10) : n) + ' ' + u[i];
}
function duration(run) {
  const end = run.finished_at || Math.floor(Date.now() / 1000);
  const s = Math.max(0, end - run.started_at);
  return s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's';
}
const statusClass = {
  success: 'bg-emerald-900/60 text-emerald-300', failed: 'bg-rose-900/60 text-rose-300',
  running: 'bg-sky-900/60 text-sky-300', cancelled: 'bg-amber-900/60 text-amber-300',
};
const TRIGGERS = { manual: 'Manual', schedule: 'Scheduled', 'dry-run': 'Dry run', verify: 'Verify' };
const triggerLabel = (t) => TRIGGERS[t] || t;
function fmtUptime(sec) {
  if (sec < 3600) return Math.floor(sec / 60) + ' min';
  if (sec < 86400) return Math.floor(sec / 3600) + ' h';
  return Math.floor(sec / 86400) + ' d';
}
function until(ts) {
  const d = ts - Date.now() / 1000;
  if (d < 60) return 'in under a minute';
  if (d < 3600) return 'in ' + Math.round(d / 60) + ' min';
  if (d < 86400) return 'in ' + Math.round(d / 3600) + ' h';
  return 'in ' + Math.round(d / 86400) + ' d';
}
// Renders a credential test result: one line per step with ✓ / ✗ / –.
function testResultView(r) {
  const icon = { ok: ['✓', 'text-emerald-400'], failed: ['✗', 'text-rose-400'], skipped: ['–', 'text-slate-600'] };
  return h('div', { class: 'space-y-1 rounded-md border p-3 text-sm ' + (r.ok ? 'border-emerald-800 bg-emerald-950/30' : 'border-rose-800 bg-rose-950/30') },
    h('p', { class: 'font-medium ' + (r.ok ? 'text-emerald-300' : 'text-rose-300') },
      r.ok ? 'Credentials work' : 'Credential test failed', h('span', { class: 'ml-2 text-xs font-normal text-slate-400' }, `tested from agent ${r.agent} → ${r.endpoint}`)),
    r.steps.map((st) => h('div', { class: 'flex gap-2' },
      h('span', { class: 'w-4 ' + icon[st.status][1] }, icon[st.status][0]),
      h('span', { class: st.status === 'skipped' ? 'text-slate-500' : '' }, st.name),
      st.status !== 'skipped' ? h('span', { class: 'text-xs text-slate-500' }, st.ms + ' ms') : null,
      st.detail ? h('span', { class: 'min-w-0 flex-1 break-words text-xs ' + (st.status === 'failed' ? 'text-rose-300' : 'text-slate-400') }, st.detail) : null)));
}
const badge = (s) => h('span', { class: 'rounded px-2 py-0.5 text-xs font-medium ' + (statusClass[s] || 'bg-slate-800') }, s);

// ---------- shell -------------------------------------------------------------
let poller = null;
const stopPolling = () => { if (poller) { clearInterval(poller); poller = null; } };

function layout(active, ...content) {
  const link = (href, label, key) => h('a', {
    href, class: 'rounded-md px-3 py-1.5 text-sm font-medium ' + (active === key ? 'bg-slate-800 text-white' : 'text-slate-400 hover:text-white'),
  }, label);
  clear(app).append(
    h('header', { class: 'border-b border-slate-800 bg-slate-900' },
      h('div', { class: 'mx-auto flex max-w-7xl items-center gap-2 px-4 py-3' },
        h('div', { class: 'mr-4 text-base font-semibold text-emerald-400' }, 'Wasabi Backup'),
        link('#/overview', 'Overview', 'overview'), link('#/agents', 'Agents', 'agents'), link('#/credentials', 'Credentials', 'creds'), link('#/runs', 'History', 'runs'),
        h('div', { class: 'flex-1' }),
        h('button', { class: 'btn btn-ghost', onclick: guard(async () => { await api('POST', '/api/logout'); renderLogin(); }) }, 'Sign out'))),
    h('main', { class: 'mx-auto max-w-7xl px-4 py-6' }, ...content));
}

function renderLogin() {
  stopPolling();
  const user = h('input', { class: 'input', autocomplete: 'username', placeholder: 'Username' });
  const pass = h('input', { class: 'input', type: 'password', autocomplete: 'current-password', placeholder: 'Password' });
  const submit = guard(async (e) => {
    e.preventDefault();
    await api('POST', '/api/login', { username: user.value, password: pass.value });
    route();
  });
  clear(app).append(h('div', { class: 'flex min-h-screen items-center justify-center px-4' },
    h('form', { class: 'card w-full max-w-sm space-y-4', onsubmit: submit },
      h('h1', { class: 'text-xl font-semibold text-emerald-400' }, 'Wasabi Backup'),
      h('p', { class: 'text-sm text-slate-400' }, 'Sign in to the central dashboard.'),
      user, pass, h('button', { class: 'btn btn-primary w-full justify-center', type: 'submit' }, 'Sign in'))));
  user.focus();
}

// ---------- router ------------------------------------------------------------
async function route() {
  stopPolling();
  const [, section, id] = (location.hash || '#/overview').split('/');
  try {
    await api('GET', '/api/me');
  } catch { return; }
  try {
    if (section === 'credentials') await viewCredentials();
    else if (section === 'runs' && id) await viewRun(id);
    else if (section === 'runs') await viewRuns();
    else if (section === 'agents' && id) await viewAgent(id);
    else if (section === 'agents') await viewAgents();
    else await viewOverview();
  } catch (e) { toast(e.message, true); }
}
window.addEventListener('hashchange', route);

// ---------- agents ------------------------------------------------------------
async function viewAgents() {
  const agents = await api('GET', '/api/agents');
  const keyBox = h('div');

  const nameIn = h('input', { class: 'input', placeholder: 'e.g. nas-01' });
  const add = guard(async (e) => {
    e.preventDefault();
    const r = await api('POST', '/api/agents', { name: nameIn.value });
    nameIn.value = '';
    showKey(keyBox, r.name, r.api_key);
    toast('Agent created');
  });

  const rows = agents.map((a) => h('tr', { class: 'border-t border-slate-800' },
    h('td', { class: 'td' }, h('span', { class: 'mr-2 inline-block h-2 w-2 rounded-full ' + (a.online ? 'bg-emerald-400' : 'bg-slate-600') }),
      h('a', { class: 'font-medium text-sky-400 hover:underline', href: '#/agents/' + a.id }, a.name)),
    h('td', { class: 'td text-slate-400' }, a.hostname || '-'),
    h('td', { class: 'td text-slate-400' }, a.rclone_version || '-'),
    h('td', { class: 'td text-slate-400' }, a.online ? 'online' : ago(a.last_seen_at)),
    h('td', { class: 'td text-right space-x-2' },
      h('a', { class: 'btn btn-primary', href: '#/agents/' + a.id }, 'Browse & configure'),
      h('button', { class: 'btn btn-ghost', onclick: guard(async () => {
        if (!confirm('Issue a new API key for ' + a.name + '? The old key stops working immediately.')) return;
        const r = await api('POST', `/api/agents/${a.id}/rotate-key`);
        showKey(keyBox, a.name, r.api_key);
      }) }, 'Rotate key'),
      h('button', { class: 'btn btn-danger', onclick: guard(async () => {
        if (!confirm('Delete agent ' + a.name + ' and all its jobs and history?')) return;
        await api('DELETE', '/api/agents/' + a.id); route();
      }) }, 'Delete'))));

  layout('agents',
    h('div', { class: 'mb-4 flex items-end justify-between gap-4' },
      h('h2', { class: 'text-lg font-semibold' }, 'Agents'),
      h('form', { class: 'flex gap-2', onsubmit: add }, nameIn, h('button', { class: 'btn btn-primary whitespace-nowrap', type: 'submit' }, 'Add agent'))),
    keyBox,
    h('div', { class: 'card overflow-x-auto p-0' },
      agents.length ? h('table', { class: 'w-full' },
        h('thead', {}, h('tr', {}, ['Name', 'Host', 'rclone', 'Status', ''].map((t) => h('th', { class: 'th' }, t)))),
        h('tbody', {}, rows))
        : h('p', { class: 'p-6 text-sm text-slate-400' }, 'No agents yet. Add one to get an API key.')));
}

function showKey(box, name, key) {
  const origin = location.origin;
  const cmd = `curl -fsSL ${origin}/install.sh | sudo sh -s -- --url ${origin} --key ${key}`;
  const copy = (text, btn) => async () => {
    try { await navigator.clipboard.writeText(text); btn.textContent = 'Copied'; setTimeout(() => { btn.textContent = 'Copy'; }, 1500); }
    catch { toast('Copy failed: select the text and copy it manually', true); }
  };
  const cmdBtn = h('button', { class: 'btn btn-primary shrink-0' }, 'Copy');
  cmdBtn.addEventListener('click', copy(cmd, cmdBtn));
  clear(box).append(h('div', { class: 'card mb-4 space-y-3 border-emerald-700' },
    h('p', { class: 'text-sm' }, 'Install ', h('b', {}, name), ': run this on the Linux machine you want to back up. It contains the agent\u2019s key, which is not shown again.'),
    h('div', { class: 'flex items-start gap-2' },
      h('code', { class: 'block flex-1 break-all rounded bg-slate-950 p-2 text-xs text-emerald-300 select-all' }, cmd), cmdBtn),
    h('p', { class: 'text-xs text-slate-400' },
      'Needs systemd and curl. It installs the agent and rclone and starts the service, which then shows up here as online. ',
      'By default the dashboard can browse /home, /root, /etc, /srv, /opt and /var/www; append --roots /data,/mnt/photos to change that. ',
      'Docker instead: use docker-compose.agent.yml with AGENT_API_KEY=', h('span', { class: 'font-mono' }, key), '.')));
}

// ---------- agent detail: file explorer + job editor -----------------------------
async function viewAgent(agentId) {
  const [agents, creds, jobs] = await Promise.all([
    api('GET', '/api/agents'), api('GET', '/api/credentials'), api('GET', '/api/jobs?agent_id=' + agentId)]);
  const agent = agents.find((a) => a.id === agentId);
  if (!agent) { toast('Agent not found', true); location.hash = '#/agents'; return; }

  const sel = new Map();           // path -> mode ("copy" | "sync")
  let editing = null;              // job being edited, or null for a new one
  const checkEls = [];             // every rendered tree checkbox, for refreshing
  const explorer = h('div', { class: 'max-h-[28rem] overflow-auto font-mono text-sm' });
  const selectedBox = h('div', { class: 'space-y-1' });
  const jobFormBox = h('div');
  const jobsBox = h('div', { class: 'space-y-2' });
  const connResult = h('span', { class: 'text-xs' });
  const connBtn = h('button', { class: 'btn btn-ghost', onclick: async () => {
    connBtn.disabled = true; connResult.className = 'text-xs text-slate-400'; connResult.textContent = 'Testing…';
    try {
      const r = await api('GET', `/api/agents/${agentId}/status`); const st = r.status;
      connResult.className = 'text-xs text-emerald-300';
      connResult.textContent = `✓ Connected · ${r.rtt_ms} ms round trip · agent ${st.version} · up ${fmtUptime(st.uptime_sec)} · ${st.jobs} job(s), ${st.schedules} schedule(s) · ` +
        (st.running_jobs.length ? 'running: ' + st.running_jobs.join(', ') : 'idle');
    } catch (e) { connResult.className = 'text-xs text-rose-400'; connResult.textContent = '✗ ' + e.message; }
    connBtn.disabled = false;
  } }, 'Test connection');

  // ---- explorer
  const coveredBy = (p) => { // nearest selected ancestor (or itself)
    for (const s of sel.keys()) if (p === s || p.startsWith(s + '/')) return s;
    return null;
  };
  function refreshChecks() {
    for (const cb of checkEls) {
      const c = coveredBy(cb.dataset.path);
      cb.checked = !!c;
      cb.disabled = !!c && c !== cb.dataset.path;
      cb.title = cb.disabled ? 'Included via ' + c : '';
    }
    renderSelected();
  }
  function renderSelected() {
    clear(selectedBox);
    if (!sel.size) { selectedBox.append(h('p', { class: 'text-sm text-slate-500' }, 'Nothing selected. Tick files or folders in the explorer.')); return; }
    for (const p of [...sel.keys()].sort()) {
      selectedBox.append(h('div', { class: 'flex items-center gap-2 rounded bg-slate-950 px-2 py-1' },
        h('span', { class: 'flex-1 truncate font-mono text-xs', title: p }, p),
        h('button', { class: 'text-rose-400 hover:text-rose-300', title: 'Remove', onclick: () => { sel.delete(p); refreshChecks(); } }, '✕')));
    }
  }
  async function loadDir(path) {
    return api('GET', `/api/agents/${agentId}/browse?path=${encodeURIComponent(path)}`);
  }
  function node(entry) {
    const kids = h('ul', { class: 'ml-5 hidden border-l border-slate-800 pl-2' });
    let loaded = false;
    const cb = h('input', { type: 'checkbox', class: 'mr-2 accent-emerald-500', 'data-path': entry.path, onchange: (e) => {
      if (e.target.checked) { for (const s of [...sel.keys()]) if (s.startsWith(entry.path + '/')) sel.delete(s); sel.set(entry.path, 'copy'); }
      else sel.delete(entry.path);
      refreshChecks();
    } });
    checkEls.push(cb);
    const arrow = h('span', { class: 'inline-block w-4 cursor-pointer select-none text-slate-500' }, entry.is_dir ? '▸' : '');
    const toggle = guard(async () => {
      if (!entry.is_dir) return;
      const open = kids.classList.toggle('hidden') === false;
      arrow.textContent = open ? '▾' : '▸';
      if (open && !loaded) {
        loaded = true;
        kids.append(h('li', { class: 'text-slate-500' }, 'loading…'));
        try {
          const res = await loadDir(entry.path);
          clear(kids);
          res.entries.forEach((e) => kids.append(node(e)));
          if (res.truncated) kids.append(h('li', { class: 'text-amber-400' }, 'listing truncated (too many entries)'));
          if (!res.entries.length) kids.append(h('li', { class: 'text-slate-500' }, '(empty)'));
          refreshChecks();
        } catch (e) { clear(kids); kids.append(h('li', { class: 'text-rose-400' }, e.message)); loaded = false; }
      }
    });
    arrow.addEventListener('click', toggle);
    const label = h('span', { class: (entry.is_dir ? 'cursor-pointer text-sky-300' : 'text-slate-300') + (entry.symlink ? ' italic opacity-60' : ''), onclick: toggle },
      (entry.is_dir ? '📁 ' : entry.symlink ? '🔗 ' : '📄 ') + entry.name);
    const meta = h('span', { class: 'ml-2 text-xs text-slate-600' }, entry.is_dir || entry.symlink ? '' : fmtBytes(entry.size));
    return h('li', {}, h('div', { class: 'flex items-center py-0.5' }, arrow, cb, label, meta), kids);
  }
  async function loadRoots() {
    clear(explorer);
    if (!agent.online) { explorer.append(h('p', { class: 'text-amber-400' }, 'Agent is offline: cannot browse its files.')); return; }
    try {
      const res = await loadDir('/');
      const ul = h('ul', {}); res.entries.forEach((e) => ul.append(node(e))); explorer.append(ul); refreshChecks();
    } catch (e) { explorer.append(h('p', { class: 'text-rose-400' }, e.message)); }
  }

  // ---- job form
  function renderJobForm() {
    const job = editing;
    const name = h('input', { class: 'input', value: job ? job.name : '', placeholder: 'e.g. Documents nightly' });
    const credSel = h('select', { class: 'input' }, creds.map((c) => h('option', { value: c.id, selected: job && job.credential_id === c.id }, `${c.name} (${c.bucket})`)));
    const prefix = h('input', { class: 'input', value: job ? job.dest_prefix : 'backups', placeholder: 'backups' });
    const enabled = h('input', { type: 'checkbox', class: 'accent-emerald-500', checked: job ? job.enabled : true });
    const schedBox = h('div', { class: 'space-y-2' });
    const schedules = job ? job.schedules.map((x) => ({ ...x })) : [{ cron_expr: '0 2 * * *', timezone: Sched.browserTimezone(), enabled: true }];
    function renderSched() {
      clear(schedBox);
      schedules.forEach((sc, i) => schedBox.append(Sched.editor(sc, { h, api, onRemove: () => { schedules.splice(i, 1); renderSched(); } })));
      if (!schedules.length) schedBox.append(h('p', { class: 'text-sm text-slate-500' }, 'No schedule: this job only runs when you press Run now.'));
      schedBox.append(h('button', { type: 'button', class: 'btn btn-ghost', onclick: () => { schedules.push({ cron_expr: '0 2 * * *', timezone: Sched.browserTimezone(), enabled: true }); renderSched(); } }, '+ Add schedule'));
    }
    renderSched();

    // Backup type
    let backupType = job ? job.backup_type : 'incremental';
    const retention = h('input', { type: 'number', min: '0', max: '3650', class: 'input w-24', value: String(job ? job.retention_days : 30) });
    const typeBox = h('div', { class: 'space-y-2' });
    function renderType() {
      const card = (value, title, text, extra) => h('label', {
        class: 'block cursor-pointer rounded-md border p-3 ' + (backupType === value ? 'border-emerald-600 bg-emerald-950/30' : 'border-slate-800 hover:border-slate-700'),
      },
      h('div', { class: 'flex items-center gap-2' },
        h('input', { type: 'radio', name: 'backup_type', class: 'accent-emerald-500', checked: backupType === value, onchange: () => { backupType = value; renderType(); } }),
        h('span', { class: 'font-medium' }, title)),
      h('p', { class: 'mt-1 text-xs text-slate-400' }, text), extra);
      clear(typeBox).append(h('div', { class: 'grid gap-2 sm:grid-cols-2' },
        card('incremental', 'Incremental with versions', 'Uploads new and changed files. Before a file is overwritten or deleted in Wasabi, the old copy is moved to a dated version folder, so earlier versions and deleted files can be recovered.'),
        card('sync', 'Mirror sync', 'Keeps Wasabi an exact copy: new files are added, and files deleted on this machine are deleted from Wasabi at the next run. No history, so an accidental deletion is not recoverable from the backup.')),
      backupType === 'incremental' ? h('div', { class: 'flex flex-wrap items-center gap-2 text-sm text-slate-300' }, 'Keep old versions for', retention, 'days',
        h('span', { class: 'text-xs text-slate-500' }, '0 = forever. Wasabi bills stored data for at least 90 days, so shorter periods do not save money.')) : null);
    }
    renderType();

    const save = guard(async (e) => {
      e.preventDefault();
      const body = {
        agent_id: agentId, credential_id: credSel.value, name: name.value, dest_prefix: prefix.value, enabled: enabled.checked,
        backup_type: backupType, retention_days: parseInt(retention.value || '0', 10),
        paths: [...sel.keys()].map((path) => ({ path })),
        schedules: schedules.map((s) => ({ cron_expr: s.cron_expr, timezone: s.timezone || 'UTC', enabled: s.enabled })),
      };
      if (job) await api('PUT', '/api/jobs/' + job.id, body); else await api('POST', '/api/jobs', body);
      toast('Job saved; the agent has been notified');
      route();
    });

    clear(jobFormBox).append(h('form', { class: 'space-y-3', onsubmit: save },
      h('h3', { class: 'font-semibold' }, job ? 'Edit job: ' + job.name : 'New backup job'),
      creds.length ? null : h('p', { class: 'text-sm text-amber-400' }, 'Add Wasabi credentials first (Credentials tab).'),
      h('div', {}, h('label', { class: 'label' }, 'Name'), name),
      h('div', { class: 'grid grid-cols-2 gap-3' },
        h('div', {}, h('label', { class: 'label' }, 'Wasabi credentials'), credSel),
        h('div', {}, h('label', { class: 'label' }, 'Bucket prefix'), prefix)),
      h('div', {}, h('label', { class: 'label' }, 'Selected paths'), selectedBox),
      h('div', {}, h('label', { class: 'label' }, 'Backup type'), typeBox),
      h('div', {}, h('label', { class: 'label' }, 'When to run'), schedBox),
      h('label', { class: 'flex items-center gap-2 text-sm' }, enabled, 'Job enabled'),
      h('div', { class: 'flex gap-2' },
        h('button', { class: 'btn btn-primary', type: 'submit', disabled: !creds.length }, job ? 'Save changes' : 'Create job'),
        job ? h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => { editing = null; sel.clear(); refreshChecks(); renderJobForm(); } }, 'Cancel edit') : null)));
  }

  // ---- existing jobs
  function renderJobs() {
    clear(jobsBox);
    if (!jobs.length) { jobsBox.append(h('p', { class: 'text-sm text-slate-500' }, 'No jobs yet.')); return; }
    for (const j of jobs) {
      const run = (mode, label) => guard(async () => {
        await api('POST', `/api/jobs/${j.id}/run`, mode ? { mode } : undefined);
        toast(label + ' started; see History for the live log'); setTimeout(route, 1500);
      });
      const active = j.schedules.filter((x) => x.enabled);
      const when = active.length ? active.map((x) => Sched.describeCron(x.cron_expr) + (x.timezone && x.timezone !== Sched.browserTimezone() ? ` (${x.timezone})` : '')).join('; ') : 'Manual only';
      jobsBox.append(h('div', { class: 'space-y-2 rounded border border-slate-800 p-3' },
        h('div', { class: 'flex flex-wrap items-center gap-2' },
          h('span', { class: 'font-medium' }, j.name), j.enabled ? null : h('span', { class: 'text-xs text-slate-500' }, '(paused)'),
          h('span', { class: 'rounded bg-slate-800 px-2 py-0.5 text-xs text-slate-300' }, j.backup_type === 'sync' ? 'Mirror sync' : 'Incremental' + (j.retention_days ? ` · ${j.retention_days}d versions` : ' · versions kept forever')),
          j.last_run ? h('a', { href: '#/runs/' + j.last_run.id, title: triggerLabel(j.last_run.trigger) + ' ' + fmtTime(j.last_run.started_at) }, badge(j.last_run.status)) : h('span', { class: 'text-xs text-slate-500' }, 'never run')),
        h('p', { class: 'text-xs text-slate-400' }, `${j.paths.length} path(s) · ${when}`,
          j.next_run ? h('span', { class: 'text-slate-500' }, ` · next ${until(j.next_run)} (${new Date(j.next_run * 1000).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' })} your time)`) : null),
        h('div', { class: 'flex flex-wrap gap-2' },
          h('button', { class: 'btn btn-primary', onclick: run('', 'Backup') }, 'Run now'),
          h('button', { class: 'btn btn-ghost', title: 'Show what would be uploaded or deleted, without changing anything', onclick: run('dry-run', 'Dry run') }, 'Dry run'),
          h('button', { class: 'btn btn-ghost', title: 'Check every file on this machine exists unchanged in Wasabi', onclick: run('verify', 'Verify') }, 'Verify'),
          h('div', { class: 'flex-1' }),
          h('button', { class: 'btn btn-ghost', onclick: () => { editing = j; sel.clear(); j.paths.forEach((p) => sel.set(p.path, 'copy')); refreshChecks(); renderJobForm(); window.scrollTo({ top: 0, behavior: 'smooth' }); } }, 'Edit'),
          h('button', { class: 'btn btn-danger', onclick: guard(async () => { if (!confirm('Delete job ' + j.name + '?')) return; await api('DELETE', '/api/jobs/' + j.id); route(); }) }, 'Delete'))));
    }
  }


  layout('agents',
    h('div', { class: 'mb-4 flex items-center gap-3' },
      h('a', { href: '#/agents', class: 'text-slate-400 hover:text-white' }, '← Agents'),
      h('h2', { class: 'text-lg font-semibold' }, agent.name),
      h('span', { class: 'text-xs ' + (agent.online ? 'text-emerald-400' : 'text-slate-500') }, agent.online ? '● online' : '○ offline'),
      h('span', { class: 'text-xs text-slate-500' }, (agent.hostname || '') + ' · ' + (agent.rclone_version || '')),
      h('div', { class: 'flex-1' }), connResult, connBtn),
    h('div', { class: 'grid gap-6 lg:grid-cols-2' },
      h('div', { class: 'card' }, h('h3', { class: 'mb-2 font-semibold' }, 'Files on this machine'),
        h('p', { class: 'mb-2 text-xs text-slate-500' }, 'Live view of the agent’s mounted directories. Tick what to back up.'), explorer),
      h('div', { class: 'space-y-6' }, h('div', { class: 'card' }, jobFormBox),
        h('div', { class: 'card' }, h('h3', { class: 'mb-2 font-semibold' }, 'Jobs on this agent'), jobsBox))));
  renderSelected(); renderJobForm(); renderJobs();
  await loadRoots();
}

// ---------- credentials -----------------------------------------------------------
async function viewCredentials() {
  const [creds, agents] = await Promise.all([api('GET', '/api/credentials'), api('GET', '/api/agents')]);
  let editing = null;
  const formBox = h('div');
  const online = agents.filter((a) => a.online);
  const agentSel = h('select', { class: 'input w-56' },
    online.length ? [h('option', { value: '' }, 'Any online agent'), online.map((a) => h('option', { value: a.id }, a.name))] : h('option', { value: '' }, 'No agent online'));
  const writeTest = h('input', { type: 'checkbox', class: 'accent-emerald-500', checked: true });
  const testOut = h('div');

  // body: { credential_id?, credential? }
  async function runTest(body, btn) {
    const label = btn.textContent;
    btn.disabled = true; btn.textContent = 'Testing…';
    clear(testOut).append(h('p', { class: 'text-sm text-slate-400' }, 'Running the test on the agent… (up to a minute if the endpoint is unreachable)'));
    try {
      const r = await api('POST', '/api/credentials/test', { ...body, agent_id: agentSel.value, write_test: writeTest.checked });
      clear(testOut).append(testResultView(r));
    } catch (e) {
      clear(testOut).append(h('p', { class: 'text-sm text-rose-400' }, '✗ ' + e.message));
    }
    btn.disabled = false; btn.textContent = label;
  }
  const testControls = h('div', { class: 'space-y-2 rounded-md border border-slate-800 p-3' },
    h('div', { class: 'flex flex-wrap items-center gap-3' },
      h('span', { class: 'text-sm' }, 'Test from'), agentSel,
      h('label', { class: 'flex items-center gap-2 text-sm text-slate-300' }, writeTest, 'Also write, read back and delete a tiny test object')),
    h('p', { class: 'text-xs text-slate-500' }, 'The test runs on the agent, so it checks the network path your backups actually use. A list-only test needs just read access. Wasabi bills deleted objects for a 90-day minimum; the test object is a few bytes.'),
    testOut);

  function renderForm() {
    const c = editing || { name: '', access_key: '', region: 'us-east-1', bucket: '', endpoint: '' };
    const f = {
      name: h('input', { class: 'input', value: c.name, placeholder: 'Production Wasabi' }),
      access_key: h('input', { class: 'input font-mono', value: c.access_key, autocomplete: 'off' }),
      secret_key: h('input', { class: 'input font-mono', type: 'password', autocomplete: 'new-password', placeholder: editing ? '(unchanged)' : '' }),
      region: h('input', { class: 'input', value: c.region, placeholder: 'us-east-1' }),
      bucket: h('input', { class: 'input', value: c.bucket }),
      endpoint: h('input', { class: 'input', value: c.endpoint, placeholder: 'optional; default s3.<region>.wasabisys.com' }),
    };
    const field = (label, el) => h('div', {}, h('label', { class: 'label' }, label), el);
    const testBtn = h('button', { class: 'btn btn-ghost', type: 'button', disabled: !online.length }, 'Test');
    testBtn.addEventListener('click', () => {
      const credential = Object.fromEntries(Object.entries(f).map(([k, el]) => [k, el.value]));
      runTest({ credential, credential_id: editing ? editing.id : '' }, testBtn);
    });
    const save = guard(async (e) => {
      e.preventDefault();
      const body = Object.fromEntries(Object.entries(f).map(([k, el]) => [k, el.value]));
      if (editing) await api('PUT', '/api/credentials/' + editing.id, body); else await api('POST', '/api/credentials', body);
      toast('Credentials saved'); route();
    });
    clear(formBox).append(h('form', { class: 'card space-y-3', onsubmit: save },
      h('h3', { class: 'font-semibold' }, editing ? 'Edit ' + editing.name : 'Add Wasabi credentials'),
      h('div', { class: 'grid gap-3 sm:grid-cols-2' },
        field('Name', f.name), field('Bucket', f.bucket), field('Access key', f.access_key), field('Secret key', f.secret_key),
        field('Region', f.region), field('Endpoint override', f.endpoint)),
      h('p', { class: 'text-xs text-slate-500' }, 'The secret is encrypted at rest (AES-256-GCM) and never shown again. Agents receive it over their authenticated connection and keep it in memory only.'),
      h('div', { class: 'flex gap-2' }, h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save'), testBtn,
        editing ? h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => { editing = null; renderForm(); } }, 'Cancel') : null)));
  }
  renderForm();

  layout('creds', h('h2', { class: 'mb-4 text-lg font-semibold' }, 'Wasabi credentials'),
    h('div', { class: 'card mb-6 overflow-x-auto p-0' }, creds.length ? h('table', { class: 'w-full' },
      h('thead', {}, h('tr', {}, ['Name', 'Bucket', 'Region', 'Access key', ''].map((t) => h('th', { class: 'th' }, t)))),
      h('tbody', {}, creds.map((c) => h('tr', { class: 'border-t border-slate-800' },
        h('td', { class: 'td font-medium' }, c.name), h('td', { class: 'td' }, c.bucket), h('td', { class: 'td' }, c.region),
        h('td', { class: 'td font-mono text-xs text-slate-400' }, c.access_key),
        h('td', { class: 'td space-x-2 text-right' },
          (() => { const b = h('button', { class: 'btn btn-ghost', disabled: !online.length }, 'Test'); b.addEventListener('click', () => runTest({ credential_id: c.id }, b)); return b; })(),
          h('button', { class: 'btn btn-ghost', onclick: () => { editing = c; renderForm(); } }, 'Edit'),
          h('button', { class: 'btn btn-danger', onclick: guard(async () => { if (!confirm('Delete ' + c.name + '?')) return; await api('DELETE', '/api/credentials/' + c.id); route(); }) }, 'Delete'))))))
      : h('p', { class: 'p-6 text-sm text-slate-400' }, 'No credentials yet.')),
    h('div', { class: 'mb-6' }, testControls),
    formBox);
}

// ---------- run history + live log -------------------------------------------------
async function viewRuns() {
  const load = async () => {
    const runs = await api('GET', '/api/runs?limit=100');
    const body = h('tbody', {}, runs.map((r) => h('tr', { class: 'border-t border-slate-800' },
      h('td', { class: 'td' }, badge(r.status)),
      h('td', { class: 'td' }, h('a', { class: 'text-sky-400 hover:underline', href: '#/runs/' + r.id }, r.job_name)),
      h('td', { class: 'td text-slate-400' }, r.agent_name), h('td', { class: 'td ' + (r.trigger === 'dry-run' || r.trigger === 'verify' ? 'text-sky-300' : 'text-slate-400') }, triggerLabel(r.trigger)),
      h('td', { class: 'td text-slate-400' }, fmtTime(r.started_at)), h('td', { class: 'td text-slate-400' }, duration(r)),
      h('td', { class: 'td text-slate-400' }, r.summary))));
    layout('runs', h('h2', { class: 'mb-4 text-lg font-semibold' }, 'Execution history'),
      h('div', { class: 'card overflow-x-auto p-0' }, runs.length ? h('table', { class: 'w-full' },
        h('thead', {}, h('tr', {}, ['Status', 'Job', 'Agent', 'Trigger', 'Started', 'Duration', 'Summary'].map((t) => h('th', { class: 'th' }, t)))), body)
        : h('p', { class: 'p-6 text-sm text-slate-400' }, 'No runs yet.')));
  };
  await load();
  poller = setInterval(() => load().catch(() => {}), 5000);
}

async function viewRun(id) {
  const run = await api('GET', '/api/runs/' + id);
  const logEl = h('pre', { class: 'max-h-[70vh] overflow-auto whitespace-pre-wrap break-all rounded bg-black p-3 font-mono text-xs leading-5 text-slate-300' });
  const head = h('div', { class: 'mb-3 flex flex-wrap items-center gap-3' });
  let after = 0;

  // h() drops null children; the native append() would print "null".
  const renderHead = (r) => clear(head).append(...[
    h('a', { href: '#/runs', class: 'text-slate-400 hover:text-white' }, '← History'),
    h('h2', { class: 'text-lg font-semibold' }, r.job_name), badge(r.status),
    h('span', { class: 'text-xs text-slate-500' }, `${r.agent_name} · ${triggerLabel(r.trigger)} · ${fmtTime(r.started_at)} · ${duration(r)}`),
    r.summary ? h('span', { class: 'text-xs text-slate-400' }, '· ' + r.summary) : null,
    r.status === 'running' ? h('button', { class: 'btn btn-danger', onclick: guard(async () => { await api('POST', `/api/runs/${id}/cancel`); toast('Cancel requested'); }) }, 'Cancel run') : null,
  ].filter(Boolean));

  const lineClass = { stderr: 'text-amber-300', agent: 'text-emerald-300' };
  async function tick() {
    const [r, logs] = await Promise.all([api('GET', '/api/runs/' + id), api('GET', `/api/runs/${id}/logs?after=${after}`)]);
    renderHead(r);
    const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 30;
    for (const l of logs.lines) {
      after = l.seq;
      const t = new Date(l.ts).toLocaleTimeString();
      logEl.append(h('span', { class: lineClass[l.stream] || '' }, `${t}  ${l.line}\n`));
    }
    if (atBottom) logEl.scrollTop = logEl.scrollHeight;
    if (r.status !== 'running' && !logs.lines.length) stopPolling();
  }
  renderHead(run);
  layout('runs', head, logEl);
  await tick();
  poller = setInterval(() => tick().catch(() => {}), 1500);
}

route();
