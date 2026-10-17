import test from 'node:test'
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { webcrypto } from 'node:crypto'

// [119] 大厅纯逻辑模块（hall-core.js）单元测试：消息合并去重、重连退避、错误码文案、跨窗口消息校验等
const require = createRequire(import.meta.url)
const Core = require('../public/hall-core.js')
const ORIGIN = 'https://cs.example.com'

function hm(id, sec, extra = {}) {
  return { id, sender: 'visitor', identity: 'v_' + id, name: '访客 1234', role: 'visitor', content: '内容' + id, media_url: '', media_kind: '', media_name: '', media_size: 0, created_at: new Date(Date.UTC(2026, 9, 17, 4, 0, sec)).toISOString().replace('Z', '+00:00'), ...extra }
}

test('normalizeMessage：同时接受 HTTP HallMessage 与 WS 广播字段（media/mkind/mname/msize + extra）', () => {
  const a = Core.normalizeMessage(hm('m1', 1, { media_url: '/files/2026/10/17/a.png?access=x', media_kind: 'image', media_name: 'a.png', media_size: 2048 }), { origin: ORIGIN })
  assert.equal(a.mediaUrl, '/files/2026/10/17/a.png?access=x')
  assert.equal(a.mediaKind, 'image')
  assert.equal(a.mediaSize, 2048)
  assert.equal(a.ts, Date.UTC(2026, 9, 17, 4, 0, 1))
  const ws = Core.normalizeMessage({ type: 'hall_chat', id: '8e0c5b2a-1f43-4d8e-9b1a-2c3d4e5f6a7b', content: '', media: '/files/x.pdf?access=y', mkind: 'file', mname: '报价.pdf', msize: 99, ts: 1760673600000, extra: { sender: 'agent', identity: 'a_7', name: '客服·小王', role: 'agent' } }, { origin: ORIGIN })
  assert.equal(ws.sender, 'agent')
  assert.equal(ws.role, 'agent')
  assert.equal(ws.name, '客服·小王')
  assert.equal(ws.mediaKind, 'file')
  assert.equal(ws.mediaName, '报价.pdf')
  assert.equal(ws.ts, 1760673600000)
  // 秒级时间戳也能识别
  assert.equal(Core.normalizeMessage({ id: 'x1', sender: 'system', content: '公告', ts: 1760673600 }).ts, 1760673600000)
})

test('normalizeMessage：拒绝非法 ID、非本站媒体地址、空消息与未知发送者', () => {
  assert.equal(Core.normalizeMessage({ id: '../x', sender: 'visitor', content: 'a' }), null)
  assert.equal(Core.normalizeMessage({ id: '"><img>', sender: 'visitor', content: 'a' }), null)
  assert.equal(Core.normalizeMessage({ id: 'ok1', sender: 'hacker', content: 'a' }), null)
  assert.equal(Core.normalizeMessage({ id: 'ok2', sender: 'visitor', content: '' }), null)
  const ext = Core.normalizeMessage({ id: 'ok3', sender: 'visitor', content: '', media_url: 'https://evil.example/files/a.png', media_kind: 'image' }, { origin: ORIGIN })
  assert.equal(ext, null, '外站媒体且无文字 → 整条丢弃')
  const js = Core.normalizeMessage({ id: 'ok4', sender: 'visitor', content: '看图', media_url: 'javascript:alert(1)', media_kind: 'image' }, { origin: ORIGIN })
  assert.equal(js.mediaUrl, '')
  assert.equal(js.mediaKind, '')
  const notFiles = Core.normalizeMessage({ id: 'ok5', sender: 'visitor', content: 'x', media_url: '/api/agent/export', media_kind: 'file' }, { origin: ORIGIN })
  assert.equal(notFiles.mediaUrl, '')
  // sql.NullString 形态兼容
  const ns = Core.normalizeMessage({ id: 'ok6', sender: 'visitor', content: { String: '你好', Valid: true } })
  assert.equal(ns.content, '你好')
})

