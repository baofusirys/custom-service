import test from 'node:test'
import assert from 'node:assert/strict'

import {
  RECALL_WINDOW_MS,
  applyRecallToList,
  canRecallMessage,
  recallFailureMeansAlreadyRecalled,
  recallPlaceholder,
  recallSafeSyncCursor,
} from '../src/modules/recall.js'
import { mergeMessageLists } from '../src/modules/messageState.js'

const NOW = Date.parse('2026-10-14T12:00:00Z')
const mine = (over = {}) => ({
  id: 'm2-0123456789abcdef0123456789abcdef',
  sender: 'agent',
  sender_ref: '7',
  status: 'persisted',
  content: '您好',
  created_at: new Date(NOW - 60 * 1000).toISOString(),
  ...over,
})

test('撤回时限：只允许自己 2 分钟内已落库的消息', () => {
  assert.equal(canRecallMessage(mine(), 7, NOW), true)
  assert.equal(canRecallMessage(mine({ created_at: new Date(NOW - RECALL_WINDOW_MS + 1).toISOString() }), '7', NOW), true)
  assert.equal(canRecallMessage(mine({ created_at: new Date(NOW - RECALL_WINDOW_MS).toISOString() }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ sender_ref: '8' }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ sender: 'visitor' }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ status: 'sending' }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ status: 'failed' }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ recalled: true }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ id: 'local-1724312345678' }), '7', NOW), false)
  assert.equal(canRecallMessage(mine({ created_at: 'bad' }), '7', NOW), false)
  assert.equal(canRecallMessage(mine(), undefined, NOW), false)
  // 本地时钟比服务端慢：消息时间在“未来” 10 秒内仍可撤回，超出 30 秒视为异常
  assert.equal(canRecallMessage(mine({ created_at: new Date(NOW + 10 * 1000).toISOString() }), '7', NOW), true)
  assert.equal(canRecallMessage(mine({ created_at: new Date(NOW + 60 * 1000).toISOString() }), '7', NOW), false)
})

test('撤回占位文案：自己撤回显示“你”，其他客服显示“客服”', () => {
  assert.equal(recallPlaceholder(mine(), 7), '你撤回了一条消息')
  assert.equal(recallPlaceholder(mine({ sender_ref: '9' }), 7), '客服撤回了一条消息')
  assert.equal(recallPlaceholder(mine(), undefined), '客服撤回了一条消息')
})

test('recall 事件按任一别名命中并清空正文与附件，重复事件幂等', () => {
  const list = [mine({ id: 'm2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', id_aliases: ['local-1', 'm2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'], media_url: { String: '/files/a.png', Valid: true } })]
  const first = applyRecallToList(list, 'local-1')
  assert.equal(first.matched, true)
  assert.equal(first.changed, true)
  assert.equal(list[0].recalled, true)
  assert.equal(list[0].content, '')
  assert.equal(list[0].media_url, null)
  assert.equal(applyRecallToList(list, 'm2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa').changed, false)
  assert.equal(applyRecallToList(list, '../evil').matched, false)
  assert.equal(applyRecallToList(null, 'm2-x').matched, false)
})

test('撤回失败码 40911（已撤回）需要同步本地状态', () => {
  assert.equal(recallFailureMeansAlreadyRecalled({ code: 40911 }), true)
  assert.equal(recallFailureMeansAlreadyRecalled({ response: { data: { code: 40911 } } }), true)
  assert.equal(recallFailureMeansAlreadyRecalled({ response: { data: { code: 40910 } } }), false)
})

test('历史接口 recalled=true 与迟到重放合并后保持撤回终态，不复活正文', () => {
  const cached = [mine({ id: 'm2-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' })]
  const fromHistory = [{ ...cached[0], recalled: true, content: '' }]
  const merged = mergeMessageLists(cached, fromHistory, false)
  assert.equal(merged[0].recalled, true)
  assert.equal(merged[0].content, '')
  // 迟到的实时重放仍带正文，也不得覆盖已撤回状态
  const replay = mergeMessageLists(merged, [{ ...cached[0], content: '您好' }], false)
  assert.equal(replay.length, 1)
  assert.equal(replay[0].recalled, true)
  assert.equal(replay[0].content, '')
  // 服务端异常返回 recalled=true 但带正文：前端同样清空
  const dirty = mergeMessageLists([], [{ ...cached[0], recalled: true, content: '残留' }], true)
  assert.equal(dirty[0].content, '')
})

test('增量同步游标回退一个撤回窗口，窗口内消息会被重新拉取', () => {
  const at = (sec) => new Date(NOW + sec * 1000).toISOString()
  const list = [
    { id: 'm2-old', status: 'persisted', created_at: at(-600) },
    { id: 'm2-mid', status: 'persisted', created_at: at(-200) },
    { id: 'm2-new1', status: 'persisted', created_at: at(-100) },
    { id: 'm2-new2', status: 'persisted', created_at: at(0) },
    { id: 'm2-pending', status: 'sending', created_at: at(5) },
  ]
  assert.equal(recallSafeSyncCursor(list), 'm2-mid')
  assert.equal(recallSafeSyncCursor(list.slice(2)), null)
  assert.equal(recallSafeSyncCursor([]), null)
})
