// 消息撤回（契约第 9 节）：时限判断、占位文案、本地状态改写与增量同步游标。
import { isPendingMessage, messageIdentityKeys } from './messageState.js'
import { toTimestamp } from './timeFormat.js'

export const RECALL_WINDOW_MS = 2 * 60 * 1000
// 客户端时钟可能略快于服务端：消息时间比本地“未来” 30 秒以内仍视为正常
const FUTURE_SKEW_TOLERANCE_MS = 30 * 1000
// 增量同步回退窗口 = 撤回时限 + 1 分钟余量（覆盖时钟误差与网络延迟）
export const RECALL_SYNC_LOOKBACK_MS = RECALL_WINDOW_MS + 60 * 1000
const SERVER_MESSAGE_ID = /^[A-Za-z0-9-]{1,36}$/

export const RECALL_ERROR = Object.freeze({ NOT_OWNER: 40310, NOT_FOUND: 40410, EXPIRED: 40910, ALREADY: 40911 })

export function isRecalled(message) {
  return !!message && message.recalled === true
}

export function canRecallMessage(message, myAgentId, now = Date.now()) {
  if (!message || isRecalled(message)) return false
  if (myAgentId === null || myAgentId === undefined || myAgentId === '') return false
  if (message.sender !== 'agent' || String(message.sender_ref) !== String(myAgentId)) return false
  if (isPendingMessage(message)) return false
  const id = typeof message.id === 'string' ? message.id : ''
  if (!SERVER_MESSAGE_ID.test(id) || id.startsWith('local-')) return false
  const created = toTimestamp(message.created_at)
  if (!Number.isFinite(created)) return false
  const age = now - created
  return age >= -FUTURE_SKEW_TOLERANCE_MS && age < RECALL_WINDOW_MS
}

export function recallPlaceholder(message, myAgentId) {
  if (message?.sender === 'agent') {
    const mine = myAgentId !== null && myAgentId !== undefined && myAgentId !== '' &&
      String(message.sender_ref) === String(myAgentId)
    return mine ? '你撤回了一条消息' : '客服撤回了一条消息'
  }
  return '该消息已撤回'
}

// 标记已撤回并清空正文与附件，返回是否发生变化（幂等）。
export function markMessageRecalled(message) {
  if (!message || typeof message !== 'object') return false
  const changed = message.recalled !== true || !!message.content || !!message.media_url
  message.recalled = true
  message.content = ''
  message.media_url = null
  message.media_kind = null
  message.media_name = null
  return changed
}

export function applyRecallToList(list, messageId) {
  const id = typeof messageId === 'string' ? messageId : ''
  if (!Array.isArray(list) || !SERVER_MESSAGE_ID.test(id)) return { matched: false, changed: false, message: null }
  const target = list.find(m => messageIdentityKeys(m).includes(id))
  if (!target) return { matched: false, changed: false, message: null }
  return { matched: true, changed: markMessageRecalled(target), message: target }
}

export function recallFailureMeansAlreadyRecalled(error) {
  const code = Number(error?.code ?? error?.response?.data?.code)
  return code === RECALL_ERROR.ALREADY
}

// 增量同步游标：撤回只可能发生在消息发出后 2 分钟内，所以把 after 游标回退到
// “最新消息时间 - 回退窗口”之前的最后一条已确认消息。这样窗口内的消息会被重新拉取，
// 即使撤回事件在断线期间丢失，也能从历史接口拿到 recalled=true 的最新状态。
export function recallSafeSyncCursor(messages, lookbackMs = RECALL_SYNC_LOOKBACK_MS) {
  const list = Array.isArray(messages) ? messages : []
  let latest = NaN
  for (let i = list.length - 1; i >= 0; i--) {
    if (isPendingMessage(list[i])) continue
    const t = toTimestamp(list[i]?.created_at)
    if (Number.isFinite(t)) { latest = t; break }
  }
  if (!Number.isFinite(latest)) return null
  const boundary = latest - lookbackMs
  for (let i = list.length - 1; i >= 0; i--) {
    const m = list[i]
    if (isPendingMessage(m) || !m?.id) continue
    const t = toTimestamp(m.created_at)
    if (Number.isFinite(t) && t <= boundary) return m.id
  }
  return null
}
