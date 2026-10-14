#!/usr/bin/env node
// 0.8.0 新功能接口端到端测试（只连本机隔离测试后端 127.0.0.1，含正常、拒绝与绕过类用例）。
// 用法（Mac 测试环境）：source secrets/test.env && node api_features_test.mjs
import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { writeFileSync, existsSync, readFileSync, statSync } from 'node:fs';

const BASE = process.env.CS_BASE || 'http://127.0.0.1:18080';
if (!/^http:\/\/127\.0\.0\.1:/.test(BASE)) { console.error('只允许连本机测试后端'); process.exit(2); }
const ADMIN_USER = process.env.QA_ADMIN_USER || 'qa_admin';
const ADMIN_PASS = process.env.ADMIN_BOOTSTRAP_PASSWORD;
const HOME = process.env.HOME;
const MYSQL = '/opt/homebrew/opt/mysql@8.4/bin/mysql';
const SOCK = HOME + '/cs-testenv/run/mysql.sock';
const UPLOADS = HOME + '/cs-testenv/app/uploads/';
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
async function api(method, path, opts = {}) {
  const headers = {};
  if (opts.token) headers.Authorization = 'Bearer ' + opts.token;
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  const r = await fetch(BASE + path, { method, headers, body: opts.body === undefined ? undefined : JSON.stringify(opts.body) });
  if (opts.raw) return r;
  const text = await r.text();
  let j; try { j = JSON.parse(text); } catch { j = { text }; }
  return { status: r.status, ...j };
}
function collect(ws) { const ev = []; ws.onmessage = (m) => { try { ev.push(JSON.parse(m.data)); } catch {} }; return ev; }
async function waitFor(events, pred, ms = 5000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { const f = events.find(pred); if (f) return f; await sleep(80); }
  return null;
}
function opened(ws) {
  return new Promise((res, rej) => { ws.onopen = () => res(); ws.onerror = () => rej(new Error('ws error')); setTimeout(() => rej(new Error('ws timeout')), 6000); });
}
async function visitor(name, vid = '') {
  const s = await api('POST', '/api/visitor/session', { body: { site_id: 'default', identifier: name, visitor_id: vid, last_page: 'https://xn--5kroo.com/product.html?id=9', ua: 'QA-API-Test/1.0' } });
  if (s.code !== 0) throw new Error('visitor session failed ' + JSON.stringify(s));
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/visitor', ['cs-auth', s.visitor_token]);
  const events = collect(ws);
  await opened(ws);
  await waitFor(events, (e) => e.type === 'hello');
  return {
    name, vid: s.visitor_id, conv: s.conversation, token: s.visitor_token, ws, events, session: s,
    send: (o) => ws.send(JSON.stringify({ ts: Date.now(), ...o })),
    async say(content) {
      const id = randomUUID();
      ws.send(JSON.stringify({ type: 'chat', id, content, ts: Date.now() }));
      const ack = await waitFor(events, (e) => e.type === 'ack' && (e.id === id || e.client_id === id));
      return { id: ack ? ack.id : id, ack };
    },
  };
}
async function agent(username, password) {
  const l = await api('POST', '/api/agent/login', { body: { username, password } });
  if (!l.token) throw new Error('agent login failed ' + JSON.stringify(l));
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/agent', ['cs-auth', l.token]);
  const events = collect(ws);
  await opened(ws);
  return { token: l.token, id: String(l.agent && l.agent.id), ws, events, send: (o) => ws.send(JSON.stringify({ ts: Date.now(), ...o })) };
}
async function agentSay(a, conv, content) {
  const id = randomUUID();
  a.send({ type: 'chat', id, conv, content });
  const ack = await waitFor(a.events, (e) => e.type === 'ack' && (e.id === id || e.client_id === id));
  return ack ? ack.id : null;
}
const listAll = (tok, q = '') => api('GET', '/api/agent/conversations?mode=all&limit=200' + q, { token: tok });
const inList = (r, vid) => !!(r.data || []).find((c) => c.visitor_id === vid);

const run = randomUUID().slice(0, 6);
const admin = await agent(ADMIN_USER, ADMIN_PASS);
const bName = 'qa_feat_' + run;
const bPass = 'Qf!' + randomUUID().slice(0, 8) + 'Zx9#';
let r = await api('POST', '/api/admin/agents', { token: admin.token, body: { username: bName, password: bPass, role: 'agent', nickname: '功能测试客服' } });
check('管理员创建普通客服 B', r.code === 0, r);
const B = await agent(bName, bPass);

