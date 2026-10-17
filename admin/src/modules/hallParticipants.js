// 多人大厅参与者状态合并（纯逻辑，可在 node 测试中直接运行）。
// 规则（契约第 2、4.2 节 + 任务要求）：
//   - 已连接 LiveKit 时，在场名单、麦克风、摄像头、正在说话以 LiveKit 房间事件为准（实时）；
//     服务端 /api/agent/hall/state 只补充 vid / agent_id / 拉黑状态 / 进入时间（30 秒校准一次）。
//   - 未连接时，完全使用服务端状态（客服未进入大厅也能看到在场情况与做管理操作）。
// 访客 vid 等同凭证：只存在内存里用于拉黑/私聊跳转，不展示、不写日志、不进 URL。
import { truncateChars } from './textLimits.js'
import { isValidVisitorId } from './conversationOps.js'

export const ROLE_VISITOR = 'visitor'
export const ROLE_AGENT = 'agent'
const VISITOR_IDENTITY = /^v_[0-9a-f]{20}$/
// 契约第 2 节补充（后端实现）：客服 identity 为 ^a_[0-9]{1,19}$
const AGENT_IDENTITY = /^a_[0-9]{1,19}$/
const VISITOR_CODE = /^\d{4}$/
const NAME_CONTROL = /[\u0000-\u001F\u007F\u200E\u200F\u202A-\u202E\u2066-\u2069]/g
const MAX_METADATA_CHARS = 1024
// 远大于契约上限 500 人，作为异常数据的硬防线
export const HALL_ROSTER_HARD_LIMIT = 2000

export function isValidHallIdentity(identity) {
  return typeof identity === 'string' && (VISITOR_IDENTITY.test(identity) || AGENT_IDENTITY.test(identity))
}

export function roleFromIdentity(identity) {
  if (typeof identity !== 'string') return ''
  if (VISITOR_IDENTITY.test(identity)) return ROLE_VISITOR
  if (AGENT_IDENTITY.test(identity)) return ROLE_AGENT
  return ''
}

// participant metadata 由服务端签发：{"role":"visitor"|"agent","code":"NNNN"}；解析失败返回空对象
export function parseParticipantMetadata(raw) {
  if (typeof raw !== 'string' || !raw || raw.length > MAX_METADATA_CHARS) return { role: '', code: '' }
  try {
    const v = JSON.parse(raw)
    if (!v || typeof v !== 'object') return { role: '', code: '' }
    return {
      role: v.role === ROLE_VISITOR || v.role === ROLE_AGENT ? v.role : '',
      code: typeof v.code === 'string' && VISITOR_CODE.test(v.code) ? v.code : '',
    }
  } catch {
    return { role: '', code: '' }
  }
}

export function sanitizeDisplayName(name, fallback = '') {
  if (typeof name !== 'string') return fallback
  const clean = truncateChars(name.replace(NAME_CONTROL, '').trim(), 64)
  return clean || fallback
}

function toMs(value) {
  if (value instanceof Date) return value.getTime()
  if (typeof value === 'number') return Number.isFinite(value) && value > 0 ? value : 0
  if (typeof value === 'string' && value.trim()) {
    const ms = Date.parse(value.trim())
    return Number.isFinite(ms) ? ms : 0
  }
  return 0
}

function bool(v) {
  return v === true || v === 'true' || v === 1
}

function defaultName(role, identity) {
  if (role === ROLE_AGENT) return '客服'
  return '访客 ' + String(identity || '').slice(2, 6)
}

// GET /api/agent/hall/state 的 participants[i]
export function normalizeServerParticipant(p) {
  if (!p || typeof p !== 'object' || !isValidHallIdentity(p.identity)) return null
  const idRole = roleFromIdentity(p.identity)
  const role = p.role === ROLE_VISITOR || p.role === ROLE_AGENT ? p.role : idRole
  if (role !== idRole) return null // 角色与身份前缀不一致视为异常数据
  return {
    identity: p.identity,
    name: sanitizeDisplayName(p.name, defaultName(role, p.identity)),
    role,
    vid: role === ROLE_VISITOR && isValidVisitorId(p.vid) ? p.vid : '',
    agentId: role === ROLE_AGENT && p.agent_id !== undefined && p.agent_id !== null ? String(p.agent_id).slice(0, 36) : '',
    micOn: bool(p.mic_on),
    cameraOn: bool(p.camera_on),
    joinedAt: toMs(p.joined_at),
    blocked: bool(p.blocked),
  }
}

// LiveKit Participant 的快照（由 hallRoom.js 生成纯对象传入）
export function normalizeLiveParticipant(s) {
  if (!s || typeof s !== 'object' || !isValidHallIdentity(s.identity)) return null
  const idRole = roleFromIdentity(s.identity)
  const meta = parseParticipantMetadata(s.metadata)
  const role = meta.role && meta.role === idRole ? meta.role : idRole
  return {
    identity: s.identity,
    name: sanitizeDisplayName(s.name, defaultName(role, s.identity)),
    role,
    micOn: bool(s.micOn),
    cameraOn: bool(s.cameraOn),
    speaking: bool(s.speaking),
    isLocal: bool(s.isLocal),
    joinedAt: toMs(s.joinedAt),
  }
}

function compareRows(a, b) {
  if (a.role !== b.role) return a.role === ROLE_AGENT ? -1 : 1
  if (a.isSelf !== b.isSelf) return a.isSelf ? -1 : 1
  const ja = a.joinedAt || Number.MAX_SAFE_INTEGER
  const jb = b.joinedAt || Number.MAX_SAFE_INTEGER
  if (ja !== jb) return ja - jb
  return a.identity < b.identity ? -1 : (a.identity > b.identity ? 1 : 0)
}

