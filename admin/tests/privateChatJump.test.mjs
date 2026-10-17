import test from 'node:test'
import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'

import { JUMP_TICKET_TTL_MS, consumeJumpTicket, createJumpTicket, isValidJumpNonce, pruneJumpTickets } from '../src/modules/privateChatJump.js'

function memStorage() {
  const m = new Map()
  return {
    get length() { return m.size },
    key: (i) => Array.from(m.keys())[i] ?? null,
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
    dump: () => Object.fromEntries(m),
  }
}

test('票据一次性：读取后立即删除，URL 中只出现随机票据', () => {
  const storage = memStorage()
  const nonce = createJumpTicket('vid_abc-1', { storage, cryptoSource: webcrypto, now: 1000 })
  assert.equal(isValidJumpNonce(nonce), true)
  assert.ok(!nonce.includes('vid_abc'))
  assert.equal(consumeJumpTicket(nonce, { storage, now: 2000 }), 'vid_abc-1')
  assert.equal(consumeJumpTicket(nonce, { storage, now: 2000 }), '')
  assert.deepEqual(storage.dump(), {})
})

test('过期、伪造、篡改的票据一律无效', () => {
  const storage = memStorage()
  const nonce = createJumpTicket('vid1', { storage, cryptoSource: webcrypto, now: 0 })
  assert.equal(consumeJumpTicket(nonce, { storage, now: JUMP_TICKET_TTL_MS + 1 }), '')
  assert.equal(consumeJumpTicket('../../cs_admin_agent', { storage }), '')
  assert.equal(consumeJumpTicket('A'.repeat(32), { storage }), '')
  const n2 = createJumpTicket('vid2', { storage, cryptoSource: webcrypto, now: 0 })
  storage.setItem('cs_hall_jump:' + n2, JSON.stringify({ vid: '../x', exp: 1e15 }))
  assert.equal(consumeJumpTicket(n2, { storage, now: 1 }), '')
  storage.setItem('cs_hall_jump:' + 'c'.repeat(32), '{bad')
  assert.equal(consumeJumpTicket('c'.repeat(32), { storage, now: 1 }), '')
})

test('非法 vid 不生成票据；创建时清理过期票据，不碰其它键', () => {
  const storage = memStorage()
  storage.setItem('cs_admin_agent', '{}')
  storage.setItem('cs_hall_jump:' + 'd'.repeat(32), JSON.stringify({ vid: 'old', exp: 5 }))
  assert.equal(createJumpTicket('../x', { storage, cryptoSource: webcrypto, now: 10 }), '')
  assert.equal(createJumpTicket('', { storage, cryptoSource: webcrypto, now: 10 }), '')
  const nonce = createJumpTicket('vid3', { storage, cryptoSource: webcrypto, now: 10 })
  assert.ok(nonce)
  const keys = Object.keys(storage.dump())
  assert.ok(keys.includes('cs_admin_agent'))
  assert.ok(!keys.includes('cs_hall_jump:' + 'd'.repeat(32)))
  assert.equal(pruneJumpTickets(storage, 10 + JUMP_TICKET_TTL_MS + 1), 1)
  assert.equal(createJumpTicket('vid4', { storage, cryptoSource: null }), '')
})
