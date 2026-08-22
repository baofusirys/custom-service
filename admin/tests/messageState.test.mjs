import test from 'node:test'
import assert from 'node:assert/strict'

import {
  advanceMessageStatus,
  canonicalMessageStatus,
  createClientMessageId,
  isPendingMessage,
  latestServerMessageId,
  mergeMessageLists,
} from '../src/modules/messageState.js'

test('密码学消息 ID 适配现有 CHAR(36) 且不同标签页不碰撞', () => {
  let seed = 0
  const cryptoSource = {
    getRandomValues(bytes) {
      for (let i = 0; i < bytes.length; i++) bytes[i] = (seed + i) & 0xff
      seed++
      return bytes
    },
  }
  const first = createClientMessageId(cryptoSource)
  const second = createClientMessageId(cryptoSource)
  assert.match(first, /^m2-[a-f0-9]{32}$/)
  assert.ok(first.length <= 36)
  assert.notEqual(first, second)
})

test('HTTP 合并按稳定 ID 原子替换并保留未确认消息', () => {
  const current = [
    { id: 'm2-a', sender: 'visitor', status: 'persisted', created_at: '2026-08-22T10:00:00+08:00' },
    { id: 'm2-b', sender: 'agent', status: 'sending', created_at: '2026-08-22T10:00:01+08:00' },
  ]
  const incoming = [
    { id: 'm2-a', sender: 'visitor', status: 'persisted', created_at: '2026-08-22T10:00:00+08:00' },
    { id: 'm2-c', sender: 'visitor', status: 'persisted', created_at: '2026-08-22T10:00:02+08:00' },
  ]
  assert.deepEqual(mergeMessageLists(current, incoming, true).map(m => m.id), ['m2-a', 'm2-b', 'm2-c'])
})

test('ACK 乱序时状态只能向 persisted、delivered、read 前进', () => {
  const message = { id: 'm2-a', status: 'sending' }
  assert.equal(advanceMessageStatus(message, 'persisted'), true)
  assert.equal(advanceMessageStatus(message, 'delivered'), true)
  assert.equal(advanceMessageStatus(message, 'persisted'), false)
  assert.equal(advanceMessageStatus(message, 'read'), true)
  assert.equal(advanceMessageStatus(message, 'delivered'), false)
  assert.equal(advanceMessageStatus(message, 'failed'), false)
  assert.equal(canonicalMessageStatus(message), 'read')
})

test('迟到 ACK 可以纠正本地超时，但游标绝不使用 pending 消息', () => {
  const failed = { id: 'm2-b', status: 'failed' }
  assert.equal(isPendingMessage(failed), true)
  assert.equal(latestServerMessageId([{ id: 'm2-a', status: 'persisted' }, failed]), 'm2-a')
  assert.equal(advanceMessageStatus(failed, 'persisted'), true)
  assert.equal(latestServerMessageId([{ id: 'm2-a', status: 'persisted' }, failed]), 'm2-b')
})

test('旧 delivered_ws 假阳性不会被当成新协议已送达', () => {
  assert.equal(canonicalMessageStatus({ id: 'local-123', delivered_ws: true }), 'persisted')
  assert.equal(canonicalMessageStatus({ id: 'm2-123', delivered_ws: true }), 'delivered')
})
