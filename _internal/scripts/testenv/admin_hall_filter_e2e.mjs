#!/usr/bin/env node
// 管理后台「多人大厅 → 敏感词屏蔽」浏览器端到端联调（[122]，契约 _internal/reports/hall-contract.md 第 9 节）。
// 覆盖：管理员登录 → 设置页看到「敏感词屏蔽」卡片 → 修改自定义屏蔽词并保存 → 检测试用命中并显示 ** →
//       邮件通知改为本机测试邮件接收器并保存（已配置）→ 发送测试邮件成功 →
//       访客（真实 widget 页面 /widget/chat.html）在大厅发「加我微信 abc123456 看片」→
//       访客端与后台大厅页都只显示屏蔽后内容（不含 abc123456）→ 拦截记录表出现该条原文 →
//       结束时把敏感词设置、邮件设置（密码字段不传）与大厅开关恢复为运行前的值。
// 运行（Mac 测试环境）：ADMIN_BOOTSTRAP_PASSWORD=... node _internal/scripts/testenv/admin_hall_filter_e2e.mjs
// 前置：测试后端 18080、admin_e2e_proxy.mjs 18090（挂载 widget/public）、测试 LiveKit 已启动；
//       本机测试邮件接收器监听 127.0.0.1:${HALL_E2E_SMTP_PORT:-2525}（不加密，接受用户名 qa / 密码 qa-pass），且测试后端能以 127.0.0.1 访问到它。
// 可选环境变量：QA_ADMIN_USER（默认 qa_admin）、CS_E2E_BASE（默认 http://127.0.0.1:18090）、CS_E2E_API（默认 http://127.0.0.1:18080）、
//               CHROME_PATH、HALL_E2E_SMTP_PORT（默认 2525）、HALL_E2E_WAIT_MAIL=1（额外等待拦截邮件状态变为「已发送」，最长 150 秒）。
// 安全：只允许连接本机地址；凭证只从环境变量读取，不打印。
// 说明：接口永不返回 SMTP 密码，恢复邮件设置时不传密码字段，因此测试中填写的测试密码会保留在测试库里（脚本结束时打印提示）。
import { chromium } from 'playwright-core';
import { mkdirSync } from 'node:fs';
import { randomBytes } from 'node:crypto';

const BASE = process.env.CS_E2E_BASE || 'http://127.0.0.1:18090';
const API = process.env.CS_E2E_API || 'http://127.0.0.1:18080';
const USER = process.env.QA_ADMIN_USER || 'qa_admin';
const PASS = process.env.ADMIN_BOOTSTRAP_PASSWORD;
const CHROME = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const SHOTS = (process.env.HOME || '.') + '/cs-testenv/shots/admin-hall-filter-e2e';
const SMTP_PORT = Number(process.env.HALL_E2E_SMTP_PORT || 2525);
const WAIT_MAIL = process.env.HALL_E2E_WAIT_MAIL === '1';
const FILTER_KEYS = ['enabled', 'mode', 'categories', 'custom_words', 'allow_words', 'allow_domains'];
const MAIL_KEYS = ['enabled', 'to', 'from', 'smtp_host', 'smtp_port', 'smtp_security', 'smtp_username'];
const CATEGORIES = ['ad', 'link', 'porn', 'gambling', 'contact'];
// 测试邮件接收器参数（只用于本机测试环境）
const SMTP = { host: '127.0.0.1', port: SMTP_PORT, username: 'qa', password: 'qa-pass', to: 'qa@example.test', from: 'kelili-test@example.test' };

if (!PASS) { console.log('FAIL 缺少环境变量 ADMIN_BOOTSTRAP_PASSWORD'); process.exit(2); }
if (!Number.isInteger(SMTP_PORT) || SMTP_PORT < 1 || SMTP_PORT > 65535) { console.log('FAIL HALL_E2E_SMTP_PORT 必须是 1-65535 的整数'); process.exit(2); }
for (const [name, url] of [['CS_E2E_BASE', BASE], ['CS_E2E_API', API]]) {
  let host = '';
  try { host = new URL(url).hostname; } catch { /* 下方统一报错 */ }
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(host)) { console.log('FAIL ' + name + ' 只允许本机地址（127.0.0.1 / localhost）'); process.exit(2); }
}
mkdirSync(SHOTS, { recursive: true });

