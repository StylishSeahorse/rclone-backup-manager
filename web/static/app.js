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
function fmtBytes(n) {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + ' ' + u[i];
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
        link('#/agents', 'Agents', 'agents'), link('#/credentials', 'Credentials', 'creds'), link('#/runs', 'History', 'runs'),
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
  const [, section, id] = (location.hash || '#/agents').split('/');
  try {
    await api('GET', '/api/me');
  } catch { return; }
  try {
    if (section === 'credentials') await viewCredentials();
    else if (section === 'runs' && id) await viewRun(id);
    else if (section === 'runs') await viewRuns();
    else if (section === 'agents' && id) await viewAgent(id);
    else await viewAgents();
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
    for (const [p, mode] of [...sel.entries()].sort()) {
      selectedBox.append(h('div', { class: 'flex items-center gap-2 rounded bg-slate-950 px-2 py-1' },
        h('span', { class: 'flex-1 truncate font-mono text-xs', title: p }, p),
        h('select', { class: 'rounded bg-slate-800 px-1 py-0.5 text-xs', title: 'copy never deletes at the destination; sync mirrors deletions', onchange: (e) => sel.set(p, e.target.value) },
          h('option', { value: 'copy', selected: mode === 'copy' }, 'copy'),
          h('option', { value: 'sync', selected: mode === 'sync' }, 'sync')),
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
    const schedules = job ? job.schedules.map((s) => ({ ...s })) : [{ cron_expr: '0 2 * * *', timezone: 'UTC', enabled: true }];

    const presets = { 'Hourly': '0 * * * *', 'Every 6 hours': '@every 6h', 'Daily 02:00': '0 2 * * *', 'Weekly (Sun 03:00)': '0 3 * * 0', 'Monthly (1st 03:00)': '0 3 1 * *' };
    function renderSched() {
      clear(schedBox);
      schedules.forEach((s, i) => {
        const cron = h('input', { class: 'input font-mono', value: s.cron_expr, oninput: (e) => { s.cron_expr = e.target.value; }, placeholder: 'min hour dom mon dow' });
        const tz = h('input', { class: 'input w-40', value: s.timezone, oninput: (e) => { s.timezone = e.target.value; }, placeholder: 'UTC' });
        const preset = h('select', { class: 'input w-40', onchange: (e) => { if (e.target.value) { s.cron_expr = e.target.value; cron.value = e.target.value; } e.target.value = ''; } },
          h('option', { value: '' }, 'Presets…'), Object.entries(presets).map(([k, v]) => h('option', { value: v }, k)));
        schedBox.append(h('div', { class: 'flex flex-wrap items-center gap-2' }, cron, preset, tz,
          h('label', { class: 'flex items-center gap-1 text-xs text-slate-400' }, h('input', { type: 'checkbox', class: 'accent-emerald-500', checked: s.enabled, onchange: (e) => { s.enabled = e.target.checked; } }), 'on'),
          h('button', { type: 'button', class: 'text-rose-400', onclick: () => { schedules.splice(i, 1); renderSched(); } }, '✕')));
      });
      schedBox.append(h('button', { type: 'button', class: 'btn btn-ghost', onclick: () => { schedules.push({ cron_expr: '0 2 * * *', timezone: 'UTC', enabled: true }); renderSched(); } }, '+ Add schedule'));
    }
    renderSched();

    const save = guard(async (e) => {
      e.preventDefault();
      const body = {
        agent_id: agentId, credential_id: credSel.value, name: name.value, dest_prefix: prefix.value, enabled: enabled.checked,
        paths: [...sel.entries()].map(([path, mode]) => ({ path, mode })),
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
      h('div', {}, h('label', { class: 'label' }, 'Schedules (cron, evaluated by the agent)'), schedBox),
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
      jobsBox.append(h('div', { class: 'rounded border border-slate-800 p-3' },
        h('div', { class: 'flex items-center gap-2' },
          h('span', { class: 'font-medium' }, j.name), j.enabled ? null : h('span', { class: 'text-xs text-slate-500' }, '(disabled)'),
          j.last_run ? h('a', { href: '#/runs/' + j.last_run.id }, badge(j.last_run.status)) : h('span', { class: 'text-xs text-slate-500' }, 'never run'),
          h('div', { class: 'flex-1' }),
          h('button', { class: 'btn btn-ghost', onclick: guard(async () => { await api('POST', `/api/jobs/${j.id}/run`); toast('Run requested'); setTimeout(route, 1500); }) }, 'Run now'),
          h('button', { class: 'btn btn-ghost', onclick: () => { editing = j; sel.clear(); j.paths.forEach((p) => sel.set(p.path, p.mode)); refreshChecks(); renderJobForm(); window.scrollTo({ top: 0, behavior: 'smooth' }); } }, 'Edit'),
          h('button', { class: 'btn btn-danger', onclick: guard(async () => { if (!confirm('Delete job ' + j.name + '?')) return; await api('DELETE', '/api/jobs/' + j.id); route(); }) }, 'Delete')),
        h('p', { class: 'mt-1 text-xs text-slate-400' }, `${j.paths.length} path(s) · ` + (j.schedules.filter((s) => s.enabled).map((s) => s.cron_expr).join(', ') || 'no schedule'))));
    }
  }

  layout('agents',
    h('div', { class: 'mb-4 flex items-center gap-3' },
      h('a', { href: '#/agents', class: 'text-slate-400 hover:text-white' }, '← Agents'),
      h('h2', { class: 'text-lg font-semibold' }, agent.name),
      h('span', { class: 'text-xs ' + (agent.online ? 'text-emerald-400' : 'text-slate-500') }, agent.online ? '● online' : '○ offline'),
      h('span', { class: 'text-xs text-slate-500' }, (agent.hostname || '') + ' · ' + (agent.rclone_version || ''))),
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
  const creds = await api('GET', '/api/credentials');
  let editing = null;
  const formBox = h('div');

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
      h('div', { class: 'flex gap-2' }, h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save'),
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
          h('button', { class: 'btn btn-ghost', onclick: () => { editing = c; renderForm(); } }, 'Edit'),
          h('button', { class: 'btn btn-danger', onclick: guard(async () => { if (!confirm('Delete ' + c.name + '?')) return; await api('DELETE', '/api/credentials/' + c.id); route(); }) }, 'Delete'))))))
      : h('p', { class: 'p-6 text-sm text-slate-400' }, 'No credentials yet.')),
    formBox);
}

// ---------- run history + live log -------------------------------------------------
async function viewRuns() {
  const load = async () => {
    const runs = await api('GET', '/api/runs?limit=100');
    const body = h('tbody', {}, runs.map((r) => h('tr', { class: 'border-t border-slate-800' },
      h('td', { class: 'td' }, badge(r.status)),
      h('td', { class: 'td' }, h('a', { class: 'text-sky-400 hover:underline', href: '#/runs/' + r.id }, r.job_name)),
      h('td', { class: 'td text-slate-400' }, r.agent_name), h('td', { class: 'td text-slate-400' }, r.trigger),
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
    h('span', { class: 'text-xs text-slate-500' }, `${r.agent_name} · ${r.trigger} · ${fmtTime(r.started_at)} · ${duration(r)}`),
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
