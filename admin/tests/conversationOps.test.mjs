import test from 'node:test'
import assert from 'node:assert/strict'

import {
  BATCH_DELETE_MAX,
  BATCH_EXPORT_MAX,
  applyProfileToConversation,
  moveToTop,
  normalizeVisitorIds,
  removeVisitorsFromList,
  senderLabel,
  summarizeDeleteResult,
  validateBatchSelection,
  visitorDisplayName,
} from '../src/modules/conversationOps.js'
import { normalizeSearchKeyword, clampSearchLimit } from '../src/modules/chatSearch.js'
import { parseAutoCleanDays, serializeAutoCleanDays } from '../src/modules/autoClean.js'
import { CONFIRM_DELETE_ALL, CONFIRM_PURGE_RECYCLE, matchesConfirmText } from '../src/modules/dangerConfirm.js'
import { compactBeijingStamp, formatBeijing } from '../src/modules/timeFormat.js'

test('批量选择：去重并过滤非法 ID，删除上限 200、导出上限 50', () => {
  assert.deepEqual(normalizeVisitorIds(['a', 'a', '../x', '', 7, 'b_1']), ['a', 'b_1'])
  const ids = Array.from({ length: BATCH_DELETE_MAX + 1 }, (_, i) => 'v' + i)
  assert.equal(validateBatchSelection(ids, BATCH_DELETE_MAX).ok, false)
  assert.equal(validateBatchSelection(ids.slice(0, BATCH_DELETE_MAX), BATCH_DELETE_MAX).ok, true)
  assert.equal(validateBatchSelection(ids.slice(0, BATCH_EXPORT_MAX + 1), BATCH_EXPORT_MAX).ok, false)
  assert.equal(validateBatchSelection([], BATCH_EXPORT_MAX).ok, false)
})

test('conversations_deleted：按客户移除，不修改原数组，返回被移除项', () => {
  const list = [{ id: 'c1', visitor_id: 'v1' }, { id: 'c2', visitor_id: 'v2' }, { id: 'c3', visitor_id: 'v3' }]
  const r = removeVisitorsFromList(list, ['v2', 'v9', '<bad>'])
  assert.deepEqual(r.list.map(c => c.id), ['c1', 'c3'])
  assert.equal(r.removed, 1)
  assert.equal(r.removedItems[0].id, 'c2')
  assert.equal(list.length, 3)
  assert.equal(removeVisitorsFromList(list, []).list, list)
})

test('profile_updated 合并：标签/备注/置顶覆盖，手动未读时未读数至少为 1', () => {
  const conv = { visitor_id: 'v1', unread: 0, tags: ['旧'], pinned: false }
  applyProfileToConversation(conv, { note: '备注', tags: ['新'], pinned: true, marked_unread: true })
  assert.deepEqual(conv.tags, ['新'])
  assert.equal(conv.note, '备注')
  assert.equal(conv.pinned, true)
  assert.equal(conv.unread, 1)
  const busy = { visitor_id: 'v2', unread: 5 }
  applyProfileToConversation(busy, { marked_unread: true })
  assert.equal(busy.unread, 5)
  const list = [{ id: 1 }, { id: 2 }, { id: 3 }]
  moveToTop(list, list[2])
  assert.deepEqual(list.map(x => x.id), [3, 1, 2])
})

test('删除结果摘要与显示名称兜底', () => {
  assert.match(summarizeDeleteResult({ visitors: 2, conversations: 3, messages: 40, skipped: 1 }), /2 位客户.*3 个会话.*40 条消息.*1 位客户无权操作/)
  assert.doesNotMatch(summarizeDeleteResult({ visitors: 1 }), /跳过/)
  assert.equal(visitorDisplayName({ visitor_id: 'abcdef123' }), '访客 abcdef')
  assert.equal(senderLabel('agent'), '客服')
  assert.equal(senderLabel('visitor'), '访客')
})

test('搜索关键词 2-64 字（按码点），条数上限 50', () => {
  assert.equal(normalizeSearchKeyword(' a ').ok, false)
  assert.equal(normalizeSearchKeyword('退款').ok, true)
  assert.equal(normalizeSearchKeyword('字'.repeat(64)).ok, true)
  assert.equal(normalizeSearchKeyword('字'.repeat(65)).ok, false)
  assert.equal(normalizeSearchKeyword('ab\ncd').ok, false)
  assert.equal(clampSearchLimit(500), 50)
  assert.equal(clampSearchLimit('x'), 30)
})

test('自动清理：0 关闭，7-3650 合法，其余拒绝', () => {
  assert.deepEqual(parseAutoCleanDays('0'), { enabled: false, days: 90 })
  assert.deepEqual(parseAutoCleanDays('30'), { enabled: true, days: 30 })
  assert.deepEqual(parseAutoCleanDays('6'), { enabled: false, days: 90 })
  assert.deepEqual(parseAutoCleanDays('abc'), { enabled: false, days: 90 })
  assert.equal(serializeAutoCleanDays(false, 5).value, '0')
  assert.equal(serializeAutoCleanDays(true, 7).value, '7')
  assert.equal(serializeAutoCleanDays(true, 3650).value, '3650')
  assert.equal(serializeAutoCleanDays(true, 6).ok, false)
  assert.equal(serializeAutoCleanDays(true, 3651).ok, false)
  assert.equal(serializeAutoCleanDays(true, 7.5).ok, false)
})

test('高危确认文字必须完全一致（允许首尾空格）', () => {
  assert.equal(matchesConfirmText('删除全部', CONFIRM_DELETE_ALL), true)
  assert.equal(matchesConfirmText(' 删除全部 ', CONFIRM_DELETE_ALL), true)
  assert.equal(matchesConfirmText('删除', CONFIRM_DELETE_ALL), false)
  assert.equal(matchesConfirmText('清空回收站', CONFIRM_PURGE_RECYCLE), true)
  assert.equal(matchesConfirmText(undefined, CONFIRM_PURGE_RECYCLE), false)
  assert.equal(matchesConfirmText('', ''), false)
})

test('北京时间格式化不受运行环境时区影响', () => {
  assert.equal(formatBeijing('2026-10-14T11:42:07Z'), '2026-10-14 19:42:07')
  assert.equal(formatBeijing('2026-10-14T19:42:07+08:00', { seconds: false }), '2026-10-14 19:42')
  assert.equal(formatBeijing('not-a-date'), '—')
  assert.equal(compactBeijingStamp(Date.parse('2026-12-31T16:30:05Z')), '20270101-003005')
})
