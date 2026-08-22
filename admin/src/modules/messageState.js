// 客服消息状态与缓存合并的唯一实现。
// 纯函数模块便于对慢网、乱序 ACK、多标签页 ID 冲突等场景做自动化测试。

const STATE_RANK = Object.freeze({
  failed: -1,
  sending: 0,
  persisted: 1,
  sent: 1, // 兼容 [079] 旧缓存；语义统一为 persisted（已写入数据库）
  delivered: 2,
  read: 3,
})

export function createClientMessageId(cryptoSource = globalThis.crypto) {
  if (!cryptoSource) throw new Error('当前浏览器不支持密码学随机数，拒绝生成消息 ID')
  let hex = ''
  if (typeof cryptoSource.randomUUID === 'function') {
    hex = cryptoSource.randomUUID().replaceAll('-', '').toLowerCase()
  } else if (typeof cryptoSource.getRandomValues === 'function') {
    const bytes = new Uint8Array(16)
    cryptoSource.getRandomValues(bytes)
    hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
  }
  if (!/^[a-f0-9]{32}$/.test(hex)) throw new Error('密码学随机消息 ID 生成失败')
  // messages.id 当前为 CHAR(36)；m2- + 32 hex = 35 字符，兼容现有 schema。
  return `m2-${hex}`
}

export function canonicalMessageStatus(message) {
  if (!message) return ''
  if (message.read === true || message.status === 'read') return 'read'
  if (message.status === 'delivered') return 'delivered'
  if (message.status === 'persisted' || message.status === 'sent') return 'persisted'
  if (message.status === 'sending' || message.status === 'failed') return message.status
  // 旧 delivered_ws 曾存在假阳性；只信新协议 m2-* 消息的服务端送达标记。
  if (String(message.id || '').startsWith('m2-') && message.delivered_ws === true) return 'delivered'
  return 'persisted'
}

export function advanceMessageStatus(message, nextStatus) {
  if (!message || !(nextStatus in STATE_RANK)) return false
  const current = canonicalMessageStatus(message)
  const currentRank = STATE_RANK[current] ?? -2
  const nextRank = STATE_RANK[nextStatus]
  // 超时/错误只能把尚未确认的 sending 标失败；迟到 error 不得覆盖已落库/送达/已读证据。
  if (nextStatus === 'failed') {
    if (current !== 'sending' && current !== 'failed') return false
    message.status = 'failed'
    return true
  }
  // 服务端迟到的 persisted/delivered/read 可以纠正本地超时 failed；已确认状态绝不回退。
  if (current === 'failed' || nextRank >= currentRank) {
    message.status = nextStatus === 'sent' ? 'persisted' : nextStatus
    if (message.status === 'read') message.read = true
    if (message.status === 'delivered' || message.status === 'read') message.delivered_ws = true
    return true
  }
  return false
}

export function isPendingMessage(message) {
  const status = canonicalMessageStatus(message)
  return status === 'sending' || status === 'failed'
}

export function latestServerMessageId(messages) {
  for (let i = (messages || []).length - 1; i >= 0; i--) {
    if (!isPendingMessage(messages[i])) return messages[i].id || null
  }
  return null
}

function timeOf(message) {
  const value = new Date(message?.created_at || 0).getTime()
  return Number.isFinite(value) ? value : 0
}

function mediaURLOf(message) {
  const value = message?.media_url
  return value && typeof value === 'object' ? (value.String || '') : (value || '')
}

function mergeSameMessage(current, incoming) {
  const merged = { ...current, ...incoming }
  const currentStatus = canonicalMessageStatus(current)
  const incomingStatus = canonicalMessageStatus(incoming)
  merged.status = STATE_RANK[currentStatus] > STATE_RANK[incomingStatus] ? currentStatus : incomingStatus
  if (current?.read || incoming?.read || merged.status === 'read') merged.read = true
  return merged
}

export function mergeMessageLists(current, incoming, replace = false) {
  const existing = Array.isArray(current) ? current : []
  const fetched = Array.isArray(incoming) ? incoming : []
  const pending = existing.filter(isPendingMessage)
  const byID = new Map()

  if (!replace) {
    for (const message of existing) {
      if (!isPendingMessage(message) && message?.id) byID.set(message.id, { ...message })
    }
  }
  for (const message of fetched) {
    if (!message?.id) continue
    const normalized = { ...message, status: canonicalMessageStatus(message) }
    byID.set(message.id, byID.has(message.id) ? mergeSameMessage(byID.get(message.id), normalized) : normalized)
  }

  const serverMessages = [...byID.values()].sort((a, b) => timeOf(a) - timeOf(b))
  const stillPending = pending.filter(local => {
    if (local?.id && byID.has(local.id)) return false
    // 仅兼容旧 local-* 客户端；新协议始终按稳定随机 ID 幂等合并。
    if (!String(local?.id || '').startsWith('local-')) return true
    return !serverMessages.some(server =>
      server.sender === 'agent' &&
      (server.content || '') === (local.content || '') &&
      mediaURLOf(server) === mediaURLOf(local) &&
      Math.abs(timeOf(server) - timeOf(local)) <= 120000)
  })
  return [...serverMessages, ...stillPending].sort((a, b) => timeOf(a) - timeOf(b))
}

export function messageStatusLabel(message) {
  switch (canonicalMessageStatus(message)) {
    case 'sending': return '发送中…'
    case 'failed': return '未发送 · 点击重发'
    case 'read': return '已读'
    case 'delivered': return '已送达'
    default: return '已发送'
  }
}