const order = 'QAORD' + randomUUID().slice(0, 8).toUpperCase();
const V1 = await visitor('功能甲' + run);
const V2 = await visitor('功能乙' + run);
await V1.say('你好，订单号 ' + order + ' 请帮忙查一下');
await V2.say('乙的第一句话');

// ===== 1. 备注与标签 =====
const tag = 'T' + run;
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/profile', { token: admin.token, body: { note: '老客户\n喜欢夜间咨询', tags: [' VIP ', tag, tag, ''] } });
check('保存备注与标签', r.code === 0, r);
check('资料变更广播给客服', !!(await waitFor(admin.events, (e) => e.type === 'profile_updated' && e.extra && e.extra.visitor_id === V1.vid)));
r = await api('GET', '/api/agent/visitor/' + V1.vid + '/profile', { token: admin.token });
check('读取资料：标签去重去空白、备注保留换行', r.code === 0 && r.data.tags.join(',') === 'VIP,' + tag && r.data.note === '老客户\n喜欢夜间咨询', r);
r = await listAll(admin.token, '&tag=' + encodeURIComponent(tag));
check('按标签筛选列表与总数', r.code === 0 && r.data.length === 1 && r.total === 1 && r.data[0].visitor_id === V1.vid && r.data[0].tags.includes('VIP') && r.data[0].note.startsWith('老客户'), r);
r = await api('GET', '/api/agent/tags', { token: admin.token });
check('标签统计', r.code === 0 && r.data.some((t) => t.tag === tag && t.count === 1), r);
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/profile', { token: admin.token, body: { note: '', tags: ['<script>'] } });
check('拒绝：标签含尖括号', r.status === 400 && r.code === 40053, r);
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/profile', { token: admin.token, body: { note: '', tags: 'abcdefghijk'.split('') } });
check('拒绝：标签超过 10 个', r.status === 400 && r.code === 40053, r);
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/profile', { token: admin.token, body: { note: '字'.repeat(501), tags: [] } });
check('拒绝：备注超过 500 字', r.status === 400 && r.code === 40054, r);
r = await api('GET', '/api/agent/visitor/' + encodeURIComponent("x' OR 1=1 --") + '/profile', { token: admin.token });
check('拒绝：访客 ID 注入字符', r.status === 400 && r.code === 40050, r);
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/profile', { body: { note: 'x', tags: [] } });
check('拒绝：未登录修改资料', r.status === 401, r);
r = await listAll(admin.token, '&tag=' + encodeURIComponent('<b>'));
check('拒绝：非法标签筛选', r.status === 400, r);

// ===== 2. 置顶 / 标为未读 =====
r = await api('POST', '/api/agent/visitor/' + V2.vid + '/pin', { token: admin.token, body: { pinned: true } });
check('置顶客户', r.code === 0, r);
r = await listAll(admin.token);
check('置顶客户排在列表第一', r.code === 0 && r.data[0].visitor_id === V2.vid && r.data[0].pinned === true, r.data && r.data.slice(0, 2));
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/unread', { token: admin.token, body: { unread: true } });
check('标为未读', r.code === 0, r);
await api('POST', '/api/agent/conversations/' + V1.conv + '/read', { token: admin.token });
r = await listAll(admin.token);
let row = (r.data || []).find((c) => c.visitor_id === V1.vid);
check('打开会话后自动清除「标为未读」', row && row.marked_unread === false, row);
await api('POST', '/api/agent/visitor/' + V1.vid + '/unread', { token: admin.token, body: { unread: true } });
r = await listAll(admin.token);
row = (r.data || []).find((c) => c.visitor_id === V1.vid);
check('标为未读后列表至少 1 条未读', row && row.marked_unread === true && row.unread >= 1, row);

