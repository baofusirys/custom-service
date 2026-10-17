import test from 'node:test'
import assert from 'node:assert/strict'

import { bootWidget } from './helpers/fakeWidgetEnv.mjs'
import { createFakeLivekit } from './helpers/fakeLivekit.mjs'

// [119] 在 node:vm 中把真实的 hall-core.js + hall.js + chat.js 跑在同一个假页面里，验证大厅的端到端行为：
// 自动入厅、默认关麦、开麦流程与名额满提示、消息收发与删除、视频开关、被移出、被接管、自动播放限制、
// 私聊页签未读与已读回执、宿主页消息校验。媒体由假 LiveKit 模拟，接口由假 fetch 模拟。
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const JOIN = {
  url: 'wss://cs.example.com/lk', token: 'lk.jwt.token', room: 'hall_default', identity: 'v_0123456789abcdef0123', name: '访客 0042',
  video_enabled: false, max_speakers: 6, max_cameras: 2,
  ice_servers: [{ urls: ['turn:cs.example.com:3478'], username: 'u', credential: 'c' }],
  history: [
    { id: '11111111-1111-4111-8111-111111111111', sender: 'agent', identity: 'a_7', name: '客服·小王', role: 'agent', content: '欢迎来到大厅', media_url: '', media_kind: '', media_name: '', media_size: 0, created_at: '2026-10-14T19:00:00+08:00' },
    { id: '22222222-2222-4222-8222-222222222222', sender: 'visitor', identity: 'v_other', name: '访客 7788', role: 'visitor', content: '', media_url: '/files/2026/10/14/p.png?access=t', media_kind: 'image', media_name: 'p.png', media_size: 1234, created_at: '2026-10-14T19:01:00+08:00' },
  ],
}

// 记录字段的假 FormData（沙箱里没有浏览器的 FormData）
class FakeFormData {
  constructor() { this.entries = [] }
  append(k, v) { this.entries.push([k, v]) }
  get(k) { const e = this.entries.find(x => x[0] === k); return e ? e[1] : null }
  has(k) { return this.entries.some(x => x[0] === k) }
}
const UPLOAD_OK = { code: 0, id: 'f-1', url: '/files/2026/10/14/h.pdf?access=k', kind: 'file', size: 2048, name: 'h.pdf', mime: 'application/pdf' }

async function bootHall({ join, mic, camera, upload, globals = {}, settings = { hall_enabled: true, hall_video_enabled: false } } = {}) {
  const { LK, ctl } = createFakeLivekit()
  const calls = { join: [], mic: [], camera: [], messages: [], upload: [] }
  const routes = async (url, opts, respond) => {
    const body = typeof opts.body === 'string' ? JSON.parse(opts.body) : undefined
    if (url.includes('/api/visitor/hall/join')) { calls.join.push(opts); return join ? join(respond, calls.join.length) : respond({ code: 0, data: JOIN }) }
    if (url.includes('/api/visitor/hall/mic')) { calls.mic.push(body); return mic ? mic(respond, body) : respond({ code: 0, data: { on: body.on, used: 1, max: 6 } }) }
    if (url.includes('/api/visitor/hall/camera')) { calls.camera.push(body); return camera ? camera(respond, body) : respond({ code: 0, data: { on: body.on, used: 1, max: 2 } }) }
    if (url.includes('/api/visitor/hall/messages')) { calls.messages.push(url); return respond({ code: 0, data: [] }) }
    if (url.includes('/api/upload')) { calls.upload.push({ url, opts }); return upload ? upload(respond) : respond(UPLOAD_OK) }
    return null
  }
  const env = await bootWidget({ hall: true, routes, globals: { LivekitClient: LK, FormData: FakeFormData, ...globals }, settings })
  const ws = env.ws()
  ws.open()
  const step = async (ms = 1000) => { env.clock.tick(ms); await env.flush() }
  await step(1000)
  const room = () => ctl.rooms.at(-1)
  const id = env.byId
  const hallMsgs = () => id('hallMsgs').children
  const hallText = () => id('hallMsgs').textContent
  return { env, ws, LK, ctl, calls, step, room, id, hallMsgs, hallText }
}

test('自动入厅：会话就绪后空闲时调用 join（Bearer 访客令牌、URL 不带 vid），按契约连接 LiveKit 并订阅大厅', async () => {
  const h = await bootHall()
  assert.equal(h.calls.join.length, 1)
  const call = h.env.fetchCalls.find(c => c.url.includes('/api/visitor/hall/join'))
  assert.equal(call.opts.method, 'POST')
  assert.equal(call.opts.headers.Authorization, 'Bearer tok-test')
  assert.doesNotMatch(call.url, /vid|v-test/)
  const room = h.room()
  assert.ok(room && room.connected)
  assert.equal(room.connectArgs.url, JOIN.url)
  assert.equal(room.connectArgs.token, JOIN.token)
  assert.equal(room.connectArgs.opts.autoSubscribe, false, '只手动订阅音频，视频按可见性订阅')
  assert.deepEqual(JSON.parse(JSON.stringify(room.connectArgs.opts.rtcConfig.iceServers)), JOIN.ice_servers)
  assert.equal(room.opts.dynacast, true)
  assert.ok(room.opts.adaptiveStream)
  assert.equal(room.opts.publishDefaults.videoSimulcastLayers[0], h.LK.VideoPresets.h180)
  assert.equal(room.opts.publishDefaults.videoEncoding, h.LK.VideoPresets.h360.encoding)
  const sub = h.ws.sent.find(e => e.type === 'hall_subscribe')
  assert.ok(sub && UUID_V4.test(sub.id), 'LiveKit 连接成功后发送 hall_subscribe')
  // 页签默认大厅、默认关麦、隐私提示所在区域
  assert.equal(h.id('tabs').hidden, false)
  assert.equal(h.id('hallPane').hidden, false)
  assert.equal(h.id('privatePane').hidden, true)
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'false')
  assert.equal(room.localParticipant.calls.length, 0, '默认不发布麦克风')
  assert.equal(h.id('hallCamBtn').hidden, true, '视频未开启时不显示摄像头按钮')
  // 历史消息：客服标识与图片缩略图（懒加载）
  assert.equal(h.hallMsgs().length, 2)
  assert.match(h.hallText(), /欢迎来到大厅/)
  assert.match(h.hallText(), /客服/)
  const img = h.env.doc.body.querySelectorAll('.hm-img')[0]
  assert.equal(img.src, 'https://cs.example.com/files/2026/10/14/p.png?access=t')
  assert.equal(img.getAttribute('loading'), 'lazy')
  await h.step(500)
  assert.ok(h.env.posted.some(p => p.type === 'hall_status' && p.state === 'live' && p.online === 1), '向宿主页同步大厅状态')
})

test('远端开麦者：自动订阅音频并挂载播放元素；静音大厅只影响本机（退订并记住偏好）', async () => {
  const h = await bootHall()
  const room = h.room()
  const p = room.addRemote('a_7', '客服·小王', 'agent')
  const pub = [...p.audioTrackPublications.values()][0]
  assert.deepEqual(pub.subscribeCalls, [true])
  assert.equal(h.id('hallAudioSink').children[0].getAttribute('data-identity'), 'a_7')
  assert.equal(h.id('hallAudioSink').children.length, 1)
  // 挂件展开后再收起：仍在收听（音频订阅与播放元素保持不变）
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'widget_state', open: true }, source: h.env.parentWindow, origin: '' })
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'widget_state', open: false }, source: h.env.parentWindow, origin: '' })
  assert.deepEqual(pub.subscribeCalls, [true], '挂件收起不退订音频')
  assert.equal(h.id('hallAudioSink').children.length, 1)
  await h.step(300)
  assert.ok(h.env.doc.body.querySelectorAll('[data-identity="a_7"]').length >= 1, '参与者条显示客服')
  h.id('hallMuteBtn').click()
  assert.deepEqual(pub.subscribeCalls, [true, false])
  assert.equal(h.env.localStorage.getItem('cs_visitor_default_hall_muted'), '1')
  assert.equal(h.id('hallMuteBtn').getAttribute('aria-pressed'), 'true')
  await h.step(500)
  assert.equal(h.env.posted.filter(m => m.type === 'hall_status').at(-1).state, 'muted')
  h.id('hallMuteBtn').click()
  assert.deepEqual(pub.subscribeCalls, [true, false, true])
})

