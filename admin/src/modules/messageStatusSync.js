import { createClientMessageId, messageIdentityKeys } from './messageState.js'

const CONFIRMED_STATUSES = new Set(['persisted', 'delivered', 'read'])
const EVENT_TTL_MS = 5 * 60 * 1000
const MAX_SEEN_EVENTS = 512

export function normalizeMessageStatusEvent(value, expectedAgentID = '') {
  if (!value || typeof value !== 'object' || value.v !== 1) return null
  const agentID = String(value.agent_id || '')
  if (!agentID || (expectedAgentID && agentID !== String(expectedAgentID))) return null
  if (!CONFIRMED_STATUSES.has(value.status)) return null
  if (!messageIdentityKeys(value).length) return null
  const ts = Number(value.ts)
  if (!Number.isFinite(ts) || Math.abs(Date.now() - ts) > EVENT_TTL_MS) return null
  const eventID = String(value.event_id || '')
  if (!messageIdentityKeys({ id: eventID }).length) return null
  return {
    v: 1,
    event_id: eventID,
    agent_id: agentID,
    source_tab: String(value.source_tab || ''),
    conv: String(value.conv || ''),
    id: String(value.id || ''),
    client_id: String(value.client_id || ''),
    status: value.status,
    ts,
  }
}

// 只同步消息 ID 与单调状态，不跨标签页传播正文、附件或 token。
// BroadcastChannel 是主通道，storage event 是旧浏览器兜底；event_id 去重两路重复投递。
export class MessageStatusSync {
  constructor({ agentID, onEvent, cryptoSource = globalThis.crypto, windowSource = globalThis.window,
    storage = globalThis.localStorage, BroadcastChannelSource = globalThis.BroadcastChannel } = {}) {
    this.agentID = String(agentID || '')
    this.onEvent = onEvent
    this.cryptoSource = cryptoSource
    this.windowSource = windowSource
    this.storage = storage
    this.BroadcastChannelSource = BroadcastChannelSource
    this.channel = null
    this.seen = new Map()
    this.sourceTab = createClientMessageId(cryptoSource)
    this.channelName = `cs-message-status:${this.agentID}`
    this.storageKey = `cs_message_status_event:${this.agentID}`
    this.handleStorage = this.handleStorage.bind(this)
  }

  start() {
    if (!this.agentID) return
    if (typeof this.BroadcastChannelSource === 'function') {
      this.channel = new this.BroadcastChannelSource(this.channelName)
      this.channel.onmessage = event => this.receive(event?.data)
    }
    this.windowSource?.addEventListener?.('storage', this.handleStorage)
  }

  stop() {
    this.windowSource?.removeEventListener?.('storage', this.handleStorage)
    if (this.channel) {
      this.channel.close()
      this.channel = null
    }
    this.seen.clear()
  }

  handleStorage(event) {
    if (event?.key !== this.storageKey || !event.newValue) return
    try { this.receive(JSON.parse(event.newValue)) } catch {}
  }

  remember(eventID, now = Date.now()) {
    this.seen.set(eventID, now)
    if (this.seen.size <= MAX_SEEN_EVENTS) return
    for (const [id, createdAt] of this.seen) {
      if (now - createdAt > EVENT_TTL_MS || this.seen.size > MAX_SEEN_EVENTS) this.seen.delete(id)
    }
  }

  receive(raw) {
    const event = normalizeMessageStatusEvent(raw, this.agentID)
    if (!event || event.source_tab === this.sourceTab || this.seen.has(event.event_id)) return false
    this.remember(event.event_id)
    this.onEvent?.(event)
    return true
  }

  publish(receipt, status, conv = '') {
    if (!CONFIRMED_STATUSES.has(status)) return false
    const keys = messageIdentityKeys(receipt)
    if (!keys.length) return false
    const event = normalizeMessageStatusEvent({
      v: 1,
      event_id: createClientMessageId(this.cryptoSource),
      agent_id: this.agentID,
      source_tab: this.sourceTab,
      conv,
      id: receipt?.id || keys[0],
      client_id: receipt?.client_id || receipt?.extra?.client_id || '',
      status,
      ts: Date.now(),
    }, this.agentID)
    if (!event) return false
    this.remember(event.event_id, event.ts)
    try { this.channel?.postMessage(event) } catch {}
    try { this.storage?.setItem?.(this.storageKey, JSON.stringify(event)) } catch {}
    return true
  }
}
