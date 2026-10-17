#!/usr/bin/env node
// [119] 多人大厅（访客网页窗口）真实浏览器联调：Chrome 无界面 + 假媒体设备 + 本机测试后端（含测试用 LiveKit）。
// 前置：admin_e2e_proxy.mjs 已在 18090 端口运行并挂载 widget/public（第三个参数），测试后端监听 18080，
//       测试 LiveKit 已启动且后端 LIVEKIT_PUBLIC_URL 指向浏览器可达的地址（http 页面允许 ws://）。
// 用法：ADMIN_BOOTSTRAP_PASSWORD=*** node hall_browser_e2e.mjs   （凭证只从环境变量读取）
// 覆盖：两个访客自动入厅、默认关麦、A 开麦后 B 收到音频轨道且 inbound-rtp 字节持续增长（音量只作信息项）、开麦名额满提示、
//       视频开关切换后摄像头按钮出现/消失、文字与图片在另一访客处出现、刷新后看到最近 50 条、被移出提示、
//       点击收听提示（大厅里没人说话时靠入厅后的主动探测）；附加：多标签页接管、宿主页大厅状态胶囊。
// 可选：HALL_E2E_TONE 指定假麦克风输入的测试音文件（默认 $HOME/cs-testenv/e2e/tone440.wav，不存在时退回 Chrome 默认假设备）。
import { chromium } from 'playwright-core';
import { randomUUID } from 'node:crypto';
import { mkdirSync, existsSync } from 'node:fs';

const BASE = process.env.HALL_E2E_BASE || 'http://127.0.0.1:18090';
const API = process.env.HALL_E2E_API || 'http://127.0.0.1:18080';
const CHROME = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const SHOTS = (process.env.HOME || '.') + '/cs-testenv/shots/hall-e2e';
const CHAT = BASE + '/widget/chat.html?site=default';
mkdirSync(SHOTS, { recursive: true });

let passed = 0, failed = 0;
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + String(detail === undefined ? '' : (typeof detail === 'string' ? detail : JSON.stringify(detail))).slice(0, 400)); } };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const problems = [];
const marker = 'HALL' + randomUUID().slice(0, 6);

