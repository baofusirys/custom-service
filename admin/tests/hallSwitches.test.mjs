import test from 'node:test'
import assert from 'node:assert/strict'

import {
  HALL_MASTER_OFF_CONFIRM,
  HALL_SWITCHES,
  HALL_SWITCH_DEFAULTS,
  HALL_SWITCH_FIELDS,
  HALL_SWITCH_TEXT,
  buildSwitchRequest,
  displaySwitches,
  effectiveSwitches,
  formatSwitchUpdated,
  mergeSwitches,
  needsSwitchConfirm,
  normalizeSwitchesView,
  parseSwitchBool,
  parseSwitchesPatch,
  pickSwitches,
  sanitizeSwitchActor,
  sanitizeSwitchTime,
  switchMetaOf,
  switchToggleMessage,
  switchTransitions,
} from '../src/modules/hallSwitches.js'
import { normalizeHallSettingsEvent } from '../src/modules/hallSettings.js'

const CJK = /[\u4e00-\u9fa5]/
const view = (over = {}) => ({
  enabled: true, chat_enabled: true, voice_enabled: true, video_enabled: false,
  updated_by: '客服·小王', updated_at: '2026-10-18T03:12:05.000+08:00', ...over,
})

test('四个开关与契约第 10 节一致：字段、接口键、设置键、默认值', () => {
  assert.deepEqual(HALL_SWITCH_FIELDS, ['enabled', 'chatEnabled', 'voiceEnabled', 'videoEnabled'])
  assert.deepEqual(HALL_SWITCHES.map(s => s.apiKey), ['enabled', 'chat_enabled', 'voice_enabled', 'video_enabled'])
  assert.deepEqual(HALL_SWITCHES.map(s => s.settingKey), ['hall_enabled', 'hall_chat_enabled', 'hall_voice_enabled', 'hall_video_enabled'])
  assert.deepEqual(HALL_SWITCH_DEFAULTS, { enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: false })
  for (const s of HALL_SWITCHES) {
    for (const k of ['label', 'onText', 'offText', 'description']) assert.match(s[k], CJK, s.field + '.' + k)
  }
  assert.equal(HALL_SWITCH_TEXT.chatPaused, '大厅文字聊天已暂停')
  assert.equal(HALL_SWITCH_TEXT.voicePaused, '大厅语音已暂停')
  assert.equal(HALL_SWITCH_TEXT.hallClosed, '大厅已被关闭')
  assert.ok(Object.isFrozen(HALL_SWITCHES) && HALL_SWITCHES.every(Object.isFrozen))
  assert.equal(switchMetaOf('chat_enabled').field, 'chatEnabled')
  assert.equal(switchMetaOf('hall_voice_enabled').field, 'voiceEnabled')
  assert.equal(switchMetaOf('nope'), null)
})

test('布尔解析只接受明确的布尔值', () => {
  assert.equal(parseSwitchBool(true), true)
  assert.equal(parseSwitchBool('true'), true)
  assert.equal(parseSwitchBool(false), false)
  assert.equal(parseSwitchBool('false'), false)
  for (const bad of ['1', 1, 0, '0', 'yes', '', null, undefined, {}, [], 'TRUE', ' true']) {
    assert.equal(parseSwitchBool(bad), undefined, JSON.stringify(bad))
  }
})

test('广播 extra / state / 设置接口：两种键名都能识别，非法值忽略', () => {
  assert.deepEqual(parseSwitchesPatch({ enabled: false, chat_enabled: true, voice_enabled: false, video_enabled: true, max_speakers: 6 }),
    { enabled: false, chatEnabled: true, voiceEnabled: false, videoEnabled: true })
  assert.deepEqual(parseSwitchesPatch({ hall_chat_enabled: 'false', hall_voice_enabled: 'true' }), { chatEnabled: false, voiceEnabled: true })
  // 短名非法时回落到 hall_ 前缀键
  assert.deepEqual(parseSwitchesPatch({ chat_enabled: 'x', hall_chat_enabled: 'false' }), { chatEnabled: false })
  assert.deepEqual(parseSwitchesPatch({ enabled: 'yes', voice_enabled: 1 }), null)
  assert.equal(parseSwitchesPatch(null), null)
  assert.equal(parseSwitchesPatch([true]), null)
  assert.equal(parseSwitchesPatch('enabled'), null)
  // 原型链上的键不采纳
  assert.equal(parseSwitchesPatch(Object.create({ enabled: false })), null)
})

