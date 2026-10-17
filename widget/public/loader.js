/*
 * Custom Service · widget loader · v0.2.0（[119] 增加多人大厅状态胶囊）
 *
 * 用法（任何第三方网站只需复制以下一行）：
 *   <script src="https://<your-domain>/widget/loader.js"
 *           data-cs-endpoint="wss://<your-domain>"
 *           data-cs-site="default" defer></script>
 *
 * 设计：右下角一个圆形浮动按钮（带未读 badge），点击后展开 iframe 聊天窗口。
 * iframe 隔离运行，宿主页 CSS / JS 全无污染。
 * [119] 按钮左侧显示多人大厅状态（在线人数、是否在收听）；与 iframe 的 postMessage 双向校验
 *       来源窗口、origin 与消息结构。
 */
(function () {
  // [054] guard 加强：__CS_WIDGET_LOADED__ 哨兵 + iframe 真实存在性双重检查
  // 旧逻辑只看哨兵，bfcache 恢复 / SPA 框架清 body 后哨兵仍 true 但 btn/iframe 已被销毁，
  // 导致 loader.js 不重新注入 + 老 chat.html 实例的 connectWS 还在跑 → 后台多实例并发风暴
  if (window.__CS_WIDGET_LOADED__) {
    var existingBtn = document.getElementById('__cs_widget_btn__');
    var existingWrap = document.getElementById('__cs_widget_wrap__');
    if (existingBtn && existingWrap && document.body.contains(existingBtn)) {
      return;  // 哨兵 + DOM 都在 → 真的不需要再注入
    }
    // 哨兵存在但 DOM 不在 = bfcache/SPA 异常状态，清哨兵让下面流程重新注入
    delete window.__CS_WIDGET_LOADED__;
  }
  window.__CS_WIDGET_LOADED__ = true;

  var script = document.currentScript || (function () {
    var s = document.getElementsByTagName('script');
    return s[s.length - 1];
  })();
  var endpoint = (script.getAttribute('data-cs-endpoint') || location.origin).replace(/\/+$/, '');
  var siteID = script.getAttribute('data-cs-site') || 'default';
  var origin = endpoint.replace(/^wss?:\/\//, function (m) { return m === 'wss://' ? 'https://' : 'http://'; });
  var brand = script.getAttribute('data-cs-theme-color') || '#2974ff';

  // 1) 浮动圆形按钮
  var btn = document.createElement('div');
  btn.id = '__cs_widget_btn__';
  btn.setAttribute('role', 'button');
  btn.setAttribute('aria-label', '在线客服');
  btn.style.cssText = [
    'position:fixed', 'right:24px', 'bottom:24px',
    'z-index:2147483646',
    'width:56px', 'height:56px', 'border-radius:50%',
    'background:linear-gradient(135deg,#4a90ff 0%,' + brand + ' 100%)',
    'color:#fff',
    'box-shadow:0 6px 20px rgba(41,116,255,.35), 0 2px 6px rgba(0,0,0,.08)',
    'cursor:pointer',
    'display:flex', 'align-items:center', 'justify-content:center',
    'transition:transform .18s ease, box-shadow .18s ease',
    'user-select:none',
    '-webkit-tap-highlight-color:transparent'
  ].join(';');
  btn.innerHTML = '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
    '<path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"/>' +
    '</svg>';

  // 未读 badge
  var badge = document.createElement('span');
  badge.style.cssText = [
    'position:absolute', 'top:-2px', 'right:-2px',
    'min-width:18px', 'height:18px', 'padding:0 5px',
    'background:#ef4444', 'color:#fff',
    'border:2px solid #fff', 'border-radius:9px',
    'font:600 11px/14px -apple-system,sans-serif',
    'display:none', 'align-items:center', 'justify-content:center',
    'box-sizing:content-box'
  ].join(';');
  btn.appendChild(badge);

  btn.onmouseenter = function () { btn.style.transform = 'translateY(-3px) scale(1.04)'; };
  btn.onmouseleave = function () { btn.style.transform = 'translateY(0) scale(1)'; };

  // 2) iframe 容器
  var wrap = document.createElement('div');
  wrap.id = '__cs_widget_wrap__';
  wrap.style.cssText = [
    'position:fixed', 'right:24px', 'bottom:24px',
    'z-index:2147483647',
    'width:380px', 'height:560px', 'max-height:80vh', 'max-width:calc(100vw - 32px)',
    'border-radius:16px', 'overflow:hidden',
    'box-shadow:0 20px 50px rgba(15,23,42,.22), 0 8px 16px rgba(15,23,42,.08)',
    'display:none', 'background:#fff',
    'transform-origin: bottom right',
    'transition:opacity .18s ease, transform .18s ease'
  ].join(';');

  var iframe = document.createElement('iframe');
  iframe.src = origin + '/widget/chat.html?endpoint=' + encodeURIComponent(endpoint) +
    '&site=' + encodeURIComponent(siteID);
  iframe.title = '在线客服';
  // [048] 跨域 iframe 必须显式 allow microphone/camera/autoplay，否则 chat.html 调
  // getUserMedia({audio:true}) 会被 Permission Policy 拒绝（访客点电话按钮没反应）。
  // - microphone：WebRTC 语音通话必需
  // - camera：未来加视频通话用，现在加上无害
  // - autoplay：来电铃声 voice-ring.mp3 循环播放（[036]）+ 通话音频远端流自动播
  // - clipboard-read/write：复制聊天消息内容（[028]）
  // 不指定 source list 即 default 'self'，对跨域 iframe 即 iframe src 的 origin（widget 域）
  iframe.allow = 'microphone; camera; autoplay; clipboard-read; clipboard-write';
  iframe.style.cssText = 'width:100%;height:100%;border:0;display:block;background:#fff';
  wrap.appendChild(iframe);

  // [119] iframe 的真实 origin（只与它通信，不再用 '*' 广播宿主页 URL 等信息）
  var frameOrigin = (function () {
    try { return new URL(iframe.src, location.href).origin; } catch (e) { return origin; }
  })();
  function postToFrame(msg) {
    try { iframe.contentWindow && iframe.contentWindow.postMessage(msg, frameOrigin); } catch (e) {}
  }

  function postState(opened) {
    postToFrame({ __cs: 1, type: 'widget_state', open: opened });
  }
  // 把宿主页 URL / title 推给 iframe（chat.html）—— 跨域时 iframe 内的 parent.location 拿不到，
  // 必须由父页主动 postMessage 过去。
  function postPageInfo() {
    postToFrame({
      __cs: 1, type: 'page_info',
      url: location.href, title: document.title || '', referrer: document.referrer || ''
    });
  }

  // ====== [119] 大厅状态消息校验 开始（widget/tests/hallLoader.test.mjs 抽取本段单测） ======
  var HALL_STATES = { off: 1, connecting: 1, live: 1, blocked: 1, muted: 1, elsewhere: 1, kicked: 1, full: 1, error: 1 };
  // 只接受来自本挂件 iframe 窗口、且 origin 完全一致的消息
  function isTrustedFrameMessage(ev, frameWin, expectedOrigin) {
    return !!ev && !!frameWin && ev.source === frameWin &&
      typeof ev.origin === 'string' && ev.origin === expectedOrigin &&
      !!ev.data && typeof ev.data === 'object' && ev.data.__cs === 1 && typeof ev.data.type === 'string';
  }
  // {__cs:1, type:'hall_status', state, online, speaking} → 规范化结果；不合法返回 null
  function parseHallStatus(d) {
    if (!d || typeof d !== 'object' || d.__cs !== 1 || d.type !== 'hall_status') return null;
    if (typeof d.state !== 'string' || !Object.prototype.hasOwnProperty.call(HALL_STATES, d.state)) return null;
    var online = typeof d.online === 'number' && isFinite(d.online) ? Math.max(0, Math.min(100000, Math.floor(d.online))) : 0;
    return { state: d.state, online: online, speaking: d.speaking === true };
  }
  // 大图地址只允许挂件同源的 /files/ 文件
  function safeLightboxSrc(src, expectedOrigin) {
    if (typeof src !== 'string' || src.length > 2048) return '';
    try {
      var u = new URL(src);
      if (u.origin !== expectedOrigin || (u.protocol !== 'https:' && u.protocol !== 'http:')) return '';
      if (u.pathname.indexOf('/files/') < 0) return '';
      return u.href;
    } catch (e) { return ''; }
  }
  // 胶囊文案：full 用于宽屏，compact 用于窄屏
  function hallChipText(st) {
    if (!st) return null;
    switch (st.state) {
      case 'connecting': return { full: '大厅连接中', compact: '大厅' };
      case 'live': return { full: '大厅 ' + st.online + ' 人 · ' + (st.speaking ? '正在播放' : '收听中'), compact: st.online + ' 人' };
      case 'blocked': return { full: '点击收听大厅', compact: '点击收听' };
      case 'muted': return { full: '大厅 ' + st.online + ' 人 · 已静音', compact: st.online + ' 人' };
      case 'elsewhere': return { full: '已在其他页面收听', compact: '其他页面' };
      case 'kicked': return { full: '已被移出大厅', compact: '已移出' };
      case 'full': return { full: '大厅已满', compact: '已满' };
      case 'error': return { full: '大厅重连中', compact: '重连中' };
    }
    return null;   // off：不显示
  }
  // ====== [119] 大厅状态消息校验 结束 ======

  // ====== [119] 大厅状态胶囊（悬浮按钮左侧） ======
  var HALL_ICON_OPEN = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">';
  var HALL_ICONS = {
    users: HALL_ICON_OPEN + '<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>',
    volume: HALL_ICON_OPEN + '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14"/></svg>',
    volumeOff: HALL_ICON_OPEN + '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="23" y1="9" x2="17" y2="15"/><line x1="17" y1="9" x2="23" y2="15"/></svg>'
  };
  var hallState = null;
  var hallChip = document.createElement('div');
  hallChip.id = '__cs_widget_hall__';
  hallChip.setAttribute('role', 'button');
  hallChip.setAttribute('tabindex', '0');
  hallChip.style.cssText = [
    'position:fixed', 'right:92px', 'bottom:36px', 'z-index:2147483646',
    'height:32px', 'padding:0 12px 0 10px', 'border-radius:16px', 'box-sizing:border-box',
    'background:#fff', 'color:#374151', 'border:1px solid #e5e7eb',
    'box-shadow:0 4px 14px rgba(15,23,42,.14), 0 1px 3px rgba(0,0,0,.06)',
    'display:none', 'align-items:center', 'gap:6px',
    'font:500 12px/1 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif',
    'white-space:nowrap', 'max-width:calc(100vw - 120px)', 'overflow:hidden',
    'cursor:pointer', 'user-select:none', '-webkit-tap-highlight-color:transparent',
    'transition:background .15s ease, color .15s ease'
  ].join(';');
  var hallChipIcon = document.createElement('span');
  hallChipIcon.style.cssText = 'display:inline-flex;flex-shrink:0';
  var hallChipLabel = document.createElement('span');
  hallChipLabel.style.cssText = 'overflow:hidden;text-overflow:ellipsis';
  hallChip.appendChild(hallChipIcon);
  hallChip.appendChild(hallChipLabel);
  var narrowMQ = window.matchMedia ? window.matchMedia('(max-width: 420px)') : null;

  function renderHallChip() {
    var texts = hallChipText(hallState);
    if (!texts || wrap.style.display === 'block') { hallChip.style.display = 'none'; return; }
    var st = hallState.state;
    var blocked = st === 'blocked';
    hallChip.style.background = blocked ? 'linear-gradient(135deg,#4a90ff 0%,' + brand + ' 100%)' : '#fff';
    hallChip.style.color = blocked ? '#fff' : (st === 'live' && hallState.speaking ? '#059669' : (st === 'live' ? '#374151' : '#6b7280'));
    hallChip.style.borderColor = blocked ? 'transparent' : '#e5e7eb';
    hallChipIcon.innerHTML = st === 'muted' ? HALL_ICONS.volumeOff : ((st === 'live' || blocked) ? HALL_ICONS.volume : HALL_ICONS.users);
    hallChipLabel.textContent = narrowMQ && narrowMQ.matches ? texts.compact : texts.full;
    hallChip.setAttribute('aria-label', '多人大厅：' + texts.full);
    hallChip.title = blocked ? '浏览器限制了自动播放，点击开始收听大厅' : ('多人大厅：' + texts.full + '，点击进入');
    hallChip.style.display = 'flex';
  }
  if (narrowMQ) {
    if (narrowMQ.addEventListener) narrowMQ.addEventListener('change', renderHallChip);
    else if (narrowMQ.addListener) narrowMQ.addListener(renderHallChip);
  }
  function onHallChipActivate() {
    // 宿主页的点击会通过 allow="autoplay" 委托给 iframe：先请求恢复播放，再打开挂件进入大厅页
    postToFrame({ __cs: 1, type: 'hall_cmd', cmd: 'start_audio' });
    postToFrame({ __cs: 1, type: 'hall_cmd', cmd: 'show_hall' });
    open();
  }
  hallChip.addEventListener('click', onHallChipActivate);
  hallChip.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onHallChipActivate(); }
  });
  iframe.addEventListener('load', postPageInfo);
  function open() {
    wrap.style.display = 'block';
    wrap.style.opacity = '0';
    wrap.style.transform = 'scale(.95)';
    requestAnimationFrame(function () {
      wrap.style.opacity = '1';
      wrap.style.transform = 'scale(1)';
    });
    btn.style.display = 'none';
    badge.style.display = 'none';
    hallChip.style.display = 'none';
    postState(true);
  }
  function close() {
    wrap.style.display = 'none';
    btn.style.display = 'flex';
    renderHallChip();
    postState(false);
  }
  btn.addEventListener('click', open);

  window.addEventListener('message', function (ev) {
    // [119] 只处理本挂件 iframe 发来的消息（来源窗口 + origin + 结构三重校验）
    if (!isTrustedFrameMessage(ev, iframe.contentWindow, frameOrigin)) return;
    if (ev.data.type === 'hall_status') {
      var st = parseHallStatus(ev.data);
      if (st) { hallState = st; renderHallChip(); }
      return;
    }
    if (ev.data.type === 'close') close();
    if (ev.data.type === 'unread' && typeof ev.data.count === 'number') {
      if (ev.data.count > 0 && wrap.style.display === 'none') {
        badge.textContent = ev.data.count > 99 ? '99+' : ev.data.count;
        badge.style.display = 'flex';
      } else {
        badge.style.display = 'none';
      }
    }
    // [038] iframe 内点聊天图片 → 在宿主页（整个浏览器视窗）全屏显示
    // 之前在 iframe 内创建 lightbox 只能在聊天窗口 380x560 区域里显示，看不清
    if (ev.data.type === 'lightbox' && typeof ev.data.src === 'string') {
      var lbSrc = safeLightboxSrc(ev.data.src, frameOrigin);
      if (lbSrc) openHostLightbox(lbSrc);
    }
  });

  // ============== [038] 宿主页全屏图片查看器 ==============
  // iframe 大小固定 380x560，里面 position:fixed 也只能在 iframe viewport 里全屏；
  // 这里在 parent document 直接创建覆盖整个浏览器视窗的 overlay，看图才看得清
  function openHostLightbox(src) {
    var existing = document.getElementById('__cs_host_lightbox__');
    if (existing && existing.parentNode) existing.parentNode.removeChild(existing);
    var overlay = document.createElement('div');
    overlay.id = '__cs_host_lightbox__';
    overlay.style.cssText = [
      'position:fixed', 'inset:0',
      'background:rgba(0,0,0,.92)',
      // 比 iframe wrap 的 z-index 还要高 +10，确保在最上层
      'z-index:2147483647',
      'display:flex', 'align-items:center', 'justify-content:center',
      'cursor:zoom-out', 'user-select:none',
      '-webkit-tap-highlight-color:transparent'
    ].join(';');
    var imgEl = document.createElement('img');
    imgEl.src = src;
    imgEl.alt = '';
    imgEl.style.cssText = [
      'max-width:92vw', 'max-height:92vh',
      'object-fit:contain',
      'box-shadow:0 8px 40px rgba(0,0,0,.4)',
      'cursor:default'
    ].join(';');
    // 防止点图片本身关闭（用户可能想长按保存）
    imgEl.onclick = function (e) { e.stopPropagation(); };
    var closeBtn = document.createElement('span');
    closeBtn.textContent = '×';
    closeBtn.style.cssText = [
      'position:absolute', 'top:24px', 'right:24px',
      'width:44px', 'height:44px', 'border-radius:50%',
      'background:rgba(255,255,255,.12)', 'color:#fff',
      'display:flex', 'align-items:center', 'justify-content:center',
      'font:24px/1 -apple-system,sans-serif',
      'cursor:pointer'
    ].join(';');
    overlay.appendChild(imgEl);
    overlay.appendChild(closeBtn);
    function closeLb() {
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
      document.removeEventListener('keydown', onKey);
    }
    function onKey(e) { if (e.key === 'Escape') closeLb(); }
    overlay.onclick = closeLb;
    closeBtn.onclick = closeLb;
    document.addEventListener('keydown', onKey);
    document.body.appendChild(overlay);
  }

  function inject() {
    // [050] self-heal：每次 inject 前检查 DOM 上是否已经有同 id 元素（防 SPA 路由切换 / bfcache 恢复后重复挂）
    // 任一缺失就把对应元素加回去；都在就 noop（幂等）
    if (!document.getElementById('__cs_widget_btn__')) {
      document.body.appendChild(btn);
    }
    if (!document.getElementById('__cs_widget_wrap__')) {
      document.body.appendChild(wrap);
    }
    if (!document.getElementById('__cs_widget_hall__')) {
      document.body.appendChild(hallChip);
    }
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', inject);
  } else {
    inject();
  }

  // ============ [050] self-heal：3 道防线确保 SPA/bfcache/外部清 DOM 时图标都能回来 ============
  // 病因：访客在 SPA 网站 page A → page B → 后退回 page A，btn 不见了
  //   - SPA 框架（Vue/React Router）切路由时把 body.innerHTML 清空 → btn/wrap DOM 被清
  //   - 但 IIFE 已经跑过，闭包里的 btn/wrap 变量仍指向原 DOM 节点（已 orphan）
  //   - 直接 appendChild orphan 节点 ✓ 浏览器会重新挂上 DOM
  //
  // 1) pageshow 事件：bfcache 恢复时触发（ev.persisted=true），同时也覆盖正常前进/后退
  window.addEventListener('pageshow', inject);
  // 2) popstate 事件：SPA history.pushState / popState 路由变化时触发
  window.addEventListener('popstate', function () { setTimeout(inject, 0); });
  // 3) MutationObserver：兜底监听 body 子节点变化（SPA 不通过 history，比如 React hash router 切路由后；
  //    或宿主页第三方脚本误删 btn）。debounce 100ms 防抖避免高频触发。
  var healTimer = null;
  var observer = new MutationObserver(function () {
    if (healTimer) return;
    healTimer = setTimeout(function () { healTimer = null; inject(); }, 100);
  });
  // childList 监听直接子元素增删；不监听 subtree 节省性能（只要 btn/wrap 是 body 直接子）
  if (document.body) {
    observer.observe(document.body, { childList: true });
  } else {
    document.addEventListener('DOMContentLoaded', function () {
      observer.observe(document.body, { childList: true });
    });
  }
})();