test('MessageStore：按时间排序、同 ID 去重更新、删除后迟到的重放不复活、容量淘汰最旧', () => {
  const s = new Core.MessageStore(3)
  const n = (id, sec) => Core.normalizeMessage(hm(id, sec), { origin: ORIGIN })
  assert.equal(s.upsert(n('a', 10)).action, 'insert')
  assert.equal(s.upsert(n('c', 30)).index, 1)
  const mid = s.upsert(n('b', 20))
  assert.equal(mid.action, 'insert')
  assert.equal(mid.index, 1)
  assert.deepEqual(s.items.map(m => m.id), ['a', 'b', 'c'])
  const dup = s.upsert({ ...n('b', 20), content: '更新后' })
  assert.equal(dup.action, 'update')
  assert.equal(s.size(), 3)
  assert.equal(s.get('b').content, '更新后')
  // 本地回显（本地时间）被服务端时间更正后移动到正确位置
  const moved = s.upsert({ ...n('a', 40) })
  assert.equal(moved.moved, true)
  assert.deepEqual(s.items.map(m => m.id), ['b', 'c', 'a'])
  // 超出容量淘汰最旧
  const ins = s.upsert(n('d', 50))
  assert.deepEqual(ins.evicted, ['b'])
  assert.deepEqual(s.items.map(m => m.id), ['c', 'a', 'd'])
  // 比保留窗口更旧的消息直接忽略
  assert.equal(s.upsert(n('old', 1)).action, 'ignore')
  assert.deepEqual(s.items.map(m => m.id), ['c', 'a', 'd'])
  // 删除 + 墓碑
  assert.equal(s.remove('a'), true)
  assert.equal(s.upsert(n('a', 40)).action, 'ignore')
  assert.equal(s.isDeleted('a'), true)
  assert.equal(s.remove('../bad'), false)
  assert.equal(s.oldestId(), 'c')
})

test('MessageStore：批量合并历史 50 条与实时消息去重', () => {
  const s = new Core.MessageStore(200)
  const hist = Array.from({ length: 50 }, (_, i) => Core.normalizeMessage(hm('h' + i, i), { origin: ORIGIN }))
  hist.forEach(m => s.upsert(m))
  hist.slice(40).forEach(m => s.upsert(m))   // 补拉时重复的 10 条
  assert.equal(s.size(), 50)
  s.upsert(Core.normalizeMessage(hm('live', 60), { origin: ORIGIN }))
  assert.equal(s.items.at(-1).id, 'live')
  assert.equal(s.size(), 51)
})

test('backoffDelay：指数增长、抖动受控、有上限、尊重最小等待', () => {
  const fixed = (r) => () => r
  const opts = { baseMs: 2000, factor: 2, maxMs: 60000, jitter: 0.2 }
  assert.equal(Core.backoffDelay(0, { ...opts, random: fixed(0.5) }), 2000)
  assert.equal(Core.backoffDelay(1, { ...opts, random: fixed(0.5) }), 4000)
  assert.equal(Core.backoffDelay(3, { ...opts, random: fixed(0.5) }), 16000)
  assert.equal(Core.backoffDelay(0, { ...opts, random: fixed(0) }), 1600)
  assert.equal(Core.backoffDelay(0, { ...opts, random: fixed(1) }), 2400)
  for (let i = 0; i < 100; i++) assert.ok(Core.backoffDelay(i, { ...opts, random: Math.random }) <= 60000)
  assert.equal(Core.backoffDelay(50, { ...opts, random: fixed(1) }), 60000, '抖动后也不超过上限')
  assert.equal(Core.backoffDelay(0, { ...opts, random: fixed(0.5), minMs: 30000 }), 30000)
  assert.ok(Core.backoffDelay(-5, { random: fixed(0.5) }) >= 250)
})

