// 多人大厅错误码 / 媒体设备错误 / 断线原因 → 中文提示的唯一映射来源（纯函数，可在 node 测试中直接运行）。
// 契约：_internal/reports/hall-contract.md 第 8 节。视图层只调用本模块，不在组件里散落提示文案。

export const HALL_ERROR_CODES = Object.freeze({
  INVALID_TARGET: 40376,
  MESSAGE_NOT_FOUND: 40477,
  LIVEKIT_UNAVAILABLE: 50370,
  HALL_CLOSED: 40371,
  KICKED: 40372,
  MEDIA_FORBIDDEN: 40374,
  VIDEO_DISABLED: 40375,
  SPEAKERS_FULL: 40973,
  CAMERAS_FULL: 40976,
  HALL_FULL: 40977,
  TOO_FREQUENT: 42910,
})

const CODE_MESSAGES = new Map([
  // 大厅专用（契约第 8 节）
  [40371, '大厅已关闭'],
  [40372, '你已被移出大厅，暂时无法进入'],
  [40374, '当前无法开麦或开摄像头'],
  [40375, '视频功能未开启'],
  [40973, '同时开麦人数已满，请稍后再试'],
  [40976, '同时开摄像头人数已满，请稍后再试'],
  [40977, '大厅人数已满'],
  [42910, '操作过于频繁，请稍后再试'],
  // 契约第 8 节「补充（后端实现）」
  [40059, '大厅设置不合法，请检查后重新保存'],
  [40060, '参数错误，请刷新页面后重试'],
  [40376, '不能对该对象执行此操作'],
  [40477, '该大厅消息不存在或已被删除'],
  [50370, '语音服务暂不可用，请稍后重试'],
  [50050, '服务器错误，请稍后重试'],
  // 复用的现有通用错误码
  [40101, '未登录或登录已失效，请重新登录'],
  [40102, '登录已过期，请重新登录'],
  [40103, '登录状态无效，请重新登录'],
  [40108, '上传鉴权失败，请刷新页面后重试'],
  [41301, '文件过大'],
  [41501, '文件类型不支持，或文件内容与扩展名不符'],
  [42903, '上传过于频繁，请稍后再试'],
])

const STATUS_MESSAGES = new Map([
  [400, '请求参数有误，请刷新页面后重试'],
  [403, '没有权限执行该操作'],
  [404, '目标不存在或已被删除'],
  [409, '当前状态冲突，请刷新后重试'],
  [413, '文件过大'],
  [415, '文件类型不支持'],
  [429, '操作过于频繁，请稍后再试'],
])

const CJK = /[\u4e00-\u9fa5]/
const MAX_SERVER_MSG_CHARS = 200

function toFiniteNumber(value) {
  const n = typeof value === 'string' && value.trim() !== '' ? Number(value) : value
  return typeof n === 'number' && Number.isFinite(n) ? n : null
}

// 统一解析三种失败形态：axios 错误（带 response）、http 拦截器 reject 的业务体 {code,msg,data}、普通 Error。
export function extractHallError(err) {
  const info = { code: null, status: null, msg: '', data: null, network: false, timeout: false }
  if (!err || typeof err !== 'object') return info
  const res = err.response && typeof err.response === 'object' ? err.response : null
  const body = res ? res.data : err
  if (res) info.status = toFiniteNumber(res.status)
  if (body && typeof body === 'object') {
    const code = toFiniteNumber(body.code)
    if (code !== null && Number.isInteger(code) && code > 0) info.code = code
    if (typeof body.msg === 'string') info.msg = body.msg
    if (body.data && typeof body.data === 'object') info.data = body.data
  }
  if (!res) {
    // axios 的 err.code 是字符串（ERR_NETWORK / ECONNABORTED），不能与业务码混淆
    const axiosCode = typeof err.code === 'string' ? err.code : ''
    if (axiosCode === 'ECONNABORTED' || axiosCode === 'ETIMEDOUT') info.timeout = true
    else if (axiosCode === 'ERR_NETWORK' || (err.isAxiosError && !res)) info.network = true
  }
  return info
}

// 剩余禁入时长（秒）→「N 秒 / N 分钟 / N 小时 M 分钟」
export function formatRetryAfter(seconds) {
  const s = toFiniteNumber(seconds)
  if (s === null || s <= 0) return ''
  const total = Math.ceil(s)
  if (total < 60) return total + ' 秒'
  const minutes = Math.ceil(total / 60)
  if (minutes < 60) return minutes + ' 分钟'
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  return m ? h + ' 小时 ' + m + ' 分钟' : h + ' 小时'
}

export function hallErrorCode(err) {
  return extractHallError(err).code
}

export function hallErrorMessage(err, fallback = '操作失败，请稍后重试') {
  const info = extractHallError(err)
  if (info.code !== null && CODE_MESSAGES.has(info.code)) {
    let text = CODE_MESSAGES.get(info.code)
    if (info.code === HALL_ERROR_CODES.KICKED) {
      const wait = formatRetryAfter(info.data?.retry_after)
      if (wait) text += '（约 ' + wait + '后可再进入）'
    }
    return text
  }
  if (info.timeout) return '请求超时，请稍后重试'
  if (info.network) return '网络连接失败，请检查网络后重试'
  // 后端统一返回中文 msg；只采纳不超长且含中文的文本，防止把英文堆栈/内部信息直接展示给客服
  const serverMsg = typeof info.msg === 'string' ? info.msg.trim() : ''
  if (serverMsg && serverMsg.length <= MAX_SERVER_MSG_CHARS && CJK.test(serverMsg)) return serverMsg
  if (info.status !== null) {
    if (STATUS_MESSAGES.has(info.status)) return STATUS_MESSAGES.get(info.status)
    if (info.status >= 500) return '服务器繁忙，请稍后重试'
  }
  return fallback
}