let passed = 0, failed = 0;
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + String(detail === undefined ? '' : (typeof detail === 'string' ? detail : JSON.stringify(detail))).slice(0, 400)); } };
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
const pick = (obj, keys) => Object.fromEntries(keys.map((k) => [k, obj ? obj[k] : undefined]));
// 随机标记只用天干汉字：不含数字、字母与常见敏感词，屏蔽后仍能用它定位消息
const STEMS = '甲乙丙丁戊己庚辛壬癸';
const rnd = (n) => Array.from(randomBytes(n), (b) => STEMS[b % STEMS.length]).join('');
const marker = '标记' + rnd(6);
const customWord = '测屏' + rnd(4);
const SECRET_PART = 'abc123456';
const visitorText = marker + ' 加我微信 ' + SECRET_PART + ' 看片';

// ---------- 接口登录：只用于读取/恢复设置与核对结果（界面操作全部走浏览器） ----------
const login = await (await fetch(API + '/api/agent/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: USER, password: PASS }) })).json().catch(() => null);
if (!login || !login.token) { console.log('FAIL 接口登录失败 ' + JSON.stringify({ code: login && login.code, msg: login && login.msg })); process.exit(2); }
const auth = { Authorization: 'Bearer ' + login.token, 'Content-Type': 'application/json' };
const api = async (method, path, body) => {
  const r = await fetch(API + path, { method, headers: auth, body: body === undefined ? undefined : JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  return { status: r.status, ...j };
};

const origFilterRes = await api('GET', '/api/admin/hall/filter');
const origMailRes = await api('GET', '/api/admin/hall/alert-mail');
const origSettingsRes = await api('GET', '/api/admin/settings');
check('测试前置：读取敏感词设置', origFilterRes.code === 0 && origFilterRes.data, { code: origFilterRes.code, msg: origFilterRes.msg });
check('测试前置：读取邮件设置（不含密码）', origMailRes.code === 0 && origMailRes.data && !('smtp_password' in origMailRes.data), { code: origMailRes.code, msg: origMailRes.msg });
if (origFilterRes.code !== 0 || origMailRes.code !== 0 || origSettingsRes.code !== 0) {
  console.log('SUMMARY passed=' + passed + ' failed=' + (failed + 1) + '（前置读取失败，未做任何修改）');
  process.exit(1);
}
const origFilter = pick(origFilterRes.data, FILTER_KEYS);
const origMail = pick(origMailRes.data, MAIL_KEYS);
const origHallEnabled = typeof origSettingsRes.data?.hall_enabled === 'string' ? origSettingsRes.data.hall_enabled : '';
console.log('INFO 运行前：屏蔽 ' + (origFilter.enabled ? '开启' : '关闭') + '，自定义词 ' + (origFilter.custom_words || []).length + ' 个；邮件通知 ' + (origMail.enabled ? '开启' : '关闭') + '，收件 ' + (origMail.to || []).length + ' 个，password_set=' + origMailRes.data.password_set);
const st0 = await api('POST', '/api/admin/settings', { hall_enabled: 'true' });
check('测试前置：开启大厅', st0.code === 0, { code: st0.code, msg: st0.msg });

const browser = await chromium.launch({ executablePath: CHROME, headless: true, args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', '--autoplay-policy=no-user-gesture-required'] });
const ctxOpts = { locale: 'zh-CN', timezoneId: 'Asia/Shanghai' };
try {
  // ---------- 管理员登录并打开设置页 ----------
  const adminCtx = await browser.newContext({ ...ctxOpts, viewport: { width: 1440, height: 1000 } });
  const page = watch(await adminCtx.newPage(), 'admin');
  const shot = (n, p = page) => p.screenshot({ path: SHOTS + '/' + n + '.png', fullPage: true }).catch(() => {});
  const toast = (text, timeout = 10000) => waitFor(async () => (await page.locator('.el-message', { hasText: text }).count()) > 0, timeout);
  const tid = (id) => page.locator('[data-testid="' + id + '"]');
  const isChecked = async (loc) => /\bis-checked\b/.test((await loc.getAttribute('class')) || '');

  await page.goto(BASE + '/admin/login');
  await page.getByPlaceholder('用户名').fill(USER);
  await page.getByPlaceholder('密码').fill(PASS);
  await page.getByRole('button', { name: '登 录' }).click();
  await page.waitForURL('**/admin/console', { timeout: 15000 });
  await page.goto(BASE + '/admin/settings');
  const card = tid('hall-filter-card');
  const cardSeen = await waitFor(() => card.isVisible(), 15000);
  const saveBtn = tid('hall-filter-save');
  const rulesLoaded = await waitFor(() => saveBtn.isEnabled(), 15000);
  await shot('f01-settings-card');
  const cardText = cardSeen ? await card.innerText() : '';
  check('设置页「多人大厅」分组下出现「敏感词屏蔽」卡片且规则已加载', !!cardSeen && !!rulesLoaded && cardText.includes('敏感词屏蔽'), cardText.slice(0, 200));
  check('卡片包含处理方式、5 个类别、三个词表、内置词库与检测试用',
    ['处理方式', '只把敏感部分替换为 **', '整条消息替换为 **', '广告', '超链接', '色情', '博彩', '联系方式', '自定义屏蔽词始终生效', '放行词', '放行域名', '内置词库', '检测试用', '邮件通知', '拦截记录'].every((t) => cardText.includes(t)),
    cardText.slice(0, 300));
  const customBefore = await tid('hall-filter-custom-words').inputValue();
  check('自定义屏蔽词输入框显示接口中的已保存词表', customBefore.split('\n').filter((s) => s.trim()).length === (origFilter.custom_words || []).length, customBefore.slice(0, 120));

  // ---------- 屏蔽规则：开启、按部分替换、勾选全部类别、追加自定义词并保存 ----------
  if (!(await isChecked(tid('hall-filter-enabled')))) await tid('hall-filter-enabled').click();
  await tid('hall-filter-mode').getByText('只把敏感部分替换为 **', { exact: true }).click();
  for (const c of CATEGORIES) {
    const cb = tid('hall-filter-cat-' + c);
    if (!(await isChecked(cb))) await cb.click();
  }
  await tid('hall-filter-custom-words').fill((customBefore.trim() ? customBefore.trim() + '\n' : '') + customWord);
  const dirtyShown = await waitFor(() => tid('hall-filter-dirty').isVisible(), 3000);
  check('修改后显示「有未保存的修改」', !!dirtyShown);
  await saveBtn.click();
  const savedToast = await toast('敏感词设置已保存');
  const dirtyGone = await waitFor(async () => (await tid('hall-filter-dirty').count()) === 0, 5000);
  await shot('f02-rules-saved');
  const afterSave = await api('GET', '/api/admin/hall/filter');
  check('保存敏感词设置成功（提示 + 接口回读包含新自定义词、开启、按部分替换、5 个类别）',
    !!savedToast && !!dirtyGone && afterSave.code === 0 && afterSave.data.enabled === true && afterSave.data.mode === 'mask' &&
    (afterSave.data.custom_words || []).includes(customWord) && CATEGORIES.every((c) => (afterSave.data.categories || []).includes(c)),
    { toast: !!savedToast, data: afterSave.data && pick(afterSave.data, ['enabled', 'mode', 'categories']) });

  // ---------- 检测试用：命中自定义词，屏蔽后显示 ** ----------
  await tid('hall-filter-test-input').fill('试用：这句话里有' + customWord + '，应当被屏蔽');
  await tid('hall-filter-test-run').click();
  const masked = await waitFor(async () => {
    const t = await tid('hall-filter-test-masked').innerText();
    return t.includes('**') && !t.includes(customWord) ? t : null;
  }, 10000);
  const verdict = await tid('hall-filter-test-verdict').innerText().catch(() => '');
  const resultText = await tid('hall-filter-test-result').innerText().catch(() => '');
  await shot('f03-filter-test');
  check('检测试用命中：屏蔽后结果含 ** 且不含自定义词', !!masked, resultText.slice(0, 200));
  check('检测试用显示「命中」与命中类别「自定义词」', verdict.includes('命中') && !verdict.includes('未命中') && resultText.includes('自定义词'), verdict + ' | ' + resultText.slice(0, 200));

  // ---------- 邮件通知：改为本机测试邮件接收器并保存 ----------
  await page.getByRole('tab', { name: '邮件通知' }).click();
  const mail = (id) => tid('hall-mail-' + id);
  const mailLoaded = await waitFor(() => mail('save').isEnabled(), 10000);
  check('邮件通知页签已加载', !!mailLoaded);
  const pwPlaceholderBefore = await mail('password').getAttribute('placeholder');
  check('密码框不回显密码，占位提示与 password_set 一致',
    (await mail('password').inputValue()) === '' && pwPlaceholderBefore === (origMailRes.data.password_set ? '已设置，留空不修改' : '未设置'), pwPlaceholderBefore);
  if (!(await isChecked(mail('enabled')))) await mail('enabled').click();
  await mail('to').fill(SMTP.to);
  await mail('from').fill(SMTP.from);
  await mail('host').fill(SMTP.host);
  await mail('security').getByText('不加密（仅本机）', { exact: true }).click();
  const portInput = mail('port').locator('input');
  await portInput.fill(String(SMTP.port));
  await portInput.blur();
  await mail('username').fill(SMTP.username);
  await mail('password').fill(SMTP.password);
  await mail('save').click();
  const mailSaved = await toast('邮件设置已保存');
  const configured = await waitFor(async () => (await mail('configured').innerText()).trim() === '已配置', 8000);
  const pwPlaceholderAfter = await mail('password').getAttribute('placeholder');
  await shot('f04-mail-saved');
  check('保存邮件设置成功且显示「已配置」', !!mailSaved && !!configured, await mail('configured').innerText().catch(() => ''));
  check('保存后密码框清空、占位显示「已设置，留空不修改」', (await mail('password').inputValue()) === '' && pwPlaceholderAfter === '已设置，留空不修改', pwPlaceholderAfter);
  const mailBack = await api('GET', '/api/admin/hall/alert-mail');
  const mb = mailBack.data || {};
  check('接口回读邮件设置与界面填写一致（且不返回密码）',
    mailBack.code === 0 && mb.enabled === true && JSON.stringify(mb.to) === JSON.stringify([SMTP.to]) && mb.from === SMTP.from &&
    mb.smtp_host === SMTP.host && mb.smtp_port === SMTP.port && mb.smtp_security === 'none' && mb.smtp_username === SMTP.username &&
    mb.password_set === true && mb.configured === true && !('smtp_password' in mb),
    pick(mb, [...MAIL_KEYS, 'password_set', 'configured']));

  // ---------- 发送测试邮件 ----------
  await mail('test').click();
  const testOk = await toast('测试邮件已发送', 65000);
  const testResult = await mail('test-result').innerText().catch(() => '');
  await shot('f05-mail-test');
  check('发送测试邮件成功提示（收件人 ' + SMTP.to + '）', !!testOk && testResult.includes(SMTP.to), testResult || '未出现成功提示');

  // ---------- 访客在大厅发敏感内容 ----------
  const vctx = await browser.newContext({ ...ctxOpts, viewport: { width: 420, height: 720 }, permissions: ['microphone', 'camera'] });
  const vp = watch(await vctx.newPage(), 'visitor');
  await vp.goto(BASE + '/widget/chat.html?site=default');
  const live = await vp.waitForFunction(() => /人在线/.test((document.getElementById('hallStatusText') || {}).textContent || ''), null, { timeout: 25000 }).then(() => true).catch(() => false);
  check('访客打开 widget 后自动进入大厅', live, await vp.locator('#hallPane').innerText().catch(() => ''));
  await vp.fill('#hallInput', visitorText);
  await vp.press('#hallInput', 'Enter');
  const visitorMasked = await waitFor(async () => {
    const t = await vp.locator('#hallMsgs').innerText();
    return t.includes(marker) && t.includes('**') && !t.includes(SECRET_PART);
  }, 15000);
  await vp.screenshot({ path: SHOTS + '/f06-visitor.png' }).catch(() => {});
  check('访客端只显示屏蔽后的内容（含 **，不含 ' + SECRET_PART + '）', !!visitorMasked, (await vp.locator('#hallMsgs').innerText().catch(() => '')).slice(-200));

  // ---------- 后台大厅页显示屏蔽后的内容 ----------
  const hallPage = watch(await adminCtx.newPage(), 'admin-hall');
  await hallPage.goto(BASE + '/admin/hall');
  const block = hallPage.locator('[data-hall-msg-id]', { hasText: marker });
  const adminMasked = await waitFor(async () => {
    if ((await block.count()) < 1) return false;
    const t = await block.first().innerText();
    return t.includes('**') && !t.includes(SECRET_PART);
  }, 15000);
  await hallPage.screenshot({ path: SHOTS + '/f07-admin-hall.png' }).catch(() => {});
  check('后台大厅页只显示屏蔽后的内容（不含 ' + SECRET_PART + '）', !!adminMasked, await block.first().innerText().catch(() => '未找到含标记的大厅消息'));
  const msgs = await api('GET', '/api/agent/hall/messages?limit=50');
  const stored = (Array.isArray(msgs.data) ? msgs.data : []).find((m) => typeof m.content === 'string' && m.content.includes(marker));
  check('大厅消息接口返回的也是屏蔽后内容（落库即屏蔽）', !!stored && !stored.content.includes(SECRET_PART) && stored.content.includes('**'), stored ? stored.content : '未找到');
  await hallPage.close();

  // ---------- 拦截记录表出现该条原文 ----------
  await page.bringToFront();
  await page.getByRole('tab', { name: '拦截记录' }).click();
  const table = tid('hall-filter-events');
  const row = table.locator('.el-table__body tr.el-table__row', { hasText: marker });
  const eventSeen = await waitFor(async () => {
    await tid('hall-filter-events-refresh').click();
    await sleep(800);
    return (await row.count()) > 0;
  }, 20000, 1500);
  const rowText = eventSeen ? await row.first().innerText() : '';
  await shot('f08-events');
  check('拦截记录表出现该条：原文含 ' + SECRET_PART + '，屏蔽后不含', !!eventSeen && rowText.includes(visitorText.slice(marker.length + 1)) && rowText.includes('**'), rowText.slice(0, 300));
  check('拦截记录显示类别「联系方式」与邮件状态标签', rowText.includes('联系方式') && /待发送|已发送|发送失败|未开启|已拉黑不发送/.test(rowText), rowText.slice(0, 300));
  const evApi = await api('GET', '/api/admin/hall/filter/events?limit=20');
  const ev = (Array.isArray(evApi.data) ? evApi.data : []).find((e) => typeof e.original === 'string' && e.original.includes(marker));
  check('拦截记录接口：原文完整、屏蔽后不含原文片段、带 vid 与类别', !!ev && ev.original === visitorText && !ev.masked.includes(SECRET_PART) && typeof ev.vid === 'string' && ev.vid !== '' && (ev.categories || []).includes('contact'),
    ev ? pick(ev, ['masked', 'categories', 'mail_status']) : '未找到');
  if (WAIT_MAIL && ev) {
    const sent = await waitFor(async () => {
      const r = await api('GET', '/api/admin/hall/filter/events?limit=20');
      const e = (Array.isArray(r.data) ? r.data : []).find((x) => x.id === ev.id);
      return e && e.mail_status === 'sent' ? e : null;
    }, 150000, 5000);
    check('拦截邮件发送成功（mail_status=sent）', !!sent, '超时未变为 sent');
  } else if (ev) {
    console.log('INFO 当前拦截邮件状态 ' + ev.mail_status + '（设置 HALL_E2E_WAIT_MAIL=1 可等待其变为 sent）');
  }
  // 加载更多按钮只在记录满一页时出现；有则点击一次，确认不报错
  if ((await tid('hall-filter-events-more').count()) > 0) {
    await tid('hall-filter-events-more').click();
    await sleep(1500);
    check('拦截记录「加载更多」可用（无错误提示）', (await page.locator('.el-message--error').count()) === 0);
  }
  await vctx.close();
} catch (e) {
  check('脚本执行未中断', false, e && e.stack ? e.stack : e);
} finally {
  // ---------- 恢复运行前的设置 ----------
  const rf = await api('POST', '/api/admin/hall/filter', origFilter).catch((e) => ({ code: -1, msg: String(e) }));
  const backF = await api('GET', '/api/admin/hall/filter').catch(() => ({}));
  check('恢复运行前的敏感词设置', rf.code === 0 && backF.code === 0 && JSON.stringify(pick(backF.data, FILTER_KEYS)) === JSON.stringify(origFilter), { code: rf.code, msg: rf.msg });
  // 运行前的值来自环境变量默认值且不完整时（发件邮箱 / 服务器为空、端口为 0、开启却没有收件人），保存接口会拒绝原样写回：
  // 这种情况下改为「关闭邮件通知」并保留测试接收器参数，避免测试环境继续向测试接收器发信，同时如实提示
  const restorable = typeof origMail.from === 'string' && origMail.from !== '' && typeof origMail.smtp_host === 'string' && origMail.smtp_host !== '' &&
    Number.isInteger(origMail.smtp_port) && origMail.smtp_port >= 1 && origMail.smtp_port <= 65535 &&
    ['ssl', 'starttls', 'none'].includes(origMail.smtp_security) && Array.isArray(origMail.to) && (!origMail.enabled || origMail.to.length > 0);
  if (restorable) {
    const rm = await api('POST', '/api/admin/hall/alert-mail', origMail).catch((e) => ({ code: -1, msg: String(e) }));
    const backM = await api('GET', '/api/admin/hall/alert-mail').catch(() => ({}));
    check('恢复运行前的邮件设置（密码字段不传）', rm.code === 0 && backM.code === 0 && JSON.stringify(pick(backM.data, MAIL_KEYS)) === JSON.stringify(origMail), { code: rm.code, msg: rm.msg });
  } else {
    const fallback = { enabled: false, to: Array.isArray(origMail.to) ? origMail.to : [], from: SMTP.from, smtp_host: SMTP.host, smtp_port: SMTP.port, smtp_security: 'none', smtp_username: typeof origMail.smtp_username === 'string' ? origMail.smtp_username : '' };
    const rm = await api('POST', '/api/admin/hall/alert-mail', fallback).catch((e) => ({ code: -1, msg: String(e) }));
    console.log('WARN 运行前的邮件设置不完整（来自空的环境变量默认值），接口无法原样写回；已改为关闭邮件通知：' + JSON.stringify(pick(origMail, ['enabled', 'from', 'smtp_host', 'smtp_port', 'smtp_security'])));
    check('邮件设置恢复兜底：关闭邮件通知', rm.code === 0, { code: rm.code, msg: rm.msg });
  }
  console.log('INFO 接口不返回 SMTP 密码，脚本无法恢复密码：测试库中已保存的 SMTP 密码现在是测试值，如需原密码请在后台重新填写');
  if (origHallEnabled) {
    const rh = await api('POST', '/api/admin/settings', { hall_enabled: origHallEnabled }).catch(() => ({ code: -1 }));
    check('恢复运行前的大厅开关', rh.code === 0, { code: rh.code, msg: rh.msg });
  }
  check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
  await browser.close();
  console.log('SUMMARY passed=' + passed + ' failed=' + failed);
  process.exit(failed ? 1 : 0);
}
