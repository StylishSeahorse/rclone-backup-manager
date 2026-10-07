'use strict';
// Friendly schedule builder. The backend still stores standard cron (which the
// agent evaluates); this file converts between cron and a form people can use,
// and describes schedules in plain English. Pure functions are exported for tests.

(function (root) {
  const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0]; // Monday first in the UI
  const MINUTE_STEPS = [5, 10, 15, 20, 30];
  const HOUR_STEPS = [1, 2, 3, 4, 6, 8, 12];

  const pad = (n) => String(n).padStart(2, '0');
  const hhmm = (h, m) => `${pad(h)}:${pad(m)}`;
  const int = (s) => parseInt(s, 10);
  const inRange = (n, lo, hi) => Number.isInteger(n) && n >= lo && n <= hi;

  function defaults() {
    return { kind: 'daily', everyMinutes: 15, everyHours: 6, atMinute: 0, time: '02:00', days: [1, 2, 3, 4, 5], dom: 1, cron: '0 2 * * *' };
  }

  // Expand "1-5,0" into [0,1,2,3,4,5]; null if it is not a plain day list.
  function parseDays(field) {
    const out = new Set();
    for (const part of field.split(',')) {
      const r = part.match(/^([0-7])(?:-([0-7]))?$/);
      if (!r) return null;
      let a = int(r[1]); let b = r[2] === undefined ? a : int(r[2]);
      if (b < a) return null;
      for (let d = a; d <= b; d++) out.add(d % 7); // 7 is also Sunday
    }
    return [...out].sort((x, y) => x - y);
  }

  // fromCron: cron expression -> form state. Anything the form can't express
  // round-trips through kind "custom", so nothing is ever lost.
  function fromCron(expr) {
    const st = defaults();
    const e = (expr || '').trim().replace(/\s+/g, ' ');
    st.cron = e;
    let m;
    if ((m = e.match(/^\*\/(\d+) \* \* \* \*$/)) && MINUTE_STEPS.includes(int(m[1]))) {
      return { ...st, kind: 'minutes', everyMinutes: int(m[1]) };
    }
    if ((m = e.match(/^(\d+) \*(?:\/(\d+))? \* \* \*$/)) && inRange(int(m[1]), 0, 59) && HOUR_STEPS.includes(m[2] ? int(m[2]) : 1)) {
      return { ...st, kind: 'hours', atMinute: int(m[1]), everyHours: m[2] ? int(m[2]) : 1 };
    }
    if ((m = e.match(/^(\d+) (\d+) \* \* \*$/)) && inRange(int(m[1]), 0, 59) && inRange(int(m[2]), 0, 23)) {
      return { ...st, kind: 'daily', time: hhmm(int(m[2]), int(m[1])) };
    }
    if ((m = e.match(/^(\d+) (\d+) \* \* ([0-7,-]+)$/)) && inRange(int(m[1]), 0, 59) && inRange(int(m[2]), 0, 23)) {
      const days = parseDays(m[3]);
      if (days && days.length) {
        if (days.length === 7) return { ...st, kind: 'daily', time: hhmm(int(m[2]), int(m[1])) };
        return { ...st, kind: 'weekly', time: hhmm(int(m[2]), int(m[1])), days };
      }
    }
    if ((m = e.match(/^(\d+) (\d+) (\d+) \* \*$/)) && inRange(int(m[1]), 0, 59) && inRange(int(m[2]), 0, 23) && inRange(int(m[3]), 1, 28)) {
      return { ...st, kind: 'monthly', time: hhmm(int(m[2]), int(m[1])), dom: int(m[3]) };
    }
    return { ...st, kind: 'custom' };
  }

  // toCron: form state -> cron expression.
  function toCron(st) {
    const [h, mi] = (st.time || '00:00').split(':').map(int);
    switch (st.kind) {
      case 'minutes': return `*/${st.everyMinutes} * * * *`;
      case 'hours': return st.everyHours === 1 ? `${st.atMinute} * * * *` : `${st.atMinute} */${st.everyHours} * * *`;
      case 'daily': return `${mi} ${h} * * *`;
      case 'weekly': return `${mi} ${h} * * ${[...st.days].sort((a, b) => a - b).join(',')}`;
      case 'monthly': return `${mi} ${h} ${st.dom} * *`;
      default: return (st.cron || '').trim();
    }
  }

  const ordinal = (n) => n + (n % 10 === 1 && n !== 11 ? 'st' : n % 10 === 2 && n !== 12 ? 'nd' : n % 10 === 3 && n !== 13 ? 'rd' : 'th');

  function describeDays(days) {
    const set = [...days].sort((a, b) => a - b).join(',');
    if (set === '1,2,3,4,5') return 'every weekday (Mon–Fri)';
    if (set === '0,6') return 'every weekend (Sat & Sun)';
    const names = WEEK_ORDER.filter((d) => days.includes(d)).map((d) => DAY_NAMES[d]);
    return 'every ' + (names.length > 1 ? names.slice(0, -1).join(', ') + ' and ' + names[names.length - 1] : names[0]);
  }

  // describe: form state -> plain English.
  function describe(st) {
    switch (st.kind) {
      case 'minutes': return `Every ${st.everyMinutes} minutes`;
      case 'hours': return (st.everyHours === 1 ? 'Every hour' : `Every ${st.everyHours} hours`) + (st.atMinute ? ` at ${pad(st.atMinute)} minutes past` : ', on the hour');
      case 'daily': return `Every day at ${st.time}`;
      case 'weekly': return st.days.length ? `${describeDays(st.days).replace(/^e/, 'E')} at ${st.time}` : 'No days selected';
      case 'monthly': return `Monthly on the ${ordinal(st.dom)} at ${st.time}`;
      default: {
        const e = (st.cron || '').trim();
        const named = { '@hourly': 'Every hour', '@daily': 'Every day at 00:00', '@midnight': 'Every day at 00:00', '@weekly': 'Every Sunday at 00:00', '@monthly': 'Monthly on the 1st at 00:00' }[e];
        if (named) return named;
        const ev = e.match(/^@every (.+)$/);
        if (ev) return 'Every ' + ev[1];
        return 'Custom: ' + (e || '(empty)');
      }
    }
  }

  const describeCron = (expr) => describe(fromCron(expr));

  function browserTimezone() {
    try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'; } catch { return 'UTC'; }
  }
  function timezones() {
    let list = [];
    try { list = Intl.supportedValuesOf('timeZone'); } catch { /* old browser */ }
    const b = browserTimezone();
    return ['UTC', ...new Set([b, ...list].filter((z) => z !== 'UTC'))];
  }

  // editor renders one schedule row. `sched` ({cron_expr, timezone, enabled})
  // is updated in place; `h` is the app's element helper, `api` its fetch helper.
  function editor(sched, { h, api, onRemove }) {
    const st = fromCron(sched.cron_expr);
    const box = h('div', { class: 'space-y-3 rounded-md border border-slate-800 bg-slate-950/60 p-3' });
    const summary = h('p', { class: 'text-sm font-medium text-emerald-300' });
    const preview = h('p', { class: 'text-xs text-slate-400' });
    const controls = h('div', { class: 'flex flex-wrap items-center gap-2' });
    let timer = null;

    const sel = (value, options, onchange, cls = 'w-auto') => h('select', { class: 'input ' + cls, onchange: (e) => { onchange(e.target.value); update(); } },
      options.map(([v, label]) => h('option', { value: v, selected: String(v) === String(value) }, label)));
    const timeIn = () => h('input', { type: 'time', class: 'input w-32', value: st.time, required: true, onchange: (e) => { st.time = e.target.value || '00:00'; update(); } });
    const text = (t) => h('span', { class: 'text-sm text-slate-400' }, t);

    function renderControls() {
      while (controls.firstChild) controls.removeChild(controls.firstChild);
      controls.append(sel(st.kind, [['minutes', 'Every few minutes'], ['hours', 'Hourly'], ['daily', 'Daily'], ['weekly', 'Weekly'], ['monthly', 'Monthly'], ['custom', 'Custom (cron)']],
        (v) => { if (v === 'custom') st.cron = toCron(st); st.kind = v; renderControls(); }, 'w-44'));
      switch (st.kind) {
        case 'minutes':
          controls.append(text('every'), sel(st.everyMinutes, MINUTE_STEPS.map((n) => [n, `${n} minutes`]), (v) => { st.everyMinutes = int(v); }));
          break;
        case 'hours':
          controls.append(text('every'), sel(st.everyHours, HOUR_STEPS.map((n) => [n, n === 1 ? 'hour' : `${n} hours`]), (v) => { st.everyHours = int(v); }),
            text('at minute'), sel(st.atMinute, Array.from({ length: 12 }, (_, i) => [i * 5, ':' + pad(i * 5)]).concat(
              MINUTE_STEPS.includes(st.atMinute) || st.atMinute % 5 === 0 ? [] : [[st.atMinute, ':' + pad(st.atMinute)]]), (v) => { st.atMinute = int(v); }));
          break;
        case 'daily':
          controls.append(text('at'), timeIn());
          break;
        case 'weekly': {
          const dayBtns = h('div', { class: 'flex gap-1' }, WEEK_ORDER.map((d) => {
            const on = st.days.includes(d);
            return h('button', {
              type: 'button', 'aria-pressed': on ? 'true' : 'false', title: DAY_NAMES[d],
              class: 'h-8 w-11 rounded-md text-xs font-medium ' + (on ? 'bg-emerald-600 text-white' : 'bg-slate-800 text-slate-400 hover:bg-slate-700'),
              onclick: () => { st.days = on ? st.days.filter((x) => x !== d) : [...st.days, d]; renderControls(); update(); },
            }, DAYS[d]);
          }));
          const quick = (label, days) => h('button', { type: 'button', class: 'text-xs text-sky-400 hover:underline', onclick: () => { st.days = days; renderControls(); update(); } }, label);
          controls.append(dayBtns, text('at'), timeIn(), quick('Weekdays', [1, 2, 3, 4, 5]), quick('Weekends', [0, 6]));
          break;
        }
        case 'monthly':
          controls.append(text('on day'), sel(st.dom, Array.from({ length: 28 }, (_, i) => [i + 1, ordinal(i + 1)]), (v) => { st.dom = int(v); }), text('at'), timeIn());
          break;
        default:
          controls.append(h('input', { class: 'input w-56 font-mono', value: st.cron, placeholder: 'min hour day month weekday', oninput: (e) => { st.cron = e.target.value; update(); } }),
            h('span', { class: 'text-xs text-slate-500' }, 'e.g. "30 1 * * 1-5" = 01:30 on weekdays, "@every 90m"'));
      }
    }

    const tz = sel(sched.timezone || browserTimezone(), timezones().map((z) => [z, z.replace(/_/g, ' ')]), (v) => { sched.timezone = v; }, 'w-56');
    const enabled = h('input', { type: 'checkbox', class: 'accent-emerald-500', checked: sched.enabled !== false, onchange: (e) => { sched.enabled = e.target.checked; update(); } });

    function update() {
      sched.cron_expr = toCron(st);
      sched.timezone = tz.value;
      summary.textContent = describe(st) + (sched.enabled === false ? ' (paused)' : '');
      preview.textContent = 'Checking…';
      preview.className = 'text-xs text-slate-400';
      clearTimeout(timer);
      timer = setTimeout(async () => {
        try {
          const r = await api('POST', '/api/schedules/preview', { cron_expr: sched.cron_expr, timezone: sched.timezone });
          const fmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: sched.timezone });
          preview.textContent = r.next.length ? 'Next runs: ' + r.next.slice(0, 3).map((t) => fmt.format(new Date(t * 1000))).join(' · ') + ` (${sched.timezone})` : 'Never runs';
        } catch (e) {
          preview.textContent = e.message;
          preview.className = 'text-xs text-rose-400';
        }
      }, 300);
    }

    box.append(
      h('div', { class: 'flex items-start justify-between gap-2' }, summary,
        h('div', { class: 'flex items-center gap-3' },
          h('label', { class: 'flex items-center gap-1 text-xs text-slate-400' }, enabled, 'Active'),
          h('button', { type: 'button', class: 'text-xs text-rose-400 hover:text-rose-300', onclick: onRemove }, 'Remove'))),
      controls,
      h('div', { class: 'flex flex-wrap items-center gap-2' }, h('span', { class: 'text-xs text-slate-500' }, 'Time zone'), tz),
      preview);
    renderControls();
    update();
    return box;
  }

  const api = { fromCron, toCron, describe, describeCron, editor, browserTimezone };
  if (typeof module !== 'undefined') module.exports = api; else root.Sched = api;
})(typeof window !== 'undefined' ? window : globalThis);
