import test from 'node:test'
import assert from 'node:assert/strict'

import { HALL_MAX_VIDEO_TILES, diffSubscriptions, selectVideoTiles } from '../src/modules/hallVideo.js'

const c = (identity, extra = {}) => ({ identity, isLocal: false, role: identity.startsWith('a_') ? 'agent' : 'visitor', speaking: false, publishedAt: 0, ...extra })

test('最多 4 路，自己的预览第一位，客服与正在说话的优先', () => {
  assert.equal(HALL_MAX_VIDEO_TILES, 4)
  const list = [c('v_1', { publishedAt: 1 }), c('v_2', { publishedAt: 2, speaking: true }), c('a_9', { publishedAt: 3 }), c('v_3', { publishedAt: 0.5 }), c('v_4', { publishedAt: 4 }), c('a_1', { isLocal: true })]
  assert.deepEqual(selectVideoTiles(list), ['a_1', 'a_9', 'v_2', 'v_3'])
})

test('粘性选择：已显示的画面只要还在发布就保持，离开后再补位', () => {
  const list = [c('v_1', { publishedAt: 1 }), c('v_2', { publishedAt: 2 }), c('v_3', { publishedAt: 3 }), c('v_4', { publishedAt: 4 }), c('v_5', { publishedAt: 5, speaking: true })]
  const first = selectVideoTiles(list.slice(0, 4))
  assert.deepEqual(first, ['v_1', 'v_2', 'v_3', 'v_4'])
  // v_5 正在说话，但已显示的 4 路不被挤掉
  assert.deepEqual(selectVideoTiles(list, { previous: first }), first)
  // v_2 停止发布 → v_5 补位
  const next = selectVideoTiles(list.filter(x => x.identity !== 'v_2'), { previous: first })
  assert.deepEqual(next, ['v_1', 'v_3', 'v_4', 'v_5'])
})

test('异常输入与上限 0', () => {
  assert.deepEqual(selectVideoTiles(null), [])
  assert.deepEqual(selectVideoTiles([null, {}, c('v_1')], { max: 0 }), [])
  assert.deepEqual(selectVideoTiles([c('v_1'), c('v_1')]), ['v_1'])
})

test('订阅差异：排除本地身份', () => {
  assert.deepEqual(diffSubscriptions(['v_1', 'v_2'], ['a_1', 'v_2', 'v_3'], 'a_1'), { subscribe: ['v_3'], unsubscribe: ['v_1'] })
  assert.deepEqual(diffSubscriptions(null, null), { subscribe: [], unsubscribe: [] })
})
