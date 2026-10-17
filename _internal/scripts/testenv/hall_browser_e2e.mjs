#!/usr/bin/env node
// [119] 多人大厅（访客网页窗口）真实浏览器联调：Chrome 无界面 + 假媒体设备 + 本机测试后端（含测试用 LiveKit）。
// 前置：admin_e2e_proxy.mjs 已在 18090 端口运行并挂载 widget/public（第三个参数），测试后端监听 18080，
//       测试 LiveKit 已启动且后端 LIVEKIT_PUBLIC_URL 指向浏览器可达的地址（http 页面允许 ws://）。
// 用法：ADMIN_BOOTSTRAP_PASSWORD=*** node hall_browser_e2e.mjs   （凭证只从环境变量读取）
// 覆盖：两个访客自动入厅、默认关麦、A 开麦后 B 收到音频轨道且 inbound-rtp 字节持续增长（音量只作信息项）、开麦名额满提示、
//       视频开关切换后摄像头按钮出现/消失、文字与图片在另一访客处出现、刷新后看到最近 50 条、被移出提示、
//       点击收听提示（大厅里没人说话时靠入厅后的主动探测）；附加：多标签页接管、宿主页大厅状态胶囊。
// [123] 客服可控的大厅开关（POST /api/agent/hall/switches，契约第 10 节）：文字聊天关 → 输入框/附件/发送隐藏并提示、历史照常、
//       客服 hall_chat 被拒「大厅聊天已关闭」；语音关 → 开麦按钮隐藏、正在开麦的立即停止且不再调用 /hall/mic；
//       总开关关 → 无大厅页签只剩私聊、LiveKit 断开且不重连、宿主页胶囊隐藏、新打开的页面不加载 LiveKit，
//       不刷新重新打开后页签恢复并自动入厅。脚本开始时记录四个开关原值，结束时恢复。
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
// [123] 大厅开关（任何启用中的客服账号可调用）：记录原值，测试前打开文字聊天与语音，结束时恢复
const setSwitches = (kv) => api('POST', '/api/agent/hall/switches', kv);
const swGet = await api('GET', '/api/agent/hall/switches');
const savedSwitches = swGet && swGet.code === 0 && swGet.data
  ? { enabled: swGet.data.enabled === true, chat_enabled: swGet.data.chat_enabled === true, voice_enabled: swGet.data.voice_enabled === true, video_enabled: swGet.data.video_enabled === true }
  : null;
