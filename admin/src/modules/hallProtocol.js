// 多人大厅 HTTP 响应校验与请求构造（纯函数，可在 node 测试中直接运行）。
// 服务端数据也按不可信处理：校验连接地址协议、令牌格式、ICE 服务器与管理操作参数，防止异常数据导致令牌外泄或注入。
import { normalizeHallMessageList, HALL_PAGE_SIZE } from './hallMessages.js'
import { isValidHallIdentity } from './hallParticipants.js'
import { normalizeHallSettingsEvent } from './hallSettings.js'

const JWT = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$/
const ICE_URL = /^(stun|turn|turns):[^\s]{1,200}$/
const ROOM = /^hall_[A-Za-z0-9_-]{1,34}$/
export const KICK_MINUTES_OPTIONS = Object.freeze([10, 30, 60, 1440])
export const KICK_DEFAULT_MINUTES = 30
export const MODERATE_ACTIONS = Object.freeze(['mute', 'mute_all', 'camera_off', 'kick'])

// LiveKit 协议枚举 livekit.TrackSource（CAMERA=1, MICROPHONE=2），用于判断发布权限
export const TRACK_SOURCE_PROTO = Object.freeze({ camera: 1, microphone: 2 })

export function validateLiveKitUrl(url, pageProtocol = 'https:') {
  if (typeof url !== 'string' || !url || url.length > 512) return ''
  let u
  try {
    u = new URL(url)
  } catch {
    return ''
  }
  if (u.username || u.password || !u.hostname) return ''
  if (u.protocol === 'wss:') return u.toString()
  // 仅本地开发（页面本身是 http）允许明文 ws
  if (u.protocol === 'ws:' && pageProtocol === 'http:') return u.toString()
  return ''
}

export function normalizeIceServers(list) {
  if (!Array.isArray(list)) return []
  const out = []
  // 最多检查前 16 项、保留 4 组有效配置，防止异常超长列表
  for (const s of list.slice(0, 16)) {
    if (out.length >= 4) break
    if (!s || typeof s !== 'object') continue
    const urls = (Array.isArray(s.urls) ? s.urls : [s.urls]).filter(u => typeof u === 'string' && ICE_URL.test(u)).slice(0, 6)
    if (!urls.length) continue
    const srv = { urls }
    if (typeof s.username === 'string' && s.username.length <= 256) srv.username = s.username
    if (typeof s.credential === 'string' && s.credential.length <= 256) srv.credential = s.credential
    out.push(srv)
  }
  return out
}

function intIn(v, min, max, def) {
  const n = Number(v)
  return Number.isInteger(n) && n >= min && n <= max ? n : def
}

// POST /api/agent/hall/join 的 data → 连接参数
export function normalizeJoinResponse(data, { pageProtocol = 'https:' } = {}) {
  const fail = error => ({ ok: false, error })
  if (!data || typeof data !== 'object') return fail('大厅服务返回的数据无效，请稍后重试')
  const url = validateLiveKitUrl(data.url, pageProtocol)
  if (!url) return fail('大厅媒体服务地址无效，请联系管理员检查配置')
  if (typeof data.token !== 'string' || data.token.length > 4096 || !JWT.test(data.token)) {
    return fail('大厅令牌无效，请稍后重试')
  }
  if (!isValidHallIdentity(data.identity) || !data.identity.startsWith('a_')) return fail('大厅身份信息无效，请重新登录后再试')
  return {
    ok: true,
    value: {
      url,
      token: data.token,
      room: typeof data.room === 'string' && ROOM.test(data.room) ? data.room : 'hall_default',
      identity: data.identity,
      name: typeof data.name === 'string' ? data.name.slice(0, 64) : '',
      videoEnabled: data.video_enabled === true || data.video_enabled === 'true',
      maxSpeakers: intIn(data.max_speakers, 1, 20, 6),
      maxCameras: intIn(data.max_cameras, 0, 6, 2),
      iceServers: normalizeIceServers(data.ice_servers),
      history: normalizeHallMessageList(data.history, HALL_PAGE_SIZE),
    },
  }
}

// GET /api/agent/hall/state 的 data
export function normalizeHallState(data) {
  const d = data && typeof data === 'object' ? data : {}
  return {
    settings: normalizeHallSettingsEvent(d.settings),
    counts: d.counts && typeof d.counts === 'object' ? d.counts : null,
    participants: Array.isArray(d.participants) ? d.participants : [],
    // LiveKit 暂不可用时为 true：名单来自后端缓存，麦克风/摄像头按名额推断
    degraded: d.degraded === true,
  }
}

// POST /api/agent/hall/moderate 请求体校验
export function buildModerateRequest({ action, identity, minutes } = {}) {
  if (!MODERATE_ACTIONS.includes(action)) return { ok: false, error: '不支持的管理操作' }
  if (action === 'mute_all') return { ok: true, body: { action } }
  if (!isValidHallIdentity(identity)) return { ok: false, error: '参与者信息无效，请刷新后重试' }
  const body = { action, identity }
  if (action === 'kick') {
    const m = minutes === undefined ? KICK_DEFAULT_MINUTES : Number(minutes)
    if (!Number.isInteger(m) || m < 1 || m > 1440) return { ok: false, error: '禁入时长必须是 1–1440 分钟' }
    body.minutes = m
  }
  return { ok: true, body }
}

export function kickMinutesLabel(minutes) {
  const m = Number(minutes)
  if (m === 1440) return '1 天'
  if (m >= 60 && m % 60 === 0) return m / 60 + ' 小时'
  return m + ' 分钟'
}

// 发布权限判断：permissions 为 LiveKit ParticipantPermission
export function hasPublishPermission(permissions, source) {
  if (!permissions || !permissions.canPublish) return false
  const proto = TRACK_SOURCE_PROTO[source]
  if (!proto) return false
  const srcs = Array.isArray(permissions.canPublishSources) ? permissions.canPublishSources : []
  return srcs.length === 0 || srcs.includes(proto)
}
