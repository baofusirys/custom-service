import test from 'node:test'
import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'

import {
  HALL_MESSAGE_MAX_CHARS,
  SlidingWindowLimiter,
  buildHallChatEnvelope,
  checkHallUploadFile,
  contentLines,
  createHallMessageId,
  createPendingHallMessage,
  formatFileSize,
  fromHallChatEnvelope,
  hallSenderLabel,
  markHallMessageStatus,
  mediaFromUploadResponse,
  mergeHallMessages,
  normalizeHallMessage,
  normalizeHallMessageList,
  oldestCursor,
  removeHallMessage,
  sanitizeMediaUrl,
  validateOutgoingText,
} from '../src/modules/hallMessages.js'

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const id = (n) => '00000000-0000-4000-8000-' + String(n).padStart(12, '0')
const http = (n, ts, extra = {}) => ({ id: id(n), sender: 'visitor', identity: 'v_0123456789abcdef0123', name: '访客 0001', role: 'visitor', content: '消息' + n, media_url: '', media_kind: '', media_name: '', media_size: 0, created_at: new Date(ts).toISOString(), ...extra })

test('消息 ID 为密码学随机 uuid v4（randomUUID 与 getRandomValues 两条路径）', () => {
  assert.match(createHallMessageId(webcrypto), UUID_V4)
  const legacy = { getRandomValues: (b) => webcrypto.getRandomValues(b) }
  for (let i = 0; i < 50; i++) assert.match(createHallMessageId(legacy), UUID_V4)
  assert.throws(() => createHallMessageId(null))
  assert.throws(() => createHallMessageId({}))
})

test('HTTP HallMessage 规范化：时间、角色、媒体白名单', () => {
  const m = normalizeHallMessage(http(1, Date.parse('2026-10-17T10:00:00+08:00'), {
    media_url: '/files/2026/10/17/abc-def.png?access=Tok_en-1', media_kind: 'image', media_name: 'a.png', media_size: '2048',
  }))
  assert.equal(m.createdAt, Date.parse('2026-10-17T02:00:00Z'))
  assert.equal(m.mediaUrl, '/files/2026/10/17/abc-def.png?access=Tok_en-1')
  assert.equal(m.mediaKind, 'image')
  assert.equal(m.mediaSize, 2048)
  assert.equal(m.status, 'sent')
  assert.equal(normalizeHallMessage({ ...http(2, 1), id: '../x' }), null)
  assert.equal(normalizeHallMessage({ ...http(2, 1), sender: 'admin' }), null)
  assert.equal(normalizeHallMessage({ ...http(2, 1), content: '   ' }), null)
  assert.equal(normalizeHallMessage({ ...http(3, 1), sender: 'system', role: '' }).sender, 'system')
})

test('媒体地址只接受本系统 /files/ 上传地址，拒绝外链、协议伪装与路径穿越', () => {
  for (const bad of ['javascript:alert(1)', 'https://evil.com/a.png', '//evil.com/a.png', '/files/../etc/passwd',
    '/files//x.png', '/api/x', '/files/a.png?access=<script>', '/files/a.png?x=1', ' /files/a b.png', 123, null]) {
    assert.equal(sanitizeMediaUrl(bad), '', String(bad))
  }
  assert.equal(sanitizeMediaUrl('/files/2026/10/17/a.pdf'), '/files/2026/10/17/a.pdf')
  const m = normalizeHallMessage(http(4, 1, { content: '', media_url: 'javascript:alert(1)', media_kind: 'image' }))
  assert.equal(m, null) // 无文字且媒体非法 → 丢弃
})

