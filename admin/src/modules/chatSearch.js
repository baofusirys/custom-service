// 全局搜索聊天内容（契约第 8 节）的关键词与条数校验。
import { charCount, hasControlChars } from './textLimits.js'

export const SEARCH_MIN_CHARS = 2
export const SEARCH_MAX_CHARS = 64
export const SEARCH_DEFAULT_LIMIT = 30
export const SEARCH_MAX_LIMIT = 50

export function normalizeSearchKeyword(raw) {
  const q = typeof raw === 'string' ? raw.trim() : ''
  const n = charCount(q)
  if (n < SEARCH_MIN_CHARS) return { ok: false, q, error: `请输入至少 ${SEARCH_MIN_CHARS} 个字` }
  if (n > SEARCH_MAX_CHARS) return { ok: false, q, error: `关键词最多 ${SEARCH_MAX_CHARS} 个字` }
  if (hasControlChars(q)) return { ok: false, q, error: '关键词不能包含换行等控制字符' }
  return { ok: true, q, error: '' }
}

export function clampSearchLimit(value) {
  const n = Math.trunc(Number(value))
  if (!Number.isFinite(n) || n <= 0) return SEARCH_DEFAULT_LIMIT
  return Math.min(SEARCH_MAX_LIMIT, n)
}
