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

const MESSAGE_ID_PATTERN = /^[A-Za-z0-9-]{1,36}$/

function validIdentity(value) {
  const identity = typeof value === 'string' ? value : ''
  return MESSAGE_ID_PATTERN.test(identity) ? identity : ''
}

// 同一消息在旧离线队列、重发帧、服务端落库后可能依次拥有 local-*、m2-* 两个 ID。
// 所有 reducer / HTTP 对账 / WSS 回执都必须走这一份身份集合，不能再只比较 message.id。
export function messageIdentityKeys(value) {
  if (!value || typeof value !== 'object') return []
  const extra = value.extra && typeof value.extra === 'object' ? value.extra : {}
  const aliases = Array.isArray(value.id_aliases) ? value.id_aliases : []
  const keys = [
    value.id,
    value.client_id,
    value.local_id,
    value.server_id,
    extra.client_id,
    extra.local_id,
    extra.server_id,
    ...aliases,
  ].map(validIdentity).filter(Boolean)
  return [...new Set(keys)]
}

function sameMessageIdentity(left, right) {
  const rightKeys = new Set(messageIdentityKeys(right))
  return messageIdentityKeys(left).some(key => rightKeys.has(key))
}

function isLegacyLocalID(id) {
  return String(id || '').startsWith('local-')
}

function mergeIdentity(target, source) {
  const before = messageIdentityKeys(target)
  const sourceKeys = messageIdentityKeys(source)
  const oldID = validIdentity(target.id)
  const incomingID = validIdentity(source?.id)
  const correlationID = validIdentity(source?.client_id) || validIdentity(source?.extra?.client_id)

  // HTTP/WSS 服务端对象的稳定 ID 优先；但绝不让迟到的 local-* 把已经确认的 m2-* 倒退。
  if (incomingID && (!oldID || isLegacyLocalID(oldID) || !isLegacyLocalID(incomingID))) {
    if (!(isLegacyLocalID(incomingID) && oldID && !isLegacyLocalID(oldID))) target.id = incomingID
  }
  if (!target.client_id) {
    target.client_id = correlationID || (oldID && oldID !== target.id ? oldID : '') || target.id
  }
  const all = [...new Set([...before, ...sourceKeys, ...messageIdentityKeys(target)])]
  if (all.length > 1) target.id_aliases = all
  return all
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
  if (current === nextStatus || (current === 'persisted' && nextStatus === 'sent')) return false
  if (current === 'failed' || nextRank > currentRank) {
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
  mergeIdentity(merged, { ...incoming, id_aliases: [...messageIdentityKeys(current), ...messageIdentityKeys(incoming)] })
  return merged
}

// ACK / delivery / 跨标签页状态事件的唯一 reducer。返回命中的全部别名，供调用方
// 同时清理 local-* 与 m2-* 两套超时定时器；重复、迟到、乱序回执均保持幂等单调。
export function applyMessageReceipt(messages, receipt, nextStatus) {
  const list = Array.isArray(messages) ? messages : []
  const receiptConv = String(receipt?.conv || receipt?.conv_id || '')
  const target = list.find(message => {
    const messageConv = String(message?.conv_id || '')
    return (!receiptConv || !messageConv || receiptConv === messageConv) && sameMessageIdentity(message, receipt)
  })
  if (!target) return { matched: false, changed: false, message: null, identities: messageIdentityKeys(receipt) }
  const identities = mergeIdentity(target, receipt)
  const status = nextStatus || receipt?.status || receipt?.extra?.status
  const changed = status ? advanceMessageStatus(target, status) : false
  return { matched: true, changed, message: target, identities }
}

export function mergeMessageLists(current, incoming, replace = false) {
  const existing = Array.isArray(current) ? current : []
  const fetched = Array.isArray(incoming) ? incoming : []
  const pending = existing.filter(isPendingMessage).map(message => ({ ...message }))
  const confirmed = []

  const upsertConfirmed = (message) => {
    const index = confirmed.findIndex(currentMessage => sameMessageIdentity(currentMessage, message))
    if (index >= 0) confirmed[index] = mergeSameMessage(confirmed[index], message)
    else confirmed.push(message)
  }

  if (!replace) {
    for (const message of existing) {
      if (!isPendingMessage(message) && message?.id) upsertConfirmed({ ...message })
    }
  }

  for (const message of fetched) {
    if (!message?.id) continue
    let normalized = { ...message, status: canonicalMessageStatus(message) }
    let pendingIndex = pending.findIndex(local => sameMessageIdentity(local, normalized))

    // 只给没有显式别名的旧 local-* 做内容/媒体/会话/时间兜底，并严格一对一消费，
    // 防止连续发送相同文本时一个服务端消息吞掉多条本地气泡。
    if (pendingIndex < 0 && normalized.sender === 'agent') {
      let bestDistance = Number.POSITIVE_INFINITY
      for (let i = 0; i < pending.length; i++) {
        const local = pending[i]
        if (!isLegacyLocalID(local?.id)) continue
        if ((local.content || '') !== (normalized.content || '')) continue
        if (mediaURLOf(local) !== mediaURLOf(normalized)) continue
        if (local.conv_id && normalized.conv_id && local.conv_id !== normalized.conv_id) continue
        if (local.sender_ref && normalized.sender_ref && String(local.sender_ref) !== String(normalized.sender_ref)) continue
        const distance = Math.abs(timeOf(normalized) - timeOf(local))
        if (distance <= 120000 && distance < bestDistance) {
          pendingIndex = i
          bestDistance = distance
        }
      }
    }
    if (pendingIndex >= 0) {
      const local = pending.splice(pendingIndex, 1)[0]
      normalized = mergeSameMessage(local, normalized)
    }
    upsertConfirmed(normalized)
  }

  return [...confirmed, ...pending].sort((a, b) => timeOf(a) - timeOf(b))
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