test('WS hall_chat 信封使用 media/mkind/mname/msize 字段与 extra 发送者信息', () => {
  const m = fromHallChatEnvelope({
    type: 'hall_chat', id: id(5), content: '', media: '/files/2026/10/17/x.pdf?access=abc', mkind: 'file', mname: '报价单.pdf', msize: 1000,
    ts: 1760000000000, extra: { sender: 'agent', identity: 'a_7', name: '客服·小王', role: 'agent' },
  })
  assert.equal(m.sender, 'agent')
  assert.equal(m.name, '客服·小王')
  assert.equal(m.mediaName, '报价单.pdf')
  assert.equal(m.createdAt, 1760000000000)
  assert.equal(fromHallChatEnvelope({ type: 'chat', id: id(5), content: 'x' }), null)
  assert.equal(hallSenderLabel(m), '客服')
})

test('合并：按 id 去重、按时间正序、服务端版本覆盖本地乐观版本', () => {
  const a = normalizeHallMessage(http(1, 1000))
  const b = normalizeHallMessage(http(2, 2000))
  const c = normalizeHallMessage(http(3, 3000))
  let list = mergeHallMessages([], [c, a])
  list = mergeHallMessages(list, [b, a])
  assert.deepEqual(list.map(m => m.id), [id(1), id(2), id(3)])
  // 同一时间按 id 排序，结果稳定
  const d = normalizeHallMessage(http(0, 2000))
  assert.deepEqual(mergeHallMessages(list, [d]).map(m => m.id), [id(1), id(0), id(2), id(3)])
  const pend = createPendingHallMessage({ id: id(9), content: '你好', self: { identity: 'a_1', name: '客服·我' } }, 5000)
  list = mergeHallMessages(list, [pend])
  assert.equal(list.at(-1).status, 'sending')
  const server = fromHallChatEnvelope({ type: 'hall_chat', id: id(9), content: '你好', ts: 4000, extra: { sender: 'agent', identity: 'a_1', name: '客服·我', role: 'agent' } })
  list = mergeHallMessages(list, [server])
  const mine = list.find(m => m.id === id(9))
  assert.equal(mine.status, 'sent')
  assert.equal(list.filter(m => m.id === id(9)).length, 1)
  // 已确认的消息不会被迟到的本地乐观版本覆盖
  list = mergeHallMessages(list, [pend])
  assert.equal(list.find(m => m.id === id(9)).status, 'sent')
})

test('内存上限：实时追加丢最旧，翻页模式丢最新', () => {
  const many = Array.from({ length: 12 }, (_, i) => normalizeHallMessage(http(i + 1, (i + 1) * 1000)))
  assert.deepEqual(mergeHallMessages([], many, { limit: 10 }).map(m => m.id), many.slice(2).map(m => m.id))
  assert.deepEqual(mergeHallMessages([], many, { limit: 10, keep: 'oldest' }).map(m => m.id), many.slice(0, 10).map(m => m.id))
})

test('删除、状态标记与翻页游标', () => {
  const a = normalizeHallMessage(http(1, 1000))
  const p = createPendingHallMessage({ id: id(2), content: 'x' }, 500)
  let list = mergeHallMessages([], [a, p])
  assert.equal(oldestCursor(list), id(1)) // 跳过待发消息
  list = markHallMessageStatus(list, id(2), 'failed')
  assert.equal(list.find(m => m.id === id(2)).status, 'failed')
  const same = markHallMessageStatus(list, id(2), 'failed')
  assert.equal(same, list)
  // 已确认消息不会被超时改成失败
  assert.equal(markHallMessageStatus(list, id(1), 'failed'), list)
  list = removeHallMessage(list, id(1))
  assert.deepEqual(list.map(m => m.id), [id(2)])
  assert.equal(removeHallMessage(list, 'bad id!'), list)
  assert.equal(oldestCursor(list), '')
})

test('发送内容校验：去空白、1–1000 字（按码点）、剔除控制字符', () => {
  assert.deepEqual(validateOutgoingText('  你好  '), { ok: true, content: '你好' })
  assert.equal(validateOutgoingText('   ').ok, false)
  assert.equal(validateOutgoingText('\u0000\u0007').ok, false)
  assert.equal(validateOutgoingText('字'.repeat(HALL_MESSAGE_MAX_CHARS)).ok, true)
  assert.equal(validateOutgoingText('字'.repeat(HALL_MESSAGE_MAX_CHARS + 1)).ok, false)
  // 代理对汉字（扩展 B 区）按 1 个字计数
  assert.equal(validateOutgoingText('𠮷'.repeat(HALL_MESSAGE_MAX_CHARS)).ok, true)
  assert.equal(validateOutgoingText(null).ok, false)
})

