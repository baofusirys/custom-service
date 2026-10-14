// 北京时间（Asia/Shanghai，UTC+8，无夏令时）展示格式化的纯函数实现。
// 不依赖浏览器时区：无论客服电脑设置成哪个时区，界面都按北京时间显示。

const BEIJING_OFFSET_MS = 8 * 60 * 60 * 1000

function pad(n) {
  return String(n).padStart(2, '0')
}

// 统一把 Date / 毫秒数 / ISO 字符串转换成毫秒时间戳；无法解析返回 NaN。
export function toTimestamp(value) {
  if (value instanceof Date) return value.getTime()
  if (typeof value === 'number') return Number.isFinite(value) ? value : NaN
  if (typeof value === 'string' && value.trim()) {
    const ms = Date.parse(value.trim())
    return Number.isFinite(ms) ? ms : NaN
  }
  return NaN
}

function beijingParts(value) {
  const ms = toTimestamp(value)
  if (!Number.isFinite(ms)) return null
  const d = new Date(ms + BEIJING_OFFSET_MS)
  return {
    y: d.getUTCFullYear(),
    mo: pad(d.getUTCMonth() + 1),
    d: pad(d.getUTCDate()),
    h: pad(d.getUTCHours()),
    mi: pad(d.getUTCMinutes()),
    s: pad(d.getUTCSeconds()),
  }
}

// 'YYYY-MM-DD HH:mm:ss'（北京时间）；无效时间返回 empty 占位。
export function formatBeijing(value, { seconds = true, empty = '—' } = {}) {
  const p = beijingParts(value)
  if (!p) return empty
  const text = `${p.y}-${p.mo}-${p.d} ${p.h}:${p.mi}`
  return seconds ? `${text}:${p.s}` : text
}

// 'YYYYMMDD-HHmmss'（北京时间），用于文件命名。
// [113] 下列为会话列表 / 聊天区使用的北京时间展示（替代按浏览器本地时区格式化的 dayjs(t).format）。

// 'YYYY-MM-DD'（北京时间日历日），用于判断今天 / 昨天 / 今年。
export function beijingDateKey(value) {
  const p = beijingParts(value)
  return p ? p.y + '-' + p.mo + '-' + p.d : ''
}

// 'HH:mm'（北京时间）。
export function formatBeijingHM(value) {
  const p = beijingParts(value)
  return p ? p.h + ':' + p.mi : ''
}

// 消息时间：今天 HH:mm / 今年 MM-DD HH:mm / 往年 YYYY-MM-DD HH:mm（均为北京时间）。
export function formatMsgTime(value, now = Date.now()) {
  const p = beijingParts(value)
  const n = beijingParts(now)
  if (!p || !n) return ''
  if (p.y === n.y && p.mo === n.mo && p.d === n.d) return p.h + ':' + p.mi
  if (p.y === n.y) return p.mo + '-' + p.d + ' ' + p.h + ':' + p.mi
  return p.y + '-' + p.mo + '-' + p.d + ' ' + p.h + ':' + p.mi
}

// 分组时间分隔：今天 HH:mm / 昨天 HH:mm / MM-DD HH:mm（北京时间日历日判断）。
export function formatGroupTime(value, now = Date.now()) {
  const p = beijingParts(value)
  const nowMs = toTimestamp(now)
  if (!p || !Number.isFinite(nowMs)) return ''
  const key = p.y + '-' + p.mo + '-' + p.d
  if (key === beijingDateKey(nowMs)) return '今天 ' + p.h + ':' + p.mi
  if (key === beijingDateKey(nowMs - 24 * 60 * 60 * 1000)) return '昨天 ' + p.h + ':' + p.mi
  return p.mo + '-' + p.d + ' ' + p.h + ':' + p.mi
}


// 'YYYYMMDD-HHmmss'（北京时间），用于文件命名（保持原位置之后的导出不变）。
export function compactBeijingStamp(value = Date.now()) {
  const p = beijingParts(value) || beijingParts(Date.now())
  return `${p.y}${p.mo}${p.d}-${p.h}${p.mi}${p.s}`
}
