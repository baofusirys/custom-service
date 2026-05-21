# 集成指南 — 把客服 Widget 装到你自己的网站

## 一句话
在你网站的 `</body>` 前贴下面一行就行。

```html
<script src="https://<your-cs-domain>/widget/loader.js"
        data-cs-endpoint="wss://<your-cs-domain>"
        data-cs-site="default" defer></script>
```

加载完成后，页面右下角会自动出现「在线客服」按钮，访客点开即可与客服实时对话。

---

## 可配置项（标签属性，全部可选）

| 属性 | 默认值 | 说明 |
| --- | --- | --- |
| `data-cs-endpoint` | 当前页所在域 | 客服服务端的 WSS 地址，例如 `wss://cs.example.com`。HTTP 接口会自动从此地址推断（`https://cs.example.com`）。 |
| `data-cs-site` | `default` | 站点标识，用于在客服后台区分不同的接入来源。多个网站接入同一套客服时，给每个网站一个独立 site 标识，方便客服筛选会话。 |
| `data-cs-theme-color` | `#409EFF` | 浮动按钮颜色，CSS 颜色值 |
| `data-cs-button-text` | `在线客服` | 浮动按钮文字 |

## 高级用法

### 1. 多站点接入同一套客服
直接每个站点用不同 `data-cs-site`：
```html
<!-- 站点 A -->
<script src=".../loader.js" data-cs-site="store-cn" ...></script>
<!-- 站点 B -->
<script src=".../loader.js" data-cs-site="store-en" ...></script>
```
客服后台拉到的会话列表会标注来源 site_id，便于筛选。

### 2. 程序化关闭气泡
父页可以监听 widget 发出的 `message` 事件：
```js
window.addEventListener('message', (ev) => {
  if (ev.data?.__cs === 1 && ev.data.type === 'unread') {
    console.log('未读数变化', ev.data.count);
  }
});
```

### 3. 自定义触发器
如果你想用自己的按钮触发，而不是右下角默认气泡：
1. 隐藏默认气泡：在 CSS 里 `#__cs_widget_btn__{display:none!important;}`
2. 自己写按钮，点击时 `document.getElementById('__cs_widget_wrap__').style.display='block';`

## 安全 / 兼容性
- Widget 通过 iframe 隔离运行，不会影响宿主页的 CSS 或 JS 全局变量。
- 访客信息（IP、UA、Referer、当前页 URL）会被服务端记录用于客服侧画像。
- 浏览器兼容：Edge / Chrome / Safari / Firefox 现代版本（>= 2021）。
- HTTPS 站点要求 endpoint 也必须是 wss/https（混合内容浏览器会拦）。

## 卸载
直接把 `<script>` 那一行从你的 HTML 删掉即可，无残留数据（访客本地缓存 `cs_visitor_*` 用户也可自行清掉）。