const DEVICE_LABEL = Object.freeze({ microphone: '麦克风', camera: '摄像头' })

// getUserMedia / LiveKit 发布失败 → 中文提示
export function mediaErrorMessage(err, kind = 'microphone') {
  const label = DEVICE_LABEL[kind] || '媒体设备'
  const name = err && typeof err.name === 'string' ? err.name : ''
  const message = err && typeof err.message === 'string' ? err.message : ''
  if (name === 'NotAllowedError' || name === 'PermissionDeniedError') {
    return '浏览器未授权使用' + label + '，请在地址栏左侧的网站设置中允许后重试'
  }
  if (name === 'NotFoundError' || name === 'DevicesNotFoundError') return '未检测到可用的' + label
  if (name === 'NotReadableError' || name === 'TrackStartError') return label + '被其他程序占用或无法启动'
  if (name === 'OverconstrainedError' || name === 'ConstraintNotSatisfiedError') return label + '不支持所需的采集参数'
  if (name === 'SecurityError') return '当前页面不是安全连接（HTTPS），浏览器禁止使用' + label
  if (name === 'DeviceUnsupportedError') return '当前浏览器不支持使用' + label
  if ((err && err.status === 403) || /insufficient permissions/i.test(message)) return '暂无' + label + '发布权限，请稍后重试'
  return label + '开启失败，请检查设备后重试'
}

// LiveKit DisconnectReason（livekit.DisconnectReason 协议枚举，数值固定；tests 中与 livekit-client 导出值逐一核对）
export const DISCONNECT_REASON = Object.freeze({
  UNKNOWN_REASON: 0,
  CLIENT_INITIATED: 1,
  DUPLICATE_IDENTITY: 2,
  SERVER_SHUTDOWN: 3,
  PARTICIPANT_REMOVED: 4,
  ROOM_DELETED: 5,
  STATE_MISMATCH: 6,
  JOIN_FAILURE: 7,
  MIGRATION: 8,
  SIGNAL_CLOSE: 9,
  ROOM_CLOSED: 10,
  USER_UNAVAILABLE: 11,
  USER_REJECTED: 12,
  SIP_TRUNK_FAILURE: 13,
  CONNECTION_TIMEOUT: 14,
  MEDIA_FAILURE: 15,
})

// 返回空串表示无需提示（本端主动离开）
export function disconnectReasonMessage(reason) {
  switch (reason) {
    case DISCONNECT_REASON.CLIENT_INITIATED:
      return ''
    case DISCONNECT_REASON.DUPLICATE_IDENTITY:
      return '你已在其他页面或设备（如手机 App）进入大厅，本页已退出'
    case DISCONNECT_REASON.PARTICIPANT_REMOVED:
      return '你已被移出大厅'
    case DISCONNECT_REASON.ROOM_DELETED:
    case DISCONNECT_REASON.ROOM_CLOSED:
      return '大厅已关闭'
    case DISCONNECT_REASON.SERVER_SHUTDOWN:
      return '媒体服务器正在重启，请稍后重新进入大厅'
    default:
      return '与大厅的连接已断开，请重新进入'
  }
}

// LiveKit ConnectionErrorReason（tests 中与 livekit-client 导出值核对）
export const CONNECTION_ERROR_REASON = Object.freeze({
  NotAllowed: 0,
  ServerUnreachable: 1,
  InternalError: 2,
  Cancelled: 3,
  LeaveRequest: 4,
  Timeout: 5,
  WebSocket: 6,
  ServiceNotFound: 7,
})

// room.connect 失败 → 中文提示；返回空串表示用户主动取消
export function connectErrorMessage(err) {
  const reason = err && typeof err.reason === 'number' ? err.reason : null
  switch (reason) {
    case CONNECTION_ERROR_REASON.Cancelled:
      return ''
    case CONNECTION_ERROR_REASON.NotAllowed:
      return '大厅令牌无效或已过期，请重新进入'
    case CONNECTION_ERROR_REASON.ServerUnreachable:
    case CONNECTION_ERROR_REASON.WebSocket:
    case CONNECTION_ERROR_REASON.ServiceNotFound:
      return '无法连接媒体服务器，请检查网络或稍后重试'
    case CONNECTION_ERROR_REASON.Timeout:
      return '连接媒体服务器超时，请检查网络后重试'
    case CONNECTION_ERROR_REASON.LeaveRequest:
      return '服务器拒绝了本次进入，请稍后重试'
    default:
      return '连接大厅失败，请稍后重试'
  }
}

// WS 错误信封的 content 由后端给出中文原因；只做类型与长度兜底
export function wsErrorText(content, fallback = '操作失败，请稍后重试') {
  const text = typeof content === 'string' ? content.trim() : ''
  if (!text) return fallback
  return text.length > MAX_SERVER_MSG_CHARS ? text.slice(0, MAX_SERVER_MSG_CHARS) + '…' : text
}
