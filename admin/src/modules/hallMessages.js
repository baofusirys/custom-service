// 多人大厅消息：规范化、去重排序、乐观发送状态、发送限流与附件预校验（纯函数，可在 node 测试中直接运行）。
// 契约：HTTP HallMessage 用 media_url/media_kind/media_name/media_size（第 6 节）；
// WS 信封沿用现有 ws.Envelope 字段名 media/mkind/mname/msize，发送者信息在 extra，时间为 ts 毫秒（主代理统一定义）。
import { charCount, truncateChars } from './textLimits.js'

export const HALL_PAGE_SIZE = 50
export const HALL_MESSAGE_MAX_CHARS = 1000
// 内存中最多保留的消息条数（向上翻页与实时追加共用上限，防止长时间挂机占用过多内存）
export const HALL_MESSAGE_MEMORY_LIMIT = 1000
export const HALL_ACK_TIMEOUT_MS = 15000
export const HALL_SEND_LIMIT = Object.freeze({ max: 5, windowMs: 10000 })
// 与后端 BACKEND_MAX_UPLOAD_MB 默认值一致；最终以服务端校验为准
export const HALL_UPLOAD_MAX_BYTES = 20 * 1024 * 1024
export const HALL_IMAGE_EXTENSIONS = Object.freeze(['jpg', 'jpeg', 'png', 'gif', 'webp'])
export const HALL_FILE_EXTENSIONS = Object.freeze(['pdf', 'zip', 'docx', 'xlsx', 'doc', 'xls', 'txt'])

const SENDERS = new Set(['visitor', 'agent', 'system'])
const MEDIA_KINDS = new Set(['image', 'file'])
const MESSAGE_ID = /^[A-Za-z0-9-]{1,36}$/
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
// 本系统上传返回的地址形如 /files/2026/10/17/<uuid>.png?access=<token>；其它来源一律不渲染
const MEDIA_URL = /^\/files\/[A-Za-z0-9_\-./]{1,200}(\?access=[A-Za-z0-9_\-]{1,256})?$/
const CONTROL = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g
const SINGLE_LINE_CONTROL = /[\u0000-\u001F\u007F\u200E\u200F\u202A-\u202E\u2066-\u2069]/g
const MAX_DISPLAY_CONTENT_CHARS = 4000
const MAX_LINES = 60

export function isValidHallMessageId(id) {
  return typeof id === 'string' && MESSAGE_ID.test(id)
}

// 契约要求 hall_chat.id 为 uuid v4；只用密码学随机源生成
export function createHallMessageId(cryptoSource = globalThis.crypto) {
  if (!cryptoSource) throw new Error('当前浏览器不支持密码学随机数，无法发送消息')
  let id = ''
  if (typeof cryptoSource.randomUUID === 'function') {
    id = String(cryptoSource.randomUUID()).toLowerCase()
  } else if (typeof cryptoSource.getRandomValues === 'function') {
    const b = new Uint8Array(16)
    cryptoSource.getRandomValues(b)
    b[6] = (b[6] & 0x0f) | 0x40
    b[8] = (b[8] & 0x3f) | 0x80
    const h = Array.from(b, x => x.toString(16).padStart(2, '0')).join('')
    id = h.slice(0, 8) + '-' + h.slice(8, 12) + '-' + h.slice(12, 16) + '-' + h.slice(16, 20) + '-' + h.slice(20)
  }
  if (!UUID_V4.test(id)) throw new Error('消息 ID 生成失败')
  return id
}

export function sanitizeMediaUrl(url) {
  if (typeof url !== 'string') return ''
  const u = url.trim()
  if (!MEDIA_URL.test(u) || u.includes('..') || u.includes('//')) return ''
  return u
}

function cleanLine(value, max) {
  if (typeof value !== 'string') return ''
  return truncateChars(value.replace(SINGLE_LINE_CONTROL, '').trim(), max)
}

function toMs(value) {
  if (typeof value === 'number') return Number.isFinite(value) && value > 0 ? value : NaN
  if (typeof value === 'string' && value.trim()) {
    const ms = Date.parse(value.trim())
    return Number.isFinite(ms) ? ms : NaN
  }
  if (value instanceof Date) return value.getTime()
  return NaN
}

function normalizeMedia(url, kind, name, size) {
  const mediaUrl = sanitizeMediaUrl(url)
  if (!mediaUrl) return { mediaUrl: '', mediaKind: '', mediaName: '', mediaSize: 0 }
  const n = typeof size === 'string' ? Number(size) : size
  return {
    mediaUrl,
    mediaKind: MEDIA_KINDS.has(kind) ? kind : 'file',
    mediaName: cleanLine(name, 120) || '附件',
    mediaSize: typeof n === 'number' && Number.isFinite(n) && n > 0 ? Math.floor(n) : 0,
  }
}