test('开麦：先请求 /hall/mic {on:true}，收到发布权限后才发布；关麦先停止发布再释放名额', async () => {
  const h = await bootHall()
  const room = h.room()
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.deepEqual(h.calls.mic, [{ on: true }])
  assert.equal(room.localParticipant.calls.length, 0, '权限未下发前不发布')
  room.localParticipant.grant([2])
  await h.env.flush()
  assert.deepEqual(room.localParticipant.calls, [['mic', true]])
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'true')
  assert.equal(h.id('hallMicLabel').textContent, '关麦')
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.deepEqual(room.localParticipant.calls.at(-1), ['unpublish', 'audio'])
  assert.deepEqual(h.calls.mic.at(-1), { on: false })
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'false')
})

test('开麦名额已满（40973）与被禁（40374）给出中文提示且不发布；麦克风授权失败时释放名额', async () => {
  let reply = (respond) => respond({ code: 40973, msg: 'full', data: { used: 6, max: 6 } }, 409)
  const h = await bootHall({ mic: (respond, body) => body.on ? reply(respond) : respond({ code: 0, data: { on: false } }) })
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.equal(h.id('hallToast').hidden, false)
  assert.equal(h.id('hallToast').textContent, '开麦人数已满（6/6），请稍后再试')
  assert.equal(h.room().localParticipant.calls.length, 0)
  reply = (respond) => respond({ code: 40374, msg: 'blocked' }, 403)
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.equal(h.id('hallToast').textContent, '当前无法开麦')
  reply = (respond) => respond({ code: 0, data: { on: true, used: 1, max: 6 } })
  h.room().localParticipant.permissions = { canPublish: true, canPublishSources: [2] }
  h.ctl.mediaError = 'NotAllowedError'
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.match(h.id('hallToast').textContent, /允许麦克风权限/)
  assert.deepEqual(h.calls.mic.at(-1), { on: false }, '授权失败后释放已占用的名额')
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'false')
})

test('大厅消息：发送带 uuid v4 与 hall_ack、失败回执显示在大厅而不进入私聊、广播去重、删除即时消失', async () => {
  const h = await bootHall()
  h.id('hallInput').value = '大家好'
  h.id('hallSendBtn').click()
  const sent = h.ws.sent.filter(e => e.type === 'hall_chat')
  assert.equal(sent.length, 1)
  assert.ok(UUID_V4.test(sent[0].id))
  assert.equal(sent[0].content, '大家好')
  assert.equal(h.id('hallInput').value, '')
  assert.match(h.hallText(), /发送中/)
  h.ws.receive({ type: 'hall_ack', id: sent[0].id })
  assert.doesNotMatch(h.hallText(), /发送中/)
  // 自己消息的广播回显不重复显示
  h.ws.receive({ type: 'hall_chat', id: sent[0].id, content: '大家好', ts: Date.parse('2026-10-14T12:00:01Z'), extra: { sender: 'visitor', identity: JOIN.identity, name: JOIN.name, role: 'visitor' } })
  assert.equal(h.hallMsgs().length, 3)
  // 失败回执
  h.id('hallInput').value = '第二条'
  h.id('hallSendBtn').click()
  const second = h.ws.sent.filter(e => e.type === 'hall_chat').at(-1)
  h.ws.receive({ type: 'error', id: second.id, content: '内容含违禁词' })
  assert.match(h.hallText(), /未发送：内容含违禁词/)
  assert.doesNotMatch(h.env.list.textContent, /违禁词/, '大厅错误不显示在私聊区')
  // 他人消息（WS 字段 media/mkind）与重复广播
  const other = { type: 'hall_chat', id: '33333333-3333-4333-8333-333333333333', content: '', media: '/files/2026/10/14/f.pdf?access=z', mkind: 'file', mname: '报价单.pdf', msize: 2048, ts: Date.parse('2026-10-14T12:00:05Z'), extra: { sender: 'visitor', identity: 'v_x', name: '访客 1001', role: 'visitor' } }
  h.ws.receive(other)
  h.ws.receive(other)
  assert.equal(h.env.doc.body.querySelectorAll('[data-hall-id="' + other.id + '"]').length, 1)
  assert.match(h.hallText(), /报价单\.pdf/)
  const file = h.env.doc.body.querySelectorAll('.hm-file')[0]
  assert.equal(file.href, 'https://cs.example.com/files/2026/10/14/f.pdf?access=z')
  // 外站媒体地址被丢弃
  h.ws.receive({ ...other, id: '44444444-4444-4444-8444-444444444444', media: 'https://evil.example/files/x.png', mkind: 'image' })
  assert.equal(h.env.doc.body.querySelectorAll('[data-hall-id="44444444-4444-4444-8444-444444444444"]').length, 0)
  // 删除
  h.ws.receive({ type: 'hall_deleted', id: other.id })
  assert.equal(h.env.doc.body.querySelectorAll('[data-hall-id="' + other.id + '"]').length, 0)
  h.ws.receive(other)
  assert.equal(h.env.doc.body.querySelectorAll('[data-hall-id="' + other.id + '"]').length, 0, '删除后的迟到重放不复活')
  // 前端频率限制：10 秒内最多 5 条
  for (let i = 0; i < 5; i++) { h.id('hallInput').value = 'm' + i; h.id('hallSendBtn').click() }
  assert.equal(h.id('hallToast').textContent, '发送过于频繁，请稍后再试')
})

test('重新入厅：以服务端最近 50 条为准，清掉断线期间被删除的消息，保留本端未发出的消息', async () => {
  let n = 0
  const h = await bootHall({ join: (respond) => (++n === 1 ? respond({ code: 0, data: JOIN }) : respond({ code: 0, data: { ...JOIN, history: [JOIN.history[1]] } })) })
  assert.equal(h.hallMsgs().length, 2)
  h.id('hallInput').value = '没发出去的'
  h.id('hallSendBtn').click()
  const mine = h.ws.sent.filter(e => e.type === 'hall_chat').at(-1)
  h.ws.receive({ type: 'error', id: mine.id, content: '发送失败' })
  h.room().emit('disconnected', 3)   // 媒体断开 → 退避后重新 join（期间客服删除了欢迎消息）
  await h.step(5000)
  assert.equal(n, 2)
  assert.doesNotMatch(h.hallText(), /欢迎来到大厅/, '断线期间被删除的客服消息消失')
  assert.equal(h.env.doc.body.querySelectorAll('[data-hall-id="' + JOIN.history[1].id + '"]').length, 1)
  assert.match(h.hallText(), /没发出去的/, '本端发送失败的消息保留，可重发')
  assert.match(h.hallText(), /重发/)
})

test('补拉历史与实时广播竞态：请求发出后才收到的消息不会被误删', async () => {
  let release
  const gate = new Promise((r) => { release = r })
  let n = 0
  const h = await bootHall({ join: async (respond) => { if (++n === 2) await gate; return respond({ code: 0, data: { ...JOIN, history: [] } }) } })
  h.room().emit('disconnected', 3)
  h.env.clock.tick(5000)          // 第二次 join 已发出，响应被挂起
  await h.env.flush()
  h.env.clock.tick(10)
  const live = { type: 'hall_chat', id: '55555555-5555-4555-8555-555555555555', content: '刚刚发来的', ts: Date.now(), extra: { sender: 'visitor', identity: 'v_y', name: '访客 2002', role: 'visitor' } }
  h.ws.receive(live)
  release()
  await h.env.flush()
  assert.equal(n, 2)
  assert.match(h.hallText(), /刚刚发来的/)
  assert.doesNotMatch(h.hallText(), /欢迎来到大厅/)
})