// ===== 3. 接管 + 正在输入 =====
r = await api('POST', '/api/agent/conversations/' + V1.conv + '/assign', { token: admin.token });
check('管理员接管甲', r.code === 0, r);
await api('POST', '/api/agent/conversations/' + V1.conv + '/read', { token: admin.token });
await sleep(1100);
V1.send({ type: 'typing' }); V1.send({ type: 'typing' }); V1.send({ type: 'typing', content: '偷偷塞内容', conv: 'forged-conv', from: 'agent:1' });
await sleep(900);
const typ = admin.events.filter((e) => e.type === 'typing' && e.from === 'visitor:' + V1.vid);
check('访客输入状态：1 秒内只转发 1 次、会话为服务器绑定值、不带内容', typ.length === 1 && typ[0].conv === V1.conv && !typ[0].content, typ);
check('未接管该会话的客服收不到输入状态', !B.events.some((e) => e.type === 'typing'), B.events.filter((e) => e.type === 'typing'));
admin.send({ type: 'typing' });
const vt = await waitFor(V1.events, (e) => e.type === 'typing' && String(e.from).startsWith('agent:'), 3000);
check('客服输入状态送达访客', !!vt, vt);

// ===== 4. 全局搜索 =====
r = await api('GET', '/api/agent/search?q=' + encodeURIComponent(order), { token: admin.token });
check('管理员按订单号搜索', r.code === 0 && r.data.length >= 1 && r.data[0].visitor_id === V1.vid && r.data[0].snippet.includes(order), r);
r = await api('GET', '/api/agent/search?q=' + encodeURIComponent(order), { token: B.token });
check('普通客服搜不到他人已接管会话', r.code === 0 && r.data.length === 0, r);
r = await api('GET', '/api/agent/search?q=a', { token: admin.token });
check('拒绝：搜索词少于 2 字', r.status === 400 && r.code === 40056, r);
r = await api('GET', '/api/agent/search?q=' + encodeURIComponent('%%'), { token: admin.token });
check('通配符按字面匹配', r.code === 0 && r.data.length === 0, r);
r = await api('GET', '/api/agent/search?q=' + encodeURIComponent("' OR '1'='1"), { token: admin.token });
check('注入字符串不生效', r.code === 0 && r.data.length === 0, r);

// ===== 5. 撤回 =====
const m1 = await agentSay(admin, V1.conv, '这条消息要撤回 ' + run);
check('客服发消息得到 ACK', !!m1, m1);
await waitFor(V1.events, (e) => e.type === 'chat' && e.id === m1);
r = await api('POST', '/api/agent/messages/' + m1 + '/recall', { token: B.token });
check('拒绝：撤回别人的消息', r.status === 403 && r.code === 40310, r);
r = await api('POST', '/api/agent/messages/' + m1 + '/recall', { token: admin.token });
check('撤回自己 2 分钟内的消息', r.code === 0, r);
check('访客实时收到撤回', !!(await waitFor(V1.events, (e) => e.type === 'recall' && e.id === m1)));
check('客服端实时收到撤回', !!(await waitFor(admin.events, (e) => e.type === 'recall' && e.id === m1)));
r = await api('POST', '/api/agent/messages/' + m1 + '/recall', { token: admin.token });
check('拒绝：重复撤回', r.status === 409 && r.code === 40911, r);
r = await api('GET', '/api/agent/conversations/' + V1.conv + '/messages?limit=50', { token: admin.token });
const rec = (r.data || []).find((m) => m.id === m1);
check('历史接口：已撤回消息不返回原文', rec && rec.recalled === true && rec.content === '', rec);
const s2 = await api('POST', '/api/visitor/session', { body: { site_id: 'default', identifier: V1.name, visitor_id: V1.vid, ua: 'QA-API-Test/1.0' } });
check('访客会话接口返回 recalled_ids', s2.code === 0 && (s2.recalled_ids || []).includes(m1), s2.recalled_ids);
const m2 = await agentSay(admin, V1.conv, '这条超时 ' + run);
sql("UPDATE messages SET created_at = created_at - INTERVAL 3 MINUTE WHERE id='" + m2 + "'");
r = await api('POST', '/api/agent/messages/' + m2 + '/recall', { token: admin.token });
check('拒绝：超过 2 分钟撤回', r.status === 409 && r.code === 40910, r);
r = await api('POST', '/api/agent/messages/' + randomUUID() + '/recall', { token: admin.token });
check('拒绝：撤回不存在的消息', r.status === 404 && r.code === 40410, r);
r = await api('POST', '/api/agent/messages/' + encodeURIComponent('a b') + '/recall', { token: admin.token });
check('拒绝：非法消息 ID', r.status === 400, r);

