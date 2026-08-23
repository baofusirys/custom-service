import test from 'node:test'
import assert from 'node:assert/strict'

import { MessageStatusSync, normalizeMessageStatusEvent } from '../src/modules/messageStatusSync.js'

function deterministicCrypto(start = 0) {
  let seed = start
  return {
    getRandomValues(bytes) {
      for (let i = 0; i < bytes.length; i++) bytes[i] = (seed + i) & 0xff
      seed++
      return bytes
    },
  }
}

test('跨标签页状态事件拒绝过期、错账号、非法状态和非法消息 ID', () => {
  const base = {
    v: 1,
    event_id: 'm2-0123456789abcdef0123456789abcdef',
    agent_id: '7',
    source_tab: 'tab-a',
    id: 'local-1724312345678',
    client_id: 'local-1724312345678',
    status: 'persisted',
    ts: Date.now(),
  }
  assert.ok(normalizeMessageStatusEvent(base, '7'))
  assert.equal(normalizeMessageStatusEvent({ ...base, agent_id: '8' }, '7'), null)
  assert.equal(normalizeMessageStatusEvent({ ...base, status: 'sending' }, '7'), null)
  assert.equal(normalizeMessageStatusEvent({ ...base, id: '../bad', client_id: '' }, '7'), null)
  assert.equal(normalizeMessageStatusEvent({ ...base, ts: Date.now() - 6 * 60 * 1000 }, '7'), null)
})

test('双通道重复事件只消费一次且不回送消息正文', () => {
  const received = []
  const storageWrites = []
  const sync = new MessageStatusSync({
    agentID: '7',
    onEvent: event => received.push(event),
    cryptoSource: deterministicCrypto(),
    windowSource: { addEventListener() {}, removeEventListener() {} },
    storage: { setItem: (key, value) => storageWrites.push([key, value]) },
    BroadcastChannelSource: undefined,
  })
  const raw = {
    v: 1,
    event_id: 'm2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    agent_id: '7',
    source_tab: 'm2-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    conv: 'conv-a',
    id: 'm2-cccccccccccccccccccccccccccccccc',
    client_id: 'local-1724312345678',
    status: 'delivered',
    ts: Date.now(),
  }
  assert.equal(sync.receive(raw), true)
  assert.equal(sync.receive(raw), false)
  assert.equal(received.length, 1)

  assert.equal(sync.publish({ ...raw, content: '不得广播的正文' }, 'persisted', 'conv-a'), true)
  const payload = JSON.parse(storageWrites.at(-1)[1])
  assert.equal('content' in payload, false)
  assert.equal(payload.agent_id, '7')
})
