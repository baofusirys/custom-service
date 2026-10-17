// 从大厅「私聊」跳转到在线会话并定位访客：通过同源 localStorage 一次性票据传递 vid。
// 访客 vid 可用于恢复访客会话（等同凭证），因此不放进 URL（不进浏览器历史、代理日志与 Referer），
// URL 中只带 32 位随机票据；票据 60 秒过期、读取一次即删除。
import { isValidVisitorId } from './conversationOps.js'

export const JUMP_TICKET_TTL_MS = 60 * 1000
const PREFIX = 'cs_hall_jump:'
const NONCE = /^[a-f0-9]{32}$/
const MAX_SCAN = 200

function randomHex(cryptoSource) {
  if (!cryptoSource || typeof cryptoSource.getRandomValues !== 'function') return ''
  const b = new Uint8Array(16)
  cryptoSource.getRandomValues(b)
  return Array.from(b, x => x.toString(16).padStart(2, '0')).join('')
}

export function isValidJumpNonce(nonce) {
  return typeof nonce === 'string' && NONCE.test(nonce)
}

// 清理过期或损坏的票据（只处理本模块前缀的键）
export function pruneJumpTickets(storage = globalThis.localStorage, now = Date.now()) {
  if (!storage || typeof storage.length !== 'number') return 0
  const keys = []
  for (let i = 0; i < Math.min(storage.length, MAX_SCAN); i++) {
    const k = storage.key(i)
    if (typeof k === 'string' && k.startsWith(PREFIX)) keys.push(k)
  }
  let removed = 0
  for (const k of keys) {
    let expired = true
    try {
      const v = JSON.parse(storage.getItem(k) || 'null')
      expired = !v || !Number.isFinite(v.exp) || v.exp < now
    } catch {}
    if (expired) {
      storage.removeItem(k)
      removed++
    }
  }
  return removed
}

export function createJumpTicket(vid, { storage = globalThis.localStorage, cryptoSource = globalThis.crypto, now = Date.now() } = {}) {
  if (!isValidVisitorId(vid) || !storage) return ''
  const nonce = randomHex(cryptoSource)
  if (!isValidJumpNonce(nonce)) return ''
  try {
    pruneJumpTickets(storage, now)
    storage.setItem(PREFIX + nonce, JSON.stringify({ vid, exp: now + JUMP_TICKET_TTL_MS }))
  } catch {
    return ''
  }
  return nonce
}

export function consumeJumpTicket(nonce, { storage = globalThis.localStorage, now = Date.now() } = {}) {
  if (!isValidJumpNonce(nonce) || !storage) return ''
  const key = PREFIX + nonce
  let value = null
  try {
    value = JSON.parse(storage.getItem(key) || 'null')
  } catch {
    value = null
  }
  try { storage.removeItem(key) } catch {}
  if (!value || !Number.isFinite(value.exp) || value.exp < now) return ''
  return isValidVisitorId(value.vid) ? value.vid : ''
}
