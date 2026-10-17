// 在 node:vm 中运行真实的 widget/public/chat.js，提供最小化的假 DOM / 存储 / WebSocket / 时钟。
// 只实现 chat.js 实际用到的 API，用于验证清空、撤回、正在输入等行为（不依赖 jsdom 等第三方库）。
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import { webcrypto } from 'node:crypto'

const SCRIPT = readFileSync(new URL('../../public/chat.js', import.meta.url), 'utf8')
// [119] 大厅模块：与 chat.html 中的书写顺序一致，先 hall-core.js、hall.js，最后 chat.js
const HALL_CORE = readFileSync(new URL('../../public/hall-core.js', import.meta.url), 'utf8')
const HALL_UI = readFileSync(new URL('../../public/hall.js', import.meta.url), 'utf8')

class FakeClassList {
  constructor(el) { this.el = el }
  _list() { return String(this.el.className || '').split(/\s+/).filter(Boolean) }
  add(...names) { const s = new Set(this._list()); names.forEach(n => s.add(n)); this.el.className = [...s].join(' ') }
  remove(...names) { this.el.className = this._list().filter(n => !names.includes(n)).join(' ') }
  contains(name) { return this._list().includes(name) }
  toggle(name, force) {
    const want = force === undefined ? !this.contains(name) : !!force
    if (want) this.add(name); else this.remove(name)
    return want
  }
}

export class FakeElement {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase()
    this.childNodes = []
    this.parentNode = null
    this.className = ''
    this.id = ''
    this.attributes = Object.create(null)
    this.style = { cssText: '', display: '' }
    this.listeners = Object.create(null)
    this.classList = new FakeClassList(this)
    this.scrollTop = 0
    this.scrollHeight = 0
    this.value = ''
    this._text = ''
    this._html = ''
  }
  get children() { return this.childNodes }
  get firstChild() { return this.childNodes[0] || null }
  get nextSibling() {
    if (!this.parentNode) return null
    const siblings = this.parentNode.childNodes
    return siblings[siblings.indexOf(this) + 1] || null
  }
  _detach(node) { if (node.parentNode) node.parentNode.removeChild(node) }
  appendChild(node) { this._detach(node); node.parentNode = this; this.childNodes.push(node); return node }
  insertBefore(node, ref) {
    if (!ref) return this.appendChild(node)
    this._detach(node)
    const idx = this.childNodes.indexOf(ref)
    if (idx < 0) throw new Error('insertBefore: ref 不是子节点')
    node.parentNode = this
    this.childNodes.splice(idx, 0, node)
    return node
  }
  removeChild(node) {
    const idx = this.childNodes.indexOf(node)
    if (idx < 0) throw new Error('removeChild: 不是子节点')
    this.childNodes.splice(idx, 1)
    node.parentNode = null
    return node
  }
  replaceChild(next, old) {
    const idx = this.childNodes.indexOf(old)
    if (idx < 0) throw new Error('replaceChild: 不是子节点')
    this._detach(next)
    const at = this.childNodes.indexOf(old)
    this.childNodes[at] = next
    next.parentNode = this
    old.parentNode = null
    return old
  }
  remove() { if (this.parentNode) this.parentNode.removeChild(this) }
  setAttribute(name, value) { this.attributes[name] = String(value) }
  getAttribute(name) { return name in this.attributes ? this.attributes[name] : null }
  set textContent(v) { this.childNodes.forEach(c => { c.parentNode = null }); this.childNodes = []; this._text = String(v); this._html = '' }
  get textContent() { return this._text + this._html.replace(/<[^>]*>/g, '') + this.childNodes.map(c => c.textContent).join('') }
  set innerHTML(v) { this.childNodes.forEach(c => { c.parentNode = null }); this.childNodes = []; this._html = String(v); this._text = '' }
  get innerHTML() { return this._html }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn) }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter(f => f !== fn) }
  dispatch(type, event = {}) {
    for (const fn of this.listeners[type] || []) fn(event)
    if (typeof this['on' + type] === 'function') this['on' + type](event)
  }
  click() { this.dispatch('click', { stopPropagation() {}, preventDefault() {} }) }
  focus() {}
  select() {}
  _matches(selector) {
    const cls = /^\.([A-Za-z0-9_-]+)$/.exec(selector)
    if (cls) return this.classList.contains(cls[1])
    const attr = /^\[([A-Za-z0-9_-]+)="([^"]*)"\]$/.exec(selector)
    if (attr) return this.getAttribute(attr[1]) === attr[2]
    return false
  }
  querySelectorAll(selector) {
    const out = []
    const walk = (node) => { for (const c of node.childNodes) { if (c._matches(selector)) out.push(c); walk(c) } }
    walk(this)
    return out
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null }
}

