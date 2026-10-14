import test from 'node:test'
import assert from 'node:assert/strict'

import {
  QR_CONTENT_MAX,
  QR_TITLE_MAX,
  canManageQuickReply,
  filterQuickReplies,
  insertQuickReply,
  splitByScope,
  validateQuickReply,
} from '../src/modules/quickReply.js'

test('快捷回复校验：标题 ≤32 可空，内容 1-1000，排序为 0-9999 整数', () => {
  assert.equal(validateQuickReply({ title: '', content: '您好' }).ok, true)
  assert.equal(validateQuickReply({ title: '标'.repeat(QR_TITLE_MAX), content: 'x' }).ok, true)
  assert.equal(validateQuickReply({ title: '标'.repeat(QR_TITLE_MAX + 1), content: 'x' }).ok, false)
  assert.equal(validateQuickReply({ title: 't', content: '   ' }).ok, false)
  assert.equal(validateQuickReply({ content: '字'.repeat(QR_CONTENT_MAX) }).ok, true)
  assert.equal(validateQuickReply({ content: '字'.repeat(QR_CONTENT_MAX + 1) }).ok, false)
  assert.deepEqual(validateQuickReply({ title: ' 问候 ', content: ' 您好 ', sort_order: 3 }).value, { title: '问候', content: '您好', sort_order: 3 })
  assert.equal(validateQuickReply({ content: 'x', sort_order: -1 }).ok, false)
  assert.equal(validateQuickReply({ content: 'x', sort_order: 1.5 }).ok, false)
  assert.equal(validateQuickReply({ content: 'x', title: 'a\nb' }).ok, false)
  assert.equal(validateQuickReply(null).ok, false)
})

test('快捷回复搜索同时匹配标题与内容并忽略大小写', () => {
  const list = [
    { id: 1, scope: 'team', title: 'Hello', content: '欢迎光临' },
    { id: 2, scope: 'personal', title: '退款', content: '请提供订单号' },
    { id: 3, scope: 'personal', title: null, content: 'ORDER 查询' },
    { id: 4, scope: 'personal', title: '坏数据' },
  ]
  assert.deepEqual(filterQuickReplies(list, 'hello').map(x => x.id), [1])
  assert.deepEqual(filterQuickReplies(list, '订单').map(x => x.id), [2])
  assert.deepEqual(filterQuickReplies(list, 'order').map(x => x.id), [3])
  assert.equal(filterQuickReplies(list, '').length, 3)
  const groups = splitByScope(list)
  assert.equal(groups.team.length, 1)
  assert.equal(groups.personal.length, 3)
})

test('团队话术仅管理员可管理，个人话术本人可管理', () => {
  assert.equal(canManageQuickReply({ scope: 'team' }, false), false)
  assert.equal(canManageQuickReply({ scope: 'team' }, true), true)
  assert.equal(canManageQuickReply({ scope: 'personal' }, false), true)
  assert.equal(canManageQuickReply({ scope: 'other' }, true), false)
})

test('点击话术填入：空草稿直接填入，已有草稿换行追加不覆盖', () => {
  assert.equal(insertQuickReply('', '您好'), '您好')
  assert.equal(insertQuickReply('  ', '您好'), '您好')
  assert.equal(insertQuickReply('请稍等', '您好'), '请稍等\n您好')
  assert.equal(insertQuickReply('请稍等\n', '您好'), '请稍等\n您好')
  assert.equal(insertQuickReply('原文', ''), '原文')
})