// ===== 6. 快捷回复 =====
r = await api('POST', '/api/agent/quick-replies', { token: B.token, body: { scope: 'personal', title: '付款方式', content: '支持支付宝和微信' } });
check('客服创建个人话术', r.code === 0 && r.data.id > 0, r);
const qB = r.data && r.data.id;
r = await api('POST', '/api/agent/quick-replies', { token: B.token, body: { scope: 'team', title: 'x', content: 'y' } });
check('拒绝：普通客服创建团队话术', r.status === 403 && r.code === 40312, r);
r = await api('POST', '/api/agent/quick-replies', { token: admin.token, body: { scope: 'team', title: '发货', content: '下单后自动发货到邮箱', sort_order: 1 } });
check('管理员创建团队话术', r.code === 0, r);
const qT = r.data && r.data.id;
r = await api('GET', '/api/agent/quick-replies', { token: B.token });
const bl = r.data || [];
check('客服看到团队话术 + 自己的话术（团队在前）', bl.some((q) => q.id === qT && q.scope === 'team') && bl.some((q) => q.id === qB && q.scope === 'personal') && bl.findIndex((q) => q.id === qT) < bl.findIndex((q) => q.id === qB), bl);
r = await api('GET', '/api/agent/quick-replies', { token: admin.token });
check('看不到别人的个人话术', !(r.data || []).some((q) => q.id === qB), r);
r = await api('POST', '/api/agent/quick-replies/' + qT + '/update', { token: B.token, body: { title: '改', content: '改' } });
check('拒绝：普通客服修改团队话术', r.status === 403 && r.code === 40312, r);
r = await api('POST', '/api/agent/quick-replies/' + qB + '/update', { token: admin.token, body: { title: '改', content: '改' } });
check('拒绝：修改别人的个人话术（按不存在处理）', r.status === 404 && r.code === 40411, r);
r = await api('POST', '/api/agent/quick-replies/' + qB + '/update', { token: B.token, body: { title: '付款', content: '   ' } });
check('拒绝：话术内容为空', r.status === 400 && r.code === 40055, r);
r = await api('POST', '/api/agent/quick-replies', { token: B.token, body: { scope: 'other', title: 'x', content: 'y' } });
check('拒绝：未知话术范围', r.status === 400, r);
r = await api('POST', '/api/agent/quick-replies/' + qB + '/update', { token: B.token, body: { title: '付款', content: '支持支付宝、微信、USDT', sort_order: 3 } });
check('修改自己的话术', r.code === 0, r);
r = await api('POST', '/api/agent/quick-replies/' + qB + '/delete', { token: B.token });
const r2 = await api('POST', '/api/agent/quick-replies/' + qT + '/delete', { token: admin.token });
check('删除话术', r.code === 0 && r2.code === 0, [r, r2]);

// ===== 7. 导出 =====
let res = await api('GET', '/api/agent/visitor/' + V1.vid + '/export?format=xlsx', { token: admin.token, raw: true });
const xbuf = Buffer.from(await res.arrayBuffer());
writeFileSync('/tmp/cs_export_test.xlsx', xbuf);
let sheet = '';
try { sheet = execFileSync('unzip', ['-p', '/tmp/cs_export_test.xlsx', 'xl/worksheets/sheet1.xml'], { encoding: 'utf8' }); } catch (e) { sheet = 'unzip failed ' + e.message; }
check('导出 xlsx：可解压且包含订单号与「[已撤回]」', res.status === 200 && (res.headers.get('content-type') || '').includes('spreadsheetml') && xbuf.slice(0, 2).toString() === 'PK' && sheet.includes(order) && sheet.includes('[已撤回]') && !sheet.includes('这条消息要撤回'), { status: res.status, ct: res.headers.get('content-type'), len: xbuf.length });
check('导出文件名为 UTF-8 编码', /filename\*=UTF-8''/.test(res.headers.get('content-disposition') || ''), res.headers.get('content-disposition'));
res = await api('GET', '/api/agent/visitor/' + V1.vid + '/export?format=txt', { token: admin.token, raw: true });
const txt = await res.text();
check('导出 txt：北京时间、发送方、撤回占位', res.status === 200 && txt.includes('北京时间') && txt.includes('访客：') && txt.includes(order) && txt.includes('[已撤回]'), txt.slice(0, 300));
r = await api('GET', '/api/agent/visitor/' + V1.vid + '/export?format=pdf', { token: admin.token });
check('拒绝：不支持的导出格式', r.status === 400 && r.code === 40057, r);
r = await api('GET', '/api/agent/visitor/' + V1.vid + '/export?format=xlsx', { token: B.token });
check('拒绝：导出他人已接管客户', r.status === 403 && r.code === 40311, r);
r = await api('POST', '/api/agent/export', { token: admin.token, body: { visitor_ids: Array.from({ length: 51 }, () => randomUUID()), format: 'xlsx' } });
check('拒绝：一次导出超过 50 位', r.status === 400 && r.code === 40051, r);
res = await api('POST', '/api/agent/export', { token: admin.token, raw: true, body: { visitor_ids: [V1.vid, V2.vid], format: 'txt' } });
const mtxt = await res.text();
check('批量导出多位客户', res.status === 200 && mtxt.includes('客户：功能甲') && mtxt.includes('客户：功能乙'), mtxt.slice(0, 200));

