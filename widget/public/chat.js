(function () {
  // ====== 配置 ======
  var qs = new URLSearchParams(location.search);
  var endpoint = qs.get('endpoint') || location.origin;
  var siteID = qs.get('site') || 'default';
  var httpBase = endpoint.replace(/^wss?:\/\//, function (m) {
    return m === 'wss://' ? 'https://' : 'http://';
  });

  var STORE_KEY = 'cs_visitor_' + siteID;

  function newMessageID() {
    if (!window.crypto || !window.crypto.getRandomValues) {
      throw new Error('当前浏览器不支持安全随机数，无法安全发送消息');
    }
    var bytes = new Uint8Array(16);
    window.crypto.getRandomValues(bytes);
    var hex = '';
    for (var i = 0; i < bytes.length; i++) hex += bytes[i].toString(16).padStart(2, '0');
    return 'm2-' + hex;
  }

  // [055-F] vid 双轨读写：localStorage 主 + cookie 兜底
  // 防 localStorage 被用户清掉（部分隐私插件 / 浏览器设置 / 用户手动清）后 vid 丢失
  // cookie 同域有效期 365 天，SameSite=Lax 兼容跨域 iframe；HttpOnly 不设（JS 要读）
  function readCookie(name) {
    var m = document.cookie.match(new RegExp('(?:^|;\\s*)' + name + '=([^;]+)'));
    return m ? decodeURIComponent(m[1]) : '';
  }
  function writeCookie(name, val) {
    var expires = new Date(Date.now() + 365 * 24 * 3600 * 1000).toUTCString();
    document.cookie = name + '=' + encodeURIComponent(val) +
      '; expires=' + expires + '; path=/; SameSite=Lax' +
      (location.protocol === 'https:' ? '; Secure' : '');
  }
  function readVisitorID() {
    var v = '';
    try { var s = JSON.parse(localStorage.getItem(STORE_KEY) || 'null'); if (s && s.visitor_id) v = s.visitor_id; } catch {}
    if (!v) v = readCookie(STORE_KEY);  // localStorage 没了 → 走 cookie 兜底
    return v;
  }
  function writeVisitorID(vid) {
    if (!vid) return;
    try { localStorage.setItem(STORE_KEY, JSON.stringify({ visitor_id: vid })); } catch {}
    writeCookie(STORE_KEY, vid);  // 双写：任一被清掉另一个还在
  }

  var visitorID = readVisitorID();
  var convID = '';
  var token = '';
  var ws = null;
  var alive = false;
  var retry = 0;
  var unread = 0;
  var seenMessageIDs = Object.create(null);
  var lastAgentMessageID = '';
  var pingTimer = null;
  // [054] 重连控制：防 connectWS 多实例并发打爆 backend rl:wsh 限流
  var reconnectTimer = null;   // 保存待执行 setTimeout id，防多个 onclose 各自排队
  var isConnecting = false;    // 防重入锁，CONNECTING 期间外部再次调 connectWS 跳过
  var lastGroup = null; // 最近一组消息的元数据，用于合并
  var notifySound = 'visitor1'; // 通知声音，启动时拉服务端覆盖；旧 key (classic/chime/...) 在 playNotify 内 fallback 到 visitor1
  // widget 是否处于打开状态。loader.js 通过 postMessage('widget_state') 通知。
  // 默认 false（页面刚加载时 iframe 在但 widget 收起）；打开时清零未读。
  var isWidgetOpen = false;
  // 宿主页 URL / title，loader.js 通过 postMessage('page_info') 推过来。
  // WSS 上线后会发一条 page 消息让客服后台看到访客访问了哪个页面。
  var hostURL = '';
  var hostTitle = '';
  var pageReported = ''; // 已经上报过的 URL，避免重复发

  // ====== DOM ======
  var listEl = document.getElementById('list');
  var inputEl = document.getElementById('input');
  var sendBtn = document.getElementById('sendBtn');
  var fileBtn = document.getElementById('fileBtn');
  var fileInput = document.getElementById('fileInput');
  var statusEl = document.getElementById('status');
  var dotEl = document.getElementById('dot');
  var closeBtn = document.getElementById('closeBtn');

  closeBtn.onclick = function () { parent.postMessage({ __cs: 1, type: 'close' }, '*'); };
  fileBtn.onclick = function () { fileInput.click(); };
  fileInput.onchange = onPickFile;
  // 语音通话按钮 / 挂断按钮 / 远端 audio 绑定 —— 放到 voice 模块定义之后，
  // 见 wireVoiceButtons()，调用时机在 voice 模块声明结束后
  sendBtn.onclick = sendText;
  inputEl.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendText(); }
  });
  inputEl.addEventListener('input', function () {
    inputEl.style.height = 'auto';
    inputEl.style.height = Math.min(100, inputEl.scrollHeight) + 'px';
  });

  function setStatus(text, ok) {
    statusEl.textContent = text;
    dotEl.classList.toggle('off', !ok);
  }

  // ====== 语音通话（WebRTC + WSS 信令） ======
  // 完整流程：
  //   visitor 点电话按钮 → getUserMedia → ws.send(voice_call) →
  //   等 voice_accept (agent 接听，来源是 e.from) → 创建 RTCPeerConnection +
  //   addTrack + createOffer + setLocalDescription → ws.send(voice_offer, to=agent) →
  //   收 voice_answer → setRemoteDescription → 双方互换 voice_ice → 通话建立
  //   任何一方 voice_end → 关闭 PC + 关麦克风 + 撤窗
  var voice = {
    state: 'idle',  // idle / ringing / accepting / talking / ended
    callId: null,
    pc: null,
    localStream: null,
    remoteAudio: null,
    remoteAgent: null,  // "agent:xxx" 接听方
    startTs: 0,
    timer: null,        // 拨号超时 / 通话计时
  };
  // ICE_SERVERS：默认值仅作兜底（fetch turn-credential 失败时用）
  // 每次 voiceStart 都会异步刷新（fetchTurnCredential），有 TURN/STUN 都注入
  var ICE_SERVERS = [{ urls: 'stun:stun.l.google.com:19302' }];

  // 拉后端发的 24h 短期 TURN 凭证 → 更新 ICE_SERVERS。
  // 注意：失败不抛错，保持原 ICE_SERVERS，让通话至少能尝试 P2P 直连（旧行为）。
  async function fetchTurnCredential() {
    try {
      var url = httpBase + '/api/visitor/turn-credential?vid=' + encodeURIComponent(visitorID || '');
      var r = await fetch(url);
      var data = await r.json();
      if (data.code === 0 && data.data && Array.isArray(data.data.urls) && data.data.urls.length) {
        var srv = { urls: data.data.urls };
        if (data.data.username) srv.username = data.data.username;
        if (data.data.credential) srv.credential = data.data.credential;
        ICE_SERVERS = [srv];
      }
    } catch (e) { /* 静默：保持兜底 STUN */ }
  }

  function voicePanel() { return document.getElementById('voicePanel'); }
  function voiceSetStatus(text, klass) {
    var p = voicePanel();
    if (!p) return;
    p.style.display = 'flex';
    p.querySelector('.voice-status').textContent = text;
    p.classList.remove('voice-panel--talking', 'voice-panel--ended');
    if (klass) p.classList.add(klass);
  }
  function voiceHidePanel() {
    var p = voicePanel(); if (!p) return;
    p.style.display = 'none';
    p.classList.remove('voice-panel--talking', 'voice-panel--ended');
  }

  async function voiceStart() {
    if (voice.state !== 'idle') return;
    if (!alive) { renderSys('未连接，无法呼叫'); return; }
    voice.state = 'ringing';
    voice.callId = 'call-' + Date.now() + '-' + Math.random().toString(36).slice(2, 8);
    // 并行：拿麦克风同时刷 TURN 凭证（凭证刷新 ~50ms 不阻塞用户感知）
    await fetchTurnCredential();
    try {
      voice.localStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (e) {
      voice.state = 'idle';
      renderSys('无法访问麦克风：' + (e.message || e));
      return;
    }
    voiceSetStatus('拨号中…等待客服接听');
    // [036] 拨号等待铃声循环；voiceOnAccept/voiceEnd 任意一个进来都会停
    playRingLoop();
    ws.send(JSON.stringify({
      type: 'voice_call', ts: Date.now(),
      extra: { call_id: voice.callId }
    }));
    // 30 秒无人接听自动挂断
    voice.timer = setTimeout(function () {
      if (voice.state === 'ringing') voiceEnd('无人接听');
    }, 30000);
  }

  function createVoicePC() {
    var pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
    pc.onicecandidate = function (ev) {
      if (ev.candidate && voice.remoteAgent) {
        ws.send(JSON.stringify({
          type: 'voice_ice', to: voice.remoteAgent, ts: Date.now(),
          extra: {
            call_id: voice.callId,
            candidate: ev.candidate.candidate,
            sdpMid: ev.candidate.sdpMid,
            sdpMLineIndex: ev.candidate.sdpMLineIndex
          }
        }));
      }
    };
    pc.ontrack = function (ev) {
      if (voice.remoteAudio && ev.streams[0]) {
        voice.remoteAudio.srcObject = ev.streams[0];
      }
    };
    pc.onconnectionstatechange = function () {
      if (pc.connectionState === 'failed' || pc.connectionState === 'disconnected') {
        voiceEnd('连接中断');
      }
    };
    return pc;
  }

  async function voiceOnAccept(env) {
    if (voice.state !== 'ringing') return;
    var cid = env.extra && env.extra.call_id;
    if (cid !== voice.callId) return;
    if (voice.timer) { clearTimeout(voice.timer); voice.timer = null; }
    stopRingLoop();  // [036] 接通了立刻停等待铃声
    voice.remoteAgent = env.from;  // "agent:xxx"
    voice.state = 'accepting';
    voiceSetStatus('正在建立通话…');
    voice.pc = createVoicePC();
    voice.localStream.getTracks().forEach(function (t) {
      voice.pc.addTrack(t, voice.localStream);
    });
    var offer = await voice.pc.createOffer();
    await voice.pc.setLocalDescription(offer);
    ws.send(JSON.stringify({
      type: 'voice_offer', to: voice.remoteAgent, ts: Date.now(),
      extra: { call_id: voice.callId, sdp: offer.sdp }
    }));
  }

  async function voiceOnAnswer(env) {
    if (voice.state !== 'accepting' || !voice.pc) return;
    try {
      await voice.pc.setRemoteDescription({ type: 'answer', sdp: env.extra.sdp });
    } catch (e) {
      voiceEnd('协商失败');
      return;
    }
    voice.state = 'talking';
    voice.startTs = Date.now();
    voiceSetStatus('通话中 00:00', 'voice-panel--talking');
    voice.timer = setInterval(function () {
      if (voice.state !== 'talking') return;
      var sec = Math.floor((Date.now() - voice.startTs) / 1000);
      var mm = String(Math.floor(sec / 60)).padStart(2, '0');
      var ss = String(sec % 60).padStart(2, '0');
      voiceSetStatus('通话中 ' + mm + ':' + ss, 'voice-panel--talking');
    }, 1000);
  }

  async function voiceOnIce(env) {
    if (!voice.pc || !env.extra) return;
    try {
      await voice.pc.addIceCandidate({
        candidate: env.extra.candidate,
        sdpMid: env.extra.sdpMid,
        sdpMLineIndex: env.extra.sdpMLineIndex
      });
    } catch (e) {}
  }

  function voiceOnReject(env) {
    if (voice.state !== 'ringing') return;
    voiceEnd('客服拒绝了通话');
  }

  function voiceOnEnd(env) {
    if (voice.state === 'idle') return;
    voiceEnd('对方已挂断');
  }

  function voiceEnd(reason) {
    if (voice.state === 'idle') return;
    stopRingLoop();  // [036] 任何挂断路径都停铃声（拒绝/超时/连接失败/正常挂断）
    // 根据 state 决定 code + duration：
    //   ringing/accepting 期间挂断 → cancel (访客主动取消)
    //   talking 状态挂断 → hangup + duration
    //   reason 关键词映射：'无人接听'→no_answer, '连接中断/协商失败'→failed
    var code = 'hangup';
    var duration = 0;
    if (voice.state === 'ringing' || voice.state === 'accepting') {
      code = 'cancel';
    } else if (voice.state === 'talking' && voice.startTs) {
      code = 'hangup';
      duration = Math.floor((Date.now() - voice.startTs) / 1000);
    }
    if (reason === '无人接听') code = 'no_answer';
    else if (reason === '连接中断' || reason === '协商失败') code = 'failed';
    else if (reason === '客服拒绝了通话') code = 'rejected';
    var extra = { call_id: voice.callId, code: code, duration: duration };
    if (voice.remoteAgent) {
      ws.send(JSON.stringify({
        type: 'voice_end', to: voice.remoteAgent, ts: Date.now(), extra: extra
      }));
    } else {
      ws.send(JSON.stringify({
        type: 'voice_end', ts: Date.now(), extra: extra
      }));
    }
    if (voice.pc) { try { voice.pc.close(); } catch {} voice.pc = null; }
    if (voice.localStream) {
      voice.localStream.getTracks().forEach(function (t) { t.stop(); });
      voice.localStream = null;
    }
    if (voice.timer) { clearTimeout(voice.timer); clearInterval(voice.timer); voice.timer = null; }
    if (voice.remoteAudio) voice.remoteAudio.srcObject = null;
    voice.state = 'ended';
    voice.callId = null;
    voice.remoteAgent = null;
    voiceSetStatus(reason || '通话结束', 'voice-panel--ended');
    setTimeout(function () {
      if (voice.state === 'ended') { voice.state = 'idle'; voiceHidePanel(); }
    }, 2500);
  }

  // 暴露给 ws.onmessage 调用
  window.handleVoiceSignal = function (env) {
    switch (env.type) {
      case 'voice_accept': voiceOnAccept(env); break;
      case 'voice_reject': voiceOnReject(env); break;
      case 'voice_answer': voiceOnAnswer(env); break;
      case 'voice_ice':    voiceOnIce(env); break;
      case 'voice_end':    voiceOnEnd(env); break;
    }
  };

  // 按钮绑定 + DOM 引用（必须在 var voice = {...} 之后，否则 hoist 后 voice 是 undefined）
  (function wireVoiceButtons() {
    var voiceBtn = document.getElementById('voiceBtn');
    var voiceHangBtn = document.getElementById('voiceHangBtn');
    voice.remoteAudio = document.getElementById('voiceRemoteAudio');
    if (voiceBtn) voiceBtn.onclick = function () { voiceStart(); };
    if (voiceHangBtn) voiceHangBtn.onclick = function () { voiceEnd('您挂断了'); };
  })();

  // ====== 通知声音（真实录制 WAV 文件） ======
  // 访客端只听 visitor 系列 3 种音色，加 none。文件在 widget/public/sounds/，
  // 浏览器访问路径 ./sounds/visitorN.wav（相对当前 chat.html 所在 /widget/ 路径）
  var SOUND_FILES = {
    visitor1: 'sounds/visitor1.wav',
    visitor2: 'sounds/visitor2.wav',
    visitor3: 'sounds/visitor3.wav'
  };
  // 预加载所有音频元素：第一次试听零延迟
  var soundCache = {};
  Object.keys(SOUND_FILES).forEach(function (k) {
    var a = new Audio(SOUND_FILES[k]);
    a.preload = 'auto';
    a.volume = 1.0;
    soundCache[k] = a;
  });
  var lastPlay = 0;
  function playNotify() {
    if (!notifySound || notifySound === 'none') return;
    var now = Date.now();
    if (now - lastPlay < 500) return; // 500ms 防抖
    lastPlay = now;
    var a = soundCache[notifySound];
    // 老用户数据库里可能存的是 classic/chime 等旧 key → fallback 到 visitor1
    if (!a) a = soundCache.visitor1;
    if (!a) return;
    try {
      a.currentTime = 0;
      a.volume = 1.0;
      var p = a.play();
      if (p && p.catch) p.catch(function () {});
    } catch (e) {}
  }
  // ====== 语音拨号等待铃声（循环）[036] ======
  // 访客拨号时（state=ringing）播放，循环；voiceOnAccept / voiceEnd 都停
  var _ringAudio = new Audio('sounds/voice-ring.mp3');
  _ringAudio.loop = true;
  _ringAudio.preload = 'auto';
  _ringAudio.volume = 1.0;
  function playRingLoop() {
    try {
      _ringAudio.currentTime = 0;
      var p = _ringAudio.play();
      if (p && p.catch) p.catch(function () {});
    } catch (e) {}
  }
  function stopRingLoop() {
    try { _ringAudio.pause(); _ringAudio.currentTime = 0; } catch (e) {}
  }

  // 浏览器要求音频必须用户手势触发；用户在聊天框第一次点击时解锁所有 Audio
  // [057] 不再解锁 voice-ring：volume=0 试播在某些浏览器/版本 pause 之前会真的发声
  //   1-2 秒（voice-ring.mp3 比短促 visitor1.wav 长很多 + loop=true）。
  //   而访客 widget 中访客是"主动呼叫方"（voiceStart 时点电话按钮 → 那个 click 自然
  //   解锁 audio → 立即可播 playRingLoop），完全不需要预解锁。删掉 [036] 当年的预解锁。
  document.addEventListener('click', function () {
    Object.keys(soundCache).forEach(function (k) {
      var a = soundCache[k];
      try {
        a.volume = 0;
        var p = a.play();
        if (p && p.then) p.then(function () { a.pause(); a.currentTime = 0; a.volume = 1.0; })
                          .catch(function () { a.volume = 1.0; });
        else a.volume = 1.0;
      } catch (e) { a.volume = 1.0; }
    });
    // [036] 解锁 voice-ring 段已删除（[057]）：访客 voiceStart 自己点电话按钮的 click
    // 已经是 user gesture，那个 click 上下文里直接 playRingLoop 完全没问题，不需预解锁。
  }, { once: true, capture: true });

  // ====== 时间格式化 ======
  function pad(n) { return n < 10 ? '0' + n : '' + n; }
  function fmtAbs(ts) {
    var d = new Date(ts);
    return d.getFullYear() + '-' + pad(d.getMonth()+1) + '-' + pad(d.getDate()) +
      ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }
  function fmtGroupTime(ts) {
    var d = new Date(ts), now = new Date();
    var sameDay = d.getFullYear() === now.getFullYear() &&
                  d.getMonth() === now.getMonth() &&
                  d.getDate() === now.getDate();
    if (sameDay) return '今天 ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    var yest = new Date(now.getFullYear(), now.getMonth(), now.getDate()-1);
    if (d.getFullYear() === yest.getFullYear() &&
        d.getMonth() === yest.getMonth() &&
        d.getDate() === yest.getDate()) {
      return '昨天 ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    }
    return pad(d.getMonth()+1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  function fmtMsgTime(ts) {   // [081] 今天 HH:mm / 今年 MM-dd HH:mm / 往年 yyyy-MM-dd HH:mm
    var d = new Date(ts), now = new Date();
    var hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
    if (d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()) return hm;
    var md = pad(d.getMonth()+1) + '-' + pad(d.getDate()) + ' ' + hm;
    return d.getFullYear() === now.getFullYear() ? md : (d.getFullYear() + '-' + md);
  }
  function escape(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // ====== 渲染 ======
  // ====== 已读角标管理 ======
  // 设计：访客视角下，只在「自己最后一条消息」的最后一组 stack 内显示『已读』。
  // 新发消息时清掉旧角标；收到客服 read 事件时挂上新角标。
  function setReadIndicator(visible) {
    var olds = listEl.querySelectorAll('.read-indicator');
    for (var i = 0; i < olds.length; i++) olds[i].remove();
    if (!visible) return;
    // 找到最后一组「自己发的」stack：lastGroup 必须 isMine=true（访客发的）
    if (lastGroup && lastGroup.isMine && lastGroup.bubbles) {
      var ind = document.createElement('div');
      ind.className = 'read-indicator';
      ind.textContent = '已读';
      lastGroup.bubbles.appendChild(ind);
    }
  }
  function sendReadAck(messageID) {
    if (alive && convID) {
      var targetID = messageID || lastAgentMessageID;
      try { ws.send(JSON.stringify({ type: 'read', conv: convID, id: targetID || undefined, ts: Date.now() })); }
      catch {}
    }
  }

  function sendDeliveryAck(messageID) {
    if (!alive || !convID || !messageID) return;
    try { ws.send(JSON.stringify({ type: 'delivery', conv: convID, id: messageID, ts: Date.now() })); }
    catch {}
  }

  // Fallback：同域时 chat.html 自己读 parent.location，不依赖 loader.js postMessage。
  // 这样即使集成方还在用 loader.js 的旧缓存（没 postPageInfo），同域场景也能正常上报。
  // 跨域时 parent.location 访问 throws SecurityError，由 loader.js postMessage 兜底。
  function tryReadHostPageDirectly() {
    try {
      if (parent && parent !== window) {
        var u = parent.location && parent.location.href;
        var t = parent.document && parent.document.title;
        if (u && !hostURL) {
          hostURL = u;
          hostTitle = t || '';
          return true;
        }
      }
    } catch (e) { /* 跨域，忽略 */ }
    return false;
  }

  // 上报当前页面给服务端（让客服后台看到访客的浏览轨迹）。
  // 触发时机：(1) WSS 上线 onopen (2) 收到 loader.js 推送的 page_info (3) 启动时 fallback 自读
  // 上报需要同时满足：WSS alive + convID 已建立 + 拿到 hostURL + URL 跟上次不同
  function reportPageView() {
    if (!alive || !convID) return;
    if (!hostURL) tryReadHostPageDirectly(); // 兜底自读
    if (!hostURL) return;
    if (pageReported === hostURL) return;
    pageReported = hostURL;
    try {
      ws.send(JSON.stringify({
        type: 'page',
        conv: convID,
        ts: Date.now(),
        extra: { url: hostURL, title: hostTitle }
      }));
    } catch {}
  }

  function renderTimeDivider(ts) {
    var d = document.createElement('div');
    d.className = 'time-divider';
    d.innerHTML = '<span>' + escape(fmtGroupTime(ts)) + '</span>';
    listEl.appendChild(d);
  }

  function renderSys(text) {
    var d = document.createElement('div');
    d.className = 'sys-chip';
    d.textContent = text;
    listEl.appendChild(d);
    listEl.scrollTop = listEl.scrollHeight;
    lastGroup = null;
  }

  function buildBubble(m, isMine) {
    var b = document.createElement('div');
    b.className = 'bubble';
    b.title = fmtAbs(m.created_at || m.ts || Date.now());

    var mediaURL = safeMediaPath(m.media_url?.String || m.media_url || m.media || '');
    var mediaKind = m.media_kind?.String || m.media_kind || m.mkind || '';
    var mediaName = m.media_name?.String || m.media_name || m.mname || '';

    if (mediaURL && mediaKind === 'image') {
      b.classList.add('bubble-image');
      var img = document.createElement('img');
      img.className = 'img-msg';
      img.src = httpBase + mediaURL;
      img.onclick = function () { openImageLightbox(httpBase + mediaURL); };
      b.appendChild(img);
    } else if (mediaURL) {
      var a = document.createElement('a');
      a.className = 'file-card';
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      a.download = '';
      a.href = httpBase + mediaURL;
      a.innerHTML =
        '<span class="ico">' +
          '<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor">' +
            '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zm-1 7V3.5L18.5 9H13z"/>' +
          '</svg>' +
        '</span>' +
        '<span class="meta">' +
          '<span class="name">' + escape(mediaName || '附件') + '</span>' +
          '<span class="sub">点击下载</span>' +
        '</span>';
      b.appendChild(a);
    }
    if (m.content) {
      var t = document.createElement('span');
      t.textContent = m.content;
      t.style.display = 'block';
      b.appendChild(t);
    }
    // 复制按钮：文本 / 文件 / 图片消息都加（hover 才显示）
    if (m.content || mediaURL) {
      var cp = document.createElement('span');
      cp.className = 'bubble-copy';
      cp.title = '复制';
      cp.innerHTML =
        '<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
          '<rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>' +
          '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>' +
        '</svg>';
      cp.onclick = function (e) {
        e.stopPropagation();
        var text = m.content || (mediaURL ? httpBase + mediaURL : '');
        if (!text) return;
        copyToClipboard(text, cp);
      };
      b.appendChild(cp);
    }
    // [080] 每条消息可见时间
    var tm = document.createElement('span');
    tm.textContent = fmtMsgTime(m.created_at || m.ts || Date.now());
    tm.style.cssText = 'display:block;font-size:11px;opacity:0.55;margin-top:3px;text-align:right';
    b.appendChild(tm);
    return b;
  }

  function safeMediaPath(raw) {
    try {
      var u = new URL(raw, window.location.origin);
      if (u.origin !== window.location.origin || u.pathname.indexOf('/files/') !== 0) return '';
      return u.pathname + u.search;
    } catch (e) { return ''; }
  }

  // 图片 lightbox：点击聊天消息里的图片，全屏黑底大图查看。点 overlay/ESC/× 关闭。
  // 不依赖任何外部库，~30 行手写。
  function openImageLightbox(src) {
    // [038] 优先让宿主页 loader.js 在整个浏览器视窗里全屏显示（iframe 太小看不清）。
    // standalone 模式（直接访问 chat.html demo / 没 parent loader.js）才走 iframe 内 fallback
    if (window.parent && window.parent !== window) {
      try {
        window.parent.postMessage({ __cs: 1, type: 'lightbox', src: src }, '*');
        return;
      } catch (e) { /* parent 不可达 → fallback */ }
    }
    var existing = document.getElementById('imgLightbox');
    if (existing) existing.parentNode.removeChild(existing);
    var overlay = document.createElement('div');
    overlay.id = 'imgLightbox';
    overlay.className = 'img-lightbox';
    var imgEl = document.createElement('img');
    imgEl.src = src;
    imgEl.alt = '';
    var closeBtn = document.createElement('span');
    closeBtn.className = 'img-lightbox-close';
    closeBtn.textContent = '×';
    overlay.appendChild(imgEl);
    overlay.appendChild(closeBtn);
    document.body.appendChild(overlay);
    function close() {
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
      document.removeEventListener('keydown', onKey);
    }
    function onKey(e) { if (e.key === 'Escape') close(); }
    overlay.onclick = close;
    // 防止点图片本身关闭（用户可能想长按保存）
    imgEl.onclick = function (e) { e.stopPropagation(); };
    closeBtn.onclick = close;
    document.addEventListener('keydown', onKey);
  }

  // 复制到剪贴板：优先 Clipboard API，老浏览器 fallback execCommand
  function copyToClipboard(text, anchorEl) {
    var done = function () {
      // 简易反馈：临时改背景色 + 显示"已复制"
      if (!anchorEl) return;
      anchorEl.title = '已复制';
      anchorEl.classList.add('bubble-copy--done');
      setTimeout(function () {
        anchorEl.title = '复制';
        anchorEl.classList.remove('bubble-copy--done');
      }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(function () {});
      return;
    }
    var ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.left = '-9999px';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); done(); } catch (e) {}
    document.body.removeChild(ta);
  }

  function render(m) {
    var isMine = m.sender === 'visitor';
    var ts = new Date(m.created_at || m.ts || Date.now()).getTime();

    // 与上一组同发送者且 < 5 分钟，则合并到同一 line（隐藏头像）
    if (lastGroup && lastGroup.isMine === isMine && (ts - lastGroup.ts) < 5 * 60 * 1000) {
      var bubble = buildBubble(m, isMine);
      lastGroup.bubbles.appendChild(bubble);
      lastGroup.ts = ts;
    } else {
      // 新组：先时间分隔条
      if (!lastGroup || (ts - lastGroup.ts) > 5 * 60 * 1000) {
        renderTimeDivider(ts);
      }
      var line = document.createElement('div');
      line.className = 'msg-line ' + (isMine ? 'mine' : 'theirs');

      var avatar = document.createElement('div');
      avatar.className = 'msg-avatar ' + (isMine ? 'visitor' : 'agent');
      avatar.textContent = isMine ? '我' : '客';
      line.appendChild(avatar);

      var stack = document.createElement('div');
      stack.className = 'msg-stack';
      line.appendChild(stack);

      var bubble = buildBubble(m, isMine);
      stack.appendChild(bubble);

      listEl.appendChild(line);
      lastGroup = { isMine: isMine, ts: ts, bubbles: stack };
    }
    listEl.scrollTop = listEl.scrollHeight;
  }

  // ====== 启动 ======
  // [058] settings 缓存：sessionStorage 5 分钟有效，避免 bootstrap 失败重试时反复打 /api/visitor/settings
  var SETTINGS_CACHE_KEY = STORE_KEY + '_settings';
  var SETTINGS_CACHE_AT_KEY = STORE_KEY + '_settings_at';
  var SETTINGS_CACHE_TTL = 5 * 60 * 1000;

  function applySettings(data) {
    if (!data) return;
    notifySound = data.notify_sound || 'classic';
    if (data.widget_title) {
      var titleEl = document.querySelector('header .header-title');
      if (titleEl) titleEl.textContent = data.widget_title;
    }
    if (data.voice_call_hint) {
      var voiceHintEl = document.getElementById('voiceBtnHint');
      if (voiceHintEl) voiceHintEl.textContent = data.voice_call_hint;
      var voiceBtnEl = document.getElementById('voiceBtn');
      if (voiceBtnEl) voiceBtnEl.title = data.voice_call_hint;
    }
  }

  async function loadPublicSettings() {
    // [058] 优先 sessionStorage 缓存（5min 内不再 fetch，省 /api/visitor/settings 请求）
    try {
      var cached = sessionStorage.getItem(SETTINGS_CACHE_KEY);
      var cachedAt = sessionStorage.getItem(SETTINGS_CACHE_AT_KEY);
      if (cached && cachedAt && (Date.now() - parseInt(cachedAt, 10) < SETTINGS_CACHE_TTL)) {
        applySettings(JSON.parse(cached));
        return;
      }
    } catch (e) {}
    try {
      var r = await fetch(httpBase + '/api/visitor/settings');
      var data = await r.json();
      if (data.code === 0 && data.data) {
        applySettings(data.data);
        try {
          sessionStorage.setItem(SETTINGS_CACHE_KEY, JSON.stringify(data.data));
          sessionStorage.setItem(SETTINGS_CACHE_AT_KEY, String(Date.now()));
        } catch (e) {}
      }
    } catch {}
  }

  // [058] bootstrap 重写：跟 [054] connectWS 同模式防多实例并发死循环打爆 backend 限流
  // 旧版 bug：无重入锁 + 固定 setTimeout(bootstrap, 5000) + 不识别 42901/42902 → 多 chat.html 实例
  //   各自每 5s 死循环 → 24h 累积 viol 触发自动拉黑 24h 误封正常访客/管理员
  var bootstrapTimer = null;
  var bootstrapRetry = 0;
  var isBootstrapping = false;

  function scheduleBootstrap(backoff) {
    if (bootstrapTimer) return;  // 已排队跳过防多次叠加
    bootstrapTimer = setTimeout(function () {
      bootstrapTimer = null;
      bootstrap();
    }, backoff);
  }

  async function bootstrap() {
    if (isBootstrapping) return;       // [058] 重入锁
    if (bootstrapTimer) { clearTimeout(bootstrapTimer); bootstrapTimer = null; }
    isBootstrapping = true;
    try {
      tryReadHostPageDirectly();
      await loadPublicSettings();
      var resp = await fetch(httpBase + '/api/visitor/session', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          visitor_id: visitorID,
          site_id: siteID,
          referer: document.referrer || '',
          // [046] 跨域 iframe 中读 parent.location.href 抛 SecurityError 会导致整个
          // bootstrap async 链 reject → widget 死循环"连接失败"。用 IIFE try/catch 兜底。
          last_page: (function () { try { return parent.location.href; } catch (e) { return ''; } })(),
          ua: navigator.userAgent
        })
      });
      // [058] 优先看 HTTP 状态码 + Retry-After header 决定退避（backend 会带）
      var retryAfter = parseInt(resp.headers.get('Retry-After') || '0', 10);
      var data;
      try { data = await resp.json(); } catch { data = { code: -1, msg: 'parse error' }; }
      // [058] 限流码识别：42901 IP 已拉黑 / 42902 RPM 超限 → 长 backoff 覆盖 Redis 窗口
      if (resp.status === 429 || data.code === 42901 || data.code === 42902) {
        var backoff = (retryAfter > 0 ? retryAfter * 1000 : 60000);
        setStatus('服务繁忙，' + Math.round(backoff / 1000) + 's 后重试', false);
        scheduleBootstrap(backoff);
        return;
      }
      if (data.code !== 0) throw new Error(data.msg || '会话创建失败');
      visitorID = data.visitor_id;
      convID = data.conversation;
      token = data.visitor_token;
      writeVisitorID(visitorID);
      loadHistoryFromCache();
      bootstrapRetry = 0;  // [058] 成功重置
      connectWS();
    } catch (e) {
      // [058] 普通失败用指数退避（5s→8s→12.8s→20.5s→cap 30s）
      var bf = Math.min(30000, 5000 * Math.pow(1.6, bootstrapRetry++));
      setStatus('连接失败，重试中', false);
      scheduleBootstrap(bf);
    } finally {
      isBootstrapping = false;
    }
  }

  // [058] pagehide 清 bootstrapTimer 防 bfcache 中旧实例继续 schedule
  window.addEventListener('pagehide', function () {
    if (bootstrapTimer) { clearTimeout(bootstrapTimer); bootstrapTimer = null; }
    isBootstrapping = false;
  });

  function loadHistoryFromCache() {
    try {
      var cache = JSON.parse(localStorage.getItem(STORE_KEY + '_msgs') || '[]');
      cache.slice(-50).forEach(function (message) {
        if (message.id) seenMessageIDs[message.id] = true;
        if (message.sender === 'agent' && message.id) lastAgentMessageID = message.id;
        render(message);
      });
    } catch {}
  }

  function persistMsg(m) {
    try {
      var cache = JSON.parse(localStorage.getItem(STORE_KEY + '_msgs') || '[]');
      var replaced = false;
      if (m.id) {
        for (var i = 0; i < cache.length; i++) {
          if (cache[i] && cache[i].id === m.id) {
            cache[i] = Object.assign({}, cache[i], m);
            replaced = true;
            break;
          }
        }
      }
      if (!replaced) cache.push(m);
      if (cache.length > 200) cache = cache.slice(-200);
      localStorage.setItem(STORE_KEY + '_msgs', JSON.stringify(cache));
    } catch {}
  }

  // [054] scheduleReconnect 是唯一的重连入口：防多个 onclose 各自 setTimeout 叠加，
  // 也防快速失败时 retry++ 把 backoff 算得过大或并发雪崩。
  // 默认指数退避 1.6^n，上限 30s；retry >= 3 时强制 60s 覆盖 backend Redis rl:wsh 60s 窗口。
  function scheduleReconnect() {
    if (reconnectTimer) {
      // 已经在排队中，本次跳过（防多 onclose 叠加打爆限流）
      return;
    }
    var backoff;
    if (retry >= 3) {
      // 连续 3 次失败大概率是被 backend 限流了（rl:wsh 60s 窗口）
      // 直接退避 60s 覆盖整个 Redis 窗口，避免继续打爆 + 触发 viol 拉黑
      backoff = 60000;
    } else {
      backoff = Math.min(30000, 1000 * Math.pow(1.6, retry));
    }
    retry++;
    reconnectTimer = setTimeout(function () {
      reconnectTimer = null;
      connectWS();
    }, backoff);
  }

  function connectWS() {
    // [054] 防重入：CONNECTING/OPEN 状态直接跳过，避免外部（pageshow/visibilitychange/
    // loader.js self-heal 等）在 ws 还没就绪时再次调用 connectWS 导致并发风暴
    if (isConnecting) return;
    if (ws) {
      if (ws.readyState === WebSocket.OPEN) return;
      if (ws.readyState === WebSocket.CONNECTING) return;
      // CLOSING / CLOSED 状态安全，可以创建新 ws
    }
    // 清掉残留的 reconnectTimer，避免重复触发
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    // 清掉残留的 pingTimer 防泄漏
    if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }

    isConnecting = true;
    var url = endpoint.replace(/^https?:\/\//, function (m) {
      return m === 'https://' ? 'wss://' : 'ws://';
    }) + '/ws/visitor';
    try {
      // JWT 通过 WebSocket 子协议头传递，不进入 URL、浏览器历史或代理访问日志。
      ws = new WebSocket(url, ['cs-auth', token]);
    } catch (e) {
      isConnecting = false;
      scheduleReconnect();
      return;
    }
    ws.onopen = function () {
      isConnecting = false;
      alive = true; retry = 0;
      setStatus('在线', true);
      if (pingTimer) clearInterval(pingTimer);  // 防重复 setInterval 泄漏
      pingTimer = setInterval(function () {
        if (alive) try { ws.send(JSON.stringify({ type: 'ping', ts: Date.now() })); } catch {}
      }, 30000);
      // WSS 上线后立即上报当前页面（如果 page_info 已经就绪）
      reportPageView();
    };
    ws.onmessage = function (ev) {
      var env;
      try { env = JSON.parse(ev.data); } catch { return; }
      if (env.type === 'pong') return;
      if (env.type === 'hello') { renderSys('您已接入客服，请描述您的问题'); return; }
      if (env.type === 'error') { renderSys('系统：' + (env.content || '')); return; }
      if (env.type === 'read') {
        // 客服读了访客消息：在访客最后一组消息上挂「已读」角标
        if (env.from && env.from.indexOf('agent:') === 0) {
          setReadIndicator(true);
        }
        return;
      }
      // 语音通话信令分发（仅当存在 voice 模块时，避免 undefined）
      if (env.type && env.type.indexOf('voice_') === 0) {
        if (window.handleVoiceSignal) window.handleVoiceSignal(env);
        return;
      }
      if (env.type === 'chat') {
        if (env.from && env.from.indexOf('visitor:') === 0) return;
        var isAgentMessage = !!(env.from && env.from.indexOf('agent:') === 0);
        var m = {
          id: env.id || '',
          conv_id: env.conv || convID,
          sender: isAgentMessage ? 'agent' : 'sys',
          content: env.content || '',
          media_url: env.media || '',
          media_kind: env.mkind || '',
          media_name: env.mname || '',
          created_at: new Date(env.ts || Date.now()).toISOString()
        };
        var duplicate = !!(m.id && seenMessageIDs[m.id]);
        if (!duplicate) {
          if (m.id) seenMessageIDs[m.id] = true;
          render(m); persistMsg(m);
          playNotify(); // 重放按 ID 去重，不重复气泡、不重复提示音
        }
        if (isAgentMessage && m.id) lastAgentMessageID = m.id;
        // DOM 已渲染（或确认本地已有同 ID）后，才向后端确认送达。
        if (isAgentMessage) sendDeliveryAck(m.id);
        // 重连补发的同 ID 消息只补 delivery，不重复累计未读；避免断线一次 badge 暴涨。
        // 新消息在 widget 收起时累计未读，loader.js 通过 widget_state 通知。
        if (!duplicate && !isWidgetOpen) {
          unread++;
          parent.postMessage({ __cs: 1, type: 'unread', count: unread }, '*');
        } else if (isWidgetOpen) {
          // widget 当前是打开的，访客看到了客服消息 → 立即发 read 通知客服
          if (isAgentMessage) sendReadAck(m.id);
        }
      }
    };
    ws.onclose = function () {
      isConnecting = false;
      alive = false;
      if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }
      setStatus('已断开，重连中', false);
      scheduleReconnect();  // [054] 走统一去重入口，不再裸 setTimeout
    };
    // [054] onerror 不主动 close —— 浏览器握手失败时会同时 fire onerror + onclose，
    // 让 onclose 自然触发即可；之前主动 close 是双触发，未来场景会踩坑
    ws.onerror = function (ev) {
      // 仅记日志方便排查（console.warn 不抛错）；不动 ws.close，交给 onclose
      try { console.warn('[cs-widget] ws error', ev); } catch (e) {}
    };
  }

  // [054] Patch 3：bfcache + 页面生命周期管理
  // pagehide：用户跳走 / 后退到别页前主动 close ws，防 bfcache 中老 ws 还活着
  window.addEventListener('pagehide', function () {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }
    if (ws) {
      try { ws.close(1000, 'pagehide'); } catch (e) {}
    }
  });
  // pageshow：bfcache 恢复时（ev.persisted=true）老 ws 已断，重置 retry 立即重连
  window.addEventListener('pageshow', function (ev) {
    if (ev.persisted) {
      retry = 0;
      connectWS();
    }
  });

  // [040] 待发送文件队列（来自粘贴 / 附件按钮多选 / 多次粘贴），点发送时才真正依次上传
  // 支持多文件：每个 chip 独立 × 移除；粘贴/选附件追加到队列而不是覆盖
  var pendingFiles = [];  // [{ file, chipEl, blobUrl }]
  var pendingListEl = document.getElementById('pendingList');

  function fmtBytes(n) {
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    return (n / 1024 / 1024).toFixed(1) + ' MB';
  }

  function addPendingFile(file) {
    if (!file) return;
    var chip = document.createElement('div');
    chip.className = 'pending-chip';
    var item = { file: file, chipEl: chip, blobUrl: null };
    var isImage = (file.type || '').indexOf('image/') === 0;
    if (isImage) {
      var img = document.createElement('img');
      img.className = 'thumb';
      item.blobUrl = URL.createObjectURL(file);
      img.src = item.blobUrl;
      chip.appendChild(img);
    } else {
      var ic = document.createElement('span');
      ic.className = 'file-icon';
      ic.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>';
      chip.appendChild(ic);
    }
    var meta = document.createElement('span');
    meta.className = 'meta';
    var name = document.createElement('div');
    name.className = 'name';
    name.textContent = file.name || (isImage ? '图片' : '文件');
    var size = document.createElement('div');
    size.className = 'size';
    size.textContent = fmtBytes(file.size || 0);
    meta.appendChild(name);
    meta.appendChild(size);
    chip.appendChild(meta);
    var rm = document.createElement('button');
    rm.className = 'remove-btn';
    rm.title = '移除';
    rm.textContent = '×';
    rm.onclick = function () { removePendingFile(item); };
    chip.appendChild(rm);
    pendingFiles.push(item);
    pendingListEl.appendChild(chip);
    pendingListEl.classList.add('is-show');
  }

  function removePendingFile(item) {
    var idx = pendingFiles.indexOf(item);
    if (idx < 0) return;
    pendingFiles.splice(idx, 1);
    if (item.blobUrl) {
      try { URL.revokeObjectURL(item.blobUrl); } catch (e) {}
    }
    if (item.chipEl && item.chipEl.parentNode) {
      item.chipEl.parentNode.removeChild(item.chipEl);
    }
    if (pendingFiles.length === 0) pendingListEl.classList.remove('is-show');
  }

  function clearAllPending() {
    // 拷贝再遍历，避免 removePendingFile 改数组导致跳元素
    pendingFiles.slice().forEach(removePendingFile);
  }

  // [040] 发送：先发文本（如果有），再依次上传所有 pending 文件（每文件一条独立消息）
  async function sendText() {
    var text = inputEl.value.trim();
    var files = pendingFiles.map(function (it) { return it.file; });
    if (!text && files.length === 0) return;
    if (!alive) { setStatus('未连接', false); return; }
    if (text) {
      var messageID = newMessageID();
      ws.send(JSON.stringify({ type: 'chat', conv: convID, id: messageID, content: text, ts: Date.now(), prio: 0 }));
      var m = { id: messageID, conv_id: convID, sender: 'visitor', content: text, created_at: new Date().toISOString() };
      seenMessageIDs[messageID] = true;
      render(m); persistMsg(m);
      setReadIndicator(false);
      inputEl.value = '';
      inputEl.style.height = 'auto';
    }
    if (files.length > 0) {
      // 先清 UI（避免重复点击）；上传失败的会由 uploadAndSendFile 内部 renderSys
      clearAllPending();
      for (var i = 0; i < files.length; i++) {
        await uploadAndSendFile(files[i]);
      }
    }
  }

  // 公共上传函数，文件选择和粘贴两个入口共用
  async function uploadAndSendFile(file) {
    if (!file) return;
    if (!alive) { renderSys('未连接，无法上传'); return; }
    var fd = new FormData();
    fd.append('file', file);
    fd.append('uploader', 'visitor');
    try {
      var r = await fetch(httpBase + '/api/upload', {
        method: 'POST',
        headers: { 'Authorization': 'Bearer ' + token },
        body: fd
      });
      var data = await r.json();
      if (data.code !== 0) throw new Error(data.msg);
      var messageID = newMessageID();
      ws.send(JSON.stringify({
        type: 'chat', conv: convID, id: messageID, content: '',
        media: data.url, mkind: data.kind, mname: data.name, msize: data.size,
        ts: Date.now(), prio: 0
      }));
      var m = {
        id: messageID, conv_id: convID,
        sender: 'visitor', content: '',
        media_url: data.url, media_kind: data.kind, media_name: data.name,
        created_at: new Date().toISOString()
      };
      seenMessageIDs[messageID] = true;
      render(m); persistMsg(m);
      setReadIndicator(false);
    } catch (err) {
      renderSys('文件发送失败：' + (err.message || ''));
    }
  }

  // [040] 选附件：multiple 支持，全部追加到 pending 队列
  function onPickFile(e) {
    var files = e.target.files;
    if (files && files.length) {
      for (var i = 0; i < files.length; i++) addPendingFile(files[i]);
    }
    e.target.value = '';
  }

  // [040] 粘贴：剪贴板里所有 file 都追加到 pending 队列（支持一次粘多张图）；
  // 纯文本粘贴不拦截，仍走默认 textarea 行为
  inputEl.addEventListener('paste', function (e) {
    var items = e.clipboardData && e.clipboardData.items;
    if (!items) return;
    var hadFile = false;
    for (var i = 0; i < items.length; i++) {
      if (items[i].kind === 'file') {
        var f = items[i].getAsFile();
        if (f) { addPendingFile(f); hadFile = true; }
      }
    }
    // 只要剪贴板含文件就阻止默认（防止把 file 当作文本粘进 textarea）
    if (hadFile) e.preventDefault();
  });

  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') {
      unread = 0;
      parent.postMessage({ __cs: 1, type: 'unread', count: 0 }, '*');
    }
  });

  // 跳到最新消息（最底部）。打开 widget 时 / loadHistory 完成时调。
  // 用双 rAF 等浏览器完成 display:none -> display:block 的 layout reflow，
  // 否则首次打开时 scrollHeight 可能还是 0。
  function scrollToBottom() {
    requestAnimationFrame(function () {
      requestAnimationFrame(function () {
        if (listEl) listEl.scrollTop = listEl.scrollHeight;
      });
    });
  }

  // 接收 loader.js 通知的 widget 开关状态 / 宿主页面信息
  window.addEventListener('message', function (ev) {
    if (!ev.data || ev.data.__cs !== 1) return;
    if (ev.data.type === 'widget_state') {
      isWidgetOpen = !!ev.data.open;
      if (isWidgetOpen) {
        unread = 0;
        parent.postMessage({ __cs: 1, type: 'unread', count: 0 }, '*');
        // widget 刚打开：访客看到了所有客服消息 → 发 read 通知客服
        sendReadAck();
        // 自动跳到最新消息（不让访客看到上次的滚动位置，省去手动下拉）
        scrollToBottom();
      }
    }
    if (ev.data.type === 'page_info') {
      hostURL = ev.data.url || '';
      hostTitle = ev.data.title || '';
      reportPageView();
    }
  });

  bootstrap();
})();