test('errorText：契约错误码映射为中文提示', () => {
  assert.match(Core.errorText(40371), /大厅已关闭/)
  assert.equal(Core.errorText(40372, { data: { retry_after: 1800 } }), '您已被移出大厅，约 30 分钟后可重新进入')
  assert.equal(Core.errorText(40372, { data: { retry_after: 20 } }), '您已被移出大厅，约 1 分钟后可重新进入')
  assert.equal(Core.errorText(40374, { action: 'mic' }), '当前无法开麦')
  assert.equal(Core.errorText(40374, { action: 'camera' }), '当前无法开启摄像头')
  assert.match(Core.errorText(40375), /未开启视频/)
  assert.equal(Core.errorText(40973, { data: { used: 6, max: 6 } }), '开麦人数已满（6/6），请稍后再试')
  assert.equal(Core.errorText(40973), '开麦人数已满，请稍后再试')
  assert.match(Core.errorText(40976), /摄像头人数已满/)
  assert.match(Core.errorText(40977), /大厅人数已满/)
  assert.equal(Core.errorText(42910), '操作过于频繁，请稍后再试')
  assert.match(Core.errorText(41501), /不支持的文件类型/)
  assert.match(Core.errorText(-2), /网络异常/)
  // 后端实现补充的错误码
  assert.equal(Core.errorText(40110), '登录状态已失效，请刷新页面后重试')
  assert.match(Core.errorText(40059), /大厅设置不合法/)
  assert.match(Core.errorText(40060), /参数错误/)
  assert.match(Core.errorText(40061, { action: 'upload' }), /^文件发送失败/)
  assert.equal(Core.errorText(50370, { action: 'mic' }), '语音服务暂不可用，请稍后再试')
  assert.match(Core.errorText(50370, { action: 'join' }), /稍后自动重试/)
  assert.match(Core.errorText(40976, { action: 'camera' }), /摄像头人数已满/)
  assert.match(Core.errorText(40376), /不能对该对象/)
  assert.match(Core.errorText(40477), /已不存在/)
  // 未知错误：服务端文案只作纯文本，含尖括号/过长的不展示
  assert.equal(Core.errorText(50000, { action: 'upload', msg: '磁盘已满' }), '文件发送失败：磁盘已满')
  assert.equal(Core.errorText(50000, { msg: '<img src=x onerror=alert(1)>' }), '操作失败，请稍后再试')
  assert.equal(Core.errorText(50000, { msg: 'x'.repeat(200) }), '操作失败，请稍后再试')
})

test('moderationText / mediaErrorText', () => {
  assert.equal(Core.moderationText('muted'), '客服已将您静音')
  assert.equal(Core.moderationText('mute_all'), '客服已将全体访客静音')
  assert.equal(Core.moderationText('camera_off'), '客服已关闭您的摄像头')
  assert.equal(Core.moderationText('kicked', 30), '您已被客服移出大厅，30 分钟后可重新进入')
  assert.equal(Core.moderationText('unknown'), '')
  assert.match(Core.mediaErrorText({ name: 'NotAllowedError' }, 'mic'), /允许麦克风权限/)
  assert.match(Core.mediaErrorText({ name: 'NotFoundError' }, 'camera'), /没有找到可用的摄像头/)
  assert.match(Core.mediaErrorText({ name: 'NotReadableError' }, 'mic'), /占用/)
})

test('normalizeSettings：兼容布尔/字符串、取值范围收敛、缺省沿用旧值', () => {
  const d = Core.normalizeSettings(null)
  assert.deepEqual(d, { enabled: true, videoEnabled: false, maxSpeakers: 6, maxCameras: 2 })
  assert.deepEqual(Core.normalizeSettings({ hall_enabled: 'false', hall_video_enabled: 'true' }), { enabled: false, videoEnabled: true, maxSpeakers: 6, maxCameras: 2 })
  assert.deepEqual(Core.normalizeSettings({ enabled: true, video_enabled: false, max_speakers: 99, max_cameras: -1 }), { enabled: true, videoEnabled: false, maxSpeakers: 20, maxCameras: 0 })
  assert.deepEqual(Core.normalizeSettings({ video_enabled: 'yes' }, { enabled: false, videoEnabled: true, maxSpeakers: 3, maxCameras: 1 }), { enabled: false, videoEnabled: true, maxSpeakers: 3, maxCameras: 1 })
})