test('hall_settings：视频开启后显示摄像头按钮、关闭后隐藏；关闭大厅后只保留私聊', async () => {
  const h = await bootHall()
  assert.equal(h.id('hallCamBtn').hidden, true)
  h.ws.receive({ type: 'hall_settings', extra: { enabled: true, video_enabled: true, max_speakers: 6, max_cameras: 2 } })
  assert.equal(h.id('hallCamBtn').hidden, false)
  assert.match(h.id('hallToast').textContent, /已开启大厅视频/)
  h.ws.receive({ type: 'hall_settings', extra: { enabled: true, video_enabled: false, max_speakers: 6, max_cameras: 2 } })
  assert.equal(h.id('hallCamBtn').hidden, true)
  h.ws.receive({ type: 'hall_settings', extra: { enabled: false, video_enabled: false, max_speakers: 6, max_cameras: 2 } })
  assert.equal(h.id('tabs').hidden, true)
  assert.equal(h.id('privatePane').hidden, false)
  assert.equal(h.room().connected, false)
  assert.ok(h.ws.sent.some(e => e.type === 'hall_unsubscribe'))
  await h.step(500)
  assert.equal(h.env.posted.filter(m => m.type === 'hall_status').at(-1).state, 'off')
  // 重新开启后自动入厅（[123] 入厅前随机错开 0.3–3 秒，再加空闲回调兜底 0.8 秒）
  h.ws.receive({ type: 'hall_settings', extra: { enabled: true, video_enabled: false, max_speakers: 6, max_cameras: 2 } })
  await h.step(4000)
  assert.equal(h.calls.join.length, 2)
  assert.equal(h.id('tabs').hidden, false)
})

test('被客服移出：断开媒体、退订、禁用输入并提示分钟数，到期后自动重新入厅；join 返回 40372 显示剩余时间', async () => {
  let n = 0
  const h = await bootHall({ join: (respond) => (++n === 2 ? respond({ code: 40372, msg: 'kicked', data: { retry_after: 600 } }, 403) : respond({ code: 0, data: JOIN })) })
  h.ws.receive({ type: 'hall_moderation', extra: { action: 'kicked', minutes: 1 } })
  assert.equal(h.id('hallNotice').hidden, false)
  assert.equal(h.id('hallNoticeText').textContent, '您已被客服移出大厅，1 分钟后可重新进入')
  assert.equal(h.id('hallInput').disabled, true)
  assert.equal(h.room().connected, false)
  assert.ok(h.ws.sent.some(e => e.type === 'hall_unsubscribe'))
  await h.step(30000)
  assert.equal(h.calls.join.length, 1, '禁入期内不重连')
  await h.step(35000)
  assert.equal(h.calls.join.length, 2, '到期后重新 join')
  assert.equal(h.id('hallNoticeText').textContent, '您已被移出大厅，约 10 分钟后可重新进入')
})

test('自动播放受限：显示“点击收听大厅”，点击后调用 room.startAudio() 并恢复', async () => {
  const h = await bootHall()
  const room = h.room()
  room.canPlaybackAudio = false
  room.emit('audioPlaybackChanged', false)
  assert.equal(h.id('hallNotice').hidden, false)
  assert.equal(h.id('hallNoticeBtn').textContent, '点击收听大厅')
  await h.step(500)
  assert.equal(h.env.posted.filter(m => m.type === 'hall_status').at(-1).state, 'blocked')
  h.id('hallNoticeBtn').click()
  await h.env.flush()
  assert.ok(room.startAudioCalls >= 1)
  assert.equal(h.id('hallNotice').hidden, true)
  await h.step(500)
  assert.equal(h.env.posted.filter(m => m.type === 'hall_status').at(-1).state, 'live')
})

test('同一访客在其他页面打开（DUPLICATE_IDENTITY）：显示“已在其他页面收听”，不自动重连；点击后接管', async () => {
  const h = await bootHall()
  h.room().emit('disconnected', 2)
  assert.equal(h.id('hallNoticeText').textContent, '您已在其他页面收听大厅')
  assert.equal(h.id('hallNoticeBtn').textContent, '在此页面收听')
  const unsub = h.ws.sent.find(e => e.type === 'hall_unsubscribe')
  assert.ok(unsub && UUID_V4.test(unsub.id), 'hall_unsubscribe 按契约带 uuid v4')
  h.ws.receive({ type: 'hall_unsubscribed', id: unsub.id })
  assert.doesNotMatch(h.env.list.textContent, /hall_unsubscribed|undefined/, '退订回执不进入私聊区')
  await h.step(120000)
  assert.equal(h.calls.join.length, 1, '被接管后不自动抢回')
  h.id('hallNoticeBtn').click()
  await h.step(1000)
  assert.equal(h.calls.join.length, 2)
  assert.ok(h.room().connected)
})

test('同浏览器多标签页：后打开的页面接管（BroadcastChannel），先打开的显示“已在其他页面收听”', async () => {
  const channels = []
  class FakeBC {
    constructor(name) { this.name = name; channels.push(this) }
    postMessage(data) { for (const c of channels) if (c !== this && c.name === this.name && c.onmessage) c.onmessage({ data: JSON.parse(JSON.stringify(data)) }) }
    close() {}
  }
  const { LK } = createFakeLivekit()
  const boot = async (now) => {
    const env = await bootWidget({ hall: true, now, globals: { LivekitClient: LK, BroadcastChannel: FakeBC }, settings: { hall_enabled: true },
      routes: async (url, opts, respond) => (url.includes('/api/visitor/hall/join') ? respond({ code: 0, data: JOIN }) : null) })
    env.ws().open()
    return env
  }
  const a = await boot()
  a.clock.tick(1000); await a.flush()
  assert.equal(a.byId('hallNotice').hidden, true)
  // 第二个标签页 5 秒后才打开（两个页面共用同一系统时钟，后打开者的宣告时间更晚）
  const b = await boot(a.clock.now + 5000)
  b.clock.tick(1000); await b.flush()
  assert.equal(a.byId('hallNoticeText').textContent, '您已在其他页面收听大厅', '先打开的页面让出')
  assert.equal(b.byId('hallNotice').hidden, true, '后打开的页面正常收听')
  // 伪造的标签页消息（结构不合法）被忽略
  channels[1].onmessage({ data: { type: 'hall_takeover', tab: '<script>', at: Date.now() } })
  assert.equal(b.byId('hallNotice').hidden, true)
})

test('媒体连接失败：大厅文字仍可用（订阅大厅），并按指数退避重试', async () => {
  const h = await bootHall()
  h.ctl.failConnect = true
  h.room().emit('disconnected', 3)
  await h.step(1000)
  assert.match(h.id('hallNoticeText').textContent, /仍可收发文字/)
  const before = h.calls.join.length
  await h.step(5000)
  assert.ok(h.calls.join.length > before, '退避后重新 join')
  assert.equal(h.id('hallInput').disabled, false)
})

