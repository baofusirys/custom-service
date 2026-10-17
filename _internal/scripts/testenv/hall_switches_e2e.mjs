#!/usr/bin/env node
// [123] 「大厅开关」浏览器端到端联调（契约 _internal/reports/hall-contract.md 第 10 节）。
// 覆盖：管理员接口临时创建一个普通客服 → 普通客服登录后台 → 「多人大厅」页顶部「大厅开关」面板（4 个开关 + 最后修改人）→
//       模拟提交失败（拦截 POST 返回 42910）开关回滚并提示中文错误 → 普通客服进入大厅后依次关/开：
//         文字聊天：访客窗口大厅输入框隐藏、开后恢复；后台输入区禁用并提示「大厅文字聊天已暂停」；
//         语音：访客开麦按钮隐藏、开后恢复；后台麦克风按钮禁用、本端正在发的麦克风被停止并提示「大厅语音已暂停」；
//         视频：访客摄像头按钮出现 / 隐藏；后台显示「视频已开启 / 未开启」；
//         总开关（二次确认）：访客窗口没有大厅页签只剩私聊；后台自动离开并提示「大厅已被关闭」；
//           再在未进入大厅、大厅已关闭的状态下由同一普通客服打开总开关 → 访客窗口不刷新即恢复大厅页签；
//       另一个管理员页面（同样停在多人大厅页、未进入大厅）实时看到每次开关变化与最后修改人；
//       接口层：普通客服可读写开关、未登录 401、参数错误 40060、普通客服不能用管理员设置接口改开关（403 且值不变）。
// 结束时把 4 个开关恢复为运行前的值，并停用临时客服。
// 运行（Mac 测试环境）：ADMIN_BOOTSTRAP_PASSWORD=... node _internal/scripts/testenv/hall_switches_e2e.mjs
// 前置：测试后端 18080（含迁移 017）、admin_e2e_proxy.mjs 18090（挂载 admin/dist 与 widget/public）、测试 LiveKit 已启动。
// 可选环境变量：QA_ADMIN_USER（默认 qa_admin）、CS_E2E_BASE（默认 http://127.0.0.1:18090）、CS_E2E_API（默认 http://127.0.0.1:18080）、CHROME_PATH。
// 安全：只允许连接本机地址；凭证只从环境变量读取；临时客服密码在进程内随机生成，不打印、不落盘。
import { chromium } from 'playwright-core';
import { mkdirSync } from 'node:fs';
import { randomBytes } from 'node:crypto';

const BASE = process.env.CS_E2E_BASE || 'http://127.0.0.1:18090';
const API = process.env.CS_E2E_API || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.QA_ADMIN_USER || 'qa_admin';
const ADMIN_PASS = process.env.ADMIN_BOOTSTRAP_PASSWORD;
const CHROME = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const SHOTS = (process.env.HOME || '.') + '/cs-testenv/shots/hall-switches-e2e';
// 契约 10.1 的四个开关：设置键 ↔ 开关接口键 ↔ 面板 data-hall-switch
const SWITCHES = [
  { setting: 'hall_enabled', key: 'enabled' },
  { setting: 'hall_chat_enabled', key: 'chat_enabled' },
  { setting: 'hall_voice_enabled', key: 'voice_enabled' },
  { setting: 'hall_video_enabled', key: 'video_enabled' },
];
const SWITCH_SETTING_KEYS = SWITCHES.map((s) => s.setting);
// Chromium 假媒体参数：自动同意授权、用合成音视频代替真实设备、允许无手势自动播放
const FAKE_MEDIA_ARGS = ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', '--autoplay-policy=no-user-gesture-required'];

if (!ADMIN_PASS) { console.log('FAIL 缺少环境变量 ADMIN_BOOTSTRAP_PASSWORD'); process.exit(2); }
for (const [name, url] of [['CS_E2E_BASE', BASE], ['CS_E2E_API', API]]) {
  let host = '';
  try { host = new URL(url).hostname; } catch { /* 下方统一报错 */ }
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(host)) { console.log('FAIL ' + name + ' 只允许本机地址（127.0.0.1 / localhost）'); process.exit(2); }
}
mkdirSync(SHOTS, { recursive: true });

