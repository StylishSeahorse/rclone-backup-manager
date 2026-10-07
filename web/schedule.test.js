// Run: node web/schedule.test.js  (part of `make test` when Node is installed)
'use strict';
const assert = require('node:assert/strict');
const S = require('./static/schedule.js');

const cases = [
  ['*/15 * * * *', 'minutes', 'Every 15 minutes'],
  ['0 * * * *', 'hours', 'Every hour, on the hour'],
  ['15 */6 * * *', 'hours', 'Every 6 hours at 15 minutes past'],
  ['0 2 * * *', 'daily', 'Every day at 02:00'],
  ['30 18 * * 1,3,5', 'weekly', 'Every Monday, Wednesday and Friday at 18:30'],
  ['30 1 * * 1-5', 'weekly', 'Every weekday (Mon–Fri) at 01:30'],
  ['0 9 * * 0,6', 'weekly', 'Every weekend (Sat & Sun) at 09:00'],
  ['0 3 * * 0', 'weekly', 'Every Sunday at 03:00'],
  ['0 4 * * 0-6', 'daily', 'Every day at 04:00'],
  ['0 3 1 * *', 'monthly', 'Monthly on the 1st at 03:00'],
  ['0 3 22 * *', 'monthly', 'Monthly on the 22nd at 03:00'],
  // Things the builder can't express stay as custom, untouched.
  ['0 3 31 * *', 'custom', 'Custom: 0 3 31 * *'],
  ['*/7 * * * *', 'custom', 'Custom: */7 * * * *'],
  ['0 2 1 1 *', 'custom', 'Custom: 0 2 1 1 *'],
  ['@every 6h', 'custom', 'Every 6h'],
  ['@daily', 'custom', 'Every day at 00:00'],
];
for (const [cron, kind, text] of cases) {
  const st = S.fromCron(cron);
  assert.equal(st.kind, kind, cron);
  assert.equal(S.describe(st), text, cron);
  if (kind === 'custom') assert.equal(S.toCron(st), cron, 'custom must round-trip exactly: ' + cron);
  else assert.equal(S.fromCron(S.toCron(st)).kind, kind, 'builder output must parse back: ' + cron);
}
const st = S.fromCron('0 2 * * *');
Object.assign(st, { kind: 'weekly', days: [5, 1], time: '07:45' });
assert.equal(S.toCron(st), '45 7 * * 1,5');
Object.assign(st, { kind: 'hours', everyHours: 1, atMinute: 30 });
assert.equal(S.toCron(st), '30 * * * *');
console.log(`schedule.js: ${cases.length + 2} cases pass`);