test('私聊页签：在看大厅时私聊新消息只显示未读点、不回已读；切回私聊页才回已读', async () => {
  const h = await bootHall()
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'widget_state', open: true }, source: h.env.parentWindow, origin: '' })
  const readsBefore = h.ws.sent.filter(e => e.type === 'read').length
  assert.equal(readsBefore, 0, '打开挂件时停留在大厅页，不回私聊已读')
  h.ws.receive({ type: 'chat', from: 'agent:7', id: 'm2-p1', conv: 'conv-1', content: '私聊回复', ts: Date.now() })
  assert.equal(h.id('tabPrivateDot').hidden, false)
  assert.equal(h.ws.sent.filter(e => e.type === 'read').length, 0)
  assert.ok(h.ws.sent.some(e => e.type === 'delivery' && e.id === 'm2-p1'), '送达回执照常')
  h.id('tabPrivate').click()
  assert.equal(h.id('tabPrivateDot').hidden, true)
  assert.equal(h.id('privatePane').hidden, false)
  assert.ok(h.ws.sent.some(e => e.type === 'read'))
  h.ws.receive({ type: 'chat', from: 'agent:7', id: 'm2-p2', conv: 'conv-1', content: '再一条', ts: Date.now() })
  assert.ok(h.ws.sent.some(e => e.type === 'read' && e.id === 'm2-p2'), '正在看私聊页时立即回已读')
})

test('宿主页命令：只接受父窗口发来的合法结构', async () => {
  const h = await bootHall()
  h.id('tabPrivate').click()
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'hall_cmd', cmd: 'show_hall' }, source: {}, origin: 'https://evil.example' })
  assert.equal(h.id('hallPane').hidden, true, '非父窗口来源被忽略')
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'hall_cmd', cmd: 'eval' }, source: h.env.parentWindow, origin: '' })
  assert.equal(h.id('hallPane').hidden, true, '未知命令被忽略')
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'hall_cmd', cmd: 'show_hall' }, source: h.env.parentWindow, origin: '' })
  assert.equal(h.id('hallPane').hidden, false)
})

test('大厅关闭或后端未提供大厅接口：不显示页签，私聊照常', async () => {
  const off = await bootHall({ settings: { hall_enabled: false } })
  assert.equal(off.calls.join.length, 0)
  assert.equal(off.id('tabs').hidden, true)
  assert.equal(off.id('privatePane').hidden, false)
  const old = await bootHall({ join: (respond) => respond({ code: 404, msg: 'not found' }, 404) })
  assert.equal(old.calls.join.length, 1)
  assert.equal(old.id('tabs').hidden, true)
  assert.equal(old.id('privatePane').hidden, false)
  const closed = await bootHall({ join: (respond) => respond({ code: 40371, msg: 'closed' }, 403) })
  assert.equal(closed.id('tabs').hidden, true)
  await closed.step(120000)
  assert.equal(closed.calls.join.length, 1, '大厅关闭后不重试，等待 hall_settings 重新开启')
})

test('大厅满员（40977）：提示并按较长间隔重试', async () => {
  let n = 0
  const h = await bootHall({ join: (respond) => (++n === 1 ? respond({ code: 40977, msg: 'full' }, 409) : respond({ code: 0, data: JOIN })) })
  assert.match(h.id('hallNoticeText').textContent, /大厅人数已满/)
  await h.step(20000)
  assert.equal(h.calls.join.length, 1, '满员后至少等待 30 秒再试')
  await h.step(15000)
  assert.equal(h.calls.join.length, 2)
  assert.ok(h.room().connected)
})

test('订阅回执：服务端拒绝 hall_subscribe（服务繁忙）时提示并按退避有限次重试；hall_subscribed 回执后重试计数清零', async () => {
  const h = await bootHall()
  const subs = () => h.ws.sent.filter(e => e.type === 'hall_subscribe')
  assert.equal(subs().length, 1)
  h.ws.receive({ type: 'error', id: subs()[0].id, content: '服务繁忙，请稍后重试' })
  assert.equal(h.id('hallToast').textContent, '大厅消息订阅失败：服务繁忙，请稍后重试，正在重试')
  assert.doesNotMatch(h.env.list.textContent, /服务繁忙/, '订阅失败不显示在私聊区')
  h.id('hallInput').value = '订阅失败时发送'
  h.id('hallSendBtn').click()
  assert.equal(h.ws.sent.filter(e => e.type === 'hall_chat').length, 0, '未订阅时不发送大厅消息')
  await h.step(2000)
  assert.equal(subs().length, 1, '退避期内不重发')
  await h.step(2000)
  assert.equal(subs().length, 2, '约 3 秒后重新订阅')
  assert.ok(UUID_V4.test(subs()[1].id) && subs()[1].id !== subs()[0].id, '每次订阅使用新的 id')
  // 成功回执：计数清零，下一次失败重新从第一次退避开始并再次提示
  h.ws.receive({ type: 'hall_subscribed', id: subs()[1].id })
  h.ws.receive({ type: 'error', id: subs()[1].id, content: '服务繁忙，请稍后重试' })
  await h.step(4000)
  assert.equal(subs().length, 3)
  // 连续失败达到上限后停止自动重试，并提示失败原因
  for (let i = 0; i < 5; i++) {
    h.ws.receive({ type: 'error', id: subs().at(-1).id, content: '服务繁忙，请稍后重试' })
    await h.step(70000)
  }
  const count = subs().length
  assert.equal(count, 7, '最多自动重试 5 次')
  h.ws.receive({ type: 'error', id: subs().at(-1).id, content: '服务繁忙，请稍后重试' })
  assert.equal(h.id('hallToast').textContent, '大厅消息订阅失败：服务繁忙，请稍后重试')
  await h.step(300000)
  assert.equal(subs().length, count, '达到上限后不再自动重试')
  // 过期回执（旧 id）被忽略
  h.ws.receive({ type: 'error', id: subs()[0].id, content: '旧回执' })
  assert.doesNotMatch(h.id('hallToast').textContent, /旧回执/)
})

test('开麦接口：50370 提示语音服务暂不可用且不发布；40371 大厅已关闭时收起大厅只保留私聊', async () => {
  let reply = (respond) => respond({ code: 50370, msg: 'livekit down' }, 503)
  const h = await bootHall({ mic: (respond, body) => body.on ? reply(respond) : respond({ code: 0, data: { on: false } }) })
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.equal(h.id('hallToast').textContent, '语音服务暂不可用，请稍后再试')
  assert.equal(h.room().localParticipant.calls.length, 0)
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'false')
  reply = (respond) => respond({ code: 40371, msg: 'closed' }, 403)
  const room = h.room()
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.equal(h.id('tabs').hidden, true, '大厅已关闭：隐藏页签')
  assert.equal(h.id('privatePane').hidden, false, '私聊照常')
  assert.equal(room.connected, false, '断开大厅媒体')
  const unsub = h.ws.sent.find(e => e.type === 'hall_unsubscribe')
  assert.ok(unsub && UUID_V4.test(unsub.id))
})

test('大厅附件：/api/upload 带 Bearer 访客令牌、uploader=visitor、scope=hall、不带 conv_id；WS 帧用 media/mkind/mname/msize；禁入期上传 40372 提示剩余分钟', async () => {
  let reply = (respond) => respond(UPLOAD_OK)
  const h = await bootHall({ upload: (respond) => reply(respond) })
  const pick = (file) => h.id('hallFileInput').onchange({ target: { files: [file], value: 'x' } })
  pick({ name: '报价单.pdf', size: 2048, type: 'application/pdf' })
  h.id('hallSendBtn').click()
  await h.env.flush()
  assert.equal(h.calls.upload.length, 1)
  const call = h.calls.upload[0]
  assert.equal(call.opts.method, 'POST')
  assert.equal(call.opts.headers.Authorization, 'Bearer tok-test')
  assert.doesNotMatch(call.url, /vid|v-test|conv/)
  const fd = call.opts.body
  assert.equal(fd.get('uploader'), 'visitor')
  assert.equal(fd.get('scope'), 'hall')
  assert.equal(fd.has('conv_id'), false, '大厅附件不带 conv_id')
  const frame = h.ws.sent.filter(e => e.type === 'hall_chat').at(-1)
  assert.ok(frame && UUID_V4.test(frame.id))
  assert.equal(frame.content, '')
  assert.equal(frame.media, '/files/2026/10/14/h.pdf?access=k')
  assert.equal(frame.mkind, 'file')
  assert.equal(frame.mname, 'h.pdf')
  assert.equal(frame.msize, 2048)
  for (const k of ['media_url', 'media_kind', 'media_name', 'media_size']) assert.equal(k in frame, false, 'WS 帧不使用 HTTP 字段名 ' + k)
  // 服务端返回外站地址：不发送
  reply = (respond) => respond({ ...UPLOAD_OK, url: 'https://evil.example/files/x.pdf' })
  pick({ name: 'b.pdf', size: 10, type: 'application/pdf' })
  h.id('hallSendBtn').click()
  await h.env.flush()
  assert.match(h.id('hallToast').textContent, /地址无效/)
  assert.equal(h.ws.sent.filter(e => e.type === 'hall_chat').length, 1)
  // 禁入期：40372 带 retry_after
  reply = (respond) => respond({ code: 40372, msg: '你已被移出大厅，请稍后再试', data: { retry_after: 600 } }, 403)
  pick({ name: 'c.pdf', size: 10, type: 'application/pdf' })
  h.id('hallSendBtn').click()
  await h.env.flush()
  assert.equal(h.id('hallToast').textContent, '您已被移出大厅，约 10 分钟后可重新进入')
  assert.equal(h.ws.sent.filter(e => e.type === 'hall_chat').length, 1)
})