// ---------- 客服身份（接口 + 实时连接） ----------
if (!process.env.ADMIN_BOOTSTRAP_PASSWORD) { console.log('FAIL 缺少环境变量 ADMIN_BOOTSTRAP_PASSWORD'); process.exit(2); }
const login = await (await fetch(API + '/api/agent/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: process.env.QA_ADMIN_USER || 'qa_admin', password: process.env.ADMIN_BOOTSTRAP_PASSWORD }) })).json();
if (!login.token) { console.log('FAIL 客服登录失败 ' + JSON.stringify({ code: login.code, msg: login.msg })); process.exit(2); }
const auth = { Authorization: 'Bearer ' + login.token, 'Content-Type': 'application/json' };
const api = async (method, path, body) => (await fetch(API + path, { method, headers: auth, body: body === undefined ? undefined : JSON.stringify(body) })).json();
const setSettings = (kv) => api('POST', '/api/admin/settings', Object.fromEntries(Object.entries(kv).map(([k, v]) => [k, String(v)])));

const HALL_KEYS = ['hall_enabled', 'hall_video_enabled', 'hall_max_speakers', 'hall_max_cameras', 'hall_max_participants'];
const original = await api('GET', '/api/admin/settings');
const saved = {};
for (const k of HALL_KEYS) if (original && original.data && original.data[k] !== undefined && original.data[k] !== '') saved[k] = original.data[k];
const st0 = await setSettings({ hall_enabled: 'true', hall_video_enabled: 'false', hall_max_speakers: '6', hall_max_cameras: '2' });
check('测试前设置大厅开启、视频关闭、开麦上限 6', st0.code === 0, st0);

// ---------- 浏览器：假麦克风/摄像头；主浏览器允许无手势播放，另起一个“需要手势”的浏览器测点击收听 ----------
// Chrome 默认假麦克风接近静音，服务端检测不到“正在说话”；有 440Hz 测试音文件（10 秒，脚本外提供）时用它作为假麦克风输入
const TONE = process.env.HALL_E2E_TONE || ((process.env.HOME || '.') + '/cs-testenv/e2e/tone440.wav');
const fakeMedia = ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'];
if (existsSync(TONE)) fakeMedia.push('--use-file-for-fake-audio-capture=' + TONE);
else console.log('INFO 未找到测试音文件 ' + TONE + '，退回 Chrome 默认假麦克风（“正在说话”高亮可能检测不到）');
const browser = await chromium.launch({ executablePath: CHROME, headless: true, args: [...fakeMedia, '--autoplay-policy=no-user-gesture-required'] });
const gestureBrowser = await chromium.launch({ executablePath: CHROME, headless: true, args: [...fakeMedia, '--autoplay-policy=user-gesture-required'] });
const ctxOpts = { viewport: { width: 400, height: 720 }, locale: 'zh-CN', timezoneId: 'Asia/Shanghai', permissions: ['microphone', 'camera'] };
// 收集页面内的 RTCPeerConnection 实例（window.__pcs），用 getStats 读 inbound-rtp 判断是否真的收到音频数据
const PC_HOOK = () => {
  const Native = window.RTCPeerConnection;
  if (!Native || Native.__csWrapped) return;
  window.__pcs = [];
  const Wrapped = function (...args) { const pc = new Native(...args); window.__pcs.push(pc); return pc; };
  Wrapped.prototype = Native.prototype;
  Object.setPrototypeOf(Wrapped, Native);
  Wrapped.__csWrapped = true;
  window.RTCPeerConnection = Wrapped;
};
const newContext = async (b, opts = ctxOpts) => { const c = await b.newContext(opts); await c.addInitScript(PC_HOOK); return c; };
const inboundAudioBytes = (page) => page.evaluate(async () => {
  let total = 0;
  for (const pc of (window.__pcs || [])) {
    if (pc.connectionState === 'closed') continue;
    try {
      const stats = await pc.getStats();
      stats.forEach((r) => { if (r.type === 'inbound-rtp' && (r.kind === 'audio' || r.mediaType === 'audio')) total += r.bytesReceived || 0; });
    } catch {}
  }
  return total;
});
const watch = (page, tag) => {
  page.on('pageerror', (e) => problems.push(tag + ' pageerror: ' + e.message));
  page.on('response', (r) => { if (r.url().includes('/api/') && r.status() >= 500) problems.push(tag + ' http ' + r.status() + ' ' + r.url()); });
  return page;
};
const ctxA = await newContext(browser);
const ctxB = await newContext(browser);
const pageA = watch(await ctxA.newPage(), 'A');
const pageB = watch(await ctxB.newPage(), 'B');
const shot = (page, n) => page.screenshot({ path: SHOTS + '/' + n + '.png' }).catch(() => {});
const until = (page, fn, arg, timeout = 10000) => page.waitForFunction(fn, arg, { timeout }).then(() => true).catch(() => false);
const live = (page, timeout = 25000) => until(page, () => /人在线/.test((document.getElementById('hallStatusText') || {}).textContent || ''), null, timeout);
const text = (page, sel) => page.locator(sel).innerText().catch(() => '');
const vidOf = (page) => page.evaluate(() => { try { return JSON.parse(localStorage.getItem('cs_visitor_default')).visitor_id; } catch { return ''; } });

try {
  // 1) 两个访客自动入厅、默认大厅页
  await pageA.goto(CHAT);
  await pageB.goto(CHAT);
  const [aLive, bLive] = await Promise.all([live(pageA), live(pageB)]);
  check('访客 A 打开页面后自动进入大厅', aLive, await text(pageA, '#hallPane'));
  check('访客 B 打开页面后自动进入大厅', bLive, await text(pageB, '#hallPane'));
  check('默认显示「大厅」页签', await pageA.evaluate(() => document.getElementById('tabHall').getAttribute('aria-selected') === 'true' && !document.getElementById('hallPane').hidden && document.getElementById('privatePane').hidden));
  check('大厅显示隐私提示', (await text(pageA, '#hallScroll')).includes('大厅消息所有人可见，订单号、账号等隐私请到'));
  const bothSeen = await until(pageB, () => /2 人在线|[3-9] 人在线|\d{2,} 人在线/.test(document.getElementById('hallStatusText').textContent), null, 15000);
  check('B 的大厅在线人数包含 A（≥2）', bothSeen, await text(pageB, '#hallStatusText'));
  await shot(pageA, 'h01-joined-a');

  // 2) 默认关麦
  const micOffA = await pageA.evaluate(() => document.getElementById('hallMicBtn').getAttribute('aria-pressed') === 'false');
  const micOffB = await pageB.evaluate(() => document.getElementById('hallMicBtn').getAttribute('aria-pressed') === 'false');
  check('A、B 默认关麦', micOffA && micOffB);
  check('B 端看到 A 未开麦（参与者条无开麦标记）', await pageB.evaluate(() => document.querySelectorAll('#hallPeople .hp.is-mic-on').length === 0));

  // 3) A 开麦 → B 收到音频轨道且有音量
  await pageA.click('#hallMicBtn');
  const aMic = await until(pageA, () => document.getElementById('hallMicBtn').getAttribute('aria-pressed') === 'true', null, 15000);
  check('A 点击开麦后麦克风开启', aMic, await text(pageA, '#hallToast'));
  const bTrack = await until(pageB, () => [...document.querySelectorAll('#hallAudioSink audio')].some((a) => a.srcObject && a.srcObject.getAudioTracks().length > 0), null, 15000);
  check('B 收到 A 的音频轨道', bTrack);
  // 判定标准：B 端 inbound-rtp(audio) 的 bytesReceived 间隔 3 秒两次读数持续增长且 > 5000 字节
  const rx1 = await inboundAudioBytes(pageB);
  await sleep(3000);
  const rx2 = await inboundAudioBytes(pageB);
  check('B 实际收到 A 的音频数据（inbound-rtp bytesReceived 3 秒内增长且 > 5000 字节）', rx2 > rx1 && rx2 > 5000, { first: rx1, second: rx2 });
  // 音量只作信息项：经 SSH 启动、没有音频输出设备的 Chrome 不解码远端音频（纯 WebRTC 对照实验同样 totalSamplesReceived=0），不计失败
  const level = await pageB.evaluate(async () => {
    const el = [...document.querySelectorAll('#hallAudioSink audio')].find((a) => a.srcObject && a.srcObject.getAudioTracks().length > 0);
    if (!el) return { ok: false, reason: 'no audio element' };
    const ctx = new AudioContext();
    try { await ctx.resume(); } catch {}
    const src = ctx.createMediaStreamSource(el.srcObject);
    const an = ctx.createAnalyser();
    an.fftSize = 2048;
    src.connect(an);
    const buf = new Float32Array(an.fftSize);
    let max = 0;
    for (let i = 0; i < 50; i++) {
      await new Promise((r) => setTimeout(r, 100));
      an.getFloatTimeDomainData(buf);
      let s = 0;
      for (let j = 0; j < buf.length; j++) s += buf[j] * buf[j];
      max = Math.max(max, Math.sqrt(s / buf.length));
    }
    const state = ctx.state;
    await ctx.close();
    return { ok: max > 0.001, max, state, paused: el.paused };
  });
  console.log('INFO B 收到的 A 音频音量（信息项，不计失败；无音频输出设备的环境不解码）：' + JSON.stringify(level));
  const micMark = await until(pageB, () => document.querySelectorAll('#hallPeople .hp.is-mic-on').length >= 1, null, 8000);
  check('B 的参与者条显示 A 已开麦', micMark);
  const speaking = await until(pageB, () => document.querySelectorAll('#hallPeople .hp.is-speaking').length >= 1, null, 8000);
  check('B 的参与者条显示 A 正在说话高亮', speaking);
  await shot(pageB, 'h02-b-hears-a');

  // 4) 开麦名额满：上限改为 1（A 已占用）→ B 开麦被拒并提示
  const st1 = await setSettings({ hall_max_speakers: '1' });
  check('设置开麦上限为 1', st1.code === 0, st1);
  await sleep(800);
  await pageB.click('#hallMicBtn');
  const fullTip = await until(pageB, () => (document.getElementById('hallToast').textContent || '').includes('开麦人数已满'), null, 10000);
  check('B 开麦时提示「开麦人数已满」', fullTip, await text(pageB, '#hallToast'));
  check('名额满时 B 仍为关麦', await pageB.evaluate(() => document.getElementById('hallMicBtn').getAttribute('aria-pressed') === 'false'));
  await shot(pageB, 'h03-mic-full');
  await pageA.click('#hallMicBtn');
  const aOff = await until(pageA, () => document.getElementById('hallMicBtn').getAttribute('aria-pressed') === 'false', null, 10000);
  check('A 关麦', aOff);
  await setSettings({ hall_max_speakers: '6' });

  // 5) 视频开关切换 → 摄像头按钮出现 / 消失
  check('视频关闭时不显示摄像头按钮', await pageA.evaluate(() => document.getElementById('hallCamBtn').hidden));
  await setSettings({ hall_video_enabled: 'true' });
  const camOnA = await until(pageA, () => !document.getElementById('hallCamBtn').hidden, null, 10000);
  const camOnB = await until(pageB, () => !document.getElementById('hallCamBtn').hidden, null, 10000);
  check('管理员开启视频后 A、B 出现摄像头按钮', camOnA && camOnB);
  await pageA.click('#hallCamBtn');
  const aCam = await until(pageA, () => document.getElementById('hallCamBtn').getAttribute('aria-pressed') === 'true', null, 15000);
  check('A 开启摄像头', aCam, await text(pageA, '#hallToast'));
  const bVideo = await until(pageB, () => document.querySelectorAll('#hallVideo .hv-tile video').length >= 1 && !document.getElementById('hallVideo').hidden, null, 15000);
  check('B 在大厅页看到 A 的视频（可见时才订阅）', bVideo);
  await shot(pageB, 'h04-video');
  await setSettings({ hall_video_enabled: 'false' });
  const camOffA = await until(pageA, () => document.getElementById('hallCamBtn').hidden, null, 10000);
  const camOffB = await until(pageB, () => document.getElementById('hallCamBtn').hidden, null, 10000);
  check('管理员关闭视频后摄像头按钮消失', camOffA && camOffB);
  const bVideoGone = await until(pageB, () => document.getElementById('hallVideo').hidden, null, 10000);
  check('关闭视频后 B 的视频画面消失', bVideoGone);

  // 6) 文字与图片在另一访客处出现
  await pageA.fill('#hallInput', '大厅文字 ' + marker);
  await pageA.press('#hallInput', 'Enter');
  const gotText = await until(pageB, (m) => document.getElementById('hallMsgs').innerText.includes('大厅文字 ' + m), marker, 10000);
  check('A 发的文字出现在 B 的大厅', gotText);
  const ackA = await until(pageA, () => !document.getElementById('hallMsgs').innerText.includes('发送中'), null, 8000);
  check('A 的消息收到服务端确认（不再显示发送中）', ackA);
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64');
  await pageA.setInputFiles('#hallFileInput', { name: 'hall-' + marker + '.png', mimeType: 'image/png', buffer: png });
  await pageA.click('#hallSendBtn');
  const gotImg = await until(pageB, () => [...document.querySelectorAll('#hallMsgs img.hm-img')].some((i) => i.src.includes('/files/') && i.complete && i.naturalWidth > 0), null, 15000);
  check('A 发的图片出现在 B 的大厅并成功加载', gotImg, await text(pageA, '#hallToast'));
  await shot(pageB, 'h05-text-image');

  // 7) 刷新后看到最近 50 条：客服连续发 52 条（客服消息带「客服」标识）
  const agentJoin = await api('POST', '/api/agent/hall/join');
  check('客服加入大厅（接口）', agentJoin.code === 0, { code: agentJoin.code, msg: agentJoin.msg });
  const agentWS = new WebSocket(API.replace('http', 'ws') + '/ws/agent', ['cs-auth', login.token]);
  const agentEvents = [];
  agentWS.onmessage = (m) => { try { agentEvents.push(JSON.parse(m.data)); } catch {} };
  await new Promise((r, j) => { agentWS.onopen = r; agentWS.onerror = j; });
  agentWS.send(JSON.stringify({ type: 'hall_subscribe', id: randomUUID() }));
  await sleep(500);
  // 后端客服限流 20 条/10 秒：每 600ms 一条（约 16.7 条/10 秒），全部发完后最多再等 15 秒收齐确认
  const seeded = [];
  for (let i = 0; i < 52; i++) {
    const id = randomUUID();
    seeded.push(id);
    agentWS.send(JSON.stringify({ type: 'hall_chat', id, content: '客服批量 ' + marker + ' #' + String(i).padStart(2, '0') }));
    await sleep(600);
  }
  const ackedIds = () => seeded.filter((id) => agentEvents.some((e) => e.type === 'hall_ack' && e.id === id));
  for (let t = 0; t < 30 && ackedIds().length < seeded.length; t++) await sleep(500);
  const acked = ackedIds();
  check('客服 52 条大厅消息全部被确认', acked.length === 52, { acked: acked.length, errors: agentEvents.filter((e) => e.type === 'error').slice(0, 3) });
  await pageB.reload();
  await live(pageB);
  await sleep(1500);
  const after = await pageB.evaluate((m) => {
    const nodes = [...document.querySelectorAll('#hallMsgs .hm')];
    const t = document.getElementById('hallMsgs').innerText;
    return { count: nodes.length, last: t.includes('客服批量 ' + m + ' #51'), first: t.includes('客服批量 ' + m + ' #00'), second: t.includes('客服批量 ' + m + ' #01'), badge: document.querySelectorAll('#hallMsgs .hm.hm-agent .hm-badge').length };
  }, marker);
  check('B 刷新后显示最近 50 条大厅消息', after.count === 50, after);
  check('最近 50 条包含最新一条、不含最早两条', after.last && !after.first && !after.second, after);
  check('客服消息带「客服」标识', after.badge >= 1, after);
  await shot(pageB, 'h06-recent-50');

  // 8) 点击收听提示（需要用户手势的浏览器）：此时大厅里没有人开麦，靠入厅后的主动探测显示提示
  // 新访客首次来访：没有授予麦克风/摄像头权限（已授权采集的站点 Chrome 会放行自动播放，不代表真实首访）
  const { permissions: _granted, ...firstVisitOpts } = ctxOpts;
  const ctxC = await newContext(gestureBrowser, firstVisitOpts);
  const pageC = watch(await ctxC.newPage(), 'C');
  await pageC.goto(CHAT);
  // 注意：Playwright 的 page.evaluate / waitForFunction 以「用户手势」身份执行，会让页面获得用户激活，
  // 从而掩盖自动播放限制。这里在入厅与探测完成前只用定位器（不执行页面脚本）等待，再读取提示。
  const btnC = pageC.locator('#hallNoticeBtn');
  let listenTip = false;
  for (let i = 0; i < 30 && !listenTip; i++) {
    await sleep(500);
    listenTip = (await btnC.isVisible().catch(() => false)) && ((await btnC.textContent().catch(() => '')) || '').trim() === '点击收听大厅';
  }
  check('浏览器限制自动播放时显示「点击收听大厅」', listenTip, await text(pageC, '#hallNotice'));
  await shot(pageC, 'h07-click-to-listen');
  if (listenTip) {
    await pageC.click('#hallNoticeBtn', { timeout: 5000 });
    const listened = await until(pageC, () => document.getElementById('hallNotice').hidden, null, 8000);
    check('点击后恢复播放、提示消失', listened, await text(pageC, '#hallNotice'));
  } else {
    check('点击后恢复播放、提示消失', false, '未出现提示，跳过点击');
  }
  await ctxC.close();

  // 9) 被移出提示
  const vidB = await vidOf(pageB);
  let identityB = '';
  for (let i = 0; i < 10 && !identityB; i++) {
    const state = await api('GET', '/api/agent/hall/state');
    const p = ((state.data && state.data.participants) || []).find((x) => x.vid === vidB);
    if (p) identityB = p.identity; else await sleep(500);
  }
  check('客服在大厅状态中找到访客 B', !!identityB, vidB ? 'vid found in page' : 'no vid');
  if (identityB) {
    const kick = await api('POST', '/api/agent/hall/moderate', { action: 'kick', identity: identityB, minutes: 1 });
    check('客服移出访客 B（1 分钟）', kick.code === 0, kick);
    const kicked = await until(pageB, () => !document.getElementById('hallNotice').hidden && document.getElementById('hallNoticeText').textContent.includes('移出'), null, 10000);
    check('B 显示被移出提示', kicked, await text(pageB, '#hallNotice'));
    check('被移出后 B 不能在大厅发言', await pageB.evaluate(() => document.getElementById('hallInput').disabled));
    await shot(pageB, 'h08-kicked');
    await pageB.reload();
    const kickedAgain = await until(pageB, () => (document.getElementById('hallNoticeText').textContent || '').includes('移出'), null, 15000);
    check('禁入期内刷新仍显示被移出（join 返回 40372）', kickedAgain, await text(pageB, '#hallNotice'));
  }

  // 10) 附加：同一访客打开第二个标签页 → 旧页显示「已在其他页面收听」
  const pageA2 = watch(await ctxA.newPage(), 'A2');
  await pageA2.goto(CHAT);
  const a2Live = await live(pageA2);
  const elsewhere = await until(pageA, () => (document.getElementById('hallNoticeText').textContent || '').includes('其他页面'), null, 15000);
  check('同一访客新标签页接管，旧页显示「已在其他页面收听」', a2Live && elsewhere, await text(pageA, '#hallNotice'));
  await shot(pageA, 'h09-elsewhere');
  await pageA2.close();

  // 11) 附加：宿主页悬浮按钮旁的大厅状态胶囊
  const ctxD = await newContext(browser, { ...ctxOpts, viewport: { width: 1280, height: 800 } });
  const pageD = watch(await ctxD.newPage(), 'D');
  await pageD.goto(BASE + '/widget/demo.html');
  const chip = await until(pageD, () => { const c = document.getElementById('__cs_widget_hall__'); return !!c && c.style.display === 'flex' && /大厅|收听/.test(c.textContent); }, null, 25000);
  check('宿主页显示大厅状态胶囊（在线人数 / 收听状态）', chip, await pageD.evaluate(() => (document.getElementById('__cs_widget_hall__') || {}).textContent || ''));
  await shot(pageD, 'h10-host-chip');
  await ctxD.close();

  // 清理：删除本次测试产生的大厅消息
  const del = async (id) => api('POST', '/api/agent/hall/messages/' + id + '/delete').catch(() => ({}));
  for (const id of acked) await del(id);
  const recent = await api('GET', '/api/agent/hall/messages?limit=50');
  for (const m of (recent.data || [])) if (String(m.content || '').includes(marker) || String(m.media_name || '').includes(marker)) await del(m.id);
  agentWS.close();
} catch (e) {
  check('脚本执行无异常', false, e && e.stack ? e.stack : String(e));
} finally {
  // 恢复测试前的大厅设置
  const restore = { hall_enabled: 'true', hall_video_enabled: 'false', hall_max_speakers: '6', hall_max_cameras: '2', ...saved };
  const rs = await setSettings(restore).catch((e) => ({ code: -1, msg: String(e) }));
  check('恢复测试前的大厅设置', rs.code === 0, rs);
  check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
  await browser.close().catch(() => {});
  await gestureBrowser.close().catch(() => {});
}
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
