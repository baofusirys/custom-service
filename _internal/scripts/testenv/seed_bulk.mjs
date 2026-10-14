#!/usr/bin/env node
// [115] 会话列表性能测试造数（只写 Mac 隔离测试库 cs_test，经本机 unix socket 连接，不连任何远端）。
// 规模对齐生产：≥9000 访客、≥18000 会话、≥50000 消息；含 open/closed、visitor_engaged、置顶、拉黑、标签、回收站、
// 标为未读、撤回、图片/文件、长文本、末条为页面浏览、多段 open、「旧 closed 会话更新晚于 open 会话」、同秒并列等边界。
// 用固定种子伪随机，重复执行结果一致；造数行 ID 前缀固定（b0000000- / c0000000- / e0000000-），可整体清理。
// 用法（Mac 测试环境）：cd ~/cs-testenv && set -a; source secrets/test.env; set +a; node seed_bulk.mjs [--reset] [--purge]
//   --reset 先删除上次造的批量数据再重造；--purge 只删除批量数据。不加参数时若已存在批量数据则拒绝重复造。
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';

const HOME = process.env.HOME || '';
const MYSQL = '/opt/homebrew/opt/mysql@8.4/bin/mysql';
const SOCK = HOME + '/cs-testenv/run/mysql.sock';
const DB = 'cs_test';
if (!HOME.startsWith('/Users/') || !existsSync(SOCK)) { console.error('只允许在 Mac 隔离测试环境运行（需要 ~/cs-testenv/run/mysql.sock）'); process.exit(2); }
if (!process.env.MYSQL_ROOT_PASSWORD) { console.error('缺少 MYSQL_ROOT_PASSWORD：先 set -a; source secrets/test.env; set +a'); process.exit(2); }

const VISITORS = 9500;
const HEAVY_CONVS = 30;          // 长对话（每段 400 条消息），检验预览 LATERAL 只取 1 条
const HEAVY_MSGS = 400;
const VP = 'b0000000-0000-4000-8000-';
const CP = 'c0000000-0000-4000-8000-';
const MP = 'e0000000-0000-4000-8000-';
const hex12 = (n) => n.toString(16).padStart(12, '0');

