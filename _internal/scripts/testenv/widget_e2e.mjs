#!/usr/bin/env node
// 访客端（widget）0.8.0 真实浏览器联调：Chrome 无界面 + 本机测试后端。客服动作通过接口与实时连接模拟。
// 覆盖：客服正在输入、撤回、删除后清空（在线 + 刷新后）、访客输入状态、北京时间显示（浏览器时区设为美国东部）。
import { chromium } from 'playwright-core';
import { randomUUID } from 'node:crypto';
import { mkdirSync } from 'node:fs';

const BASE = 'http://127.0.0.1:18090';
const API = 'http://127.0.0.1:18080';
const SHOTS = process.env.HOME + '/cs-testenv/shots/widget-e2e';
mkdirSync(SHOTS, { recursive: true });
let passed = 0, failed = 0;
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + String(detail || '').slice(0, 300)); } };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const problems = [];

const login = await (await fetch(API + '/api/agent/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'qa_admin', password: process.env.ADMIN_BOOTSTRAP_PASSWORD }) })).json();
const auth = { Authorization: 'Bearer ' + login.token, 'Content-Type': 'application/json' };
const api = async (method, path, body) => (await fetch(API + path, { method, headers: auth, body: body === undefined ? undefined : JSON.stringify(body) })).json();
const agentWS = new WebSocket(API.replace('http', 'ws') + '/ws/agent', ['cs-auth', login.token]);
const agentEvents = [];
agentWS.onmessage = (m) => { try { agentEvents.push(JSON.parse(m.data)); } catch {} };
await new Promise((r) => { agentWS.onopen = r; });

const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const ctx = await browser.newContext({ viewport: { width: 420, height: 720 }, locale: 'zh-CN', timezoneId: 'America/New_York' });
const page = await ctx.newPage();
page.on('pageerror', (e) => problems.push('pageerror: ' + e.message));
page.on('response', (r) => { if (r.url().includes('/api/') && r.status() >= 500) problems.push('http ' + r.status() + ' ' + r.url()); });
const shot = (n) => page.screenshot({ path: SHOTS + '/' + n + '.png' });
const listText = () => page.locator('#list').innerText();
// [119] 挂件默认显示「大厅」页签；本脚本覆盖一对一私聊，加载后切到「私聊客服」页签
const toPrivate = async () => {
  await page.waitForSelector('#tabPrivate', { timeout: 15000 });
  if ((await page.getAttribute('#tabPrivate', 'aria-selected')) !== 'true') await page.click('#tabPrivate');
  await page.waitForSelector('#input', { state: 'visible', timeout: 10000 });
};

await page.goto(BASE + '/widget/chat.html?site=default');
await toPrivate();
await page.waitForFunction(() => /已连接|在线/.test(document.getElementById('status').textContent || ''), null, { timeout: 15000 }).catch(() => {});
const marker = 'WEB' + randomUUID().slice(0, 6);
await page.fill('#input', '访客网页测试 ' + marker);
await page.keyboard.press('Enter');
await sleep(1500);
check('访客发送消息后显示在聊天区', (await listText()).includes(marker));
const bjHM = new Date(Date.now() + 8 * 3600e3).toISOString().slice(11, 16);
const prevHM = new Date(Date.now() + 8 * 3600e3 - 60e3).toISOString().slice(11, 16);
const lt = await listText();
check('浏览器时区为美国东部时访客端仍显示北京时间', lt.includes(bjHM) || lt.includes(prevHM), 'expect ' + bjHM);

// 找到该访客会话并接管
let hit;
for (let i = 0; i < 10 && !hit; i++) { const s = await api('GET', '/api/agent/search?q=' + marker); hit = (s.data || [])[0]; if (!hit) await sleep(500); }
check('客服端能搜到访客消息', !!hit);
await api('POST', '/api/agent/conversations/' + hit.conv_id + '/assign');

// 访客输入状态 → 客服收到 typing（访客端每 3 秒最多发送一次：等过上次发送消息时的节流窗口）
await sleep(3200);
await page.fill('#input', '正在输入');
await page.locator('#input').press('Space');
const gotTyping = await (async () => { for (let i = 0; i < 30; i++) { if (agentEvents.some((e) => e.type === 'typing' && e.from === 'visitor:' + hit.visitor_id)) return true; await sleep(100); } return false; })();
check('访客输入时客服收到「正在输入」', gotTyping);
await page.fill('#input', '');

// 客服正在输入 → 访客显示提示
agentWS.send(JSON.stringify({ type: 'typing', conv: hit.conv_id, ts: Date.now() }));
const tip = await page.waitForFunction(() => document.body.innerText.includes('客服正在输入'), null, { timeout: 4000 }).then(() => true).catch(() => false);
check('访客端显示「客服正在输入…」', tip);
await shot('v01-typing');

// 客服消息 + 撤回
const mid = randomUUID();
agentWS.send(JSON.stringify({ type: 'chat', id: mid, conv: hit.conv_id, content: '这条客服消息将被撤回 ' + marker, ts: Date.now() }));
const shown = await page.waitForFunction((m) => document.getElementById('list').innerText.includes('这条客服消息将被撤回 ' + m), marker, { timeout: 6000 }).then(() => true).catch(() => false);
check('访客收到客服消息', shown);
const typingHidden = await page.evaluate(() => !document.body.innerText.includes('客服正在输入'));
check('收到客服消息后「正在输入」隐藏', typingHidden);
const rc = await api('POST', '/api/agent/messages/' + mid + '/recall');
check('客服撤回成功', rc.code === 0, JSON.stringify(rc));
const recalled = await page.waitForFunction(() => document.getElementById('list').innerText.includes('客服撤回了一条消息'), null, { timeout: 5000 }).then(() => true).catch(() => false);
const afterRecall = await listText();
check('访客端显示「客服撤回了一条消息」且不再显示原文', recalled && !afterRecall.includes('这条客服消息将被撤回'), afterRecall.slice(-200));
await shot('v02-recalled');
await page.reload();
await toPrivate();
await sleep(2500);
const afterReload = await listText();
check('刷新后撤回的消息仍不显示原文', !afterReload.includes('这条客服消息将被撤回') && afterReload.includes(marker), afterReload.slice(-200));

// 删除聊天记录 → 在线清空
const del = await api('POST', '/api/agent/visitors/delete', { visitor_ids: [hit.visitor_id] });
check('客服删除该访客聊天记录', del.code === 0 && del.data.visitors === 1, JSON.stringify(del));
const cleared = await page.waitForFunction(() => document.body.innerText.includes('聊天记录已清空'), null, { timeout: 5000 }).then(() => true).catch(() => false);
check('在线访客聊天区立即清空', cleared && !(await listText()).includes(marker));
await shot('v03-cleared');
await page.reload();
await toPrivate();
await sleep(2500);
check('刷新后本地缓存也已清空', !(await listText()).includes(marker));
await page.fill('#input', '清空后的新消息 ' + marker);
await page.keyboard.press('Enter');
await sleep(1500);
const s2 = await api('GET', '/api/agent/search?q=' + encodeURIComponent('清空后的新消息 ' + marker));
check('清空后访客可继续咨询（新会话）', (s2.data || []).some((h) => h.visitor_id === hit.visitor_id && h.conv_id !== hit.conv_id), JSON.stringify(s2.data));
await shot('v04-after-clear-new-message');

// 清理：彻底删除该测试访客
await api('POST', '/api/agent/visitors/delete', { visitor_ids: [hit.visitor_id] });
await api('POST', '/api/admin/recycle-bin/purge', { visitor_ids: [hit.visitor_id] });
check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
agentWS.close();
await browser.close();
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
