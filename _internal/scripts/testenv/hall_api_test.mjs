#!/usr/bin/env node
// [119] 多人大厅接口与 WebSocket 端到端测试（只连本机隔离测试后端 127.0.0.1:18080 与本机测试 LiveKit 127.0.0.1:17880）。
// 覆盖正常 / 拒绝 / 绕过：入场令牌字段与权限（解码 JWT，不验签）、identity 不含 vid、访客令牌缺失/伪造 401、
// 移出后 join/上传/消息 40372、拉黑静默（消息只回显自己）、消息限流（WS 等价 42910）、hall_chat 引用私聊文件或他人文件被拒、
// 越权 moderate 40376、非管理员改大厅设置 403、设置非法值 40059（整次不保存）、视频关闭时摄像头 40375、
// 伪造 webhook 签名 401 与带 X-Forwarded-For / X-Real-IP 404、删除消息广播、新进入者 history 最多 50 条正序、
// 访客令牌不能调用 LiveKit 服务端 API（Twirp 越权）。
// 需要真实 WebRTC 媒体的用例（开麦名额占满 40973、摄像头名额占满 40976、发布中被收回权限、静音状态续租）
// 由浏览器端到端覆盖：hall_browser_e2e.mjs（访客网页窗口）与 admin_hall_e2e.mjs（管理后台）。
// 用法（Mac 测试环境）：source secrets/test.env && node _internal/scripts/testenv/hall_api_test.mjs
// 凭证只从环境变量读取（ADMIN_BOOTSTRAP_PASSWORD、LIVEKIT_API_KEY、LIVEKIT_API_SECRET、MYSQL_ROOT_PASSWORD），输出中不打印任何凭证与令牌。
// 结束时恢复 5 项大厅设置、解除拉黑、停用临时客服、删除本次写入的大厅测试消息；被移出的测试访客 1 分钟后自动解除。
import { execFileSync } from 'node:child_process';
import { randomUUID, randomBytes, createHmac, createHash } from 'node:crypto';

const BASE = process.env.CS_BASE || 'http://127.0.0.1:18080';
const LK = process.env.LK_BASE || 'http://127.0.0.1:17880';
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(BASE) || !/^http:\/\/127\.0\.0\.1:\d+$/.test(LK)) {
  console.error('只允许连本机测试后端与本机 LiveKit（127.0.0.1）');
  process.exit(2);
}
const ADMIN_USER = process.env.QA_ADMIN_USER || 'qa_admin';
const ADMIN_PASS = process.env.ADMIN_BOOTSTRAP_PASSWORD;
const LK_KEY = process.env.LIVEKIT_API_KEY;
const LK_SECRET = process.env.LIVEKIT_API_SECRET;
const missing = [['ADMIN_BOOTSTRAP_PASSWORD', ADMIN_PASS], ['LIVEKIT_API_KEY', LK_KEY], ['LIVEKIT_API_SECRET', LK_SECRET]]
  .filter(([, v]) => !v).map(([k]) => k);
