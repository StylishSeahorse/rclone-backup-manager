'use strict';
// Overview dashboard: backup health at a glance. Uses the helpers defined in
// app.js (h, api, clear, layout, fmtBytes, ago, ...), which are resolved when
// viewOverview runs, so this file can load before app.js.
//
// Colour roles (validated for CVD and contrast on the card surface #0f172b):
//   successful = recessive slate (context), failed = critical red (the point),
//   cancelled = warning amber, data = sequential blue.
const OV = {
  success: '#62748e', failed: '#d03b3b', cancelled: '#fab219', data: '#3987e5',
  good: '#0ca30c', grid: '#1e293b', axis: '#334155', muted: '#94a3b8', surface: '#0f172b',
};
const RANGES = [[1, '24 hours'], [7, '7 days'], [30, '30 days'], [90, '90 days']];
const ovState = { days: 7, agent: '', timer: null, resize: null };

const svgNS = 'http://www.w3.org/2000/svg';
function s(tag, attrs, ...kids) {
  const el = document.createElementNS(svgNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) if (v != null) el.setAttribute(k, v);
  for (const k of kids) if (k != null) el.append(k.nodeType ? k : document.createTextNode(String(k)));
  return el;
}
const compact = (n) => new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n);
const pct = (r) => (r < 0 ? '–' : (Math.round(r * 1000) / 10) + '%');
function fmtDur(sec) {
  if (!sec) return '–';
  sec = Math.round(sec);
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm ' + (sec % 60) + 's';
  return Math.floor(sec / 3600) + 'h ' + Math.floor((sec % 3600) / 60) + 'm';
}

// Status is never colour alone: icon + label, icon in the status colour.
const HEALTH = {
  ok: ['✓', OV.good, 'Healthy'], failing: ['✗', OV.failed, 'Failing'], overdue: ['!', OV.cancelled, 'Overdue'],
  offline: ['!', OV.cancelled, 'Agent offline'],
  running: ['●', OV.data, 'Running'], never: ['○', OV.muted, 'Not run yet'], paused: ['❚❚', OV.muted, 'Paused'],
};
function healthTag(key) {
  const [icon, color, label] = HEALTH[key] || HEALTH.never;
  const i = h('span', { class: 'inline-block w-4 text-center font-bold' }, icon);
  i.style.color = color;
  return h('span', { class: 'inline-flex items-center gap-1 whitespace-nowrap text-sm text-slate-200' }, i, label);
}
const RUN_COLOR = { success: OV.success, failed: OV.failed, cancelled: OV.cancelled, running: OV.data };

// ---- nice axis ------------------------------------------------------------------------
function niceStep(max, ticks = 4) {
  const raw = max / ticks;
  const mag = Math.pow(10, Math.floor(Math.log10(raw || 1)));
  const f = raw / mag;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * mag;
}