class FakeDocument {
  constructor() {
    this.body = new FakeElement('body')
    this.byId = Object.create(null)
    this.listeners = Object.create(null)
    this.cookie = ''
    this.visibilityState = 'visible'
    this.referrer = ''
  }
  createElement(tag) { return new FakeElement(tag) }
  getElementById(id) { return this.byId[id] || null }
  querySelector() { return null }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn) }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter(f => f !== fn) }
  // 模拟文档级事件（如用户点击页面任意处）
  dispatch(type, event = {}) { for (const fn of [...(this.listeners[type] || [])]) fn({ type, ...event }) }
  execCommand() { return true }
}

class FakeStorage {
  constructor(init = {}) { this.map = new Map(Object.entries(init)) }
  getItem(k) { return this.map.has(k) ? this.map.get(k) : null }
  setItem(k, v) { this.map.set(k, String(v)) }
  removeItem(k) { this.map.delete(k) }
  key(i) { return [...this.map.keys()][i] ?? null }
  get length() { return this.map.size }
}

class FakeClock {
  constructor(now) { this.now = now; this.seq = 0; this.timers = new Map() }
  setTimeout(fn, ms = 0) { const id = ++this.seq; this.timers.set(id, { at: this.now + Math.max(0, ms), fn, every: 0 }); return id }
  setInterval(fn, ms = 0) { const id = ++this.seq; this.timers.set(id, { at: this.now + Math.max(1, ms), fn, every: Math.max(1, ms) }); return id }
  clear(id) { this.timers.delete(id) }
  tick(ms) {
    const target = this.now + ms
    for (;;) {
      let nextId = null
      let next = null
      for (const [id, t] of this.timers) if (t.at <= target && (!next || t.at < next.at)) { nextId = id; next = t }
      if (!next) break
      // 时钟只前进不后退：测试里手动把 now 拨快后，早先排期的到期定时器在“当前时刻”触发（与浏览器一致），
      // 不能把 now 拨回定时器的原定时刻，否则之后读取的 Date.now() 会倒退
      const firedAt = Math.max(this.now, next.at)
      this.now = firedAt
      if (next.every) next.at = firedAt + next.every
      else this.timers.delete(nextId)
      next.fn()
    }
    this.now = target
  }
}