function build({ id, sender, identity, name, role, content, media, createdAt, status }) {
  if (!isValidHallMessageId(id)) return null
  const s = SENDERS.has(sender) ? sender : (sender === 'sys' ? 'system' : '')
  if (!s) return null
  const text = typeof content === 'string' ? truncateChars(content.replace(CONTROL, ''), MAX_DISPLAY_CONTENT_CHARS) : ''
  if (!text.trim() && !media.mediaUrl) return null
  const ts = Number.isFinite(createdAt) ? createdAt : Date.now()
  const r = role === 'agent' || role === 'visitor' ? role : (s === 'system' ? '' : s)
  return {
    id,
    sender: s,
    identity: cleanLine(identity, 40),
    name: cleanLine(name, 64) || (s === 'agent' ? '客服' : (s === 'system' ? '系统' : '访客')),
    role: r,
    content: text,
    ...media,
    createdAt: ts,
    status: status || 'sent',
  }
}

// HTTP HallMessage（join.history / GET messages）
export function normalizeHallMessage(raw) {
  if (!raw || typeof raw !== 'object') return null
  return build({
    id: raw.id,
    sender: raw.sender,
    identity: raw.identity,
    name: raw.name,
    role: raw.role,
    content: raw.content,
    media: normalizeMedia(raw.media_url, raw.media_kind, raw.media_name, raw.media_size),
    createdAt: toMs(raw.created_at),
  })
}

// WS 广播 {type:"hall_chat", id, content, media, mkind, mname, msize, ts, extra:{sender, identity, name, role}}
export function fromHallChatEnvelope(env) {
  if (!env || typeof env !== 'object' || env.type !== 'hall_chat') return null
  const extra = env.extra && typeof env.extra === 'object' ? env.extra : {}
  return build({
    id: env.id,
    sender: extra.sender,
    identity: extra.identity,
    name: extra.name,
    role: extra.role,
    content: env.content,
    media: normalizeMedia(env.media, env.mkind, env.mname, env.msize),
    createdAt: toMs(env.ts),
  })
}

export function normalizeHallMessageList(list, max = HALL_PAGE_SIZE) {
  if (!Array.isArray(list)) return []
  const out = []
  for (const raw of list.slice(0, Math.max(0, max))) {
    const m = normalizeHallMessage(raw)
    if (m) out.push(m)
  }
  return out
}

function compareMessages(a, b) {
  if (a.createdAt !== b.createdAt) return a.createdAt - b.createdAt
  return a.id < b.id ? -1 : (a.id > b.id ? 1 : 0)
}

const PENDING = new Set(['sending', 'failed'])

// 按 id 幂等合并并按时间正序排列。服务端版本覆盖本地乐观版本；本地待发版本不覆盖服务端版本。
// keep='newest' 超限时丢弃最旧（实时追加），keep='oldest' 超限时丢弃最新（向上翻页时保护已加载的历史不被挤掉）。
export function mergeHallMessages(current, incoming, { limit = HALL_MESSAGE_MEMORY_LIMIT, keep = 'newest' } = {}) {
  const map = new Map()
  for (const m of Array.isArray(current) ? current : []) if (m && isValidHallMessageId(m.id)) map.set(m.id, m)
  for (const m of Array.isArray(incoming) ? incoming : []) {
    if (!m || !isValidHallMessageId(m.id)) continue
    const old = map.get(m.id)
    if (old && !PENDING.has(old.status) && PENDING.has(m.status)) continue
    map.set(m.id, m)
  }
  const list = Array.from(map.values()).sort(compareMessages)
  const max = Math.max(1, Math.floor(limit))
  if (list.length <= max) return list
  return keep === 'oldest' ? list.slice(0, max) : list.slice(list.length - max)
}

export function removeHallMessage(list, id) {
  if (!Array.isArray(list) || !isValidHallMessageId(id)) return Array.isArray(list) ? list : []
  const next = list.filter(m => m.id !== id)
  return next.length === list.length ? list : next
}

export function markHallMessageStatus(list, id, status) {
  if (!Array.isArray(list) || !isValidHallMessageId(id)) return Array.isArray(list) ? list : []
  let changed = false
  const next = list.map(m => {
    if (m.id !== id || m.status === status) return m
    // 已确认（sent）的消息不能被迟到的超时再改成失败
    if (m.status === 'sent' && status === 'failed') return m
    changed = true
    return { ...m, status }
  })
  return changed ? next : list
}

// 向上翻页游标：最旧的一条已确认消息
export function oldestCursor(list) {
  if (!Array.isArray(list)) return ''
  for (const m of list) if (m && m.status === 'sent' && isValidHallMessageId(m.id)) return m.id
  return ''
}