function mysql(sql, extra = []) {
  return execFileSync(MYSQL, ['--socket=' + SOCK, '-uroot', '-N', '--batch', ...extra, DB],
    { input: sql, env: { ...process.env, MYSQL_PWD: process.env.MYSQL_ROOT_PASSWORD }, encoding: 'utf8', maxBuffer: 64 << 20 }).trim();
}
const q = (s) => s === null || s === undefined ? 'NULL' : "'" + String(s).replace(/\\/g, '\\\\').replace(/'/g, "''") + "'";

function purge() {
  const before = mysql(`SELECT (SELECT COUNT(*) FROM visitors WHERE id LIKE '${VP}%'), (SELECT COUNT(*) FROM conversations WHERE id LIKE '${CP}%'), (SELECT COUNT(*) FROM messages WHERE id LIKE '${MP}%');`);
  mysql(`
DELETE FROM messages WHERE id LIKE '${MP}%';
DELETE FROM messages WHERE conv_id LIKE '${CP}%';
DELETE FROM files WHERE conv_id LIKE '${CP}%';
DELETE FROM conversations WHERE id LIKE '${CP}%';
DELETE FROM visitor_tags WHERE visitor_id LIKE '${VP}%';
DELETE FROM visitor_profiles WHERE visitor_id LIKE '${VP}%';
DELETE FROM blocked_ips WHERE visitor_id LIKE '${VP}%';
DELETE FROM visitors WHERE id LIKE '${VP}%';`);
  console.log('purged bulk rows (visitors, conversations, messages) = ' + before.replace(/\t/g, ', '));
}

const args = process.argv.slice(2);
if (args.includes('--purge')) { purge(); process.exit(0); }
const existing = Number(mysql(`SELECT COUNT(*) FROM visitors WHERE id LIKE '${VP}%';`));
if (existing > 0) {
  if (!args.includes('--reset')) { console.error('已存在 ' + existing + ' 个批量访客；用 --reset 重造或 --purge 清理'); process.exit(3); }
  purge();
}

// 固定种子伪随机（mulberry32）
let seed = 20261014;
const rnd = () => { seed |= 0; seed = (seed + 0x6D2B79F5) | 0; let t = Math.imul(seed ^ (seed >>> 15), 1 | seed); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
const ri = (a, b) => a + Math.floor(rnd() * (b - a + 1));
const pick = (arr) => arr[Math.floor(rnd() * arr.length)];

// 北京时间格式化（数据库连接 time_zone=+08:00，按北京时间字面量写入）
const fmt = (ms, frac = false) => {
  const d = new Date(ms + 8 * 3600 * 1000);
  const p = (n, w = 2) => String(n).padStart(w, '0');
  const s = `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`;
  return frac ? s + '.' + p(d.getUTCMilliseconds(), 3) : s;
};
const NOW = Math.floor(Date.now() / 1000) * 1000 - 3600 * 1000; // 一小时前为上限，避免与实时测试数据抢「最新」
const SPAN = 60 * 86400 * 1000;

const agents = mysql('SELECT id FROM agents WHERE active=1 ORDER BY id LIMIT 20;').split('\n').filter(Boolean).map(Number);
if (!agents.length) { console.error('测试库没有客服账号'); process.exit(4); }

const visitors = [], convs = [], msgs = [], profiles = new Map(), tags = [];
const cities = [['中国', '北京'], ['中国', '上海'], ['中国', '深圳'], ['中国', '成都'], ['美国', '洛杉矶'], ['', '']];
let convSeq = 0, msgSeq = 0, tiePool = [];
const engagedVisitors = new Set();

function addMsg(convId, sender, senderRef, content, createdMs, extra = {}) {
  msgs.push({ id: MP + hex12(++msgSeq), conv_id: convId, sender, sender_ref: senderRef, content, created: fmt(createdMs, true),
    media_url: extra.media_url || null, media_kind: extra.media_kind || null, media_name: extra.media_name || null,
    media_size: extra.media_size || null, recalled_at: extra.recalled ? fmt(createdMs + 30000, true) : null, recalled_by: extra.recalled ? 'qa_admin' : '' });
}

function genMessages(c, vid, engaged, heavy) {
  // 返回最后一条消息时间
  let t = c.startMs;
  const step = () => (t += (heavy ? ri(2, 20) : ri(2, 90)) * 1000);
  if (rnd() < 0.7) for (let k = ri(1, 3); k > 0; k--) addMsg(c.id, 'sys', 'page:https://xn--5kroo.com/item?id=' + ri(1, 999), '商品页 ' + ri(1, 999), step());
  if (engaged) {
    const n = heavy ? HEAVY_MSGS : ri(2, 6);
    for (let k = 0; k < n; k++) {
      const r = rnd();
      if (r < 0.40) addMsg(c.id, 'visitor', vid, '你好，我想咨询订单 QA' + ri(10000, 99999) + ' 的发货进度', step());
      else if (r < 0.55) addMsg(c.id, 'agent', String(pick(agents)), '您好，已为您查询，稍后回复', step());
      else if (r < 0.63) addMsg(c.id, 'visitor', vid, '', step(), { media_url: '/api/files/bulk-' + ri(1, 1e6) + '.png', media_kind: 'image', media_name: 'shot.png', media_size: ri(1e4, 9e5) });
      else if (r < 0.68) addMsg(c.id, 'agent', String(pick(agents)), '', step(), { media_url: '/api/files/bulk-' + ri(1, 1e6) + '.pdf', media_kind: 'file', media_name: '说明.pdf', media_size: ri(1e4, 9e5) });
      else if (r < 0.80) addMsg(c.id, 'visitor', vid, '这是一段很长的咨询内容，用来检验列表预览超过五十个字时会被截断并加省略号，'.repeat(ri(1, 3)) + ri(1, 9999), step());
      else if (r < 0.86) addMsg(c.id, 'agent', String(pick(agents)), '这条消息已被撤回 ' + ri(1, 9999), step(), { recalled: true });
      else if (r < 0.90) addMsg(c.id, 'sys', 'voice:missed', '语音通话 未接通', step());
      else addMsg(c.id, 'sys', 'page:https://xn--5kroo.com/pay?id=' + ri(1, 999), '支付页', step()); // 末条可能是页面浏览（预览须跳过）
    }
  }
  return t;
}

for (let i = 1; i <= VISITORS; i++) {
  const vid = VP + hex12(i);
  const [country, city] = pick(cities);
  const r = rnd();
  const k = r < 0.40 ? 1 : r < 0.75 ? 2 : r < 0.90 ? 3 : 4;
  const engagedVisitor = rnd() < 0.25;
  // 会话时间段：按时间先后排列
  const LATEST_START = NOW - 6 * 3600 * 1000; // 会话最晚开始时间：给消息留足时间，任何时间都不晚于 NOW
  let t = LATEST_START - Math.floor(rnd() * SPAN);
  const firstSeen = t;
  const vconvs = [];
  for (let j = 0; j < k; j++) {
    const c = { id: CP + hex12(++convSeq), vid, startMs: t, engaged: engagedVisitor && (j === k - 1 || rnd() < 0.4) };
    const heavy = convSeq <= HEAVY_CONVS * 300 && convSeq % 300 === 0; // 均匀分布的长对话
    if (heavy) c.engaged = true;
    const last = genMessages(c, vid, c.engaged, heavy);
    c.updMs = Math.min(NOW, Math.max(last, t + ri(5, 600) * 1000));
    c.status = 'closed';
    vconvs.push(c);
    t = Math.min(LATEST_START, c.updMs + ri(31 * 60, 5 * 86400) * 1000);
  }
  const lastC = vconvs[vconvs.length - 1];
  if (rnd() < 0.88) lastC.status = 'open';
  if (k >= 2 && rnd() < 0.03) vconvs[k - 2].status = 'open';            // 多段 open（规则：仍取最新 open）
  if (k >= 2 && lastC.status === 'open' && rnd() < 0.05) {              // 旧 closed 会话更新晚于 open 会话（排序键必须取 open 会话）
    const old = vconvs[k - 2];
    if (old.status === 'closed') old.updMs = Math.min(NOW, lastC.updMs + ri(60, 3600) * 1000);
  }
  if (rnd() < 0.02 && tiePool.length) lastC.updMs = pick(tiePool);      // 与其他访客同秒并列
  if (rnd() < 0.05) tiePool.push(lastC.updMs);
  for (const c of vconvs) {
    const agent = rnd() < 0.5 ? null : pick(agents);
    convs.push({ id: c.id, vid, agent, status: c.status, unread: c.status === 'open' && c.engaged ? ri(0, 3) : 0,
      started: fmt(c.startMs), updated: fmt(c.updMs), closed: c.status === 'closed' ? fmt(c.updMs) : null, engaged: c.engaged ? 1 : 0, replied: c.engaged && rnd() < 0.6 ? 1 : 0 });
    if (c.engaged) engagedVisitors.add(vid);
  }
  visitors.push({ id: vid, ident: rnd() < 0.3 ? '压测客户' + i : '', country, city, page: 'https://xn--5kroo.com/item?id=' + ri(1, 999),
    ref: rnd() < 0.5 ? 'https://www.baidu.com/s?wd=' + ri(1, 999) : '', first: fmt(firstSeen), last: fmt(lastC.updMs), ua: 'Mozilla/5.0 (bulk-seed) Safari/605.1.15' });
}

// 资料：置顶 30、拉黑 40、备注 200、标为未读 50（互不重叠的访客段，便于核对）
const prof = (vid) => { if (!profiles.has(vid)) profiles.set(vid, { note: '', pinned: null, marked: 0, blocked: null }); return profiles.get(vid); };
const ago = (days) => fmt(NOW - Math.floor(rnd() * days * 86400 * 1000), true);
for (let i = 1; i <= 30; i++) prof(VP + hex12(i * 97)).pinned = ago(10);
for (let i = 1; i <= 40; i++) prof(VP + hex12(i * 89 + 5)).blocked = ago(20);
for (let i = 1; i <= 200; i++) prof(VP + hex12(i * 31 + 7)).note = '压测备注 ' + i;
for (let i = 1; i <= 50; i++) prof(VP + hex12(i * 61 + 11)).marked = 1;
// 标签：压测 2000、老客户 500、VIP 300（含重叠）
for (let i = 1; i <= 2000; i++) tags.push([VP + hex12(i * 4 + 1), '压测']);
for (let i = 1; i <= 500; i++) tags.push([VP + hex12(i * 17 + 2), '老客户']);
for (let i = 1; i <= 300; i++) tags.push([VP + hex12(i * 23 + 3), 'VIP']);
// 回收站：150 位客户整段删除（1 天内，避开 7 天自动彻底删除）
const deletedVisitors = new Set();
for (let i = 1; i <= 150; i++) deletedVisitors.add(VP + hex12(i * 53 + 13));
const delAt = fmt(NOW - 86400 * 1000 + 1000, true);

// ===== 写库：多行 INSERT，每批 1000 行 =====
function batches(rows, size, head, toSql) {
  const out = [];
  for (let s = 0; s < rows.length; s += size) out.push(head + rows.slice(s, s + size).map(toSql).join(',\n') + ';');
  return out;
}
const sqls = [];
sqls.push(...batches(visitors, 1000, 'INSERT INTO visitors(id, site_id, ip_cipher, ip_hash, ua, country, city, referer, last_page, identifier, first_seen, last_seen) VALUES\n',
  (v) => `(${q(v.id)},'default','','',${q(v.ua)},${q(v.country)},${q(v.city)},${q(v.ref)},${q(v.page)},${q(v.ident)},${q(v.first)},${q(v.last)})`));
sqls.push(...batches(convs, 1000, 'INSERT INTO conversations(id, site_id, visitor_id, agent_id, status, unread_visitor, unread_agent, started_at, updated_at, closed_at, agent_replied, visitor_engaged, deleted_at, deleted_by) VALUES\n',
  (c) => `(${q(c.id)},'default',${q(c.vid)},${c.agent === null ? 'NULL' : c.agent},${q(c.status)},0,${c.unread},${q(c.started)},${q(c.updated)},${q(c.closed)},${c.replied},${c.engaged},${deletedVisitors.has(c.vid) ? q(delAt) : 'NULL'},${deletedVisitors.has(c.vid) ? "'bulk_seed'" : "''"})`));
sqls.push(...batches(msgs, 1000, 'INSERT INTO messages(id, conv_id, sender, sender_ref, content, media_url, media_kind, media_name, media_size, delivered_ws, created_at, recalled_at, recalled_by) VALUES\n',
  (m) => `(${q(m.id)},${q(m.conv_id)},${q(m.sender)},${q(m.sender_ref)},${q(m.content)},${q(m.media_url)},${q(m.media_kind)},${q(m.media_name)},${m.media_size === null ? 'NULL' : m.media_size},1,${q(m.created)},${q(m.recalled_at)},${q(m.recalled_by)})`));
const nowStr = fmt(NOW, true);
sqls.push(...batches([...profiles.entries()], 1000, 'INSERT INTO visitor_profiles(visitor_id, note, pinned_at, marked_unread, blocked_at, blocked_by, block_reason, updated_by, updated_at) VALUES\n',
  ([vid, p]) => `(${q(vid)},${q(p.note)},${q(p.pinned)},${p.marked},${q(p.blocked)},${p.blocked ? "'bulk_seed'" : "''"},${p.blocked ? "'压测拉黑'" : "''"},'bulk_seed',${q(nowStr)})`));
sqls.push(...batches(tags, 1000, 'INSERT IGNORE INTO visitor_tags(visitor_id, tag, created_at) VALUES\n', ([vid, tag]) => `(${q(vid)},${q(tag)},${q(nowStr)})`));
// 回收站客户的历史清空时间（与产品删除行为一致）
sqls.push(...batches([...deletedVisitors], 1000, 'INSERT INTO visitor_profiles(visitor_id, history_reset_at, updated_by, updated_at) VALUES\n',
  (vid) => `(${q(vid)},${q(delAt)},'bulk_seed',${q(nowStr)})`).map((s) => s.replace(/;$/, ' ON DUPLICATE KEY UPDATE history_reset_at=VALUES(history_reset_at);')));

const t0 = Date.now();
// 分块送入 mysql，单次输入不超过约 8MB
let chunk = [], size = 0;
const flush = () => { if (chunk.length) { mysql('SET NAMES utf8mb4; SET time_zone=\'+08:00\';\n' + chunk.join('\n')); chunk = []; size = 0; } };
for (const s of sqls) { chunk.push(s); size += s.length; if (size > 8e6) flush(); }
flush();
mysql('ANALYZE TABLE conversations, messages, visitors, visitor_profiles, visitor_tags;');

const stats = mysql(`SELECT
 (SELECT COUNT(*) FROM visitors), (SELECT COUNT(*) FROM conversations), (SELECT COUNT(*) FROM messages),
 (SELECT COUNT(*) FROM visitors WHERE id LIKE '${VP}%'), (SELECT COUNT(*) FROM conversations WHERE id LIKE '${CP}%'), (SELECT COUNT(*) FROM messages WHERE id LIKE '${MP}%'),
 (SELECT COUNT(*) FROM conversations WHERE status='open'), (SELECT COUNT(*) FROM conversations WHERE visitor_engaged=1),
 (SELECT COUNT(*) FROM conversations WHERE deleted_at IS NOT NULL), (SELECT COUNT(*) FROM visitor_profiles WHERE pinned_at IS NOT NULL),
 (SELECT COUNT(*) FROM visitor_profiles WHERE blocked_at IS NOT NULL), (SELECT COUNT(*) FROM visitor_tags);`).split('\t');
const names = ['visitors_total', 'conversations_total', 'messages_total', 'bulk_visitors', 'bulk_conversations', 'bulk_messages', 'open_conversations', 'engaged_conversations', 'recycle_conversations', 'pinned', 'blocked', 'tags'];
console.log(JSON.stringify({ seeded_in_ms: Date.now() - t0, engaged_visitors: engagedVisitors.size, ...Object.fromEntries(names.map((n, i) => [n, Number(stats[i])])) }));
