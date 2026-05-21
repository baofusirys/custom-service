/*
 * Custom Service · widget loader · v0.1.0
 *
 * 用法（任何第三方网站只需复制以下一行）：
 *   <script src="https://<your-domain>/widget/loader.js"
 *           data-cs-endpoint="wss://<your-domain>"
 *           data-cs-site="default" defer></script>
 *
 * 作用：在右下角注入一个浮动按钮 + 隐藏的 iframe；点击按钮展开 iframe，
 * 真正的聊天 UI 跑在 iframe 里（CSS / DOM 跟宿主页彻底隔离）。
 */
(function () {
  if (window.__CS_WIDGET_LOADED__) return;
  window.__CS_WIDGET_LOADED__ = true;

  // 取自身 script 标签上的 data-* 配置
  var script = document.currentScript || (function () {
    var s = document.getElementsByTagName('script');
    return s[s.length - 1];
  })();
  var endpoint = (script.getAttribute('data-cs-endpoint') || location.origin).replace(/\/+$/, '');
  var siteID = script.getAttribute('data-cs-site') || 'default';
  var origin = endpoint.replace(/^wss?:\/\//, function (m) { return m === 'wss://' ? 'https://' : 'http://'; });

  // 颜色 / 文案（允许第三方覆盖）
  var theme = script.getAttribute('data-cs-theme-color') || '#409EFF';
  var btnText = script.getAttribute('data-cs-button-text') || '在线客服';

  // 1) 浮动按钮
  var btn = document.createElement('div');
  btn.id = '__cs_widget_btn__';
  btn.style.cssText = [
    'position:fixed', 'right:24px', 'bottom:24px', 'z-index:2147483646',
    'background:' + theme, 'color:#fff', 'padding:12px 18px',
    'border-radius:24px', 'box-shadow:0 4px 14px rgba(0,0,0,.18)',
    'cursor:pointer', 'font:14px/1 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif',
    'user-select:none', 'transition:transform .15s'
  ].join(';');
  btn.textContent = btnText;
  btn.onmouseenter = function () { btn.style.transform = 'translateY(-2px)'; };
  btn.onmouseleave = function () { btn.style.transform = 'translateY(0)'; };

  // 2) iframe 容器（默认隐藏，点击按钮展开）
  var wrap = document.createElement('div');
  wrap.id = '__cs_widget_wrap__';
  wrap.style.cssText = [
    'position:fixed', 'right:24px', 'bottom:84px', 'z-index:2147483647',
    'width:360px', 'height:520px', 'max-height:80vh',
    'border-radius:12px', 'overflow:hidden',
    'box-shadow:0 12px 36px rgba(0,0,0,.18)',
    'display:none', 'background:#fff',
    'transform-origin: bottom right'
  ].join(';');

  var iframe = document.createElement('iframe');
  iframe.src = origin + '/widget/chat.html?endpoint=' + encodeURIComponent(endpoint)
    + '&site=' + encodeURIComponent(siteID);
  iframe.title = '在线客服';
  iframe.allow = 'clipboard-read; clipboard-write';
  iframe.style.cssText = 'width:100%;height:100%;border:0;display:block;background:#fff';
  wrap.appendChild(iframe);

  function open() { wrap.style.display = 'block'; btn.style.display = 'none'; }
  function close() { wrap.style.display = 'none'; btn.style.display = 'block'; }
  btn.addEventListener('click', open);

  // iframe 通过 postMessage 通知父页（关闭、未读角标等）
  window.addEventListener('message', function (ev) {
    if (!ev.data || ev.data.__cs !== 1) return;
    if (ev.data.type === 'close') close();
    if (ev.data.type === 'unread' && typeof ev.data.count === 'number') {
      if (ev.data.count > 0 && wrap.style.display === 'none') {
        btn.style.background = '#F56C6C';
        btn.textContent = btnText + ' · ' + (ev.data.count > 99 ? '99+' : ev.data.count);
      } else {
        btn.style.background = theme;
        btn.textContent = btnText;
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
