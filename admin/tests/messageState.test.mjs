import test from 'node:test'
import assert from 'node:assert/strict'

import {
  applyMessageReceipt,
  advanceMessageStatus,
  canonicalMessageStatus,
  createClientMessageId,
  isPendingMessage,
  latestServerMessageId,
  messageIdentityKeys,
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

test('离线 local 临时 ID 可按 client_id 与服务端稳定 ID 原子归并', () => {
  const local = {
    id: 'local-1724312345678',
    sender: 'agent',
    sender_ref: '7',
    conv_id: 'conv-a',
    content: '断线期间发送',
    status: 'sending',
    created_at: '2026-08-23T09:00:00+08:00',
  }
  const persisted = {
    id: 'm2-0123456789abcdef0123456789abcdef',
    client_id: 'local-1724312345678',
    sender: 'agent',
    sender_ref: '7',
    conv_id: 'conv-a',
    content: '断线期间发送',
    status: 'persisted',
    created_at: '2026-08-23T09:00:00.009+08:00',
  }

  const merged = mergeMessageLists([local], [persisted], false)
  assert.equal(merged.length, 1)
  assert.equal(merged[0].id, persisted.id)
  assert.equal(merged[0].client_id, local.id)
  assert.equal(merged[0].status, 'persisted')
  assert.deepEqual(new Set(messageIdentityKeys(merged[0])), new Set([local.id, persisted.id]))
})

test('ACK 可按临时 ID、稳定 ID、extra.client_id 幂等归并且状态不回退', () => {
  const messages = [{
    id: 'local-1724312345678',
    client_id: 'local-1724312345678',
    status: 'sending',
  }]
  const canonicalID = 'm2-0123456789abcdef0123456789abcdef'

  const first = applyMessageReceipt(messages, {
    id: canonicalID,
    extra: { client_id: 'local-1724312345678', status: 'persisted' },
  }, 'persisted')
  assert.equal(first.matched, true)
  assert.equal(messages[0].id, canonicalID)
  assert.equal(messages[0].status, 'persisted')

  assert.equal(applyMessageReceipt(messages, { id: 'local-1724312345678' }, 'persisted').matched, true)
  assert.equal(applyMessageReceipt(messages, { id: canonicalID }, 'delivered').matched, true)
  assert.equal(applyMessageReceipt(messages, { id: canonicalID }, 'persisted').changed, false)
  assert.equal(applyMessageReceipt(messages, { id: 'local-1724312345678' }, 'failed').changed, false)
  assert.equal(messages[0].status, 'delivered')
})

test('缓存重载后 ID 别名仍可恢复 ACK，且相同内容只一对一归并', () => {
  const reloaded = JSON.parse(JSON.stringify([{
    id: 'm2-0123456789abcdef0123456789abcdef',
    client_id: 'local-1724312345678',
    id_aliases: ['local-1724312345678', 'm2-0123456789abcdef0123456789abcdef'],
    status: 'sending',
  }]))
  assert.equal(applyMessageReceipt(reloaded, { id: 'local-1724312345678' }, 'persisted').matched, true)
  assert.equal(reloaded[0].status, 'persisted')

  const pending = [1, 2].map(i => ({
    id: `local-${i}`,
    sender: 'agent',
    content: '重复文本',
    status: 'sending',
    created_at: `2026-08-23T09:00:0${i}+08:00`,
  }))
  const server = [{
    id: 'm2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    sender: 'agent',
    content: '重复文本',
    status: 'persisted',
    created_at: '2026-08-23T09:00:01.010+08:00',
  }]
  const merged = mergeMessageLists(pending, server, false)
  assert.equal(merged.length, 2)
  assert.equal(merged.filter(isPendingMessage).length, 1)
})
