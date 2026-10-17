// 同账号多标签页进入大厅时「后开的接管」：新标签页进入前广播占用声明，其它标签页收到后主动退出大厅。
// BroadcastChannel 为主通道，storage 事件为旧浏览器兜底（与 messageStatusSync.js 同一模式）；
// 只广播账号 ID、标签页随机 ID 与时间，不跨标签页传播令牌或任何访客信息。
// 另外 LiveKit 本身对同一 identity 也会踢掉旧连接（DUPLICATE_IDENTITY），两条路径互为兜底。

const CLAIM_TTL_MS = 30 * 1000
const MAX_SEEN = 64
const TAB_ID = /^[a-f0-9]{32}$/

function randomHex(cryptoSource) {
  if (!cryptoSource || typeof cryptoSource.getRandomValues !== 'function') throw new Error('当前浏览器不支持密码学随机数')
  const b = new Uint8Array(16)
  cryptoSource.getRandomValues(b)
  return Array.from(b, x => x.toString(16).padStart(2, '0')).join('')
}

export function normalizeTabClaim(raw, agentID, now = Date.now()) {
  if (!raw || typeof raw !== 'object' || raw.v !== 1 || raw.type !== 'hall_claim') return null
  const agent = String(raw.agent_id || '')
  if (!agent || agent !== String(agentID || '')) return null
  if (typeof raw.tab !== 'string' || !TAB_ID.test(raw.tab)) return null
  if (typeof raw.event_id !== 'string' || !TAB_ID.test(raw.event_id)) return null
  const ts = Number(raw.ts)
  if (!Number.isFinite(ts) || Math.abs(now - ts) > CLAIM_TTL_MS) return null
  return { v: 1, type: 'hall_claim', agent_id: agent, tab: raw.tab, event_id: raw.event_id, ts }
}

export class HallTabCoordinator {
  constructor({ agentID, onTakeover, cryptoSource = globalThis.crypto, windowSource = globalThis.window,
    storage = globalThis.localStorage, BroadcastChannelSource = globalThis.BroadcastChannel, now = () => Date.now() } = {}) {
    this.agentID = String(agentID || '')
    this.onTakeover = onTakeover
    this.cryptoSource = cryptoSource
    this.windowSource = windowSource
    this.storage = storage
    this.BroadcastChannelSource = BroadcastChannelSource
    this.now = now
    this.tabId = randomHex(cryptoSource)
    this.channel = null
    this.seen = new Set()
    this.channelName = 'cs-hall-owner:' + this.agentID
    this.storageKey = 'cs_hall_owner:' + this.agentID
    this.handleStorage = this.handleStorage.bind(this)
    this.started = false
  }

  start() {
    if (!this.agentID || this.started) return
    this.started = true
    if (typeof this.BroadcastChannelSource === 'function') {
      try {
        this.channel = new this.BroadcastChannelSource(this.channelName)
        this.channel.onmessage = ev => this.receive(ev?.data)
      } catch {
        this.channel = null
      }
    }
    this.windowSource?.addEventListener?.('storage', this.handleStorage)
  }

  stop() {
    this.started = false
    this.windowSource?.removeEventListener?.('storage', this.handleStorage)
    if (this.channel) {
      try { this.channel.close() } catch {}
      this.channel = null
    }
    this.seen.clear()
  }

  handleStorage(ev) {
    if (ev?.key !== this.storageKey || !ev.newValue) return
    try { this.receive(JSON.parse(ev.newValue)) } catch {}
  }

  receive(raw) {
    const claim = normalizeTabClaim(raw, this.agentID, this.now())
    if (!claim || claim.tab === this.tabId || this.seen.has(claim.event_id)) return false
    this.seen.add(claim.event_id)
    if (this.seen.size > MAX_SEEN) this.seen = new Set([...this.seen].slice(-MAX_SEEN / 2))
    this.onTakeover?.(claim)
    return true
  }

  // 本标签页即将进入大厅：通知同账号其它标签页退出
  claim() {
    if (!this.agentID) return false
    const event = { v: 1, type: 'hall_claim', agent_id: this.agentID, tab: this.tabId, event_id: randomHex(this.cryptoSource), ts: this.now() }
    this.seen.add(event.event_id)
    try { this.channel?.postMessage(event) } catch {}
    try { this.storage?.setItem?.(this.storageKey, JSON.stringify(event)) } catch {}
    return true
  }
}
