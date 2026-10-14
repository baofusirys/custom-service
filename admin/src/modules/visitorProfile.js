// 客户备注 / 标签 / 拉黑原因的客户端校验（与契约第 4、6 节限制一致，后端仍做最终校验）。
import { charCount, hasControlChars, truncateChars } from './textLimits.js'

export const TAG_MAX_COUNT = 10
export const TAG_MAX_LENGTH = 16
export const NOTE_MAX_LENGTH = 500
export const BLOCK_REASON_MAX_LENGTH = 100

function fail(error, extra = {}) {
  return { ok: false, error, ...extra }
}

// 单个标签：去首尾空格后 1-16 字，不含控制字符。
export function validateTagText(raw) {
  const tag = typeof raw === 'string' ? raw.trim() : ''
  if (!tag) return fail('标签不能为空', { tag })
  if (charCount(tag) > TAG_MAX_LENGTH) return fail(`每个标签最多 ${TAG_MAX_LENGTH} 个字`, { tag })
  if (hasControlChars(tag)) return fail('标签不能包含换行等控制字符', { tag })
  return { ok: true, tag, error: '' }
}

// 标签整体规范化：去首尾空格、空项忽略、去重（保持首次出现顺序）、最多 10 个。
export function normalizeTags(list) {
  if (list === null || list === undefined) return { ok: true, tags: [], error: '' }
  if (!Array.isArray(list)) return fail('标签格式不正确', { tags: [] })
  const tags = []
  const seen = new Set()
  for (const raw of list) {
    if (typeof raw !== 'string') return fail('标签格式不正确', { tags: [] })
    const trimmed = raw.trim()
    if (!trimmed) continue
    const check = validateTagText(trimmed)
    if (!check.ok) return fail(`「${truncateChars(trimmed, 20)}」${check.error}`, { tags: [] })
    if (seen.has(check.tag)) continue
    seen.add(check.tag)
    tags.push(check.tag)
  }
  if (tags.length > TAG_MAX_COUNT) return fail(`标签最多 ${TAG_MAX_COUNT} 个`, { tags: [] })
  return { ok: true, tags, error: '' }
}

// 输入框追加一个标签：返回新数组，不修改原数组。
export function tryAddTag(existing, raw) {
  const current = Array.isArray(existing) ? existing.slice() : []
  const check = validateTagText(raw)
  if (!check.ok) return fail(check.error, { tags: current })
  if (current.includes(check.tag)) return fail('该标签已存在', { tags: current })
  if (current.length >= TAG_MAX_COUNT) return fail(`标签最多 ${TAG_MAX_COUNT} 个`, { tags: current })
  return { ok: true, tags: [...current, check.tag], error: '' }
}

export function validateNote(raw) {
  if (raw !== null && raw !== undefined && typeof raw !== 'string') return fail('备注格式不正确', { note: '' })
  const note = raw ?? ''
  if (charCount(note) > NOTE_MAX_LENGTH) return fail(`备注最多 ${NOTE_MAX_LENGTH} 字`, { note })
  if (hasControlChars(note, { allowNewline: true })) return fail('备注不能包含控制字符', { note })
  return { ok: true, note, error: '' }
}

export function validateBlockReason(raw) {
  if (raw !== null && raw !== undefined && typeof raw !== 'string') return fail('拉黑原因格式不正确', { reason: '' })
  const reason = (raw ?? '').trim()
  if (charCount(reason) > BLOCK_REASON_MAX_LENGTH) return fail(`拉黑原因最多 ${BLOCK_REASON_MAX_LENGTH} 字`, { reason })
  if (hasControlChars(reason)) return fail('拉黑原因不能包含换行等控制字符', { reason })
  return { ok: true, reason, error: '' }
}

// 备注摘要：取第一行非空文字并截断，用于会话列表。
export function noteSummary(note, max = 24) {
  const firstLine = String(note ?? '').split(/\r?\n/).map(line => line.trim()).find(Boolean) || ''
  return truncateChars(firstLine, max)
}

// 后端资料对象规范化：字段缺失或类型异常时给安全默认值，避免模板渲染报错。
export function normalizeProfile(data) {
  const d = data && typeof data === 'object' ? data : {}
  return {
    note: typeof d.note === 'string' ? d.note : '',
    tags: Array.isArray(d.tags) ? d.tags.filter(t => typeof t === 'string' && t) : [],
    pinned: d.pinned === true,
    marked_unread: d.marked_unread === true,
    blocked: d.blocked === true,
    block_reason: typeof d.block_reason === 'string' ? d.block_reason : '',
    blocked_at: d.blocked_at || null,
    ip_blocked: d.ip_blocked === true,
  }
}