// ===== 8. 拉黑 =====
const V3 = await visitor('功能丙' + run);
await V3.say('丙的第一句');
r = await api('POST', '/api/agent/visitor/' + V3.vid + '/block', { token: B.token, body: { blocked: true, reason: '发广告', block_ip: false } });
check('客服拉黑访客', r.code === 0, r);
r = await listAll(admin.token);
check('拉黑后不在会话列表', r.code === 0 && !inList(r, V3.vid), null);
r = await api('GET', '/api/agent/blocked', { token: admin.token });
check('黑名单可见、含原因与 IP', r.code === 0 && (r.data || []).some((b) => b.visitor_id === V3.vid && b.reason === '发广告' && b.ip === '127.0.0.1'), r);
let mark = admin.events.length;
const blockedSay = await V3.say('我被拉黑了吗');
await sleep(800);
check('已拉黑访客仍收到 ACK（无感知）', !!blockedSay.ack, blockedSay);
check('已拉黑访客的消息不推送给客服', !admin.events.slice(mark).some((e) => e.type === 'chat' && e.from === 'visitor:' + V3.vid), admin.events.slice(mark).map((e) => e.type));
r = await api('POST', '/api/agent/visitor/' + V1.vid + '/block', { token: B.token, body: { blocked: true } });
check('拒绝：普通客服拉黑他人已接管客户', r.status === 403 && r.code === 40311, r);
r = await api('POST', '/api/agent/visitor/' + V3.vid + '/block', { token: admin.token, body: { blocked: false } });
check('解除拉黑', r.code === 0, r);
mark = admin.events.length;
await V3.say('解除后的消息');
check('解除后客服重新收到消息', !!(await waitFor(admin.events, (e) => admin.events.indexOf(e) >= mark && e.type === 'chat' && e.from === 'visitor:' + V3.vid && e.content === '解除后的消息', 3000)), null);
r = await listAll(admin.token);
check('解除后回到会话列表', inList(r, V3.vid), null);
// IP 拉黑：之后从同一 IP 打开的访客自动继承；解除源头后一并解除
r = await api('POST', '/api/agent/visitor/' + V3.vid + '/block', { token: admin.token, body: { blocked: true, reason: '骚扰', block_ip: true } });
check('拉黑并拉黑 IP', r.code === 0, r);
const V4 = await visitor('功能丁' + run);
r = await api('GET', '/api/agent/visitor/' + V4.vid + '/profile', { token: admin.token });
check('同 IP 新访客自动继承拉黑', r.code === 0 && r.data.blocked === true && r.data.block_reason === '同 IP 已被拉黑', r.data);
r = await api('POST', '/api/agent/visitor/' + V3.vid + '/block', { token: admin.token, body: { blocked: false } });
r = await api('GET', '/api/agent/visitor/' + V4.vid + '/profile', { token: admin.token });
check('解除源头拉黑后继承者一并解除', r.code === 0 && r.data.blocked === false, r.data);
const ipRows = sql("SELECT COUNT(*) FROM blocked_ips");
check('IP 黑名单已清理', ipRows === '0', ipRows);