export function contentLines(content) {
  if (typeof content !== 'string' || !content) return []
  return content.split(/\r?\n/).slice(0, MAX_LINES)
}

export function hallSenderLabel(message) {
  if (!message) return ''
  if (message.sender === 'system') return '系统'
  return message.role === 'agent' || message.sender === 'agent' ? '客服' : '访客'
}

// 发送前校验：去首尾空白；1–1000 字（按 Unicode 码点，与后端口径一致）
export function validateOutgoingText(text) {
  const content = typeof text === 'string' ? text.replace(CONTROL, '').trim() : ''
  if (!content) return { ok: false, error: '请输入消息内容' }
  if (charCount(content) > HALL_MESSAGE_MAX_CHARS) return { ok: false, error: '消息最多 ' + HALL_MESSAGE_MAX_CHARS + ' 字' }
  return { ok: true, content }
}

// 构造发送信封（字段名与现有 ws.Envelope 一致）
export function buildHallChatEnvelope({ id, content = '', media = null }) {
  if (!UUID_V4.test(String(id || ''))) return null
  const env = { type: 'hall_chat', id, content: typeof content === 'string' ? content : '' }
  if (media) {
    const url = sanitizeMediaUrl(media.url)
    if (!url) return null
    env.media = url
    env.mkind = MEDIA_KINDS.has(media.kind) ? media.kind : 'file'
    env.mname = cleanLine(media.name, 120) || '附件'
    const size = Number(media.size)
    if (Number.isFinite(size) && size > 0) env.msize = Math.floor(size)
  }
  if (!env.content && !env.media) return null
  return env
}

// 本地乐观消息（发送中）
export function createPendingHallMessage({ id, content = '', media = null, self = {} }, now = Date.now()) {
  return build({
    id,
    sender: 'agent',
    identity: self.identity || '',
    name: self.name || '我',
    role: 'agent',
    content,
    media: media ? normalizeMedia(media.url, media.kind, media.name, media.size) : normalizeMedia(''),
    createdAt: now,
    status: 'sending',
  })
}

// 滑动窗口限流（客户端自我约束，真正的限流在服务端）
export class SlidingWindowLimiter {
  constructor({ max = HALL_SEND_LIMIT.max, windowMs = HALL_SEND_LIMIT.windowMs, now = () => Date.now() } = {}) {
    this.max = Math.max(1, Math.floor(max))
    this.windowMs = Math.max(1, Math.floor(windowMs))
    this.now = now
    this.hits = []
  }

  tryAcquire() {
    const t = this.now()
    // 时钟回拨时清空窗口，避免长期卡死
    this.hits = this.hits.filter(h => h <= t && t - h < this.windowMs)
    if (this.hits.length >= this.max) return false
    this.hits.push(t)
    return true
  }
}

function extensionOf(name) {
  const base = typeof name === 'string' ? name.split(/[\\/]/).pop() || '' : ''
  const idx = base.lastIndexOf('.')
  return idx > 0 ? base.slice(idx + 1).toLowerCase() : ''
}

// 上传前预校验（类型白名单与大小）；服务端仍会做扩展名 + MIME + 文件头三重校验
export function checkHallUploadFile(file, kind, maxBytes = HALL_UPLOAD_MAX_BYTES) {
  if (!file || typeof file !== 'object') return { ok: false, error: '请选择文件' }
  const size = Number(file.size)
  if (!Number.isFinite(size) || size <= 0) return { ok: false, error: '文件为空，无法发送' }
  if (size > maxBytes) return { ok: false, error: '文件不能超过 ' + Math.floor(maxBytes / 1024 / 1024) + 'MB' }
  const ext = extensionOf(file.name)
  const allowed = kind === 'image' ? HALL_IMAGE_EXTENSIONS : [...HALL_IMAGE_EXTENSIONS, ...HALL_FILE_EXTENSIONS]
  if (!allowed.includes(ext)) {
    return { ok: false, error: kind === 'image' ? '仅支持 JPG、PNG、GIF、WEBP 图片' : '仅支持图片、PDF、ZIP、Word、Excel、TXT 文件' }
  }
  return { ok: true }
}

// 上传接口返回 {code:0,id,url,kind,size,name,mime} → 发送用的媒体描述
export function mediaFromUploadResponse(res) {
  if (!res || typeof res !== 'object') return null
  const url = sanitizeMediaUrl(res.url)
  if (!url) return null
  return {
    url,
    kind: MEDIA_KINDS.has(res.kind) ? res.kind : 'file',
    name: cleanLine(res.name, 120) || '附件',
    size: Number.isFinite(Number(res.size)) ? Math.max(0, Math.floor(Number(res.size))) : 0,
  }
}

export function formatFileSize(bytes) {
  const n = Number(bytes)
  if (!Number.isFinite(n) || n <= 0) return ''
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}
