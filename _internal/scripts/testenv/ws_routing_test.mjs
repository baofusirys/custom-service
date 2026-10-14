#!/usr/bin/env node
// [117] 客服消息串台回归与接管规则端到端测试（只连本机隔离测试后端 127.0.0.1）。
// 复现生产 2026-10-15 04:38 事故：打开 A（接管成功）→ 打开 B（他人接待、接管被拒）→ 在 B 页面发消息被改投给 A。
// 用法（Mac 测试环境）：set -a; source secrets/test.env; set +a; node ws_routing_test.mjs
import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';

const BASE = process.env.CS_BASE || 'http://127.0.0.1:18080';
if (!/^http:\/\/127\.0\.0\.1:/.test(BASE)) { console.error('只允许连本机测试后端'); process.exit(2); }
const MYSQL = '/opt/homebrew/opt/mysql@8.4/bin/mysql';
const SOCK = process.env.HOME + '/cs-testenv/run/mysql.sock';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let passed = 0, failed = 0;
function check(name, ok, detail) {
  if (ok) { passed++; console.log('PASS ' + name); return; }
  failed++; console.log('FAIL ' + name + ' :: ' + JSON.stringify(detail === undefined ? null : detail).slice(0, 400));
}
function sql(q) {
  return execFileSync(MYSQL, ['--socket=' + SOCK, '-uroot', '-N', 'cs_test', '-e', q],
    { env: { ...process.env, MYSQL_PWD: process.env.MYSQL_ROOT_PASSWORD }, encoding: 'utf8' }).trim();
}
const q = (s) => "'" + String(s).replace(/'/g, "''") + "'";
async function api(method, path, opts = {}) {
  const headers = {};
  if (opts.token) headers.Authorization = 'Bearer ' + opts.token;
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  const r = await fetch(BASE + path, { method, headers, body: opts.body === undefined ? undefined : JSON.stringify(opts.body) });
  const text = await r.text();
  let j; try { j = JSON.parse(text); } catch { j = { text }; }
  return { status: r.status, ...j };
}
function collect(ws) { const ev = []; ws.onmessage = (m) => { try { ev.push(JSON.parse(m.data)); } catch {} }; return ev; }
async function waitFor(events, pred, ms = 4000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { const f = events.find(pred); if (f) return f; await sleep(60); }
  return null;
}
function opened(ws) {
  return new Promise((res, rej) => { ws.onopen = () => res(); ws.onerror = () => rej(new Error('ws error')); setTimeout(() => rej(new Error('ws timeout')), 6000); });
}
async function visitor(name, vid = '', greet = true) {
  const s = await api('POST', '/api/visitor/session', { body: { site_id: 'default', identifier: name, visitor_id: vid, ua: 'QA-Routing-Test/1.0' } });
  if (s.code !== 0) throw new Error('visitor session failed ' + JSON.stringify(s));
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/visitor', ['cs-auth', s.visitor_token]);
  const events = collect(ws);
  await opened(ws);
  await waitFor(events, (e) => e.type === 'hello');
  const v = { name, vid: s.visitor_id, conv: s.conversation, ws, events };
  if (!greet) return v;
  const id = randomUUID();
  ws.send(JSON.stringify({ type: 'chat', id, content: name + ' 的咨询', ts: Date.now() }));
  await waitFor(events, (e) => e.type === 'ack' && (e.id === id || e.client_id === id));
  v.conv = sql('SELECT id FROM conversations WHERE visitor_id=' + q(v.vid) + " AND status='open' AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1") || v.conv;
  return v;
}
async function login(username, password) {
  const l = await api('POST', '/api/agent/login', { body: { username, password } });
  if (!l.token) throw new Error('agent login failed ' + JSON.stringify(l));
  return { token: l.token, id: String(l.agent && l.agent.id), username };
}
async function conn(a) {
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/agent', ['cs-auth', a.token]);
  const events = collect(ws);
  await opened(ws);
  const hello = await waitFor(events, (e) => e.type === 'hello');
  return { ws, events, connId: hello && hello.extra ? hello.extra.conn_id : '', send: (o) => ws.send(JSON.stringify({ ts: Date.now(), ...o })) };
}
async function say(c, conv, content) {
  const id = randomUUID();
  const o = { type: 'chat', id, content };
  if (conv !== undefined) o.conv = conv;
  c.send(o);
  const r = await waitFor(c.events, (e) => (e.type === 'ack' || e.type === 'error') && (e.id === id || e.client_id === id));
  return { id, r };
}
const assign = (a, conv, connId) => {
  const headers = { Authorization: 'Bearer ' + a.token };
  if (connId) headers['X-CS-Conn'] = connId;
  return fetch(BASE + '/api/agent/conversations/' + conv + '/assign', { method: 'POST', headers }).then(async (r) => ({ status: r.status, ...(await r.json().catch(() => ({}))) }));
};
const storedConv = (id) => sql('SELECT conv_id FROM messages WHERE id=' + q(id));
const got = (v, pred, ms = 1500) => waitFor(v.events, pred, ms);

const run = randomUUID().slice(0, 6);
const admin = await login(process.env.QA_ADMIN_USER || 'qa_admin', process.env.ADMIN_BOOTSTRAP_PASSWORD);
const mk = async (tag, nick) => {
  const u = 'qa_rt' + tag + '_' + run, p = 'Rt!' + randomUUID().slice(0, 8) + 'Zx9#';
  const r = await api('POST', '/api/admin/agents', { token: admin.token, body: { username: u, password: p, role: 'agent', nickname: nick } });
  if (r.code !== 0) throw new Error('create agent failed ' + JSON.stringify(r));
  return login(u, p);
};
const X = await mk('x', '路由甲' + run), Y = await mk('y', '路由乙' + run), Z = await mk('z', '路由丙' + run);
const VA = await visitor('路由A' + run), VB = await visitor('路由B' + run), VC = await visitor('路由C' + run), VD = await visitor('路由D' + run);

// ===== 1. 复现生产事故：A 接管成功 → B 由在线的他人接待 → 在 B 发消息 =====
const X1 = await conn(X), Y1 = await conn(Y);
check('X 接管 A 成功', (await assign(X, VA.conv)).status === 200);
check('Y 接管 B 成功', (await assign(Y, VB.conv)).status === 200);
let r = await assign(X, VB.conv);
check('B 由在线的 Y 接待：X 接管被拒 40303 且返回接待人', r.status === 403 && r.code === 40303 && String(r.msg).includes('路由乙'), r);
let s = await say(X1, VB.conv, '串台探针-' + run);
check('X 在 B 页面发送：服务端回 error（不再改投）', s.r && s.r.type === 'error' && String(s.r.content).includes('其他客服接待'), s.r);
check('该消息没有落库到任何会话（尤其不在 A）', storedConv(s.id) === '', storedConv(s.id));
check('A 客户没有收到这条消息', !(await got(VA, (e) => e.type === 'chat' && String(e.content).includes('串台探针'))));
check('B 客户也没有收到', !(await got(VB, (e) => e.type === 'chat' && String(e.content).includes('串台探针'), 300)));
s = await say(X1, VA.conv, 'A 正常消息-' + run);
check('X 回到 A 发送正常落库到 A', s.r && s.r.type === 'ack' && storedConv(s.id) === VA.conv, { r: s.r, stored: storedConv(s.id) });
check('A 客户收到', !!(await got(VA, (e) => e.type === 'chat' && String(e.content).includes('A 正常消息'))));

// ===== 2. 原接待客服离线后可接管，接管后原客服不能再发 =====
Y1.ws.close(); await sleep(800);
r = await assign(X, VB.conv);
check('Y 离线后 X 接管 B 成功', r.status === 200, r);
check('B 的接待人改为 X', sql('SELECT agent_id FROM conversations WHERE id=' + q(VB.conv)) === X.id);
check('接管写入审计', sql("SELECT COUNT(*) FROM audit_logs WHERE action='conversation_takeover' AND target=" + q(VB.conv)) !== '0');
s = await say(X1, VB.conv, 'B 接管后消息-' + run);
check('接管后 X 在 B 发送正常', s.r && s.r.type === 'ack' && storedConv(s.id) === VB.conv, s.r);
check('B 客户收到', !!(await got(VB, (e) => e.type === 'chat' && String(e.content).includes('B 接管后消息'))));
const Y2 = await conn(Y);
s = await say(Y2, VB.conv, 'Y 越权-' + run);
check('Y 重新上线后不能再在 B 发送', s.r && s.r.type === 'error' && storedConv(s.id) === '', s.r);

// ===== 3. 管理员可直接接管（即使原接待在线） =====
const AD = await conn(admin);
r = await assign(admin, VB.conv);
check('管理员接管在线客服 X 的会话 B 成功', r.status === 200, r);
s = await say(X1, VB.conv, 'X 被接管后-' + run);
check('被管理员接管后 X 不能再在 B 发送', s.r && s.r.type === 'error' && storedConv(s.id) === '', s.r);
s = await say(AD, VB.conv, '管理员回复-' + run);
check('管理员在 B 发送正常', s.r && s.r.type === 'ack' && storedConv(s.id) === VB.conv, s.r);

// ===== 4. 同账号多设备：两台设备各开不同会话，互不串 =====
check('X 接管 C 成功', (await assign(X, VC.conv)).status === 200);
const X2 = await conn(X);
await assign(X, VA.conv); // HTTP 接管会把 X 的全部连接都切到 A（模拟另一台设备刚打开 A）
s = await say(X1, VC.conv, '设备1发C-' + run);
check('设备 1 在 C 发送仍落到 C（不被另一设备的绑定改投）', s.r && s.r.type === 'ack' && storedConv(s.id) === VC.conv, { r: s.r, stored: storedConv(s.id) });
s = await say(X2, VA.conv, '设备2发A-' + run);
check('设备 2 在 A 发送落到 A', s.r && s.r.type === 'ack' && storedConv(s.id) === VA.conv, { r: s.r, stored: storedConv(s.id) });
check('A 客户没有收到发给 C 的消息', !(await got(VA, (e) => e.type === 'chat' && String(e.content).includes('设备1发C'), 500)));
check('C 客户收到自己的消息', !!(await got(VC, (e) => e.type === 'chat' && String(e.content).includes('设备1发C'))));

// ===== 4b. 带设备连接 ID 的接管只绑定该设备：另一设备继续实时收到自己会话的输入状态 =====
check('设备 1 接管 C（带连接 ID）', (await assign(X, VC.conv, X1.connId)).status === 200);
check('设备 2 接管 A（带连接 ID）', (await assign(X, VA.conv, X2.connId)).status === 200);
X1.events.length = 0; X2.events.length = 0;
VC.ws.send(JSON.stringify({ type: 'typing', ts: Date.now() }));
check('访客 C 输入时设备 1 收到正在输入', !!(await waitFor(X1.events, (e) => e.type === 'typing', 1500)));
check('设备 2（在 A）不受影响：不会收到 C 的输入状态', !(await waitFor(X2.events, (e) => e.type === 'typing', 500)));
check('访客 C 不会收到自己输入状态的回声', !(await waitFor(VC.events, (e) => e.type === 'typing', 300)));

// ===== 5. 正在输入 / 已读不再落到错误会话 =====
const Y3 = await conn(Y);
check('Y 接管 D', (await assign(Y, VD.conv)).status === 200);
VA.events.length = 0; VC.events.length = 0; VD.events.length = 0;
X1.send({ type: 'typing', conv: VD.conv });
check('X 对他人在线接待的 D 发输入状态：D 收不到', !(await got(VD, (e) => e.type === 'typing', 700)));
check('也不会落到 X 当前绑定的会话客户', !(await got(VA, (e) => e.type === 'typing', 300)) && !(await got(VC, (e) => e.type === 'typing', 300)));
await sleep(1100);
X1.send({ type: 'typing', conv: VA.conv });
check('X 对自己的 A 发输入状态：A 收到', !!(await got(VA, (e) => e.type === 'typing', 1500)));
VD.events.length = 0;
X1.send({ type: 'read', conv: VD.conv });
check('X 对无权查看的 D 发已读：D 收不到已读回执', !(await got(VD, (e) => e.type === 'read', 800)));

// ===== 6. 非法输入与旧客户端 =====
s = await say(X1, "x' OR '1'='1", '注入探针-' + run);
check('非法会话 ID 被拒绝', s.r && s.r.type === 'error' && storedConv(s.id) === '', s.r);
r = await assign(X, "x' OR '1'='1");
check('非法会话 ID 接管被拒 403', r.status === 403, r);
await assign(X, VA.conv);
s = await say(X1, undefined, '旧客户端不带conv-' + run);
check('旧客户端不带 conv 时沿用当前接管的会话', s.r && s.r.type === 'ack' && storedConv(s.id) === VA.conv, s.r);

// ===== 7. 并发接管：只能一个成功 =====
const Y4 = await conn(Y);
check('Y 接管 C（C 原由 X 接待，X 在线 → 普通客服被拒）', (await assign(Y, VC.conv)).status === 403);
X1.ws.close(); X2.ws.close(); await sleep(800); // X 离线
const Zc = await conn(Z);
const [ry, rz] = await Promise.all([assign(Y, VC.conv), assign(Z, VC.conv)]);
const owner = sql('SELECT agent_id FROM conversations WHERE id=' + q(VC.conv));
const wins = [ry, rz].filter((x) => x.status === 200).length;
check('并发接管只有一个成功且接待人与之一致', wins === 1 && ((ry.status === 200 && owner === Y.id) || (rz.status === 200 && owner === Z.id)), { ry: ry.status, rz: rz.status, owner });

// ===== 8. 列表并发合并：并发请求结果一致、行互不影响 =====
const lists = await Promise.all(Array.from({ length: 12 }, () => api('GET', '/api/agent/conversations?mode=all&limit=50', { token: admin.token })));
const totals = new Set(lists.map((x) => x.total));
check('12 个并发列表请求全部成功且总数一致', lists.every((x) => x.status === 200 && x.code === 0) && totals.size === 1, [...totals]);
check('每份结果都不含密文字段', lists.every((x) => (x.data || []).every((row) => !('ip_cipher' in row))));

// ===== 9. 超时关旧开新：在旧会话页面回复，投递到客户当前所在的新会话 =====
const VE = await visitor('路由E' + run);
check('管理员接管 E', (await assign(admin, VE.conv)).status === 200);
r = await api('POST', '/api/agent/conversations/' + VE.conv + '/close', { token: admin.token });
check('结束 E 的会话', r.status === 200 && r.code === 0, r);
VE.ws.close();
const VE2 = await visitor('路由E' + run, VE.vid, false); // 客户刷新页面：开启新会话
const newConv = sql('SELECT id FROM conversations WHERE visitor_id=' + q(VE.vid) + " AND status='open' AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1");
check('客户已进入新会话', newConv && newConv !== VE.conv, { old: VE.conv, newConv });
s = await say(AD, VE.conv, '旧页面回复-' + run);
check('在旧会话页面回复：确认回执指向新会话并带原请求会话', s.r && s.r.type === 'ack' && s.r.conv === newConv && s.r.extra && s.r.extra.requested_conv === VE.conv, s.r);
check('消息落库在新会话', storedConv(s.id) === newConv, storedConv(s.id));
check('客户在新会话实时收到', !!(await got(VE2, (e) => e.type === 'chat' && String(e.content).includes('旧页面回复'))));
check('新会话随之分配给回复的客服', sql('SELECT agent_id FROM conversations WHERE id=' + q(newConv)) === admin.id);
VE2.ws.close();

for (const c of [Y2, Y3, Y4, AD, Zc]) c.ws.close();
for (const v of [VA, VB, VC, VD]) v.ws.close();
// 清理：测试客服停用
for (const a of [X, Y, Z]) await api('POST', '/api/admin/agents/active', { token: admin.token, body: { id: Number(a.id), active: false } });
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