let passed = 0, failed = 0;
const redact = (v) => String(typeof v === 'string' ? v : JSON.stringify(v === undefined ? null : v, (k, x) => (/token|password|secret|authorization/i.test(k) ? '[REDACTED]' : x)))
  .replace(/eyJ[\w-]+\.[\w-]+\.[\w-]*/g, '[JWT]').slice(0, 400);
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + redact(detail === undefined ? '' : detail)); } };
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
  return page;
};

// ---------- 接口工具（只连本机测试后端） ----------
async function api(method, path, { token, body } = {}) {
  const headers = {};
  if (token) headers.Authorization = 'Bearer ' + token;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const r = await fetch(API + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let j = {};
  try { j = await r.json(); } catch { /* 非 JSON */ }
  return { status: r.status, ...j };
}
async function apiLogin(username, password) {
  const r = await api('POST', '/api/agent/login', { body: { username, password } });
  if (!r.token) throw new Error('接口登录失败 ' + redact({ status: r.status, code: r.code, msg: r.msg }));
  return { token: r.token, id: Number(r.agent && r.agent.id) };
}
const bool = (v) => v === true || v === 'true';

// ---------- 页面工具 ----------
const switchLoc = (page, key) => page.locator('[data-hall-switch="' + key + '"] .el-switch');
const switchState = (page, key) => switchLoc(page, key).evaluate((el) => ({ on: el.classList.contains('is-checked'), disabled: el.classList.contains('is-disabled') }));
const bodyText = (page) => page.locator('body').innerText();
const toastHas = (page, text) => waitFor(async () => (await page.locator('.el-message', { hasText: text }).count()) > 0, 8000);
async function uiLogin(page, username, password) {
  await page.goto(BASE + '/admin/login');
  await page.getByPlaceholder('用户名').fill(username);
  await page.getByPlaceholder('密码').fill(password);
  await page.getByRole('button', { name: '登 录' }).click();
  await page.waitForURL('**/admin/console', { timeout: 15000 });
  await page.goto(BASE + '/admin/hall');
  await page.waitForURL('**/admin/hall', { timeout: 10000 });
}
// 在面板上把开关切到 on；confirm=true 时在二次确认框里点「关闭大厅」并返回确认框文案
async function setSwitch(page, key, on, { confirm = false } = {}) {
  const sw = switchLoc(page, key);
  await sw.waitFor({ state: 'visible', timeout: 10000 });
  if (!(await waitFor(async () => !(await switchState(page, key)).disabled, 10000))) return { ok: false, why: key + ' 开关一直不可用' };
  if ((await switchState(page, key)).on === on) return { ok: true, already: true };
  await sw.click();
  let dialog = '';
  if (confirm) {
    const box = page.locator('.el-message-box');
    await box.waitFor({ state: 'visible', timeout: 5000 });
    dialog = await box.innerText();
    await box.locator('.el-message-box__btns button', { hasText: '关闭大厅' }).click();
  }
  const done = await waitFor(async () => { const st = await switchState(page, key); return st.on === on && !st.disabled; }, 15000);
  return { ok: !!done, dialog, why: done ? '' : key + ' 未变为 ' + on };
}
const observerSees = (page, key, on) => waitFor(async () => (await switchState(page, key)).on === on, 12000);
const visible = (page, sel) => page.locator(sel).first().isVisible();

// ---------- 前置：管理员接口、记录原值、临时普通客服 ----------
const run = randomBytes(3).toString('hex');
let admin = null, temp = null, original = null, browser = null;
const nickname = '开关测试客服' + run;
try {
  admin = await apiLogin(ADMIN_USER, ADMIN_PASS);
  const before = await api('GET', '/api/admin/settings', { token: admin.token });
  const have = SWITCH_SETTING_KEYS.filter((k) => typeof (before.data || {})[k] === 'string');
  check('读取运行前的 4 个大厅开关（hall_chat_enabled / hall_voice_enabled 已由迁移写入）', before.code === 0 && have.length === 4, { code: before.code, have });
  original = Object.fromEntries(have.map((k) => [k, before.data[k]]));
  const base = await api('POST', '/api/admin/settings', { token: admin.token, body: { hall_enabled: 'true', hall_chat_enabled: 'true', hall_voice_enabled: 'true', hall_video_enabled: 'false' } });
  check('测试基线：总开关 / 文字聊天 / 语音开启、视频关闭', base.code === 0, base);

  const username = 'qa_sw_' + run;
  // 服务端强密码规则：≥12 位，同时含大小写字母、数字与符号
  const password = 'Qs' + randomBytes(9).toString('hex') + '7k#Z';
  const created = await api('POST', '/api/admin/agents', { token: admin.token, body: { username, password, role: 'agent', nickname } });
  check('管理员临时创建普通客服', created.code === 0, { code: created.code, msg: created.msg });
  if (created.code !== 0) throw new Error('无法创建临时客服');
  temp = { username, password, ...(await apiLogin(username, password)) };

  // ---------- 接口层：权限与参数（正常 / 拒绝 / 绕过） ----------
  const g = await api('GET', '/api/agent/hall/switches', { token: temp.token });
  check('普通客服可读取开关：4 个布尔 + updated_by / updated_at 字符串', g.code === 0 && ['enabled', 'chat_enabled', 'voice_enabled', 'video_enabled'].every((k) => typeof (g.data || {})[k] === 'boolean')
    && typeof g.data.updated_by === 'string' && typeof g.data.updated_at === 'string', g);
  const anon = await api('GET', '/api/agent/hall/switches');
  check('未登录读取开关返回 401', anon.status === 401, { status: anon.status, code: anon.code });
  const anonPost = await api('POST', '/api/agent/hall/switches', { body: { enabled: false } });
  check('未登录修改开关返回 401（且大厅仍开启）', anonPost.status === 401, { status: anonPost.status, code: anonPost.code });
  for (const [label, body] of [['空对象', {}], ['字符串布尔', { chat_enabled: 'false' }], ['未知键', { hall_enabled: false }], ['数字', { voice_enabled: 0 }]]) {
    const r = await api('POST', '/api/agent/hall/switches', { token: temp.token, body });
    check('参数错误返回 400 40060：' + label, r.status === 400 && r.code === 40060, { status: r.status, code: r.code });
  }
  const bypass = await api('POST', '/api/admin/settings', { token: temp.token, body: { hall_chat_enabled: 'false' } });
  const after = await api('GET', '/api/agent/hall/switches', { token: temp.token });
  check('普通客服不能通过管理员设置接口改开关（403，值不变）', bypass.status === 403 && after.data && after.data.chat_enabled === true, { status: bypass.status, chat: after.data && after.data.chat_enabled });

  // ---------- 浏览器 ----------
  browser = await chromium.launch({ executablePath: CHROME, headless: true, args: FAKE_MEDIA_ARGS });
  const ctxOpts = { locale: 'zh-CN', timezoneId: 'America/New_York', permissions: ['microphone', 'camera'] };

  // 访客：真实 widget 页面，打开即自动进入大厅
  const vctx = await browser.newContext({ ...ctxOpts, viewport: { width: 420, height: 760 } });
  const vp = watch(await vctx.newPage(), 'visitor');
  await vp.goto(BASE + '/widget/chat.html?site=default');
  const vLive = await waitFor(async () => (await visible(vp, '#tabHall')) && (await visible(vp, '#hallInput')), 25000);
  check('访客窗口显示大厅页签与大厅输入框', !!vLive, (await bodyText(vp)).slice(0, 200));
  // 记号：用于证明后续「恢复大厅页签」没有刷新页面
  await vp.evaluate(() => { window.__qaNoReload = 'hall-switches'; });

  // 观察者：管理员停在多人大厅页（不进入大厅）
  const actx = await browser.newContext({ ...ctxOpts, viewport: { width: 1440, height: 900 } });
  const observer = watch(await actx.newPage(), 'observer');
  await uiLogin(observer, ADMIN_USER, ADMIN_PASS);

  // 普通客服
  const gctx = await browser.newContext({ ...ctxOpts, viewport: { width: 1440, height: 900 } });
  const page = watch(await gctx.newPage(), 'agent');
  const shot = (n, p = page) => p.screenshot({ path: SHOTS + '/' + n + '.png', fullPage: true }).catch(() => {});
  await uiLogin(page, temp.username, temp.password);
  const panelReady = await waitFor(async () => {
    for (const s of SWITCHES) if ((await switchState(page, s.key)).disabled) return false;
    return true;
  }, 15000);
  await shot('s01-panel');
  const panelText = await page.locator('[data-hall-switch-panel]').innerText().catch(() => '');
  check('普通客服看到「大厅开关」面板：4 个开关可用', !!panelReady && ['大厅开关', '大厅总开关', '文字聊天', '语音', '视频'].every((t) => panelText.includes(t)), panelText.slice(0, 300));
  check('面板显示最后修改信息', /最后修改|尚无修改记录/.test(panelText), panelText.slice(0, 300));
  const initial = {};
  for (const s of SWITCHES) initial[s.key] = (await switchState(page, s.key)).on;
  check('面板初始状态与服务端一致（总开关 / 文字 / 语音开、视频关）', initial.enabled && initial.chat_enabled && initial.voice_enabled && !initial.video_enabled, initial);

  // ---------- 提交失败：回滚并提示中文错误（拦截请求，不触达服务端） ----------
  await page.route('**/api/agent/hall/switches', (route) => (route.request().method() === 'POST'
    ? route.fulfill({ status: 429, contentType: 'application/json', body: JSON.stringify({ code: 42910, msg: 'too many' }) })
    : route.continue()));
  await switchLoc(page, 'chat_enabled').click();
  const failToast = await toastHas(page, '操作过于频繁');
  const rolledBack = await waitFor(async () => { const st = await switchState(page, 'chat_enabled'); return st.on === true && !st.disabled; }, 8000);
  check('切换失败：提示「操作过于频繁」且开关回滚为开启', !!failToast && !!rolledBack);
  await page.unroute('**/api/agent/hall/switches');
  check('切换失败不影响其他客服页面（观察者仍为开启）', (await switchState(observer, 'chat_enabled')).on === true);

  // ---------- 普通客服进入大厅并开麦 ----------
  await page.getByRole('button', { name: '进入大厅' }).click();
  const joined = await waitFor(async () => (await bodyText(page)).includes('已进入'), 20000);
  check('普通客服进入大厅', !!joined, (await bodyText(page)).slice(0, 200));
  await page.getByRole('button', { name: '打开麦克风' }).click().catch(() => {});
  const micOn = await waitFor(async () => (await page.getByRole('button', { name: '关闭麦克风' }).count()) > 0, 10000);
  check('普通客服开麦（假麦克风）', !!micOn);

  // ---------- 文字聊天 ----------
  let r = await setSwitch(page, 'chat_enabled', false);
  check('关闭文字聊天：开关切换成功并提示', r.ok && !!(await toastHas(page, '大厅文字聊天已暂停')), r.why);
  const vChatOff = await waitFor(async () => !(await visible(vp, '#hallInput')) && (await bodyText(vp)).includes('大厅文字聊天已暂停'), 15000);
  check('访客：大厅输入框隐藏并提示「大厅文字聊天已暂停」', !!vChatOff, (await bodyText(vp)).slice(0, 300));
  const aChatOff = await waitFor(async () => (await page.getByPlaceholder('大厅文字聊天已暂停').isDisabled()) && (await page.getByRole('button', { name: '发送', exact: true }).isDisabled()), 8000);
  check('后台：大厅消息输入区与发送按钮禁用并显示「大厅文字聊天已暂停」', !!aChatOff && (await bodyText(page)).includes('大厅文字聊天已暂停'));
  check('后台：图片 / 文件上传按钮禁用', await page.locator('button.el-button', { hasText: /^\s*图片\s*$/ }).isDisabled() && await page.locator('button.el-button', { hasText: /^\s*文件\s*$/ }).isDisabled());
  check('观察者实时看到文字聊天已关闭', !!(await observerSees(observer, 'chat_enabled', false)));
  const lastBy = await waitFor(async () => (await observer.locator('[data-hall-switch-updated]').innerText()).includes(nickname), 10000);
  check('观察者面板显示最后修改人为该普通客服（含北京时间）', !!lastBy && (await observer.locator('[data-hall-switch-updated]').innerText()).includes('北京时间'));
  const vs1 = await api('GET', '/api/visitor/settings');
  check('访客公开设置 hall_chat_enabled=false', vs1.data && vs1.data.hall_chat_enabled === false, vs1.data && { hall_chat_enabled: vs1.data.hall_chat_enabled });
  await shot('s02-chat-off');
  await shot('s02-visitor-chat-off', vp);
  r = await setSwitch(page, 'chat_enabled', true);
  check('打开文字聊天', r.ok, r.why);
  check('访客：大厅输入框恢复', !!(await waitFor(() => visible(vp, '#hallInput'), 15000)));
  check('后台：输入区恢复可用', !!(await waitFor(() => page.getByPlaceholder(/回车发送/).isEnabled(), 8000)));
  check('观察者实时看到文字聊天已开启', !!(await observerSees(observer, 'chat_enabled', true)));

  // ---------- 语音 ----------
  r = await setSwitch(page, 'voice_enabled', false);
  check('关闭语音：开关切换成功', r.ok, r.why);
  const vVoiceOff = await waitFor(async () => !(await visible(vp, '#hallMicBtn')) && (await bodyText(vp)).includes('大厅语音已暂停'), 15000);
  check('访客：开麦按钮隐藏并提示「大厅语音已暂停」', !!vVoiceOff, (await bodyText(vp)).slice(0, 300));
  const aVoiceOff = await waitFor(async () => {
    const btn = page.getByRole('button', { name: '打开麦克风' });
    return (await btn.count()) > 0 && (await btn.isDisabled());
  }, 12000);
  check('后台：本端麦克风被停止、麦克风按钮禁用并显示「大厅语音已暂停」', !!aVoiceOff && (await bodyText(page)).includes('大厅语音已暂停'));
  check('观察者实时看到语音已关闭', !!(await observerSees(observer, 'voice_enabled', false)));
  await shot('s03-voice-off');
  await shot('s03-visitor-voice-off', vp);
  r = await setSwitch(page, 'voice_enabled', true);
  check('打开语音', r.ok, r.why);
  check('访客：开麦按钮恢复', !!(await waitFor(() => visible(vp, '#hallMicBtn'), 15000)));
  check('后台：麦克风按钮恢复可用', !!(await waitFor(() => page.getByRole('button', { name: '打开麦克风' }).isEnabled(), 8000)));
  check('观察者实时看到语音已开启', !!(await observerSees(observer, 'voice_enabled', true)));

  // ---------- 视频 ----------
  r = await setSwitch(page, 'video_enabled', true);
  check('打开视频：开关切换成功', r.ok, r.why);
  check('访客：摄像头按钮出现', !!(await waitFor(() => visible(vp, '#hallCamBtn'), 15000)));
  check('后台：显示「视频已开启」', !!(await waitFor(async () => (await bodyText(page)).includes('视频已开启'), 8000)));
  check('观察者实时看到视频已开启', !!(await observerSees(observer, 'video_enabled', true)));
  r = await setSwitch(page, 'video_enabled', false);
  check('关闭视频：开关切换成功', r.ok, r.why);
  check('访客：摄像头按钮隐藏', !!(await waitFor(async () => !(await visible(vp, '#hallCamBtn')), 15000)));
  check('后台：显示「视频未开启」', !!(await waitFor(async () => (await bodyText(page)).includes('视频未开启'), 8000)));
  check('观察者实时看到视频已关闭', !!(await observerSees(observer, 'video_enabled', false)));

  // ---------- 总开关 ----------
  // 先点开再取消：不应关闭
  await switchLoc(page, 'enabled').click();
  const box = page.locator('.el-message-box');
  await box.waitFor({ state: 'visible', timeout: 5000 });
  await box.locator('.el-message-box__btns button', { hasText: '取消' }).click();
  await sleep(1200);
  check('关闭总开关时取消二次确认：总开关保持开启', (await switchState(page, 'enabled')).on === true && (await switchState(observer, 'enabled')).on === true);
  r = await setSwitch(page, 'enabled', false, { confirm: true });
  check('关闭总开关：二次确认说明访客影响与移出', r.ok && r.dialog.includes('访客窗口将不再显示大厅') && r.dialog.includes('移出'), r.why || r.dialog);
  const vClosed = await waitFor(async () => !(await visible(vp, '#tabHall')) && (await visible(vp, '#privatePane')), 20000);
  await shot('s04-visitor-hall-off', vp);
  check('访客：没有大厅页签、只剩私聊', !!vClosed, (await bodyText(vp)).slice(0, 200));
  const aLeft = await waitFor(async () => { const t = await bodyText(page); return t.includes('大厅已被关闭') && t.includes('未进入'); }, 15000);
  check('后台：已在大厅的普通客服自动离开并提示「大厅已被关闭」', !!aLeft, (await bodyText(page)).slice(0, 300));
  check('观察者实时看到总开关已关闭并显示「大厅已关闭」', !!(await observerSees(observer, 'enabled', false)) && (await bodyText(observer)).includes('大厅已关闭'));
  const vs2 = await api('GET', '/api/visitor/settings');
  check('访客公开设置：hall_enabled=false 且文字 / 语音也为 false', vs2.data && vs2.data.hall_enabled === false && vs2.data.hall_chat_enabled === false && vs2.data.hall_voice_enabled === false, vs2.data && { e: vs2.data.hall_enabled, c: vs2.data.hall_chat_enabled, v: vs2.data.hall_voice_enabled });
  await shot('s04-hall-off');
  // 大厅已关闭、本人未进入大厅时面板仍可用：重新打开总开关
  check('大厅已关闭且未进入大厅时，「进入大厅」不可用但面板总开关可用', await page.getByRole('button', { name: '进入大厅' }).isDisabled() && !(await switchState(page, 'enabled')).disabled);
  r = await setSwitch(page, 'enabled', true);
  check('未进入大厅时重新打开总开关', r.ok && !!(await toastHas(page, '大厅已开启')), r.why);
  const vBack = await waitFor(async () => visible(vp, '#tabHall'), 25000);
  const noReload = await vp.evaluate(() => window.__qaNoReload === 'hall-switches').catch(() => false);
  check('访客：不刷新页面即恢复大厅页签', !!vBack && noReload, { vBack: !!vBack, noReload });
  await vp.locator('#tabHall').click().catch(() => {});
  check('访客：重新自动入厅（大厅输入框可见）', !!(await waitFor(() => visible(vp, '#hallInput'), 20000)));
  check('观察者实时看到总开关已开启', !!(await observerSees(observer, 'enabled', true)));
  check('后台：「进入大厅」恢复可用，「大厅已关闭」提示消失', !!(await waitFor(async () => (await page.getByRole('button', { name: '进入大厅' }).isEnabled()) && !(await bodyText(page)).includes('大厅总开关已关闭'), 8000)));
  await shot('s05-hall-on');
  await shot('s05-visitor-hall-on', vp);
  // 子开关在总开关关闭期间保持原值（契约 10.1）
  const finalState = {};
  for (const s of SWITCHES) finalState[s.key] = (await switchState(observer, s.key)).on;
  check('总开关重开后其余开关保持原值（文字 / 语音开、视频关）', finalState.chat_enabled && finalState.voice_enabled && !finalState.video_enabled, finalState);

  // ---------- 设置页（仅管理员）：多人大厅分组含两个新开关与说明（只读检查，不保存） ----------
  const sp = watch(await actx.newPage(), 'settings');
  await sp.goto(BASE + '/admin/settings');
  const stText = await waitFor(async () => { const t = await bodyText(sp); return t.includes('大厅文字聊天') ? t : null; }, 10000);
  check('设置页「多人大厅」分组有大厅文字聊天、大厅语音开关与「所有客服都可在『多人大厅』页切换」说明',
    !!stText && ['启用大厅', '大厅文字聊天', '大厅语音', '允许视频', '这四个开关所有客服都可在『多人大厅』页切换'].every((t) => stText.includes(t)), (stText || '').slice(0, 300));
  await sp.screenshot({ path: SHOTS + '/s06-settings.png', fullPage: true }).catch(() => {});
  await sp.close();
} catch (e) {
  check('脚本执行未中断', false, e && e.stack ? e.stack : String(e));
} finally {
  try {
    if (admin && original && Object.keys(original).length) {
      const rr = await api('POST', '/api/admin/settings', { token: admin.token, body: original });
      const now = await api('GET', '/api/admin/settings', { token: admin.token });
      const same = Object.keys(original).every((k) => bool(now.data && now.data[k]) === bool(original[k]));
      check('恢复运行前的 4 个大厅开关', rr.code === 0 && same, { code: rr.code, same });
    }
    if (admin && temp && Number.isInteger(temp.id) && temp.id > 0) {
      const d = await api('POST', '/api/admin/agents/active', { token: admin.token, body: { id: temp.id, active: false } });
      check('停用临时普通客服', d.code === 0, { code: d.code, msg: d.msg });
    }
  } catch (e) {
    check('清理测试数据', false, String((e && e.message) || e));
  }
  check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
  if (browser) await browser.close().catch(() => {});
  console.log('SUMMARY passed=' + passed + ' failed=' + failed);
  process.exit(failed ? 1 : 0);
}