if (missing.length) { console.log('FAIL 缺少环境变量 ' + missing.join(', ')); process.exit(2); }
const MYSQL = '/opt/homebrew/opt/mysql@8.4/bin/mysql';
const SOCK = (process.env.HOME || '') + '/cs-testenv/run/mysql.sock';
const ROOM = 'hall_default';
const HALL_KEYS = ['hall_enabled', 'hall_video_enabled', 'hall_max_speakers', 'hall_max_cameras', 'hall_max_participants'];
const ID_RE = /^v_[0-9a-f]{20}$/;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- 计数与脱敏输出 ----------
let passed = 0, failed = 0;
const redact = (v) => JSON.stringify(v === undefined ? null : v, (k, x) => (/token|credential|secret|password|authorization/i.test(k) ? '[REDACTED]' : x))
  .replace(/access=[^"&\s]+/g, 'access=***').replace(/eyJ[\w-]+\.[\w-]+\.[\w-]*/g, '[JWT]').slice(0, 400);
function check(name, ok, detail) {
  if (ok) { passed++; console.log('PASS ' + name); return; }
  failed++; console.log('FAIL ' + name + ' :: ' + redact(detail));
}

// ---------- 基础工具 ----------
const q = (s) => "'" + String(s).replace(/\\/g, '\\\\').replace(/'/g, "''") + "'";
function sql(query) {
  return execFileSync(MYSQL, ['--socket=' + SOCK, '-uroot', '-N', 'cs_test', '-e', query],
    { env: { ...process.env, MYSQL_PWD: process.env.MYSQL_ROOT_PASSWORD }, encoding: 'utf8' }).trim();
}
async function api(method, path, opts = {}) {
  const headers = { ...(opts.headers || {}) };
  if (opts.token) headers.Authorization = 'Bearer ' + opts.token;
  let body;
  if (opts.rawBody !== undefined) body = opts.rawBody;
  else if (opts.body !== undefined) { headers['Content-Type'] = 'application/json'; body = JSON.stringify(opts.body); }
  const r = await fetch(BASE + path, { method, headers, body });
  const text = await r.text();
  let j; try { j = JSON.parse(text); } catch { j = { text: text.slice(0, 200) }; }
  return { status: r.status, ...j };
}
function collect(ws) { const ev = []; ws.onmessage = (m) => { try { ev.push(JSON.parse(m.data)); } catch {} }; return ev; }
async function waitFor(events, pred, ms = 4000, from = 0) {
  const end = Date.now() + ms;
  while (Date.now() < end) { const f = events.slice(from).find(pred); if (f) return f; await sleep(60); }
  return null;
}
function opened(ws) {
  return new Promise((res, rej) => { ws.onopen = () => res(); ws.onerror = () => rej(new Error('ws error')); setTimeout(() => rej(new Error('ws timeout')), 6000); });
}

// ---------- JWT（只用于解码检查与构造伪造 / 回调令牌；从不打印） ----------
const b64url = (s) => Buffer.from(s).toString('base64url');
function signJWT(payload, secret, alg = 'HS256') {
  const h = b64url(JSON.stringify({ alg, typ: 'JWT' })), p = b64url(JSON.stringify(payload));
  const sig = alg === 'none' ? '' : createHmac('sha256', secret).update(h + '.' + p).digest('base64url');
  return h + '.' + p + '.' + sig;
}
function decodeJWT(tok) {
  try { return JSON.parse(Buffer.from(String(tok).split('.')[1], 'base64url').toString('utf8')); } catch { return null; }
}
const nowSec = () => Math.floor(Date.now() / 1000);

// ---------- 参与者 ----------
const run = randomUUID().slice(0, 6);
const actors = [];
// pace：每个身份大厅消息限流（访客 5 条/10 秒、客服 20 条/10 秒），普通用例自动等待，只有限流用例故意不等
function pacer(limit) {
  const sent = [];
  return async () => {
    for (;;) {
      const now = Date.now();
      while (sent.length && now - sent[0] > 10500) sent.shift();
      if (sent.length < limit) { sent.push(now); return; }
      await sleep(10600 - (now - sent[0]));
    }
  };
}
async function visitor(name) {
  const s = await api('POST', '/api/visitor/session', { body: { site_id: 'default', identifier: name, visitor_id: '', last_page: 'https://example.test/hall?qa=' + run, ua: 'QA-Hall-API/1.0' } });
  if (s.code !== 0) throw new Error('visitor session failed ' + redact(s));
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/visitor', ['cs-auth', s.visitor_token]);
  const events = collect(ws);
  await opened(ws);
  await waitFor(events, (e) => e.type === 'hello');
  const v = { kind: 'visitor', name, vid: s.visitor_id, token: s.visitor_token, ws, events, pace: pacer(5), send: (o) => ws.send(JSON.stringify({ ts: Date.now(), ...o })) };
  actors.push(v);
  return v;
}
async function agent(username, password) {
  const l = await api('POST', '/api/agent/login', { body: { username, password } });
  if (!l.token) throw new Error('agent login failed ' + redact({ status: l.status, code: l.code, msg: l.msg }));
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/agent', ['cs-auth', l.token]);
  const events = collect(ws);
  await opened(ws);
  const a = { kind: 'agent', token: l.token, id: String(l.agent && l.agent.id), ws, events, pace: pacer(20), send: (o) => ws.send(JSON.stringify({ ts: Date.now(), ...o })) };
  actors.push(a);
  return a;
}
async function subscribe(p) {
  const id = randomUUID();
  p.send({ type: 'hall_subscribe', id });
  return waitFor(p.events, (e) => (e.type === 'hall_subscribed' || e.type === 'error') && e.id === id);
}
// hallSay：发大厅消息，返回 { id, r }（r 为 hall_ack 或 error 帧）
async function hallSay(p, content, extra = {}, opts = {}) {
  if (!opts.noPace) await p.pace();
  const id = extra.id || randomUUID();
  p.send({ type: 'hall_chat', content, ...extra, id });
  // 服务端对不合法的 id 不回显（错误帧不带 id），此时用 replyId: '' 匹配
  const want = opts.replyId === undefined ? id : opts.replyId;
  const r = await waitFor(p.events, (e) => (e.type === 'hall_ack' || e.type === 'error') && (e.id || '') === want && !e.__seen, opts.ms || 4000);
  if (r) r.__seen = true;
  return { id, r };
}
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64');
async function upload(token, uploader, scope) {
  const fd = new FormData();
  fd.append('uploader', uploader);
  if (scope !== undefined) fd.append('scope', scope);
  fd.append('file', new Blob([PNG], { type: 'image/png' }), 'qa-hall.png');
  const r = await fetch(BASE + '/api/upload', { method: 'POST', headers: token ? { Authorization: 'Bearer ' + token } : {}, body: fd });
  let j; try { j = await r.json(); } catch { j = {}; }
  return { status: r.status, ...j };
}
async function twirp(method, body, token) {
  const r = await fetch(LK + '/twirp/livekit.RoomService/' + method, { method: 'POST', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  let j; try { j = await r.json(); } catch { j = {}; }
  return { status: r.status, body: j };
}
function webhookAuth(body, { secret = LK_SECRET, iss = LK_KEY, exp = nowSec() + 60, alg = 'HS256', sha } = {}) {
  return signJWT({ iss, nbf: nowSec() - 5, exp, sha256: sha || createHash('sha256').update(body).digest('base64') }, secret, alg);
}
const hookBody = (event, identity) => JSON.stringify({ event, id: 'EV_qa' + run + randomBytes(4).toString('hex'), createdAt: String(nowSec()), room: { name: ROOM }, participant: { identity, name: 'qa' } });
const postHook = (body, headers) => api('POST', '/api/livekit/webhook', { rawBody: body, headers: { 'Content-Type': 'application/webhook+json', ...headers } });
const setHall = (admin, body) => api('POST', '/api/admin/settings', { token: admin.token, body });

let admin, B, saved = null;
const blockedVids = [];
let createdB = false;
try {
  // ===== 0. 前置：LiveKit 可达、管理员与临时普通客服 =====
  let lkHealth = '';
  // LiveKit 刚启动或刚经历高负载时会短暂返回「Not Ready」（节点统计未刷新），最多等 20 秒
  for (let i = 0; i < 20 && lkHealth.trim() !== 'OK'; i++) {
    try { lkHealth = await (await fetch(LK + '/')).text(); } catch {}
    if (lkHealth.trim() !== 'OK') await sleep(1000);
  }
  check('本机测试 LiveKit 健康（GET / 返回 OK）', lkHealth.trim() === 'OK', lkHealth);
  admin = await agent(ADMIN_USER, ADMIN_PASS);
  const bName = 'qa_hall_' + run, bPass = 'Qh!' + randomUUID().slice(0, 8) + 'Zx9#';
  let r = await api('POST', '/api/admin/agents', { token: admin.token, body: { username: bName, password: bPass, role: 'agent', nickname: '大厅测试客服' } });
  check('管理员创建普通客服 B', r.code === 0, r);
  createdB = r.code === 0;
  B = await agent(bName, bPass);
  const before = await api('GET', '/api/admin/settings', { token: admin.token });
  check('读取大厅设置（5 个键都存在）', before.code === 0 && HALL_KEYS.every((k) => typeof (before.data || {})[k] === 'string'), before.data);
  saved = Object.fromEntries(HALL_KEYS.filter((k) => typeof (before.data || {})[k] === 'string').map((k) => [k, before.data[k]]));
  r = await setHall(admin, { hall_enabled: 'true', hall_video_enabled: 'false', hall_max_speakers: '6', hall_max_cameras: '2', hall_max_participants: '100' });
  check('测试基线设置保存成功', r.code === 0, r);

  const V1 = await visitor('大厅甲' + run), V2 = await visitor('大厅乙' + run), V3 = await visitor('大厅丙' + run);
  const V4 = await visitor('大厅丁' + run), V5 = await visitor('大厅戊' + run);

  // ===== 1. 鉴权：缺失 / 伪造访客令牌 401；令牌类型混用 401 =====
  r = await api('POST', '/api/visitor/hall/join');
  check('拒绝：访客 join 未带令牌 401 40110', r.status === 401 && r.code === 40110, r);
  const forged = signJWT({ vid: V1.vid, visitor_id: V1.vid, site_id: 'default', exp: nowSec() + 3600 }, randomBytes(32).toString('hex'));
  r = await api('POST', '/api/visitor/hall/join', { token: forged });
  check('绕过：伪造签名的访客令牌 401 40110', r.status === 401 && r.code === 40110, r);
  r = await api('POST', '/api/visitor/hall/join', { token: signJWT({ vid: V1.vid, visitor_id: V1.vid, site_id: 'default', exp: nowSec() + 3600 }, '', 'none') });
  check('绕过：alg=none 访客令牌 401', r.status === 401, r);
  r = await api('POST', '/api/visitor/hall/join', { token: admin.token });
  check('绕过：客服令牌调用访客 join 401', r.status === 401, r);
  r = await api('GET', '/api/agent/hall/state', { token: V1.token });
  check('越权：访客令牌调用客服 state 401', r.status === 401, r);
  r = await api('POST', '/api/agent/hall/join');
  check('拒绝：客服 join 未登录 401', r.status === 401, r);

  // ===== 2. 访客 join：字段、令牌权限、identity 不含 vid =====
  const j1 = await api('POST', '/api/visitor/hall/join', { token: V1.token });
  const d1 = j1.data || {};
  V1.identity = d1.identity;
  check('访客 join 成功', j1.status === 200 && j1.code === 0, j1);
  check('join 字段齐全且类型正确', /^wss?:\/\//.test(d1.url || '') && d1.room === ROOM && typeof d1.token === 'string' && d1.video_enabled === false
    && d1.max_speakers === 6 && d1.max_cameras === 2 && Array.isArray(d1.ice_servers) && Array.isArray(d1.history) && d1.history.length <= 50, { ...d1, token: undefined, history: (d1.history || []).length });
  const vidHex = V1.vid.replace(/-/g, '');
  check('identity 为 v_+20 位 hex 且不含 vid', ID_RE.test(d1.identity || '') && !String(d1.identity).includes(V1.vid) && !String(d1.identity).includes(vidHex.slice(0, 20)), d1.identity);
  check('显示名为「访客 NNNN」', /^访客 \d{4}$/.test(d1.name || ''), d1.name);
  const respNoToken = JSON.stringify({ ...d1, token: undefined });
  check('join 返回内容不含 vid（含 ICE 用户名）', !respNoToken.includes(V1.vid) && !respNoToken.includes(vidHex), null);
  const c1 = decodeJWT(d1.token) || {};
  const g1 = c1.video || {};
  check('访客令牌：sub=identity、name 一致、iss=API Key', c1.sub === d1.identity && c1.name === d1.name && c1.iss === LK_KEY, { sub: c1.sub, name: c1.name });
  check('访客令牌授权：仅本房间 roomJoin + 可订阅', g1.roomJoin === true && g1.room === ROOM && g1.canSubscribe === true, g1);
  check('访客令牌授权：不可发布 / 不可发 data / 不可改 metadata', g1.canPublish === false && g1.canPublishData === false && g1.canUpdateOwnMetadata === false
    && !(g1.canPublishSources && g1.canPublishSources.length), g1);
  check('访客令牌授权：无 roomAdmin / roomCreate / roomList / hidden', !g1.roomAdmin && !g1.roomCreate && !g1.roomList && !g1.hidden, g1);
  check('访客令牌 TTL 6 小时', Math.abs((c1.exp - (c1.nbf || c1.iat)) - 21600) <= 120, { exp: c1.exp, nbf: c1.nbf });
  let meta1 = {}; try { meta1 = JSON.parse(c1.metadata || '{}'); } catch {}
  check('访客 metadata 为 {role:visitor, code:NNNN}', meta1.role === 'visitor' && /^\d{4}$/.test(meta1.code || '') && d1.name === '访客 ' + meta1.code && Object.keys(meta1).length === 2, meta1);
  check('访客令牌载荷不含 vid', !JSON.stringify(c1).includes(V1.vid) && !JSON.stringify(c1).includes(vidHex), null);
  const j1b = await api('POST', '/api/visitor/hall/join', { token: V1.token });
  check('同一访客再次 join：identity 稳定', j1b.code === 0 && j1b.data.identity === d1.identity, null);

  // ===== 3. 客服 join：只授予麦克风发布，无管理权限 =====
  const ja = await api('POST', '/api/agent/hall/join', { token: admin.token });
  const ca = decodeJWT((ja.data || {}).token) || {};
  const ga = ca.video || {};
  check('客服 join 成功且 identity=a_<id>', ja.code === 0 && ja.data.identity === 'a_' + admin.id && /^客服·/.test(ja.data.name || ''), { ...(ja.data || {}), token: undefined, history: undefined });
  check('客服令牌授权：canPublish 且只允许 microphone', ga.canPublish === true && JSON.stringify(ga.canPublishSources) === JSON.stringify(['microphone']), ga);
  check('客服令牌授权：不可发 data / 改 metadata，无 roomAdmin', ga.canPublishData === false && ga.canUpdateOwnMetadata === false && !ga.roomAdmin && !ga.roomCreate && ga.room === ROOM, ga);
  let metaA = {}; try { metaA = JSON.parse(ca.metadata || '{}'); } catch {}
  check('客服 metadata 为 {role:agent, code:""}', metaA.role === 'agent' && metaA.code === '', metaA);
  const jb = await api('POST', '/api/agent/hall/join', { token: B.token });
  check('普通客服 B join 成功', jb.code === 0 && jb.data.identity === 'a_' + B.id, null);

  // ===== 4. LiveKit 服务端 API（Twirp）：访客令牌越权被拒，管理令牌可见的 identity 都不含 vid =====
  let t = await twirp('ListParticipants', { room: ROOM }, d1.token);
  check('越权：访客入场令牌调用 Twirp ListParticipants 被 LiveKit 拒绝', t.status === 401 || t.status === 403, t);
  t = await twirp('UpdateParticipant', { room: ROOM, identity: d1.identity, permission: { can_subscribe: true, can_publish: true, can_publish_sources: ['MICROPHONE', 'CAMERA'] } }, d1.token);
  check('越权：访客入场令牌给自己加发布权限被 LiveKit 拒绝', t.status === 401 || t.status === 403, t);
  t = await twirp('RemoveParticipant', { room: ROOM, identity: 'a_' + admin.id }, d1.token);
  check('越权：访客入场令牌移出客服被 LiveKit 拒绝', t.status === 401 || t.status === 403, t);
  const lkAdmin = signJWT({ iss: LK_KEY, nbf: nowSec() - 5, exp: nowSec() + 60, video: { roomAdmin: true, room: ROOM } }, LK_SECRET);
  t = await twirp('ListParticipants', { room: ROOM }, lkAdmin);
  const parts = (t.body && t.body.participants) || [];
  check('LiveKit 管理令牌可查询房间（200 或房间尚未创建 404）', t.status === 200 || t.status === 404, { status: t.status, code: t.body && t.body.code });
  check('LiveKit 中所有 identity 均为 v_/a_ 格式且不含任何 vid', parts.every((x) => /^(v_[0-9a-f]{20}|a_\d{1,19})$/.test(x.identity || '')) && !JSON.stringify(parts).includes(V1.vid), parts.map((x) => x.identity));

  // ===== 5. WS 订阅 =====
  const notSub = await hallSay(V2, '未订阅先发' + run);
  check('拒绝：未订阅大厅发 hall_chat', notSub.r && notSub.r.type === 'error' && /进入大厅/.test(notSub.r.content || ''), notSub.r);
  for (const [n, p] of [['V1', V1], ['V3', V3], ['V4', V4], ['V5', V5], ['admin', admin], ['B', B]]) {
    const s = await subscribe(p);
    check('订阅大厅：' + n, s && s.type === 'hall_subscribed', s);
  }

  // ===== 6. 发消息与广播；新进入者 history 最多 50 条正序 =====
  const m0 = await hallSay(V1, '大厅首条' + run);
  check('访客发大厅消息收到 hall_ack（带服务器时间）', m0.r && m0.r.type === 'hall_ack' && typeof m0.r.ts === 'number', m0.r);
  const b0 = await waitFor(V4.events, (e) => e.type === 'hall_chat' && e.id === m0.id);
  check('其他访客收到广播：extra 含 sender/identity/name/role', b0 && b0.content === '大厅首条' + run && b0.extra && b0.extra.sender === 'visitor' && b0.extra.identity === V1.identity
    && b0.extra.role === 'visitor' && /^访客 \d{4}$/.test(b0.extra.name || '') && typeof b0.ts === 'number', b0);
  check('广播帧不含发送者 vid', b0 && !JSON.stringify(b0).includes(V1.vid), null);
  const ba = await waitFor(admin.events, (e) => e.type === 'hall_chat' && e.id === m0.id);
  check('客服也收到大厅广播', !!ba, null);
  let insertedOk = false;
  try {
    const m0At = sql('SELECT DATE_FORMAT(created_at, ' + q('%Y-%m-%d %H:%i:%s.%f') + ') FROM hall_messages WHERE id=' + q(m0.id));
    const rows = [];
    for (let i = 0; i < 55; i++) {
      rows.push('(' + [q(randomUUID()), q(ROOM), q('agent'), q(admin.id), q('a_' + admin.id), q('QA历史'), q('QA历史' + run + '#' + i),
        'NULL', 'NULL', 'NULL', 'NULL', 'DATE_SUB(' + q(m0At) + ', INTERVAL ' + (100 - i) + ' SECOND)', 'NULL', q('')].join(', ') + ')');
    }
    sql('INSERT INTO hall_messages(id, room, sender, sender_ref, identity, name, content, media_url, media_kind, media_name, media_size, created_at, deleted_at, deleted_by) VALUES ' + rows.join(', '));
    insertedOk = true;
  } catch (e) { check('写入 55 条较早的历史消息（测试库）', false, String(e.message || e).slice(0, 200)); }
  if (insertedOk) {
    const V6 = await visitor('大厅己' + run);
    const j6 = await api('POST', '/api/visitor/hall/join', { token: V6.token });
    const h = (j6.data && j6.data.history) || [];
    const asc = h.every((m, i) => i === 0 || String(h[i - 1].created_at) <= String(m.created_at));
    check('新进入者 history 恰好 50 条', h.length === 50, h.length);
    check('history 按时间正序', asc, h.slice(0, 3).map((m) => m.created_at));
    check('history 末条是最新消息、最早的历史被截掉', h.length && h[h.length - 1].id === m0.id && h.some((m) => m.content === 'QA历史' + run + '#54') && !h.some((m) => m.content === 'QA历史' + run + '#0'), h.slice(-2).map((m) => m.content));
    const keys = ['id', 'sender', 'identity', 'name', 'role', 'content', 'media_url', 'media_kind', 'media_name', 'media_size', 'created_at'];
    check('HallMessage 字段齐全、created_at 带 +08:00、不含 sender_ref', h.every((m) => keys.every((k) => k in m) && !('sender_ref' in m) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}\+08:00$/.test(m.created_at)), h[0]);
  }
  r = await api('GET', '/api/visitor/hall/messages?limit=500', { token: V1.token });
  check('历史分页 limit 越界按 50', r.code === 0 && Array.isArray(r.data) && r.data.length <= 50, (r.data || []).length);
  r = await api('GET', '/api/visitor/hall/messages?limit=50&before=' + m0.id, { token: V1.token });
  check('before=<id> 只返回严格更早的消息', r.code === 0 && (r.data || []).every((m) => m.id !== m0.id), null);
  r = await api('GET', '/api/visitor/hall/messages?before=' + encodeURIComponent("x' OR '1'='1"), { token: V1.token });
  check('拒绝：before 非法（注入字符）400 40060', r.status === 400 && r.code === 40060, r);

  // ===== 7. hall_chat 校验：标识、长度、空内容、幂等重试、标识冲突 =====
  let x = await hallSay(V1, '标识无效' + run, { id: 'not-a-uuid' }, { replyId: '' });
  check('拒绝：hall_chat id 不是 UUID v4', x.r && x.r.type === 'error' && /标识/.test(x.r.content || ''), x.r);
  x = await hallSay(V1, '长'.repeat(1001));
  check('拒绝：内容超过 1000 字', x.r && x.r.type === 'error' && /1000/.test(x.r.content || ''), x.r);
  x = await hallSay(V1, '   ');
  check('拒绝：无附件时内容为空', x.r && x.r.type === 'error', x.r);
  const dupId = randomUUID();
  const d1r = await hallSay(V1, '幂等重试' + run, { id: dupId });
  const d2r = await hallSay(V1, '幂等重试' + run, { id: dupId });
  await sleep(800);
  check('同一 id 同内容重试：两次都 ack、只广播一次', d1r.r && d1r.r.type === 'hall_ack' && d2r.r && d2r.r.type === 'hall_ack'
    && V4.events.filter((e) => e.type === 'hall_chat' && e.id === dupId).length === 1, null);
  x = await hallSay(V1, '改了内容' + run, { id: dupId });
  check('绕过：同一 id 换内容被拒（消息标识冲突）', x.r && x.r.type === 'error' && /冲突/.test(x.r.content || ''), x.r);
  x = await hallSay(V4, '冒用他人标识' + run, { id: m0.id });
  check('绕过：他人复用别人的消息 id 被拒', x.r && x.r.type === 'error', x.r);

  // ===== 8. 附件：scope、只接受本人以 scope=hall 上传的文件 =====
  r = await upload(V1.token, 'visitor', 'admin');
  check('拒绝：上传 scope 非法 400 40061', r.status === 400 && r.code === 40061, r);
  r = await upload('', 'visitor', 'hall');
  check('拒绝：大厅上传未带令牌 401', r.status === 401, r);
  const priv = await upload(V1.token, 'visitor');
  check('访客私聊上传成功', priv.code === 0 && typeof priv.url === 'string', priv);
  const hup = await upload(V1.token, 'visitor', 'hall');
  check('访客大厅上传成功（结构与私聊一致）', hup.code === 0 && /^\/files\/.+\?access=/.test(hup.url || '') && hup.kind === 'image' && hup.size === PNG.length, hup);
  if (priv.code === 0) {
    x = await hallSay(V1, '贴私聊文件' + run, { media: priv.url, mkind: 'image' });
    check('绕过：hall_chat 引用私聊文件被拒', x.r && x.r.type === 'error' && /附件/.test(x.r.content || ''), x.r);
  }
  if (hup.code === 0) {
    x = await hallSay(V4, '贴别人的大厅文件' + run, { media: hup.url, mkind: 'image' });
    check('绕过：访客引用他人的大厅文件被拒', x.r && x.r.type === 'error' && /附件/.test(x.r.content || ''), x.r);
    x = await hallSay(admin, '客服贴访客的大厅文件' + run, { media: hup.url, mkind: 'image' });
    check('绕过：客服引用访客的大厅文件被拒', x.r && x.r.type === 'error' && /附件/.test(x.r.content || ''), x.r);
    x = await hallSay(V1, '篡改访问令牌' + run, { media: hup.url.replace(/access=[^&]+/, 'access=' + randomBytes(16).toString('hex')) });
    check('绕过：篡改附件访问令牌被拒', x.r && x.r.type === 'error' && /附件/.test(x.r.content || ''), x.r);
    x = await hallSay(V1, '本人大厅图片' + run, { media: hup.url, mkind: 'file', mname: 'evil.exe', msize: 999999999 });
    const bm = await waitFor(V4.events, (e) => e.type === 'hall_chat' && e.id === x.id);
    check('本人大厅附件发送成功', x.r && x.r.type === 'hall_ack', x.r);
    check('附件元数据以服务端记录为准（忽略客户端 mkind/mname/msize）', bm && String(bm.media || '').split('?')[0] === hup.url.split('?')[0] && bm.mkind === 'image' && bm.mname === 'qa-hall.png' && bm.msize === PNG.length, bm && { mkind: bm.mkind, mname: bm.mname, msize: bm.msize });
  }

  // ===== 9. 消息限流：每访客 5 条/10 秒（WS 错误帧即 42910 的等价语义） =====
  const burst = [];
  for (let i = 0; i < 7; i++) burst.push(await hallSay(V5, '限流' + run + '#' + i, {}, { noPace: true }));
  const acks = burst.filter((b) => b.r && b.r.type === 'hall_ack').length;
  const limited = burst.filter((b) => b.r && b.r.type === 'error' && /频繁/.test(b.r.content || '')).length;
  check('消息限流：10 秒内前 5 条成功、超出被拒「发送过于频繁」', acks === 5 && limited === 2, { acks, limited });

  // ===== 10. 大厅设置：越权、非法值整次不保存、变更广播 =====
  r = await setHall(B, { hall_enabled: 'false' });
  check('越权：普通客服改大厅设置 403', r.status === 403, r);
  for (const bad of [{ hall_max_speakers: '21' }, { hall_max_speakers: '0' }, { hall_enabled: 'yes' }, { hall_video_enabled: '1' }, { hall_max_cameras: '7' }, { hall_max_participants: '501' }, { hall_max_speakers: '6a' }, { hall_max_speakers: '-1' }, { hall_max_speakers: '1e1' }]) {
    r = await setHall(admin, bad);
    check('拒绝：大厅设置非法值 40059 ' + JSON.stringify(bad), r.status === 400 && r.code === 40059, r);
  }
  // 首尾空白会被去掉并按规范化值保存（例如 ' 6' 保存为 '6'）
  r = await setHall(admin, { hall_max_speakers: ' 6' });
  const trimmed = await api('GET', '/api/admin/settings', { token: admin.token });
  check('大厅设置首尾空白被去掉、按规范值保存', r.code === 0 && trimmed.data && trimmed.data.hall_max_speakers === '6', trimmed.data && trimmed.data.hall_max_speakers);
  r = await setHall(admin, { hall_max_cameras: '3', hall_max_participants: '9' });
  const afterBad = await api('GET', '/api/admin/settings', { token: admin.token });
  check('非法值整次不保存（合法键也不落库）', r.code === 40059 && afterBad.data && afterBad.data.hall_max_cameras === '2', afterBad.data && afterBad.data.hall_max_cameras);
  r = await setHall(admin, { hall_max_speakers: '5' });
  check('管理员保存 hall_max_speakers=5', r.code === 0, r);
  const s1 = await waitFor(V1.events, (e) => e.type === 'hall_settings' && e.extra && e.extra.max_speakers === 5);
  const s2 = await waitFor(V2.events, (e) => e.type === 'hall_settings' && e.extra && e.extra.max_speakers === 5);
  const sa = await waitFor(B.events, (e) => e.type === 'hall_settings' && e.extra && e.extra.max_speakers === 5);
  check('hall_settings 广播给已订阅访客、未订阅的在线访客与客服', !!s1 && !!s2 && !!sa, null);
  check('hall_settings 字段：enabled/video_enabled/max_speakers/max_cameras', s1 && s1.extra.enabled === true && s1.extra.video_enabled === false && s1.extra.max_cameras === 2, s1 && s1.extra);
  await setHall(admin, { hall_max_speakers: '6' });

  // ===== 11. 开麦 / 摄像头规则（无真实媒体：未在 LiveKit 中即 40374；名额占满由浏览器端到端覆盖） =====
  r = await api('GET', '/api/visitor/settings');
  check('访客公开设置追加 hall_enabled / hall_video_enabled（布尔）', r.code === 0 && r.data && r.data.hall_enabled === true && r.data.hall_video_enabled === false, r.data && { hall_enabled: r.data.hall_enabled, hall_video_enabled: r.data.hall_video_enabled });
  r = await api('POST', '/api/visitor/hall/camera', { token: V1.token, body: { on: true } });
  check('视频关闭时访客开摄像头 403 40375', r.status === 403 && r.code === 40375, r);
  r = await api('POST', '/api/agent/hall/camera', { token: admin.token, body: { on: true } });
  check('视频关闭时客服开摄像头 403 40375', r.status === 403 && r.code === 40375, r);
  r = await api('POST', '/api/visitor/hall/mic', { token: V1.token, body: { on: true } });
  check('未连上 LiveKit 时开麦 403 40374（以 LiveKit 中查不到为准）', r.status === 403 && r.code === 40374, r);
  r = await api('POST', '/api/visitor/hall/mic', { token: V1.token, body: { on: false } });
  check('关麦幂等：data.on=false、max=6', r.code === 0 && r.data && r.data.on === false && r.data.max === 6 && typeof r.data.used === 'number', r);
  r = await api('POST', '/api/visitor/hall/mic', { token: V1.token, rawBody: '{"on":"yes"}', headers: { 'Content-Type': 'application/json' } });
  check('拒绝：开麦请求体不合法 400 40060', r.status === 400 && r.code === 40060, r);
  r = await api('POST', '/api/agent/hall/mic', { token: admin.token, body: { on: true } });
  check('客服没有开麦名额接口（404）', r.status === 404, r.status);
  r = await setHall(admin, { hall_video_enabled: 'true' });
  const vs = await api('GET', '/api/visitor/settings');
  check('开启视频后公开设置 hall_video_enabled=true', r.code === 0 && vs.data && vs.data.hall_video_enabled === true, vs.data && vs.data.hall_video_enabled);
  r = await api('POST', '/api/visitor/hall/camera', { token: V1.token, body: { on: true } });
  check('视频开启但未连上 LiveKit：开摄像头 403 40374', r.status === 403 && r.code === 40374, r);
  const mark = V1.events.length;
  r = await setHall(admin, { hall_video_enabled: 'false' });
  const sv = await waitFor(V1.events, (e) => e.type === 'hall_settings' && e.extra && e.extra.video_enabled === false, 4000, mark);
  check('关闭视频广播 hall_settings video_enabled=false', r.code === 0 && !!sv, null);

  // ===== 12. 拉黑静默 =====
  r = await api('POST', '/api/agent/visitor/' + V3.vid + '/block', { token: admin.token, body: { blocked: true, reason: '大厅测试', block_ip: false } });
  check('拉黑访客丙', r.code === 0, r);
  if (r.code === 0) blockedVids.push(V3.vid);
  const j3 = await api('POST', '/api/visitor/hall/join', { token: V3.token });
  check('被拉黑访客照常可进入收听（静默）', j3.code === 0, j3);
  r = await api('POST', '/api/visitor/hall/mic', { token: V3.token, body: { on: true } });
  check('被拉黑访客开麦 403 40374', r.status === 403 && r.code === 40374, r);
  const sil = await hallSay(V3, '静默消息' + run);
  const echo = await waitFor(V3.events, (e) => e.type === 'hall_chat' && e.id === sil.id);
  await sleep(1200);
  check('被拉黑访客发消息仍回 hall_ack 并只回显给自己', sil.r && sil.r.type === 'hall_ack' && !!echo, sil.r);
  check('静默消息不广播给其他访客与客服', !V1.events.some((e) => e.type === 'hall_chat' && e.id === sil.id) && !admin.events.some((e) => e.type === 'hall_chat' && e.id === sil.id), null);
  const own = await api('GET', '/api/visitor/hall/messages', { token: V3.token });
  const oth = await api('GET', '/api/visitor/hall/messages', { token: V1.token });
  const agh = await api('GET', '/api/agent/hall/messages', { token: admin.token });
  check('静默消息只出现在本人历史', (own.data || []).some((m) => m.id === sil.id) && !(oth.data || []).some((m) => m.id === sil.id) && !(agh.data || []).some((m) => m.id === sil.id), null);
  r = await upload(V3.token, 'visitor', 'hall');
  check('被拉黑访客仍可大厅上传（静默）', r.code === 0, r);

  // ===== 13. moderate 越权与参数校验 =====
  r = await api('POST', '/api/agent/hall/moderate', { token: B.token, body: { action: 'kick', identity: 'a_' + admin.id, minutes: 5 } });
  check('越权：普通客服移出管理员 403 40376', r.status === 403 && r.code === 40376, r);
  r = await api('POST', '/api/agent/hall/moderate', { token: B.token, body: { action: 'mute', identity: 'a_' + B.id } });
  check('越权：对自己操作 403 40376', r.status === 403 && r.code === 40376, r);
  r = await api('POST', '/api/agent/hall/moderate', { token: admin.token, body: { action: 'kick', identity: 'v_' + V4.vid.replace(/-/g, '').slice(0, 20) + 'zz' } });
  check('拒绝：identity 格式不合法 400 40060', r.status === 400 && r.code === 40060, r);
  r = await api('POST', '/api/agent/hall/moderate', { token: admin.token, body: { action: 'ban', identity: V1.identity } });
  check('拒绝：action 不合法 400 40060', r.status === 400 && r.code === 40060, r);
  for (const minutes of [2000, -1]) {
    r = await api('POST', '/api/agent/hall/moderate', { token: admin.token, body: { action: 'kick', identity: V1.identity, minutes } });
    check('拒绝：kick minutes=' + minutes + ' 400 40060', r.status === 400 && r.code === 40060, r);
  }
  r = await api('POST', '/api/agent/hall/moderate', { token: V1.token, body: { action: 'mute_all' } });
  check('越权：访客令牌调用 moderate 401', r.status === 401, r);
  r = await api('POST', '/api/agent/hall/moderate', { token: admin.token, body: { action: 'mute', identity: V1.identity } });
  check('静音不在 LiveKit 中的访客：幂等成功 affected=0', r.code === 0 && r.data && r.data.affected === 0, r);
  r = await api('POST', '/api/agent/hall/moderate', { token: B.token, body: { action: 'mute_all' } });
  const ma = await waitFor(V1.events, (e) => e.type === 'hall_moderation' && e.extra && e.extra.action === 'mute_all');
  check('全员静音：成功并通知已订阅访客', r.code === 0 && !!ma, r);

  // ===== 14. 移出：join / 上传 / 消息 / 订阅 / 开麦全部被拒 =====
  const j2 = await api('POST', '/api/visitor/hall/join', { token: V2.token });
  V2.identity = j2.data && j2.data.identity;
  const s2sub = await subscribe(V2);
  check('访客乙进入并订阅', j2.code === 0 && s2sub && s2sub.type === 'hall_subscribed', s2sub);
  r = await api('POST', '/api/agent/hall/moderate', { token: admin.token, body: { action: 'kick', identity: V2.identity, minutes: 1 } });
  check('管理员移出访客乙 1 分钟（不在 LiveKit 时 affected=0；LiveKit 不可用时 50370）', (r.code === 0 && r.data && r.data.identity === V2.identity) || r.code === 50370, r);
  const kn = await waitFor(V2.events, (e) => e.type === 'hall_moderation' && e.extra && e.extra.action === 'kicked');
  check('被移出者收到 hall_moderation kicked（minutes=1）', kn && kn.extra.minutes === 1, kn && kn.extra);
  r = await api('POST', '/api/visitor/hall/join', { token: V2.token });
  check('移出后 join 403 40372 且带 retry_after', r.status === 403 && r.code === 40372 && r.data && r.data.retry_after > 0 && r.data.retry_after <= 60, r);
  r = await upload(V2.token, 'visitor', 'hall');
  check('移出后大厅上传 403 40372', r.status === 403 && r.code === 40372, r);
  r = await api('GET', '/api/visitor/hall/messages', { token: V2.token });
  check('移出后拉历史 403 40372', r.status === 403 && r.code === 40372, r);
  r = await api('POST', '/api/visitor/hall/mic', { token: V2.token, body: { on: true } });
  check('移出后开麦 403 40374', r.status === 403 && r.code === 40374, r);
  x = await hallSay(V2, '移出后发言' + run);
  await sleep(600);
  check('移出后发消息被拒且不广播', x.r && x.r.type === 'error' && /移出|进入大厅/.test(x.r.content || '') && !V1.events.some((e) => e.type === 'hall_chat' && e.id === x.id), x.r);
  const rs = await subscribe(V2);
  check('移出后重新订阅被拒', rs && rs.type === 'error' && /移出/.test(rs.content || ''), rs);

  // ===== 15. 删除消息：广播、幂等、不存在、越权 =====
  r = await api('POST', '/api/agent/hall/messages/' + m0.id + '/delete', { token: admin.token });
  const del1 = await waitFor(V1.events, (e) => e.type === 'hall_deleted' && e.id === m0.id);
  const del4 = await waitFor(V4.events, (e) => e.type === 'hall_deleted' && e.id === m0.id);
  check('客服删除大厅消息并广播 hall_deleted', r.code === 0 && !!del1 && !!del4, r);
  r = await api('POST', '/api/agent/hall/messages/' + m0.id + '/delete', { token: admin.token });
  await sleep(800);
  check('重复删除幂等成功且不重复广播', r.code === 0 && V4.events.filter((e) => e.type === 'hall_deleted' && e.id === m0.id).length === 1, r);
  r = await api('POST', '/api/agent/hall/messages/' + randomUUID() + '/delete', { token: admin.token });
  check('删除不存在的消息 404 40477', r.status === 404 && r.code === 40477, r);
  r = await api('POST', '/api/agent/hall/messages/' + encodeURIComponent("x' OR '1'='1") + '/delete', { token: admin.token });
  check('删除注入字符 id 被拒（4xx 非 500）', r.status >= 400 && r.status < 500, r);
  r = await api('POST', '/api/agent/hall/messages/' + dupId + '/delete', { token: V1.token });
  check('越权：访客令牌删除大厅消息 401', r.status === 401, r);
  r = await api('POST', '/api/agent/hall/messages/' + dupId + '/delete', { token: B.token });
  check('普通客服也可删除大厅消息', r.code === 0, r);
  const after = await api('GET', '/api/visitor/hall/messages', { token: V1.token });
  check('已删除消息对访客历史消失', after.code === 0 && !(after.data || []).some((m) => m.id === m0.id || m.id === dupId), null);

  // ===== 16. 客服 state =====
  r = await api('GET', '/api/agent/hall/state', { token: admin.token });
  const st = r.data || {};
  check('客服 state：settings 5 项、counts、participants、degraded', r.code === 0 && st.settings && ['enabled', 'video_enabled', 'max_speakers', 'max_cameras', 'max_participants'].every((k) => k in st.settings)
    && st.counts && 'participants' in st.counts && 'speakers' in st.counts && 'cameras' in st.counts && Array.isArray(st.participants) && typeof st.degraded === 'boolean', { ...st, participants: (st.participants || []).length });
  check('LiveKit 可用时 state 非降级', st.degraded === false, st.degraded);

  // ===== 17. LiveKit 回调：签名校验与「只允许内网直连」 =====
  const fakeIdentity = 'v_' + randomBytes(10).toString('hex');
  let body = hookBody('participant_left', fakeIdentity);
  r = await postHook(body, {});
  check('回调：未带签名 401 40120', r.status === 401 && r.code === 40120, r);
  r = await postHook(body, { Authorization: webhookAuth(body, { secret: randomBytes(32).toString('hex') }) });
  check('回调：伪造签名（错误密钥）401 40120', r.status === 401 && r.code === 40120, r);
  r = await postHook(body, { Authorization: webhookAuth(body, { alg: 'none' }) });
  check('回调：alg=none 401', r.status === 401, r);
  r = await postHook(body, { Authorization: webhookAuth(body, { iss: 'APIforged' + run }) });
  check('回调：iss 不是本服务 API Key 401', r.status === 401, r);
  r = await postHook(body, { Authorization: webhookAuth(body, { exp: nowSec() - 600 }) });
  check('回调：令牌已过期 401', r.status === 401, r);
  r = await postHook(body.replace('participant_left', 'participant_joined'), { Authorization: webhookAuth(body) });
  check('回调：请求体被篡改（sha256 不符）401', r.status === 401, r);
  r = await postHook(body, { Authorization: webhookAuth(body), 'X-Forwarded-For': '203.0.113.9' });
  check('回调：带 X-Forwarded-For（经反向代理）404', r.status === 404, r.status);
  r = await postHook(body, { Authorization: webhookAuth(body), 'X-Real-IP': '203.0.113.9' });
  check('回调：带 X-Real-IP（经反向代理）404', r.status === 404, r.status);
  r = await postHook(body, { Authorization: webhookAuth(body) });
  check('回调：正确签名 200 {code:0}', r.status === 200 && r.code === 0, r);
  r = await postHook(body, { Authorization: webhookAuth(body) });
  check('回调：同一事件重放 200（10 分钟内只处理一次）', r.status === 200 && r.code === 0, r);
  r = await postHook(body, { Authorization: 'Bearer ' + webhookAuth(body) });
  check('回调：兼容 Bearer 前缀', r.status === 200 && r.code === 0, r);

  // ===== 18. 关闭大厅：广播、join/上传/订阅/消息全部 40371，公开设置同步 =====
  r = await setHall(admin, { hall_enabled: 'false' });
  const off = await waitFor(V1.events, (e) => e.type === 'hall_settings' && e.extra && e.extra.enabled === false);
  check('关闭大厅：保存成功并广播 enabled=false', r.code === 0 && !!off, r);
  r = await api('GET', '/api/visitor/settings');
  check('关闭大厅后公开设置 hall_enabled=false', r.data && r.data.hall_enabled === false && r.data.hall_video_enabled === false, r.data && r.data.hall_enabled);
  r = await api('POST', '/api/visitor/hall/join', { token: V1.token });
  check('关闭大厅后访客 join 403 40371', r.status === 403 && r.code === 40371, r);
  r = await api('POST', '/api/agent/hall/join', { token: admin.token });
  check('关闭大厅后客服 join 403 40371', r.status === 403 && r.code === 40371, r);
  r = await upload(V1.token, 'visitor', 'hall');
  check('关闭大厅后大厅上传 403 40371', r.status === 403 && r.code === 40371, r);
  x = await hallSay(V1, '关闭后发言' + run);
  check('关闭大厅后发消息被拒', x.r && x.r.type === 'error', x.r);
  const so = await subscribe(V1);
  check('关闭大厅后订阅被拒', so && so.type === 'error' && /关闭/.test(so.content || ''), so);
  r = await api('GET', '/api/visitor/hall/messages', { token: V1.token });
  check('关闭大厅后拉历史 403 40371', r.status === 403 && r.code === 40371, r);
  r = await setHall(admin, { hall_enabled: 'true' });
  check('重新开启大厅', r.code === 0, r);

  // ===== 19. 隐私：访客收到的任何大厅帧都不含任何访客 vid =====
  const vids = actors.filter((a) => a.kind === 'visitor').map((a) => a.vid);
  const leak = actors.filter((a) => a.kind === 'visitor').some((a) => a.events.filter((e) => String(e.type || '').startsWith('hall_')).some((e) => vids.some((v) => JSON.stringify(e).includes(v))));
  check('访客收到的大厅帧不含任何 vid', !leak, null);
} catch (e) {
  check('测试流程异常中断', false, String((e && e.stack) || e).slice(0, 300));
} finally {
  // ===== 20. 清理 =====
  try {
    if (admin && saved && Object.keys(saved).length) {
      const rr = await setHall(admin, saved);
      check('恢复运行前的大厅设置', rr.code === 0, rr);
    }
    for (const vid of blockedVids) await api('POST', '/api/agent/visitor/' + vid + '/block', { token: admin.token, body: { blocked: false } });
    if (admin && B && createdB) await api('POST', '/api/admin/agents/active', { token: admin.token, body: { id: Number(B.id), active: false } });
    try { sql('DELETE FROM hall_messages WHERE content LIKE ' + q('%' + run + '%')); } catch {}
  } catch (e) { check('清理测试数据', false, String(e.message || e).slice(0, 200)); }
  for (const a of actors) { try { a.ws.close(1000); } catch {} }
}
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