export class HallRoster {
  constructor({ hardLimit = HALL_ROSTER_HARD_LIMIT } = {}) {
    this.hardLimit = hardLimit
    this.live = new Map()
    this.server = new Map()
    this.serverCounts = null
    this.speakers = new Set()
    this.connected = false
    this.selfIdentity = ''
  }

  setConnected(connected, selfIdentity = '') {
    this.connected = !!connected
    this.selfIdentity = connected && isValidHallIdentity(selfIdentity) ? selfIdentity : ''
    if (!connected) {
      this.live.clear()
      this.speakers.clear()
    }
  }

  // 返回 true 表示这是服务端尚未知道的新身份（调用方可提前触发一次校准）
  upsertLive(snapshot) {
    const p = normalizeLiveParticipant(snapshot)
    if (!p) return false
    if (!this.live.has(p.identity) && this.live.size >= this.hardLimit) return false
    this.live.set(p.identity, p)
    return !this.server.has(p.identity)
  }

  replaceLive(snapshots) {
    this.live.clear()
    for (const s of Array.isArray(snapshots) ? snapshots : []) this.upsertLive(s)
  }

  removeLive(identity) {
    this.speakers.delete(identity)
    return this.live.delete(identity)
  }

  setSpeakers(identities) {
    this.speakers = new Set((Array.isArray(identities) ? identities : []).filter(isValidHallIdentity))
  }

  replaceServer(list, counts = null) {
    this.server.clear()
    for (const raw of Array.isArray(list) ? list.slice(0, this.hardLimit) : []) {
      const p = normalizeServerParticipant(raw)
      if (p) this.server.set(p.identity, p)
    }
    this.serverCounts = normalizeCounts(counts)
  }

  mergedRow(identity) {
    const live = this.live.get(identity)
    const srv = this.server.get(identity)
    if (this.connected) {
      if (!live) return null
      return {
        identity,
        name: live.name || srv?.name || defaultName(live.role, identity),
        role: live.role,
        vid: srv?.vid || '',
        agentId: srv?.agentId || '',
        micOn: live.micOn,
        cameraOn: live.cameraOn,
        speaking: this.speakers.has(identity) || live.speaking,
        blocked: !!srv?.blocked,
        joinedAt: srv?.joinedAt || live.joinedAt || 0,
        isSelf: identity === this.selfIdentity || live.isLocal,
        known: !!srv,
      }
    }
    if (!srv) return null
    return {
      identity,
      name: srv.name,
      role: srv.role,
      vid: srv.vid,
      agentId: srv.agentId,
      micOn: srv.micOn,
      cameraOn: srv.cameraOn,
      speaking: false,
      blocked: srv.blocked,
      joinedAt: srv.joinedAt,
      isSelf: false,
      known: true,
    }
  }

  rows() {
    const keys = this.connected ? this.live.keys() : this.server.keys()
    const out = []
    for (const id of keys) {
      const row = this.mergedRow(id)
      if (row) out.push(row)
    }
    return out.sort(compareRows)
  }

  get(identity) {
    return isValidHallIdentity(identity) ? this.mergedRow(identity) : null
  }

  counts() {
    if (!this.connected && this.serverCounts) return { ...this.serverCounts }
    return countRows(this.rows())
  }
}

export function countRows(rows) {
  const list = Array.isArray(rows) ? rows : []
  let speakers = 0
  let visitorSpeakers = 0
  let cameras = 0
  for (const r of list) {
    if (r.micOn) {
      speakers++
      if (r.role === ROLE_VISITOR) visitorSpeakers++
    }
    if (r.cameraOn) cameras++
  }
  return { participants: list.length, speakers, visitorSpeakers, cameras }
}

function normalizeCounts(c) {
  if (!c || typeof c !== 'object') return null
  const n = v => (Number.isFinite(Number(v)) && Number(v) >= 0 ? Math.floor(Number(v)) : 0)
  // 契约 counts.speakers 为占名额的访客开麦数
  return { participants: n(c.participants), speakers: n(c.speakers), visitorSpeakers: n(c.speakers), cameras: n(c.cameras) }
}

// 每行可用的管理操作（界面按钮可用性；真正的权限由后端校验，违反返回 40376）。
// 契约 4.2 补充：不能对自己操作；普通客服只能操作访客；管理员可以对其他客服静音 / 关摄像头 / 移出。
export function rowActions(row, { isAdmin = false } = {}) {
  const none = { mute: false, cameraOff: false, kick: false, block: false, chat: false }
  if (!row || row.isSelf) return none
  const visitor = row.role === ROLE_VISITOR
  const canTarget = visitor || (row.role === ROLE_AGENT && isAdmin === true)
  const hasVid = visitor && isValidVisitorId(row.vid)
  return {
    mute: canTarget && !!row.micOn,
    cameraOff: canTarget && !!row.cameraOn,
    kick: canTarget,
    block: hasVid && !row.blocked,
    chat: hasVid,
  }
}

// 名字筛选（仅按显示名，不暴露/匹配 vid）
export function filterRows(rows, keyword) {
  const k = typeof keyword === 'string' ? keyword.trim().toLowerCase() : ''
  if (!k) return Array.isArray(rows) ? rows : []
  return (Array.isArray(rows) ? rows : []).filter(r => String(r.name || '').toLowerCase().includes(k))
}
