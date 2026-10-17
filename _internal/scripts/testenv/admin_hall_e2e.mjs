#!/usr/bin/env node
// 管理后台「多人大厅」浏览器端到端联调（[119]）：Chrome 无界面 + Chromium 假媒体设备 + 本机测试后端（含 cs-livekit）。
// 覆盖：客服进入大厅、看到访客、访客开麦后客服单人静音、全员静音、移出（10 分钟）、发送并删除大厅消息（访客端同步消失）、
//       设置页大厅分组与视频开关（开启后客服开摄像头出现画面，关闭后摄像头被收回）、同账号多标签页后开接管、离开大厅。
// 访客由真实 widget 页面（/widget/chat.html）扮演：打开即自动进入大厅（契约第 1 节）。
// 运行（Mac 测试环境）：ADMIN_BOOTSTRAP_PASSWORD=... node _internal/scripts/testenv/admin_hall_e2e.mjs
// 可选环境变量：QA_ADMIN_USER（默认 qa_admin）、CS_E2E_BASE（默认 http://127.0.0.1:18090）、CS_E2E_API（默认 http://127.0.0.1:18080）、CHROME_PATH。
// 结束时把 5 项大厅设置恢复为运行前的值；被移出的测试访客 10 分钟后自动解除。
import { chromium } from 'playwright-core';
import { mkdirSync } from 'node:fs';
import { randomUUID } from 'node:crypto';

const BASE = process.env.CS_E2E_BASE || 'http://127.0.0.1:18090';
const API = process.env.CS_E2E_API || 'http://127.0.0.1:18080';
const USER = process.env.QA_ADMIN_USER || 'qa_admin';
const PASS = process.env.ADMIN_BOOTSTRAP_PASSWORD;
const CHROME = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const SHOTS = (process.env.HOME || '.') + '/cs-testenv/shots/admin-hall-e2e';
const HALL_KEYS = ['hall_enabled', 'hall_video_enabled', 'hall_max_speakers', 'hall_max_cameras', 'hall_max_participants'];
// Chromium 假媒体参数：自动同意授权、用合成音视频代替真实设备、允许无手势自动播放
const FAKE_MEDIA_ARGS = ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', '--autoplay-policy=no-user-gesture-required'];

if (!PASS) { console.log('FAIL 缺少环境变量 ADMIN_BOOTSTRAP_PASSWORD'); process.exit(2); }
mkdirSync(SHOTS, { recursive: true });
let passed = 0, failed = 0;
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + String(detail || '').slice(0, 300)); } };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, timeout = 15000, step = 300) {
  const end = Date.now() + timeout;
  for (;;) {
    try { const v = await fn(); if (v) return v; } catch { /* 页面还在渲染 */ }
    if (Date.now() > end) return null;
    await sleep(step);
  }
}
const problems = [];
const watch = (page, tag) => {
  page.on('pageerror', (e) => problems.push(tag + ' pageerror: ' + e.message));
  page.on('response', (r) => { if (r.url().includes('/api/') && r.status() >= 500) problems.push(tag + ' http ' + r.status() + ' ' + r.url()); });
};