test('宿主页消息：只接受直接父窗口发来的 widget_state，宿主页内其他窗口伪造的忽略', async () => {
  const h = await bootHall()
  h.id('tabPrivate').click()
  h.ws.receive({ type: 'chat', from: 'agent:7', id: 'm3-p1', conv: 'conv-1', content: '私聊回复', ts: Date.now() })
  const reads = () => h.ws.sent.filter(e => e.type === 'read').length
  assert.equal(reads(), 0, '挂件收起时不回已读')
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'widget_state', open: true }, source: {}, origin: 'https://evil.example' })
  assert.equal(reads(), 0, '非父窗口伪造的 widget_state 被忽略')
  h.env.dispatchWindow('message', { data: null, source: h.env.parentWindow, origin: '' })
  h.env.dispatchWindow('message', { data: { __cs: 1, type: 'widget_state', open: true }, source: h.env.parentWindow, origin: '' })
  assert.ok(reads() > 0, '父窗口通知展开后回已读')
})

test('hall_ack 带服务器时间 ts：自己的消息按服务器时间排序（本机时钟偏差不影响顺序）', async () => {
  const h = await bootHall()
  const order = () => [...h.id('hallMsgs').children].map(n => n.getAttribute('data-hall-id'))
  h.id('hallInput').value = '时钟偏快的本机'
  h.id('hallSendBtn').click()
  const sent = h.ws.sent.filter(e => e.type === 'hall_chat').at(-1)
  assert.equal(order().at(-1), sent.id, '本地回显先排在末尾')
  // 服务器时间早于第二条历史消息（北京时间 19:01）：确认后移到两条历史消息之间
  h.ws.receive({ type: 'hall_ack', id: sent.id, ts: Date.parse('2026-10-14T19:00:30+08:00') })
  assert.deepEqual(order(), [JOIN.history[0].id, sent.id, JOIN.history[1].id])
  assert.doesNotMatch(h.hallText(), /发送中/)
  // 非法 ts 被忽略
  h.id('hallInput').value = '第二条'
  h.id('hallSendBtn').click()
  const second = h.ws.sent.filter(e => e.type === 'hall_chat').at(-1)
  h.ws.receive({ type: 'hall_ack', id: second.id, ts: 'x' })
  assert.equal(order().at(-1), second.id)
})

// 自动播放探测用的假浏览器能力：userActivation、AudioContext（resume 行为可控，只提供静音的 MediaStreamDestination）、Audio
//   ctxState：新建 AudioContext 的初始状态；resume：'pending'（无交互时一直不返回，模拟 Chrome）| 'ok'（立即恢复为 running）
//   play：'reject'（无交互时 NotAllowedError）| 'ok' | 'pending'
function autoplayEnv({ active = false, ctxState = 'suspended', resume = 'pending', play = 'ok' } = {}) {
  const ua = { hasBeenActive: active }
  const contexts = []
  const probes = []
  class FakeAudioContext {
    constructor() { this.state = ua.hasBeenActive ? 'running' : ctxState; this.tracks = []; this.resumeCalls = 0; contexts.push(this) }
    resume() {
      this.resumeCalls++
      if (ua.hasBeenActive || resume === 'ok') { this.state = 'running'; return Promise.resolve() }
      return new Promise(() => {})
    }
    createMediaStreamDestination() { const t = { stopped: false, stop() { this.stopped = true } }; this.tracks.push(t); return { stream: { getTracks: () => [t] } } }
    close() { this.state = 'closed'; return Promise.resolve() }
  }
  class ProbeAudio {
    constructor(src) { this.src = src; this.muted = false; this.srcObject = null; this.paused = true; if (!src) probes.push(this) }
    play() {
      if (!this.srcObject) return Promise.resolve()   // chat.js 提示音
      if (play === 'pending') return new Promise(() => {})
      if (play === 'reject' && !ua.hasBeenActive) return Promise.reject(Object.assign(new Error('play() failed because the user did not interact'), { name: 'NotAllowedError' }))
      this.paused = false
      return Promise.resolve()
    }
    pause() { this.paused = true }
  }
  const globals = { AudioContext: FakeAudioContext, Audio: ProbeAudio, navigator: { userAgent: 'node-test', mediaDevices: {}, userActivation: ua } }
  return { ua, contexts, probes, globals }
}

test('主动探测自动播放：AudioContext 为 suspended 且 resume() 300ms 内未恢复即判受限（即使 play() 会成功），没人说话也显示“点击收听大厅”；点击页面任意处恢复', async () => {
  const ap = autoplayEnv({ ctxState: 'suspended', resume: 'pending', play: 'ok' })
  const h = await bootHall({ globals: ap.globals })
  const room = h.room()
  assert.equal(room.canPlaybackAudio, true, 'livekit 自身尚未发现限制（大厅里没有远端音频）')
  assert.equal(ap.contexts.length, 1, '入厅成功后探测一次')
  assert.equal(ap.contexts[0].resumeCalls, 1, 'suspended 时先尝试 resume()')
  await h.step(200)
  assert.equal(h.id('hallNotice').hidden, true, '300ms 等待期内不下结论')
  await h.step(200)
  assert.equal(h.id('hallNotice').hidden, false)
  assert.equal(h.id('hallNoticeBtn').textContent, '点击收听大厅')
  assert.equal(h.id('hallStatusText').textContent, '大厅 · 1 人在线', '状态仍为已入厅')
  // 主判据已判定受限：不再试播；探测资源全部释放
  assert.equal(ap.probes.length, 0)
  assert.equal(ap.contexts[0].state, 'closed')
  await h.step(500)
  assert.equal(h.env.posted.filter(m => m.type === 'hall_status').at(-1).state, 'blocked')
  // 没有用户激活的合成事件不触发
  h.env.doc.dispatch('pointerdown')
  await h.env.flush()
  assert.equal(room.startAudioCalls, 0)
  // 用户第一次点击页面任意处（浏览器先置 hasBeenActive，再派发事件）
  ap.ua.hasBeenActive = true
  h.env.doc.dispatch('pointerdown')
  await h.env.flush()
  assert.equal(room.startAudioCalls, 1)
  assert.equal(h.id('hallNotice').hidden, true)
  await h.step(500)
  assert.equal(h.env.posted.filter(m => m.type === 'hall_status').at(-1).state, 'live')
  // 恢复后不再监听页面点击；同一页面只探测一次
  h.env.doc.dispatch('click')
  await h.env.flush()
  assert.equal(room.startAudioCalls, 1)
  assert.equal(ap.contexts.length, 1)
})