test('validateJoinData：校验 LiveKit 地址协议、令牌与 ICE 服务器白名单', () => {
  const base = { url: 'wss://cs.example.com/lk', token: 'eyJ.a.b', room: 'hall_default', identity: 'v_abc', name: '访客 1234', video_enabled: true, max_speakers: 6, max_cameras: 2,
    ice_servers: [{ urls: ['turn:cs.example.com:3478?transport=udp', 'javascript:x'], username: 'u', credential: 'c' }, { urls: 'stun:cs.example.com:3478' }, { urls: ['http://x'] }], history: [] }
  const ok = Core.validateJoinData(base, 'https:')
  assert.equal(ok.identity, 'v_abc')
  assert.equal(ok.settings.videoEnabled, true)
  assert.deepEqual(ok.iceServers, [{ urls: ['turn:cs.example.com:3478?transport=udp'], username: 'u', credential: 'c' }, { urls: ['stun:cs.example.com:3478'] }])
  assert.equal(Core.validateJoinData({ ...base, url: 'ws://cs.example.com/lk' }, 'https:'), null, 'https 页面禁止明文 ws')
  assert.ok(Core.validateJoinData({ ...base, url: 'ws://127.0.0.1:7880' }, 'http:'), 'http 测试环境允许 ws')
  assert.equal(Core.validateJoinData({ ...base, url: 'https://cs.example.com/lk' }, 'https:'), null)
  assert.equal(Core.validateJoinData({ ...base, url: 'wss://user:pass@cs.example.com/lk' }, 'https:'), null)
  assert.equal(Core.validateJoinData({ ...base, token: '' }, 'https:'), null)
  assert.equal(Core.validateJoinData({ ...base, token: 'a b' }, 'https:'), null)
  assert.equal(Core.validateJoinData({ ...base, identity: '' }, 'https:'), null)
  const many = Core.validateJoinData({ ...base, history: Array.from({ length: 80 }, (_, i) => ({ id: 'h' + i })) }, 'https:')
  assert.equal(many.history.length, 50)
  assert.equal(many.history[0].id, 'h30')
})

test('参与者：metadata 白名单解析、头像首字、排序、视频订阅挑选', () => {
  assert.deepEqual(Core.parseMetadata('{"role":"agent","code":"0042","vid":"secret"}'), { role: 'agent', code: '0042' })
  assert.deepEqual(Core.parseMetadata('{"role":"admin"}'), { role: 'visitor', code: '' })
  assert.deepEqual(Core.parseMetadata('not json'), { role: 'visitor', code: '' })
  assert.equal(Core.avatarText('客服·小王', 'agent'), '小')
  assert.equal(Core.avatarText('客服·alice', 'agent'), 'A')
  assert.equal(Core.avatarText('客服·', 'agent'), '客')
  assert.equal(Core.avatarText('访客 1234', 'visitor'), '')
  const sorted = Core.sortParticipants([
    { identity: 'v1', role: 'visitor', joinedAt: 1 },
    { identity: 'v2', role: 'visitor', micOn: true, joinedAt: 2 },
    { identity: 'a1', role: 'agent', joinedAt: 3 },
    { identity: 'me', role: 'visitor', isLocal: true, joinedAt: 9 },
    { identity: 'v3', role: 'visitor', speaking: true, joinedAt: 4 },
  ])
  assert.deepEqual(sorted.map(p => p.identity), ['me', 'a1', 'v3', 'v2', 'v1'])
  const cands = [{ sid: 't1', role: 'visitor', publishedAt: 1 }, { sid: 't2', role: 'visitor', publishedAt: 5 }, { sid: 't3', role: 'agent', publishedAt: 2 }]
  assert.deepEqual(Core.pickVideoSubscriptions(cands, 2, true), ['t3', 't2'])
  assert.deepEqual(Core.pickVideoSubscriptions(cands, 2, false), [], '大厅页不可见时一律不订阅视频')
  assert.deepEqual(Core.pickVideoSubscriptions(cands, 0, true), [])
})

test('classifyDisconnect：区分主动断开、被接管、被移出、房间关闭与可重试', () => {
  const DR = { CLIENT_INITIATED: 1, DUPLICATE_IDENTITY: 2, SERVER_SHUTDOWN: 3, PARTICIPANT_REMOVED: 4, ROOM_DELETED: 5, ROOM_CLOSED: 10 }
  assert.equal(Core.classifyDisconnect(1, DR), 'client')
  assert.equal(Core.classifyDisconnect(2, DR), 'elsewhere')
  assert.equal(Core.classifyDisconnect(4, DR), 'removed')
  assert.equal(Core.classifyDisconnect(5, DR), 'closed')
  assert.equal(Core.classifyDisconnect(3, DR), 'retry')
  assert.equal(Core.classifyDisconnect(undefined, DR), 'retry')
  assert.equal(Core.classifyDisconnect(2), 'elsewhere', '缺少枚举时使用协议数值')
})