// ===== 9. 删除 / 回收站 =====
await V2.say('乙要被删除的消息');
const up = new FormData();
up.append('uploader', 'visitor');
up.append('file', new Blob([readFileSync(HOME + '/cs-testenv/fixtures/product.png')]), 'v2.png');
const upr = await (await fetch(BASE + '/api/upload', { method: 'POST', headers: { Authorization: 'Bearer ' + V2.token }, body: up })).json();
check('乙上传图片', upr.code === 0, upr);
if (upr.code === 0) V2.send({ type: 'chat', id: randomUUID(), content: '', media: upr.url, mkind: upr.kind, mname: upr.name, msize: upr.size });
await sleep(600);
const v2File = sql("SELECT f.store_key FROM files f JOIN conversations c ON c.id=f.conv_id WHERE c.visitor_id='" + V2.vid + "' LIMIT 1");
r = await api('POST', '/api/agent/visitors/delete', { token: B.token, body: { visitor_ids: [V2.vid, V1.vid] } });
check('多选删除：可删的删除、他人已接管的跳过', r.code === 0 && r.data.visitors === 1 && r.data.skipped === 1 && r.data.messages >= 2, r);
check('在线访客收到清空通知', !!(await waitFor(V2.events, (e) => e.type === 'history_cleared')));
check('所有客服收到删除广播', !!(await waitFor(admin.events, (e) => e.type === 'conversations_deleted' && e.extra && (e.extra.visitor_ids || []).includes(V2.vid))));
r = await listAll(admin.token);
check('删除后不在列表（不会以空会话立即回来）', !inList(r, V2.vid), null);
r = await api('GET', '/api/agent/visitor/' + V2.vid + '/messages', { token: admin.token });
check('删除后聊天记录不可读', r.status === 403, r);
r = await api('GET', '/api/agent/search?q=' + encodeURIComponent('乙要被删除'), { token: admin.token });
check('删除后搜索不到', r.code === 0 && r.data.length === 0, r);
const sv2 = await api('POST', '/api/visitor/session', { body: { site_id: 'default', identifier: V2.name, visitor_id: V2.vid, ua: 'QA-API-Test/1.0' } });
check('访客会话接口返回 history_reset_at', sv2.code === 0 && sv2.history_reset_at > Date.now() - 60000, sv2.history_reset_at);
r = await api('GET', '/api/admin/recycle-bin', { token: B.token });
check('拒绝：普通客服访问回收站', r.status === 403, r);
r = await api('GET', '/api/admin/recycle-bin?limit=200', { token: admin.token });
const binItem = (r.data || []).find((i) => i.visitor_id === V2.vid);
check('回收站可见、7 天后到期、显示操作人', binItem && binItem.deleted_by === bName && Math.abs(new Date(binItem.purge_at) - new Date(binItem.deleted_at) - 7 * 86400000) < 5000 && binItem.messages >= 2, binItem);
r = await api('POST', '/api/admin/recycle-bin/restore', { token: admin.token, body: { visitor_ids: [V2.vid] } });
check('从回收站恢复', r.code === 0 && r.data.conversations >= 1, r);
r = await listAll(admin.token);
check('恢复后回到列表', inList(r, V2.vid), null);
await api('POST', '/api/agent/visitors/delete', { token: admin.token, body: { visitor_ids: [V2.vid] } });
r = await api('POST', '/api/admin/recycle-bin/purge', { token: admin.token, body: { all: true, confirm: '清空' } });
check('拒绝：清空回收站确认文字错误', r.status === 400 && r.code === 40052, r);
r = await api('POST', '/api/admin/recycle-bin/purge', { token: admin.token, body: { visitor_ids: [V2.vid] } });
check('彻底删除指定客户', r.code === 0 && r.data.conversations >= 1 && r.data.messages >= 2, r);
check('彻底删除后数据库无残留', sql("SELECT COUNT(*) FROM conversations WHERE visitor_id='" + V2.vid + "'") === '0', null);
check('彻底删除同时删除上传文件', v2File !== '' && !existsSync(UPLOADS + v2File), v2File);

