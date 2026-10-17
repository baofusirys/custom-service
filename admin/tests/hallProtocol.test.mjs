import test from 'node:test'
import assert from 'node:assert/strict'

import {
  KICK_MINUTES_OPTIONS,
  buildModerateRequest,
  hasPublishPermission,
  kickMinutesLabel,
  normalizeHallState,
  normalizeIceServers,
  normalizeJoinResponse,
  validateLiveKitUrl,
} from '../src/modules/hallProtocol.js'

const TOKEN = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhXzcifQ.c2ln'
const join = (extra = {}) => ({
  url: 'wss://k7m2x9p3.example.com/lk', token: TOKEN, room: 'hall_default', identity: 'a_7', name: '客服·小王',
  video_enabled: true, max_speakers: 6, max_cameras: 2,
  ice_servers: [{ urls: ['turn:example.com:3478?transport=udp'], username: 'u', credential: 'c' }],
  history: [{ id: '00000000-0000-4000-8000-000000000001', sender: 'visitor', identity: 'v_0123456789abcdef0123', name: '访客 0001', role: 'visitor', content: '你好', created_at: '2026-10-17T10:00:00+08:00' }],
  ...extra,
})

test('LiveKit 地址：只允许 wss；http 页面（本地开发）才允许 ws；拒绝带账号密码的地址', () => {
  assert.equal(validateLiveKitUrl('wss://a.com/lk'), 'wss://a.com/lk')
  assert.equal(validateLiveKitUrl('ws://127.0.0.1:7880'), '')
  assert.equal(validateLiveKitUrl('ws://127.0.0.1:7880', 'http:'), 'ws://127.0.0.1:7880/')
  assert.equal(validateLiveKitUrl('https://a.com/lk'), '')
  assert.equal(validateLiveKitUrl('javascript:alert(1)'), '')
  assert.equal(validateLiveKitUrl('wss://user:pass@a.com/lk'), '')
  assert.equal(validateLiveKitUrl('wss://' + 'a'.repeat(600)), '')
  assert.equal(validateLiveKitUrl(null), '')
})

test('ICE 服务器：最多 4 组、只接受 stun/turn/turns', () => {
  const out = normalizeIceServers([
    { urls: 'turn:a.com:3478', username: 'u', credential: 'c' },
    { urls: ['http://evil', 'stun:b.com'] },
    { urls: ['javascript:x'] },
    null, { urls: 'turns:c.com:5349' }, { urls: 'stun:d.com' }, { urls: 'stun:e.com' },
  ])
  assert.deepEqual(out, [
    { urls: ['turn:a.com:3478'], username: 'u', credential: 'c' },
    { urls: ['stun:b.com'] },
    { urls: ['turns:c.com:5349'] },
    { urls: ['stun:d.com'] },
  ])
  assert.deepEqual(normalizeIceServers('x'), [])
})

test('join 响应校验', () => {
  const ok = normalizeJoinResponse(join())
  assert.equal(ok.ok, true)
  assert.equal(ok.value.identity, 'a_7')
  assert.equal(ok.value.videoEnabled, true)
  assert.equal(ok.value.history.length, 1)
  assert.equal(ok.value.iceServers.length, 1)
  assert.equal(normalizeJoinResponse(join({ url: 'ws://a.com' })).ok, false)
  assert.equal(normalizeJoinResponse(join({ token: 'not-a-jwt' })).ok, false)
  assert.equal(normalizeJoinResponse(join({ token: TOKEN + '<' })).ok, false)
  assert.equal(normalizeJoinResponse(join({ identity: 'v_0123456789abcdef0123' })).ok, false)
  assert.equal(normalizeJoinResponse(null).ok, false)
  const fallback = normalizeJoinResponse(join({ room: '../x', max_speakers: 99, max_cameras: 'x', history: 'bad' }))
  assert.equal(fallback.value.room, 'hall_default')
  assert.equal(fallback.value.maxSpeakers, 6)
  assert.equal(fallback.value.maxCameras, 2)
  assert.deepEqual(fallback.value.history, [])
  assert.match(normalizeJoinResponse(join({ url: 'ws://a.com' })).error, /[\u4e00-\u9fa5]/)
  // [123] 入场返回的文字聊天 / 语音开关：只采纳布尔，缺失或非法为 undefined
  const sw = normalizeJoinResponse(join({ chat_enabled: false, voice_enabled: true }))
  assert.equal(sw.value.chatEnabled, false)
  assert.equal(sw.value.voiceEnabled, true)
  const legacy = normalizeJoinResponse(join({ chat_enabled: 'off' }))
  assert.equal(legacy.value.chatEnabled, undefined)
  assert.equal(legacy.value.voiceEnabled, undefined)
})

