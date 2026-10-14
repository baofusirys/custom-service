// 会话列表批量操作与实时事件的纯函数（删除、拉黑、资料更新、选择上限）。
import { normalizeProfile } from './visitorProfile.js'

export const BATCH_DELETE_MAX = 200
export const BATCH_EXPORT_MAX = 50
export const RECYCLE_BATCH_MAX = 200

const VISITOR_ID_PATTERN = /^[A-Za-z0-9_-]{1,64}$/

export function isValidVisitorId(value) {
  return typeof value === 'string' && VISITOR_ID_PATTERN.test(value)
}

// 去重 + 过滤非法 ID（防止把异常值拼进请求或路径）。
export function normalizeVisitorIds(list) {
  if (!Array.isArray(list)) return []
  const out = []
  const seen = new Set()
  for (const id of list) {
    if (!isValidVisitorId(id) || seen.has(id)) continue
    seen.add(id)
    out.push(id)
  }
  return out
}

export function validateBatchSelection(list, max) {
  const ids = normalizeVisitorIds(list)
  if (!ids.length) return { ok: false, ids, error: '请先选择客户' }
  if (ids.length > max) return { ok: false, ids, error: `单次最多选择 ${max} 位客户，当前已选 ${ids.length} 位` }
  return { ok: true, ids, error: '' }
}

// 从列表移除指定客户，返回新数组与移除条数（不修改原数组）。
export function removeVisitorsFromList(list, visitorIds) {
  const source = Array.isArray(list) ? list : []
  const drop = new Set(normalizeVisitorIds(visitorIds))
  if (!drop.size) return { list: source, removed: 0, removedItems: [] }
  const kept = []
  const removedItems = []
  for (const item of source) {
    if (drop.has(item?.visitor_id)) removedItems.push(item)
    else kept.push(item)
  }
  return { list: kept, removed: removedItems.length, removedItems }
}

// 把资料接口结果合并到会话列表项；marked_unread 为 true 时未读至少为 1（契约第 1 节）。
export function applyProfileToConversation(conv, profile) {
  if (!conv || typeof conv !== 'object' || !profile) return false
  const p = normalizeProfile(profile)
  conv.note = p.note
  conv.tags = p.tags
  conv.pinned = p.pinned
  conv.marked_unread = p.marked_unread
  if (p.marked_unread && !(Number(conv.unread) > 0)) conv.unread = 1
  return true
}

export function moveToTop(list, item) {
  if (!Array.isArray(list)) return
  const idx = list.indexOf(item)
  if (idx > 0) {
    list.splice(idx, 1)
    list.unshift(item)
  }
}

function count(data, key) {
  const n = Number(data?.[key])
  return Number.isFinite(n) && n > 0 ? Math.trunc(n) : 0
}

export function summarizeDeleteResult(data) {
  let text = `已删除 ${count(data, 'visitors')} 位客户的 ${count(data, 'conversations')} 个会话、${count(data, 'messages')} 条消息，已移入回收站（保留 7 天）`
  const skipped = count(data, 'skipped')
  if (skipped > 0) text += `；${skipped} 位客户无权操作，已跳过`
  return text
}

export function summarizeRestoreResult(data) {
  return `已恢复 ${count(data, 'visitors')} 位客户的 ${count(data, 'conversations')} 个会话`
}

export function summarizePurgeResult(data) {
  return `已彻底删除 ${count(data, 'conversations')} 个会话、${count(data, 'messages')} 条消息、${count(data, 'files')} 个文件`
}

export function senderLabel(sender) {
  if (sender === 'visitor') return '访客'
  if (sender === 'agent') return '客服'
  if (sender === 'sys' || sender === 'system') return '系统'
  return '未知'
}

export function visitorDisplayName(item) {
  if (item && typeof item.identifier === 'string' && item.identifier) return item.identifier
  return '访客 ' + String(item?.visitor_id || '').slice(0, 6)
}
