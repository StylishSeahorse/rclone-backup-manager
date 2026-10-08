'use strict';
// Job form sections: excludes and MySQL/MariaDB database dumps.
// Uses the h/api/clear/toast helpers from app.js at call time.

// ---- Excludes ------------------------------------------------------------------------

// Suggestions offered for any selected path that is (or contains) a /var tree.
const VAR_SUGGESTIONS = [
  ['/lib/docker/overlay2', 'Docker image layers (re-downloadable)'],
  ['/lib/docker/containers', 'Docker container logs & runtime'],
  ['/lib/docker/buildkit', 'Docker build cache'],
  ['/lib/docker/volumes/*mysql*', 'Raw MySQL volumes (dump the database instead)'],
  ['/lib/docker/volumes/*redis*', 'Redis volumes (cache/queue)'],
  ['/cache', 'System caches'],
  ['/tmp', 'Temporary files'],
  ['/log/journal', 'systemd journal'],
];
const PATTERN_SUGGESTIONS = [['*.sock', 'Sockets'], ['*.pid', 'PID files'], ['*.tmp', 'Temp files'], ['node_modules', 'node_modules folders']];

function excludesEditor(list) {
  const box = h('div', { class: 'space-y-2' });
  let selected = [];
  const input = h('input', { class: 'input font-mono', placeholder: '/var/lib/docker/overlay2  or  *.log' });
  const add = (v) => {
    v = (v || '').trim();
    if (!v || list.includes(v)) return;
    list.push(v); render();
  };
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); add(input.value); input.value = ''; } });

  function suggestions() {
    const out = [];
    for (const p of selected) {
      // a selected /var (or a folder inside a mounted root ending in /var)
      const m = p.match(/^(.*\/var)(\/.*)?$/);
      if (!m) continue;
      const varRoot = m[1];
      for (const [suffix, label] of VAR_SUGGESTIONS) {
        const full = varRoot + suffix;
        if (full.startsWith(p) || p.startsWith(varRoot) && full.startsWith(p.replace(/\/$/, ''))) out.push([full, label]);
      }
    }
    for (const s of PATTERN_SUGGESTIONS) out.push(s);
    const seen = new Set();
    return out.filter(([v]) => !list.includes(v) && !seen.has(v) && seen.add(v));
  }

  function render() {
    clear(box);
    if (list.length) {
      box.append(h('div', { class: 'space-y-1' }, list.map((e, i) => h('div', { class: 'flex items-center gap-2 rounded bg-slate-950 px-2 py-1' },
        h('span', { class: 'flex-1 truncate font-mono text-xs', title: e }, e),
        h('span', { class: 'text-xs text-slate-500' }, e.startsWith('/') && !/[*?[{]/.test(e) ? 'folder' : 'pattern'),
        h('button', { type: 'button', class: 'text-rose-400 hover:text-rose-300', title: 'Remove', onclick: () => { list.splice(i, 1); render(); } }, '✕')))));
    } else {
      box.append(h('p', { class: 'text-sm text-slate-500' }, 'Nothing excluded.'));
    }
    box.append(h('div', { class: 'flex gap-2' }, input, h('button', { type: 'button', class: 'btn btn-ghost whitespace-nowrap', onclick: () => { add(input.value); input.value = ''; } }, 'Add')));
    const sug = suggestions();
    if (sug.length) {
      box.append(h('div', { class: 'flex flex-wrap gap-1.5' }, h('span', { class: 'text-xs text-slate-500' }, 'Suggestions:'),
        sug.map(([v, label]) => h('button', { type: 'button', title: v, class: 'rounded-full border border-slate-700 px-2 py-0.5 text-xs text-slate-300 hover:border-emerald-600 hover:text-white', onclick: () => add(v) }, '+ ' + label))));
    }
    box.append(h('p', { class: 'text-xs text-slate-500' }, 'A full path skips that folder and everything in it. Anything else is a name pattern matched anywhere, e.g. *.log or cache/**. Entries containing * ? [ or { are patterns.'));
  }
  render();
  return { el: box, refresh(paths) { selected = paths; render(); } };
}

// ---- Database dumps ------------------------------------------------------------------------

function dumpsEditor(dumps, { agentId, jobId }) {
  const box = h('div', { class: 'space-y-3' });

  function card(d, i) {
    let mode = d.host ? 'host' : 'docker';
    const result = h('div', { class: 'text-xs' });
    const field = (label, el, cls = '') => h('div', { class: cls }, h('label', { class: 'label' }, label), el);
    const bind = (key, attrs = {}) => {
      const el = h('input', { class: 'input', value: d[key] == null ? '' : String(d[key]), ...attrs });
      el.addEventListener('input', () => { d[key] = attrs.type === 'number' ? parseInt(el.value || '0', 10) : el.value; });
      return el;
    };
    const name = bind('name', { placeholder: 'e.g. ninja-db' });
    const container = bind('container', { placeholder: 'e.g. debian-mysql-1 (see: docker ps)' });
    const host = bind('host', { placeholder: '127.0.0.1' });
    const port = bind('port', { type: 'number', min: '1', max: '65535', placeholder: '3306' });
    const user = bind('user', { placeholder: 'root' });
    const pass = h('input', { class: 'input', type: 'password', autocomplete: 'new-password', placeholder: d.has_password ? '(stored; leave blank to keep)' : '' });
    pass.addEventListener('input', () => { d.password = pass.value; });
    const dbs = h('input', { class: 'input', value: (d.databases || []).join(', '), placeholder: 'blank = all databases' });
    dbs.addEventListener('input', () => { d.databases = dbs.value.split(/[\s,]+/).filter(Boolean); });
    const keep = bind('keep_days', { type: 'number', min: '0', max: '3650', class: 'input w-24' });
    const useEnv = h('input', { type: 'checkbox', class: 'accent-emerald-500', checked: !!d.use_container_env });
    const credBox = h('div', { class: 'grid gap-3 sm:grid-cols-2' });

    function renderCreds() {
      clear(credBox);
      if (mode === 'docker' && d.use_container_env) {
        credBox.append(h('p', { class: 'text-xs text-slate-400 sm:col-span-2' }, 'Signs in as root with the container’s MYSQL_ROOT_PASSWORD (or MARIADB_ROOT_PASSWORD). The password never leaves the container and is not stored here.'));
      } else {
        credBox.append(field('User', user), field('Password', pass));
      }
    }
    useEnv.addEventListener('change', () => { d.use_container_env = useEnv.checked; renderCreds(); });

    const where = h('div', { class: 'space-y-2' });
    function renderWhere() {
      clear(where);
      const tab = (m, label) => h('button', { type: 'button', class: 'rounded px-3 py-1 text-xs ' + (mode === m ? 'bg-slate-700 text-white' : 'text-slate-400 hover:text-white'),
        onclick: () => { mode = m; if (m === 'docker') { d.host = ''; d.port = 0; } else { d.container = ''; d.use_container_env = false; useEnv.checked = false; } renderWhere(); renderCreds(); } }, label);
      where.append(h('div', { class: 'inline-flex rounded-md bg-slate-900 p-0.5' }, tab('docker', 'Docker container'), tab('host', 'Host & port')));
      if (mode === 'docker') {
        where.append(field('Container name', container),
          h('label', { class: 'flex items-center gap-2 text-sm text-slate-300' }, useEnv, 'Use the container’s own root password'));
      } else {
        where.append(h('div', { class: 'grid grid-cols-3 gap-3' }, field('Host', host, 'col-span-2'), field('Port', port)),
          h('p', { class: 'text-xs text-slate-500' }, 'Needs the mysql/mariadb client installed on the agent machine.'));
      }
    }
    renderWhere(); renderCreds();

    const test = h('button', { type: 'button', class: 'btn btn-ghost' }, 'Test connection');
    test.addEventListener('click', async () => {
      test.disabled = true; result.className = 'text-xs text-slate-400'; result.textContent = 'Connecting from the agent…';
      try {
        const r = await api('POST', '/api/dumps/test', { agent_id: agentId, job_id: jobId || '', dump: { ...d, name: d.name || 'test' } });
        clear(result);
        if (r.ok) {
          result.className = 'space-y-1 text-xs text-slate-300';
          const ic = h('span', { class: 'font-bold' }, '✓ '); ic.style.color = '#0ca30c';
          const userDbs = r.databases.filter((x) => !['information_schema', 'performance_schema', 'mysql', 'sys'].includes(x));
          result.append(h('div', {}, ic, `Connected to MySQL/MariaDB ${r.version} in ${r.ms} ms.`),
            h('div', { class: 'flex flex-wrap items-center gap-1.5' }, h('span', { class: 'text-slate-500' }, 'Databases (click to dump only these):'),
              userDbs.map((db) => h('button', { type: 'button', class: 'rounded-full border border-slate-700 px-2 py-0.5 hover:border-emerald-600', onclick: () => {
                d.databases = [...new Set([...(d.databases || []), db])]; dbs.value = d.databases.join(', ');
              } }, db))));
        } else {
          result.className = 'text-xs text-rose-300';
          result.textContent = '✗ ' + r.error;
        }
      } catch (e) { result.className = 'text-xs text-rose-300'; result.textContent = '✗ ' + e.message; }
      test.disabled = false;
    });

    return h('div', { class: 'space-y-3 rounded-md border border-slate-800 bg-slate-950/60 p-3' },
      h('div', { class: 'flex items-center justify-between' }, h('span', { class: 'text-sm font-medium' }, 'MySQL / MariaDB dump'),
        h('button', { type: 'button', class: 'text-xs text-rose-400 hover:text-rose-300', onclick: () => { dumps.splice(i, 1); render(); } }, 'Remove')),
      h('div', { class: 'grid gap-3 sm:grid-cols-2' }, field('Name (used in the file name)', name), field('Databases', dbs)),
      where, credBox,
      h('div', { class: 'flex flex-wrap items-center gap-3' }, h('span', { class: 'text-sm text-slate-300' }, 'Keep dumps for'), keep, h('span', { class: 'text-sm text-slate-300' }, 'days'),
        h('span', { class: 'text-xs text-slate-500' }, '(0 = keep all; the newest is always kept)'), h('div', { class: 'flex-1' }), test),
      result);
  }

  function render() {
    clear(box);
    dumps.forEach((d, i) => box.append(card(d, i)));
    box.append(h('button', { type: 'button', class: 'btn btn-ghost', onclick: () => {
      dumps.push({ name: dumps.length ? '' : 'mysql', container: '', host: '', port: 0, user: 'root', password: '', use_container_env: true, databases: [], keep_days: 30 });
      render();
    } }, '+ Add MySQL / MariaDB database'));
    box.append(h('p', { class: 'text-xs text-slate-500' },
      'Each run takes a consistent dump with mysqldump --single-transaction (no table locks for InnoDB), gzips it and streams it straight to Wasabi under _databases/<name>/. Nothing is written to local disk. Back up the dump, not the raw database volume.'));
  }
  render();
  return box;
}