test('SlidingWindow：5 条 / 10 秒', () => {
  const w = new Core.SlidingWindow(5, 10000)
  for (let i = 0; i < 5; i++) assert.equal(w.tryTake(1000 + i), true)
  assert.equal(w.tryTake(1005), false)
  assert.equal(w.retryInMs(1005), 9995)
  assert.equal(w.tryTake(11000), true)
})

test('跨窗口消息校验：宿主页命令、宿主 origin 推断、多标签页接管、状态消息构造', () => {
  assert.deepEqual(Core.validateHostCommand({ __cs: 1, type: 'hall_cmd', cmd: 'start_audio' }), { cmd: 'start_audio' })
  assert.equal(Core.validateHostCommand({ __cs: 1, type: 'hall_cmd', cmd: 'eval' }), null)
  assert.equal(Core.validateHostCommand({ __cs: '1', type: 'hall_cmd', cmd: 'start_audio' }), null)
  assert.equal(Core.validateHostCommand({ type: 'hall_cmd', cmd: 'start_audio' }), null)
  assert.equal(Core.validateHostCommand('{"__cs":1}'), null)
  assert.equal(Core.hostOriginFrom(['https://shop.example'], ''), 'https://shop.example')
  assert.equal(Core.hostOriginFrom(null, 'https://shop.example/a/b?c=1'), 'https://shop.example')
  assert.equal(Core.hostOriginFrom(['javascript:alert(1)'], 'file:///etc/passwd'), '')
  assert.deepEqual(Core.validateTabMessage({ type: 'hall_takeover', tab: '0f8fad5b-d9cb-469f-a165-70867728950e', at: 5 }), { tab: '0f8fad5b-d9cb-469f-a165-70867728950e', at: 5 })
  assert.equal(Core.validateTabMessage({ type: 'hall_takeover', tab: '<x>', at: 5 }), null)
  assert.equal(Core.validateTabMessage({ type: 'hall_takeover', tab: '0f8fad5b-d9cb-469f-a165-70867728950e' }), null)
  assert.deepEqual(Core.buildHallStatus({ state: 'live', online: 12.7, speaking: 1 }), { __cs: 1, type: 'hall_status', state: 'live', online: 12, speaking: true })
  assert.deepEqual(Core.buildHallStatus({ state: 'pwned', online: -5 }), { __cs: 1, type: 'hall_status', state: 'off', online: 0, speaking: false })
})

test('上传前置校验与后端白名单一致；UUID v4 生成', () => {
  assert.equal(Core.checkUploadFile({ name: 'a.PNG', size: 10 }), '')
  assert.equal(Core.checkUploadFile({ name: '报价.xlsx', size: 10 }), '')
  assert.match(Core.checkUploadFile({ name: 'a.svg', size: 10 }), /不支持的文件类型/)
  assert.match(Core.checkUploadFile({ name: 'a.html', size: 10 }), /不支持的文件类型/)
  assert.match(Core.checkUploadFile({ name: 'noext', size: 10 }), /不支持的文件类型/)
  assert.match(Core.checkUploadFile({ name: 'a.jpg', size: 0 }), /文件为空/)
  assert.match(Core.checkUploadFile({ name: 'a.jpg', size: 21 * 1024 * 1024 }), /不能超过 20 MB/)
  assert.equal(Core.isImageFile({ name: 'x.webp' }), true)
  assert.equal(Core.isImageFile({ name: 'x.pdf' }), false)
  const id1 = Core.newUUIDv4(webcrypto)
  const id2 = Core.newUUIDv4({ getRandomValues: (b) => webcrypto.getRandomValues(b) })
  assert.ok(Core.isUUIDv4(id1) && Core.isUUIDv4(id2), id1 + ' ' + id2)
  assert.notEqual(id1, id2)
  assert.throws(() => Core.newUUIDv4({}), /安全随机数/)
  // 扩展 B 区汉字占两个 UTF-16 码元：按码点计数与截断，不截出半个字符
  assert.equal(Core.textLength('a\u{20BB7}'), 2)
  assert.equal(Core.clampText('\u{20BB7}\u{20BB7}', 1), '\u{20BB7}')
  assert.equal(Core.clampText('一二三四', 2), '一二')
})
