import test from 'node:test'
import assert from 'node:assert/strict'

import {
  HALL_SETTING_DEFAULTS,
  estimateHallBandwidth,
  normalizeHallSettingsEvent,
  parseHallSettings,
  validateHallSettings,
} from '../src/modules/hallSettings.js'

test('缺省设置回落到契约默认值', () => {
  assert.deepEqual(parseHallSettings({}), HALL_SETTING_DEFAULTS)
  assert.deepEqual(parseHallSettings(null), HALL_SETTING_DEFAULTS)
  assert.deepEqual(HALL_SETTING_DEFAULTS, { hall_enabled: true, hall_video_enabled: false, hall_max_speakers: 6, hall_max_cameras: 2, hall_max_participants: 100 })
})

test('解析字符串设置：布尔、整数、越界夹紧、非法回落', () => {
  assert.deepEqual(parseHallSettings({
    hall_enabled: 'false', hall_video_enabled: 'true', hall_max_speakers: '25', hall_max_cameras: '-1', hall_max_participants: 'abc',
  }), { hall_enabled: false, hall_video_enabled: true, hall_max_speakers: 20, hall_max_cameras: 0, hall_max_participants: 100 })
  assert.equal(parseHallSettings({ hall_max_speakers: '3.5' }).hall_max_speakers, 6)
  assert.equal(parseHallSettings({ hall_max_participants: ' 250 ' }).hall_max_participants, 250)
  assert.equal(parseHallSettings({ hall_enabled: 'yes' }).hall_enabled, true)
})

test('保存校验：严格范围，输出与现有设置接口一致的字符串', () => {
  const ok = validateHallSettings({ hall_enabled: true, hall_video_enabled: false, hall_max_speakers: 1, hall_max_cameras: 0, hall_max_participants: 500 })
  assert.deepEqual(ok, { ok: true, payload: { hall_enabled: 'true', hall_video_enabled: 'false', hall_max_speakers: '1', hall_max_cameras: '0', hall_max_participants: '500' } })
  const base = { hall_enabled: true, hall_video_enabled: true, hall_max_speakers: 6, hall_max_cameras: 2, hall_max_participants: 100 }
  for (const bad of [
    { hall_max_speakers: 0 }, { hall_max_speakers: 21 }, { hall_max_cameras: 7 }, { hall_max_cameras: -1 },
    { hall_max_participants: 9 }, { hall_max_participants: 501 }, { hall_max_participants: 50.5 }, { hall_max_speakers: null },
    { hall_max_speakers: '6' }, { hall_enabled: 'true' },
  ]) {
    const r = validateHallSettings({ ...base, ...bad })
    assert.equal(r.ok, false, JSON.stringify(bad))
    assert.match(r.error, /[\u4e00-\u9fa5]/)
  }
  assert.equal(validateHallSettings(null).ok, false)
})

test('WS hall_settings 与 state.settings 两种字段名都能识别', () => {
  assert.deepEqual(normalizeHallSettingsEvent({ enabled: false, video_enabled: true, max_speakers: 4, max_cameras: 1 }),
    { enabled: false, videoEnabled: true, maxSpeakers: 4, maxCameras: 1 })
  assert.deepEqual(normalizeHallSettingsEvent({ hall_enabled: 'true', hall_video_enabled: 'false', hall_max_participants: '200' }),
    { enabled: true, videoEnabled: false, maxParticipants: 200 })
  assert.equal(normalizeHallSettingsEvent({}), null)
  assert.equal(normalizeHallSettingsEvent('x'), null)
})

test('带宽粗估：100 人、2 人同时说话约 6.4Mbps；开视频每人看 2 路低清', () => {
  assert.deepEqual(estimateHallBandwidth({ participants: 100, maxCameras: 2, videoEnabled: false }), { downstream: 6.4, upstream: 0, audio: 6.4, video: 0 })
  const v = estimateHallBandwidth({ participants: 100, maxCameras: 6, videoEnabled: true })
  assert.equal(v.video, 32)
  assert.equal(v.upstream, 3.7)
  assert.equal(estimateHallBandwidth({}).downstream, 0)
})