check('读取大厅开关原值（GET /api/agent/hall/switches）', !!savedSwitches, swGet);
const sw0 = await setSwitches({ chat_enabled: true, voice_enabled: true });
check('测试前打开大厅文字聊天与语音开关', sw0.code === 0 && sw0.data && sw0.data.chat_enabled === true && sw0.data.voice_enabled === true, sw0);

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
// [123] 记录每个页面发出的大厅访客接口请求（只记路径、方法与请求体前 200 字，不含令牌），用于验证「不再调用 /hall/mic」「不再重连」
const hallReqs = {};
const hallReqCount = (tag, part) => (hallReqs[tag] || []).filter((x) => x.url.includes(part)).length;
const watch = (page, tag) => {
  hallReqs[tag] = [];
  page.on('request', (r) => { if (r.url().includes('/api/visitor/hall/')) hallReqs[tag].push({ url: r.url().replace(/\?.*$/, ''), method: r.method(), body: (r.postData() || '').slice(0, 200) }); });
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

  // 7.5) [123] 客服可控的大厅开关（POST /api/agent/hall/switches）
  const shown = (page, id) => page.evaluate((i) => {
    const e = document.getElementById(i);
    return !!e && !e.hidden && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0;
  }, id);
  // 7.5.1 文字聊天：关闭 → 输入框、附件、发送隐藏并提示，历史照常；客服 hall_chat 被拒；打开 → 恢复
  const histB = await pageB.evaluate(() => document.querySelectorAll('#hallMsgs .hm').length);
  let swr = await setSwitches({ chat_enabled: false });
  check('客服关闭大厅文字聊天（switches 接口）', swr.code === 0 && swr.data && swr.data.chat_enabled === false, swr);
  const chatOffA = await until(pageA, () => {
    const p = document.getElementById('hallChatPaused');
    return !!p && !p.hidden && document.getElementById('hallInputWrap').hidden && document.getElementById('hallFileBtn').hidden && document.getElementById('hallSendBtn').hidden;
  }, null, 10000);
  check('文字聊天关闭后 A 的输入框、附件、发送按钮隐藏', chatOffA, await text(pageA, '#hallPane footer'));
  check('A 显示「大厅文字聊天已暂停」', (await shown(pageA, 'hallChatPaused')) && (await text(pageA, '#hallChatPaused')).includes('大厅文字聊天已暂停'));
  check('文字聊天关闭后输入框在页面上不可见', !(await shown(pageA, 'hallInput')) && !(await shown(pageA, 'hallFileBtn')));
  const chatOffB = await until(pageB, () => !document.getElementById('hallChatPaused').hidden && document.getElementById('hallInputWrap').hidden, null, 10000);
  check('B 同样切换为文字聊天暂停', chatOffB);
  const histB2 = await pageB.evaluate(() => document.querySelectorAll('#hallMsgs .hm').length);
  check('文字聊天关闭时历史消息照常显示', histB > 0 && histB2 === histB, { before: histB, after: histB2 });
  check('文字聊天关闭时开麦按钮仍在（语音不受影响）', await shown(pageA, 'hallMicBtn'));
  const rejId = randomUUID();
  agentWS.send(JSON.stringify({ type: 'hall_chat', id: rejId, content: '关闭时发送 ' + marker }));
  let rej = null;
  for (let t = 0; t < 20 && !rej; t++) { rej = agentEvents.find((e) => e.id === rejId && (e.type === 'error' || e.type === 'hall_ack')) || null; if (!rej) await sleep(250); }
  check('聊天关闭时客服 hall_chat 被拒（error「大厅聊天已关闭」）', !!rej && rej.type === 'error' && String(rej.content || '').includes('大厅聊天已关闭'), rej);
  await shot(pageA, 'h11-chat-paused');
  swr = await setSwitches({ chat_enabled: true });
  check('客服重新打开大厅文字聊天', swr.code === 0 && swr.data && swr.data.chat_enabled === true, swr);
  const chatBackA = await until(pageA, () => document.getElementById('hallChatPaused').hidden && !document.getElementById('hallInputWrap').hidden && !document.getElementById('hallSendBtn').hidden && !document.getElementById('hallFileBtn').hidden, null, 10000);
  check('文字聊天恢复后 A 的输入框与附件按钮恢复', chatBackA);
  await pageA.fill('#hallInput', '恢复后发言 ' + marker);
  await pageA.press('#hallInput', 'Enter');
  check('文字聊天恢复后 A 发的文字出现在 B 的大厅', await until(pageB, (m) => document.getElementById('hallMsgs').innerText.includes('恢复后发言 ' + m), marker, 10000));

  // 7.5.2 语音：A 正在开麦时关闭 → 立即停止、开麦按钮隐藏、提示暂停、不再调用 /hall/mic；打开 → 恢复开麦按钮
  await pageA.click('#hallMicBtn');
  const aMic2 = await until(pageA, () => document.getElementById('hallMicBtn').getAttribute('aria-pressed') === 'true', null, 15000);
  check('语音开关测试前 A 开麦', aMic2, await text(pageA, '#hallToast'));
  await until(pageB, () => document.querySelectorAll('#hallPeople .hp.is-mic-on').length >= 1, null, 8000);
  const micReqs0 = hallReqCount('A', '/hall/mic');
  swr = await setSwitches({ voice_enabled: false });
  check('客服关闭大厅语音', swr.code === 0 && swr.data && swr.data.voice_enabled === false, swr);
  const voiceOffA = await until(pageA, () => {
    const b = document.getElementById('hallMicBtn'), p = document.getElementById('hallVoicePaused');
    return b.hidden && b.getAttribute('aria-pressed') === 'false' && !!p && !p.hidden;
  }, null, 10000);
  check('语音关闭后 A 的开麦按钮隐藏、麦克风立即停止', voiceOffA, await text(pageA, '#hallPane footer'));
  check('A 显示「大厅语音已暂停」', (await shown(pageA, 'hallVoicePaused')) && (await text(pageA, '#hallVoicePaused')).includes('大厅语音已暂停'));
  check('B 的开麦按钮同样隐藏', await until(pageB, () => document.getElementById('hallMicBtn').hidden && !document.getElementById('hallVoicePaused').hidden, null, 10000));
  check('B 的参与者条不再显示 A 开麦', await until(pageB, () => document.querySelectorAll('#hallPeople .hp.is-mic-on').length === 0, null, 10000));
  await sleep(2500);
  const micReqsAfter = (hallReqs.A || []).filter((x) => x.url.includes('/hall/mic')).slice(micReqs0);
  check('语音关闭后 A 不再调用 /hall/mic', micReqsAfter.length === 0, micReqsAfter);
  check('语音关闭后 A、B 仍在大厅（收听不受影响）', /人在线/.test(await text(pageA, '#hallStatusText')) && /人在线/.test(await text(pageB, '#hallStatusText')));
  check('语音关闭时文字聊天不受影响', await shown(pageA, 'hallInput'));
  await shot(pageA, 'h12-voice-paused');
  swr = await setSwitches({ voice_enabled: true });
  check('客服重新打开大厅语音', swr.code === 0 && swr.data && swr.data.voice_enabled === true, swr);
  const voiceBack = (await until(pageA, () => !document.getElementById('hallMicBtn').hidden && document.getElementById('hallVoicePaused').hidden, null, 10000)) &&
    (await until(pageB, () => !document.getElementById('hallMicBtn').hidden && document.getElementById('hallVoicePaused').hidden, null, 10000));
  check('语音恢复后 A、B 的开麦按钮恢复', voiceBack);

  // 7.5.3 总开关：关闭 → 无大厅页签只剩私聊、LiveKit 断开且不重连、宿主页胶囊隐藏、新页面不加载 LiveKit；
  //       不刷新页面重新打开 → 页签恢复并自动入厅
  const ctxH = await newContext(browser, { ...ctxOpts, viewport: { width: 1280, height: 800 } });
  const pageH = watch(await ctxH.newPage(), 'H');
  await pageH.goto(BASE + '/widget/demo.html');
  const chipShown = () => until(pageH, () => { const c = document.getElementById('__cs_widget_hall__'); return !!c && c.style.display === 'flex'; }, null, 25000);
  check('总开关测试前宿主页显示大厅状态胶囊', await chipShown());
  await pageA.evaluate(() => { window.__csNoReload = 'A'; });
  await pageB.evaluate(() => { window.__csNoReload = 'B'; });
  swr = await setSwitches({ enabled: false });
  check('客服关闭大厅总开关', swr.code === 0 && swr.data && swr.data.enabled === false, swr);
  const onlyPrivate = (page) => until(page, () => document.getElementById('tabs').hidden && document.getElementById('hallPane').hidden && !document.getElementById('privatePane').hidden, null, 10000);
  const closedA = await onlyPrivate(pageA);
  const closedB = await onlyPrivate(pageB);
  check('总开关关闭后 A、B 没有大厅页签、只显示私聊', closedA && closedB);
  check('总开关关闭后宿主页大厅状态胶囊隐藏', await until(pageH, () => { const c = document.getElementById('__cs_widget_hall__'); return !c || c.style.display === 'none'; }, null, 10000));
  const pcsClosed = await until(pageA, () => (window.__pcs || []).every((pc) => pc.connectionState === 'closed' || pc.connectionState === 'failed'), null, 10000);
  check('总开关关闭后 A 的 LiveKit 媒体连接断开', pcsClosed, await pageA.evaluate(() => (window.__pcs || []).map((pc) => pc.connectionState)));
  const joinsA0 = hallReqCount('A', '/hall/join'), joinsB0 = hallReqCount('B', '/hall/join');
  await sleep(6000);
  check('总开关关闭后 A、B 不再重连（6 秒内无入场请求）', hallReqCount('A', '/hall/join') === joinsA0 && hallReqCount('B', '/hall/join') === joinsB0,
    { a: hallReqCount('A', '/hall/join') - joinsA0, b: hallReqCount('B', '/hall/join') - joinsB0 });
  await shot(pageA, 'h13-hall-off');
  // 关闭期间新访客打开页面：与上线前一致，没有大厅页签、不请求入场、不加载 LiveKit 组件
  const ctxE = await newContext(browser);
  const pageE = watch(await ctxE.newPage(), 'E');
  await pageE.goto(CHAT);
  await until(pageE, () => !document.getElementById('privatePane').hidden && (document.getElementById('status').textContent || '') !== '连接中…', null, 15000);
  await sleep(3000);
  const eOff = await pageE.evaluate(() => ({ tabsHidden: document.getElementById('tabs').hidden, privateShown: !document.getElementById('privatePane').hidden, lkScripts: document.querySelectorAll('script[src*="livekit-client"]').length, lkGlobal: typeof window.LivekitClient }));
  check('总开关关闭时新打开的页面没有大厅页签、不加载 LiveKit 组件', eOff.tabsHidden && eOff.privateShown && eOff.lkScripts === 0 && eOff.lkGlobal === 'undefined', eOff);
  check('总开关关闭时新打开的页面不请求入场', hallReqCount('E', '/hall/join') === 0, hallReqs.E);
  await pageE.evaluate(() => { window.__csNoReload = 'E'; });
  swr = await setSwitches({ enabled: true });
  check('客服重新打开大厅总开关', swr.code === 0 && swr.data && swr.data.enabled === true, swr);
  const tabsBack = async (page) => until(page, () => !document.getElementById('tabs').hidden, null, 10000);
  const backA = await tabsBack(pageA), backB = await tabsBack(pageB), backE = await tabsBack(pageE);
  check('不刷新页面：A、B、E 的大厅页签恢复', backA && backB && backE);
  const [liveA2, liveB2, liveE2] = await Promise.all([live(pageA, 30000), live(pageB, 30000), live(pageE, 30000)]);
  check('总开关恢复后 A、B、E 自动入厅', liveA2 && liveB2 && liveE2, { a: await text(pageA, '#hallStatusText'), b: await text(pageB, '#hallStatusText'), e: await text(pageE, '#hallStatusText') });
  const marks = await Promise.all([pageA, pageB, pageE].map((p) => p.evaluate(() => window.__csNoReload || '')));
  check('总开关关闭与恢复过程中页面没有刷新', marks.join(',') === 'A,B,E', marks);
  check('关闭前停留在大厅页的 A 恢复后回到大厅页', await pageA.evaluate(() => !document.getElementById('hallPane').hidden && document.getElementById('privatePane').hidden));
  const eOn = await pageE.evaluate(() => ({ privateShown: !document.getElementById('privatePane').hidden, lkScripts: document.querySelectorAll('script[src*="livekit-client"]').length }));
  check('E（打开时只有私聊）恢复后留在私聊页，LiveKit 组件只加载一次', eOn.privateShown && eOn.lkScripts === 1, eOn);
  check('E 只入厅一次（无重复初始化）', hallReqCount('E', '/hall/join') === 1, hallReqs.E);
  check('总开关恢复后宿主页大厅状态胶囊重新出现', await chipShown());
  await shot(pageA, 'h14-hall-back');
  await ctxE.close();
  await ctxH.close();

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
  if (savedSwitches) {
    const rsw = await setSwitches(savedSwitches).catch((e) => ({ code: -1, msg: String(e) }));
    check('恢复测试前的大厅开关（总开关 / 文字聊天 / 语音 / 视频）', rsw.code === 0, rsw);
  }
  check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
  await browser.close().catch(() => {});
  await gestureBrowser.close().catch(() => {});
}
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
