// 多人大厅客服接口（契约第 4.2 节）。所有请求 silent：错误提示统一由 modules/hallErrors.js 映射为中文，
// 避免 http 拦截器的通用提示与大厅专用提示重复弹出；401 续期/跳登录仍由拦截器统一处理。
import http from './http'
import { HALL_PAGE_SIZE, isValidHallMessageId } from '../modules/hallMessages'
import { buildModerateRequest } from '../modules/hallProtocol'
import { HALL_SWITCHES } from '../modules/hallSwitches'

const seg = (value) => encodeURIComponent(String(value ?? ''))

export function joinHall() {
  return http.post('/agent/hall/join', null, { silent: true })
}

export function setHallCamera(on) {
  return http.post('/agent/hall/camera', { on: on === true }, { silent: true })
}

export function getHallState() {
  return http.get('/agent/hall/state', { silent: true })
}

// [123] 大厅开关（契约第 10.2 节）：任何启用中的客服都可读取与切换
export function getHallSwitches() {
  return http.get('/agent/hall/switches', { silent: true })
}

// body 由 modules/hallSwitches.buildSwitchRequest 构造；这里再兜底一次：只允许四个开关键、值必须是布尔、至少一项
export function updateHallSwitches(body) {
  const allowed = new Set(HALL_SWITCHES.map(s => s.apiKey))
  const keys = body && typeof body === 'object' && !Array.isArray(body) ? Object.keys(body) : []
  if (!keys.length || keys.some(k => !allowed.has(k) || typeof body[k] !== 'boolean')) {
    return Promise.reject({ code: -1, msg: '开关参数无效，请刷新页面后重试' })
  }
  return http.post('/agent/hall/switches', body, { silent: true })
}

export function moderateHall(params) {
  const req = buildModerateRequest(params)
  if (!req.ok) return Promise.reject({ code: -1, msg: req.error })
  return http.post('/agent/hall/moderate', req.body, { silent: true })
}

export function listHallMessages({ before = '', limit = HALL_PAGE_SIZE } = {}) {
  const params = { limit: Math.min(HALL_PAGE_SIZE, Math.max(1, Math.floor(Number(limit) || HALL_PAGE_SIZE))) }
  if (before) {
    if (!isValidHallMessageId(before)) return Promise.reject({ code: -1, msg: '消息游标无效' })
    params.before = before
  }
  return http.get('/agent/hall/messages', { params, silent: true })
}

export function deleteHallMessage(id) {
  if (!isValidHallMessageId(id)) return Promise.reject({ code: -1, msg: '消息 ID 无效' })
  return http.post('/agent/hall/messages/' + seg(id) + '/delete', null, { silent: true })
}

// 复用现有上传接口：scope=hall 不带 conv_id，鉴权依赖 HttpOnly Cookie（与后端约定，见契约「待确认」）
export function uploadHallFile(file, { onProgress } = {}) {
  const fd = new FormData()
  fd.append('file', file)
  fd.append('uploader', 'agent')
  fd.append('scope', 'hall')
  return http.post('/upload', fd, {
    silent: true,
    timeout: 120000,
    onUploadProgress: typeof onProgress === 'function'
      ? (e) => { if (e && e.total) onProgress(Math.round((e.loaded / e.total) * 100)) }
      : undefined,
  })
}