test('主动探测自动播放：AudioContext 可运行时参考 play()（NotAllowedError 判受限，静音试播且释放资源）', async () => {
  const ap = autoplayEnv({ ctxState: 'running', play: 'reject' })
  const h = await bootHall({ globals: ap.globals })
  await h.env.flush()
  assert.equal(ap.contexts[0].resumeCalls, 0, '已 running 不调用 resume()')
  assert.equal(ap.probes.length, 1)
  assert.equal(ap.probes[0].muted, false)
  assert.equal(ap.probes[0].srcObject, null, '试播结束后解除静音轨道')
  assert.equal(ap.probes[0].paused, true)
  assert.ok(ap.contexts[0].tracks.length === 1 && ap.contexts[0].tracks.every(t => t.stopped))
  assert.equal(ap.contexts[0].state, 'closed')
  assert.equal(h.id('hallNoticeBtn').textContent, '点击收听大厅')
  assert.equal(h.id('hallNotice').hidden, false)
})

test('主动探测自动播放：已有交互、AudioContext 直接 running 且可播放、resume() 及时恢复、play() 不返回或已静音大厅时都不提示', async () => {
  // 页面已有用户交互：不探测
  const active = autoplayEnv({ active: true })
  const a = await bootHall({ globals: active.globals })
  assert.equal(active.contexts.length, 0)
  assert.equal(a.id('hallNotice').hidden, true)
  // 允许自动播放的环境：AudioContext 直接 running，play() 成功
  const allowed = autoplayEnv({ ctxState: 'running', play: 'ok' })
  const b = await bootHall({ globals: allowed.globals })
  await b.step(2000)
  assert.equal(allowed.probes.length, 1)
  assert.equal(b.id('hallNotice').hidden, true)
  assert.equal(b.room().startAudioCalls, 0)
  // suspended 但 resume() 立即恢复：不提示
  const resumed = autoplayEnv({ ctxState: 'suspended', resume: 'ok', play: 'ok' })
  const c = await bootHall({ globals: resumed.globals })
  await c.step(2000)
  assert.equal(resumed.contexts[0].resumeCalls, 1)
  assert.equal(c.id('hallNotice').hidden, true)
  // running 但 play() 一直不返回：超时按允许处理（不误报），并释放资源
  const pending = autoplayEnv({ ctxState: 'running', play: 'pending' })
  const d = await bootHall({ globals: pending.globals })
  await d.step(2000)
  assert.equal(d.id('hallNotice').hidden, true)
  assert.equal(pending.contexts[0].state, 'closed')
  // 访客已静音大厅：不探测、不提示
  const muted = autoplayEnv()
  const { LK } = createFakeLivekit()
  const env = await bootWidget({ hall: true, storage: { cs_visitor_default_hall_muted: '1' }, settings: { hall_enabled: true },
    globals: { LivekitClient: LK, ...muted.globals },
    routes: async (url, opts, respond) => (url.includes('/api/visitor/hall/join') ? respond({ code: 0, data: JOIN }) : null) })
  env.ws().open()
  env.clock.tick(1000); await env.flush()
  env.clock.tick(2000); await env.flush()
  assert.equal(muted.contexts.length, 0)
  assert.equal(env.byId('hallNotice').hidden, true)
})

// ====================== [123] 客服可控的大厅开关（契约第 10 节） ======================
const SW = { enabled: true, video_enabled: false, chat_enabled: true, voice_enabled: true, max_speakers: 6, max_cameras: 2 }
const sw = (o = {}) => ({ type: 'hall_settings', extra: { ...SW, ...o } })
const lastStatus = (env) => env.posted.filter(m => m.type === 'hall_status').at(-1)
const allNodes = (root) => { const out = []; const walk = (n) => { for (const c of n.childNodes) { out.push(c); walk(c) } }; walk(root); return out }
const CACHE_KEY = 'cs_visitor_default_settings'

test('[123] 总开关：加载时关闭 → 无大厅页签、不请求入场、不加载 LiveKit 脚本；运行中打开 → 不刷新恢复页签并自动入厅（脚本只加载一次、重复广播不重复入厅），访客留在私聊页', async () => {
  const { LK, ctl } = createFakeLivekit()
  let joins = 0
  const env = await bootWidget({ hall: true, settings: { hall_enabled: false, hall_video_enabled: false, hall_chat_enabled: false, hall_voice_enabled: false },
    routes: async (url, opts, respond) => (url.includes('/api/visitor/hall/join') ? (joins++, respond({ code: 0, data: JOIN })) : null) })
  const ws = env.ws()
  ws.open()
  const step = async (ms) => { env.clock.tick(ms); await env.flush() }
  const scripts = () => allNodes(env.doc.body).filter(n => n.tagName === 'SCRIPT')
  await step(5000)
  assert.equal(env.byId('tabs').hidden, true, '与上线前一致：没有大厅页签')
  assert.equal(env.byId('privatePane').hidden, false)
  assert.equal(env.byId('hallPane').hidden, true)
  assert.equal(joins, 0)
  assert.equal(scripts().length, 0, '不加载 LiveKit 组件')
  assert.equal(ws.sent.filter(e => e.type === 'hall_subscribe').length, 0)
  assert.equal(lastStatus(env).state, 'off', '宿主页状态胶囊隐藏')
  // 打开（同一广播重复到达两次）
  ws.receive(sw())
  ws.receive(sw())
  assert.equal(env.byId('tabs').hidden, false, '立即恢复大厅页签')
  assert.equal(env.byId('privatePane').hidden, false, '访客正在私聊页：不强制切走')
  assert.equal(env.byId('hallPane').hidden, true)
  await step(4000)
  assert.equal(joins, 1, '错峰后只入厅一次')
  assert.equal(scripts().length, 1, '按需加载 vendor 脚本一次')
  const s = scripts()[0]
  assert.equal(s.src, 'vendor/livekit-client-2.23.0.umd.js')
  assert.match(s.integrity, /^sha384-/)
  env.sandbox.LivekitClient = LK
  s.onload()
  await env.flush()
  assert.equal(ctl.rooms.length, 1)
  assert.ok(ctl.rooms[0].connected)
  assert.equal(ws.sent.filter(e => e.type === 'hall_subscribe').length, 1)
  assert.equal(env.byId('hallInputWrap').hidden, false, '入场后文字聊天按广播的开关恢复')
  assert.equal(env.byId('hallMicBtn').hidden, false)
  // 运行中再次收到相同广播：不重复入厅、不重复连接、不重复加载脚本
  ws.receive(sw())
  await step(6000)
  assert.equal(joins, 1)
  assert.equal(ctl.rooms.length, 1)
  assert.equal(scripts().length, 1)
  assert.equal(lastStatus(env).state, 'live')
})