test('state 响应容错', () => {
  const st = normalizeHallState({ settings: { hall_enabled: 'true', hall_video_enabled: 'false', hall_max_speakers: '6', hall_max_cameras: '2', hall_max_participants: '100' }, counts: { participants: 1 }, participants: [{ identity: 'a_1' }] })
  assert.deepEqual(st.settings, { enabled: true, videoEnabled: false, maxSpeakers: 6, maxCameras: 2, maxParticipants: 100 })
  assert.equal(st.participants.length, 1)
  assert.equal(st.degraded, false)
  assert.equal(normalizeHallState({ degraded: true }).degraded, true)
  assert.equal(normalizeHallState({ degraded: 'true' }).degraded, false)
  assert.deepEqual(normalizeHallState(null), { settings: null, counts: null, participants: [], degraded: false })
})

test('管理操作请求体校验：动作白名单、身份格式、禁入分钟数 1–1440', () => {
  assert.deepEqual(buildModerateRequest({ action: 'mute_all', identity: 'ignored' }), { ok: true, body: { action: 'mute_all' } })
  assert.deepEqual(buildModerateRequest({ action: 'mute', identity: 'v_0123456789abcdef0123' }), { ok: true, body: { action: 'mute', identity: 'v_0123456789abcdef0123' } })
  assert.deepEqual(buildModerateRequest({ action: 'kick', identity: 'v_0123456789abcdef0123' }).body.minutes, 30)
  assert.equal(buildModerateRequest({ action: 'kick', identity: 'v_0123456789abcdef0123', minutes: 1440 }).body.minutes, 1440)
  for (const m of [0, 1441, 1.5, '10', -5]) assert.equal(buildModerateRequest({ action: 'kick', identity: 'v_0123456789abcdef0123', minutes: m }).ok, m === '10', String(m))
  assert.equal(buildModerateRequest({ action: 'drop_table', identity: 'v_0123456789abcdef0123' }).ok, false)
  assert.equal(buildModerateRequest({ action: 'mute', identity: "v_1' OR 1=1" }).ok, false)
  assert.equal(buildModerateRequest().ok, false)
  assert.deepEqual(KICK_MINUTES_OPTIONS, [10, 30, 60, 1440])
  assert.deepEqual(KICK_MINUTES_OPTIONS.map(kickMinutesLabel), ['10 分钟', '30 分钟', '1 小时', '1 天'])
})

test('发布权限判断（livekit.TrackSource：CAMERA=1，MICROPHONE=2）', () => {
  assert.equal(hasPublishPermission({ canPublish: true, canPublishSources: [] }, 'camera'), true)
  assert.equal(hasPublishPermission({ canPublish: true, canPublishSources: [2] }, 'camera'), false)
  assert.equal(hasPublishPermission({ canPublish: true, canPublishSources: [2] }, 'microphone'), true)
  assert.equal(hasPublishPermission({ canPublish: true, canPublishSources: [1, 2] }, 'camera'), true)
  assert.equal(hasPublishPermission({ canPublish: false, canPublishSources: [] }, 'camera'), false)
  assert.equal(hasPublishPermission(null, 'camera'), false)
  assert.equal(hasPublishPermission({ canPublish: true, canPublishSources: [] }, 'screen'), false)
})