test('hall_settings 广播经 normalizeHallSettingsEvent 输出新增的 chatEnabled / voiceEnabled', () => {
  assert.deepEqual(normalizeHallSettingsEvent({ enabled: true, chat_enabled: false, voice_enabled: false, video_enabled: false, max_speakers: 6, max_cameras: 2 }),
    { enabled: true, chatEnabled: false, voiceEnabled: false, videoEnabled: false, maxSpeakers: 6, maxCameras: 2 })
  // 旧后端（无新字段）保持原输出
  assert.deepEqual(normalizeHallSettingsEvent({ enabled: false, video_enabled: true }), { enabled: false, videoEnabled: true })
  // 非法布尔视为未提供，而不是回落默认值
  assert.equal(normalizeHallSettingsEvent({ enabled: 'maybe' }), null)
})

test('开关接口返回：四项齐全且为布尔才采纳，修改人与时间做清洗', () => {
  const ok = normalizeSwitchesView(view())
  assert.deepEqual(ok, { ok: true, value: { enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: false, updatedBy: '客服·小王', updatedAt: '2026-10-18T03:12:05.000+08:00' } })
  assert.deepEqual(normalizeSwitchesView(view({ updated_by: '', updated_at: '' })).value.updatedBy, '')
  for (const bad of [null, 'x', [], {}, view({ chat_enabled: undefined }), view({ voice_enabled: 'on' }), view({ video_enabled: null })]) {
    const r = normalizeSwitchesView(bad)
    assert.equal(r.ok, false, JSON.stringify(bad))
    assert.match(r.error, CJK)
  }
  const { chat_enabled: _omit, ...missing } = view()
  assert.equal(normalizeSwitchesView(missing).ok, false)
  const dirty = normalizeSwitchesView(view({ updated_by: '  客服\u202e·\u200b张三\n  ', updated_at: 'not-a-time' })).value
  assert.equal(dirty.updatedBy, '客服·张三')
  assert.equal(dirty.updatedAt, '')
  assert.equal(normalizeSwitchesView(view({ updated_by: 12, updated_at: 5 })).value.updatedBy, '')
})

test('修改人与时间：长度上限与时间格式校验', () => {
  assert.equal(Array.from(sanitizeSwitchActor('客'.repeat(100))).length, 64)
  assert.equal(sanitizeSwitchActor(null), '')
  assert.equal(sanitizeSwitchTime('2026-10-18T03:12:05+08:00'), '2026-10-18T03:12:05+08:00')
  assert.equal(sanitizeSwitchTime('2026-10-18T03:12:05+08:00' + ' '.repeat(5) + 'x'.repeat(40)), '')
  assert.equal(sanitizeSwitchTime('2026-13-45'), '')
})

test('合并：只采纳布尔值，不修改入参；页面状态的其它键被忽略', () => {
  const cur = { enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: false, maxSpeakers: 6 }
  const next = mergeSwitches(cur, { chatEnabled: false, voiceEnabled: 'false', videoEnabled: null })
  assert.deepEqual(next, { enabled: true, chatEnabled: false, voiceEnabled: true, videoEnabled: false })
  assert.equal(cur.chatEnabled, true)
  assert.deepEqual(mergeSwitches(null, null), HALL_SWITCH_DEFAULTS)
  assert.deepEqual(pickSwitches({ enabled: 'false' }), HALL_SWITCH_DEFAULTS)
})

test('乐观显示与失败回滚：去掉覆盖层后回到确认值，且保留提交期间收到的其他客服广播', () => {
  const confirmed = { enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: false }
  const overlay = { chatEnabled: false }
  assert.equal(displaySwitches(confirmed, overlay).chatEnabled, false)
  // 提交期间其他客服关闭了语音（广播更新确认值）
  const afterBroadcast = mergeSwitches(confirmed, { voiceEnabled: false })
  assert.deepEqual(displaySwitches(afterBroadcast, overlay), { enabled: true, chatEnabled: false, voiceEnabled: false, videoEnabled: false })
  // 提交失败：去掉覆盖层
  assert.deepEqual(displaySwitches(afterBroadcast, {}), { enabled: true, chatEnabled: true, voiceEnabled: false, videoEnabled: false })
})

