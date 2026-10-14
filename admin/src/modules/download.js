// 文件下载的纯逻辑：Content-Disposition 文件名解析与安全化（不触碰 DOM）。
import { compactBeijingStamp } from './timeFormat.js'

export const EXPORT_FORMATS = Object.freeze(['xlsx', 'txt'])
const MAX_HEADER_LENGTH = 4096
const RFC5987_VALUE = /^([A-Za-z0-9!#$&+.^_`|~-]+)'([A-Za-z0-9-]*)'(.*)$/

export function normalizeExportFormat(format) {
  return EXPORT_FORMATS.includes(format) ? format : 'xlsx'
}

function stripQuotes(raw) {
  const value = String(raw || '').trim()
  if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
    return value.slice(1, -1).replace(/\\(.)/g, '$1')
  }
  return value
}

// 按分号拆分参数，忽略引号内的分号。
function parseParams(header) {
  const parts = []
  let current = ''
  let inQuote = false
  let escaped = false
  for (const ch of header) {
    if (escaped) { current += ch; escaped = false; continue }
    if (inQuote && ch === '\\') { current += ch; escaped = true; continue }
    if (ch === '"') { inQuote = !inQuote; current += ch; continue }
    if (ch === ';' && !inQuote) { parts.push(current); current = ''; continue }
    current += ch
  }
  parts.push(current)
  const params = Object.create(null)
  for (const part of parts) {
    const idx = part.indexOf('=')
    if (idx <= 0) continue
    const key = part.slice(0, idx).trim().toLowerCase()
    if (key && !(key in params)) params[key] = part.slice(idx + 1).trim()
  }
  return params
}

function decodeExtValue(raw) {
  const match = RFC5987_VALUE.exec(stripQuotes(raw))
  if (!match) return ''
  const charset = match[1].toLowerCase()
  const encoded = match[3]
  try {
    if (charset === 'utf-8' || charset === 'utf8') return decodeURIComponent(encoded)
    if (charset === 'iso-8859-1' || charset === 'latin1') {
      return encoded.replace(/%([0-9A-Fa-f]{2})/g, (_, hex) => String.fromCharCode(parseInt(hex, 16)))
    }
  } catch {
    return ''
  }
  return ''
}

// 优先 filename*（RFC 5987/6266，支持中文），其次 filename；解析失败返回空串。
export function parseContentDispositionFilename(header) {
  if (typeof header !== 'string' || !header || header.length > MAX_HEADER_LENGTH) return ''
  const params = parseParams(header)
  if (params['filename*']) {
    const decoded = decodeExtValue(params['filename*'])
    if (decoded) return decoded
  }
  if (params.filename) return stripQuotes(params.filename)
  return ''
}

const RESERVED_WINDOWS_NAME = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i

// 文件名安全化：去路径、去控制字符/保留字符/双向文本控制符（防扩展名伪装）、限制长度并保留扩展名。
export function sanitizeFilename(name, fallback = 'export.xlsx', maxChars = 120) {
  const clean = (value) => {
    let s = typeof value === 'string' ? value : ''
    s = s.split(/[\\/]/).pop() || ''
    s = s.replace(/[\u200E\u200F\u202A-\u202E\u2066-\u2069]/g, '')
    s = s.replace(/[\u0000-\u001F\u007F<>:"|?*]/g, '_')
    s = s.trim().replace(/^[.\s]+/, '').replace(/[.\s]+$/, '')
    if (!s) return ''
    if (RESERVED_WINDOWS_NAME.test(s)) s = '_' + s
    const chars = Array.from(s)
    if (chars.length > maxChars) {
      const dot = s.lastIndexOf('.')
      const ext = dot > 0 ? s.slice(dot) : ''
      const extChars = Array.from(ext)
      if (extChars.length > 1 && extChars.length <= 10) {
        s = Array.from(s.slice(0, dot)).slice(0, maxChars - extChars.length).join('') + ext
      } else {
        s = chars.slice(0, maxChars).join('')
      }
    }
    return s
  }
  return clean(name) || clean(fallback) || 'export'
}

// 服务端未给文件名时的兜底名：前缀-北京时间.格式
export function exportFallbackName(prefix, format, now = Date.now()) {
  const safePrefix = sanitizeFilename(String(prefix || '聊天记录'), '聊天记录')
  return `${safePrefix}-${compactBeijingStamp(now)}.${normalizeExportFormat(format)}`
}
