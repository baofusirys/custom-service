#!/usr/bin/env node
// [119] 大厅机器人访客（只连本机测试后端 127.0.0.1）：进入大厅、订阅大厅消息；收到客服在大厅发的以「巡检」开头的文字时回一句「大厅收到「…」」。
// 不连接 LiveKit 媒体（Node 没有 WebRTC），只验证文字消息链路；App 与浏览器端到端巡检用它当「另一位访客」。
// 用法：node hall_bot.mjs --name 大厅机器人 --seconds 900
import { randomUUID } from 'node:crypto';

const BASE = process.env.CS_BASE || 'http://127.0.0.1:18080';
if (!/^http:\/\/127\.0\.0\.1:/.test(BASE)) { console.error('只允许连本机测试后端'); process.exit(2); }
const args = Object.fromEntries(process.argv.slice(2).reduce((a, v, i, arr) => (v.startsWith('--') ? [...a, [v.slice(2), arr[i + 1]]] : a), []));
const NAME = args.name || '大厅机器人';
const SECONDS = Math.min(Math.max(Number(args.seconds) || 900, 10), 3600);
const post = async (path, body, token) => {
  const r = await fetch(BASE + path, { method: 'POST', headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: 'Bearer ' + token } : {}) }, body: JSON.stringify(body || {}) });
  return { status: r.status, ...(await r.json().catch(() => ({}))) };
};
const s = await post('/api/visitor/session', { site_id: 'default', identifier: NAME, ua: 'QA-Hall-Bot/1.0' });
if (s.code !== 0) { console.error('session failed', s.status, s.code); process.exit(1); }
const join = await post('/api/visitor/hall/join', {}, s.visitor_token);
if (join.code !== 0) { console.error('hall join failed', join.status, join.code, join.msg); process.exit(1); }
const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/visitor', ['cs-auth', s.visitor_token]);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('ws error')); setTimeout(() => rej(new Error('ws timeout')), 8000); });
const subId = randomUUID();
let subscribed = false;
ws.onmessage = (m) => {
  let e; try { e = JSON.parse(m.data); } catch { return; }
  if (e.type === 'hello') { ws.send(JSON.stringify({ type: 'hall_subscribe', id: subId, ts: Date.now() })); return; }
  if (e.type === 'hall_subscribed' && e.id === subId) { subscribed = true; console.log('hall bot ready', join.data.name, join.data.identity); return; }
  if (e.type === 'error' && e.id === subId) { console.error('subscribe failed', e.content); process.exit(1); }
  if (e.type === 'hall_chat' && e.extra && e.extra.sender === 'agent' && typeof e.content === 'string' && e.content.startsWith('巡检')) {
    setTimeout(() => ws.send(JSON.stringify({ type: 'hall_chat', id: randomUUID(), content: '大厅收到「' + e.content + '」', ts: Date.now() })), 300);
  }
};
setInterval(() => ws.readyState === 1 && ws.send(JSON.stringify({ type: 'ping', ts: Date.now() })), 20000);
setTimeout(() => { if (!subscribed) { console.error('subscribe timeout'); process.exit(1); } }, 10000);
setTimeout(() => { ws.close(); process.exit(0); }, SECONDS * 1000);
