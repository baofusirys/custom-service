/*
 * Custom Service · 多人大厅 · 纯逻辑模块（[119]）
 *
 * 只放与 DOM、网络、LiveKit 无关的纯函数与小型数据结构，便于在 Node 中做单元测试：
 *   - 大厅消息规范化、合并去重、删除墓碑（MessageStore）
 *   - 断线重连指数退避（带上限与抖动）
 *   - 接口错误码 / 管理动作 → 中文提示文案
 *   - postMessage / BroadcastChannel 消息结构校验
 *   - join 返回值校验（LiveKit 地址协议、ICE 服务器白名单）
 *   - 上传前置校验（与后端 security.ValidateUpload 白名单一致）
 *   - 参与者排序、视频订阅挑选、发送频率滑动窗口
 *   - [123] 客服可控的大厅开关（总开关 / 文字聊天 / 语音 / 视频）：设置合并、开关 → 界面状态映射、变化提示
 *
 * 浏览器中挂到 window.CSHallCore；Node 中通过 module.exports 导出。
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === 'object' && module && module.exports) module.exports = api;
  else root.CSHallCore = api;
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  // ====== 常量（与契约 _internal/reports/hall-contract.md 对齐） ======
  var HISTORY_LIMIT = 50;          // 进入大厅 / 刷新时显示最近 50 条
  var KEEP_LIMIT = 200;            // 内存与 DOM 中最多保留 200 条，超出淘汰最旧的
  var CONTENT_MAX = 1000;          // 单条文字上限 1000 字
  var TOMBSTONE_MAX = 500;         // 已删除消息 ID 墓碑上限，防止迟到的重放让已删消息复活
  var NAME_MAX = 64;
  var IDENTITY_MAX = 64;
  var MEDIA_NAME_MAX = 255;
  var ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
  var UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
  var SENDERS = { visitor: true, agent: true, system: true };
  var ROLES = { visitor: true, agent: true };
  var MEDIA_KINDS = { image: true, file: true };

  // 与后端 backend/internal/security/sanitize.go ValidateUpload 白名单保持一致（服务端仍做最终校验）
  var UPLOAD_EXTS = {
    jpg: 'image', jpeg: 'image', png: 'image', gif: 'image', webp: 'image',
    pdf: 'file', zip: 'file', doc: 'file', docx: 'file', xls: 'file', xlsx: 'file', txt: 'file'
  };
  var UPLOAD_ACCEPT = '.jpg,.jpeg,.png,.gif,.webp,.pdf,.zip,.doc,.docx,.xls,.xlsx,.txt';
  var UPLOAD_MAX_BYTES = 20 * 1024 * 1024;   // 后端 BACKEND_MAX_UPLOAD_MB 默认 20

  // ====== 基础工具 ======
  // 兼容后端 sql.NullString 序列化形态 {String, Valid}
  function str(v) {
    if (v && typeof v === 'object' && typeof v.String === 'string') return v.String;
    return typeof v === 'string' ? v : '';
  }
  // 按 Unicode 码点截断，避免把代理对截成半个字符
  function clampText(s, max) {
    s = str(s);
    if (s.length <= max) return s;
    return Array.from(s).slice(0, max).join('');
  }
  function textLength(s) { return Array.from(str(s)).length; }
  function validId(id) { return typeof id === 'string' && ID_RE.test(id); }
  function isUUIDv4(id) { return typeof id === 'string' && UUID_V4_RE.test(id); }

  // 时间统一换算为毫秒时间戳：数字（毫秒，过小视为秒）或 ISO 8601 字符串
  function toMs(v) {
    if (typeof v === 'number' && isFinite(v) && v > 0) return v < 1e11 ? Math.floor(v * 1000) : Math.floor(v);
    if (typeof v === 'string' && v) {
      if (/^\d+$/.test(v)) return toMs(Number(v));
      var p = Date.parse(v);
      if (isFinite(p)) return p;
    }
    return 0;
  }

  function toInt(v, def) {
    var n = typeof v === 'string' && v.trim() !== '' ? Number(v) : v;
    return typeof n === 'number' && isFinite(n) ? Math.floor(n) : def;
  }
  function toBool(v, def) {
    if (v === true || v === 1 || v === '1' || v === 'true') return true;
    if (v === false || v === 0 || v === '0' || v === 'false') return false;
    return def;
  }
  function clamp(n, lo, hi) { return Math.max(lo, Math.min(hi, n)); }

  // 生成 UUID v4（契约要求 hall_chat.id 为 uuid v4），只用密码学随机源
  function newUUIDv4(cryptoObj) {
    var c = cryptoObj || (typeof crypto !== 'undefined' ? crypto : null);
    if (c && typeof c.randomUUID === 'function') {
      try { return c.randomUUID(); } catch (e) { /* 非安全上下文时回落到 getRandomValues */ }
    }
    if (!c || typeof c.getRandomValues !== 'function') throw new Error('当前浏览器不支持安全随机数');
    var b = new Uint8Array(16);
    c.getRandomValues(b);
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    var h = '';
    for (var i = 0; i < 16; i++) h += (b[i] < 16 ? '0' : '') + b[i].toString(16);
    return h.slice(0, 8) + '-' + h.slice(8, 12) + '-' + h.slice(12, 16) + '-' + h.slice(16, 20) + '-' + h.slice(20);
  }

  // 只接受本系统 /files/ 下的同源地址（与 chat.js safeMediaPath 规则一致），返回 path+query
  function safeMediaPath(raw, origin) {
    raw = str(raw);
    if (!raw || !origin) return '';
    try {
      var u = new URL(raw, origin);
      if (u.origin !== origin || u.pathname.indexOf('/files/') !== 0) return '';
      if (u.pathname.indexOf('..') >= 0 || u.pathname.indexOf('\\') >= 0) return '';
      return u.pathname + u.search;
    } catch (e) { return ''; }
  }

  // ====== 大厅消息规范化 ======
  // 同时接受两种来源：
  //   HTTP HallMessage：{id, sender, identity, name, role, content, media_url, media_kind, media_name, media_size, created_at}
  //   WS 广播（沿用 ws.Envelope 字段名）：{type:"hall_chat", id, content, media, mkind, mname, msize, ts(毫秒), extra:{sender, identity, name, role}}
  // 返回统一结构；不合法返回 null。opts.origin 用于媒体地址同源校验。
  function normalizeMessage(raw, opts) {
    if (!raw || typeof raw !== 'object') return null;
    opts = opts || {};
    var id = raw.id;
    if (!validId(id)) return null;
    var extra = raw.extra && typeof raw.extra === 'object' ? raw.extra : {};
    var role = str(raw.role || extra.role);
    if (!ROLES[role]) role = '';
    var sender = str(raw.sender || extra.sender);
    if (!SENDERS[sender]) sender = role || '';
    if (!sender) return null;
    if (!role && sender !== 'system') role = sender;
    var mediaRaw = raw.media_url !== undefined ? raw.media_url : raw.media;
    var mediaUrl = safeMediaPath(mediaRaw, opts.origin);
    var kind = str(raw.media_kind !== undefined ? raw.media_kind : raw.mkind);
    if (!MEDIA_KINDS[kind]) kind = mediaUrl ? 'file' : '';
    if (!mediaUrl) kind = '';
    var size = toInt(raw.media_size !== undefined ? raw.media_size : raw.msize, 0);
    var msg = {
      id: id,
      sender: sender,
      role: role,
      identity: clampText(raw.identity !== undefined ? raw.identity : extra.identity, IDENTITY_MAX),
      name: clampText(raw.name !== undefined ? raw.name : extra.name, NAME_MAX),
      content: clampText(raw.content, CONTENT_MAX * 4),
      mediaUrl: mediaUrl,
      mediaKind: kind,
      mediaName: mediaUrl ? clampText(raw.media_name !== undefined ? raw.media_name : raw.mname, MEDIA_NAME_MAX) : '',
      mediaSize: mediaUrl && size > 0 ? size : 0,
      ts: toMs(raw.created_at) || toMs(raw.ts) || (opts.now || Date.now())
    };
    if (!msg.content && !msg.mediaUrl) return null;
    return msg;
  }

  // ====== 消息存储：按时间排序、按 ID 去重、删除墓碑、容量淘汰 ======
  function MessageStore(limit) {
    this.limit = limit > 0 ? limit : KEEP_LIMIT;
    this.items = [];
    this.byId = Object.create(null);
    this.tombstones = Object.create(null);
    this.tombOrder = [];
  }
  MessageStore.prototype._indexFor = function (ts) {
    // 绝大多数是新消息追加到末尾：从尾部往前找插入点（同时间戳保持到达顺序）
    var i = this.items.length;
    while (i > 0 && this.items[i - 1].ts > ts) i--;
    return i;
  };
  MessageStore.prototype.indexOf = function (id) {
    for (var i = this.items.length - 1; i >= 0; i--) if (this.items[i].id === id) return i;
    return -1;
  };
  MessageStore.prototype.has = function (id) { return !!this.byId[id]; };
  MessageStore.prototype.get = function (id) { return this.byId[id] || null; };
  MessageStore.prototype.isDeleted = function (id) { return !!this.tombstones[id]; };
  // 写入一条：返回 {action:'insert'|'update'|'ignore', index, msg, moved, evicted:[id...]}
  MessageStore.prototype.upsert = function (msg) {
    if (!msg || !validId(msg.id)) return { action: 'ignore', evicted: [] };
    if (this.tombstones[msg.id]) return { action: 'ignore', evicted: [] };
    var old = this.byId[msg.id];
    if (old) {
      var oldIndex = this.indexOf(msg.id);
      var merged = Object.assign({}, old, msg);
      // 本地回显先占位，服务端广播带权威时间到达时以服务端为准
      this.items.splice(oldIndex, 1);
      var ni = this._indexFor(merged.ts);
      this.items.splice(ni, 0, merged);
      this.byId[msg.id] = merged;
      return { action: 'update', index: ni, msg: merged, moved: ni !== oldIndex, evicted: [] };
    }
    var index = this._indexFor(msg.ts);
    var stored = Object.assign({}, msg);
    this.items.splice(index, 0, stored);
    this.byId[msg.id] = stored;
    var evicted = [];
    while (this.items.length > this.limit) {
      var gone = this.items.shift();
      delete this.byId[gone.id];
      evicted.push(gone.id);
      index--;
    }
    if (index < 0) {
      // 新消息比保留窗口里最旧的还旧，插入后立即被淘汰
      return { action: 'ignore', evicted: evicted.filter(function (x) { return x !== stored.id; }) };
    }
    return { action: 'insert', index: index, msg: stored, moved: false, evicted: evicted };
  };
  MessageStore.prototype.remove = function (id) {
    if (!validId(id)) return false;
    if (!this.tombstones[id]) {
      this.tombstones[id] = true;
      this.tombOrder.push(id);
      while (this.tombOrder.length > TOMBSTONE_MAX) delete this.tombstones[this.tombOrder.shift()];
    }
    if (!this.byId[id]) return false;
    var i = this.indexOf(id);
    if (i >= 0) this.items.splice(i, 1);
    delete this.byId[id];
    return true;
  };
  // 本地回显发送失败后放弃：只移除，不留墓碑（同 ID 不会再出现）
  MessageStore.prototype.drop = function (id) {
    if (!this.byId[id]) return false;
    var i = this.indexOf(id);
    if (i >= 0) this.items.splice(i, 1);
    delete this.byId[id];
    return true;
  };
  MessageStore.prototype.oldestId = function () { return this.items.length ? this.items[0].id : ''; };
  MessageStore.prototype.size = function () { return this.items.length; };

  // ====== 重连退避：指数增长、有上限、带抖动（random 可注入便于测试） ======
  function backoffDelay(attempt, opts) {
    opts = opts || {};
    var base = opts.baseMs > 0 ? opts.baseMs : 2000;
    var factor = opts.factor > 1 ? opts.factor : 2;
    var max = opts.maxMs > 0 ? opts.maxMs : 60000;
    var jitter = typeof opts.jitter === 'number' ? clamp(opts.jitter, 0, 0.5) : 0.2;
    var rnd = typeof opts.random === 'function' ? opts.random : Math.random;
    var minMs = opts.minMs > 0 ? opts.minMs : 0;
    var n = Math.max(0, Math.floor(attempt || 0));
    var raw = Math.min(max, base * Math.pow(factor, Math.min(n, 30)));
    var r = clamp(Number(rnd()) || 0, 0, 1);
    var delay = Math.round(raw * (1 - jitter + 2 * jitter * r));
    // 抖动后仍不超过上限；服务端要求的最小等待（如被移出剩余时间）优先
    delay = Math.min(delay, max);
    return Math.max(delay, minMs, 250);
  }

  // ====== 错误码 → 中文提示 ======
  function minutesFromSeconds(sec) {
    var s = toInt(sec, 0);
    return s > 0 ? Math.max(1, Math.ceil(s / 60)) : 0;
  }
  function safeServerMsg(msg) {
    var m = str(msg).trim();
    // 服务端文案只当纯文本展示；过长或含控制字符、尖括号的不展示
    if (!m || m.length > 80 || /[\u0000-\u001f<>]/.test(m)) return '';
    return m;
  }
  // ctx: {action:'join'|'mic'|'camera'|'chat'|'upload', data:{retry_after, used, max}, msg}
  function errorText(code, ctx) {
    ctx = ctx || {};
    var data = ctx.data && typeof ctx.data === 'object' ? ctx.data : {};
    var action = ctx.action || '';
    switch (toInt(code, -1)) {
      case 40371: return '大厅已关闭，您仍可使用私聊客服';
      case 40372: {
        var m = minutesFromSeconds(data.retry_after);
        return '您已被移出大厅' + (m ? '，约 ' + m + ' 分钟后可重新进入' : '');
      }
      case 40374: return action === 'camera' ? '当前无法开启摄像头' : (action === 'chat' ? '当前无法在大厅发言' : '当前无法开麦');
      case 40375: return '大厅暂未开启视频，暂时不能开摄像头';
      case 40377: return '大厅语音已关闭';
      case 40378: return '大厅聊天已关闭';
      case 40973: {
        var used = toInt(data.used, -1), max = toInt(data.max, -1);
        return '开麦人数已满' + (used >= 0 && max > 0 ? '（' + used + '/' + max + '）' : '') + '，请稍后再试';
      }
      case 40976: return '摄像头人数已满，请稍后再试';
      case 40977: return '大厅人数已满，稍后会自动重试；您可以先使用私聊客服';
      case 42910: return '操作过于频繁，请稍后再试';
      case 40059: return '大厅设置不合法，请检查后重试';
      case 40060: return '请求参数错误，请刷新页面后重试';
      case 40061: return '文件发送失败：上传参数错误，请刷新页面后重试';
      case 40376: return '不能对该对象执行此操作';
      case 40477: return '该大厅消息已不存在';
      case 50370: return action === 'join' ? '语音服务暂不可用，稍后自动重试；仍可使用私聊客服' : '语音服务暂不可用，请稍后再试';
      case 50050: return '服务器繁忙，请稍后再试';
      case 42903: return '上传过于频繁，请稍后再试';
      case 41301: return '文件过大，无法发送';
      case 41501: return '不支持的文件类型，仅支持图片、PDF、压缩包、Office 文档与文本';
      case 40110: case 40108: case 40101: case 40102: case 40103: case 40104: return '登录状态已失效，请刷新页面后重试';
      case -2: return '网络异常，请检查网络后重试';
    }
    var fallback = action === 'upload' ? '文件发送失败' : (action === 'join' ? '进入大厅失败，稍后自动重试' : '操作失败，请稍后再试');
    var sm = safeServerMsg(ctx.msg);
    return sm ? fallback + '：' + sm : fallback;
  }

  // getUserMedia / 发布失败 → 中文提示
  function mediaErrorText(err, kind) {
    var what = kind === 'camera' ? '摄像头' : '麦克风';
    var name = err && err.name ? String(err.name) : '';
    if (name === 'NotAllowedError' || name === 'SecurityError' || name === 'PermissionDeniedError') return '无法使用' + what + '：请在浏览器地址栏中允许' + what + '权限';
    if (name === 'NotFoundError' || name === 'DevicesNotFoundError' || name === 'OverconstrainedError') return '没有找到可用的' + what;
    if (name === 'NotReadableError' || name === 'TrackStartError' || name === 'AbortError') return what + '正被其他程序占用，请关闭后重试';
    return what + '开启失败，请稍后重试';
  }

  // 客服管理动作（hall_moderation）→ 中文提示
  function moderationText(action, minutes) {
    var m = toInt(minutes, 0);
    switch (action) {
      case 'muted': return '客服已将您静音';
      case 'mute_all': return '客服已将全体访客静音';
      case 'camera_off': return '客服已关闭您的摄像头';
      case 'kicked': return '您已被客服移出大厅' + (m > 0 ? '，' + m + ' 分钟后可重新进入' : '');
    }
    return '';
  }

  // ====== 设置规范化（hall_settings.extra / 访客公开设置 / join 返回值） ======
  // [123] 契约第 10 节四个开关：enabled（总开关）、chatEnabled（文字/图片/文件）、voiceEnabled（语音）、videoEnabled（摄像头）。
  // 键名兼容三种来源：hall_settings.extra 与入场返回用 enabled / chat_enabled / voice_enabled / video_enabled，
  // /api/visitor/settings 用 hall_enabled / hall_chat_enabled / hall_voice_enabled / hall_video_enabled。
  // 缺省或取值不合法时沿用 prev（旧后端没有新开关字段时保持默认开启）。
  // 总开关关闭时服务端下发的文字/语音开关可能是派生出的 false（契约 10.3），而真实值保留不变（契约 10.1）：
  // 此时不覆盖已知的文字/语音开关，总开关重新打开时以广播或入场返回的真实值为准。
  function boolOr(v, d) { return typeof v === 'boolean' ? v : d; }
  function pickKey(raw, a, b) { return raw[a] !== undefined ? raw[a] : raw[b]; }
  function normalizeSettings(raw, prev) {
    var p = prev && typeof prev === 'object' ? prev : {};
    var base = {
      enabled: boolOr(p.enabled, true),
      videoEnabled: boolOr(p.videoEnabled, false),
      chatEnabled: boolOr(p.chatEnabled, true),
      voiceEnabled: boolOr(p.voiceEnabled, true),
      maxSpeakers: toInt(p.maxSpeakers, 6),
      maxCameras: toInt(p.maxCameras, 2)
    };
    raw = raw && typeof raw === 'object' ? raw : {};
    var enabled = toBool(pickKey(raw, 'enabled', 'hall_enabled'), base.enabled);
    var video = toBool(pickKey(raw, 'video_enabled', 'hall_video_enabled'), base.videoEnabled);
    var chat = base.chatEnabled, voice = base.voiceEnabled;
    if (enabled) {
      chat = toBool(pickKey(raw, 'chat_enabled', 'hall_chat_enabled'), base.chatEnabled);
      voice = toBool(pickKey(raw, 'voice_enabled', 'hall_voice_enabled'), base.voiceEnabled);
    }
    var sp = toInt(pickKey(raw, 'max_speakers', 'hall_max_speakers'), base.maxSpeakers);
    var cam = toInt(pickKey(raw, 'max_cameras', 'hall_max_cameras'), base.maxCameras);
    return { enabled: enabled, videoEnabled: video, chatEnabled: chat, voiceEnabled: voice, maxSpeakers: clamp(sp, 1, 20), maxCameras: clamp(cam, 0, 6) };
  }

  // ====== [123] 客服可控的大厅开关 → 访客窗口界面 ======
  var CHAT_PAUSED_TEXT = '大厅文字聊天已暂停';
  var VOICE_PAUSED_TEXT = '大厅语音已暂停';
  // 开关状态 → 界面（纯映射，hall.js 只负责写入 DOM）：
  //   tabs：是否显示「大厅」页签；chatInput：输入框、附件与发送按钮；micButton / camButton：开麦 / 摄像头按钮；
  //   chatPaused / voicePaused：暂停提示文案（空串表示不显示）。视频与文字、语音开关相互独立。
  function switchView(s) {
    s = s && typeof s === 'object' ? s : {};
    var hall = s.enabled !== false;
    var chat = hall && s.chatEnabled !== false;
    var voice = hall && s.voiceEnabled !== false;
    return {
      tabs: hall,
      chatInput: chat,
      chatPaused: hall && !chat ? CHAT_PAUSED_TEXT : '',
      micButton: voice,
      voicePaused: hall && !voice ? VOICE_PAUSED_TEXT : '',
      camButton: hall && s.videoEnabled === true
    };
  }
  // 前后两次设置之间各开关的变化：'on' | 'off' | ''（未变化）
  function switchChanges(prev, next) {
    prev = prev && typeof prev === 'object' ? prev : {};
    next = next && typeof next === 'object' ? next : {};
    function d(k) { var a = prev[k] === true, b = next[k] === true; return a === b ? '' : (b ? 'on' : 'off'); }
    return { hall: d('enabled'), chat: d('chatEnabled'), voice: d('voiceEnabled'), video: d('videoEnabled') };
  }
  // 客服切换开关后（WS 广播）给访客的一次性提示；总开关的变化由页签收起 / 恢复体现，不另外提示。
  // ctx.micStopped / ctx.camStopped：本次切换是否关掉了访客正在发布的麦克风 / 摄像头
  function switchToastText(ch, ctx) {
    if (!ch || typeof ch !== 'object' || ch.hall) return '';
    ctx = ctx || {};
    var parts = [];
    if (ch.chat === 'off') parts.push('客服已暂停大厅文字聊天');
    else if (ch.chat === 'on') parts.push('大厅文字聊天已恢复');
    if (ch.voice === 'off') parts.push('客服已暂停大厅语音' + (ctx.micStopped ? '，您的麦克风已关闭' : ''));
    else if (ch.voice === 'on') parts.push('大厅语音已恢复，您可以开麦');
    if (ch.video === 'off') parts.push('客服已关闭大厅视频' + (ctx.camStopped ? '，您的摄像头已关闭' : ''));
    else if (ch.video === 'on') parts.push('客服已开启大厅视频，您可以打开摄像头');
    return parts.join('；');
  }
  // 服务端以 error「大厅聊天已关闭」拒绝 hall_chat（契约 10.1）：据此把界面切到文字聊天暂停状态
  function isChatClosedError(content) { return str(content).indexOf('大厅聊天已关闭') >= 0; }
  // 总开关重新打开后停留在哪个页签：只有关闭前正停留在大厅页、且关闭期间没有私聊往来时才切回大厅；
  // 关闭前就在私聊页、页面加载时大厅已关闭、或关闭期间收发过私聊消息时都留在私聊页，不强制切走
  function tabAfterReopen(closedFrom, privateActivity) { return closedFrom === 'hall' && !privateActivity ? 'hall' : 'private'; }
  // 总开关重新打开时所有在线访客同时收到广播：入厅前随机错开 0.3–3 秒，避免同一瞬间集中请求后端与媒体服务
  var REOPEN_MIN_MS = 300, REOPEN_MAX_MS = 3000;
  function reopenJoinDelay(random) {
    var rnd = typeof random === 'function' ? random : Math.random;
    var r = clamp(Number(rnd()) || 0, 0, 1);
    return Math.round(REOPEN_MIN_MS + r * (REOPEN_MAX_MS - REOPEN_MIN_MS));
  }
  // 运行中得知的开关 → /api/visitor/settings 同名键（chat.js 据此刷新本标签页的设置缓存；总开关关闭时两项为 false，与契约 10.3 一致）
  function publicSettingsPatch(s) {
    s = s && typeof s === 'object' ? s : {};
    var hall = s.enabled !== false;
    return { hall_enabled: hall, hall_video_enabled: s.videoEnabled === true, hall_chat_enabled: hall && s.chatEnabled !== false, hall_voice_enabled: hall && s.voiceEnabled !== false };
  }

  // ====== join 返回值校验 ======
  function sanitizeIceServers(list) {
    var out = [];
    if (!Array.isArray(list)) return out;
    for (var i = 0; i < list.length && out.length < 6; i++) {
      var s = list[i];
      if (!s || typeof s !== 'object') continue;
      var urls = Array.isArray(s.urls) ? s.urls : (typeof s.urls === 'string' ? [s.urls] : []);
      urls = urls.filter(function (u) { return typeof u === 'string' && u.length <= 256 && /^(stun|turn|turns):[^\s]+$/i.test(u); }).slice(0, 6);
      if (!urls.length) continue;
      var item = { urls: urls };
      if (typeof s.username === 'string' && s.username && s.username.length <= 512) item.username = s.username;
      if (typeof s.credential === 'string' && s.credential && s.credential.length <= 512) item.credential = s.credential;
      out.push(item);
    }
    return out;
  }
  // 页面为 https 时只允许 wss；页面为 http（本地/测试环境）时允许 ws 与 wss
  function validLiveKitURL(url, pageProtocol) {
    if (typeof url !== 'string' || url.length > 512) return false;
    try {
      var u = new URL(url);
      if (u.username || u.password) return false;
      if (u.protocol === 'wss:') return true;
      return u.protocol === 'ws:' && pageProtocol === 'http:';
    } catch (e) { return false; }
  }
  // prevSettings：调用时的当前设置（入场返回缺少的开关沿用它，而不是退回默认值）
  function validateJoinData(data, pageProtocol, prevSettings) {
    if (!data || typeof data !== 'object') return null;
    if (!validLiveKitURL(data.url, pageProtocol)) return null;
    if (typeof data.token !== 'string' || !data.token || data.token.length > 8192 || /\s/.test(data.token)) return null;
    var identity = clampText(data.identity, IDENTITY_MAX);
    if (!identity) return null;
    return {
      url: data.url,
      token: data.token,
      room: clampText(data.room, 64),
      identity: identity,
      name: clampText(data.name, NAME_MAX),
      settings: normalizeSettings({
        video_enabled: data.video_enabled, max_speakers: data.max_speakers, max_cameras: data.max_cameras,
        chat_enabled: data.chat_enabled, voice_enabled: data.voice_enabled
      }, prevSettings),
      iceServers: sanitizeIceServers(data.ice_servers),
      history: Array.isArray(data.history) ? data.history.slice(-HISTORY_LIMIT) : []
    };
  }

  // ====== 参与者 ======
  // participant metadata 由服务端签发：{"role":"visitor"|"agent","code":"NNNN"}；客户端只读取白名单字段
  function parseMetadata(meta) {
    var out = { role: 'visitor', code: '' };
    if (typeof meta !== 'string' || !meta || meta.length > 2048) return out;
    try {
      var o = JSON.parse(meta);
      if (o && typeof o === 'object') {
        if (o.role === 'agent' || o.role === 'visitor') out.role = o.role;
        if (typeof o.code === 'string' && /^\d{4}$/.test(o.code)) out.code = o.code;
      }
    } catch (e) {}
    return out;
  }
  // 头像文字：客服取昵称首字；访客统一用人形图标（返回空串）
  function avatarText(name, role) {
    if (role !== 'agent') return '';
    var n = str(name).replace(/^客服\s*[·・.]\s*/, '').trim();
    var first = Array.from(n)[0] || '';
    return /[\p{L}\p{N}]/u.test(first) ? first.toUpperCase() : '客';
  }
  // 排序：自己 → 客服 → 正在说话 → 已开麦 → 已开摄像头 → 进入时间
  function sortParticipants(list) {
    return list.slice().sort(function (a, b) {
      function score(p) { return (p.isLocal ? 16 : 0) + (p.role === 'agent' ? 8 : 0) + (p.speaking ? 4 : 0) + (p.micOn ? 2 : 0) + (p.camOn ? 1 : 0); }
      var d = score(b) - score(a);
      if (d) return d;
      return (a.joinedAt || 0) - (b.joinedAt || 0);
    });
  }
  // 视频订阅挑选：不可见时一律不订阅；可见时客服优先，其次最近发布，最多 max 路
  function pickVideoSubscriptions(candidates, max, visible) {
    var out = [];
    if (!visible || !Array.isArray(candidates) || !(max > 0)) return out;
    var sorted = candidates.slice().sort(function (a, b) {
      var ra = a.role === 'agent' ? 1 : 0, rb = b.role === 'agent' ? 1 : 0;
      if (ra !== rb) return rb - ra;
      return (b.publishedAt || 0) - (a.publishedAt || 0);
    });
    for (var i = 0; i < sorted.length && out.length < max; i++) if (sorted[i] && sorted[i].sid) out.push(sorted[i].sid);
    return out;
  }

  // ====== 断线原因分类（LiveKit DisconnectReason，枚举对象由调用方传入，缺失时用协议数值） ======
  var DR_FALLBACK = { CLIENT_INITIATED: 1, DUPLICATE_IDENTITY: 2, SERVER_SHUTDOWN: 3, PARTICIPANT_REMOVED: 4, ROOM_DELETED: 5, ROOM_CLOSED: 10 };
  function classifyDisconnect(reason, DR) {
    var d = DR && typeof DR === 'object' ? DR : DR_FALLBACK;
    function v(k) { return typeof d[k] === 'number' ? d[k] : DR_FALLBACK[k]; }
    if (reason === undefined || reason === null) return 'retry';
    if (reason === v('CLIENT_INITIATED')) return 'client';
    if (reason === v('DUPLICATE_IDENTITY')) return 'elsewhere';
    if (reason === v('PARTICIPANT_REMOVED')) return 'removed';
    if (reason === v('ROOM_DELETED') || reason === v('ROOM_CLOSED')) return 'closed';
    return 'retry';
  }

  // ====== 发送频率滑动窗口（服务端限 5 条 / 10 秒，前端先拦一道，减少无效请求） ======
  function SlidingWindow(limit, windowMs) {
    this.limit = limit > 0 ? limit : 5;
    this.windowMs = windowMs > 0 ? windowMs : 10000;
    this.hits = [];
  }
  SlidingWindow.prototype._prune = function (now) {
    while (this.hits.length && now - this.hits[0] >= this.windowMs) this.hits.shift();
  };
  SlidingWindow.prototype.tryTake = function (now) {
    this._prune(now);
    if (this.hits.length >= this.limit) return false;
    this.hits.push(now);
    return true;
  };
  SlidingWindow.prototype.retryInMs = function (now) {
    this._prune(now);
    if (this.hits.length < this.limit) return 0;
    return Math.max(0, this.windowMs - (now - this.hits[0]));
  };

  // ====== 跨窗口消息校验 ======
  var HOST_CMDS = { start_audio: true, show_hall: true };
  // 宿主页 → iframe：{__cs:1, type:'hall_cmd', cmd}
  function validateHostCommand(data) {
    if (!data || typeof data !== 'object' || data.__cs !== 1 || data.type !== 'hall_cmd') return null;
    if (typeof data.cmd !== 'string' || !HOST_CMDS[data.cmd]) return null;
    return { cmd: data.cmd };
  }
  // 推断宿主页 origin：优先 location.ancestorOrigins，其次 document.referrer；推断不出返回 ''
  function hostOriginFrom(ancestorOrigins, referrer) {
    try {
      if (ancestorOrigins && ancestorOrigins.length > 0) {
        var a = String(ancestorOrigins[0]);
        if (/^https?:\/\/[^/\s]+$/.test(a)) return a;
      }
    } catch (e) {}
    try {
      if (referrer) {
        var u = new URL(referrer);
        if (u.protocol === 'https:' || u.protocol === 'http:') return u.origin;
      }
    } catch (e) {}
    return '';
  }
  // 同浏览器多标签页接管：{type:'hall_takeover', tab, at}
  function validateTabMessage(data) {
    if (!data || typeof data !== 'object' || data.type !== 'hall_takeover') return null;
    if (typeof data.tab !== 'string' || !/^[A-Za-z0-9-]{8,64}$/.test(data.tab)) return null;
    var at = toInt(data.at, 0);
    if (at <= 0) return null;
    return { tab: data.tab, at: at };
  }
  // iframe → 宿主页的大厅状态（loader.js 内有同规则的独立实现，二者由测试保证一致）
  var STATUS_STATES = { off: true, connecting: true, live: true, blocked: true, muted: true, elsewhere: true, kicked: true, full: true, error: true };
  function buildHallStatus(s) {
    var st = s && STATUS_STATES[s.state] ? s.state : 'off';
    return { __cs: 1, type: 'hall_status', state: st, online: clamp(toInt(s && s.online, 0), 0, 100000), speaking: !!(s && s.speaking) };
  }

  // ====== 上传前置校验 ======
  function fileExt(name) {
    var m = /\.([A-Za-z0-9]{1,8})$/.exec(str(name));
    return m ? m[1].toLowerCase() : '';
  }
  function checkUploadFile(file, maxBytes) {
    if (!file) return '请选择文件';
    var ext = fileExt(file.name);
    if (!UPLOAD_EXTS[ext]) return errorText(41501);
    var max = maxBytes > 0 ? maxBytes : UPLOAD_MAX_BYTES;
    if (!(file.size > 0)) return '文件为空，无法发送';
    if (file.size > max) return '文件过大，单个文件不能超过 ' + Math.round(max / 1024 / 1024) + ' MB';
    return '';
  }
  function isImageFile(file) { return !!file && UPLOAD_EXTS[fileExt(file.name)] === 'image'; }
  function formatBytes(n) {
    n = toInt(n, 0);
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    return (n / 1024 / 1024).toFixed(1) + ' MB';
  }

  return {
    HISTORY_LIMIT: HISTORY_LIMIT,
    KEEP_LIMIT: KEEP_LIMIT,
    CONTENT_MAX: CONTENT_MAX,
    UPLOAD_ACCEPT: UPLOAD_ACCEPT,
    UPLOAD_MAX_BYTES: UPLOAD_MAX_BYTES,
    str: str,
    clampText: clampText,
    textLength: textLength,
    validId: validId,
    isUUIDv4: isUUIDv4,
    toMs: toMs,
    toBool: toBool,
    newUUIDv4: newUUIDv4,
    safeMediaPath: safeMediaPath,
    normalizeMessage: normalizeMessage,
    MessageStore: MessageStore,
    backoffDelay: backoffDelay,
    errorText: errorText,
    mediaErrorText: mediaErrorText,
    moderationText: moderationText,
    minutesFromSeconds: minutesFromSeconds,
    normalizeSettings: normalizeSettings,
    CHAT_PAUSED_TEXT: CHAT_PAUSED_TEXT,
    VOICE_PAUSED_TEXT: VOICE_PAUSED_TEXT,
    switchView: switchView,
    switchChanges: switchChanges,
    switchToastText: switchToastText,
    isChatClosedError: isChatClosedError,
    tabAfterReopen: tabAfterReopen,
    reopenJoinDelay: reopenJoinDelay,
    publicSettingsPatch: publicSettingsPatch,
    sanitizeIceServers: sanitizeIceServers,
    validLiveKitURL: validLiveKitURL,
    validateJoinData: validateJoinData,
    parseMetadata: parseMetadata,
    avatarText: avatarText,
    sortParticipants: sortParticipants,
    pickVideoSubscriptions: pickVideoSubscriptions,
    classifyDisconnect: classifyDisconnect,
    SlidingWindow: SlidingWindow,
    validateHostCommand: validateHostCommand,
    hostOriginFrom: hostOriginFrom,
    validateTabMessage: validateTabMessage,
    buildHallStatus: buildHallStatus,
    checkUploadFile: checkUploadFile,
    isImageFile: isImageFile,
    fileExt: fileExt,
    formatBytes: formatBytes
  };
});
