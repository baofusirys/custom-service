// 多人大厅「大厅开关」（契约第 10 节）：所有客服都能切换的四个布尔开关——总开关、文字聊天、语音、视频。
// 本模块只放纯函数（状态解析、合并、乐观显示、前后对比、请求体构造与文案），可在 node 测试中直接运行。
// 数据来源：GET/POST /api/agent/hall/switches、WS hall_settings 的 extra、/hall/state 的 settings、/hall/join 返回，
// 以及 /api/admin/settings 的 hall_* 字符串键；一律按不可信数据处理，只接受明确的布尔值。
import { formatBeijing, toTimestamp } from './timeFormat.js'

// 页面内统一使用的字段名（与 hallSettings.normalizeHallSettingsEvent 的输出一致）
export const HALL_SWITCH_FIELDS = Object.freeze(['enabled', 'chatEnabled', 'voiceEnabled', 'videoEnabled'])

export const HALL_SWITCHES = Object.freeze([
  Object.freeze({
    field: 'enabled',
    apiKey: 'enabled',
    settingKey: 'hall_enabled',
    label: '大厅总开关',
    onText: '大厅已开启',
    offText: '大厅已关闭',
    description: '关闭后访客窗口不再显示大厅、只保留私聊，已在大厅的人会被移出；重新打开即恢复。',
  }),
  Object.freeze({
    field: 'chatEnabled',
    apiKey: 'chat_enabled',
    settingKey: 'hall_chat_enabled',
    label: '文字聊天',
    onText: '大厅文字聊天已开启',
    offText: '大厅文字聊天已暂停',
    description: '大厅里的文字、图片、文件消息；关闭后访客和客服都不能发送，历史消息仍可查看。',
  }),
  Object.freeze({
    field: 'voiceEnabled',
    apiKey: 'voice_enabled',
    settingKey: 'hall_voice_enabled',
    label: '语音',
    onText: '大厅语音已开启',
    offText: '大厅语音已暂停',
    description: '关闭后访客不能开麦，所有人（含客服）正在开的麦克风会被立即关闭。',
  }),
  Object.freeze({
    field: 'videoEnabled',
    apiKey: 'video_enabled',
    settingKey: 'hall_video_enabled',
    label: '视频',
    onText: '大厅视频已开启',
    offText: '大厅视频已关闭',
    description: '开启后客服和访客都可以开摄像头（受人数上限约束）；关闭时立即收回所有人的摄像头。',
  }),
])

// 契约 10.1：默认全部开启，视频默认关闭
export const HALL_SWITCH_DEFAULTS = Object.freeze({ enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: false })

export const HALL_SWITCH_TEXT = Object.freeze({
  hallClosed: '大厅已被关闭',
  chatPaused: '大厅文字聊天已暂停',
  voicePaused: '大厅语音已暂停',
  videoOff: '大厅视频已关闭',
  never: '尚无修改记录',
})

// 关闭总开关前的二次确认（契约 10.4）
export const HALL_MASTER_OFF_CONFIRM = Object.freeze({
  title: '关闭大厅',
  message: '关闭大厅总开关后，访客窗口将不再显示大厅（只保留私聊），已在大厅里的访客和客服都会被移出。'
    + '文字聊天、语音、视频的当前设置会保留，重新打开总开关即可恢复。确定关闭？',
  confirmButtonText: '关闭大厅',
  cancelButtonText: '取消',
})

// 各字段在不同来源里的键名：开关接口 / WS / state / join 用短名，管理员设置接口用 hall_ 前缀
const SOURCE_KEYS = Object.freeze({
  enabled: Object.freeze(['enabled', 'hall_enabled']),
  chatEnabled: Object.freeze(['chat_enabled', 'hall_chat_enabled']),
  voiceEnabled: Object.freeze(['voice_enabled', 'hall_voice_enabled']),
  videoEnabled: Object.freeze(['video_enabled', 'hall_video_enabled']),
})

const MAX_NAME_CHARS = 64
const MAX_TIME_CHARS = 40
// 控制字符、零宽字符与双向文本控制符：显示名里一律去掉，防止界面错乱或伪装
const UNSAFE_CHARS = /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2028-\u202e\u2060-\u2064\u2066-\u2069\ufeff]/g

const hasOwn = (obj, key) => Object.prototype.hasOwnProperty.call(obj, key)

// 只接受明确的布尔值（JSON 布尔或设置接口的 'true'/'false' 字符串）；其它一律视为「未提供」
export function parseSwitchBool(value) {
  if (value === true || value === 'true') return true
  if (value === false || value === 'false') return false
  return undefined
}

export function switchMetaOf(fieldOrKey) {
  return HALL_SWITCHES.find(s => s.field === fieldOrKey || s.apiKey === fieldOrKey || s.settingKey === fieldOrKey) || null
}

