import test from 'node:test'
import assert from 'node:assert/strict'

import {
  NOTE_MAX_LENGTH,
  TAG_MAX_COUNT,
  noteSummary,
  normalizeProfile,
  normalizeTags,
  tryAddTag,
  validateBlockReason,
  validateNote,
  validateTagText,
} from '../src/modules/visitorProfile.js'
import { charCount, truncateChars } from '../src/modules/textLimits.js'

test('标签按码点计数：16 个汉字或代理对字符合法，17 个拒绝', () => {
  assert.equal(validateTagText('一二三四五六七八九十一二三四五六').ok, true)
  assert.equal(validateTagText('一二三四五六七八九十一二三四五六七').ok, false)
  const astral = '\u{20BB7}'.repeat(16) // 16 个扩展区汉字（UTF-16 下长度为 32）
  assert.equal(astral.length, 32)
  assert.equal(charCount(astral), 16)
  assert.equal(validateTagText(astral).ok, true)
})

test('标签去首尾空格、空项忽略、去重保序，超过 10 个拒绝', () => {
  const r = normalizeTags(['  VIP ', 'VIP', '', '   ', '老客户'])
  assert.deepEqual(r, { ok: true, tags: ['VIP', '老客户'], error: '' })
  const eleven = Array.from({ length: TAG_MAX_COUNT + 1 }, (_, i) => 't' + i)
  assert.equal(normalizeTags(eleven).ok, false)
  assert.equal(normalizeTags(eleven.slice(0, TAG_MAX_COUNT)).ok, true)
})

test('标签拒绝非字符串、换行等控制字符', () => {
  assert.equal(normalizeTags(['ok', 123]).ok, false)
  assert.equal(normalizeTags('VIP').ok, false)
  assert.equal(validateTagText('a\nb').ok, false)
  assert.equal(validateTagText('a\u0000b').ok, false)
})

test('输入框追加标签：重复、超量、超长均拒绝且不修改原数组', () => {
  const base = ['VIP']
  assert.deepEqual(tryAddTag(base, ' 新客 ').tags, ['VIP', '新客'])
  assert.deepEqual(base, ['VIP'])
  assert.equal(tryAddTag(base, 'VIP').ok, false)
  const full = Array.from({ length: TAG_MAX_COUNT }, (_, i) => 'x' + i)
  assert.equal(tryAddTag(full, '再来一个').ok, false)
  assert.equal(tryAddTag(base, '一二三四五六七八九十一二三四五六七').ok, false)
})

test('备注最多 500 字且允许换行；拉黑原因最多 100 字且不允许换行', () => {
  assert.equal(validateNote('第一行\n第二行').ok, true)
  assert.equal(validateNote('字'.repeat(NOTE_MAX_LENGTH)).ok, true)
  assert.equal(validateNote('字'.repeat(NOTE_MAX_LENGTH + 1)).ok, false)
  assert.equal(validateNote(null).ok, true)
  assert.equal(validateNote(12).ok, false)
  assert.equal(validateBlockReason(' 骚扰 ').reason, '骚扰')
  assert.equal(validateBlockReason('因'.repeat(101)).ok, false)
  assert.equal(validateBlockReason('a\nb').ok, false)
})

test('备注摘要取首个非空行并按码点截断；资料对象缺字段时给安全默认值', () => {
  assert.equal(noteSummary('\n  \n 老客户，偏好电话 \n第二行', 4), '老客户，…')
  assert.equal(truncateChars('\u{1F600}\u{1F600}\u{1F600}', 2), '\u{1F600}\u{1F600}…')
  const p = normalizeProfile({ tags: ['a', 1, null, 'b'], pinned: 'yes', blocked: true })
  assert.deepEqual(p.tags, ['a', 'b'])
  assert.equal(p.pinned, false)
  assert.equal(p.blocked, true)
  assert.equal(p.note, '')
})