// columnChart draws stacked columns with a tooltip per column and returns a
// card body that can flip to a table view.
function columnChart({ data, series, label, fmt, xFmt, tipTitle }) {
  const wrap = h('div', { class: 'relative' });
  const tip = h('div', { class: 'pointer-events-none absolute z-10 hidden min-w-36 rounded-md border border-slate-700 bg-slate-950/95 px-3 py-2 text-xs shadow-lg', role: 'tooltip' });
  const legend = series.length > 1 ? h('div', { class: 'mb-2 flex flex-wrap gap-4 text-xs text-slate-300' }, series.map((se) => {
    const sw = h('span', { class: 'inline-block h-2.5 w-2.5 rounded-sm' }); sw.style.background = se.color;
    return h('span', { class: 'inline-flex items-center gap-1.5' }, sw, se.label);
  })) : null;

  function draw() {
    const W = Math.max(280, wrap.clientWidth || 600), H = 200;
    const m = { l: 60, r: 8, t: 10, b: 24 }; // room for tick labels like "1.5 GB"
    const pw = W - m.l - m.r, ph = H - m.t - m.b;
    const totals = data.map((d) => series.reduce((a, se) => a + (d[se.key] || 0), 0));
    const max = Math.max(...totals, 0);
    const step = niceStep(max || 1);
    const top = Math.max(step, Math.ceil(max / step) * step);
    const y = (v) => m.t + ph - (v / top) * ph;
    const svg = s('svg', { width: W, height: H, viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': label, class: 'block' });

    for (let v = 0; v <= top + 1e-9; v += step) { // hairline grid + clean ticks
      svg.append(s('line', { x1: m.l, x2: W - m.r, y1: y(v), y2: y(v), stroke: v === 0 ? OV.axis : OV.grid, 'stroke-width': 1, 'shape-rendering': 'crispEdges' }));
      svg.append(s('text', { x: m.l - 6, y: y(v) + 4, 'text-anchor': 'end', 'font-size': 11, fill: OV.muted, class: 'tabular-nums' }, fmt(v, true)));
    }
    const slot = pw / data.length;
    const cw = Math.max(2, Math.min(24, slot * 0.7));
    const every = Math.ceil(data.length / Math.max(1, Math.floor(pw / 64)));
    data.forEach((d, i) => {
      const x = m.l + i * slot + (slot - cw) / 2;
      const g = s('g', {});
      const hover = s('rect', { x: m.l + i * slot, y: m.t, width: slot, height: ph, fill: '#1e293b', opacity: 0 });
      g.append(hover);
      let base = 0;
      const visible = series.filter((se) => d[se.key] > 0);
      visible.forEach((se, k) => {
        const v = d[se.key];
        let y0 = y(base), y1 = y(base + v);
        if (y0 - y1 < 2) y1 = y0 - 2; // a non-zero value is always visible
        const isTop = k === visible.length - 1;
        const gapTop = isTop ? 0 : 2; // 2px surface gap between stacked segments
        const hgt = Math.max(1, y0 - y1 - gapTop);
        const yt = y0 - hgt;
        const r = isTop ? Math.min(4, hgt, cw / 2) : 0; // rounded data-end, square at the baseline
        g.append(s('path', { fill: se.color, d: `M${x},${y0}V${yt + r}Q${x},${yt} ${x + r},${yt}H${x + cw - r}Q${x + cw},${yt} ${x + cw},${yt + r}V${y0}Z` }));
        base += v;
      });
      if (i % every === 0 || i === data.length - 1 && data.length < 10) {
        svg.append(s('text', { x: m.l + i * slot + slot / 2, y: H - 6, 'text-anchor': 'middle', 'font-size': 11, fill: OV.muted }, xFmt(d.start)));
      }
      // Hit target = the whole slot (bigger than the mark), hover and keyboard focus alike.
      const hit = s('rect', { x: m.l + i * slot, y: m.t, width: slot, height: ph, fill: 'transparent', tabindex: 0,
        'aria-label': `${tipTitle(d.start)}: ` + series.map((se) => `${se.label} ${fmt(d[se.key] || 0)}`).join(', ') });
      const show = () => {
        hover.setAttribute('opacity', 0.6);
        clear(tip).append(h('div', { class: 'mb-1 text-slate-400' }, tipTitle(d.start)),
          ...series.map((se) => {
            const key = h('span', { class: 'inline-block h-0.5 w-3 rounded' }); key.style.background = se.color;
            return h('div', { class: 'flex items-center gap-2' }, key, h('span', { class: 'font-semibold text-slate-100' }, fmt(d[se.key] || 0)), h('span', { class: 'text-slate-400' }, se.label));
          }));
        tip.classList.remove('hidden');
        const left = Math.min(Math.max(0, m.l + i * slot + slot / 2 - tip.offsetWidth / 2), W - tip.offsetWidth);
        tip.style.left = left + 'px'; tip.style.top = '0px';
      };
      const hide = () => { hover.setAttribute('opacity', 0); tip.classList.add('hidden'); };
      hit.addEventListener('pointerenter', show); hit.addEventListener('pointerleave', hide);
      hit.addEventListener('focus', show); hit.addEventListener('blur', hide);
      g.append(hit);
      svg.append(g);
    });
    clear(wrap).append(svg, tip);
  }

  const table = h('table', { class: 'w-full text-sm' },
    h('thead', {}, h('tr', {}, h('th', { class: 'th' }, 'Period'), series.map((se) => h('th', { class: 'th text-right' }, se.label)))),
    h('tbody', {}, [...data].reverse().map((d) => h('tr', { class: 'border-t border-slate-800' },
      h('td', { class: 'td text-slate-300' }, tipTitle(d.start)),
      series.map((se) => h('td', { class: 'td text-right tabular-nums text-slate-200' }, fmt(d[se.key] || 0)))))));
  const tableBox = h('div', { class: 'hidden max-h-56 overflow-auto' }, table);
  const toggle = h('button', { class: 'text-xs text-sky-400 hover:underline', type: 'button' }, 'View as table');
  toggle.addEventListener('click', () => {
    const showTable = tableBox.classList.toggle('hidden') === false;
    wrap.classList.toggle('hidden', showTable);
    if (legend) legend.classList.toggle('hidden', showTable);
    toggle.textContent = showTable ? 'View as chart' : 'View as table';
  });
  return { el: h('div', {}, legend, wrap, tableBox), toggle, draw };
}

// ---- stat tiles -----------------------------------------------------------------------------
function tile(label, value, sub, extra) {
  return h('div', { class: 'card' },
    h('div', { class: 'text-xs font-medium text-slate-400' }, label),
    h('div', { class: 'mt-1 text-2xl font-semibold text-slate-100' }, value),
    sub ? h('div', { class: 'mt-1 text-xs text-slate-400' }, sub) : null, extra);
}
function delta(cur, prev, { unit = '', goodUp = true, fmt = (x) => x, label }) {
  if (prev == null || cur == null) return null;
  const d = cur - prev;
  if (Math.abs(d) < 1e-9) return h('span', { class: 'text-slate-400' }, `no change vs previous ${label}`);
  const good = (d > 0) === goodUp;
  const el = h('span', {}, `${d > 0 ? '▲ +' : '▼ −'}${fmt(Math.abs(d))}${unit}`);
  el.style.color = good ? OV.good : '#f87171';
  return h('span', {}, el, h('span', { class: 'text-slate-400' }, ` vs previous ${label}`));
}

async function viewOverview() {
  clearInterval(ovState.timer);
  const agents = await api('GET', '/api/agents');
  const body = h('div', { class: 'space-y-6 transition-opacity' });
  const updated = h('span', { class: 'text-xs text-slate-500' });

  // Filters: one row above everything they scope.
  const rangeBtns = h('div', { class: 'inline-flex rounded-md bg-slate-900 p-0.5' });
  function renderRange() {
    clear(rangeBtns).append(...RANGES.map(([d, l]) => h('button', {
      type: 'button', 'aria-pressed': ovState.days === d ? 'true' : 'false',
      class: 'rounded px-3 py-1 text-sm ' + (ovState.days === d ? 'bg-slate-700 font-medium text-white' : 'text-slate-400 hover:text-white'),
      onclick: () => { ovState.days = d; renderRange(); load(); },
    }, l)));
  }
  renderRange();
  const agentSel = h('select', { class: 'input w-48', onchange: (e) => { ovState.agent = e.target.value; load(); } },
    h('option', { value: '' }, 'All agents'), agents.map((a) => h('option', { value: a.id, selected: ovState.agent === a.id }, a.name)));

  layout('overview',
    h('div', { class: 'mb-5 flex flex-wrap items-center gap-3' },
      h('h2', { class: 'mr-2 text-lg font-semibold' }, 'Overview'), rangeBtns, agentSel, h('div', { class: 'flex-1' }), updated),
    body);

  let charts = [];
  async function load() {
    body.classList.add('opacity-60'); // keep the previous frame while refetching
    try {
      const tz = Sched.browserTimezone();
      const o = await api('GET', `/api/overview?days=${ovState.days}&tz=${encodeURIComponent(tz)}${ovState.agent ? '&agent_id=' + ovState.agent : ''}`);
      render(o);
      updated.textContent = 'Updated ' + new Date().toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) + ' · refreshes every 30 s';
    } catch (e) { toast(e.message, true); }
    body.classList.remove('opacity-60');
  }

  function render(o) {
    const t = o.totals, p = o.previous;
    const rangeLabel = RANGES.find(([d]) => d === ovState.days)[1];
    const finished = t.success + t.failed + t.cancelled;

    // Hero figure: the one number this page leads with.
    const heroColor = t.success_rate < 0 ? OV.muted : t.success_rate >= 0.95 ? OV.good : t.success_rate >= 0.8 ? OV.cancelled : OV.failed;
    const heroIcon = h('span', { class: 'mr-2 text-3xl' }, t.success_rate < 0 ? '○' : t.success_rate >= 0.95 ? '✓' : '!'); heroIcon.style.color = heroColor;
    const hero = h('div', { class: 'card flex flex-col justify-between' },
      h('div', { class: 'text-xs font-medium text-slate-400' }, `Backup success rate · last ${rangeLabel}`),
      h('div', { class: 'mt-2 flex items-center' }, heroIcon, h('span', { class: 'text-5xl font-semibold text-slate-50' }, pct(t.success_rate))),
      h('div', { class: 'mt-2 text-xs text-slate-400' }, finished ? `${t.success} of ${finished} finished backups succeeded` : 'No backups finished in this period'),
      h('div', { class: 'mt-1 text-xs' }, t.success_rate >= 0 && p.success_rate >= 0 ? delta(t.success_rate * 100, p.success_rate * 100, { unit: ' pts', fmt: (x) => x.toFixed(1), label: rangeLabel }) : null));

    const failIcon = h('span', { class: 'mr-1' }, t.failed ? '✗' : '✓'); failIcon.style.color = t.failed ? OV.failed : OV.good;
    const onlineIcon = h('span', { class: 'mr-1' }, o.agents.online === o.agents.total ? '✓' : '!');
    onlineIcon.style.color = o.agents.online === o.agents.total ? OV.good : OV.cancelled;
    const tiles = h('div', { class: 'grid grid-cols-2 gap-4 lg:grid-cols-3' },
      tile('Successful backups', compact(t.success), null, h('div', { class: 'mt-1 text-xs' }, delta(t.success, p.success, { label: rangeLabel, fmt: compact }))),
      tile('Failed backups', h('span', {}, failIcon, compact(t.failed)), t.cancelled ? `${t.cancelled} cancelled` : null,
        h('div', { class: 'mt-1 text-xs' }, delta(t.failed, p.failed, { goodUp: false, label: rangeLabel, fmt: compact }))),
      tile('Data uploaded', fmtBytes(t.bytes), null, h('div', { class: 'mt-1 text-xs' }, delta(t.bytes, p.bytes, { label: rangeLabel, fmt: fmtBytes }))),
      tile('Files changed', compact(t.files_transferred), `uploaded · ${compact(t.files_versioned)} versioned · ${compact(t.files_deleted)} deleted`),
      tile('Agents online', h('span', {}, onlineIcon, `${o.agents.online} of ${o.agents.total}`), o.running ? `${o.running} backup(s) running now` : 'nothing running now'),
      tile('Average backup time', fmtDur(t.avg_duration_sec), `${compact(t.runs)} backup run(s)`));

    // Needs attention.
    const sevIcon = { critical: ['✗', OV.failed], warning: ['!', OV.cancelled] };
    const attention = h('div', { class: 'card' },
      h('h3', { class: 'mb-2 font-semibold' }, 'Needs attention'),
      o.attention.length ? h('ul', { class: 'space-y-1.5' }, o.attention.map((a) => {
        const ic = h('span', { class: 'w-4 shrink-0 text-center font-bold' }, sevIcon[a.severity][0]); ic.style.color = sevIcon[a.severity][1];
        const link = a.run_id ? '#/runs/' + a.run_id : a.agent_id ? '#/agents/' + a.agent_id : a.job_id ? '#/agents/' + (o.jobs.find((j) => j.id === a.job_id) || {}).agent_id : null;
        return h('li', { class: 'flex gap-2 text-sm' }, ic,
          h('span', { class: 'text-xs uppercase tracking-wide text-slate-500 w-16 shrink-0 pt-0.5' }, a.severity === 'critical' ? 'Critical' : 'Warning'),
          link ? h('a', { href: link, class: 'text-slate-200 hover:underline' }, a.text) : h('span', { class: 'text-slate-200' }, a.text));
      })) : h('p', { class: 'flex items-center gap-2 text-sm text-slate-300' }, (() => { const i = h('span', { class: 'font-bold' }, '✓'); i.style.color = OV.good; return i; })(),
        'Nothing needs attention: every job is healthy and every agent is online.'));

    // Charts.
    const isHourly = o.range.bucket_sec === 3600;
    const xFmt = (ts) => new Date(ts * 1000).toLocaleString(undefined, isHourly ? { hour: '2-digit' } : ovState.days > 7 ? { day: 'numeric', month: 'short' } : { weekday: 'short' });
    const tipTitle = (ts) => new Date(ts * 1000).toLocaleString(undefined, isHourly ? { weekday: 'short', hour: '2-digit', minute: '2-digit' } : { weekday: 'short', day: 'numeric', month: 'short' });
    const runsChart = columnChart({
      data: o.series, label: 'Backups per ' + (isHourly ? 'hour' : 'day') + ' by outcome', xFmt, tipTitle,
      series: [{ key: 'success', label: 'Successful', color: OV.success }, { key: 'failed', label: 'Failed', color: OV.failed }, { key: 'cancelled', label: 'Cancelled', color: OV.cancelled }],
      fmt: (v) => compact(v),
    });
    const dataChart = columnChart({
      data: o.series, label: 'Data uploaded per ' + (isHourly ? 'hour' : 'day'), xFmt, tipTitle,
      series: [{ key: 'bytes', label: 'Uploaded', color: OV.data }],
      fmt: (v, axis) => (axis && v === 0 ? '0' : fmtBytes(v)),
    });
    charts = [runsChart, dataChart];
    const chartCard = (title, c) => h('div', { class: 'card' },
      h('div', { class: 'mb-2 flex items-baseline justify-between' }, h('h3', { class: 'font-semibold' }, title), c.toggle), c.el);

    // Job health table.
    const strip = (runs) => h('div', { class: 'flex items-end gap-0.5' }, runs.length ? runs.map((r) => {
      const a = h('a', { href: '#/runs/' + r.id, class: 'block h-4 w-1.5 rounded-sm hover:opacity-70', title: `${fmtTime(r.started_at)} · ${r.status}`, 'aria-label': `${fmtTime(r.started_at)}: ${r.status}` });
      a.style.background = RUN_COLOR[r.status] || OV.muted;
      return a;
    }) : h('span', { class: 'text-xs text-slate-600' }, 'no runs'));
    const order = { failing: 0, overdue: 1, offline: 2, running: 3, never: 4, ok: 5, paused: 6 };
    const jobs = [...o.jobs].sort((a, b) => order[a.health] - order[b.health] || a.name.localeCompare(b.name));
    const jobTable = h('div', { class: 'card overflow-x-auto p-0' },
      h('div', { class: 'flex items-baseline justify-between px-4 pt-4' }, h('h3', { class: 'font-semibold' }, 'Backup jobs'),
        h('span', { class: 'text-xs text-slate-500' }, 'Recent runs: oldest → newest. Gray = success, red = failed, amber = cancelled.')),
      jobs.length ? h('table', { class: 'mt-2 w-full' },
        h('thead', {}, h('tr', {}, ['Health', 'Job', 'Last backup', 'Last success', 'Next run', `Success (${rangeLabel})`, 'Data', 'Recent runs'].map((x) => h('th', { class: 'th whitespace-nowrap' }, x)))),
        h('tbody', {}, jobs.map((j) => h('tr', { class: 'border-t border-slate-800 align-top' },
          h('td', { class: 'td' }, healthTag(j.health), j.reason ? h('div', { class: 'mt-0.5 max-w-56 text-xs text-slate-500' }, j.reason) : null),
          h('td', { class: 'td' }, h('a', { href: '#/agents/' + j.agent_id, class: 'font-medium text-sky-400 hover:underline' }, j.name),
            h('div', { class: 'text-xs text-slate-500' }, `${j.agent_name}${j.agent_online ? '' : ' (offline)'} · ${j.backup_type === 'sync' ? 'Mirror sync' : 'Incremental'}`)),
          h('td', { class: 'td whitespace-nowrap text-slate-300' }, j.last_run ? h('a', { href: '#/runs/' + j.last_run.id, class: 'hover:underline' }, ago(j.last_run.started_at)) : '–',
            j.last_run ? h('div', { class: 'text-xs text-slate-500' }, `${j.last_run.status} · ${fmtDur(j.last_run.finished_at ? j.last_run.finished_at - j.last_run.started_at : 0)}`) : null),
          h('td', { class: 'td whitespace-nowrap text-slate-300' }, j.last_success_at ? ago(j.last_success_at) : 'never'),
          h('td', { class: 'td whitespace-nowrap text-slate-300' }, j.enabled && j.next_run ? until(j.next_run) : '–',
            j.schedule ? h('div', { class: 'text-xs text-slate-500' }, Sched.describeCron(j.schedule)) : h('div', { class: 'text-xs text-slate-500' }, 'manual only')),
          h('td', { class: 'td whitespace-nowrap tabular-nums text-slate-300' }, j.runs ? `${j.success} / ${j.runs}` : '–',
            j.failed ? h('div', { class: 'text-xs text-slate-500' }, `${j.failed} failed`) : null),
          h('td', { class: 'td whitespace-nowrap tabular-nums text-slate-300' }, j.bytes ? fmtBytes(j.bytes) : '–'),
          h('td', { class: 'td' }, strip(j.recent)))))) : h('p', { class: 'p-4 text-sm text-slate-400' }, 'No backup jobs yet. Open an agent to create one.'));

    const failures = h('div', { class: 'card' },
      h('h3', { class: 'mb-2 font-semibold' }, 'Recent failures'),
      o.recent_failures.length ? h('ul', { class: 'divide-y divide-slate-800' }, o.recent_failures.map((r) => h('li', { class: 'flex flex-wrap items-baseline gap-x-3 py-2 text-sm' },
        h('a', { href: '#/runs/' + r.id, class: 'font-medium text-sky-400 hover:underline' }, r.job_name),
        h('span', { class: 'text-xs text-slate-500' }, `${r.agent_name} · ${triggerLabel(r.trigger)} · ${fmtTime(r.started_at)}`),
        h('span', { class: 'w-full text-xs text-slate-400' }, r.summary || 'failed')))) : h('p', { class: 'text-sm text-slate-400' }, `No failures in the last ${rangeLabel}.`));

    clear(body).append(
      h('div', { class: 'grid gap-4 lg:grid-cols-3' }, hero, h('div', { class: 'lg:col-span-2' }, tiles)),
      attention,
      h('div', { class: 'grid gap-4 lg:grid-cols-2' }, chartCard(`Backups per ${isHourly ? 'hour' : 'day'}`, runsChart), chartCard(`Data uploaded per ${isHourly ? 'hour' : 'day'}`, dataChart)),
      jobTable, failures);
    charts.forEach((c) => c.draw()); // after mount, so they can measure their width
  }

  await load();
  ovState.timer = setInterval(() => { if (location.hash === '' || location.hash.startsWith('#/overview')) load(); else clearInterval(ovState.timer); }, 30000);
  window.removeEventListener('resize', ovState.resize);
  ovState.resize = () => { clearTimeout(ovState.rt); ovState.rt = setTimeout(() => charts.forEach((c) => c.draw()), 150); };
  window.addEventListener('resize', ovState.resize);
}