test('实际生效：总开关关闭时其余三项都不生效，但值保留', () => {
  assert.deepEqual(effectiveSwitches({ enabled: false, chatEnabled: true, voiceEnabled: true, videoEnabled: true }), { hall: false, chat: false, voice: false, video: false })
  assert.deepEqual(effectiveSwitches({ enabled: true, chatEnabled: false, voiceEnabled: true, videoEnabled: true }), { hall: true, chat: false, voice: true, video: true })
})

test('前后对比：总开关、聊天、语音、视频的暂停与恢复', () => {
  const on = { enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: true }
  const none = switchTransitions(on, on)
  assert.equal(none.changed, false)
  assert.ok(Object.entries(none).every(([, v]) => v === false))
  const closed = switchTransitions(on, { ...on, enabled: false })
  assert.equal(closed.changed, true)
  assert.equal(closed.hallClosed, true)
  assert.equal(closed.chatPaused && closed.voicePaused && closed.videoOff, true)
  const opened = switchTransitions({ ...on, enabled: false }, on)
  assert.equal(opened.hallOpened && opened.chatResumed && opened.voiceResumed && opened.videoOn, true)
  const chat = switchTransitions(on, { ...on, chatEnabled: false })
  assert.deepEqual([chat.chatPaused, chat.voicePaused, chat.hallClosed], [true, false, false])
  const voice = switchTransitions(on, { ...on, voiceEnabled: false })
  assert.deepEqual([voice.voicePaused, voice.chatPaused, voice.videoOff], [true, false, false])
  const video = switchTransitions(on, { ...on, videoEnabled: false })
  assert.deepEqual([video.videoOff, video.voicePaused], [true, false])
  // 总开关关闭期间改子开关：值变化但不触发暂停/恢复动作
  const sub = switchTransitions({ ...on, enabled: false }, { ...on, enabled: false, chatEnabled: false })
  assert.equal(sub.changed, true)
  assert.equal(sub.chatPaused || sub.chatResumed, false)
})

test('请求体只包含改动的那一项；非法开关与非布尔值拒绝', () => {
  assert.deepEqual(buildSwitchRequest('enabled', false), { ok: true, body: { enabled: false } })
  assert.deepEqual(buildSwitchRequest('chatEnabled', true), { ok: true, body: { chat_enabled: true } })
  assert.deepEqual(buildSwitchRequest('voiceEnabled', false), { ok: true, body: { voice_enabled: false } })
  assert.deepEqual(buildSwitchRequest('videoEnabled', true), { ok: true, body: { video_enabled: true } })
  for (const [f, v] of [['chat_enabled', true], ['hall_enabled', true], ['maxSpeakers', 6], ['__proto__', true], ['enabled', 'false'], ['enabled', 1], ['voiceEnabled', null]]) {
    const r = buildSwitchRequest(f, v)
    assert.equal(r.ok, false, f + '=' + JSON.stringify(v))
    assert.match(r.error, CJK)
  }
})

test('二次确认：只有关闭总开关需要；确认文案说明访客影响与移出', () => {
  assert.equal(needsSwitchConfirm('enabled', false), true)
  assert.equal(needsSwitchConfirm('enabled', true), false)
  assert.equal(needsSwitchConfirm('chatEnabled', false), false)
  assert.match(HALL_MASTER_OFF_CONFIRM.message, /访客窗口将不再显示大厅/)
  assert.match(HALL_MASTER_OFF_CONFIRM.message, /移出/)
})

test('切换结果文案与最后修改人展示（北京时间）', () => {
  assert.equal(switchToggleMessage('chatEnabled', false), '大厅文字聊天已暂停')
  assert.equal(switchToggleMessage('voiceEnabled', false), '大厅语音已暂停')
  assert.equal(switchToggleMessage('enabled', true), '大厅已开启')
  assert.equal(switchToggleMessage('x', true), '')
  assert.equal(formatSwitchUpdated({ updatedBy: '客服·小王', updatedAt: '2026-10-17T19:12:05.000Z' }), '最后修改：客服·小王，2026-10-18 03:12:05（北京时间）')
  assert.equal(formatSwitchUpdated({ updatedBy: '客服·小王', updatedAt: '' }), '最后修改：客服·小王')
  assert.equal(formatSwitchUpdated({}), '尚无修改记录')
  assert.equal(formatSwitchUpdated(), '尚无修改记录')
})