// 删除全部 + 一次性全部恢复（不破坏后续 UI 测试数据）
// [115] 只恢复本次「删除全部」移入回收站的客户：删除前就整段在回收站里的客户（如批量造数的回收站数据）保持原状，
//   按 SQL 直接取 ID 分批恢复（不依赖回收站分页顺序，万级客户也不会漏恢复）。
const preBin = new Set(sql("SELECT DISTINCT visitor_id FROM conversations WHERE deleted_at IS NOT NULL AND visitor_id NOT IN (SELECT visitor_id FROM conversations WHERE deleted_at IS NULL)").split('\n').filter(Boolean));
r = await api('POST', '/api/admin/visitors/delete-all', { token: admin.token, body: { confirm: '删除' } });
check('拒绝：删除全部确认文字错误', r.status === 400 && r.code === 40052, r);
r = await api('POST', '/api/admin/visitors/delete-all', { token: B.token, body: { confirm: '删除全部' } });
check('拒绝：普通客服删除全部', r.status === 403, r);
const beforeTotal = (await listAll(admin.token)).total;
r = await api('POST', '/api/admin/visitors/delete-all', { token: admin.token, body: { confirm: '删除全部' } });
check('删除全部', r.code === 0 && r.data.visitors >= beforeTotal, { r, beforeTotal });
r = await listAll(admin.token);
check('删除全部后列表为空', r.code === 0 && r.total === 0 && r.data.length === 0, r.total);
check('删除全部广播 all', !!(await waitFor(admin.events, (e) => e.type === 'conversations_deleted' && e.extra && e.extra.all === true)));
let restored = 0;
const toRestore = sql('SELECT DISTINCT visitor_id FROM conversations WHERE deleted_at IS NOT NULL').split('\n').filter((id) => id && !preBin.has(id));
for (let i = 0; i < toRestore.length; i += 200) {
  let rr;
  // 恢复接口按「账号+IP」限流 30 次/分钟（安全机制，不绕过）：遇 429 等窗口过去后重试同一批，最多 3 次
  for (let attempt = 0; attempt < 4; attempt++) {
    rr = await api('POST', '/api/admin/recycle-bin/restore', { token: admin.token, body: { visitor_ids: toRestore.slice(i, i + 200) } });
    if (rr.status !== 429) break;
    await sleep(61000);
  }
  restored += rr.data ? rr.data.visitors : 0;
}
r = await listAll(admin.token);
check('全部恢复后数量一致', r.total === beforeTotal && restored >= beforeTotal, { total: r.total, beforeTotal, restored });

// ===== 10. 自动清理设置 =====
r = await api('POST', '/api/admin/settings', { token: admin.token, body: { auto_clean_days: '5' } });
check('拒绝：自动清理天数 < 7', r.status === 400 && r.code === 40058, r);
r = await api('POST', '/api/admin/settings', { token: admin.token, body: { auto_clean_days: '30' } });
check('保存自动清理天数', r.code === 0, r);
r = await api('GET', '/api/admin/settings', { token: admin.token });
check('读取自动清理天数', r.code === 0 && JSON.stringify(r).includes('"auto_clean_days":"30"'), null);
await api('POST', '/api/admin/settings', { token: admin.token, body: { auto_clean_days: '0' } });

// ===== 11. [115] 已读权限：能查看聊天记录即可标记已读；写操作归属不变 =====
const SECLOG = HOME + '/cs-testenv/app/logs/security.log';
const secCount = () => { try { return (readFileSync(SECLOG, 'utf8').match(/http_read_receipt_forbidden/g) || []).length; } catch { return -1; } };
const V5 = await visitor('已读权限甲' + run);
await V5.say('甲：他人接管但我能查看');
await sleep(300);
// 构造生产场景：访客另有一段未分配的历史会话（B 可查看该访客记录），当前会话已被管理员接管（agent_id=管理员）
sql("INSERT INTO conversations(id, site_id, visitor_id, agent_id, status, unread_visitor, unread_agent, started_at, updated_at, closed_at) VALUES('" + randomUUID() + "', 'default', '" + V5.vid + "', NULL, 'closed', 0, 0, NOW() - INTERVAL 2 DAY, NOW() - INTERVAL 2 DAY, NOW() - INTERVAL 2 DAY)");
sql("UPDATE conversations SET agent_id=" + Number(admin.id) + ", unread_agent=3 WHERE id='" + V5.conv + "'");
r = await api('GET', '/api/agent/visitor/' + V5.vid + '/messages', { token: B.token });
check('前提：普通客服能查看该访客聊天记录', r.status === 200 && r.code === 0, r);
r = await api('POST', '/api/agent/conversations/' + V5.conv + '/assign', { token: B.token });
check('写操作不变：普通客服仍不能接管他人会话', r.status === 403 && r.code === 40302, r);
const bMark = B.events.length;
const bSend = await agentSay(B, V5.conv, '越权发送 ' + run);
check('写操作不变：普通客服不能向他人会话发消息', !bSend && !!(await waitFor(B.events, (e) => B.events.indexOf(e) >= bMark && e.type === 'error', 3000)), bSend);
r = await api('POST', '/api/agent/conversations/' + V5.conv + '/read', { token: B.token });
check('普通客服对「他人接管但自己可查看」的会话标记已读成功', r.status === 200 && r.code === 0, r);
check('标记已读后未读清零、读游标已写入', sql("SELECT CONCAT(unread_agent, '|', last_read_agent_at IS NOT NULL, '|', agent_id) FROM conversations WHERE id='" + V5.conv + "'") === '0|1|' + admin.id, null);
r = await api('GET', '/api/agent/conversations?mode=all&limit=50', { token: B.token });
row = (r.data || []).find((c) => c.visitor_id === V5.vid);
check('列表中该客户未读为 0（重启/重新拉取后不再回来）', row && row.unread === 0 && row.id === V5.conv, row);