test('发送信封：只接受 uuid v4，媒体字段按现有 ws.Envelope 命名', () => {
  const env = buildHallChatEnvelope({ id: id(1), content: '你好' })
  assert.deepEqual(env, { type: 'hall_chat', id: id(1), content: '你好' })
  const media = buildHallChatEnvelope({ id: id(2), media: { url: '/files/2026/10/17/a.png?access=x', kind: 'image', name: 'a.png', size: 10 } })
  assert.deepEqual(media, { type: 'hall_chat', id: id(2), content: '', media: '/files/2026/10/17/a.png?access=x', mkind: 'image', mname: 'a.png', msize: 10 })
  assert.equal(buildHallChatEnvelope({ id: 'm2-abc', content: 'x' }), null)
  assert.equal(buildHallChatEnvelope({ id: id(3), media: { url: 'https://evil.com/a.png' } }), null)
  assert.equal(buildHallChatEnvelope({ id: id(3), content: '' }), null)
})

test('发送限流：10 秒内最多 5 条，时钟回拨不会卡死', () => {
  let now = 100000
  const l = new SlidingWindowLimiter({ max: 5, windowMs: 10000, now: () => now })
  for (let i = 0; i < 5; i++) assert.equal(l.tryAcquire(), true)
  assert.equal(l.tryAcquire(), false)
  now += 9999
  assert.equal(l.tryAcquire(), false)
  now += 1
  assert.equal(l.tryAcquire(), true)
  now -= 60000
  assert.equal(l.tryAcquire(), true)
})

test('上传预校验与上传结果解析', () => {
  assert.equal(checkHallUploadFile({ name: 'a.PNG', size: 10 }, 'image').ok, true)
  assert.equal(checkHallUploadFile({ name: 'a.pdf', size: 10 }, 'image').ok, false)
  assert.equal(checkHallUploadFile({ name: 'a.pdf', size: 10 }, 'file').ok, true)
  assert.equal(checkHallUploadFile({ name: 'a.svg', size: 10 }, 'file').ok, false)
  assert.equal(checkHallUploadFile({ name: 'a.html', size: 10 }, 'file').ok, false)
  assert.equal(checkHallUploadFile({ name: 'noext', size: 10 }, 'file').ok, false)
  assert.equal(checkHallUploadFile({ name: 'a.png', size: 0 }, 'image').ok, false)
  assert.match(checkHallUploadFile({ name: 'a.png', size: 21 * 1024 * 1024 }, 'image').error, /20MB/)
  assert.deepEqual(mediaFromUploadResponse({ code: 0, url: '/files/2026/10/17/x.png?access=t', kind: 'image', name: 'x.png', size: 5 }),
    { url: '/files/2026/10/17/x.png?access=t', kind: 'image', name: 'x.png', size: 5 })
  assert.equal(mediaFromUploadResponse({ url: 'http://x/y' }), null)
  assert.equal(mediaFromUploadResponse(null), null)
})

test('列表规范化最多 50 条并丢弃坏数据；多行内容拆分；文件大小格式', () => {
  const raw = [...Array.from({ length: 60 }, (_, i) => http(i + 1, i + 1)), null, 'x']
  assert.equal(normalizeHallMessageList(raw).length, 50)
  assert.equal(normalizeHallMessageList('x').length, 0)
  assert.deepEqual(contentLines('a\nb\r\nc'), ['a', 'b', 'c'])
  assert.deepEqual(contentLines(''), [])
  assert.equal(formatFileSize(512), '512 B')
  assert.equal(formatFileSize(2048), '2.0 KB')
  assert.equal(formatFileSize(3 * 1024 * 1024), '3.0 MB')
  assert.equal(formatFileSize(0), '')
})
