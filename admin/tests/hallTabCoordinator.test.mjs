import test from 'node:test'
import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'

import { HallTabCoordinator, normalizeTabClaim } from '../src/modules/hallTabCoordinator.js'

// 模拟同源多标签页共享的 BroadcastChannel（不回送给自己，与浏览器行为一致）
function fakeBus() {
  const channels = new Set()
  class FakeChannel {
    constructor(name) { this.name = name; this.onmessage = null; channels.add(this) }
    postMessage(data) {
      for (const ch of channels) if (ch !== this && ch.name === this.name) ch.onmessage?.({ data: structuredClone(data) })
    }
    close() { channels.delete(this) }
  }
  return { FakeChannel, size: () => channels.size }
}

const memStorage = () => { const m = new Map(); return { setItem: (k, v) => m.set(k, String(v)), getItem: k => m.get(k) ?? null } }

test('后开的标签页接管：其它同账号标签页收到通知，自己与其它账号不受影响', () => {
  const bus = fakeBus()
  const events = { a: 0, b: 0, other: 0 }
  const mk = (agentID, key) => new HallTabCoordinator({
    agentID, onTakeover: () => events[key]++, cryptoSource: webcrypto, windowSource: null, storage: memStorage(), BroadcastChannelSource: bus.FakeChannel,
  })
  const a = mk('7', 'a'); const b = mk('7', 'b'); const other = mk('8', 'other')
  a.start(); b.start(); other.start()
  b.claim()
  assert.deepEqual(events, { a: 1, b: 0, other: 0 })
  a.claim()
  assert.deepEqual(events, { a: 1, b: 1, other: 0 })
  a.stop(); b.stop(); other.stop()
  assert.equal(bus.size(), 0)
})

test('storage 兜底通道与重复事件去重', () => {
  let n = 0
  const listeners = []
  const win = { addEventListener: (t, fn) => listeners.push(fn), removeEventListener: () => {} }
  // 传 null 表示浏览器不支持 BroadcastChannel（undefined 会触发默认参数取到 Node 的真实实现）
  const c = new HallTabCoordinator({ agentID: '7', onTakeover: () => n++, cryptoSource: webcrypto, windowSource: win, storage: memStorage(), BroadcastChannelSource: null })
  c.start()
  const claim = { v: 1, type: 'hall_claim', agent_id: '7', tab: 'a'.repeat(32), event_id: 'b'.repeat(32), ts: Date.now() }
  listeners[0]({ key: 'cs_hall_owner:7', newValue: JSON.stringify(claim) })
  listeners[0]({ key: 'cs_hall_owner:7', newValue: JSON.stringify(claim) })
  listeners[0]({ key: 'other', newValue: JSON.stringify(claim) })
  listeners[0]({ key: 'cs_hall_owner:7', newValue: '{bad json' })
  assert.equal(n, 1)
  c.stop()
})

test('占用声明校验：版本、类型、账号、随机 ID 格式、30 秒时效', () => {
  const now = 1_000_000
  const ok = { v: 1, type: 'hall_claim', agent_id: '7', tab: 'a'.repeat(32), event_id: 'b'.repeat(32), ts: now }
  assert.ok(normalizeTabClaim(ok, '7', now))
  assert.equal(normalizeTabClaim({ ...ok, agent_id: '8' }, '7', now), null)
  assert.equal(normalizeTabClaim({ ...ok, v: 2 }, '7', now), null)
  assert.equal(normalizeTabClaim({ ...ok, type: 'x' }, '7', now), null)
  assert.equal(normalizeTabClaim({ ...ok, tab: 'x' }, '7', now), null)
  assert.equal(normalizeTabClaim({ ...ok, ts: now - 31_000 }, '7', now), null)
  assert.equal(normalizeTabClaim(null, '7', now), null)
})

test('无密码学随机源时拒绝创建（由页面降级为 LiveKit 重复身份兜底）', () => {
  assert.throws(() => new HallTabCoordinator({ agentID: '7', cryptoSource: null }))
})