// 任意来源对象 → 只含已提供且合法的开关字段的部分对象；一个都没有返回 null
export function parseSwitchesPatch(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const out = {}
  for (const field of HALL_SWITCH_FIELDS) {
    for (const key of SOURCE_KEYS[field]) {
      if (!hasOwn(raw, key)) continue
      const b = parseSwitchBool(raw[key])
      if (b !== undefined) {
        out[field] = b
        break
      }
    }
  }
  return Object.keys(out).length ? out : null
}

// 从页面状态对象（可能带其它键）里取出四个开关，缺省用默认值
export function pickSwitches(state) {
  const s = state && typeof state === 'object' ? state : {}
  const out = { ...HALL_SWITCH_DEFAULTS }
  for (const field of HALL_SWITCH_FIELDS) if (typeof s[field] === 'boolean') out[field] = s[field]
  return out
}

// 把部分更新合并进当前状态（只采纳布尔值），返回新对象，不修改入参
export function mergeSwitches(current, patch) {
  const out = pickSwitches(current)
  if (!patch || typeof patch !== 'object') return out
  for (const field of HALL_SWITCH_FIELDS) if (typeof patch[field] === 'boolean') out[field] = patch[field]
  return out
}

// 面板显示值 = 服务端已确认的状态 + 正在提交中的乐观值。
// 提交失败时只需去掉覆盖层即完成回滚，并且不会覆盖提交期间收到的其他客服的广播。
export function displaySwitches(confirmed, overlay) {
  return mergeSwitches(confirmed, overlay)
}

// 实际生效状态：总开关关闭时其余三项都不生效（契约 10.1）
export function effectiveSwitches(state) {
  const s = pickSwitches(state)
  return {
    hall: s.enabled,
    chat: s.enabled && s.chatEnabled,
    voice: s.enabled && s.voiceEnabled,
    video: s.enabled && s.videoEnabled,
  }
}

// 前后状态对比 → 页面需要执行的动作（离开大厅、停止麦克风、关闭摄像头、刷新数据）
export function switchTransitions(prev, next) {
  const p = pickSwitches(prev)
  const n = pickSwitches(next)
  const a = effectiveSwitches(p)
  const b = effectiveSwitches(n)
  return {
    changed: HALL_SWITCH_FIELDS.some(f => p[f] !== n[f]),
    hallClosed: a.hall && !b.hall,
    hallOpened: !a.hall && b.hall,
    chatPaused: a.chat && !b.chat,
    chatResumed: !a.chat && b.chat,
    voicePaused: a.voice && !b.voice,
    voiceResumed: !a.voice && b.voice,
    videoOff: a.video && !b.video,
    videoOn: !a.video && b.video,
  }
}

export function sanitizeSwitchActor(value) {
  if (typeof value !== 'string') return ''
  const text = value.replace(UNSAFE_CHARS, '').trim()
  return Array.from(text).slice(0, MAX_NAME_CHARS).join('')
}

export function sanitizeSwitchTime(value) {
  if (typeof value !== 'string') return ''
  const text = value.trim()
  if (!text || text.length > MAX_TIME_CHARS) return ''
  return Number.isFinite(toTimestamp(text)) ? text : ''
}

// GET/POST /api/agent/hall/switches 的 data → 完整状态；四个开关必须齐全且为布尔，否则视为异常数据
export function normalizeSwitchesView(data) {
  const fail = { ok: false, error: '大厅开关数据无效，请稍后重试' }
  if (!data || typeof data !== 'object' || Array.isArray(data)) return fail
  const value = {}
  for (const s of HALL_SWITCHES) {
    if (!hasOwn(data, s.apiKey)) return fail
    const b = parseSwitchBool(data[s.apiKey])
    if (b === undefined) return fail
    value[s.field] = b
  }
  value.updatedBy = sanitizeSwitchActor(data.updated_by)
  value.updatedAt = sanitizeSwitchTime(data.updated_at)
  return { ok: true, value }
}

// POST 请求体：只传改动的那一项（契约 10.2）
export function buildSwitchRequest(field, value) {
  const meta = switchMetaOf(field)
  if (!meta || meta.field !== field) return { ok: false, error: '不支持的大厅开关' }
  if (typeof value !== 'boolean') return { ok: false, error: '开关取值无效，请刷新页面后重试' }
  return { ok: true, body: { [meta.apiKey]: value } }
}

// 只有关闭总开关需要二次确认
export function needsSwitchConfirm(field, value) {
  return field === 'enabled' && value === false
}

export function switchToggleMessage(field, value) {
  const meta = switchMetaOf(field)
  if (!meta) return ''
  return value ? meta.onText : meta.offText
}

// 「最后修改：客服·张三，2026-10-18 03:12:05（北京时间）」；从未修改过返回「尚无修改记录」
export function formatSwitchUpdated({ updatedBy, updatedAt } = {}) {
  const who = sanitizeSwitchActor(updatedBy)
  const time = sanitizeSwitchTime(updatedAt)
  const when = time ? formatBeijing(time) + '（北京时间）' : ''
  if (!who && !when) return HALL_SWITCH_TEXT.never
  return '最后修改：' + [who, when].filter(Boolean).join('，')
}