// 选项（[119] 新增，均为可选，不传时与原行为完全一致）：
//   hall: true 时加载 hall-core.js + hall.js 并构造大厅页 DOM
//   routes(url, opts, respond) 返回响应即覆盖默认的假接口
//   globals 合并进沙箱全局（如假 LivekitClient、BroadcastChannel）
//   settings 为 /api/visitor/settings 的 data
export async function bootWidget({ storage = {}, session = {}, now = Date.parse('2026-10-14T12:00:00Z'), hall = false, routes = null, globals = {}, settings = {} } = {}) {
  const clock = new FakeClock(now)
  const doc = new FakeDocument()
  const el = (tag, id) => { const e = doc.createElement(tag); if (id) { e.id = id; doc.byId[id] = e } return e }
  const app = el('div', 'app')
  doc.body.appendChild(app)
  const list = el('div', 'list')
  const footer = el('footer')
  app.appendChild(list)
  app.appendChild(footer)
  const input = el('textarea', 'input')
  const pendingList = el('div', 'pendingList')
  footer.appendChild(pendingList)
  footer.appendChild(input)
  for (const [tag, id] of [['button', 'sendBtn'], ['button', 'fileBtn'], ['input', 'fileInput'], ['span', 'status'], ['span', 'dot'],
    ['div', 'closeBtn'], ['button', 'voiceBtn'], ['button', 'voiceHangBtn'], ['audio', 'voiceRemoteAudio'], ['div', 'voicePanel'], ['span', 'voiceBtnHint']]) {
    footer.appendChild(el(tag, id))
  }
  if (hall) {
    // 与 chat.html 中大厅页的 id / class 保持一致（只构造 hall.js 用到的节点）
    const tabs = el('nav', 'tabs'); tabs.hidden = true
    const tabHall = el('button', 'tabHall'); const tabPrivate = el('button', 'tabPrivate')
    tabs.appendChild(tabHall); tabs.appendChild(tabPrivate)
    tabHall.appendChild(el('span', 'tabHallCount'))
    const hd = el('span', 'tabHallDot'); hd.hidden = true; tabHall.appendChild(hd)
    const pd = el('span', 'tabPrivateDot'); pd.hidden = true; tabPrivate.appendChild(pd)
    app.insertBefore(tabs, list)
    const pane = el('section', 'hallPane'); pane.hidden = true
    app.insertBefore(pane, list)
    const priv = el('section', 'privatePane')
    app.appendChild(priv)
    const withIcon = (id, labelId) => { const b = el('button', id); const ico = doc.createElement('span'); ico.className = 'hall-btn-ico'; b.appendChild(ico); if (labelId) b.appendChild(el('span', labelId)); return b }
    pane.appendChild(el('span', 'hallStatusText'))
    pane.appendChild(withIcon('hallMuteBtn', 'hallMuteLabel'))
    for (const id of ['hallPeople', 'hallVideo']) { const e = el('div', id); e.hidden = true; pane.appendChild(e) }
    const notice = el('div', 'hallNotice'); notice.hidden = true; pane.appendChild(notice)
    notice.appendChild(el('span', 'hallNoticeText'))
    const nb = el('button', 'hallNoticeBtn'); nb.hidden = true; notice.appendChild(nb)
    const scroll = el('div', 'hallScroll'); pane.appendChild(scroll)
    scroll.appendChild(el('button', 'hallGoPrivate'))
    const more = el('button', 'hallMore'); more.hidden = true; scroll.appendChild(more)
    scroll.appendChild(el('div', 'hallMsgs'))
    for (const id of ['hallNewBtn', 'hallToast']) { const e = el('div', id); e.hidden = true; pane.appendChild(e) }
    pane.appendChild(el('div', 'hallPendingList'))
    pane.appendChild(el('textarea', 'hallInput'))
    pane.appendChild(el('button', 'hallFileBtn'))
    pane.appendChild(el('input', 'hallFileInput'))
    pane.appendChild(withIcon('hallMicBtn', 'hallMicLabel'))
    const cam = withIcon('hallCamBtn', 'hallCamLabel'); cam.hidden = true; pane.appendChild(cam)
    pane.appendChild(el('button', 'hallSendBtn'))
    pane.appendChild(el('div', 'hallAudioSink'))
  }

  const sockets = []
  class FakeWebSocket {
    constructor(url, protocols) { this.url = url; this.protocols = protocols; this.readyState = 0; this.sent = []; sockets.push(this) }
    send(data) { if (this.readyState !== 1) throw new Error('WebSocket 未连接'); this.sent.push(JSON.parse(data)) }
    close() { this.readyState = 3; if (this.onclose) this.onclose({ code: 1000 }) }
    open() { this.readyState = 1; if (this.onopen) this.onopen({}) }
    receive(env) { if (this.onmessage) this.onmessage({ data: JSON.stringify(env) }) }
  }
  FakeWebSocket.CONNECTING = 0
  FakeWebSocket.OPEN = 1
  FakeWebSocket.CLOSING = 2
  FakeWebSocket.CLOSED = 3

  const posted = []
  const parentWindow = { postMessage: (msg) => posted.push(msg), location: { href: 'https://host.example/page' }, document: { title: '宿主页' } }
  const localStorage = new FakeStorage(storage)
  const sessionStorage = new FakeStorage()
  const fetchCalls = []
  const respond = (body, status = 200) => ({ status, ok: status < 400, headers: { get: () => null }, json: async () => JSON.parse(JSON.stringify(body)) })
  async function fetch(url, opts) {
    fetchCalls.push({ url: String(url), opts })
    if (routes) {
      const custom = await routes(String(url), opts || {}, respond)
      if (custom) return custom
    }
    if (String(url).includes('/api/visitor/settings')) return respond({ code: 0, data: settings })
    if (String(url).includes('/api/visitor/session')) {
      return respond({ code: 0, visitor_id: 'v-test', conversation: 'conv-1', visitor_token: 'tok-test', history_reset_at: 0, recalled_ids: [], ...session })
    }
    return respond({ code: 404, msg: 'not found' }, 404)
  }
  class FakeAudio { constructor(src) { this.src = src; this.currentTime = 0; this.volume = 1; this.loop = false } play() { return Promise.resolve() } pause() {} }
  const FakeDate = class extends Date {
    constructor(...args) { if (args.length === 0) super(clock.now); else super(...args) }
    static now() { return clock.now }
  }
  const windowListeners = Object.create(null)

  const sandbox = {
    console: { log() {}, info() {}, warn() {}, error() {} },
    location: { search: '?site=default', origin: 'https://cs.example.com', protocol: 'https:', href: 'https://cs.example.com/widget/chat.html' },
    URL,
    URLSearchParams,
    Date: FakeDate,
    document: doc,
    navigator: { userAgent: 'node-test', mediaDevices: {} },
    localStorage,
    sessionStorage,
    WebSocket: FakeWebSocket,
    Audio: FakeAudio,
    fetch,
    parent: parentWindow,
    crypto: webcrypto,
    setTimeout: (fn, ms) => clock.setTimeout(fn, ms),
    clearTimeout: (id) => clock.clear(id),
    setInterval: (fn, ms) => clock.setInterval(fn, ms),
    clearInterval: (id) => clock.clear(id),
    requestAnimationFrame: (fn) => { fn(); return 0 },
    addEventListener: (type, fn) => { (windowListeners[type] ||= []).push(fn) },
    removeEventListener() {},
    ...globals,
  }
  sandbox.window = sandbox
  sandbox.self = sandbox
  vm.createContext(sandbox)
  if (hall) {
    vm.runInContext(HALL_CORE, sandbox, { filename: 'hall-core.js' })
    vm.runInContext(HALL_UI, sandbox, { filename: 'hall.js' })
  }
  vm.runInContext(SCRIPT, sandbox, { filename: 'chat.js' })

  const flush = async () => { for (let i = 0; i < 20; i++) await new Promise(resolve => setImmediate(resolve)) }
  await flush()
  return {
    clock,
    doc,
    app,
    list,
    input,
    posted,
    localStorage,
    fetchCalls,
    flush,
    ws: () => sockets[sockets.length - 1],
    sockets,
    sandbox,
    parentWindow,
    byId: (id) => doc.getElementById(id),
    // 模拟窗口级事件（postMessage、pagehide 等）
    dispatchWindow: (type, ev) => { for (const fn of windowListeners[type] || []) fn(ev) },
    listText: () => list.textContent,
    byClass: (name) => doc.body.querySelectorAll('.' + name),
    byMsgId: (id) => doc.body.querySelectorAll('[data-msg-id="' + id + '"]'),
  }
}