test('[123] 总开关：运行中关闭 → 收起页签切到私聊、断开 LiveKit、停止重连（含退避中的重试）；重新打开回到关闭前的大厅页，关闭期间有私聊往来或关闭前在私聊页则留在私聊页', async () => {
  const h = await bootHall()
  const onHall = () => h.id('hallPane').hidden === false && h.id('privatePane').hidden === true
  // 媒体断开进入退避重连，此时关闭大厅
  h.ctl.failConnect = true
  h.room().emit('disconnected', 3)
  await h.step(300)
  const joins = h.calls.join.length
  h.ws.receive(sw({ enabled: false }))
  assert.equal(h.id('tabs').hidden, true)
  assert.equal(h.id('privatePane').hidden, false)
  assert.equal(h.id('hallPane').hidden, true)
  assert.ok(h.ws.sent.some(e => e.type === 'hall_unsubscribe'))
  await h.step(300000)
  assert.equal(h.calls.join.length, joins, '关闭后停止重连')
  assert.equal(lastStatus(h.env).state, 'off')
  // 重新打开：关闭前在大厅页 → 回到大厅
  h.ctl.failConnect = false
  h.ws.receive(sw())
  assert.ok(onHall(), '关闭前停留在大厅页：恢复后回到大厅')
  await h.step(4000)
  assert.equal(h.calls.join.length, joins + 1)
  assert.ok(h.room().connected)
  const liveRoom = h.room()
  // 第二轮：关闭期间收到客服私聊 → 留在私聊页
  h.ws.receive(sw({ enabled: false }))
  assert.equal(liveRoom.connected, false, '断开 LiveKit')
  h.ws.receive({ type: 'chat', from: 'agent:7', id: 'm9-p1', conv: 'conv-1', content: '私聊回复', ts: Date.now() })
  h.ws.receive(sw())
  assert.equal(h.id('tabs').hidden, false)
  assert.equal(h.id('privatePane').hidden, false, '关闭期间有私聊往来：不切走')
  await h.step(4000)
  assert.equal(h.calls.join.length, joins + 2)
  // 第三轮：回到大厅页后关闭，访客在私聊页发出消息 → 留在私聊页
  h.id('tabHall').click()
  h.ws.receive(sw({ enabled: false }))
  h.env.input.value = '请问发货了吗'
  h.id('sendBtn').click()
  assert.ok(h.ws.sent.some(e => e.type === 'chat' && e.content === '请问发货了吗'), '私聊照常')
  h.ws.receive(sw())
  assert.equal(h.id('privatePane').hidden, false)
  await h.step(4000)
  // 第四轮：关闭前就在私聊页 → 留在私聊页
  h.id('tabPrivate').click()
  h.ws.receive(sw({ enabled: false }))
  h.ws.receive(sw())
  assert.equal(h.id('privatePane').hidden, false)
  assert.equal(h.id('hallPane').hidden, true)
  await h.step(4000)
  assert.equal(h.calls.join.length, joins + 4)
})

test('[123] 总开关：关闭前已由其他页面收听的页面，重新打开后仍显示「在此页面收听」，不自动抢回', async () => {
  const h = await bootHall()
  h.room().emit('disconnected', 2)
  const n = h.calls.join.length
  h.ws.receive(sw({ enabled: false }))
  h.ws.receive(sw())
  await h.step(10000)
  assert.equal(h.calls.join.length, n)
  assert.equal(h.id('tabs').hidden, false)
  assert.equal(h.id('hallNoticeText').textContent, '您已在其他页面收听大厅')
  h.id('hallNoticeBtn').click()
  await h.step(1000)
  assert.equal(h.calls.join.length, n + 1, '点击后照常接管')
})

test('[123] 文字聊天开关：初始值来自访客公开设置与入场返回；关闭 → 隐藏输入框 / 附件 / 发送并提示，历史照常显示；开启 → 恢复', async () => {
  const h = await bootHall({ settings: { hall_enabled: true, hall_video_enabled: false, hall_chat_enabled: false, hall_voice_enabled: true } })
  const paused = () => h.id('hallChatPaused').hidden === false
  assert.equal(paused(), true, '入场返回没有 chat_enabled（旧后端）时沿用公开设置')
  assert.equal(h.id('hallChatPausedText').textContent, '大厅文字聊天已暂停')
  assert.equal(h.id('hallInputWrap').hidden, true)
  assert.equal(h.id('hallFileBtn').hidden, true)
  assert.equal(h.id('hallSendBtn').hidden, true)
  assert.equal(h.hallMsgs().length, 2, '历史消息照常显示')
  assert.equal(h.id('hallMicBtn').hidden, false, '语音不受影响')
  assert.equal(h.id('hallVoicePaused').hidden, true)
  h.id('hallInput').value = '暂停时发送'
  h.id('hallSendBtn').click()
  assert.equal(h.ws.sent.filter(e => e.type === 'hall_chat').length, 0, '暂停时不发送')
  assert.equal(h.id('hallToast').textContent, '大厅文字聊天已暂停')
  h.ws.receive(sw())
  assert.equal(paused(), false)
  assert.equal(h.id('hallInputWrap').hidden, false)
  assert.equal(h.id('hallFileBtn').hidden, false)
  assert.equal(h.id('hallSendBtn').hidden, false)
  assert.equal(h.id('hallToast').textContent, '大厅文字聊天已恢复')
  h.id('hallSendBtn').click()
  assert.equal(h.ws.sent.filter(e => e.type === 'hall_chat').length, 1, '恢复后保留已输入的内容并可发送')
  h.ws.receive(sw({ chat_enabled: false }))
  assert.equal(paused(), true)
  assert.equal(h.id('hallToast').textContent, '客服已暂停大厅文字聊天')
  assert.equal(h.hallMsgs().length, 3)
  assert.equal(JSON.parse(h.env.sessionStorage.getItem(CACHE_KEY)).hall_chat_enabled, false, '同步本标签页的设置缓存')
  // 入场返回 chat_enabled=false 优先于（可能过期的）公开设置
  const j = await bootHall({ join: (respond) => respond({ code: 0, data: { ...JOIN, chat_enabled: false, voice_enabled: true } }) })
  assert.equal(j.id('hallChatPaused').hidden, false)
  assert.equal(j.id('hallInputWrap').hidden, true)
})

test('[123] 文字聊天被服务端拒绝：hall_chat 回 error「大厅聊天已关闭」或上传 403 40378 时切换为暂停状态', async () => {
  const h = await bootHall()
  h.id('hallInput').value = '你好'
  h.id('hallSendBtn').click()
  const sent = h.ws.sent.filter(e => e.type === 'hall_chat').at(-1)
  h.ws.receive({ type: 'error', id: sent.id, content: '大厅聊天已关闭' })
  assert.equal(h.id('hallChatPaused').hidden, false)
  assert.equal(h.id('hallInputWrap').hidden, true)
  assert.match(h.hallText(), /未发送：大厅聊天已关闭/)
  assert.doesNotMatch(h.env.list.textContent, /大厅聊天已关闭/, '大厅错误不进入私聊区')
  h.env.doc.body.querySelectorAll('.hm-resend')[0].click()
  assert.equal(h.id('hallToast').textContent, '大厅文字聊天已暂停')
  assert.equal(h.ws.sent.filter(e => e.type === 'hall_chat').length, 1, '暂停时重发不发送')
  // 上传 403 40378：切换为暂停，剩余文件不再上传
  const u = await bootHall({ upload: (respond) => respond({ code: 40378, msg: '大厅聊天已关闭' }, 403) })
  u.id('hallFileInput').onchange({ target: { files: [{ name: 'a.pdf', size: 10 }, { name: 'b.pdf', size: 10 }], value: 'x' } })
  u.id('hallSendBtn').click()
  await u.env.flush()
  assert.equal(u.calls.upload.length, 1)
  assert.equal(u.id('hallToast').textContent, '大厅聊天已关闭')
  assert.equal(u.id('hallChatPaused').hidden, false)
  assert.equal(u.ws.sent.filter(e => e.type === 'hall_chat').length, 0)
})

