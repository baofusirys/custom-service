// 快捷回复（契约第 3 节）的客户端校验、筛选与插入逻辑。
import { charCount, hasControlChars } from './textLimits.js'

export const QR_TITLE_MAX = 32
export const QR_CONTENT_MAX = 1000
export const QR_SORT_MIN = 0
export const QR_SORT_MAX = 9999
export const QR_PERSONAL_LIMIT = 100
export const QR_TEAM_LIMIT = 200

export function validateQuickReply(input) {
  const src = input && typeof input === 'object' ? input : {}
  if (src.title !== undefined && src.title !== null && typeof src.title !== 'string') return { ok: false, error: '标题格式不正确' }
  if (typeof src.content !== 'string') return { ok: false, error: '请填写话术内容' }
  const title = (src.title ?? '').trim()
  const content = src.content.trim()
  if (charCount(title) > QR_TITLE_MAX) return { ok: false, error: `标题最多 ${QR_TITLE_MAX} 个字` }
  if (hasControlChars(title)) return { ok: false, error: '标题不能包含换行等控制字符' }
  if (!content) return { ok: false, error: '请填写话术内容' }
  if (charCount(content) > QR_CONTENT_MAX) return { ok: false, error: `内容最多 ${QR_CONTENT_MAX} 个字` }
  if (hasControlChars(content, { allowNewline: true })) return { ok: false, error: '内容不能包含控制字符' }
  const value = { title, content }
  if (src.sort_order !== undefined && src.sort_order !== null && src.sort_order !== '') {
    const n = Number(src.sort_order)
    if (!Number.isInteger(n) || n < QR_SORT_MIN || n > QR_SORT_MAX) {
      return { ok: false, error: `排序需为 ${QR_SORT_MIN}-${QR_SORT_MAX} 的整数` }
    }
    value.sort_order = n
  }
  return { ok: true, value, error: '' }
}

// 关键词同时匹配标题与内容，忽略大小写；空关键词返回原列表副本。
export function filterQuickReplies(list, keyword) {
  const items = Array.isArray(list) ? list.filter(it => it && typeof it.content === 'string') : []
  const q = typeof keyword === 'string' ? keyword.trim().toLowerCase() : ''
  if (!q) return items
  return items.filter(it => String(it.title || '').toLowerCase().includes(q) || it.content.toLowerCase().includes(q))
}

export function splitByScope(list) {
  const items = Array.isArray(list) ? list : []
  return {
    team: items.filter(it => it?.scope === 'team'),
    personal: items.filter(it => it?.scope === 'personal'),
  }
}

// 团队话术仅管理员可改删；个人话术接口只返回本人的，可直接管理。
export function canManageQuickReply(item, isAdmin) {
  if (!item) return false
  if (item.scope === 'team') return isAdmin === true
  return item.scope === 'personal'
}

// 点击话术填入输入框：草稿为空则直接填入，否则换行追加，避免覆盖客服已输入的内容。
export function insertQuickReply(draft, content) {
  const text = typeof content === 'string' ? content : ''
  const current = typeof draft === 'string' ? draft : ''
  if (!text) return current
  if (!current.trim()) return text
  return current.endsWith('\n') ? current + text : current + '\n' + text
}