const V6 = await visitor('已读权限乙' + run);
await V6.say('乙：完全无权');
await sleep(300);
sql("UPDATE conversations SET agent_id=" + Number(admin.id) + ", unread_agent=2 WHERE visitor_id='" + V6.vid + "'");
const sec0 = secCount();
r = await api('POST', '/api/agent/conversations/' + V6.conv + '/read', { token: B.token });
check('拒绝：普通客服对完全无权的会话标记已读', r.status === 403 && r.code === 40302, r);
check('拒绝后未读保持不变', sql("SELECT unread_agent FROM conversations WHERE id='" + V6.conv + "'") === '2', null);
r = await api('GET', '/api/agent/visitor/' + V6.vid + '/messages', { token: B.token });
check('口径一致：同一客服也查看不到该访客记录', r.status === 403, r);
r = await api('POST', '/api/agent/conversations/' + V6.conv + '/read', { token: admin.token });
check('管理员可标记任意未删除会话已读', r.code === 0 && sql("SELECT unread_agent FROM conversations WHERE id='" + V6.conv + "'") === '0', r);

const V7 = await visitor('已读权限丙' + run);
await V7.say('丙：会话将被删除');
await sleep(300);
r = await api('POST', '/api/agent/visitors/delete', { token: admin.token, body: { visitor_ids: [V7.vid] } });
r = await api('POST', '/api/agent/conversations/' + V7.conv + '/read', { token: B.token });
check('拒绝：普通客服标记已删除（回收站）会话', r.status === 403 && r.code === 40302, r);
r = await api('POST', '/api/agent/conversations/' + V7.conv + '/read', { token: admin.token });
check('拒绝：管理员也不能标记回收站会话', r.status === 403 && r.code === 40302, r);
r = await api('POST', '/api/agent/conversations/' + randomUUID() + '/read', { token: B.token });
check('拒绝：不存在的会话', r.status === 403 && r.code === 40302, r);
r = await api('POST', '/api/agent/conversations/' + encodeURIComponent("x' OR '1'='1") + '/read', { token: B.token });
check('拒绝：注入字符会话 ID（403 而非 500）', r.status === 403 && r.code === 40302, r);
r = await api('POST', '/api/agent/conversations/' + V5.conv + '/read');
check('拒绝：未登录标记已读', r.status === 401, r);
await sleep(500);
const sec1 = secCount();
check('越权标记已读写入安全日志（≥5 条）', sec0 >= 0 && sec1 - sec0 >= 5, { sec0, sec1 });

// ===== 12. 清理测试数据 =====
await api('POST', '/api/agent/visitors/delete', { token: admin.token, body: { visitor_ids: [V1.vid, V3.vid, V4.vid, V5.vid, V6.vid] } });
await api('POST', '/api/admin/recycle-bin/purge', { token: admin.token, body: { visitor_ids: [V1.vid, V3.vid, V4.vid, V5.vid, V6.vid, V7.vid] } });
await api('POST', '/api/admin/agents/active', { token: admin.token, body: { id: Number(B.id), active: false } });
for (const x of [V1, V2, V3, V4, V5, V6, V7, admin, B]) { try { x.ws.close(1000); } catch {} }
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