test('[123] 语音开关：关闭 → 隐藏开麦按钮、正在发布的麦克风立即停止且不调用 /hall/mic、提示暂停，仍可收听；开启 → 恢复开麦按钮', async () => {
  const h = await bootHall()
  const room = h.room()
  const p = room.addRemote('a_7', '客服·小王', 'agent')
  const pub = [...p.audioTrackPublications.values()][0]
  h.id('hallMicBtn').click()
  await h.env.flush()
  room.localParticipant.grant([2])
  await h.env.flush()
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'true')
  assert.deepEqual(h.calls.mic, [{ on: true }])
  h.ws.receive(sw({ voice_enabled: false }))
  await h.env.flush()
  assert.equal(h.id('hallMicBtn').hidden, true)
  assert.equal(h.id('hallMicBtn').getAttribute('aria-pressed'), 'false')
  assert.equal(h.id('hallVoicePaused').hidden, false)
  assert.equal(h.id('hallVoicePausedText').textContent, '大厅语音已暂停')
  assert.deepEqual(room.localParticipant.calls.at(-1), ['unpublish', 'audio'], '立即停止发布麦克风')
  assert.deepEqual(h.calls.mic, [{ on: true }], '不再调用 /hall/mic')
  assert.equal(h.id('hallToast').textContent, '客服已暂停大厅语音，您的麦克风已关闭')
  assert.deepEqual(pub.subscribeCalls, [true], '仍可收听（不影响订阅）')
  assert.equal(h.id('hallAudioSink').children.length, 1)
  assert.equal(h.id('hallInputWrap').hidden, false, '文字聊天不受影响')
  await h.step(500)
  assert.equal(lastStatus(h.env).state, 'live')
  h.ws.receive(sw({ voice_enabled: true }))
  assert.equal(h.id('hallMicBtn').hidden, false)
  assert.equal(h.id('hallVoicePaused').hidden, true)
  assert.equal(h.id('hallToast').textContent, '大厅语音已恢复，您可以开麦')
  assert.deepEqual(h.calls.mic, [{ on: true }])
})

test('[123] 开麦被拒 40377 提示「大厅语音已关闭」并隐藏开麦按钮；开麦等待发布权限期间语音被关闭则放弃发布', async () => {
  const h = await bootHall({ mic: (respond, body) => (body.on ? respond({ code: 40377, msg: '大厅语音已关闭' }, 403) : respond({ code: 0, data: { on: false } })) })
  h.id('hallMicBtn').click()
  await h.env.flush()
  assert.equal(h.id('hallToast').textContent, '大厅语音已关闭')
  assert.equal(h.id('hallMicBtn').hidden, true)
  assert.equal(h.id('hallVoicePaused').hidden, false)
  assert.equal(h.room().localParticipant.calls.length, 0)
  assert.equal(JSON.parse(h.env.sessionStorage.getItem(CACHE_KEY)).hall_voice_enabled, false)
  const g = await bootHall()
  g.id('hallMicBtn').click()
  await g.env.flush()
  assert.deepEqual(g.calls.mic, [{ on: true }])
  g.ws.receive(sw({ voice_enabled: false }))
  g.room().localParticipant.grant([2])   // 权限下发晚于开关广播
  await g.step(6000)
  assert.equal(g.room().localParticipant.calls.filter(c => c[0] === 'mic').length, 0, '放弃发布')
  assert.equal(g.id('hallMicBtn').getAttribute('aria-pressed'), 'false')
  assert.deepEqual(g.calls.mic, [{ on: true }], '不再调用 /hall/mic')
})

test('[123] 视频与新开关组合：文字、语音都关闭时摄像头按钮仍按视频开关显示；关闭视频收回摄像头；40375 同步隐藏摄像头按钮', async () => {
  const h = await bootHall()
  h.ws.receive(sw({ chat_enabled: false, voice_enabled: false, video_enabled: true }))
  assert.equal(h.id('hallCamBtn').hidden, false)
  assert.equal(h.id('hallMicBtn').hidden, true)
  assert.equal(h.id('hallInputWrap').hidden, true)
  assert.equal(h.id('hallChatPaused').hidden, false)
  assert.equal(h.id('hallVoicePaused').hidden, false)
  assert.equal(h.id('hallToast').textContent, '客服已暂停大厅文字聊天；客服已暂停大厅语音；客服已开启大厅视频，您可以打开摄像头')
  h.id('hallCamBtn').click()
  await h.env.flush()
  h.room().localParticipant.grant([1])
  await h.env.flush()
  assert.equal(h.id('hallCamBtn').getAttribute('aria-pressed'), 'true')
  h.ws.receive(sw({ chat_enabled: false, voice_enabled: false, video_enabled: false }))
  await h.env.flush()
  assert.equal(h.id('hallCamBtn').hidden, true)
  assert.equal(h.id('hallCamBtn').getAttribute('aria-pressed'), 'false')
  assert.equal(h.id('hallToast').textContent, '客服已关闭大厅视频，您的摄像头已关闭')
  h.ws.receive(sw())
  assert.equal(h.id('hallMicBtn').hidden, false)
  assert.equal(h.id('hallInputWrap').hidden, false)
  assert.equal(h.id('hallChatPaused').hidden, true)
  assert.equal(h.id('hallVoicePaused').hidden, true)
  assert.equal(h.id('hallCamBtn').hidden, true)
  // 摄像头接口 40375（广播丢失时的兜底）：隐藏摄像头按钮
  const c = await bootHall({ join: (respond) => respond({ code: 0, data: { ...JOIN, video_enabled: true } }), camera: (respond, body) => (body.on ? respond({ code: 40375, msg: 'video off' }, 403) : respond({ code: 0, data: { on: false } })) })
  assert.equal(c.id('hallCamBtn').hidden, false)
  c.id('hallCamBtn').click()
  await c.env.flush()
  assert.equal(c.id('hallToast').textContent, '大厅暂未开启视频，暂时不能开摄像头')
  assert.equal(c.id('hallCamBtn').hidden, true)
})

test('[123] 公开设置缓存：开关变化同步到本标签页缓存；缓存显示大厅关闭时本页向服务端确认一次，服务端不可用时退回缓存', async () => {
  const h = await bootHall()
  const cache = () => JSON.parse(h.env.sessionStorage.getItem(CACHE_KEY))
  assert.equal(cache().hall_enabled, true)
  h.ws.receive(sw({ enabled: false }))
  assert.equal(cache().hall_enabled, false)
  assert.equal(cache().hall_chat_enabled, false)
  const now = Date.parse('2026-10-14T12:00:00Z')
  const offCache = { [CACHE_KEY]: JSON.stringify({ hall_enabled: false, hall_video_enabled: false, hall_chat_enabled: false, hall_voice_enabled: false }), [CACHE_KEY + '_at']: String(now - 60000) }
  const settingsCalls = (env) => env.fetchCalls.filter(c => c.url.includes('/api/visitor/settings')).length
  const boot = async (sessionStore, routes) => {
    const { LK } = createFakeLivekit()
    let joins = 0
    const env = await bootWidget({ hall: true, now, sessionStore, settings: { hall_enabled: true, hall_chat_enabled: true, hall_voice_enabled: true }, globals: { LivekitClient: LK },
      routes: async (url, opts, respond) => (url.includes('/api/visitor/hall/join') ? (joins++, respond({ code: 0, data: JOIN })) : (routes ? routes(url, respond) : null)) })
    env.ws().open()
    env.clock.tick(1000)
    await env.flush()
    return { env, joins: () => joins }
  }
  // 缓存（未过期）显示关闭、服务端已重新打开：向服务端确认后显示大厅并入厅
  const a = await boot(offCache)
  assert.equal(settingsCalls(a.env), 1)
  assert.equal(a.env.byId('tabs').hidden, false)
  assert.equal(a.joins(), 1)
  assert.equal(JSON.parse(a.env.sessionStorage.getItem(CACHE_KEY)).hall_enabled, true, '缓存刷新为服务端最新值')
  // 缓存显示开启：照常使用缓存，不请求
  const b = await boot({ [CACHE_KEY]: JSON.stringify({ hall_enabled: true }), [CACHE_KEY + '_at']: String(now - 60000) })
  assert.equal(settingsCalls(b.env), 0)
  assert.equal(b.joins(), 1)
  // 服务端不可用：退回缓存（大厅关闭），不入厅
  const c = await boot(offCache, (url, respond) => (url.includes('/api/visitor/settings') ? respond({ code: 50050, msg: '服务器繁忙' }, 500) : null))
  assert.equal(settingsCalls(c.env), 1)
  assert.equal(c.env.byId('tabs').hidden, true)
  assert.equal(c.joins(), 0)
})

