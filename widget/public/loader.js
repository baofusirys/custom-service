/*
 * Custom Service · widget loader · v0.1.2
 *
 * 用法（任何第三方网站只需复制以下一行）：
 *   <script src="https://<your-domain>/widget/loader.js"
 *           data-cs-endpoint="wss://<your-domain>"
 *           data-cs-site="default" defer></script>
 *
 * 设计：右下角一个圆形浮动按钮（带未读 badge），点击后展开 iframe 聊天窗口。
 * iframe 隔离运行，宿主页 CSS / JS 全无污染。
 */
(function () {
  if (window.__CS_WIDGET_LOADED__) return;
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
  iframe.allow = 'clipboard-read; clipboard-write';
  iframe.style.cssText = 'width:100%;height:100%;border:0;display:block;background:#fff';
  wrap.appendChild(iframe);

  function postState(opened) {
    try {
      iframe.contentWindow &&
        iframe.contentWindow.postMessage({ __cs: 1, type: 'widget_state', open: opened }, '*');
    } catch (e) {}
  }
  // 把宿主页 URL / title 推给 iframe（chat.html）—— 跨域时 iframe 内的 parent.location 拿不到，
  // 必须由父页主动 postMessage 过去。
  function postPageInfo() {
    try {
      iframe.contentWindow &&
        iframe.contentWindow.postMessage({
          __cs: 1, type: 'page_info',
          url: location.href, title: document.title || '', referrer: document.referrer || ''
        }, '*');
    } catch (e) {}
  }
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
    postState(true);
  }
  function close() {
    wrap.style.display = 'none';
    btn.style.display = 'flex';
    postState(false);
  }
  btn.addEventListener('click', open);

  window.addEventListener('message', function (ev) {
    if (!ev.data || ev.data.__cs !== 1) return;
    if (ev.data.type === 'close') close();
    if (ev.data.type === 'unread' && typeof ev.data.count === 'number') {
      if (ev.data.count > 0 && wrap.style.display === 'none') {
        badge.textContent = ev.data.count > 99 ? '99+' : ev.data.count;
        badge.style.display = 'flex';
      } else {
        badge.style.display = 'none';
      }
    }
  });

  function inject() {
    document.body.appendChild(btn);
    document.body.appendChild(wrap);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', inject);
  } else {
    inject();
  }
})();
