// 多人大厅系统设置（契约第 3 节、第 10 节）：解析、校验、序列化与带宽估算（纯函数，可在 node 测试中直接运行）。
// 现有 /api/admin/settings 的值统一是字符串（map[string]string），布尔用 'true'/'false'。
// [123] 四个开关（总开关、文字聊天、语音、视频）所有客服都可在「多人大厅」页切换，开关的解析统一复用 hallSwitches.js。
import { parseSwitchesPatch } from './hallSwitches.js'

export const HALL_SETTING_LIMITS = Object.freeze({
  hall_max_speakers: Object.freeze({ min: 1, max: 20, def: 6, label: '同时开麦访客上限' }),
  hall_max_cameras: Object.freeze({ min: 0, max: 6, def: 2, label: '同时开摄像头上限' }),
  hall_max_participants: Object.freeze({ min: 10, max: 500, def: 100, label: '大厅人数上限' }),
})

export const HALL_SETTING_DEFAULTS = Object.freeze({
  hall_enabled: true,
  hall_chat_enabled: true,
  hall_voice_enabled: true,
  hall_video_enabled: false,
  hall_max_speakers: 6,
  hall_max_cameras: 2,
  hall_max_participants: 100,
})

export const HALL_SETTING_KEYS = Object.freeze(Object.keys(HALL_SETTING_DEFAULTS))

// [123] 所有客服都能切换的四个开关（设置页保存时只提交管理员改动过的开关，避免覆盖其他客服刚做的切换）
export const HALL_SWITCH_SETTING_KEYS = Object.freeze(['hall_enabled', 'hall_chat_enabled', 'hall_voice_enabled', 'hall_video_enabled'])

// 服务器公网带宽（与主站共用）及推荐摄像头路数，用于设置页提示
export const HALL_SERVER_BANDWIDTH_MBPS = 7
export const HALL_RECOMMENDED_MAX_CAMERAS = 2

function parseBool(value, def) {
  if (value === true || value === 'true' || value === '1' || value === 1) return true
  if (value === false || value === 'false' || value === '0' || value === 0) return false
  return def
}

function parseIntIn(value, { min, max, def }) {
  const n = typeof value === 'string' && value.trim() !== '' ? Number(value.trim()) : value
  if (typeof n !== 'number' || !Number.isFinite(n) || !Number.isInteger(n)) return def
  return Math.min(max, Math.max(min, n))
}

// GET /api/admin/settings 的原始对象 → 表单值（缺省/非法值回落默认，越界值夹紧到范围内）
export function parseHallSettings(raw) {
  const d = raw && typeof raw === 'object' ? raw : {}
  return {
    hall_enabled: parseBool(d.hall_enabled, HALL_SETTING_DEFAULTS.hall_enabled),
    hall_chat_enabled: parseBool(d.hall_chat_enabled, HALL_SETTING_DEFAULTS.hall_chat_enabled),
    hall_voice_enabled: parseBool(d.hall_voice_enabled, HALL_SETTING_DEFAULTS.hall_voice_enabled),
    hall_video_enabled: parseBool(d.hall_video_enabled, HALL_SETTING_DEFAULTS.hall_video_enabled),
    hall_max_speakers: parseIntIn(d.hall_max_speakers, HALL_SETTING_LIMITS.hall_max_speakers),
    hall_max_cameras: parseIntIn(d.hall_max_cameras, HALL_SETTING_LIMITS.hall_max_cameras),
    hall_max_participants: parseIntIn(d.hall_max_participants, HALL_SETTING_LIMITS.hall_max_participants),
  }
}

// 保存前严格校验（不夹紧，越界直接提示），返回可直接合并进保存请求的字符串键值
export function validateHallSettings(form) {
  const f = form && typeof form === 'object' ? form : {}
  if (HALL_SWITCH_SETTING_KEYS.some(k => typeof f[k] !== 'boolean')) {
    return { ok: false, error: '大厅开关取值无效，请刷新页面后重试' }
  }
  const payload = {}
  for (const k of HALL_SWITCH_SETTING_KEYS) payload[k] = f[k] ? 'true' : 'false'
  for (const [key, lim] of Object.entries(HALL_SETTING_LIMITS)) {
    const v = f[key]
    if (typeof v !== 'number' || !Number.isInteger(v) || v < lim.min || v > lim.max) {
      return { ok: false, error: lim.label + '必须是 ' + lim.min + '–' + lim.max + ' 之间的整数' }
    }
    payload[key] = String(v)
  }
  return { ok: true, payload }
}

// [123] 设置页保存：在 validateHallSettings 的基础上，去掉与载入时相同的四个开关
// （其他客服可能在管理员打开设置页之后切换过开关，原样回写会把他们的切换悄悄改回去）。
// loaded 为载入时 parseHallSettings 的结果；缺失时按全部提交处理（与原行为一致）。
export function buildHallSettingsSavePayload(form, loaded) {
  const v = validateHallSettings(form)
  if (!v.ok) return v
  const payload = { ...v.payload }
  if (loaded && typeof loaded === 'object') {
    for (const k of HALL_SWITCH_SETTING_KEYS) {
      if (typeof loaded[k] === 'boolean' && loaded[k] === form[k]) delete payload[k]
    }
  }
  return { ok: true, payload }
}

// WS hall_settings 的 extra 或 /hall/state、/hall/join 中的设置 → 统一结构；无法识别返回 null。
// 四个开关只接受明确的布尔值（非法值视为未提供，不回落默认值，避免把开关显示错）。
export function normalizeHallSettingsEvent(raw) {
  if (!raw || typeof raw !== 'object') return null
  const has = k => Object.prototype.hasOwnProperty.call(raw, k)
  const out = { ...(parseSwitchesPatch(raw) || {}) }
  const num = (a, b, lim) => {
    if (!has(a) && !has(b)) return undefined
    return parseIntIn(has(a) ? raw[a] : raw[b], lim)
  }
  const ms = num('max_speakers', 'hall_max_speakers', HALL_SETTING_LIMITS.hall_max_speakers)
  const mc = num('max_cameras', 'hall_max_cameras', HALL_SETTING_LIMITS.hall_max_cameras)
  const mp = num('max_participants', 'hall_max_participants', HALL_SETTING_LIMITS.hall_max_participants)
  if (ms !== undefined) out.maxSpeakers = ms
  if (mc !== undefined) out.maxCameras = mc
  if (mp !== undefined) out.maxParticipants = mp
  return Object.keys(out).length ? out : null
}

// 带宽粗估（Mbps，服务器出方向峰值）。假设：
//   语音 Opus 约 0.032Mbps/路，按 activeTalkers 人同时说话、每位在场者都收听；
//   每路摄像头观看端只收 180p 低清层约 0.16Mbps，每位观看者最多看 2 路；
//   上行每路摄像头约 0.61Mbps（360p 0.45 + 180p 0.16 联播）。
export function estimateHallBandwidth({ participants, maxCameras, videoEnabled, activeTalkers = 2 } = {}) {
  const n = Math.max(0, Math.floor(Number(participants) || 0))
  const cams = videoEnabled ? Math.max(0, Math.floor(Number(maxCameras) || 0)) : 0
  const audio = n * Math.max(0, activeTalkers) * 0.032
  const video = n * Math.min(cams, 2) * 0.16
  const upstream = cams * 0.61
  const round = v => Math.round(v * 10) / 10
  return { downstream: round(audio + video), upstream: round(upstream), audio: round(audio), video: round(video) }
}