// ---------- 接口登录：只用于读取/恢复设置（界面操作全部走浏览器） ----------
const login = await (await fetch(API + '/api/agent/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: USER, password: PASS }) })).json();
if (!login || !login.token) { console.log('FAIL 接口登录失败'); process.exit(2); }
const auth = { Authorization: 'Bearer ' + login.token, 'Content-Type': 'application/json' };
const api = async (method, path, body) => (await fetch(API + path, { method, headers: auth, body: body === undefined ? undefined : JSON.stringify(body) })).json();
const original = (await api('GET', '/api/admin/settings')).data || {};
const restore = Object.fromEntries(HALL_KEYS.filter((k) => typeof original[k] === 'string').map((k) => [k, original[k]]));
const setHall = (kv) => api('POST', '/api/admin/settings', kv);
const r0 = await setHall({ hall_enabled: 'true', hall_video_enabled: 'false', hall_max_speakers: '6', hall_max_cameras: '2', hall_max_participants: '100' });
check('测试前置：大厅设置可保存（5 个键在设置白名单内）', r0 && r0.code === 0, JSON.stringify(r0));

const browser = await chromium.launch({ executablePath: CHROME, headless: true, args: FAKE_MEDIA_ARGS });
const ctxOpts = { locale: 'zh-CN', timezoneId: 'America/New_York', permissions: ['microphone', 'camera'] };
try {
  // ---------- 访客（两个独立上下文 = 两个不同访客） ----------
  const visitors = [];
  for (let i = 0; i < 2; i++) {
    const ctx = await browser.newContext({ ...ctxOpts, viewport: { width: 420, height: 720 } });
    const p = await ctx.newPage();
    watch(p, 'visitor' + i);
    await p.goto(BASE + '/widget/chat.html?site=default');
    visitors.push(p);
  }
  await sleep(4000);

  // ---------- 客服登录并打开大厅页 ----------
  const adminCtx = await browser.newContext({ ...ctxOpts, viewport: { width: 1440, height: 900 } });
  const page = await adminCtx.newPage();
  watch(page, 'admin');
  const shot = (n, p = page) => p.screenshot({ path: SHOTS + '/' + n + '.png' }).catch(() => {});
  await page.goto(BASE + '/admin/login');
  await page.getByPlaceholder('用户名').fill(USER);
  await page.getByPlaceholder('密码').fill(PASS);
  await page.getByRole('button', { name: '登 录' }).click();
  await page.waitForURL('**/admin/console', { timeout: 15000 });
  check('导航菜单有「多人大厅」入口', await page.getByText('多人大厅', { exact: true }).count() > 0);
  await page.getByText('多人大厅', { exact: true }).first().click();
  await page.waitForURL('**/admin/hall', { timeout: 10000 });
  await sleep(1500);
  await shot('h01-hall-before-join');
  const before = await page.locator('body').innerText();
  check('大厅页渲染：操作区、参与者、大厅消息', ['进入大厅', '参与者', '大厅消息', '在线人数', '开麦数', '摄像头数'].every((t) => before.includes(t)), before.slice(0, 300));

  // ---------- 进入大厅 ----------
  await page.getByRole('button', { name: '进入大厅' }).click();
  const joined = await waitFor(async () => (await page.locator('body').innerText()).includes('已进入'), 20000);
  await shot('h02-hall-joined');
  check('客服进入大厅（状态显示「已进入」）', !!joined, (await page.locator('body').innerText()).slice(0, 300));
  check('进入后出现「离开大厅」与可用的麦克风按钮', await page.getByRole('button', { name: '离开大厅' }).count() > 0 && await page.getByRole('button', { name: /麦克风/ }).isEnabled());
  const audioBlocked = await page.getByText('浏览器阻止了自动播放').count();
  check('「进入大厅」作为用户手势已解锁声音（未出现自动播放拦截提示）', audioBlocked === 0);

  const visitorRows = () => page.locator('.el-table__body tr.el-table__row').filter({ has: page.locator('.el-tag', { hasText: /^访客$/ }) });
  const seen = await waitFor(async () => (await visitorRows().count()) >= 2, 25000);
  await shot('h03-visitors');
  check('参与者表格看到 2 位访客（名字为「访客 NNNN」）', !!seen, 'count=' + (await visitorRows().count()));
  const tableText = await page.locator('.el-table__body').innerText().catch(() => '');
  check('参与者表格不暴露访客 IP / vid', !/\b\d{1,3}(\.\d{1,3}){3}\b/.test(tableText) && !/v_[0-9a-f]{20}/.test(tableText), tableText.slice(0, 200));

  // ---------- 单人静音 ----------
  const micRow = () => visitorRows().filter({ hasText: '已开' });
  const openMic = async (vp) => {
    const btn = vp.getByText('开麦', { exact: true }).first();
    if (!(await btn.count())) return false;
    await btn.click().catch(() => {});
    return true;
  };
  const clicked = await openMic(visitors[0]);
  const micOn = clicked && await waitFor(async () => (await micRow().count()) >= 1, 15000);
  check('访客开麦后客服表格显示麦克风「已开」', !!micOn, clicked ? '表格未显示已开' : '访客端未找到「开麦」按钮');
  if (micOn) {
    await micRow().first().getByRole('button', { name: '静音' }).click();
    const toast = await waitFor(async () => (await page.locator('.el-message').innerText().catch(() => '')).includes('已静音'), 8000);
    const off = await waitFor(async () => (await micRow().count()) === 0, 15000);
    await shot('h04-muted');
    check('单人静音成功且该访客麦克风变为「关闭」', !!toast && !!off);
  }

  // ---------- 全员静音 ----------
  const clicked2 = await openMic(visitors[0]);
  await waitFor(async () => (await micRow().count()) >= 1, 15000);
  await page.getByRole('button', { name: '全员静音' }).click();
  await page.locator('.el-message-box__btns button', { hasText: '全员静音' }).click();
  const muteAllToast = await waitFor(async () => (await page.locator('.el-message').innerText().catch(() => '')).includes('已全员静音'), 8000);
  const allOff = await waitFor(async () => (await micRow().count()) === 0, 15000);
  await shot('h05-mute-all');
  check('全员静音成功且没有访客处于开麦状态', !!muteAllToast && !!allOff, clicked2 ? '' : '访客端未能再次开麦（仍校验接口成功提示）');

  // ---------- 大厅消息：发送 + 删除（访客端同步消失） ----------
  const marker = 'HALL' + randomUUID().slice(0, 8);
  const input = page.getByPlaceholder(/回车发送/);
  await input.fill('后台大厅消息测试 ' + marker);
  await input.press('Enter');
  const msgBlock = () => page.locator('[data-hall-msg-id]', { hasText: marker });
  const sent = await waitFor(async () => (await msgBlock().count()) === 1 && !(await msgBlock().innerText()).includes('发送中'), 10000);
  check('客服发送大厅消息成功（无「发送中/发送失败」）', !!sent && !(await msgBlock().innerText().catch(() => '')).includes('发送失败'));
  const visitorGot = await waitFor(async () => (await visitors[1].locator('body').innerText()).includes(marker), 10000);
  check('访客端实时收到客服的大厅消息', !!visitorGot);
  await msgBlock().getByRole('button', { name: '删除' }).click();
  await page.locator('.el-message-box__btns button', { hasText: '删除' }).click();
  const gone = await waitFor(async () => (await msgBlock().count()) === 0, 8000);
  const visitorGone = await waitFor(async () => !(await visitors[1].locator('body').innerText()).includes(marker), 10000);
  await shot('h06-deleted');
  check('删除大厅消息（二次确认）后客服端消失', !!gone);
  check('删除后访客端同步消失（hall_deleted 广播）', !!visitorGone);

  // ---------- 移出 ----------
  const beforeKick = await visitorRows().count();
  await visitorRows().last().getByRole('button', { name: '移出' }).click();
  await page.locator('.el-dialog').getByText('10 分钟', { exact: true }).click();
  await page.locator('.el-dialog').getByRole('button', { name: '确认移出' }).click();
  const kickToast = await waitFor(async () => (await page.locator('.el-message').innerText().catch(() => '')).includes('已移出大厅'), 8000);
  const kicked = await waitFor(async () => (await visitorRows().count()) === beforeKick - 1, 20000);
  await shot('h07-kicked');
  check('移出 10 分钟：成功提示且访客从表格消失', !!kickToast && !!kicked, 'before=' + beforeKick + ' after=' + (await visitorRows().count()));
  const kickedSeen = await waitFor(async () => {
    for (const vp of visitors) if ((await vp.locator('body').innerText()).includes('移出')) return true;
    return false;
  }, 10000);
  check('被移出的访客端显示被移出提示', !!kickedSeen);

  // ---------- 设置页：大厅分组与视频开关（另开标签页，不影响大厅页） ----------
  const settingsPage = await adminCtx.newPage();
  watch(settingsPage, 'settings');
  await settingsPage.goto(BASE + '/admin/settings');
  await sleep(2000);
  const st = await settingsPage.locator('body').innerText();
  check('设置页有「多人大厅」分组与 5 项设置及带宽说明', ['多人大厅', '启用大厅', '允许视频', '同时开麦访客上限', '同时开摄像头上限', '大厅人数上限', '7Mbps'].every((t) => st.includes(t)), st.slice(0, 300));
  const toggleVideo = async () => {
    await settingsPage.locator('.el-form-item', { hasText: '允许视频' }).locator('.el-switch').click();
    await settingsPage.getByRole('button', { name: '保 存' }).click();
    return waitFor(async () => (await settingsPage.locator('.el-message').innerText().catch(() => '')).includes('保存成功'), 8000);
  };
  check('设置页开启视频并保存成功', !!(await toggleVideo()));
  await settingsPage.screenshot({ path: SHOTS + '/h08-settings.png', fullPage: true }).catch(() => {});
  const videoOn = await waitFor(async () => (await page.locator('body').innerText()).includes('视频已开启'), 10000);
  check('大厅页实时收到 hall_settings：显示「视频已开启」', !!videoOn);
  await page.getByRole('button', { name: '打开摄像头' }).click();
  const playing = await waitFor(() => page.evaluate(() => Array.from(document.querySelectorAll('video')).some((v) => v.readyState >= 2 && v.videoWidth > 0)), 20000);
  await shot('h09-camera-on');
  check('客服开摄像头后视频区出现本地画面（假摄像头）', !!playing);
  const camCount = await page.locator('.el-statistic', { hasText: '摄像头数' }).locator('.el-statistic__number').innerText().catch(() => '');
  check('摄像头数统计为 1', camCount.trim() === '1', camCount);
  check('设置页关闭视频并保存成功', !!(await toggleVideo()));
  const camOff = await waitFor(async () => {
    const t = await page.locator('body').innerText();
    return t.includes('视频未开启') && (await page.getByRole('button', { name: '打开摄像头' }).count()) > 0;
  }, 15000);
  await shot('h10-video-off');
  check('管理员关闭视频后客服摄像头被收回、按钮恢复「打开摄像头」', !!camOff);
  await settingsPage.close();

  // ---------- 同账号多标签页：后开的接管 ----------
  const page2 = await adminCtx.newPage();
  watch(page2, 'admin-tab2');
  await page2.goto(BASE + '/admin/hall');
  await sleep(1500);
  await page2.getByRole('button', { name: '进入大厅' }).click();
  const tab2In = await waitFor(async () => (await page2.locator('body').innerText()).includes('已进入'), 20000);
  const tab1Out = await waitFor(async () => {
    const t = await page.locator('body').innerText();
    return t.includes('未进入') && /已在其他(标签页|地方)进入大厅/.test(t);
  }, 15000);
  await shot('h11-takeover');
  check('同账号后开标签页进入大厅，先开的标签页自动退出并提示', !!tab2In && !!tab1Out);

  // ---------- 离开大厅 ----------
  await page2.getByRole('button', { name: '离开大厅' }).click();
  await page2.locator('.el-message-box__btns button', { hasText: '离开' }).click();
  const left = await waitFor(async () => (await page2.locator('body').innerText()).includes('未进入'), 10000);
  check('离开大厅后状态恢复「未进入」', !!left);
  await page2.close();
} catch (e) {
  check('脚本执行未中断', false, e && e.stack ? e.stack : e);
} finally {
  if (Object.keys(restore).length) {
    const rr = await setHall(restore).catch(() => null);
    check('恢复运行前的大厅设置', rr && rr.code === 0, JSON.stringify(rr));
  }
  check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
  await browser.close();
  console.log('SUMMARY passed=' + passed + ' failed=' + failed);
  process.exit(failed ? 1 : 0);
}
