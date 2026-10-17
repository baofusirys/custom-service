/*
 * Custom Service · 多人大厅（访客端）· [119]
 *
 * 职责：大厅页签的界面、LiveKit 媒体连接、大厅消息收发，以及与宿主页 loader.js 的状态同步。
 * 依赖：hall-core.js（纯逻辑）、vendor/livekit-client-2.23.0.umd.js（空闲时异步加载，Apache-2.0）。
 * 与 chat.js 的关系：chat.js 负责私聊与访客 WS 连接，加载完成后调用 CSHall.attach(bridge) 把
 *   令牌、WS 发送、上传、大图查看、时间格式化等公共能力借给本模块；本模块不另建 WS 连接。
 * 契约：_internal/reports/hall-contract.md（[123] 第 10 节：客服可控的总开关 / 文字聊天 / 语音 / 视频开关）
 */
(function () {
  'use strict';
  var Core = window.CSHallCore;
  if (!Core) return;

  // ====== 常量 ======
  var LK_SRC = 'vendor/livekit-client-2.23.0.umd.js';
  // 子资源完整性校验（与 vendor 文件逐字节一致；由 widget/tests/hallAssets.test.mjs 校验）
  var LK_SRI = 'sha384-HkA7rQNo8H04l1p4zoXX1Cy+h9YoYp8LvKt4vfMFhv4KFk07UkrNwIUnyBum8Pw6';
  var MAX_VIDEO_TILES = 2;            // 契约：单个参与者最多同时订阅 2 路视频
  var PEOPLE_RENDER_MAX = 40;         // 参与者条最多渲染 40 个，其余以 +N 显示
  var ACK_TIMEOUT_MS = 10000;         // hall_chat 等待 hall_ack 的超时
  var PERMISSION_WAIT_MS = 5000;      // 开麦/开摄像头接口成功后等待 LiveKit 下发发布权限
  var REQUEST_TIMEOUT_MS = 15000;
  var MAX_JOIN_ATTEMPTS = 30;         // 连续失败上限：超过后停止自动重试，改为手动“重试”
  var JOIN_BACKOFF = { baseMs: 2000, factor: 2, maxMs: 60000, jitter: 0.2 };
  var BACKFILL_AFTER_MS = 3000;       // 距上次拉取历史超过 3 秒才在订阅后补拉，避免重复请求
  var AUTOPLAY_RESUME_WAIT_MS = 300;     // 自动播放探测：AudioContext.resume() 最长等待时间，仍为 suspended 即判为受限
  var AUTOPLAY_PROBE_TIMEOUT_MS = 1500;   // 自动播放探测：AudioContext 可运行时，静音试播 play() 的最长等待时间
  var GESTURE_EVENTS = ['pointerdown', 'keydown', 'touchend', 'click'];
  var SUBSCRIBE_MAX_ATTEMPTS = 5;     // hall_subscribe 被服务端拒绝（如“服务繁忙”）后的自动重试上限
  var SUBSCRIBE_BACKOFF = { baseMs: 3000, factor: 2, maxMs: 60000, jitter: 0.2 };
  var NEAR_BOTTOM_PX = 80;
  var TOAST_MS = 3500;
  var OWNED_IDS_MAX = 300;
  var TRACK_SOURCE_PROTO = { camera: 1, microphone: 2 };   // LiveKit 协议 TrackSource 数值

  var SVG_OPEN = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">';
  var ICON = {
    person: SVG_OPEN + '<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>',
    mic: SVG_OPEN + '<path d="M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><line x1="12" y1="19" x2="12" y2="22"/></svg>',
    micOff: SVG_OPEN + '<line x1="2" y1="2" x2="22" y2="22"/><path d="M18.89 13.23A7 7 0 0 0 19 12v-2"/><path d="M5 10v2a7 7 0 0 0 12 5"/><path d="M15 9.34V5a3 3 0 0 0-5.68-1.33"/><path d="M9 9v3a3 3 0 0 0 5.12 2.12"/><line x1="12" y1="19" x2="12" y2="22"/></svg>',
    cam: SVG_OPEN + '<path d="M23 7l-7 5 7 5V7z"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg>',
    camOff: SVG_OPEN + '<path d="M16 16v1a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h2m5.66 0H14a2 2 0 0 1 2 2v3.34l1 1L23 7v10"/><line x1="1" y1="1" x2="23" y2="23"/></svg>',
    volume: SVG_OPEN + '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14"/></svg>',
    volumeOff: SVG_OPEN + '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="23" y1="9" x2="17" y2="15"/><line x1="17" y1="9" x2="23" y2="15"/></svg>',
    file: SVG_OPEN + '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>'
  };

  // ====== 状态 ======
  var host = null;
  var els = {};
  var S = {
    attached: false,
    sessionReady: false,
    settings: Core.normalizeSettings(null),
    tabsShown: false,
    tab: 'hall',
    // idle | joining | connecting | live | reconnecting | degraded（媒体失败、文字可用、重试中）
    // | elsewhere | kicked | full | closed | error
    phase: 'idle',
    notice: '',
    joinSeq: 0,
    joinInFlight: false,
    joinTimer: null,
    attempt: 0,
    room: null,
    LK: null,
    lkLoading: null,
    lkScript: null,
    me: { identity: '', name: '' },
    micOn: false, micBusy: false, micQuota: null,
    camOn: false, camBusy: false,
    muted: false,
    ducked: false,
    voiceBusy: false,
    subscribed: false,
    subscribeId: '',
    subscribeAttempt: 0,
    subscribeTimer: null,
    autoplayBlocked: false,    // 主动探测判定：页面尚无用户交互且浏览器不允许出声
    autoplayProbed: false,
    autoplayProbing: false,
    audioStarting: false,
    gestureWatching: false,
    lastHistoryAt: 0,
    store: new Core.MessageStore(Core.KEEP_LIMIT),
    nodes: Object.create(null),
    pending: Object.create(null),
    owned: Object.create(null),
    ownedOrder: [],
    pendingFiles: [],
    rate: new Core.SlidingWindow(5, 10000),
    speakers: Object.create(null),
    peopleEls: Object.create(null),
    peopleTimer: null,
    peopleMoreEl: null,
    desired: Object.create(null),
    firstSeen: Object.create(null),
    tiles: Object.create(null),
    permWaiters: [],
    newBelow: 0,
    statusKey: '',
    statusTimer: null,
    statusLastAt: 0,
    toastTimer: null,
    tabId: '',
    announcedAt: 0,
    channel: null,
    hostOrigin: '',
    noMoreHistory: false,
    loadingMore: false,
    // [123] 总开关：关闭时访客所在页签、关闭前的阶段、关闭期间是否有私聊往来（决定重新打开后的页签与是否自动入厅）
    closedFromTab: '',
    phaseBeforeClose: '',
    privateActivity: false,
    settingsSeq: 0            // 已处理的 hall_settings 广播数：入场请求期间收到过广播时以广播为准
  };

  function $(id) { return document.getElementById(id); }
  function mk(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }
  // 统一异常兜底：同步异常与异步 Promise 拒绝都记录到控制台，不让单个事件处理把整个模块打崩
  function logErr(e) { try { console.warn('[cs-hall]', e); } catch (_) {} }
  function safe(fn) {
    return function () {
      try {
        var out = fn.apply(this, arguments);
        if (out && typeof out.then === 'function') out.then(null, logErr);
        return out;
      } catch (e) { logErr(e); }
    };
  }
  function now() { return Date.now(); }
  function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function lsSet(k, v) { try { localStorage.setItem(k, v); } catch (e) {} }

  // ====== 挂接 ======
  function attach(bridge) {
    if (S.attached || !bridge) return;
    host = bridge;
    S.attached = true;
    els = {
      tabs: $('tabs'), tabHall: $('tabHall'), tabPrivate: $('tabPrivate'),
      tabHallDot: $('tabHallDot'), tabPrivateDot: $('tabPrivateDot'), tabHallCount: $('tabHallCount'),
      hallPane: $('hallPane'), privatePane: $('privatePane'),
      status: $('hallStatusText'), muteBtn: $('hallMuteBtn'), muteLabel: $('hallMuteLabel'),
      people: $('hallPeople'), video: $('hallVideo'),
      notice: $('hallNotice'), noticeText: $('hallNoticeText'), noticeBtn: $('hallNoticeBtn'),
      scroll: $('hallScroll'), goPrivate: $('hallGoPrivate'), more: $('hallMore'), msgs: $('hallMsgs'), newBtn: $('hallNewBtn'),
      toast: $('hallToast'),
      pendingList: $('hallPendingList'), input: $('hallInput'), fileBtn: $('hallFileBtn'), fileInput: $('hallFileInput'),
      inputWrap: $('hallInputWrap'), chatPaused: $('hallChatPaused'), chatPausedText: $('hallChatPausedText'),
      voicePaused: $('hallVoicePaused'), voicePausedText: $('hallVoicePausedText'),
      micBtn: $('hallMicBtn'), micLabel: $('hallMicLabel'), camBtn: $('hallCamBtn'), camLabel: $('hallCamLabel'),
      sendBtn: $('hallSendBtn'), audioSink: $('hallAudioSink')
    };
    if (!els.hallPane || !els.msgs || !els.tabs) { S.attached = false; return; }   // 页面结构不完整（旧缓存）时不启用大厅
    S.muted = lsGet(host.storeKey + '_hall_muted') === '1';
    try { S.tabId = Core.newUUIDv4(); } catch (e) { S.tabId = 'tab-' + now(); }
    try { S.hostOrigin = Core.hostOriginFrom(location.ancestorOrigins, document.referrer); } catch (e) { S.hostOrigin = ''; }
    bindUI();
    setupTabChannel();
    renderControls();
    renderStatus();
    var pub = host.getPublicSettings && host.getPublicSettings();
    if (pub) onSettings(pub);
  }

  function bindUI() {
    if (els.tabHall) els.tabHall.onclick = function () { selectTab('hall'); };
    if (els.tabPrivate) els.tabPrivate.onclick = function () { selectTab('private'); };
    if (els.goPrivate) els.goPrivate.onclick = function () { selectTab('private'); };
    if (els.muteBtn) els.muteBtn.onclick = safe(toggleMute);
    if (els.noticeBtn) els.noticeBtn.onclick = safe(onNoticeAction);
    if (els.micBtn) els.micBtn.onclick = safe(function () { S.micOn ? disableMic({ release: true }) : enableMic(); });
    if (els.camBtn) els.camBtn.onclick = safe(function () { S.camOn ? disableCam({ release: true }) : enableCam(); });
    if (els.sendBtn) els.sendBtn.onclick = safe(sendFromInput);
    if (els.more) els.more.onclick = safe(loadMore);
    if (els.newBtn) els.newBtn.onclick = function () { scrollToBottom(); };
    if (els.fileBtn && els.fileInput) {
      els.fileInput.setAttribute('accept', Core.UPLOAD_ACCEPT);
      els.fileBtn.onclick = function () { els.fileInput.click(); };
      els.fileInput.onchange = safe(function (e) {
        var files = e && e.target && e.target.files;
        if (files) for (var i = 0; i < files.length; i++) addPendingFile(files[i]);
        if (e && e.target) e.target.value = '';
      });
    }
    if (els.input) {
      els.input.setAttribute('maxlength', String(Core.CONTENT_MAX));
      els.input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); safe(sendFromInput)(); }
      });
      els.input.addEventListener('input', function () {
        if (!els.input.style) return;
        els.input.style.height = 'auto';
        els.input.style.height = Math.min(100, els.input.scrollHeight || 38) + 'px';
      });
      els.input.addEventListener('paste', safe(function (e) {
        var items = e.clipboardData && e.clipboardData.items;
        if (!items) return;
        var had = false;
        for (var i = 0; i < items.length; i++) {
          if (items[i].kind === 'file') { var f = items[i].getAsFile(); if (f) { addPendingFile(f); had = true; } }
        }
        if (had) e.preventDefault();
      }));
    }
    if (els.scroll) {
      els.scroll.addEventListener('scroll', function () {
        if (S.newBelow && nearBottom()) { S.newBelow = 0; renderNewBtn(); }
      });
    }
    // 浏览器自动播放限制：iframe 内任意一次点击都尝试恢复大厅声音（契约 7.3）
    document.addEventListener('click', function () { tryStartAudio(); }, true);
    document.addEventListener('visibilitychange', safe(function () { reconcileVideo(); }));
    window.addEventListener('pagehide', function () {
      if (S.joinTimer) { clearTimeout(S.joinTimer); S.joinTimer = null; }
      clearPendingTimers();
    });
    window.addEventListener('pageshow', safe(function (ev) {
      // 从往返缓存恢复：LiveKit 已在 pagehide 时断开，重新入厅（视为后打开的页面，接管收听）
      if (ev && ev.persisted && S.sessionReady && S.settings.enabled && S.phase !== 'kicked') { S.attempt = 0; scheduleJoin(0); }
    }));
  }

  // ====== 页签 ======
  function showTabs(show) {
    S.tabsShown = !!show;
    if (els.tabs) els.tabs.hidden = !show;
    if (!show) selectTab('private', { force: true });
  }
  function selectTab(name, opts) {
    if (name !== 'hall' && name !== 'private') return;
    if (name === 'hall' && !S.tabsShown) return;
    var changed = S.tab !== name;
    S.tab = name;
    if (els.hallPane) els.hallPane.hidden = name !== 'hall';
    if (els.privatePane) els.privatePane.hidden = name !== 'private';
    if (els.tabHall) els.tabHall.setAttribute('aria-selected', name === 'hall' ? 'true' : 'false');
    if (els.tabPrivate) els.tabPrivate.setAttribute('aria-selected', name === 'private' ? 'true' : 'false');
    if (name === 'private') {
      if (els.tabPrivateDot) els.tabPrivateDot.hidden = true;
      if (host && host.onPrivateShown && (changed || (opts && opts.force))) host.onPrivateShown();
    } else {
      if (els.tabHallDot) els.tabHallDot.hidden = true;
      scrollToBottom();
    }
    reconcileVideo();
  }
  function hallVisible() {
    if (!host || !S.tabsShown || S.tab !== 'hall') return false;
    if (typeof document.visibilityState === 'string' && document.visibilityState === 'hidden') return false;
    return !!host.isWidgetOpen();
  }

  // ====== 设置 ======
  // 访客公开设置（chat.js 拉取 /api/visitor/settings 后转交）
  function onSettings(data) {
    if (!data || typeof data !== 'object') return;
    if (data.hall_enabled === undefined && data.hall_video_enabled === undefined &&
        data.hall_chat_enabled === undefined && data.hall_voice_enabled === undefined) return;
    applySettings(Core.normalizeSettings(data, S.settings), 'public');
  }
  // source：public（/api/visitor/settings）| ws（hall_settings 广播）| join（入场返回 / 入场错误码）| local（开麦、上传等接口错误码推断）
  function applySettings(next, source) {
    var prev = S.settings;
    S.settings = next;
    if (source !== 'public') syncPublicCache();
    if (!next.enabled) {
      if (S.phase !== 'closed') disableHall();
      else { renderControls(); postStatus(); }
      return;
    }
    var ch = Core.switchChanges(prev, next);
    if (S.phase === 'closed') {
      reopenHall(source);
    } else if (!S.tabsShown && (S.sessionReady || source === 'public')) {
      showTabs(true);
      selectTab('hall');
    }
    var stopped = { micStopped: false, camStopped: false };
    if (ch.voice === 'off') {
      failPermWaiters('microphone');
      stopped.micStopped = stopMicBySwitch();
    }
    if (ch.video === 'off') {
      failPermWaiters('camera');
      if (S.camOn) { disableCam({ release: false }); stopped.camStopped = true; }
    }
    if (source === 'ws') {
      var t = Core.switchToastText(ch, stopped);
      if (t) toast(t);
    }
    renderControls();
    reconcileVideo();
    postStatus();
  }
  // 运行中得知的开关同步到本标签页的公开设置缓存（下次打开页面时不沿用旧开关）
  function syncPublicCache() {
    if (host && typeof host.updatePublicSettings === 'function') host.updatePublicSettings(Core.publicSettingsPatch(S.settings));
  }
  // 总开关关闭：与上线前一致，只保留私聊。作废进行中的入厅与重连、断开 LiveKit、退订大厅广播、宿主页胶囊隐藏（状态 off）
  function disableHall() {
    S.closedFromTab = S.tabsShown ? S.tab : '';
    S.phaseBeforeClose = S.phase;
    S.privateActivity = false;
    leave('closed');
    clearPendingFiles();
    showTabs(false);
    renderControls();
    postStatus();
  }
  // 总开关重新打开：不刷新页面恢复页签并自动入厅（入厅流程同首次加载：按需加载 LiveKit、单次入厅锁防重复连接）
  function reopenHall(source) {
    var wasElsewhere = S.phaseBeforeClose === 'elsewhere';
    var tab = Core.tabAfterReopen(S.closedFromTab, S.privateActivity);
    S.closedFromTab = '';
    S.phaseBeforeClose = '';
    S.privateActivity = false;
    if (!S.sessionReady) { S.phase = 'idle'; return; }   // 会话就绪后由 onSessionReady 显示页签并入厅
    showTabs(true);
    if (els.tabHallDot) els.tabHallDot.hidden = true;
    if (tab === 'hall') selectTab('hall');
    if (wasElsewhere) { setPhase('elsewhere'); return; }   // 关闭前已由其他页面收听：仍由那个页面入厅，本页不抢
    S.attempt = 0;
    setPhase('idle');
    scheduleJoin(source === 'ws' ? Core.reopenJoinDelay() : 0);
  }

  // ====== 会话就绪 → 空闲时入厅 ======
  function onSessionReady() {
    S.sessionReady = true;
    if (!S.settings.enabled) { showTabs(false); postStatus(); return; }
    if (!S.tabsShown) { showTabs(true); selectTab('hall'); }
    if (S.phase === 'idle' || S.phase === 'error') { S.attempt = 0; scheduleJoin(0); }
  }
  function runWhenIdle(fn) {
    if (typeof window.requestIdleCallback === 'function') window.requestIdleCallback(function () { fn(); }, { timeout: 3000 });
    else setTimeout(fn, 800);
  }
  function scheduleJoin(delay) {
    if (S.joinTimer) clearTimeout(S.joinTimer);
    S.joinTimer = setTimeout(function () {
      S.joinTimer = null;
      runWhenIdle(safe(join));
    }, Math.max(0, delay || 0));
  }
  function scheduleRetry(opts) {
    if (S.attempt >= MAX_JOIN_ATTEMPTS) {
      setPhase('error', '大厅连接多次失败，请稍后点击重试');
      return;
    }
    var o = Object.assign({}, JOIN_BACKOFF, opts || {});
    scheduleJoin(Core.backoffDelay(S.attempt++, o));
  }

  // ====== LiveKit 脚本按需加载（空闲时机、带完整性校验、失败可重试） ======
  function loadLivekit() {
    if (window.LivekitClient && window.LivekitClient.Room) return Promise.resolve(window.LivekitClient);
    if (S.lkLoading) return S.lkLoading;
    S.lkLoading = new Promise(function (resolve, reject) {
      var s = document.createElement('script');
      s.src = LK_SRC;
      s.async = true;
      s.integrity = LK_SRI;
      s.onload = function () {
        if (window.LivekitClient && window.LivekitClient.Room) resolve(window.LivekitClient);
        else reject(new Error('livekit not ready'));
      };
      s.onerror = function () { reject(new Error('livekit load failed')); };
      S.lkScript = s;
      (document.head || document.body).appendChild(s);
    }).catch(function (e) {
      S.lkLoading = null;
      if (S.lkScript && S.lkScript.parentNode) S.lkScript.parentNode.removeChild(S.lkScript);
      S.lkScript = null;
      throw e;
    });
    return S.lkLoading;
  }

  // ====== HTTP（访客接口统一带 Authorization: Bearer <visitor_token>，URL 不带 vid） ======
  async function requestJSON(method, path, body) {
    var token = host.getToken();
    if (!token) return { ok: false, status: 0, code: 40103, msg: '', data: null, retryAfter: 0 };
    var headers = { 'Authorization': 'Bearer ' + token };
    var opts = { method: method, headers: headers, credentials: 'omit', cache: 'no-store' };
    if (body !== undefined) { headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
    var ctrl = typeof AbortController === 'function' ? new AbortController() : null;
    var timer = null;
    if (ctrl) { opts.signal = ctrl.signal; timer = setTimeout(function () { try { ctrl.abort(); } catch (e) {} }, REQUEST_TIMEOUT_MS); }
    var r;
    try { r = await fetch(host.httpBase + path, opts); }
    catch (e) { return { ok: false, status: 0, code: -2, msg: '', data: null, retryAfter: 0 }; }
    finally { if (timer) clearTimeout(timer); }
    var j = null;
    try { j = await r.json(); } catch (e) { j = null; }
    var code = j && typeof j.code === 'number' ? j.code : (r.ok ? -1 : r.status);
    var ra = 0;
    try { ra = parseInt((r.headers && r.headers.get && r.headers.get('Retry-After')) || '0', 10) || 0; } catch (e) {}
    return { ok: !!r.ok && code === 0, status: r.status, code: code, msg: j && typeof j.msg === 'string' ? j.msg : '', data: j ? j.data : null, retryAfter: ra };
  }

  // ====== 入厅 ======
  var ACTIVE_PHASES = { joining: true, connecting: true, live: true, reconnecting: true, degraded: true, error: true, full: true, kicked: true };
  async function join() {
    if (S.joinTimer) { clearTimeout(S.joinTimer); S.joinTimer = null; }
    if (!host || !S.sessionReady || !S.settings.enabled || S.joinInFlight) return;
    if (!host.getToken()) return;
    var seq = ++S.joinSeq;
    var settingsSeq = S.settingsSeq;
    var startedAt = now();
    S.joinInFlight = true;
    announceTakeover();
    if (S.phase !== 'degraded' && S.phase !== 'reconnecting') setPhase('joining');
    try {
      var res = await Promise.all([
        loadLivekit().catch(function () { return null; }),
        requestJSON('POST', '/api/visitor/hall/join', {})
      ]);
      if (seq !== S.joinSeq) return;
      var LK = res[0], resp = res[1];
      if (!resp.ok) { handleJoinError(resp); return; }
      var info = Core.validateJoinData(resp.data, location.protocol, S.settings);
      if (!info) { setPhase('error', '大厅服务返回的数据异常，稍后自动重试'); scheduleRetry(); return; }
      S.me = { identity: info.identity, name: info.name };
      // 入场返回的视频 / 文字 / 语音开关与上限是服务端当时的状态；请求期间已收到 hall_settings 广播时以广播为准
      if (settingsSeq === S.settingsSeq) applySettings(info.settings, 'join');
      if (seq !== S.joinSeq) return;
      ingestBatch(info.history, { initial: true, since: startedAt });
      S.lastHistoryAt = now();
      renderControls();
      if (!LK || (typeof LK.isBrowserSupported === 'function' && !LK.isBrowserSupported())) {
        // 媒体不可用（脚本加载失败或浏览器不支持 WebRTC）：文字讨论照常，稍后重试语音
        setPhase('degraded', LK ? '当前浏览器不支持大厅语音，仍可收发文字' : '语音组件加载失败，稍后自动重试；仍可收发文字');
        sendSubscribe();
        if (LK) return;
        scheduleRetry();
        return;
      }
      S.LK = LK;
      await teardownRoom();
      if (seq !== S.joinSeq) return;
      if (S.phase !== 'degraded') setPhase('connecting');
      var ok = await connectRoom(info, seq);
      if (seq !== S.joinSeq) return;
      if (ok) {
        S.attempt = 0;
        setPhase('live');
        sendSubscribe();
        maybeProbeAutoplay();
      } else {
        setPhase('degraded', '语音连接失败，正在重试；仍可收发文字');
        sendSubscribe();
        scheduleRetry();
      }
    } finally {
      // 只释放本次 join 持有的锁；被 leave() 作废后锁已释放，且可能已有新的 join 在进行
      if (seq === S.joinSeq) S.joinInFlight = false;
    }
  }

  function handleJoinError(resp) {
    var code = resp.code;
    if (resp.status === 404 || code === 40371) {
      // 大厅关闭（或后端尚未提供大厅接口）：只保留私聊
      applySettings(Object.assign({}, S.settings, { enabled: false }), 'join');
      return;
    }
    if (code === 40372) {
      var sec = resp.data && typeof resp.data === 'object' ? Number(resp.data.retry_after) || 0 : 0;
      enterKicked(Core.errorText(40372, { data: { retry_after: sec } }), sec);
      return;
    }
    if (code === 40977) {
      setPhase('full', Core.errorText(40977));
      scheduleRetry({ minMs: 30000 });
      return;
    }
    if (code === 42910 || resp.status === 429) {
      setPhase('error', Core.errorText(42910));
      scheduleRetry({ minMs: Math.max(15000, (resp.retryAfter || 0) * 1000) });
      return;
    }
    if (resp.status === 401 || resp.status === 403) {
      setPhase('error', Core.errorText(code, { action: 'join', msg: resp.msg }));
      scheduleRetry({ minMs: 30000 });
      return;
    }
    setPhase('error', Core.errorText(code, { action: 'join', msg: resp.msg }));
    scheduleRetry();
  }

  function enterKicked(text, retryAfterSec) {
    leave('kicked', text);
    var waitMs = retryAfterSec > 0 ? retryAfterSec * 1000 + 1500 : 60000;
    S.attempt = 0;
    S.joinTimer = setTimeout(function () { S.joinTimer = null; if (S.phase === 'kicked') runWhenIdle(safe(join)); }, waitMs);
  }

  async function connectRoom(info, seq) {
    var LK = S.LK;
    var room;
    try {
      room = new LK.Room({
        adaptiveStream: { pixelDensity: 1, pauseVideoInBackground: true },
        dynacast: true,
        stopLocalTrackOnUnpublish: true,
        disconnectOnPageLeave: true,
        audioCaptureDefaults: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
        videoCaptureDefaults: { resolution: LK.VideoPresets.h360.resolution, facingMode: 'user' },
        publishDefaults: {
          simulcast: true,
          videoSimulcastLayers: [LK.VideoPresets.h180],
          videoEncoding: LK.VideoPresets.h360.encoding,
          videoCodec: 'vp8',
          dtx: true,
          red: true,
          audioPreset: LK.AudioPresets.speech
        }
      });
    } catch (e) { return false; }
    S.room = room;
    wireRoom(room);
    var opts = { autoSubscribe: false, maxRetries: 1, peerConnectionTimeout: 15000, websocketTimeout: 15000 };
    if (info.iceServers.length) opts.rtcConfig = { iceServers: info.iceServers };
    try {
      await room.connect(info.url, info.token, opts);
    } catch (e) {
      if (S.room === room) S.room = null;
      try { room.removeAllListeners && room.removeAllListeners(); } catch (_) {}
      try { await room.disconnect(true); } catch (_) {}
      return false;
    }
    if (seq !== S.joinSeq || S.room !== room) {
      try { room.removeAllListeners && room.removeAllListeners(); } catch (_) {}
      try { await room.disconnect(true); } catch (_) {}
      return false;
    }
    reconcileAudio();
    reconcileVideo();
    schedulePeople();
    return true;
  }

  function wireRoom(room) {
    var E = S.LK.RoomEvent;
    function mine(fn) { return safe(function () { if (room === S.room) fn.apply(null, arguments); }); }
    room.on(E.ParticipantConnected, mine(function () { reconcileAudio(); schedulePeople(); }))
      .on(E.ParticipantDisconnected, mine(function () { reconcileVideo(); schedulePeople(); }))
      .on(E.ParticipantNameChanged, mine(schedulePeople))
      .on(E.ParticipantMetadataChanged, mine(schedulePeople))
      .on(E.TrackPublished, mine(function (pub) {
        if (pub && pub.kind === 'audio') reconcileAudio(); else reconcileVideo();
        schedulePeople();
      }))
      .on(E.TrackUnpublished, mine(function (pub) { if (pub && pub.trackSid) delete S.desired[pub.trackSid]; reconcileVideo(); schedulePeople(); }))
      .on(E.TrackSubscribed, mine(onTrackSubscribed))
      .on(E.TrackUnsubscribed, mine(onTrackUnsubscribed))
      .on(E.TrackMuted, mine(schedulePeople))
      .on(E.TrackUnmuted, mine(schedulePeople))
      .on(E.ActiveSpeakersChanged, mine(function (speakers) {
        S.speakers = Object.create(null);
        (speakers || []).forEach(function (p) { if (p && p.identity) S.speakers[p.identity] = true; });
        schedulePeople();
        postStatus();
      }))
      .on(E.ParticipantPermissionsChanged, mine(function (prev, participant) {
        if (!participant || participant === room.localParticipant) onLocalPermissions();
      }))
      .on(E.LocalTrackUnpublished, mine(onLocalUnpublished))
      .on(E.LocalTrackPublished, mine(function () { schedulePeople(); renderVideoTiles(); }))
      .on(E.AudioPlaybackStatusChanged, mine(function () { renderStatus(); postStatus(); }))
      .on(E.Reconnecting, mine(function () { setPhase('reconnecting'); }))
      .on(E.Reconnected, mine(function () { setPhase('live'); reconcileAudio(); reconcileVideo(); }))
      .on(E.MediaDevicesError, mine(function (e) { toast(Core.mediaErrorText(e, S.camBusy ? 'camera' : 'mic')); }))
      .on(E.Disconnected, safe(function (reason) { onRoomDisconnected(room, reason); }));
  }

  function onRoomDisconnected(room, reason) {
    if (room !== S.room) return;
    S.room = null;
    resetMediaState();
    var kind = Core.classifyDisconnect(reason, S.LK && S.LK.DisconnectReason);
    if (kind === 'client') return;
    if (kind === 'elsewhere') { leave('elsewhere'); return; }
    if (kind === 'removed') {
      // 被移出：重新 join，由服务端返回 40372 与剩余禁入时间
      setPhase('degraded', '大厅连接已断开，正在重新进入');
      scheduleJoin(1500);
      return;
    }
    setPhase('degraded', '大厅连接已断开，正在重连；仍可收发文字');
    scheduleRetry();
  }

  async function teardownRoom() {
    var room = S.room;
    S.room = null;
    if (room) {
      try { room.removeAllListeners && room.removeAllListeners(); } catch (e) {}
      try { await room.disconnect(true); } catch (e) {}
    }
    resetMediaState();
  }
  function resetMediaState() {
    S.micOn = false; S.camOn = false; S.micBusy = false; S.camBusy = false;
    S.speakers = Object.create(null);
    S.desired = Object.create(null);
    resolvePermWaiters(true);
    if (els.audioSink) while (els.audioSink.firstChild) els.audioSink.removeChild(els.audioSink.firstChild);
    Object.keys(S.tiles).forEach(removeTile);
    if (els.video) els.video.hidden = true;
    renderControls();
    schedulePeople();
  }

  // 离开大厅（被接管 / 被移出 / 大厅关闭）：作废进行中的 join、断开媒体、退订大厅广播
  function leave(phase, notice) {
    S.joinSeq++;
    S.joinInFlight = false;
    if (S.joinTimer) { clearTimeout(S.joinTimer); S.joinTimer = null; }
    sendUnsubscribe();
    teardownRoom();
    setPhase(phase, notice);
  }

  // ====== 多标签页接管 ======
  function setupTabChannel() {
    if (typeof BroadcastChannel !== 'function') return;
    try {
      S.channel = new BroadcastChannel('cs_hall_' + host.siteID);
      S.channel.onmessage = safe(function (ev) {
        var m = Core.validateTabMessage(ev && ev.data);
        if (!m || m.tab === S.tabId) return;
        // 后宣告者接管；两个页面几乎同时打开时按时间戳、再按标签页 ID 判定，避免双方同时让出
        if (S.announcedAt > m.at || (S.announcedAt === m.at && S.tabId > m.tab)) return;
        if (ACTIVE_PHASES[S.phase]) leave('elsewhere');
      });
    } catch (e) { S.channel = null; }
  }
  function announceTakeover() {
    S.announcedAt = now();
    if (!S.channel) return;
    try { S.channel.postMessage({ type: 'hall_takeover', tab: S.tabId, at: S.announcedAt }); } catch (e) {}
  }

  // ====== 音频 ======
  function wantAudio() { return !S.muted && !S.ducked; }
  function reconcileAudio() {
    var room = S.room;
    if (!room) return;
    var want = wantAudio();
    room.remoteParticipants.forEach(function (p) {
      p.audioTrackPublications.forEach(function (pub) {
        var sid = pub.trackSid;
        if (S.desired[sid] !== want) { S.desired[sid] = want; try { pub.setSubscribed(want); } catch (e) {} }
      });
    });
  }
  function tryStartAudio() {
    var room = S.room;
    if (!room || !wantAudio()) return;
    if (room.canPlaybackAudio && !S.autoplayBlocked) return;
    if (S.audioStarting) return;
    S.audioStarting = true;
    var p;
    try { p = room.startAudio(); } catch (e) { p = Promise.reject(e); }
    Promise.resolve(p).then(function () {
      S.audioStarting = false;
      if (room !== S.room) return;
      // 页面已有用户交互（或浏览器不提供该信息）时才认为自动播放限制已解除；
      // 宿主页命令等非本页手势触发的 startAudio 不清除探测结果
      if (!userActivationKnown() || hasUserActivation()) S.autoplayBlocked = false;
      renderStatus(); postStatus();
    }, function () { S.audioStarting = false; renderStatus(); });
  }
  function audioBlocked() { return !!(S.room && S.phase === 'live' && wantAudio() && (!S.room.canPlaybackAudio || S.autoplayBlocked)); }

  // ====== 自动播放探测（契约 7.3） ======
  // livekit-client 只有在远端音频挂载后 play() 被拒绝时才把 canPlaybackAudio 置为 false；大厅里没人说话时
  // 访客看不到提示，第一句话就会漏听。因此入厅成功后，若页面尚无用户交互，主动探测浏览器是否允许出声：
  // 以 AudioContext 能否运行为主判据，再用无任何输入的 MediaStreamDestination 静音试播（必然不出声），
  // 不使用网络地址（不受 CSP 影响），已在播放或已静音大厅时不探测。
  function userActivationKnown() { try { return !!navigator.userActivation; } catch (e) { return false; } }
  function hasUserActivation() { try { return !!(navigator.userActivation && navigator.userActivation.hasBeenActive); } catch (e) { return false; } }
  function audioPlayingNow() {
    var list = els.audioSink && els.audioSink.children;
    if (!list) return false;
    for (var i = 0; i < list.length; i++) if (list[i] && list[i].paused === false) return true;
    return false;
  }
  // Promise<boolean>：true 表示浏览器当前不允许本页自动出声。
  // 主判据是 AudioContext：新建后若为 suspended，调用 resume() 并最多等待 AUTOPLAY_RESUME_WAIT_MS，仍未 running 即判为受限
  // （Chrome 对 srcObject 为 MediaStream 的 play() 不一定按自动播放规则拦截，不能只看 play()）；
  // AudioContext 能运行时，再参考媒体元素 play() 是否被拒（NotAllowedError，如 Safari / Firefox）。
  function probeAutoplayBlocked() {
    var Ctx = window.AudioContext || window.webkitAudioContext;
    if (typeof Ctx !== 'function') return Promise.resolve(false);
    var ctx = null, el = null, stream = null, timer = null;
    function clearTimer() { if (timer) { clearTimeout(timer); timer = null; } }
    function cleanup() {
      clearTimer();
      try { if (el) { el.pause(); el.srcObject = null; } } catch (e) {}
      try { if (stream && stream.getTracks) stream.getTracks().forEach(function (t) { try { t.stop(); } catch (_) {} }); } catch (e) {}
      try { if (ctx && ctx.state !== 'closed') { var c = ctx.close(); if (c && typeof c.then === 'function') c.then(null, function () {}); } } catch (e) {}
    }
    return new Promise(function (resolve) {
      var done = false;
      function finish(blocked) { if (done) return; done = true; cleanup(); resolve(!!blocked); }
      try { ctx = new Ctx(); } catch (e) { finish(false); return; }
      var ready;
      if (ctx.state === 'running') {
        ready = Promise.resolve();
      } else {
        var rp = null;
        try { rp = ctx.resume(); } catch (e) { rp = null; }
        // resume() 在没有用户交互时可能一直不返回：最多等待 AUTOPLAY_RESUME_WAIT_MS
        ready = new Promise(function (r) {
          timer = setTimeout(function () { timer = null; r(); }, AUTOPLAY_RESUME_WAIT_MS);
          if (rp && typeof rp.then === 'function') rp.then(function () { clearTimer(); r(); }, function () { clearTimer(); r(); });
        });
      }
      ready.then(function () {
        if (done) return;
        if (ctx.state !== 'running') { finish(true); return; }
        if (typeof Audio !== 'function') { finish(false); return; }
        try {
          // 无任何输入的 MediaStreamDestination 只产生静音轨道：试探媒体元素的播放策略，不会出声
          stream = ctx.createMediaStreamDestination().stream;
          el = new Audio();
          el.muted = false;
          el.srcObject = stream;
        } catch (e) { finish(false); return; }
        timer = setTimeout(function () { timer = null; finish(false); }, AUTOPLAY_PROBE_TIMEOUT_MS);
        var p;
        try { p = el.play(); } catch (e) { finish(!!(e && e.name === 'NotAllowedError')); return; }
        if (!p || typeof p.then !== 'function') { finish(false); return; }
        p.then(function () { finish(false); }, function (e) { finish(!!(e && e.name === 'NotAllowedError')); });
      }, function () { finish(false); });
    });
  }
  function maybeProbeAutoplay() {
    if (S.autoplayProbed || S.autoplayProbing) return;
    if (!S.room || S.phase !== 'live' || !wantAudio()) return;
    if (hasUserActivation() || audioPlayingNow()) { S.autoplayProbed = true; return; }
    S.autoplayProbing = true;
    probeAutoplayBlocked().then(function (blocked) {
      S.autoplayProbing = false;
      S.autoplayProbed = true;
      if (!blocked || hasUserActivation()) return;   // 探测期间用户已经点过页面
      S.autoplayBlocked = true;
      renderStatus();
      postStatus();
    }, function () { S.autoplayProbing = false; S.autoplayProbed = true; });
  }
  // 提示“点击收听大厅”期间，用户第一次点击 / 按键 / 触摸页面任意处即恢复播放（不必点中按钮）
  function onPageGesture(ev) {
    if (ev && ev.type === 'keydown' && ev.key === 'Escape') return;   // Esc 不产生用户激活
    if (userActivationKnown() && !hasUserActivation()) return;
    tryStartAudio();
  }
  var onPageGestureSafe = safe(onPageGesture);
  function watchGesture(on) {
    on = !!on;
    if (S.gestureWatching === on) return;
    S.gestureWatching = on;
    GESTURE_EVENTS.forEach(function (t) {
      try {
        if (on) document.addEventListener(t, onPageGestureSafe, true);
        else document.removeEventListener(t, onPageGestureSafe, true);
      } catch (e) {}
    });
  }
  function toggleMute() {
    S.muted = !S.muted;
    lsSet(host.storeKey + '_hall_muted', S.muted ? '1' : '0');
    // 静音只影响本机播放：不再订阅大厅音频（同时节省服务器带宽），不影响其他人
    reconcileAudio();
    if (!S.muted) tryStartAudio();
    renderControls();
    renderStatus();
    postStatus();
  }

  function onTrackSubscribed(track, pub, participant) {
    if (!track) return;
    if (track.kind === 'audio') {
      if (!wantAudio()) { S.desired[pub.trackSid] = false; try { pub.setSubscribed(false); } catch (e) {} return; }
      var a = track.attach();
      a.setAttribute('data-identity', participant ? participant.identity : '');
      if (els.audioSink) els.audioSink.appendChild(a);
      return;
    }
    if (track.kind === 'video') {
      if (!S.desired[pub.trackSid]) { try { pub.setSubscribed(false); } catch (e) {} return; }
      try { pub.setVideoQuality(S.LK.VideoQuality.LOW); } catch (e) {}
      renderVideoTiles();
    }
  }
  function onTrackUnsubscribed(track) {
    if (!track) return;
    try { (track.detach() || []).forEach(function (el) { if (el && el.parentNode && track.kind === 'audio') el.parentNode.removeChild(el); }); } catch (e) {}
    if (track.kind === 'video') renderVideoTiles();
  }

  // ====== 视频（只在大厅页可见时订阅，最多 2 路，默认低清层） ======
  function roleOf(p) { return Core.parseMetadata(p && p.metadata).role; }
  function reconcileVideo() {
    var room = S.room;
    if (!room || !S.LK) { renderVideoTiles(); return; }
    var visible = S.settings.videoEnabled && hallVisible();
    var cands = [];
    room.remoteParticipants.forEach(function (p) {
      p.videoTrackPublications.forEach(function (pub) {
        if (pub.source !== S.LK.Track.Source.Camera) return;
        if (!S.firstSeen[pub.trackSid]) S.firstSeen[pub.trackSid] = now();
        cands.push({ sid: pub.trackSid, role: roleOf(p), publishedAt: S.firstSeen[pub.trackSid], pub: pub });
      });
    });
    var chosen = Core.pickVideoSubscriptions(cands, MAX_VIDEO_TILES, visible);
    cands.forEach(function (c) {
      var want = chosen.indexOf(c.sid) >= 0;
      if (S.desired[c.sid] !== want) {
        S.desired[c.sid] = want;
        try { c.pub.setSubscribed(want); } catch (e) {}
      }
    });
    renderVideoTiles();
  }
  function renderVideoTiles() {
    if (!els.video) return;
    var want = [];
    var room = S.room;
    if (room && S.LK && hallVisible()) {
      if (S.camOn) {
        var lp = room.localParticipant.getTrackPublication(S.LK.Track.Source.Camera);
        if (lp && lp.track) want.push({ key: 'local', track: lp.track, name: '我', local: true });
      }
      room.remoteParticipants.forEach(function (p) {
        p.videoTrackPublications.forEach(function (pub) {
          if (pub.source === S.LK.Track.Source.Camera && S.desired[pub.trackSid] && pub.track && !pub.isMuted) {
            want.push({ key: pub.trackSid, track: pub.track, name: p.name || '参与者', local: false });
          }
        });
      });
    }
    var keep = Object.create(null);
    want.slice(0, MAX_VIDEO_TILES + 1).forEach(function (w) {
      keep[w.key] = true;
      var t = S.tiles[w.key];
      if (!t || t.track !== w.track) {
        if (t) removeTile(w.key);
        var tile = mk('div', 'hv-tile' + (w.local ? ' is-local' : ''));
        tile.setAttribute('data-key', w.key);
        var v = document.createElement('video');
        v.setAttribute('playsinline', '');
        v.setAttribute('autoplay', '');
        v.muted = true;
        tile.appendChild(v);
        tile.appendChild(mk('span', 'hv-name', Core.clampText(w.name, 24)));
        els.video.appendChild(tile);
        try { w.track.attach(v); } catch (e) {}
        S.tiles[w.key] = { el: tile, video: v, track: w.track };
      }
    });
    Object.keys(S.tiles).forEach(function (k) { if (!keep[k]) removeTile(k); });
    els.video.hidden = Object.keys(S.tiles).length === 0;
  }
  function removeTile(key) {
    var t = S.tiles[key];
    if (!t) return;
    try { t.track.detach(t.video); } catch (e) {}
    if (t.el.parentNode) t.el.parentNode.removeChild(t.el);
    delete S.tiles[key];
    if (els.video && Object.keys(S.tiles).length === 0) els.video.hidden = true;
  }

  // ====== 发布权限（开麦/开摄像头接口成功后，等待 LiveKit 下发权限再发布） ======
  function canPublishSource(room, source) {
    var perms = room && room.localParticipant && room.localParticipant.permissions;
    if (!perms || !perms.canPublish) return false;
    var list = perms.canPublishSources;
    return !list || list.length === 0 || list.indexOf(TRACK_SOURCE_PROTO[source]) >= 0;
  }
  function waitForPublishPermission(room, source) {
    if (canPublishSource(room, source)) return Promise.resolve(true);
    return new Promise(function (resolve) {
      var w = { room: room, source: source, resolve: resolve, timer: null };
      w.timer = setTimeout(function () { finishWaiter(w, canPublishSource(room, source)); }, PERMISSION_WAIT_MS);
      S.permWaiters.push(w);
    });
  }
  function finishWaiter(w, ok) {
    var i = S.permWaiters.indexOf(w);
    if (i >= 0) S.permWaiters.splice(i, 1);
    clearTimeout(w.timer);
    w.resolve(ok);
  }
  // 开关关闭时立即结束对应来源的等待（不再等满 PERMISSION_WAIT_MS）
  function failPermWaiters(source) {
    S.permWaiters.slice().forEach(function (w) { if (w.source === source) finishWaiter(w, false); });
  }
  function resolvePermWaiters(fail) {
    S.permWaiters.slice().forEach(function (w) {
      if (fail) finishWaiter(w, false);
      else if (canPublishSource(w.room, w.source)) finishWaiter(w, true);
    });
  }
  function onLocalPermissions() {
    resolvePermWaiters(false);
    var room = S.room;
    if (!room) return;
    // 权限被服务端收回（客服静音 / 关摄像头 / 视频关闭）：本地立即停止采集
    if (S.micOn && !S.micBusy && !canPublishSource(room, 'microphone')) { stopLocal(room, 'microphone'); S.micOn = false; renderControls(); schedulePeople(); }
    if (S.camOn && !S.camBusy && !canPublishSource(room, 'camera')) { stopLocal(room, 'camera'); S.camOn = false; renderControls(); renderVideoTiles(); }
  }
  function onLocalUnpublished(pub) {
    if (!pub || !S.LK) return;
    if (pub.source === S.LK.Track.Source.Microphone && !S.micBusy) S.micOn = false;
    if (pub.source === S.LK.Track.Source.Camera && !S.camBusy) S.camOn = false;
    renderControls();
    renderVideoTiles();
    schedulePeople();
  }
  async function stopLocal(room, source) {
    if (!room || !S.LK) return;
    var lp = room.localParticipant;
    var src = source === 'camera' ? S.LK.Track.Source.Camera : S.LK.Track.Source.Microphone;
    var pub = lp.getTrackPublication(src);
    if (pub && pub.track) {
      try { await lp.unpublishTrack(pub.track, true); } catch (e) { try { pub.track.stop(); } catch (_) {} }
    }
  }

  // ====== 麦克风 ======
  async function enableMic() {
    if (S.micBusy || S.micOn) return;
    if (!S.settings.voiceEnabled) { toast(Core.errorText(40377)); renderControls(); return; }
    var room = S.room;
    if (S.phase !== 'live' || !room) { toast('大厅语音尚未连接，暂时无法开麦'); return; }
    if (S.voiceBusy) { toast('正在与客服语音通话，通话结束后才能在大厅开麦'); return; }
    S.micBusy = true; renderControls();
    tryStartAudio();
    try {
      var resp = await requestJSON('POST', '/api/visitor/hall/mic', { on: true });
      if (!resp.ok) { onActionError(resp, 'mic'); return; }
      S.micQuota = resp.data && typeof resp.data === 'object' ? resp.data : null;
      if (room !== S.room) { releaseQuota('mic'); return; }
      // 等待期间客服关闭了语音：服务端已收回权限并清空名额，放弃发布（不再调用 /hall/mic）
      if (!S.settings.voiceEnabled) return;
      await waitForPublishPermission(room, 'microphone');
      if (room !== S.room) { releaseQuota('mic'); return; }
      if (!S.settings.voiceEnabled) return;
      try {
        await room.localParticipant.setMicrophoneEnabled(true);
        if (!S.settings.voiceEnabled || room !== S.room) { await stopLocal(room, 'microphone'); return; }
        S.micOn = true;
      } catch (e) {
        toast(Core.mediaErrorText(e, 'mic'));
        await stopLocal(room, 'microphone');
        releaseQuota('mic');
      }
    } finally {
      S.micBusy = false;
      renderControls();
      schedulePeople();
    }
  }
  async function disableMic(opts) {
    if (S.micBusy) return;
    S.micBusy = true; renderControls();
    try {
      await stopLocal(S.room, 'microphone');   // 契约：客户端先停止发布，再通知服务端释放名额
      S.micOn = false;
      if (!opts || opts.release !== false) await releaseQuota('mic');
    } finally {
      S.micBusy = false;
      renderControls();
      schedulePeople();
    }
  }
  // 开麦 / 开摄像头接口失败：大厅已关闭（40371）时按关闭处理（只保留私聊）；语音已关闭（40377）/ 视频未开启（40375）
  // 时同步对应开关（隐藏按钮并提示）；其余给出中文提示
  function onActionError(resp, action) {
    if (resp.code === 40371) { applySettings(Object.assign({}, S.settings, { enabled: false }), 'local'); return; }
    if (resp.code === 40377) { applySettings(Object.assign({}, S.settings, { voiceEnabled: false }), 'local'); toast(Core.errorText(40377)); return; }
    if (resp.code === 40375) { applySettings(Object.assign({}, S.settings, { videoEnabled: false }), 'local'); toast(Core.errorText(40375)); return; }
    toast(Core.errorText(resp.code, { action: action, data: resp.data, msg: resp.msg }));
  }
  // 语音开关关闭：正在发布的麦克风立即停止（服务端已收回权限并清空名额，不再调用 /hall/mic）。
  // 开麦流程进行中（micBusy）时由 enableMic 的后续检查放弃发布。返回是否关掉了正在发布的麦克风
  function stopMicBySwitch() {
    if (!S.micOn) return false;
    var room = S.room;
    S.micOn = false;
    stopLocal(room, 'microphone').then(null, logErr);
    schedulePeople();
    return true;
  }
  function releaseQuota(kind) {
    return requestJSON('POST', kind === 'camera' ? '/api/visitor/hall/camera' : '/api/visitor/hall/mic', { on: false }).catch(function () {});
  }

  // ====== 摄像头（仅视频开启时可用） ======
  async function enableCam() {
    if (S.camBusy || S.camOn) return;
    var room = S.room;
    if (!S.settings.videoEnabled) { toast(Core.errorText(40375)); return; }
    if (S.phase !== 'live' || !room) { toast('大厅尚未连接，暂时无法开启摄像头'); return; }
    S.camBusy = true; renderControls();
    tryStartAudio();
    try {
      var resp = await requestJSON('POST', '/api/visitor/hall/camera', { on: true });
      if (!resp.ok) { onActionError(resp, 'camera'); return; }
      if (room !== S.room) { releaseQuota('camera'); return; }
      if (!S.settings.videoEnabled) return;   // 等待期间视频被关闭：服务端已收回全部摄像头
      await waitForPublishPermission(room, 'camera');
      if (room !== S.room) { releaseQuota('camera'); return; }
      if (!S.settings.videoEnabled) return;
      try {
        await room.localParticipant.setCameraEnabled(true);
        if (!S.settings.videoEnabled || room !== S.room) { await stopLocal(room, 'camera'); return; }
        S.camOn = true;
      } catch (e) {
        toast(Core.mediaErrorText(e, 'camera'));
        await stopLocal(room, 'camera');
        releaseQuota('camera');
      }
    } finally {
      S.camBusy = false;
      renderControls();
      renderVideoTiles();
      schedulePeople();
    }
  }
  async function disableCam(opts) {
    if (S.camBusy) return;
    S.camBusy = true; renderControls();
    try {
      await stopLocal(S.room, 'camera');
      S.camOn = false;
      if (!opts || opts.release !== false) await releaseQuota('camera');
    } finally {
      S.camBusy = false;
      renderControls();
      renderVideoTiles();
      schedulePeople();
    }
  }

  // ====== 私聊语音通话期间：暂停大厅麦克风、暂停播放大厅声音 ======
  function onVoiceState(state) {
    var busy = typeof state === 'string' && state !== 'idle';
    if (busy === S.voiceBusy) return;
    S.voiceBusy = busy;
    S.ducked = busy;
    if (busy && S.micOn) { disableMic({ release: true }); toast('正在与客服语音通话，已暂停大厅麦克风'); }
    reconcileAudio();
    renderControls();
    postStatus();
  }

  // ====== WS：大厅订阅与信封处理 ======
  function rememberOwned(id) {
    if (S.owned[id]) return;
    S.owned[id] = true;
    S.ownedOrder.push(id);
    while (S.ownedOrder.length > OWNED_IDS_MAX) delete S.owned[S.ownedOrder.shift()];
  }
  function ownsMessageId(id) { return typeof id === 'string' && !!S.owned[id]; }
  function clearSubscribeRetry() {
    if (S.subscribeTimer) { clearTimeout(S.subscribeTimer); S.subscribeTimer = null; }
  }
  function sendSubscribe() {
    clearSubscribeRetry();
    if (S.subscribed || !host || !host.isWSOpen()) return;
    var id;
    try { id = Core.newUUIDv4(); } catch (e) { return; }
    S.subscribeId = id;
    rememberOwned(id);
    if (host.sendWS({ type: 'hall_subscribe', id: id })) {
      S.subscribed = true;
      if (now() - S.lastHistoryAt > BACKFILL_AFTER_MS) backfill();
      renderControls();
    }
  }
  // 契约：{type:"hall_unsubscribe", id}，成功回 {type:"hall_unsubscribed", id}
  function sendUnsubscribe() {
    clearSubscribeRetry();
    S.subscribeAttempt = 0;
    if (!S.subscribed) return;
    S.subscribed = false;
    S.subscribeId = '';
    if (host && host.isWSOpen()) {
      var frame = { type: 'hall_unsubscribe' };
      try { frame.id = Core.newUUIDv4(); rememberOwned(frame.id); } catch (e) {}
      host.sendWS(frame);
    }
    renderControls();
  }
  // 订阅被服务端拒绝：大厅关闭 / 被移出另有 hall_settings / hall_moderation 处理；
  // “服务繁忙”等临时失败按退避有限次重试，避免这条 WS 连接一直收不到大厅广播
  function scheduleSubscribeRetry() {
    clearSubscribeRetry();
    if (S.subscribeAttempt >= SUBSCRIBE_MAX_ATTEMPTS) return false;
    var delay = Core.backoffDelay(S.subscribeAttempt++, SUBSCRIBE_BACKOFF);
    S.subscribeTimer = setTimeout(function () {
      S.subscribeTimer = null;
      if (subscribablePhase() && S.me.identity) sendSubscribe();
    }, delay);
    return true;
  }
  function subscribablePhase() { return S.phase === 'live' || S.phase === 'reconnecting' || S.phase === 'degraded' || S.phase === 'connecting'; }
  function onWSOpen() {
    S.subscribed = false;
    S.subscribeAttempt = 0;
    clearSubscribeRetry();
    if (subscribablePhase() && S.me.identity) sendSubscribe();
  }
  function onWSClose() {
    S.subscribed = false;
    renderControls();
  }
  async function backfill() {
    var since = now();
    var resp = await requestJSON('GET', '/api/visitor/hall/messages?limit=' + Core.HISTORY_LIMIT);
    if (resp.ok && Array.isArray(resp.data)) { ingestBatch(resp.data, { latest: true, since: since }); S.lastHistoryAt = now(); }
  }
  async function loadMore() {
    if (S.loadingMore || S.noMoreHistory || S.store.size() >= Core.KEEP_LIMIT) return;
    var before = S.store.oldestId();
    if (!before) return;
    S.loadingMore = true;
    if (els.more) els.more.textContent = '加载中…';
    try {
      var resp = await requestJSON('GET', '/api/visitor/hall/messages?before=' + encodeURIComponent(before) + '&limit=' + Core.HISTORY_LIMIT);
      if (!resp.ok) { toast(Core.errorText(resp.code, { msg: resp.msg })); return; }
      var list = Array.isArray(resp.data) ? resp.data : [];
      if (list.length < Core.HISTORY_LIMIT) S.noMoreHistory = true;
      var sc = els.scroll;
      var oldH = sc ? sc.scrollHeight || 0 : 0, oldTop = sc ? sc.scrollTop || 0 : 0;
      ingestBatch(list, { older: true });
      if (sc) sc.scrollTop = oldTop + ((sc.scrollHeight || 0) - oldH);
    } finally {
      S.loadingMore = false;
      if (els.more) els.more.textContent = '查看更早的消息';
      renderMore();
    }
  }

  function onEnvelope(env) {
    if (!env || typeof env !== 'object') return;
    switch (env.type) {
      case 'hall_chat': onHallChat(env); break;
      case 'hall_ack': if (Core.validId(env.id)) markAck(env.id, env.ts); break;
      case 'hall_subscribed': if (env.id && env.id === S.subscribeId) S.subscribeAttempt = 0; break;
      case 'hall_unsubscribed': break;
      case 'hall_deleted': if (Core.validId(env.id)) removeMessage(env.id); break;
      case 'hall_settings': S.settingsSeq++; applySettings(Core.normalizeSettings(env.extra, S.settings), 'ws'); break;
      case 'hall_moderation': onModeration(env.extra || {}); break;
      case 'error': onError(env); break;
    }
  }
  function onHallChat(env) {
    if (!S.me.identity || S.phase === 'elsewhere' || S.phase === 'closed') return;
    var msg = Core.normalizeMessage(env, { origin: originOfHttpBase() });
    if (!msg) return;
    var isOwn = !!S.pending[msg.id] || (msg.identity && msg.identity === S.me.identity);
    if (S.pending[msg.id]) markAck(msg.id);
    msg.local = '';
    var r = ingest(msg, {});
    if (r && r.action === 'insert' && !isOwn && !hallVisible() && S.tabsShown && els.tabHallDot) els.tabHallDot.hidden = false;
  }
  function onError(env) {
    var text = Core.clampText(Core.str(env.content), 120) || '操作失败';
    if (env.id && env.id === S.subscribeId) {
      S.subscribed = false;
      S.subscribeId = '';
      var first = S.subscribeAttempt === 0;
      var retrying = subscribablePhase() && scheduleSubscribeRetry();
      if (first || !retrying) toast('大厅消息订阅失败：' + text + (retrying ? '，正在重试' : ''));
      renderControls();
      return;
    }
    if (S.pending[env.id]) markFailed(env.id, text);
    // 客服已关闭大厅文字聊天（广播可能晚于发送）：切换为暂停状态
    if (Core.isChatClosedError(text) && S.settings.chatEnabled) applySettings(Object.assign({}, S.settings, { chatEnabled: false }), 'local');
  }
  function onModeration(extra) {
    var action = extra.action;
    var text = Core.moderationText(action, extra.minutes);
    if (!text || S.phase === 'elsewhere') return;
    if (action === 'kicked') {
      var min = Number(extra.minutes) || 0;
      enterKicked(text, min > 0 ? min * 60 : 0);
      return;
    }
    if (action === 'muted' || action === 'mute_all') {
      if (S.micOn) { stopLocal(S.room, 'microphone'); S.micOn = false; }
    } else if (action === 'camera_off') {
      if (S.camOn) { stopLocal(S.room, 'camera'); S.camOn = false; renderVideoTiles(); }
    }
    renderControls();
    schedulePeople();
    toast(text);
  }

  // ====== 消息渲染（增量：只插入/更新/删除变化的节点） ======
  function originOfHttpBase() {
    try { return new URL(host.httpBase).origin; } catch (e) { return location.origin; }
  }
  // opts.initial：join 返回的最近 50 条；opts.latest：订阅后补拉的最近 50 条；opts.older：向上翻页
  function ingestBatch(list, opts) {
    if (!Array.isArray(list)) return;
    opts = opts || {};
    var origin = originOfHttpBase();
    var stick = nearBottom();
    var fresh = [];
    for (var i = 0; i < list.length; i++) {
      var msg = Core.normalizeMessage(list[i], { origin: origin });
      if (msg) { msg.local = ''; fresh.push(msg); ingest(msg, { batch: true }); }
    }
    // 断线期间被客服删除的消息收不到 hall_deleted：以服务端最新一页为准，清掉该时间窗口内服务端已没有的消息
    if ((opts.initial || opts.latest) && opts.since > 0) pruneMissing(fresh, list.length < Core.HISTORY_LIMIT, opts.since);
    if (opts && opts.initial) S.noMoreHistory = list.length < Core.HISTORY_LIMIT;
    renderMore();
    if (!(opts && opts.older) && (stick || (opts && opts.initial))) scrollToBottom();
  }
  function ingest(msg, opts) {
    var stick = nearBottom();
    if (!S.store.has(msg.id)) msg.seenAt = now();
    var r = S.store.upsert(msg);
    (r.evicted || []).forEach(dropNode);
    if (r.action === 'insert') {
      var node = buildNode(r.msg);
      S.nodes[r.msg.id] = node;
      var ref = els.msgs.children[r.index] || null;
      els.msgs.insertBefore(node, ref);
    } else if (r.action === 'update') {
      var old = S.nodes[r.msg.id];
      var fresh = buildNode(r.msg);
      S.nodes[r.msg.id] = fresh;
      if (old && old.parentNode && !r.moved) els.msgs.replaceChild(fresh, old);
      else {
        if (old && old.parentNode) old.parentNode.removeChild(old);
        els.msgs.insertBefore(fresh, els.msgs.children[r.index] || null);
      }
    } else {
      return r;
    }
    if (!(opts && opts.batch)) {
      var mine = r.msg.identity && r.msg.identity === S.me.identity;
      if (stick || mine) scrollToBottom();
      else if (r.action === 'insert') { S.newBelow++; renderNewBtn(); }
    }
    return r;
  }
  // complete=true：服务端返回不足一页，说明已是全部现存消息；否则只清理这一页覆盖的时间窗口
  // since：本次请求发出的本地时间；请求发出之后才收到的实时消息不参与清理（防止与 WS 广播竞态误删）
  function pruneMissing(fresh, complete, since) {
    var keep = Object.create(null);
    var minTs = Infinity;
    fresh.forEach(function (m) { keep[m.id] = true; if (m.ts < minTs) minTs = m.ts; });
    S.store.items.slice().forEach(function (m) {
      if (keep[m.id] || m.local) return;   // 本端发送中 / 发送失败的消息不受影响
      if (!(m.seenAt < since)) return;
      if (!complete && m.ts < minTs) return;
      removeMessage(m.id);
    });
  }
  function dropNode(id) {
    var n = S.nodes[id];
    if (n && n.parentNode) n.parentNode.removeChild(n);
    delete S.nodes[id];
  }
  function removeMessage(id) {
    if (S.pending[id]) { clearTimeout(S.pending[id].timer); delete S.pending[id]; }
    S.store.remove(id);
    dropNode(id);
  }
  function buildNode(m) {
    var isSystem = m.sender === 'system';
    var isAgent = !isSystem && (m.role === 'agent' || m.sender === 'agent');
    var mine = !isSystem && !!m.identity && m.identity === S.me.identity;
    var row = mk('div', 'hm' + (isSystem ? ' hm-system' : '') + (isAgent ? ' hm-agent' : '') + (mine ? ' hm-mine' : ''));
    row.setAttribute('data-hall-id', m.id);
    if (isSystem) {
      row.appendChild(mk('div', 'hm-sys', m.content));
      return row;
    }
    var av = mk('div', 'hm-av' + (isAgent ? ' agent' : ''));
    if (isAgent) av.textContent = Core.avatarText(m.name, 'agent');
    else av.innerHTML = ICON.person;
    row.appendChild(av);
    var body = mk('div', 'hm-body');
    var meta = mk('div', 'hm-meta');
    meta.appendChild(mk('span', 'hm-name', mine ? '我' : (m.name || (isAgent ? '客服' : '访客'))));
    if (isAgent) meta.appendChild(mk('span', 'hm-badge', '客服'));
    var tm = mk('span', 'hm-time', host.fmtMsgTime(m.ts));
    tm.title = host.fmtAbs(m.ts);
    meta.appendChild(tm);
    body.appendChild(meta);
    var bubble = mk('div', 'hm-bubble');
    if (m.mediaUrl && m.mediaKind === 'image') {
      bubble.className += ' hm-bubble-img';
      var img = document.createElement('img');
      img.className = 'hm-img';
      img.alt = '图片';
      img.setAttribute('loading', 'lazy');
      img.setAttribute('decoding', 'async');
      var src = host.httpBase + m.mediaUrl;
      img.src = src;
      img.onclick = function () { host.openImage(src); };
      bubble.appendChild(img);
    } else if (m.mediaUrl) {
      var a = document.createElement('a');
      a.className = 'hm-file';
      a.href = host.httpBase + m.mediaUrl;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      a.setAttribute('download', m.mediaName || '');
      var ico = mk('span', 'hm-file-ico');
      ico.innerHTML = ICON.file;
      a.appendChild(ico);
      var fm = mk('span', 'hm-file-meta');
      fm.appendChild(mk('span', 'hm-file-name', m.mediaName || '附件'));
      fm.appendChild(mk('span', 'hm-file-sub', (m.mediaSize ? Core.formatBytes(m.mediaSize) + ' · ' : '') + '点击下载'));
      a.appendChild(fm);
      bubble.appendChild(a);
    }
    if (m.content) bubble.appendChild(mk('span', 'hm-text', m.content));
    body.appendChild(bubble);
    if (m.local === 'sending') body.appendChild(mk('div', 'hm-state', '发送中…'));
    else if (m.local === 'failed') {
      var st = mk('div', 'hm-state is-failed', '未发送：' + (m.failReason || '发送失败') + ' ');
      var rb = mk('button', 'hm-resend', '重发');
      rb.type = 'button';
      rb.onclick = safe(function () { resend(m.id); });
      st.appendChild(rb);
      body.appendChild(st);
    }
    row.appendChild(body);
    return row;
  }
  function nearBottom() {
    var sc = els.scroll;
    if (!sc) return true;
    return ((sc.scrollHeight || 0) - (sc.scrollTop || 0) - (sc.clientHeight || 0)) < NEAR_BOTTOM_PX;
  }
  function scrollToBottom() {
    S.newBelow = 0;
    renderNewBtn();
    var sc = els.scroll;
    if (!sc) return;
    var go = function () { sc.scrollTop = sc.scrollHeight || 0; };
    go();
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(function () { requestAnimationFrame(go); });
  }
  function renderNewBtn() {
    if (!els.newBtn) return;
    els.newBtn.hidden = !(S.newBelow > 0);
    if (S.newBelow > 0) els.newBtn.textContent = S.newBelow + ' 条新消息';
  }
  function renderMore() {
    if (!els.more) return;
    els.more.hidden = S.noMoreHistory || S.store.size() < Core.HISTORY_LIMIT || S.store.size() >= Core.KEEP_LIMIT;
  }

  // ====== 发送 ======
  function canChat() { return S.settings.chatEnabled && S.subscribed && !!host && host.isWSOpen(); }
  function chatBlockedText() {
    if (!S.settings.chatEnabled) return Core.CHAT_PAUSED_TEXT;
    if (S.phase === 'kicked') return S.notice || Core.errorText(40372);
    if (S.phase === 'elsewhere') return '已在其他页面收听大厅，请在当前页面点击“在此页面收听”';
    if (S.phase === 'full') return Core.errorText(40977);
    return '大厅尚未连接，暂时无法发送';
  }
  function sendFromInput() {
    var text = els.input ? String(els.input.value || '').trim() : '';
    if (!text && !S.pendingFiles.length) return;
    if (!canChat()) { toast(chatBlockedText()); return; }
    if (text) {
      if (Core.textLength(text) > Core.CONTENT_MAX) { toast('单条消息不能超过 ' + Core.CONTENT_MAX + ' 字'); return; }
      if (!S.rate.tryTake(now())) { toast('发送过于频繁，请稍后再试'); return; }
      sendChat({ content: text });
      els.input.value = '';
      if (els.input.style) els.input.style.height = 'auto';
    }
    if (S.pendingFiles.length) sendFiles();
  }
  function sendChat(payload) {
    var id;
    try { id = Core.newUUIDv4(); } catch (e) { toast(e.message || '当前浏览器不支持安全随机数'); return ''; }
    var env = { type: 'hall_chat', id: id, content: payload.content || '' };
    if (payload.media) { env.media = payload.media; env.mkind = payload.mkind; env.mname = payload.mname; env.msize = payload.msize; }
    rememberOwned(id);
    var local = Core.normalizeMessage({
      id: id, sender: 'visitor', role: 'visitor', identity: S.me.identity, name: S.me.name,
      content: env.content, media_url: env.media || '', media_kind: env.mkind || '', media_name: env.mname || '', media_size: env.msize || 0,
      ts: now()
    }, { origin: originOfHttpBase() });
    if (!local) return '';
    local.local = 'sending';
    local.payload = payload;
    ingest(local, {});
    if (!host.sendWS(env)) { markFailed(id, '未连接'); return id; }
    S.pending[id] = { timer: setTimeout(function () { markFailed(id, '发送超时'); }, ACK_TIMEOUT_MS) };
    return id;
  }
  // serverTs：hall_ack 带回的服务器时间（毫秒）。自己的消息以服务器时间排序，避免本机时钟偏差导致顺序与他人看到的不一致
  function markAck(id, serverTs) {
    var p = S.pending[id];
    if (p) { clearTimeout(p.timer); delete S.pending[id]; }
    var m = S.store.get(id);
    if (!m || !m.local) return;
    var patch = { local: '' };
    if (typeof serverTs === 'number' && isFinite(serverTs) && serverTs > 0) patch.ts = Math.floor(serverTs);
    ingest(Object.assign({}, m, patch), { batch: true });
  }
  function markFailed(id, reason) {
    var p = S.pending[id];
    if (p) { clearTimeout(p.timer); delete S.pending[id]; }
    var m = S.store.get(id);
    if (m && m.local === 'sending') ingest(Object.assign({}, m, { local: 'failed', failReason: reason }), { batch: true });
  }
  function resend(id) {
    var m = S.store.get(id);
    if (!m || m.local !== 'failed' || !m.payload) return;
    if (!canChat()) { toast(chatBlockedText()); return; }
    if (!S.rate.tryTake(now())) { toast('发送过于频繁，请稍后再试'); return; }
    S.store.drop(id);
    dropNode(id);
    sendChat(m.payload);
  }
  function clearPendingTimers() {
    Object.keys(S.pending).forEach(function (id) { clearTimeout(S.pending[id].timer); });
  }

  // 待发送附件队列（与私聊相同的预览样式），点“发送”后依次上传，每个文件一条消息
  function addPendingFile(file) {
    if (!file || !els.pendingList) return;
    var err = Core.checkUploadFile(file);
    if (err) { toast(err); return; }
    if (S.pendingFiles.length >= 9) { toast('一次最多发送 9 个文件'); return; }
    var chip = mk('div', 'pending-chip');
    var item = { file: file, chip: chip, blobUrl: '' };
    if (Core.isImageFile(file) && typeof URL.createObjectURL === 'function') {
      var img = document.createElement('img');
      img.className = 'thumb';
      img.alt = '';
      item.blobUrl = URL.createObjectURL(file);
      img.src = item.blobUrl;
      chip.appendChild(img);
    } else {
      var ic = mk('span', 'file-icon');
      ic.innerHTML = ICON.file;
      chip.appendChild(ic);
    }
    var meta = mk('span', 'meta');
    meta.appendChild(mk('div', 'name', file.name || '文件'));
    meta.appendChild(mk('div', 'size', Core.formatBytes(file.size || 0)));
    chip.appendChild(meta);
    var rm = mk('button', 'remove-btn', '×');
    rm.type = 'button';
    rm.title = '移除';
    rm.onclick = function () { removePendingFile(item); };
    chip.appendChild(rm);
    S.pendingFiles.push(item);
    els.pendingList.appendChild(chip);
    els.pendingList.classList.add('is-show');
  }
  function removePendingFile(item) {
    var i = S.pendingFiles.indexOf(item);
    if (i < 0) return;
    S.pendingFiles.splice(i, 1);
    if (item.blobUrl) { try { URL.revokeObjectURL(item.blobUrl); } catch (e) {} }
    if (item.chip.parentNode) item.chip.parentNode.removeChild(item.chip);
    if (!S.pendingFiles.length && els.pendingList) els.pendingList.classList.remove('is-show');
  }
  function clearPendingFiles() { S.pendingFiles.slice().forEach(removePendingFile); }
  async function sendFiles() {
    var items = S.pendingFiles.slice();
    items.forEach(removePendingFile);
    var origin = originOfHttpBase();
    for (var i = 0; i < items.length; i++) {
      var file = items[i].file;
      if (!canChat()) { toast(chatBlockedText()); return; }
      if (!S.rate.tryTake(now())) { toast('发送过于频繁，剩余文件未发送'); return; }
      var data;
      try {
        data = await host.upload(file, { scope: 'hall' });
      } catch (e) {
        if (e && e.code === 40371) { applySettings(Object.assign({}, S.settings, { enabled: false }), 'local'); return; }
        if (e && e.code === 40378) {
          // 客服已关闭大厅文字聊天：切换为暂停状态，剩余文件不再发送
          applySettings(Object.assign({}, S.settings, { chatEnabled: false }), 'local');
          toast(Core.errorText(40378));
          return;
        }
        toast(Core.errorText(e && e.code, { action: 'upload', data: e && e.data, msg: e && e.message }));
        continue;
      }
      var url = data && Core.safeMediaPath(data.url, origin);
      if (!url) { toast('文件发送失败：服务器返回的地址无效'); continue; }
      sendChat({
        content: '',
        media: url,
        mkind: data.kind === 'image' ? 'image' : 'file',
        mname: Core.clampText(data.name || file.name || '', 255),
        msize: Number(data.size) || file.size || 0
      });
    }
  }

  // ====== 参与者条（防抖增量渲染） ======
  function schedulePeople() {
    if (S.peopleTimer) return;
    S.peopleTimer = setTimeout(function () { S.peopleTimer = null; safe(renderPeople)(); }, 150);
  }
  function onlineCount() {
    var room = S.room;
    if (!room) return 0;
    return (room.remoteParticipants ? room.remoteParticipants.size : 0) + 1;
  }
  function renderPeople() {
    var room = S.room;
    var list = [];
    if (room && S.me.identity) {
      list.push({ identity: S.me.identity, name: S.me.name || '我', role: 'visitor', micOn: S.micOn, camOn: S.camOn, speaking: !!S.speakers[S.me.identity], isLocal: true, joinedAt: 0 });
      room.remoteParticipants.forEach(function (p) {
        list.push({
          identity: p.identity, name: Core.clampText(p.name || '参与者', 32), role: roleOf(p),
          micOn: !!p.isMicrophoneEnabled, camOn: !!p.isCameraEnabled,
          speaking: !!S.speakers[p.identity] || !!p.isSpeaking, isLocal: false,
          joinedAt: p.joinedAt && p.joinedAt.getTime ? p.joinedAt.getTime() : 0
        });
      });
    }
    var sorted = Core.sortParticipants(list);
    var shown = sorted.slice(0, PEOPLE_RENDER_MAX);
    var keep = Object.create(null);
    if (els.people) {
      shown.forEach(function (p) {
        keep[p.identity] = true;
        var rec = S.peopleEls[p.identity];
        if (!rec) {
          var item = mk('div', 'hp');
          item.setAttribute('data-identity', p.identity);
          if (p.isLocal) item.setAttribute('data-self', '1');
          var av = mk('span', 'hp-av');
          var badge = mk('span', 'hp-mic');
          var name = mk('span', 'hp-name');
          item.appendChild(av);
          item.appendChild(badge);
          item.appendChild(name);
          rec = S.peopleEls[p.identity] = { el: item, av: av, badge: badge, name: name, sig: '' };
        }
        var sig = [p.name, p.role, p.micOn, p.camOn, p.speaking, p.isLocal].join('|');
        if (rec.sig !== sig) {
          rec.sig = sig;
          rec.el.className = 'hp' + (p.role === 'agent' ? ' is-agent' : '') + (p.speaking ? ' is-speaking' : '') + (p.micOn ? ' is-mic-on' : '') + (p.isLocal ? ' is-self' : '');
          if (p.role === 'agent') { rec.av.innerHTML = ''; rec.av.textContent = Core.avatarText(p.name, 'agent'); }
          else rec.av.innerHTML = ICON.person;
          rec.badge.innerHTML = (p.micOn ? ICON.mic : ICON.micOff) + (p.camOn ? ICON.cam : '');
          rec.name.textContent = p.isLocal ? '我' : p.name;
          rec.el.title = (p.isLocal ? (p.name + '（我）') : p.name) + (p.role === 'agent' ? ' · 客服' : '') + (p.micOn ? ' · 已开麦' : ' · 未开麦') + (p.camOn ? ' · 摄像头已开' : '');
        }
        els.people.appendChild(rec.el);   // appendChild 会移动已有节点，按排序结果重排
      });
      Object.keys(S.peopleEls).forEach(function (id) {
        if (!keep[id]) {
          var r = S.peopleEls[id];
          if (r.el.parentNode) r.el.parentNode.removeChild(r.el);
          delete S.peopleEls[id];
        }
      });
      var extra = sorted.length - shown.length;
      if (extra > 0) {
        if (!S.peopleMoreEl) S.peopleMoreEl = mk('div', 'hp hp-more');
        S.peopleMoreEl.textContent = '+' + extra;
        S.peopleMoreEl.title = '另有 ' + extra + ' 人在大厅';
        els.people.appendChild(S.peopleMoreEl);
      } else if (S.peopleMoreEl && S.peopleMoreEl.parentNode) S.peopleMoreEl.parentNode.removeChild(S.peopleMoreEl);
      els.people.hidden = list.length === 0;
    }
    renderStatus();
    postStatus();
  }

  // ====== 状态显示 ======
  var PHASE_TEXT = {
    idle: '正在进入大厅…', joining: '正在进入大厅…', connecting: '正在连接大厅语音…',
    reconnecting: '网络波动，正在重连大厅…', degraded: '大厅语音未连接', elsewhere: '已在其他页面收听大厅',
    kicked: '已被移出大厅', full: '大厅人数已满', closed: '大厅已关闭', error: '大厅连接失败'
  };
  function setPhase(phase, notice) {
    S.phase = phase;
    S.notice = notice || '';
    renderStatus();
    renderControls();
    postStatus();
  }
  function renderStatus() {
    var count = onlineCount();
    if (els.status) els.status.textContent = S.phase === 'live' ? ('大厅 · ' + count + ' 人在线') : (PHASE_TEXT[S.phase] || '');
    if (els.tabHallCount) els.tabHallCount.textContent = S.phase === 'live' && count > 0 ? String(count) : '';
    var text = '', btn = '', kind = '';
    if (S.phase === 'elsewhere') { text = '您已在其他页面收听大厅'; btn = '在此页面收听'; kind = 'takeover'; }
    else if (S.phase === 'kicked') { text = S.notice || Core.errorText(40372); }
    else if (S.phase === 'full') { text = S.notice || Core.errorText(40977); btn = '重试'; kind = 'retry'; }
    else if (S.phase === 'error') { text = S.notice || PHASE_TEXT.error; btn = '立即重试'; kind = 'retry'; }
    else if (S.phase === 'degraded') { text = S.notice || PHASE_TEXT.degraded; btn = '立即重试'; kind = 'retry'; }
    else if (audioBlocked()) { text = '浏览器限制了自动播放，点击后即可听到大厅声音'; btn = '点击收听大厅'; kind = 'listen'; }
    watchGesture(kind === 'listen');
    if (els.notice) {
      els.notice.hidden = !text;
      els.notice.setAttribute('data-kind', kind);
      if (els.noticeText) els.noticeText.textContent = text;
      if (els.noticeBtn) { els.noticeBtn.hidden = !btn; els.noticeBtn.textContent = btn; }
    }
  }
  function onNoticeAction() {
    var kind = els.notice ? els.notice.getAttribute('data-kind') : '';
    if (kind === 'listen') { tryStartAudio(); return; }
    if (kind === 'takeover' || kind === 'retry') { S.attempt = 0; setPhase('joining'); scheduleJoin(0); }
  }
  function renderControls() {
    var live = S.phase === 'live' && !!S.room;
    var view = Core.switchView(S.settings);
    if (els.micBtn) {
      els.micBtn.hidden = !view.micButton;
      els.micBtn.disabled = S.micBusy || (!live && !S.micOn);
      els.micBtn.setAttribute('aria-pressed', S.micOn ? 'true' : 'false');
      els.micBtn.classList.toggle('is-on', S.micOn);
      els.micBtn.classList.toggle('is-busy', S.micBusy);
      var mi = els.micBtn.querySelector ? els.micBtn.querySelector('.hall-btn-ico') : null;
      if (mi) mi.innerHTML = S.micOn ? ICON.mic : ICON.micOff;
      if (els.micLabel) els.micLabel.textContent = S.micBusy ? '请稍候' : (S.micOn ? '关麦' : '开麦');
      els.micBtn.title = S.micOn ? '关闭麦克风' : '打开麦克风（大厅所有人都能听到）';
    }
    if (els.camBtn) {
      els.camBtn.hidden = !view.camButton;
      els.camBtn.disabled = S.camBusy || (!live && !S.camOn);
      els.camBtn.setAttribute('aria-pressed', S.camOn ? 'true' : 'false');
      els.camBtn.classList.toggle('is-on', S.camOn);
      var ci = els.camBtn.querySelector ? els.camBtn.querySelector('.hall-btn-ico') : null;
      if (ci) ci.innerHTML = S.camOn ? ICON.cam : ICON.camOff;
      if (els.camLabel) els.camLabel.textContent = S.camBusy ? '请稍候' : (S.camOn ? '关摄像头' : '摄像头');
    }
    if (els.muteBtn) {
      els.muteBtn.setAttribute('aria-pressed', S.muted ? 'true' : 'false');
      els.muteBtn.classList.toggle('is-on', S.muted);
      var vi = els.muteBtn.querySelector ? els.muteBtn.querySelector('.hall-btn-ico') : null;
      if (vi) vi.innerHTML = S.muted ? ICON.volumeOff : ICON.volume;
      if (els.muteLabel) els.muteLabel.textContent = S.muted ? '已静音' : '静音大厅';
      els.muteBtn.title = S.muted ? '恢复播放大厅声音' : '静音大厅（只影响本机播放）';
    }
    if (els.voicePaused) {
      els.voicePaused.hidden = !view.voicePaused;
      if (els.voicePausedText) els.voicePausedText.textContent = view.voicePaused;
    }
    // 文字聊天暂停：隐藏输入框（含待发送附件）、附件与发送按钮，显示暂停提示；历史消息照常显示
    var chatOk = S.phase !== 'elsewhere' && S.phase !== 'kicked' && S.phase !== 'closed';
    if (els.inputWrap) els.inputWrap.hidden = !view.chatInput;
    else if (els.input) els.input.hidden = !view.chatInput;   // 旧版页面结构兜底
    if (els.sendBtn) { els.sendBtn.hidden = !view.chatInput; els.sendBtn.disabled = !chatOk; }
    if (els.input) els.input.disabled = !chatOk || !view.chatInput;
    if (els.fileBtn) { els.fileBtn.hidden = !view.chatInput; els.fileBtn.disabled = !chatOk; }
    if (els.chatPaused) {
      els.chatPaused.hidden = !view.chatPaused;
      if (els.chatPausedText) els.chatPausedText.textContent = view.chatPaused;
    }
  }

  // ====== 提示条 ======
  function toast(text) {
    if (!els.toast || !text) return;
    els.toast.textContent = text;
    els.toast.hidden = false;
    if (S.toastTimer) clearTimeout(S.toastTimer);
    S.toastTimer = setTimeout(function () { els.toast.hidden = true; }, TOAST_MS);
  }

  // ====== 宿主页同步（loader.js 显示在线人数与收听状态） ======
  function statusState() {
    if (!S.settings.enabled || S.phase === 'closed') return 'off';
    if (S.phase === 'elsewhere' || S.phase === 'kicked' || S.phase === 'full' || S.phase === 'error') return S.phase;
    if (S.phase !== 'live') return 'connecting';
    if (audioBlocked()) return 'blocked';
    if (!wantAudio()) return 'muted';
    return 'live';
  }
  function anyoneSpeaking() {
    for (var id in S.speakers) if (id !== S.me.identity) return true;
    return false;
  }
  function postStatus() {
    if (!host || window.parent === window) return;
    var st = statusState();
    var msg = Core.buildHallStatus({ state: st, online: st === 'live' || st === 'blocked' || st === 'muted' ? onlineCount() : 0, speaking: st === 'live' && anyoneSpeaking() });
    var key = msg.state + '|' + msg.online + '|' + msg.speaking;
    if (key === S.statusKey) return;
    // 节流：最多每 300ms 发一次，保证最后一次状态一定送达
    var wait = 300 - (now() - S.statusLastAt);
    if (wait > 0) {
      if (!S.statusTimer) S.statusTimer = setTimeout(function () { S.statusTimer = null; postStatus(); }, wait);
      return;
    }
    S.statusKey = key;
    S.statusLastAt = now();
    host.postToParent(msg, S.hostOrigin || '*');
  }

  // ====== 宿主页命令（校验来源窗口、来源 origin 与消息结构） ======
  function onHostCommand(ev) {
    if (!ev || ev.source !== window.parent || window.parent === window) return;
    if (S.hostOrigin && ev.origin !== S.hostOrigin) return;
    var cmd = Core.validateHostCommand(ev.data);
    if (!cmd) return;
    if (cmd.cmd === 'start_audio') tryStartAudio();
    else if (cmd.cmd === 'show_hall' && S.tabsShown) selectTab('hall');
  }

  // ====== chat.js 回调 ======
  function onWidgetState(open) {
    if (open && S.tab === 'hall' && els.tabHallDot) els.tabHallDot.hidden = true;
    if (open && S.tab === 'hall') scrollToBottom();
    reconcileVideo();
  }
  function onPrivateIncoming() {
    if (S.phase === 'closed') S.privateActivity = true;
    if (!isPrivateVisible() && els.tabPrivateDot) els.tabPrivateDot.hidden = false;
  }
  // 访客在私聊页发出消息（chat.js 调用）：大厅关闭期间有私聊往来时，重新打开后不把访客切离私聊页
  function onPrivateActivity() {
    if (S.phase === 'closed') S.privateActivity = true;
  }
  function isPrivateVisible() { return !S.tabsShown || S.tab === 'private'; }

  window.CSHall = {
    attach: safe(attach),
    onSessionReady: safe(onSessionReady),
    onSettings: safe(onSettings),
    onEnvelope: safe(onEnvelope),
    ownsMessageId: ownsMessageId,
    onWSOpen: safe(onWSOpen),
    onWSClose: safe(onWSClose),
    onWidgetState: safe(onWidgetState),
    onPrivateIncoming: safe(onPrivateIncoming),
    onPrivateActivity: safe(onPrivateActivity),
    isPrivateVisible: isPrivateVisible,
    onVoiceState: safe(onVoiceState),
    onHostCommand: safe(onHostCommand)
  };
})();
