#!/usr/bin/env node
// [115] 会话列表接口耗时测试 + 新旧响应等价对比（只连本机隔离测试后端 127.0.0.1）。
// 用法（Mac 测试环境，先 set -a; source secrets/test.env; set +a）：
//   node list_perf.mjs run <label>            → 各场景顺序 20 次（预热 2 次）取中位数/P95，3、5 并发各 20 轮；
//                                              结果与完整响应写 ~/cs-testenv/results/list_perf_<label>.json
//   node list_perf.mjs compare <oldLabel> <newLabel> → 逐场景比较 data/total；不一致时区分「排序键并列」与真实差异
// 输出只含耗时、数量、位置序号，不打印访客数据与凭证。
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';

const BASE = process.env.CS_BASE || 'http://127.0.0.1:18080';
if (!/^http:\/\/127\.0\.0\.1:/.test(BASE)) { console.error('只允许连本机测试后端'); process.exit(2); }
const RESULTS = (process.env.HOME || '.') + '/cs-testenv/results';
const RUNS = 20, WARMUP = 2, ROUNDS = 20;
const SCENARIOS = [
  { name: 'all_first_page', q: 'mode=all&limit=50' },
  { name: 'all_offset_200', q: 'mode=all&limit=50&offset=200' },
  { name: 'contacted_first_page', q: 'mode=contacted&limit=50' },
  { name: 'contacted_offset_200', q: 'mode=contacted&limit=50&offset=200' },
  { name: 'tag_filter_bulk', q: 'mode=all&limit=50&tag=' + encodeURIComponent('压测') },
  { name: 'tag_filter_vip_contacted', q: 'mode=contacted&limit=50&tag=VIP' },
  { name: 'all_limit_200', q: 'mode=all&limit=200' },
];

const pct = (arr, p) => { const s = [...arr].sort((a, b) => a - b); return s[Math.min(s.length - 1, Math.ceil(p / 100 * s.length) - 1)]; };
const r1 = (x) => Math.round(x * 10) / 10;
const summary = (arr) => ({ n: arr.length, median_ms: r1(pct(arr, 50)), p95_ms: r1(pct(arr, 95)), min_ms: r1(Math.min(...arr)), max_ms: r1(Math.max(...arr)) });

async function login() {
  const r = await fetch(BASE + '/api/agent/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: process.env.QA_ADMIN_USER || 'qa_admin', password: process.env.ADMIN_BOOTSTRAP_PASSWORD }) });
  const j = await r.json();
  if (!j.token) { console.error('login failed status=' + r.status); process.exit(1); }
  return j.token;
}

async function timedGet(token, q) {
  const t0 = performance.now();
  const r = await fetch(BASE + '/api/agent/conversations?' + q, { headers: { Authorization: 'Bearer ' + token } });
  const text = await r.text();
  const ms = performance.now() - t0;
  if (r.status !== 200) throw new Error('HTTP ' + r.status + ' for ' + q);
  return { ms, body: JSON.parse(text), bytes: text.length };
}

async function run(label) {
  const token = await login();
  const out = { label, base: BASE, at: new Date(Date.now() + 8 * 3600e3).toISOString().replace('Z', '+08:00'), scenarios: {}, concurrency: {}, responses: {} };
  for (const s of SCENARIOS) {
    for (let i = 0; i < WARMUP; i++) await timedGet(token, s.q);
    const times = [];
    let last;
    for (let i = 0; i < RUNS; i++) { last = await timedGet(token, s.q); times.push(last.ms); }
    out.scenarios[s.name] = { query: s.q, ...summary(times), rows: last.body.data.length, total: last.body.total, bytes: last.bytes,
      sha256: createHash('sha256').update(JSON.stringify(last.body.data)).digest('hex').slice(0, 16) };
    out.responses[s.name] = { total: last.body.total, data: last.body.data };
    console.log(label + ' ' + s.name + ' ' + JSON.stringify(out.scenarios[s.name]));
  }
  for (const c of [3, 5]) {
    for (const sname of ['all_first_page', 'contacted_first_page']) {
      const s = SCENARIOS.find((x) => x.name === sname);
      const times = [], walls = [];
      for (let round = 0; round < ROUNDS; round++) {
        const w0 = performance.now();
        const rs = await Promise.all(Array.from({ length: c }, () => timedGet(token, s.q)));
        walls.push(performance.now() - w0);
        for (const r of rs) times.push(r.ms);
      }
      out.concurrency['c' + c + '_' + sname] = { concurrency: c, rounds: ROUNDS, per_request: summary(times), per_round_wall: summary(walls) };
      console.log(label + ' concurrency=' + c + ' ' + sname + ' ' + JSON.stringify(out.concurrency['c' + c + '_' + sname]));
    }
  }
  mkdirSync(RESULTS, { recursive: true });
  writeFileSync(RESULTS + '/list_perf_' + label + '.json', JSON.stringify(out));
  console.log('saved ' + RESULTS + '/list_perf_' + label + '.json');
}

function compare(a, b) {
  const A = JSON.parse(readFileSync(RESULTS + '/list_perf_' + a + '.json', 'utf8'));
  const B = JSON.parse(readFileSync(RESULTS + '/list_perf_' + b + '.json', 'utf8'));
  let allOk = true;
  for (const s of SCENARIOS) {
    const x = A.responses[s.name], y = B.responses[s.name];
    if (!x || !y) { console.log(s.name + ' missing'); allOk = false; continue; }
    const key = (r) => (r.pinned ? '1' : '0') + '|' + r.updated_at;
    const diffPos = [], tieOnly = [], fieldDiff = [];
    const n = Math.max(x.data.length, y.data.length);
    for (let i = 0; i < n; i++) {
      const p = x.data[i], q = y.data[i];
      if (!p || !q) { diffPos.push(i + 1); continue; }
      if (p.visitor_id !== q.visitor_id) {
        diffPos.push(i + 1);
        if (key(p) === key(q)) tieOnly.push(i + 1);
      } else if (JSON.stringify(p) !== JSON.stringify(q)) {
        fieldDiff.push(i + 1);
      }
    }
    // 同一位访客在两边的整行是否一致（排除并列换位的影响）
    const byId = new Map(y.data.map((r) => [r.visitor_id, JSON.stringify(r)]));
    const sameRowsById = x.data.filter((r) => byId.get(r.visitor_id) === JSON.stringify(r)).length;
    const setEqual = x.data.length === y.data.length && x.data.every((r) => byId.has(r.visitor_id));
    const ok = x.total === y.total && fieldDiff.length === 0 && diffPos.length === tieOnly.length;
    if (!ok) allOk = false;
    console.log(JSON.stringify({ scenario: s.name, total_old: x.total, total_new: y.total, rows_old: x.data.length, rows_new: y.data.length,
      identical: JSON.stringify(x.data) === JSON.stringify(y.data), diff_positions: diffPos, diff_positions_tie_only: tieOnly,
      same_visitor_but_field_diff: fieldDiff, rows_identical_by_visitor: sameRowsById, page_set_equal: setEqual, verdict: ok ? 'EQUIVALENT' : 'MISMATCH' }));
  }
  console.log('COMPARE ' + (allOk ? 'PASS' : 'FAIL'));
  process.exit(allOk ? 0 : 1);
}

const [cmd, a, b] = process.argv.slice(2);
if (cmd === 'run' && /^[a-z0-9_-]{1,32}$/i.test(a || '')) await run(a);
else if (cmd === 'compare' && a && b) compare(a, b);
else { console.error('usage: node list_perf.mjs run <label> | compare <oldLabel> <newLabel>'); process.exit(2); }
