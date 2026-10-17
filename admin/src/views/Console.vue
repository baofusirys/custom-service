<script setup>
import { onMounted, onUnmounted, ref, nextTick, computed, watch } from 'vue'
import { ElMessage, ElMessageBox, ElNotification } from 'element-plus'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'
import { formatBeijing, formatBeijingHM, formatMsgTime, formatGroupTime } from '../modules/timeFormat.js'
import http from '../api/http'
import { AgentWS } from '../api/ws'
import { playSound, unlockAudio, playRingLoop, stopRingLoop } from '../api/sound'
import { useSession } from '../store/session'
import {
  applyMessageReceipt,
  advanceMessageStatus,
  canonicalMessageStatus,
  createClientMessageId,
  isPendingMessage,
  latestServerMessageId,
  messageIdentityKeys,
  mergeMessageLists,
  messageStatusLabel,
} from '../modules/messageState'
import { MessageStatusSync } from '../modules/messageStatusSync'
// [113] 0.8.0 新功能：组件与纯逻辑模块（纯逻辑均有 node 测试，见 tests/）
import ConversationToolbar from '../components/console/ConversationToolbar.vue'
import ConversationMeta from '../components/console/ConversationMeta.vue'
import ConversationActions from '../components/console/ConversationActions.vue'
import VisitorProfileDrawer from '../components/console/VisitorProfileDrawer.vue'
import BlockVisitorDialog from '../components/console/BlockVisitorDialog.vue'
import QuickReplyPicker from '../components/console/QuickReplyPicker.vue'
import ChatSearchDialog from '../components/console/ChatSearchDialog.vue'
import {
  deleteVisitors,
  exportVisitor,
  exportVisitors,
  getVisitorProfile,
  markConversationRead,
  setVisitorMarkedUnread,
  setVisitorPinned,
} from '../api/visitors'
import { recallMessage } from '../api/chat'
import {
  BATCH_DELETE_MAX,
  BATCH_EXPORT_MAX,
  applyProfileToConversation,
  isValidVisitorId,
  moveToTop,
  normalizeVisitorIds,
  removeVisitorsFromList,
  summarizeDeleteResult,
  validateBatchSelection,
} from '../modules/conversationOps'
import {
  applyRecallToList,
  canRecallMessage,
  recallFailureMeansAlreadyRecalled,
  recallPlaceholder,
  recallSafeSyncCursor,
} from '../modules/recall'
import {
  TYPING_DISPLAY_TIMEOUT_MS,
  TYPING_SEND_INTERVAL_MS,
  TypingIndicator,
  TypingThrottle,
  parseParticipant,
} from '../modules/typing'
import { insertQuickReply } from '../modules/quickReply'
import { useRoute, useRouter } from 'vue-router'
import { consumeJumpTicket } from '../modules/privateChatJump'

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

const session = useSession()
const route = useRoute()
const router = useRouter()
const convs = ref([])
// [065][067] 客服列表过滤模式：'all' 全部 / 'contacted' 已主动联系
// 「已主动联系」判定：严格只信后端字段 has_visitor_msg
// 由后端 SQL EXISTS 计算（[067] 收紧为 messages.sender='visitor' 真实消息，voice/sys 事件不再算）
// [067] 去掉 unread>0 兜底：避免 sys 事件曾让 unread 虚高 → 把"只点了来电立即挂掉"的会话误判为已联系
// [085] 列表重构（修复"实际接待 450 客户、工作台只显示 4 个"）：
//   filterMode='all'       进行中(open)会话，滚动分页
//   filterMode='contacted' 客服真正回复过的客户(按访客去重，跨 open/closed)，滚动分页
//   「已联系」口径由 has_visitor_msg(访客发过消息) 改为后端 contacted(客服回复过 sender='agent'，按访客历史聚合)，
//   不再受 200 条窗口限制，会话超时重建也不丢。
const filterMode = ref('all')
const tagFilter = ref('')       // [113] 按标签精确筛选（契约第 1 节 tag 参数），空串=不筛选
const isContacted = (c) => c.contacted === true
const allTotal = ref(0)         // 「全部」总数（后端返回，与已加载条数解耦）
const contactedTotal = ref(0)   // 「已联系」总数
const convLoading = ref(false)  // 分页加载中（防重复触发）
const convHasMore = ref(true)   // 当前 tab 是否还有下一页
const CONV_PAGE = 50
let convOffset = 0
// 后端已按 mode 返回，前端不再本地过滤
const filteredConvs = computed(() => convs.value)
const activeConv = ref(null)
const messages = ref([])
// ============================================================
// [070] 消息本地缓存（进会话秒显，刷新页面/重开也不丢）—— 对齐 App 端方案
//   msgCache[visitorId]：内存消息数组（当前访客时与 messages.value 同引用）
//   localStorage 'cs_msgs:<visitorId>'：持久化最近 N 条；'cs_msgs:_index' 维护 LRU 顺序
//   HTTP / WSS / 本地 outbox 统一按稳定密码学消息 ID 幂等合并。
// ============================================================
const msgCache = {}
const MSG_PREFIX = 'cs_msgs:'
const MSG_INDEX = 'cs_msgs:_index'
const MSG_MAX_CONVS = 60
const MSG_MAX_PER_CONV = 200
function loadCachedMsgs(convId) {
  try {
    const v = JSON.parse(localStorage.getItem(MSG_PREFIX + convId) || '[]')
    return Array.isArray(v) ? v : []
  } catch { return [] }
}
function saveCachedMsgs(convId) {
  // pending outbox 也必须持久化；刷新/崩溃后沿用同一 m2-* ID 重发，后端按 ID 幂等。
  const list = (msgCache[convId] || []).slice(-MSG_MAX_PER_CONV)
  try {
    localStorage.setItem(MSG_PREFIX + convId, JSON.stringify(list))
    let idx = []
    try { idx = JSON.parse(localStorage.getItem(MSG_INDEX) || '[]') } catch {}
    idx = [convId, ...idx.filter(c => c !== convId)]
    while (idx.length > MSG_MAX_CONVS) { const e = idx.pop(); localStorage.removeItem(MSG_PREFIX + e) }
    localStorage.setItem(MSG_INDEX, JSON.stringify(idx))
  } catch {}
}
function lastRealId(convId) {
  // [113] 增量游标回退一个撤回窗口（2 分钟 + 余量）：窗口内消息重新拉取，
  // 即使撤回事件在断线期间丢失，也能从历史接口拿到 recalled=true，不会继续显示已撤回内容。
  return recallSafeSyncCursor(msgCache[convId] || [])
}
function mergeMessages(visitorID, incoming, replace) {
  const merged = mergeMessageLists(msgCache[visitorID] || [], incoming, replace)
  msgCache[visitorID] = merged
  // [098] 缓存键是 visitor_id，活动判断必须使用同一维度；这是 A01 的直接根因修复。
  if (activeConv.value?.visitor_id === visitorID) messages.value = merged
  return merged
}
// ============================================================
// [068] per-conv 草稿 / 附件隔离 —— 防"切会话串台"导致敏感账密泄露
// ------------------------------------------------------------
// 历史 bug：旧版 draft = ref('') 是单例全局变量。客服在访客 A 输入框
// 打了「敏感账密」草稿（未发送），点切到访客 B，textarea 内容仍残留
// A 的草稿；若客服一回车，文本会以 B 的 activeConv.id 发出去 → 敏感
// 信息错发给 B，造成 critical 数据泄露。
//
// 修复方案：把 draft / pendingFiles 改成 per-conv 字典，
//   drafts          = { [convId]: string }
//   pendingFilesMap = { [convId]: [{file, blobUrl, isImage}] }
// 通过 computed currentDraft / currentPendingFiles 绑定到当前活跃会话；
// pickConv 不再清空草稿，切回旧会话自动恢复。
//
// 配合 sendText / uploadAndSendFile 入口 snapshot `sendingConvId`，
// 即使发送途中用户被弹窗 / race 切走会话，消息也锁定原 conv，
// 不会污染新会话视图。
// ============================================================
const drafts = ref({})          // { [convId]: string }
// [040] 待发送文件队列（粘贴 / 选附件追加；点发送时依次上传）
// 每项：{ file: File, blobUrl: string|null, isImage: bool }
// [068] 改成 per-conv 字典，杜绝切会话附件串台
const pendingFilesMap = ref({}) // { [convId]: [{file, blobUrl, isImage}] }

// [068] textarea v-model 绑定 —— get 读当前活跃会话草稿，set 写回字典
const currentDraft = computed({
  get: () => (activeConv.value ? (drafts.value[activeConv.value.id] || '') : ''),
  set: (v) => {
    if (activeConv.value) drafts.value[activeConv.value.id] = v
  },
})
// [068] 模板 v-for 当前活跃会话的 pending 附件
const currentPendingFiles = computed(
  () => (activeConv.value ? (pendingFilesMap.value[activeConv.value.id] || []) : [])
)

// [055] 关联访客（同 IP 30 天内出现的其他 vid）
const relatedDialog = ref(false)
const relatedLoading = ref(false)
const relatedList = ref([])      // [{vid, identifier, ip, country, city, last_seen, first_seen}]
const relatedCount = ref({})     // { vid: count } 缓存避免每次刷新都拉

async function openRelatedDialog(vid) {
  if (!vid) return
  relatedDialog.value = true
  relatedLoading.value = true
  relatedList.value = []
  try {
    const r = await http.get(`/agent/visitor/${vid}/related`)
    relatedList.value = r.data || []
    relatedCount.value[vid] = r.count || 0
  } catch (e) {
    relatedList.value = []
  } finally {
    relatedLoading.value = false
  }
}
const onlineStats = ref({ visitors: 0, agents: 0 })
const fileInput = ref(null)
const sending = ref(false)
const agentSound = ref('agent1') // 由 /admin/settings 拉到的客服端通知音
const notifyVisitorEnter = ref(true) // [082] 新访客进入是否提醒(弹窗+声音)；前端二道保险，后端 service.go 也已按此 gate 下发
const myConnId = ref('') // 自己 WSS 连接 ID（hello 时拿到，用于多端同步去重）
let ws = null
let statusSync = null

async function loadSoundPref() {
  // 仅管理员可读全设置；普通客服 fallback 默认值
  if (session.agent?.role !== 'admin') return
  try {
    const r = await http.get('/admin/settings')
    agentSound.value = r.data?.agent_notify_sound || 'agent1'
    notifyVisitorEnter.value = r.data?.notify_visitor_enter !== 'false' // [082] 默认开；显式 'false' 才关
  } catch {}
}

// [085] 分页加载会话列表。reset=true 回首页(切 tab/刷新)，false 追加下一页(滚动触底)。
async function loadConvs(reset = true) {
  if (convLoading.value) return
  convLoading.value = true
  try {
    if (reset) { convOffset = 0; convHasMore.value = true }
    const r = await http.get('/agent/conversations', {
      params: { mode: filterMode.value, offset: convOffset, limit: CONV_PAGE, tag: tagFilter.value || undefined }
    })
    const data = r.data || []
    if (reset) {
      convs.value = data
      pruneSelection()   // [113] 重载后只保留仍可见客户的勾选
    } else {
      // 去重追加：防 WSS 上浮/并发导致同一会话重复
      const seen = new Set(convs.value.map(x => x.id))
      convs.value.push(...data.filter(x => !seen.has(x.id)))
    }
    convOffset += data.length
    convHasMore.value = data.length === CONV_PAGE
    if (filterMode.value === 'contacted') contactedTotal.value = r.total || 0
    else allTotal.value = r.total || 0
  } catch (e) {
    // 静默：下次滚动/定时兜底会重试
  } finally {
    convLoading.value = false
  }
}
// 兼容旧调用名：重载当前 tab 首页
async function refreshConvs() { await loadConvs(true) }
// [088] 异步预取「已联系」总数（tab 数字就准，不阻塞主列表）
async function refreshSideTotals() {
  try {
    const c = await http.get('/agent/conversations', { params: { mode: 'contacted', offset: 0, limit: 1, tag: tagFilter.value || undefined } })
    contactedTotal.value = c.total || 0
  } catch {}
}
// 列表滚动触底 → 加载下一页
const convScrollRef = ref(null)
function onConvScroll({ scrollTop }) {
  const wrap = convScrollRef.value?.wrapRef
  if (!wrap || !convHasMore.value || convLoading.value) return
  if (wrap.scrollHeight - scrollTop - wrap.clientHeight < 150) loadConvs(false)
}
// 切换 tab：重载对应口径首页
watch(filterMode, () => { clearSelection(); loadConvs(true) })
// [113] 切换标签筛选：清空勾选并重载首页（总数同步按标签口径）
watch(tagFilter, () => { clearSelection(); loadConvs(true); refreshSideTotals() })

// [090] 按「客户(访客)」加载其所有会话段的完整历史消息（合并成一条时间流）。
//   配合 [088] 列表按客户聚合：详情也按客户聚合。缓存键统一用 visitorID。
const messageLoadVersion = Object.create(null)
async function loadMessages(visitorID) {
  const requestVersion = (messageLoadVersion[visitorID] || 0) + 1
  messageLoadVersion[visitorID] = requestVersion
  // [070] 1) 缓存秒显：内存没有就读 localStorage，立即铺到 UI（0 转圈）
  let cached = msgCache[visitorID]
  if (!cached) {
    cached = loadCachedMsgs(visitorID).sort((a, b) => new Date(a.created_at) - new Date(b.created_at))
    msgCache[visitorID] = cached
  }
  if (activeConv.value?.visitor_id === visitorID) {
    messages.value = cached
    if (cached.length) { await nextTick(); scrollToBottom() }
  }
  // [070] 2) 后台同步：有本地真实消息走增量(after) 只补新的，否则全量首拉
  const lastReal = lastRealId(visitorID)
  const q = lastReal ? `?limit=200&after=${encodeURIComponent(lastReal)}` : `?limit=100`
  const r = await http.get(`/agent/visitor/${visitorID}/messages${q}`)
  // 同一访客如果已有更新的一次加载完成，旧慢响应不得覆盖新状态。
  if (messageLoadVersion[visitorID] !== requestVersion) return
  // 全量接口返回 DESC（最新在前）→ reverse 成 ASC；增量 after 已是 ASC
  const fetched = r.data || []
  const norm = lastReal ? fetched : fetched.slice().reverse()
  mergeMessages(visitorID, norm, !lastReal)
  saveCachedMsgs(visitorID)
  if (activeConv.value?.visitor_id === visitorID) { await nextTick(); scrollToBottom() }
}

async function pickConv(c) {
  // [063] 乐观 UI：未读 badge 立刻消失，不等任何网络 RPC
  // 旧逻辑串行 4 步（loadMessages → assign → unread=0 → sendReadAck），美国服务器 200ms RTT 下
  // 单次点击要 1-2 秒才让红色未读 badge 消失，体感像「卡顿」。
  // 改：unread 先清，DB 操作后台跑；loadMessages 和 assign 是独立 RPC 可并行（少一半串行时间）。
  activeConv.value = c
  c.unread = 0
  // 拉消息 + 接管会话并行（两个 RPC 互相独立，时间 = max 而非 sum）
  // [117] 接管被拒（该客户由在线的其他客服接待）时不弹错误：会话转为只能查看、隐藏输入区，并显示接待人
  const [, assigned] = await Promise.all([
    loadMessages(c.visitor_id),   // [090] 按客户拉完整历史（跨该访客所有会话段）
    assignConversation(c),
  ])
  if (!assigned) return
  // 刷新/崩溃恢复出的本地 outbox 在会话重新接管后立即用原 ID 幂等重发。
  resendPending()
  // assign 后 agent 已加入 byConv[c.id]，立即发 WSS read：
  // 触发后端 UpdateLastRead + FanoutToConv 通知访客「客服已读」
  const latestVisitorMessage = [...messages.value].reverse().find(m =>
    m.sender === 'visitor' && (!m.conv_id || m.conv_id === c.id))
  sendReadAck(c.id, latestVisitorMessage?.id)
  // 本地把所有访客消息直接标 read（自己看到了）
  for (const m of messages.value) {
    if (m.sender === 'visitor') m.read = true
  }
  // [113] 被手动“标为未读”的客户：打开即走 HTTP 标记已读，服务端自动清除 marked_unread（契约第 5 节）
  if (c.marked_unread === true) {
    c.marked_unread = false
    markConversationRead(c.id).catch(() => {})
  }
}

// [117] 接管会话：成功返回 true；被拒（他人在线接待 / 已删除）时把会话标为只读并记录原因，返回 false。
async function assignConversation(c) {
  try {
    await http.post(`/agent/conversations/${c.id}/assign`, null, { silent: true })
    c._sendBlocked = ''
    return true
  } catch (e) {
    const body = e?.response?.data || e || {}
    if (e?.response?.status === 403 || e?.response?.status === 409 || body.code === 40303) {
      c._sendBlocked = body.msg || '该客户正由其他客服接待，你只能查看聊天记录'
    } else {
      ElMessage.error(body.msg || '接管会话失败，请重试')
    }
    // 只读状态下仍可标记已读（服务端按「能查看即可已读」校验）
    markConversationRead(c.id).catch(() => {})
    return false
  }
}

async function retryAssign() {
  const c = activeConv.value
  if (!c || c._readonly) return
  if (await assignConversation(c)) {
    resendPending()
    ElMessage.success('已接管该客户，可以回复了')
  }
}

function sendReadAck(convID, messageID) {
  if (ws?.alive && convID) {
    ws.send({ type: 'read', conv: convID, id: messageID || undefined, ts: Date.now() })
  }
}

// 标记「自己发过的、目标消息之前」为已读；messageID 优先，时间仅兼容旧端。
function markMineReadUpTo(convID, messageID, upToTs) {
  const myID = String(session.agent?.id)
  const target = messageID ? messages.value.find(m => m.id === messageID) : null
  const targetTs = target ? new Date(target.created_at).getTime() : upToTs
  for (const m of messages.value) {
    if (m.sender === 'agent' && String(m.sender_ref) === myID &&
        (!m.conv_id || !convID || m.conv_id === convID)) {
      const mTs = new Date(m.created_at).getTime()
      if (mTs <= targetTs) advanceMessageStatus(m, 'read')
    }
  }
  const visitorID = activeConv.value?.visitor_id
  if (visitorID) saveCachedMsgs(visitorID)
}

// 该组是否是「页面跳转」横幅（不是普通消息组）
function isPageGroup(g) {
  return g.sender === 'sys' && g.items.length > 0 &&
    typeof g.items[0].sender_ref === 'string' &&
    g.items[0].sender_ref.indexOf('page:') === 0
}

function pageURL(m) {
  const raw = m.page_url || (m.sender_ref && m.sender_ref.indexOf('page:') === 0 ? m.sender_ref.slice(5) : '')
  try {
    const u = new URL(raw, window.location.origin)
    return (u.protocol === 'http:' || u.protocol === 'https:') ? u.href : ''
  } catch { return '' }
}
function pageTitle(m) {
  if (m.page_title) return m.page_title
  // 历史消息从 content 反解析「xxx」
  if (m.content) {
    const match = /「(.+?)」/.exec(m.content)
    if (match) return match[1]
  }
  return ''
}

// [079][098] 发送可靠性 —— 数据库提交 ACK + 超时 failed + 点击重发 + 重连自动重发
//   ACK 仅表示数据库提交成功（persisted/已发送）；delivery/read 是访客端的独立确认。
//   12s 未收 ACK→未送达(红可重发); WS 重连(onOpen)自动重发未确认。
const ackTimers = {}
const resendLastAttempt = new Map()
const RESEND_MIN_INTERVAL_MS = 1500
function scheduleAckTimeout(mid) {
  if (ackTimers[mid]) clearTimeout(ackTimers[mid])
  ackTimers[mid] = setTimeout(() => { markStatus({ id: mid }, 'failed', false) }, 12000)
}
function clearAckTimers(identities) {
  for (const id of identities) {
    if (ackTimers[id]) clearTimeout(ackTimers[id])
    delete ackTimers[id]
    resendLastAttempt.delete(id)
  }
}
function markStatus(receiptOrID, st, shouldBroadcast = true) {
  const receipt = typeof receiptOrID === 'string' ? { id: receiptOrID } : (receiptOrID || {})
  let result = { matched: false, changed: false, identities: messageIdentityKeys(receipt) }
  let cacheKey = ''
  for (const [key, list] of Object.entries(msgCache)) {
    result = applyMessageReceipt(list, receipt, st)
    if (result.matched) { cacheKey = key; break }
  }
  if (!result.matched) result = applyMessageReceipt(messages.value, receipt, st)
  if (result.matched && cacheKey) saveCachedMsgs(cacheKey)
  if (st !== 'sending') clearAckTimers([...result.identities, ...messageIdentityKeys(receipt)])
  // ACK 可能属于另一标签页的本地 outbox：即使本页没命中也发布无正文状态事件。
  if (shouldBroadcast && (st === 'persisted' || st === 'delivered' || st === 'read')) {
    statusSync?.publish(receipt, st, receipt.conv || result.message?.conv_id || '')
  }
  return result
}
function buildResendEnv(m, convId) {
  const d = {
    type: 'chat', conv: convId, id: m.id,
    client_id: m.client_id || m.local_id || m.id,
    ts: Date.now(), prio: 0,
  }
  if (m.media_url?.Valid) { d.content = ''; d.media = m.media_url.String; d.mkind = m.media_kind?.String; d.mname = m.media_name?.String }
  else { d.content = m.content }
  return d
}
function canAutoResend(m) {
  const key = m.client_id || m.id
  const now = Date.now()
  const last = resendLastAttempt.get(key) || 0
  if (now - last < RESEND_MIN_INTERVAL_MS) return false
  resendLastAttempt.set(key, now)
  return true
}
function resendMsg(m) {
  if (!m || !activeConv.value) return
  if (m.conv_id && m.conv_id !== activeConv.value.id) {
    ElMessage.warning('该消息属于历史会话，不能发送到当前会话')
    return
  }
  resendLastAttempt.set(m.client_id || m.id, Date.now())
  markStatus(m, 'sending', false)
  if (ws?.send(buildResendEnv(m, m.conv_id || activeConv.value.id))) scheduleAckTimeout(m.id)
  else markStatus(m, 'failed', false)
}
function resendPending() {
  if (!activeConv.value) return
  for (const m of messages.value) {
    if (m.sender === 'agent' && (!m.conv_id || m.conv_id === activeConv.value.id) &&
        (m.status === 'sending' || m.status === 'failed')) {
      if (!canAutoResend(m)) continue
      if (ws?.send(buildResendEnv(m, m.conv_id || activeConv.value.id))) {
        advanceMessageStatus(m, 'sending')
        scheduleAckTimeout(m.id)
      }
    }
  }
  saveCachedMsgs(activeConv.value.visitor_id)
}

let reconnectRecoveryVersion = 0
async function recoverPendingAfterReconnect() {
  const current = activeConv.value
  if (!current) return
  const version = ++reconnectRecoveryVersion
  try {
    // 先用 HTTP 对账吃掉「服务端已落库但 ACK 丢失」的消息，再只重发真正 pending。
    await loadMessages(current.visitor_id)
  } catch {}
  if (version !== reconnectRecoveryVersion || !ws?.alive || activeConv.value?.id !== current.id) return
  resendPending()
}

// [040] 发送：先发文本（如果有），再依次上传 pendingFiles 队列里的所有附件
// [068] 入口 snapshot sendingConvId：杜绝发送过程中切换会话造成 conv-content 错配
async function sendText() {
  // [068] 入口锁定 conv —— 整个函数生命周期内不再读 activeConv.value.id
  const sendingConvId = activeConv.value?.id
  if (!sendingConvId) { ElMessage.warning('请先选择一个会话再发送'); return }   // [078] 明确提示、不静默（配合后端 [077] 防孤儿）
  const text = (drafts.value[sendingConvId] || '').trim()
  const fileItems = pendingFilesMap.value[sendingConvId] || []
  const files = fileItems.map(it => it.file)
  if (!text && files.length === 0) return
  if (!ws?.alive) {
    ElMessage.warning('与服务器断开，正在重连')
    return
  }
  // [068] 同时锁住 conv 对象本身，避免后续 activeConv 切走时找不到原 conv
  const sendingConv = convs.value.find(x => x.id === sendingConvId) || activeConv.value
  sending.value = true
  try {
    if (text) {
      const now = new Date().toISOString()
      // [068] WSS 路由用 sendingConvId（锁定的会话），不读 activeConv
      const sentMid = createClientMessageId()
      const sentOk = ws.send({ type: 'chat', conv: sendingConvId, content: text, id: sentMid, client_id: sentMid, ts: Date.now(), prio: 0 })   // [079] 带 id 供 ACK 匹配
      // [068] 仅当 UI 仍停留在 sendingConvId 才做本地乐观渲染，
      // 否则切走后 push 到 messages.value 会污染新会话视图
      if (activeConv.value?.id === sendingConvId) {
        messages.value.push({
          id: sentMid,
          client_id: sentMid,
          sender: 'agent',
          sender_ref: String(session.agent?.id),
          conv_id: sendingConvId,
          content: text,
          created_at: now,
          status: sentOk ? 'sending' : 'failed'   // [079] 没发出→未送达
        })
        saveCachedMsgs(sendingConv.visitor_id)
      }
      if (sentOk) scheduleAckTimeout(sentMid)
      // 不论是否切走，conv 对象本身的 last_message / updated_at 都要更新（左侧列表预览）
      if (sendingConv) {
        sendingConv.last_message = { sender: 'agent', content: text, created_at: now }
        sendingConv.updated_at = now
        // [091] 发完立即上浮到列表顶部（跟 WSS 收消息一致，不等刷新）
        const idx = convs.value.indexOf(sendingConv)
        if (idx > 0) { convs.value.splice(idx, 1); convs.value.unshift(sendingConv) }
      }
      // [068] 按 sendingConvId 清草稿，不影响新切到的其他 conv 草稿
      drafts.value[sendingConvId] = ''
    }
    if (files.length > 0) {
      // [068] 按 sendingConvId 清 pending，不影响其他 conv 的 pending
      clearPendingFor(sendingConvId)
      for (const f of files) {
        // [068] 把 sendingConvId 显式传给上传函数，避免上传过程中切走出错
        await uploadAndSendFile(f, sendingConvId)
      }
    }
  } finally {
    sending.value = false
    nextTick(scrollToBottom)
  }
}

// 公共上传函数，附件按钮和粘贴事件共用
// [068] 加 convId 参数：调用方在入口 snapshot 后传入，杜绝 race 时 activeConv 已切走
async function uploadAndSendFile(file, convId) {
  // [068] 兼容旧调用（未传 convId）：fallback 用当前 activeConv，但发送路径已全部传入
  if (!convId) convId = activeConv.value?.id
  if (!convId) { ElMessage.warning('会话已失效，请重新选择会话'); return }   // [078] conv 空明确提示，不静默丢弃
  if (!file) return
  const fd = new FormData()
  fd.append('file', file)
  fd.append('uploader', 'agent')
  fd.append('conv_id', convId)
  try {
    const r = await http.post('/upload', fd, { headers: { 'Content-Type': 'multipart/form-data' } })
    const upMid = createClientMessageId()
    const upOk = ws?.send({
      type: 'chat', conv: convId, id: upMid,
      client_id: upMid,
      content: '', media: r.url, mkind: r.kind, mname: r.name, msize: r.size,
      ts: Date.now(), prio: 0
    })
    // [068] 仅当 UI 仍停在 convId 才本地乐观渲染，否则只发不渲染（避免污染新会话）
    if (activeConv.value?.id === convId) {
      messages.value.push({
        id: upMid, client_id: upMid, conv_id: convId, sender: 'agent', sender_ref: String(session.agent?.id),
        content: '',
        media_url: { String: r.url, Valid: true },
        media_kind: { String: r.kind, Valid: true },
        media_name: { String: r.name, Valid: true },
        created_at: new Date().toISOString(),
        status: upOk ? 'sending' : 'failed'   // [079]
      })
      saveCachedMsgs(activeConv.value.visitor_id)
      nextTick(scrollToBottom)
      if (upOk) scheduleAckTimeout(upMid)
    }
  } catch (error) {
    ElMessage.error(error?.message || '文件发送失败，请重试')
  }
}

// [040] 选附件：multiple 支持，所有文件追加到 pendingFiles 队列
// [068] 入口 snapshot convId，避免 for 循环中途 activeConv 切换
function pickFile(e) {
  const convId = activeConv.value?.id
  if (!convId) { e.target.value = ''; return }
  const files = e.target.files
  if (files && files.length) {
    for (const f of files) addPendingFile(f, convId)
  }
  e.target.value = ''
}

// ============= [040] pendingFiles 队列管理（[068] 改为 per-conv 字典）=============
function fmtBytes(n) {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

// [068] 按 convId 加入 pending 列表；调用方必须显式传 convId
function addPendingFile(file, convId) {
  if (!file || !convId) return
  const isImage = (file.type || '').indexOf('image/') === 0
  if (!pendingFilesMap.value[convId]) {
    pendingFilesMap.value[convId] = []
  }
  pendingFilesMap.value[convId].push({
    file,
    blobUrl: isImage ? URL.createObjectURL(file) : null,
    isImage,
  })
}

// [068] 按 convId 移除 pending 项；模板点 × 时传当前 activeConv.id（模板层面已锁）
function removePendingFile(item, convId) {
  if (!convId) convId = activeConv.value?.id
  if (!convId) return
  const list = pendingFilesMap.value[convId]
  if (!list) return
  const idx = list.indexOf(item)
  if (idx < 0) return
  if (item.blobUrl) {
    try { URL.revokeObjectURL(item.blobUrl) } catch {}
  }
  list.splice(idx, 1)
}

// [068] 按 convId 清空 pending 列表 + revoke blob URL，防内存泄漏
function clearPendingFor(convId) {
  if (!convId) return
  const list = pendingFilesMap.value[convId]
  if (!list) return
  for (const it of list) {
    if (it.blobUrl) {
      try { URL.revokeObjectURL(it.blobUrl) } catch {}
    }
  }
  delete pendingFilesMap.value[convId]
}

// 复制消息内容到剪贴板：文本消息复制 m.content；文件/图片消息复制 URL
function copyMessage(m) {
  const text = m.content || (m.media_url?.String || m.media_url || '')
  if (!text) return
  const writeText = (s) => {
    if (navigator.clipboard?.writeText) {
      return navigator.clipboard.writeText(s)
    }
    // 老浏览器 fallback：临时 textarea + execCommand
    const ta = document.createElement('textarea')
    ta.value = s; ta.style.position = 'fixed'; ta.style.left = '-9999px'
    document.body.appendChild(ta); ta.select()
    try { document.execCommand('copy') } catch {}
    document.body.removeChild(ta)
    return Promise.resolve()
  }
  writeText(text).then(() => {
    ElMessage.success({ message: '已复制', duration: 1500, offset: 60 })
  }).catch(() => {
    ElMessage.warning({ message: '复制失败', duration: 1500 })
  })
}

// [040] 粘贴：剪贴板里所有 file 都追加到 pendingFiles 队列（支持一次粘多张图）
// 纯文本粘贴不拦截，仍走默认 textarea 行为
// [068] 入口 snapshot convId，避免循环中途 activeConv 切走
function onPasteDraft(e) {
  const convId = activeConv.value?.id
  if (!convId) return
  const items = e.clipboardData?.items
  if (!items) return
  let hadFile = false
  for (const it of items) {
    if (it.kind === 'file') {
      const f = it.getAsFile()
      if (f) { addPendingFile(f, convId); hadFile = true }
    }
  }
  if (hadFile) e.preventDefault()
}

function scrollToBottom() {
  const el = document.getElementById('msg-list')
  if (el) el.scrollTop = el.scrollHeight
}

function isMine(m) {
  return m.sender === 'agent' && String(m.sender_ref) === String(session.agent?.id)
}

function mediaURL(m) {
  const raw = m.media_url?.Valid ? m.media_url.String : (typeof m.media_url === 'string' ? m.media_url : '')
  try {
    const u = new URL(raw, window.location.origin)
    if (u.origin !== window.location.origin || !u.pathname.startsWith('/files/')) return ''
    return u.pathname + u.search
  } catch { return '' }
}

function mediaKind(m) {
  if (m.media_kind?.Valid) return m.media_kind.String
  if (typeof m.media_kind === 'string') return m.media_kind
  return ''
}

function mediaName(m) {
  if (m.media_name?.Valid) return m.media_name.String
  if (typeof m.media_name === 'string') return m.media_name
  return '附件'
}

// 把连续消息按 5 分钟内同发送者分组，组与组之间显示时间分隔
const grouped = computed(() => {
  const groups = []
  let last = null
  for (const m of messages.value) {
    const t = dayjs(m.created_at)
    const same =
      last &&
      last.sender === m.sender &&
      last.sender_ref === m.sender_ref &&
      t.diff(last.last, 'minute') < 5
    if (same) {
      last.items.push(m)
      last.last = t
    } else {
      last = { sender: m.sender, sender_ref: m.sender_ref, ts: t, last: t, items: [m] }
      groups.push(last)
    }
  }
  return groups
})

// [113] 时间一律按北京时间显示，不随客服电脑的时区变化（原 dayjs(t).format 按浏览器本地时区）
function fmtAbs(t) { return formatBeijing(t, { empty: '' }) }
function fmtHM(t) { return formatBeijingHM(t) }
function fmtMsgTime(t) { return formatMsgTime(t) }   // [081] 今天 HH:mm / 今年 MM-DD HH:mm / 往年 YYYY-MM-DD HH:mm
function fmtGroupTime(t) { return formatGroupTime(t) }

// [059] 会话列表第 3 行：地理位置 + IP 拼装。任一存在才返非空（外层 v-if 隐藏行）
function convGeoIp(c) {
  const parts = []
  const loc = [c.country, c.city].filter(Boolean).join('·')
  if (loc) parts.push(loc)
  if (c.ip) parts.push(c.ip)
  return parts.join(' · ')
}

function lastMsgPreview(c) {
  const lm = c.last_message
  if (lm && lm.content) {
    let prefix = ''
    if (lm.sender === 'agent') prefix = '我：'
    else if (lm.sender === 'sys') prefix = ''
    else prefix = ''
    return prefix + lm.content
  }
  // fallback：地理位置 / 活动时间
  const loc = [c.country, c.city].filter(Boolean).join(' · ')
  return loc || ('最近活动 · ' + fmtGroupTime(c.updated_at))
}

function senderName(g) {
  if (g.sender === 'visitor') {
    return activeConv.value?.identifier || ('访客 ' + (activeConv.value?.visitor_id || '').slice(0, 6))
  }
  if (g.sender === 'agent') {
    if (isMineGroup(g)) return session.agent?.nickname || session.agent?.username || '我'
    return '客服 ' + (g.sender_ref || '')
  }
  return '系统'
}

function isMineGroup(g) {
  return g.sender === 'agent' && String(g.sender_ref) === String(session.agent?.id)
}

function avatarChar(name) {
  if (!name) return '?'
  // 优先中文首字
  const c = name.replace(/[\s_-]/g, '').charAt(0)
  return c.toUpperCase()
}

function visitorAvatarColor(id) {
  if (!id) return '#909399'
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) & 0xffffffff
  const colors = ['#67C23A', '#E6A23C', '#F56C6C', '#409EFF', '#909399', '#9C27B0', '#00BCD4', '#FF9800']
  return colors[Math.abs(h) % colors.length]
}

async function refreshStats() {
  try {
    const h = await http.get('/health')
    onlineStats.value = { visitors: h.visitors, agents: h.agents }
  } catch {}
}

// 防抖：WSS 收到非当前会话的新消息会触发 refreshConvs；多条短时间内只触发一次。
let convsDebounce = null
function scheduleConvsRefresh() {
  if (convsDebounce) return
  convsDebounce = setTimeout(() => {
    convsDebounce = null
    refreshConvs()        // [085] 重载当前 tab 首页（新会话/新消息上浮）
    refreshSideTotals()   // [086] 顺带刷新「待回复」「已联系」总数
  }, 3000)
}

let convsTimer
onMounted(async () => {
  await Promise.all([refreshConvs(), refreshStats(), loadSoundPref(), refreshSideTotals()])
  openFromHallJump()   // [119] 大厅「私聊」跳转：一次性票据定位访客会话
  statusSync = new MessageStatusSync({
    agentID: session.agent?.id,
    onEvent: event => markStatus(event, event.status, false),
  })
  statusSync.start()
  // 解锁 AudioContext（Chrome 等需要用户手势）—— 用户既然能进到 console 页就算手势
  document.addEventListener('click', unlockAudio, { once: true, capture: true })
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new AgentWS({
    url: `${proto}://${location.host}/ws/agent`,
    token: session.token,
    onOpen: () => {                      // 建连后等 hello 再做状态恢复，避免 connID 尚未建立时重发
      refreshConvs()                     // [092] 重连对账：拉最新第一页，补断线期间漏的消息（先显缓存，后台静默刷新）
      refreshSideTotals()                // 顺带刷新「已联系」总数
    },
    onMessage: (env) => {
      // hello：记住自己的 connID（多端同步去重必需）
      if (env.type === 'hello') {
        myConnId.value = env.extra?.conn_id || ''
        recoverPendingAfterReconnect()
        return
      }
      // [098] ACK 与数据库提交绑定，只表示 persisted（已发送），不能冒充已送达。
      if (env.type === 'ack') {
        markStatus(env, env.extra?.status || 'persisted')
        return
      }
      if (env.type === 'delivery') {
        markStatus(env, 'delivered')
        return
      }
      if (env.type === 'error') {
        if (env.id || env.client_id || env.extra?.client_id) markStatus(env, 'failed', false)
        if (env.content) ElMessage.error(env.content)
        return
      }
      // [113] 删除 / 资料更新 / 撤回 / 正在输入 等新事件统一在独立函数处理
      if (handleFeatureEvent(env)) return

      // 语音通话信令分发
      if (env.type && env.type.indexOf('voice_') === 0) {
        handleVoiceSignal(env)
        return
      }

      const myId = String(session.agent?.id)

      // 已读事件
      if (env.type === 'read') {
        const fromVisitor = env.from && env.from.startsWith('visitor:')
        const fromAgent = env.from && env.from.startsWith('agent:')
        const isFromMyAccount = fromAgent && env.from.split(':')[1] === myId

        // 同账号在另一端读了 → 同步清掉本端该 conv 的 unread
        if (isFromMyAccount && env.conn !== myConnId.value) {
          const c = convs.value.find(x => x.id === env.conv)
          if (c && c.unread > 0) c.unread = 0
          return
        }
        // 访客读了客服消息 → 当前会话标 mine 已读
        if (fromVisitor && activeConv.value && env.conv === activeConv.value.id) {
          markMineReadUpTo(env.conv, env.id, env.ts || Date.now())
        }
        return
      }
      if (env.type === 'sys') {
        // 系统通知：访客进入提醒（弹 toast + 播声 + 刷会话列表）
        if (env.extra?.kind === 'visitor_enter') {
          // [082] 二道保险：本地开关关了就不弹不响（后端 service.go 也已按 notify_visitor_enter gate 下发）
          if (notifyVisitorEnter.value) {
            ElNotification({
              title: '访客进入',
              message: env.content || '有新访客进入网站',
              type: 'info',
              duration: 4500,
              position: 'bottom-right'
            })
            playSound(agentSound.value)
          }
        }
        scheduleConvsRefresh()
        return
      }
      if (env.type !== 'chat') return

      const fromAgent = env.from && env.from.startsWith('agent:')
      const fromVisitor = env.from && env.from.startsWith('visitor:')
      const fromSys = env.from === 'sys'
      if (fromVisitor) clearVisitorTyping(env.from)   // [113] 收到访客消息即隐藏“对方正在输入”
      // 多端去重改用 connID：只有自己这一端（同 connID）发的回声才跳过；
      // 同账号另一端（web/app）发的消息，本端正常接受（这才是「双端同步」的关键）
      const isMyConnEcho = env.conn && env.conn === myConnId.value
      if (isMyConnEcho) return

      const inCurrent = activeConv.value && env.conv === activeConv.value.id
      if (inCurrent) {
        // 页面跳转事件：用 sender_ref 携带 URL 标记，前端按横幅样式渲染
        const isPageNav = fromSys && env.extra && env.extra.kind === 'page_navigation'
        const senderRef = isPageNav
          ? 'page:' + (env.extra?.url || '')
          : (env.from?.includes(':') ? env.from.split(':')[1] : (fromSys ? 'system' : ''))
        const realtimeMessage = {
          id: env.id,
          client_id: env.client_id || env.extra?.client_id || env.id,
          conv_id: env.conv,
          sender: fromAgent ? 'agent' : (fromSys ? 'sys' : 'visitor'),
          sender_ref: senderRef,
          content: env.content || '',
          media_url: env.media ? { String: env.media, Valid: true } : null,
          media_kind: env.mkind ? { String: env.mkind, Valid: true } : null,
          media_name: env.mname ? { String: env.mname, Valid: true } : null,
          created_at: new Date(env.ts || Date.now()).toISOString(),
          // 给前端渲染用：标记是否是页面跳转 + URL
          page_url: isPageNav ? env.extra.url : '',
          page_title: isPageNav ? env.extra.title : '',
          status: fromAgent ? 'persisted' : undefined,
        }
        // 按 ID 幂等合并，重放/Redis 回环/HTTP 对账不会生成重复气泡。
        mergeMessages(activeConv.value.visitor_id, [realtimeMessage], false)
        nextTick(scrollToBottom)
        saveCachedMsgs(activeConv.value?.visitor_id)  // [090] 按客户缓存（详情已按客户聚合）
        // 同步更新当前会话的 last_message 预览（保持左侧列表跟实时一致）
        if (activeConv.value && !isPageNav) {
          let preview = env.content || ''
          if (!preview && env.mkind === 'image') preview = '[图片]'
          else if (!preview && env.media) preview = '[文件]'
          activeConv.value.last_message = {
            sender: fromAgent ? 'agent' : (fromSys ? 'sys' : 'visitor'),
            content: preview,
            created_at: new Date(env.ts || Date.now()).toISOString(),
          }
          activeConv.value.updated_at = new Date(env.ts || Date.now()).toISOString()
        }
        // 访客发到当前会话：发 WSS read 通知访客「客服已读」+ 播声
        if (fromVisitor) {
          activeConv.value.has_visitor_msg = true   // [065] 当前会话也实时翻成「已联系」
          sendReadAck(env.conv, env.id)
          playSound(agentSound.value)
        }
        // [071] 访客来电（voice 通话事件）也实时翻「已联系」——爷爷：有来电就算（含秒挂）
        if (fromSys && env.extra?.kind === 'voice_finished') {
          activeConv.value.has_visitor_msg = true
        }
        return
      }

      // 非当前会话的新消息：本地实时更新（WSS，0 延迟）
      const conv = convs.value.find(x => x.id === env.conv)
      // [072] 页面访问（浏览动作）不更新列表时间/不上浮，只在聊天记录显示
      const isPageNav = fromSys && env.extra?.kind === 'page_navigation'
      if (conv) {
        // 只有访客的消息才 +1 未读 + 播声；其他客服/sys 发的只更新活动时间 + 上浮
        if (fromVisitor) {
          conv.unread = (conv.unread || 0) + 1
          conv.has_visitor_msg = true   // [065] 实时把会话从「未联系」翻成「已联系」
          playSound(agentSound.value)
        }
        // [071] 访客来电也实时翻「已联系」（不计未读、不外加提示音）
        if (fromSys && env.extra?.kind === 'voice_finished') {
          conv.has_visitor_msg = true
        }
        // [072] page_navigation 不动时间/预览/排序——浏览动作不该顶起会话
        if (!isPageNav) {
          conv.updated_at = new Date(env.ts || Date.now()).toISOString()
          // 实时更新最后一条消息预览（WSS 本地维护，跟服务端 last_message 字段一致）
          let preview = env.content || ''
          if (!preview && env.mkind === 'image') preview = '[图片]'
          else if (!preview && env.media) preview = '[文件]'
          conv.last_message = {
            sender: fromAgent ? 'agent' : (fromSys ? 'sys' : 'visitor'),
            content: preview,
            created_at: new Date(env.ts || Date.now()).toISOString(),
          }
          const idx = convs.value.indexOf(conv)
          if (idx > 0) {
            convs.value.splice(idx, 1)
            convs.value.unshift(conv)
          }
        }
      } else if (fromVisitor || (fromSys && !isPageNav)) {
        // 全新访客（或新会话的系统问候/来电）—— 会话还没在客服当前列表里，触发一次防抖刷新
        // [072] 纯页面访问不触发刷新：避免把「只浏览没联系」的会话拉进列表上浮
        scheduleConvsRefresh()
        if (fromVisitor) playSound(agentSound.value)
      }
    }
  })
  ws.start()
  // health 不再定时轮询（数据变化主要由 WSS 推送驱动；统计冷数据每 5 分钟刷一次就够）
  // conv 列表低频兜底，主要靠 WSS 触发刷新
  convsTimer = setInterval(() => { refreshConvs(); refreshStats() }, 5 * 60 * 1000)
})

onUnmounted(() => {
  ws?.stop()
  statusSync?.stop()
  statusSync = null
  for (const timer of Object.values(ackTimers)) clearTimeout(timer)
  clearInterval(convsTimer)
  if (convsDebounce) clearTimeout(convsDebounce)
  if (voice.timer) { clearTimeout(voice.timer); clearInterval(voice.timer) }
  if (voice.pc) { try { voice.pc.close() } catch {} }
  if (voice.localStream) voice.localStream.getTracks().forEach(t => t.stop())
  // [113] 新功能计时器回收
  visitorTyping.dispose()
  if (recallTicker) clearInterval(recallTicker)
  for (const timer of profileRefreshTimers.values()) clearTimeout(timer)
  profileRefreshTimers.clear()
})

// ============================================================
// [113] 0.8.0 新功能接线：标签筛选 / 多选批量 / 置顶未读 / 拉黑 / 删除导出 /
//   客户资料 / 快捷回复 / 正在输入 / 消息撤回 / 全局搜索 / 实时事件。
//   纯逻辑都在 modules/（有 node 测试），这里只做状态接线，不改动既有消息管线。
// ============================================================
const toolbarRef = ref(null)
const selectedVisitors = ref({})     // { [visitorId]: true }
const selectedIds = computed(() => convs.value.map(c => c.visitor_id).filter(v => selectedVisitors.value[v] === true))
const batchBusy = ref(false)
const opBusy = new Set()             // 单客户操作防连点（命令:访客）
const profileVisible = ref(false)
const profileTarget = ref(null)
const profileDrawerRef = ref(null)
const blockVisible = ref(false)
const blockTarget = ref(null)
const searchVisible = ref(false)
const draftInputRef = ref(null)
const myAgentId = computed(() => session.agent?.id)

// ---------- 多选 ----------
function toggleSelect(c, checked) {
  if (!isValidVisitorId(c?.visitor_id)) return
  if (checked) selectedVisitors.value[c.visitor_id] = true
  else delete selectedVisitors.value[c.visitor_id]
}
function toggleSelectAll(checked) {
  const next = {}
  if (checked) for (const c of convs.value) if (isValidVisitorId(c.visitor_id)) next[c.visitor_id] = true
  selectedVisitors.value = next
}
function clearSelection() { selectedVisitors.value = {} }
// 列表重载后只保留仍可见的客户：批量操作绝不作用于用户看不到的条目
function pruneSelection() {
  const present = new Set(convs.value.map(c => c.visitor_id))
  const next = {}
  for (const vid of Object.keys(selectedVisitors.value)) if (present.has(vid)) next[vid] = true
  selectedVisitors.value = next
}

// ---------- 本地缓存清理（删除后不在本机残留已删除的聊天内容） ----------
function purgeVisitorCaches(visitorIds) {
  const drop = new Set(visitorIds)
  for (const vid of drop) {
    delete msgCache[vid]
    messageLoadVersion[vid] = (messageLoadVersion[vid] || 0) + 1   // 作废在途加载，防止旧响应写回缓存
    try { localStorage.removeItem(MSG_PREFIX + vid) } catch {}
  }
  try {
    const idx = JSON.parse(localStorage.getItem(MSG_INDEX) || '[]')
    if (Array.isArray(idx)) localStorage.setItem(MSG_INDEX, JSON.stringify(idx.filter(v => !drop.has(v))))
  } catch {}
}
function purgeAllVisitorCaches() {
  for (const vid of Object.keys(msgCache)) {
    delete msgCache[vid]
    messageLoadVersion[vid] = (messageLoadVersion[vid] || 0) + 1
  }
  try {
    const keys = []
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && k.startsWith(MSG_PREFIX)) keys.push(k)
    }
    for (const k of keys) localStorage.removeItem(k)
  } catch {}
}

// 从列表移除客户（删除 / 拉黑 / 他端删除事件共用）；purgeCache=true 同时清本地聊天缓存与草稿附件
function removeVisitorsLocally(visitorIds, { purgeCache = false, closeProfile = true } = {}) {
  const ids = normalizeVisitorIds(visitorIds)
  if (!ids.length) return 0
  const drop = new Set(ids)
  const { list, removed, removedItems } = removeVisitorsFromList(convs.value, ids)
  if (removed) convs.value = list
  for (const vid of ids) {
    delete selectedVisitors.value[vid]
    visitorTyping.clear(vid)
  }
  if (activeConv.value && drop.has(activeConv.value.visitor_id)) {
    activeConv.value = null
    messages.value = []
  }
  if (closeProfile && profileVisible.value && drop.has(profileTarget.value?.visitor_id)) profileVisible.value = false
  if (purgeCache) {
    for (const c of removedItems) {
      clearPendingFor(c.id)
      delete drafts.value[c.id]
    }
    purgeVisitorCaches(ids)
  }
  if (removed) scheduleConvsRefresh()   // 3 秒防抖后按服务端对账总数与分页
  return removed
}

function resetAfterDeleteAll() {
  for (const c of convs.value) clearPendingFor(c.id)
  drafts.value = {}
  convs.value = []
  activeConv.value = null
  messages.value = []
  clearSelection()
  profileVisible.value = false
  visitorTyping.dispose()
  typingState.value = {}
  purgeAllVisitorCaches()
  loadConvs(true)
  refreshSideTotals()
  scheduleConvsRefresh()   // 兜底：若此刻有在途列表请求，3 秒后再对账一次
}

function onConversationsDeleted(extra) {
  if (extra && extra.all === true) {
    resetAfterDeleteAll()
    return
  }
  const ids = Array.isArray(extra?.visitor_ids) ? extra.visitor_ids.slice(0, 1000) : []
  removeVisitorsLocally(ids, { purgeCache: true })
}

// ---------- profile_updated：按客户合并 300ms 内的重复事件；突发过多时直接整页对账 ----------
const PROFILE_REFRESH_DEBOUNCE_MS = 300
const PROFILE_REFRESH_BURST_LIMIT = 20
const profileRefreshTimers = new Map()
function queueProfileRefresh(vid) {
  if (!isValidVisitorId(vid) || profileRefreshTimers.has(vid)) return
  if (profileRefreshTimers.size >= PROFILE_REFRESH_BURST_LIMIT) {
    scheduleConvsRefresh()
    return
  }
  profileRefreshTimers.set(vid, setTimeout(() => {
    profileRefreshTimers.delete(vid)
    refreshVisitorProfile(vid)
  }, PROFILE_REFRESH_DEBOUNCE_MS))
}
async function refreshVisitorProfile(vid) {
  if (profileVisible.value && profileTarget.value?.visitor_id === vid) profileDrawerRef.value?.reload()
  const conv = convs.value.find(c => c.visitor_id === vid)
  if (!conv) {
    scheduleConvsRefresh()   // 可能是解除拉黑 / 恢复的客户，重新拉列表
    return
  }
  let profile = null
  try {
    const r = await getVisitorProfile(vid, { silent: true })
    profile = r?.data
  } catch { return }
  if (!profile) return
  if (profile.blocked === true) {
    removeVisitorsLocally([vid], { closeProfile: false })
    return
  }
  const pinnedBefore = conv.pinned === true
  applyProfileToConversation(conv, profile)
  if (pinnedBefore !== (conv.pinned === true) || tagFilter.value) scheduleConvsRefresh()
}

// ---------- 单客户操作菜单 ----------
async function onConvCommand(cmd, c) {
  if (!c || !isValidVisitorId(c.visitor_id)) return
  if (cmd === 'profile') { openProfile(c); return }
  if (cmd === 'block') {
    blockTarget.value = { visitor_id: c.visitor_id, identifier: c.identifier || '' }
    blockVisible.value = true
    return
  }
  if (cmd === 'delete') { await confirmAndDelete([c.visitor_id]); return }
  const key = cmd + ':' + c.visitor_id
  if (opBusy.has(key)) return
  opBusy.add(key)
  try {
    if (cmd === 'pin' || cmd === 'unpin') {
      const pinned = cmd === 'pin'
      await setVisitorPinned(c.visitor_id, pinned)
      c.pinned = pinned
      if (pinned) moveToTop(convs.value, c)
      scheduleConvsRefresh()   // 最终以服务端排序为准（置顶按置顶时间倒序）
      ElMessage.success(pinned ? '已置顶' : '已取消置顶')
    } else if (cmd === 'mark-unread') {
      await setVisitorMarkedUnread(c.visitor_id, true)
      c.marked_unread = true
      if (!(Number(c.unread) > 0)) c.unread = 1
    } else if (cmd === 'mark-read') {
      await setVisitorMarkedUnread(c.visitor_id, false)
      c.marked_unread = false
      c.unread = 0
      markConversationRead(c.id).catch(() => {})
    } else if (cmd === 'export') {
      const r = await exportVisitor(c.visitor_id, 'xlsx')
      ElMessage.success('已导出：' + r.filename)
    }
  } catch {
    // 错误提示由 http 拦截器 / 下载模块统一弹出
  } finally {
    opBusy.delete(key)
  }
}

async function confirmAndDelete(visitorIds) {
  const check = validateBatchSelection(visitorIds, BATCH_DELETE_MAX)
  if (!check.ok) { ElMessage.warning(check.error); return }
  const n = check.ids.length
  try {
    await ElMessageBox.confirm(
      `确定删除${n > 1 ? '选中的 ' + n + ' 位客户' : '该客户'}的全部聊天记录（含图片文件）？删除后进入回收站，保留 7 天后自动彻底删除；访客端聊天窗口会同时清空。`,
      '删除聊天记录',
      { type: 'warning', confirmButtonText: '确认删除', cancelButtonText: '取消' },
    )
  } catch { return }
  if (batchBusy.value) return
  batchBusy.value = true
  try {
    const r = await deleteVisitors(check.ids)
    const data = r?.data || {}
    ElMessage.success({ message: summarizeDeleteResult(data), duration: 5000 })
    removeVisitorsLocally(check.ids, { purgeCache: true })
    // 有无权访问而被跳过的客户：以服务端列表为准重新对账，让它们恢复显示
    if (Number(data.skipped) > 0) loadConvs(true)
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    batchBusy.value = false
  }
}
function batchDeleteSelected() { confirmAndDelete(selectedIds.value) }
async function batchExportSelected() {
  const check = validateBatchSelection(selectedIds.value, BATCH_EXPORT_MAX)
  if (!check.ok) { ElMessage.warning(check.error); return }
  if (batchBusy.value) return
  batchBusy.value = true
  try {
    const r = await exportVisitors(check.ids, 'xlsx')
    ElMessage.success(`已导出 ${check.ids.length} 位客户的聊天记录：${r.filename}`)
  } catch {
    // 错误提示由 http 拦截器 / 下载模块统一弹出
  } finally {
    batchBusy.value = false
  }
}

// ---------- 客户资料 / 拉黑 ----------
function openProfile(c) {
  if (!c || !isValidVisitorId(c.visitor_id)) return
  profileTarget.value = { visitor_id: c.visitor_id, identifier: c.identifier || '' }
  profileVisible.value = true
}
function onProfileSaved(profile) {
  const vid = profile?.visitor_id
  const conv = convs.value.find(x => x.visitor_id === vid)
  if (conv) applyProfileToConversation(conv, profile)
  if (activeConv.value?.visitor_id === vid && activeConv.value !== conv) applyProfileToConversation(activeConv.value, profile)
  toolbarRef.value?.reloadTags()
  if (tagFilter.value) scheduleConvsRefresh()
}
function onBlockedChange(payload) {
  const vid = payload?.visitor_id
  if (!isValidVisitorId(vid)) return
  // 拉黑客户不进会话列表（契约第 6 节）；记录保留可查，因此不清本地缓存
  if (payload.blocked === true) removeVisitorsLocally([vid], { closeProfile: false })
  else scheduleConvsRefresh()
}

// ---------- 快捷回复：点击填入输入框，可直接回车发送 ----------
function onQuickReplyPick(content) {
  if (!activeConv.value || activeConv.value._readonly) return
  currentDraft.value = insertQuickReply(currentDraft.value, content)
  onDraftInput(currentDraft.value)
  nextTick(() => draftInputRef.value?.focus())
}

// ---------- 对方正在输入（契约第 10 节） ----------
const typingState = ref({})          // { [visitorId]: true }
const visitorTyping = new TypingIndicator({
  timeoutMs: TYPING_DISPLAY_TIMEOUT_MS,
  onChange: (vid, on) => {
    if (on) typingState.value[vid] = true
    else delete typingState.value[vid]
  },
})
const agentTypingThrottle = new TypingThrottle({ intervalMs: TYPING_SEND_INTERVAL_MS })
const activeTyping = computed(() => !!(activeConv.value && typingState.value[activeConv.value.visitor_id]))
function onDraftInput(value) {
  const conv = activeConv.value
  if (!conv || conv._readonly || !conv.id || !ws?.alive) return
  if (!String(value ?? '').trim()) return
  if (!agentTypingThrottle.tryAcquire(conv.id)) return
  // 发送失败（连接刚断开）时释放节流，恢复后可立即再发
  if (!ws.send({ type: 'typing', conv: conv.id, ts: Date.now() })) agentTypingThrottle.reset(conv.id)
}
function onTypingEvent(env) {
  const who = parseParticipant(env.from)
  if (who.role !== 'visitor') return   // 只展示访客的输入状态；其他客服的 typing 忽略
  visitorTyping.mark(who.id)
}
function clearVisitorTyping(from) {
  const who = parseParticipant(from)
  if (who.role === 'visitor') visitorTyping.clear(who.id)
}

// ---------- 消息撤回（契约第 9 节，仅自己 2 分钟内的消息） ----------
const recallNow = ref(Date.now())
let recallTicker = null
onMounted(() => {
  recallTicker = setInterval(() => {
    // 只有当前会话里有消息的“可撤回”状态发生变化时才推进时间，避免整页每 5 秒无谓重渲染
    const now = Date.now()
    const me = myAgentId.value
    const prev = recallNow.value
    if (messages.value.some(m => canRecallMessage(m, me, now) !== canRecallMessage(m, me, prev))) recallNow.value = now
  }, 5000)
})
const recallingIds = new Set()
function canRecall(m) { return canRecallMessage(m, myAgentId.value, recallNow.value) }
function recallText(m) { return recallPlaceholder(m, myAgentId.value) }
function applyRecallLocally(messageId) {
  for (const vid of Object.keys(msgCache)) {
    // 当前会话通过响应式数组修改，界面立即刷新；其他客户直接改缓存
    const list = activeConv.value?.visitor_id === vid ? messages.value : msgCache[vid]
    const r = applyRecallToList(list, messageId)
    if (r.matched) {
      saveCachedMsgs(vid)
      return true
    }
  }
  return false
}
async function recallMine(m) {
  if (!canRecallMessage(m, myAgentId.value, Date.now()) || recallingIds.has(m.id)) return
  try {
    await ElMessageBox.confirm('撤回后访客端将显示「客服撤回了一条消息」，确定撤回？', '撤回消息', {
      type: 'warning', confirmButtonText: '撤回', cancelButtonText: '取消',
    })
  } catch { return }
  if (!canRecallMessage(m, myAgentId.value, Date.now())) {
    ElMessage.warning('已超过 2 分钟，无法撤回')
    return
  }
  recallingIds.add(m.id)
  try {
    await recallMessage(m.id)
    applyRecallLocally(m.id)
  } catch (e) {
    if (recallFailureMeansAlreadyRecalled(e)) applyRecallLocally(m.id)   // 40911 已撤回：同步本地状态
  } finally {
    recallingIds.delete(m.id)
  }
}
function onBubbleContextMenu(e, m) {
  if (!canRecall(m)) return
  e.preventDefault()
  recallMine(m)
}

// ---------- [119] 从多人大厅「私聊」跳转：URL 只带一次性票据，vid 从同源 localStorage 读取后立即删除 ----------
function openFromHallJump() {
  const nonce = typeof route.query.open === 'string' ? route.query.open : ''
  if (!nonce) return
  router.replace({ path: route.path, query: {} }).catch(() => {})
  const vid = consumeJumpTicket(nonce)
  if (!vid) {
    ElMessage.warning('私聊跳转已失效，请回到大厅重新点击「私聊」')
    return
  }
  openFromSearch({ visitor_id: vid })
}

// ---------- 全局搜索：点击结果打开并定位 ----------
async function openFromSearch(hit) {
  if (!hit || !isValidVisitorId(hit.visitor_id)) return
  const conv = convs.value.find(c => c.visitor_id === hit.visitor_id)
  try {
    if (conv) {
      await pickConv(conv)
    } else {
      // 不在当前列表（已结束 / 未加载到 / 筛选外）：只读打开历史记录，不允许向旧会话发送
      activeConv.value = {
        id: typeof hit.conv_id === 'string' ? hit.conv_id : '',
        visitor_id: hit.visitor_id,
        identifier: typeof hit.identifier === 'string' ? hit.identifier : '',
        unread: 0,
        _readonly: true,
      }
      messages.value = []
      await loadMessages(hit.visitor_id)
    }
  } catch {
    // 错误提示由 http 拦截器统一弹出
  }
  await nextTick()
  locateMessage(hit.message_id)
}
function locateMessage(messageId) {
  // ID 先按白名单校验再拼选择器，杜绝选择器注入
  if (typeof messageId !== 'string' || !/^[A-Za-z0-9-]{1,36}$/.test(messageId)) return
  const el = document.querySelector('#msg-list [data-msg-id="' + messageId + '"]')
  if (el) el.scrollIntoView({ block: 'center' })
  else ElMessage.info('会话已打开；该消息较早，未在最近记录中显示')
}

// ---------- 新实时事件分发 ----------
function handleFeatureEvent(env) {
  switch (env.type) {
    case 'history_cleared':          // 访客端事件，客服端忽略
      return true
    case 'conversations_deleted':
      onConversationsDeleted(env.extra)
      return true
    case 'profile_updated':
      queueProfileRefresh(env.extra?.visitor_id)
      return true
    case 'recall':
      if (typeof env.id === 'string') applyRecallLocally(env.id)
      return true
    case 'typing':
      onTypingEvent(env)
      return true
    default:
      return false
  }
}

// ====== 语音通话（接听端） ======
// 默认仅 STUN 兜底；每次 voiceAccept 都会刷新 fetchTurnCredential
let ICE_SERVERS = [{ urls: 'stun:stun.l.google.com:19302' }]

// 拉后端 24h 短期 TURN 凭证（与 widget 同源 service.GenerateTurnCredential）
// 失败保持原 ICE_SERVERS 兜底，不阻塞通话流程
async function fetchTurnCredential() {
  try {
    const r = await http.get('/agent/turn-credential')
    if (r?.code === 0 && r?.data && Array.isArray(r.data.urls) && r.data.urls.length) {
      const srv = { urls: r.data.urls }
      if (r.data.username) srv.username = r.data.username
      if (r.data.credential) srv.credential = r.data.credential
      ICE_SERVERS = [srv]
    }
  } catch { /* 静默：保持兜底 STUN */ }
}
const voiceState = ref('idle')   // idle / incoming / accepting / talking / ended
const voiceStatusText = ref('')
const voiceCallerLabel = ref('')
const voiceRemoteAudioRef = ref(null)
const voice = {
  callId: null,
  callerFrom: null,  // "visitor:vid"
  pc: null,
  localStream: null,
  startTs: 0,
  timer: null,
}

function handleVoiceSignal(env) {
  switch (env.type) {
    case 'voice_call':   return voiceOnIncoming(env)
    case 'voice_taken':  return voiceOnTaken(env)
    case 'voice_offer':  return voiceOnOffer(env)
    case 'voice_ice':    return voiceOnIce(env)
    case 'voice_end':    return voiceOnRemoteEnd(env)
    // accept/reject/answer 是接听端主动发出的，不会回到自己（除非多客服环境同账号其它端，可忽略）
  }
}

function voiceOnIncoming(env) {
  if (voiceState.value !== 'idle' && voiceState.value !== 'ended') return  // 忙线
  voice.callId = env.extra?.call_id
  voice.callerFrom = env.from   // "visitor:vid"
  const vid = (env.from || '').split(':')[1] || ''
  // 在会话列表里找名字
  const conv = convs.value.find(c => c.visitor_id === vid)
  voiceCallerLabel.value = conv?.identifier || '访客 ' + vid.slice(0, 6)
  voiceState.value = 'incoming'
  voiceStatusText.value = '语音来电…'
  // [036] 来电铃声循环播放，accept/reject/voiceEnd 都会进 voiceCleanup → stopRingLoop
  playRingLoop()
  // 30 秒未接听自动错过
  if (voice.timer) clearTimeout(voice.timer)
  voice.timer = setTimeout(() => {
    if (voiceState.value === 'incoming') voiceEnd('未接听', false)
  }, 30000)
}

function voiceOnTaken(env) {
  if (voiceState.value !== 'incoming') return
  if (env.extra?.call_id !== voice.callId) return
  // 别的客服接了，撤销我的来电浮窗
  voiceCleanup()
  voiceState.value = 'idle'
}

async function voiceAccept() {
  if (voiceState.value !== 'incoming') return
  if (voice.timer) { clearTimeout(voice.timer); voice.timer = null }
  // [036] 接听后停铃声；voiceCleanup 不会在 accept 路径触发（state 走 accepting→talking 直到挂断）
  stopRingLoop()
  // 接听前刷一次 TURN 凭证（~50ms）；失败不阻塞，会用默认 STUN
  await fetchTurnCredential()
  try {
    voice.localStream = await navigator.mediaDevices.getUserMedia({ audio: true })
  } catch (e) {
    ElMessage.error('麦克风访问失败：' + (e.message || e))
    voiceEnd('麦克风失败', false)
    return
  }
  voiceState.value = 'accepting'
  voiceStatusText.value = '已接听，等待对方建立通话…'
  // 通知访客接听 + 广播给其他 agent 撤窗
  ws?.send({
    type: 'voice_accept', to: voice.callerFrom, ts: Date.now(),
    extra: { call_id: voice.callId, agent_id: String(session.agent?.id), agent_name: session.agent?.nickname || '' }
  })
  ws?.send({
    type: 'voice_taken', ts: Date.now(),
    extra: { call_id: voice.callId }
  })
}

function voiceReject() {
  if (voiceState.value !== 'incoming') return
  ws?.send({
    type: 'voice_reject', to: voice.callerFrom, ts: Date.now(),
    extra: { call_id: voice.callId, code: 'rejected', duration: 0 }
  })
  voiceEnd('已拒绝', false)
}

function createPCAgent() {
  const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS })
  pc.onicecandidate = (ev) => {
    if (ev.candidate && voice.callerFrom) {
      ws?.send({
        type: 'voice_ice', to: voice.callerFrom, ts: Date.now(),
        extra: {
          call_id: voice.callId,
          candidate: ev.candidate.candidate,
          sdpMid: ev.candidate.sdpMid,
          sdpMLineIndex: ev.candidate.sdpMLineIndex
        }
      })
    }
  }
  pc.ontrack = (ev) => {
    if (voiceRemoteAudioRef.value && ev.streams[0]) {
      voiceRemoteAudioRef.value.srcObject = ev.streams[0]
    }
  }
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === 'failed' || pc.connectionState === 'disconnected') {
      voiceEnd('连接中断', true)
    }
  }
  return pc
}

async function voiceOnOffer(env) {
  if (voiceState.value !== 'accepting') return
  if (env.extra?.call_id !== voice.callId) return
  voice.pc = createPCAgent()
  voice.localStream.getTracks().forEach(t => voice.pc.addTrack(t, voice.localStream))
  await voice.pc.setRemoteDescription({ type: 'offer', sdp: env.extra.sdp })
  const answer = await voice.pc.createAnswer()
  await voice.pc.setLocalDescription(answer)
  ws?.send({
    type: 'voice_answer', to: voice.callerFrom, ts: Date.now(),
    extra: { call_id: voice.callId, sdp: answer.sdp }
  })
  voiceState.value = 'talking'
  voice.startTs = Date.now()
  voice.timer = setInterval(() => {
    if (voiceState.value !== 'talking') return
    const sec = Math.floor((Date.now() - voice.startTs) / 1000)
    const mm = String(Math.floor(sec / 60)).padStart(2, '0')
    const ss = String(sec % 60).padStart(2, '0')
    voiceStatusText.value = '通话中 ' + mm + ':' + ss
  }, 1000)
}

async function voiceOnIce(env) {
  if (!voice.pc || !env.extra) return
  try {
    await voice.pc.addIceCandidate({
      candidate: env.extra.candidate,
      sdpMid: env.extra.sdpMid,
      sdpMLineIndex: env.extra.sdpMLineIndex
    })
  } catch {}
}

function voiceOnRemoteEnd(env) {
  if (voiceState.value === 'idle') return
  voiceEnd('对方已挂断', true)
}

function voiceEnd(reason, notifyPeer) {
  if (voiceState.value === 'idle') return
  if (notifyPeer && voice.callerFrom && voice.callId) {
    // 根据 state 决定 code + duration（跟 widget 端逻辑一致）
    let code = 'hangup'
    let duration = 0
    if (voiceState.value === 'incoming' || voiceState.value === 'accepting') {
      code = 'cancel'
    } else if (voiceState.value === 'talking' && voice.startTs) {
      code = 'hangup'
      duration = Math.floor((Date.now() - voice.startTs) / 1000)
    }
    if (reason === '连接中断') code = 'failed'
    ws?.send({
      type: 'voice_end', to: voice.callerFrom, ts: Date.now(),
      extra: { call_id: voice.callId, code: code, duration: duration }
    })
  }
  voiceCleanup()
  voiceState.value = 'ended'
  voiceStatusText.value = reason
  setTimeout(() => {
    if (voiceState.value === 'ended') voiceState.value = 'idle'
  }, 2500)
}

function voiceCleanup() {
  // [036] 统一停来电铃声（任何挂断 / 接听 / 被别的客服抢接 路径都会进这里）
  stopRingLoop()
  if (voice.pc) { try { voice.pc.close() } catch {} voice.pc = null }
  if (voice.localStream) {
    voice.localStream.getTracks().forEach(t => t.stop())
    voice.localStream = null
  }
  if (voice.timer) { clearTimeout(voice.timer); clearInterval(voice.timer); voice.timer = null }
  if (voiceRemoteAudioRef.value) voiceRemoteAudioRef.value.srcObject = null
  voice.callId = null
  voice.callerFrom = null
}
</script>

<template>
  <el-container class="console-root">
    <!-- 左：会话列表 -->
    <el-aside width="360px" class="aside">
      <div class="aside-header">
        <el-row :gutter="8">
          <el-col :span="12">
            <el-statistic title="在线访客" :value="onlineStats.visitors" />
          </el-col>
          <el-col :span="12">
            <el-statistic title="在线客服" :value="onlineStats.agents" />
          </el-col>
        </el-row>
        <!-- [065] 会话过滤 tab：全部 / 仅主动联系过的访客 -->
        <div class="aside-filter" style="margin-top:8px">
          <el-space direction="vertical" fill :size="8">
            <el-radio-group v-model="filterMode" size="small" style="width:100%">
              <el-radio-button value="all" style="width:50%;text-align:center">
                全部 ({{ allTotal }})
              </el-radio-button>
              <el-radio-button value="contacted" style="width:50%;text-align:center">
                已联系 ({{ contactedTotal }})
              </el-radio-button>
            </el-radio-group>
            <!-- [113] 标签筛选 + 多选批量删除/导出 + 全局搜索入口 -->
            <ConversationToolbar
              ref="toolbarRef"
              v-model:tag="tagFilter"
              :selected-count="selectedIds.length"
              :loaded-count="convs.length"
              :busy="batchBusy"
              @toggle-all="toggleSelectAll"
              @batch-delete="batchDeleteSelected"
              @batch-export="batchExportSelected"
              @open-search="searchVisible = true" />
          </el-space>
        </div>
      </div>
      <el-divider style="margin:0" />
      <el-scrollbar class="conv-scroll" ref="convScrollRef" @scroll="onConvScroll">
        <el-empty
          v-if="!filteredConvs.length && !convLoading"
          :description="tagFilter ? '该标签下暂无客户' : (filterMode==='contacted' ? '暂无主动联系过的客户' : '暂无访客')"
          :image-size="80" />
        <template v-else>
          <div
            v-for="c in filteredConvs"
            :key="c.id"
            class="conv-item"
            :class="{ 'conv-item--active': activeConv?.id === c.id }"
            @click="pickConv(c)">
            <!-- [113] 多选复选框：阻止冒泡，勾选不切换会话 -->
            <span @click.stop>
              <el-checkbox :model-value="selectedVisitors[c.visitor_id] === true" @change="(v) => toggleSelect(c, v === true)" />
            </span>
            <el-avatar :size="40" :style="{ background: visitorAvatarColor(c.visitor_id), color:'#fff', fontSize:'14px', fontWeight:600 }">
              {{ avatarChar(c.identifier || c.visitor_id) }}
            </el-avatar>
            <div class="conv-body">
              <div class="conv-row1">
                <span class="conv-name">{{ c.identifier || '访客 ' + c.visitor_id.slice(0, 6) }}</span>
                <span class="conv-time">{{ fmtGroupTime(c.updated_at) }}</span>
              </div>
              <div class="conv-row2">
                <span class="conv-preview">{{ lastMsgPreview(c) }}</span>
                <el-badge v-if="c.unread > 0" :value="c.unread" :max="99" />
              </div>
              <!-- [113] 置顶 / 手动未读 / 标签 / 备注摘要（纯文本渲染） -->
              <ConversationMeta :conv="c" />
              <!-- [059] 第 3 行：访客地理位置 + IP（任一非空才显示，省垂直空间）-->
              <div v-if="convGeoIp(c)" class="conv-row3">
                <span class="conv-geoip">{{ convGeoIp(c) }}</span>
              </div>
            </div>
            <!-- [113] 单客户操作菜单 -->
            <ConversationActions :conv="c" @command="(cmd) => onConvCommand(cmd, c)" />
          </div>
          <div v-if="convLoading" style="text-align:center;padding:10px;color:#909399;font-size:12px">加载中…</div>
          <div v-else-if="!convHasMore && filteredConvs.length" style="text-align:center;padding:10px;color:#c0c4cc;font-size:12px">— 没有更多了 —</div>
        </template>
      </el-scrollbar>
    </el-aside>

    <!-- 右：聊天区 -->
    <el-container>
      <el-header v-if="activeConv" class="chat-header">
        <div class="chat-header-left">
          <el-avatar :size="36" :style="{ background: visitorAvatarColor(activeConv.visitor_id), color:'#fff', fontSize:'13px', fontWeight:600 }">
            {{ avatarChar(activeConv.identifier || activeConv.visitor_id) }}
          </el-avatar>
          <div class="chat-header-info">
            <div class="chat-header-name">{{ activeConv.identifier || '访客 ' + activeConv.visitor_id.slice(0, 6) }}</div>
            <div class="chat-header-sub">
              <el-tag size="small" effect="plain" type="info" v-if="activeConv.country || activeConv.city">
                {{ [activeConv.country, activeConv.city].filter(Boolean).join(' · ') }}
              </el-tag>
              <el-tag size="small" effect="plain" v-if="activeConv.referer">来源：{{ activeConv.referer }}</el-tag>
              <el-tag size="small" effect="plain" v-if="activeConv.last_page">当前页：{{ activeConv.last_page }}</el-tag>
              <!-- [055] 关联访客按钮：点开 dialog 看同 IP 30 天内其他 vid -->
              <el-button link type="primary" size="small" @click="openRelatedDialog(activeConv.visitor_id)">
                关联访客 <span v-if="relatedCount[activeConv.visitor_id] != null">({{ relatedCount[activeConv.visitor_id] }})</span>
              </el-button>
              <!-- [113] 客户资料：备注 / 标签 / 拉黑 -->
              <el-button link type="primary" size="small" @click="openProfile(activeConv)">客户资料</el-button>
              <el-tag v-if="activeConv._readonly" size="small" type="info">只读（历史记录）</el-tag>
              <el-tag v-else-if="activeConv._sendBlocked" size="small" type="warning">只能查看</el-tag>
            </div>
          </div>
        </div>
      </el-header>

      <!-- [055] 关联访客 dialog：同 IP 30 天内出现的其他 vid 列表 -->
      <el-dialog v-model="relatedDialog" title="关联访客（同 IP 30 天内出现）" width="640">
        <div v-if="relatedLoading" style="text-align:center;padding:24px;color:#909399">加载中…</div>
        <el-empty v-else-if="relatedList.length === 0" description="没有同 IP 历史访客" :image-size="80" />
        <el-table v-else :data="relatedList" size="small" max-height="420">
          <el-table-column prop="vid" label="访客 ID" width="180">
            <template #default="{ row }">
              <el-text size="small" truncated style="font-family:monospace">{{ row.vid }}</el-text>
            </template>
          </el-table-column>
          <el-table-column prop="identifier" label="身份" width="160">
            <template #default="{ row }">
              {{ row.identifier || '—' }}
            </template>
          </el-table-column>
          <el-table-column prop="ip" label="IP" width="130">
            <template #default="{ row }">
              <el-text size="small" style="font-family:monospace">{{ row.ip || '—' }}</el-text>
            </template>
          </el-table-column>
          <el-table-column label="最近活动">
            <template #default="{ row }">
              {{ formatBeijing(row.last_seen, { seconds: false, empty: '' }) }}
            </template>
          </el-table-column>
        </el-table>
        <template #footer>
          <span style="font-size:12px;color:#909399">
            提示：列表仅供参考"疑似同一人"。vid 仍按浏览器维度独立。
          </span>
        </template>
      </el-dialog>

      <el-main id="msg-list" class="msg-main">
        <el-empty v-if="!activeConv" description="请从左侧选择一个会话开始服务" :image-size="120" />
        <template v-else>
          <div v-for="(g, gi) in grouped" :key="gi" class="msg-group">
            <div class="time-divider">
              <span>{{ fmtGroupTime(g.ts) }}</span>
            </div>
            <!-- 页面跳转：横幅样式（参考 Crisp） -->
            <template v-if="isPageGroup(g)">
              <div v-for="m in g.items" :key="m.id" class="page-banner">
                <span class="page-banner-arrow">→</span>
                <span class="page-banner-label">访客访问了</span>
                <a v-if="pageURL(m)" :href="pageURL(m)" target="_blank" rel="noopener noreferrer" class="page-banner-link"
                   :title="pageURL(m)">
                  {{ pageTitle(m) || pageURL(m) }}
                </a>
              </div>
            </template>
            <!-- 普通消息组 -->
            <div v-else class="msg-line" :class="{ 'msg-line--mine': isMineGroup(g) }">
              <el-avatar
                :size="36"
                :style="{
                  background: isMineGroup(g) ? '#409EFF' : (g.sender === 'visitor' ? visitorAvatarColor(g.sender_ref) : '#909399'),
                  color:'#fff', fontSize:'13px', fontWeight:600, flexShrink:0
                }">
                {{ avatarChar(senderName(g)) }}
              </el-avatar>
              <div class="msg-stack">
                <div class="msg-stack-name">{{ senderName(g) }}</div>
                <template v-for="m in g.items" :key="m.id">
                  <!-- [113] 已撤回：只显示占位文字，不渲染任何正文/附件 -->
                  <el-text v-if="m.recalled === true" size="small" type="info" :data-msg-id="m.id">{{ recallText(m) }}</el-text>
                  <div
                    v-else
                    class="bubble"
                    :class="{ 'bubble--mine': isMineGroup(g) }"
                    :title="fmtAbs(m.created_at)"
                    :data-msg-id="m.id"
                    @contextmenu="onBubbleContextMenu($event, m)">
                    <template v-if="mediaURL(m)">
                      <el-image
                        v-if="mediaKind(m) === 'image'"
                        :src="mediaURL(m)"
                        :preview-src-list="[mediaURL(m)]"
                        :initial-index="0"
                        preview-teleported
                        hide-on-click-modal
                        fit="cover"
                        class="bubble-img"
                      />
                      <a v-else :href="mediaURL(m)" target="_blank" rel="noopener noreferrer" download class="bubble-file">
                        <el-icon><svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zm-1 7V3.5L18.5 9H13z"/></svg></el-icon>
                        <span>{{ mediaName(m) }}</span>
                      </a>
                    </template>
                    <span v-if="m.content" class="bubble-text">{{ m.content }}</span>
                    <span class="bubble-time" style="display:block;font-size:11px;opacity:0.55;margin-top:3px;text-align:right">{{ fmtMsgTime(m.created_at) }}</span>
                    <span
                      v-if="m.content || mediaURL(m)"
                      class="bubble-copy"
                      :class="{ 'bubble-copy--mine': isMineGroup(g) }"
                      title="复制"
                      @click.stop="copyMessage(m)">
                      <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                    </span>
                  </div>
                  <!-- [098] 每条消息保留自己的状态，后续消息不会覆盖前一条已读。 -->
                  <div v-if="isMineGroup(g) && m.recalled !== true" class="read-indicator">
                    <el-space :size="8">
                      <el-link
                        v-if="canonicalMessageStatus(m) === 'failed'"
                        type="danger"
                        :underline="false"
                        @click.stop="resendMsg(m)">
                        <el-icon><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 3 2 21h20L12 3Z"/><path d="M12 9v5"/><path d="M12 18h.01"/></svg></el-icon>
                        <span>{{ messageStatusLabel(m) }}</span>
                      </el-link>
                      <el-text v-else size="small" type="info">{{ messageStatusLabel(m) }}</el-text>
                      <!-- [113] 2 分钟内自己的消息可撤回（也可右键气泡） -->
                      <el-button v-if="canRecall(m)" link type="primary" size="small" @click.stop="recallMine(m)">撤回</el-button>
                    </el-space>
                  </div>
                </template>
              </div>
            </div>
          </div>
        </template>
      </el-main>

      <el-alert v-if="activeConv && !activeConv._readonly && activeConv._sendBlocked" type="warning" show-icon :closable="false" :title="activeConv._sendBlocked">
        <template #default>
          <el-button size="small" type="primary" link @click="retryAssign">重新检查（对方离线后可接管）</el-button>
        </template>
      </el-alert>
      <el-footer v-if="activeConv && !activeConv._readonly && !activeConv._sendBlocked" class="chat-footer">
        <!-- [040] pending 多文件预览队列：粘贴 / 选附件追加到这里，点发送才依次上传 -->
        <!-- [068] currentPendingFiles：仅显示当前活跃会话的 pending，切会话自动切换显示 -->
        <div v-if="currentPendingFiles.length" class="pending-list">
          <div v-for="(it, idx) in currentPendingFiles" :key="idx" class="pending-chip">
            <img v-if="it.isImage" :src="it.blobUrl" class="thumb" />
            <span v-else class="file-icon">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
                <polyline points="14 2 14 8 20 8"/>
              </svg>
            </span>
            <span class="meta">
              <span class="name">{{ it.file.name || (it.isImage ? '图片' : '文件') }}</span>
              <span class="size">{{ fmtBytes(it.file.size || 0) }}</span>
            </span>
            <!-- [068] 移除只针对当前 activeConv，convId 自动来自模板上下文 -->
            <button class="remove-btn" title="移除" @click="removePendingFile(it, activeConv?.id)">×</button>
          </div>
        </div>
        <!-- [113] 访客正在输入（5 秒无新 typing 或收到访客消息后自动隐藏） -->
        <el-text v-if="activeTyping" size="small" type="info">对方正在输入…</el-text>
        <!-- [068] v-model 绑定 currentDraft computed，自动 per-conv 隔离 -->
        <el-input
          ref="draftInputRef"
          v-model="currentDraft"
          type="textarea"
          :rows="3"
          resize="none"
          placeholder="回车发送，Shift+回车换行，粘贴可上传图片/文件（可多张）"
          @keydown.enter.exact.prevent="sendText"
          @input="onDraftInput"
          @paste.native="onPasteDraft" />
        <div class="chat-footer-actions">
          <div>
            <el-button :icon="undefined" plain size="small" @click="fileInput.click()">附件 / 图片</el-button>
            <!-- [113] 快捷回复：点击填入输入框，回车即可发送 -->
            <QuickReplyPicker @pick="onQuickReplyPick" />
            <input ref="fileInput" type="file" multiple style="display:none" @change="pickFile" />
          </div>
          <el-button type="primary" :loading="sending" @click="sendText">发送</el-button>
        </div>
      </el-footer>
      <!-- [113] 搜索打开的非列表客户：只读查看，不向旧会话发送 -->
      <el-footer v-else-if="activeConv" class="chat-footer">
        <el-alert type="info" :closable="false" show-icon title="该客户当前不在会话列表中（可能已结束或不在当前筛选内），仅可查看聊天记录" />
      </el-footer>
    </el-container>

    <!-- [113] 客户资料 / 拉黑 / 全局搜索（均挂载到 body，不影响布局） -->
    <VisitorProfileDrawer
      ref="profileDrawerRef"
      v-model="profileVisible"
      :visitor="profileTarget"
      @saved="onProfileSaved"
      @blocked-change="onBlockedChange" />
    <BlockVisitorDialog v-model="blockVisible" :visitor="blockTarget" @done="onBlockedChange" />
    <ChatSearchDialog v-model="searchVisible" @open="openFromSearch" />

    <!-- 语音通话浮窗（来电 / 通话中 / 结束） -->
    <div v-if="voiceState !== 'idle'" class="voice-overlay" :class="`voice-overlay--${voiceState}`">
      <div class="voice-card">
        <div class="voice-icon-wrap">
          <svg width="44" height="44" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
            <path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72 12.84 12.84 0 0 0 .7 2.81 2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45 12.84 12.84 0 0 0 2.81.7A2 2 0 0 1 22 16.92z"/>
          </svg>
        </div>
        <div class="voice-name">{{ voiceCallerLabel || '访客' }}</div>
        <div class="voice-status">{{ voiceStatusText }}</div>
        <div class="voice-actions">
          <template v-if="voiceState === 'incoming'">
            <el-button type="danger" @click="voiceReject">拒绝</el-button>
            <el-button type="success" @click="voiceAccept">接听</el-button>
          </template>
          <template v-else-if="voiceState === 'accepting' || voiceState === 'talking'">
            <el-button type="danger" @click="voiceEnd('您挂断了', true)">挂断</el-button>
          </template>
        </div>
        <audio ref="voiceRemoteAudioRef" autoplay style="display:none"></audio>
      </div>
    </div>
  </el-container>
</template>

<style scoped>
/* 仅布局类样式，未覆盖任何 element-plus 组件的内部样式 */
.console-root { height: calc(100vh - 100px); background: #f5f7fa; }
.aside { background: #fff; border-right: 1px solid #e6e6e6; display: flex; flex-direction: column; }
.aside-header { padding: 16px 16px 8px; }
.conv-scroll { flex: 1; }

.conv-item {
  display: flex; align-items: center; gap: 12px;
  padding: 12px 14px; cursor: pointer;
  border-bottom: 1px solid #f0f0f0; transition: background .15s;
}
.conv-item:hover { background: #f5f7fa; }
.conv-item--active { background: #ecf5ff; }
.conv-body { flex: 1; min-width: 0; }
.conv-row1 { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.conv-name { font-size: 14px; font-weight: 600; color: #303133; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conv-time { font-size: 12px; color: #c0c4cc; flex-shrink: 0; }
.conv-row2 { display: flex; justify-content: space-between; align-items: center; margin-top: 4px; gap: 8px; }
.conv-preview { font-size: 12px; color: #909399; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
/* [059] 第 3 行：地理位置 + IP，小字灰色 */
.conv-row3 { margin-top: 3px; }
.conv-geoip { font-size: 11px; color: #b1b3b8; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; display: block; }

.chat-header {
  background: #fff; border-bottom: 1px solid #e6e6e6;
  height: auto; padding: 12px 20px;
}
.chat-header-left { display: flex; align-items: center; gap: 12px; }
.chat-header-info { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.chat-header-name { font-size: 15px; font-weight: 600; color: #303133; }
.chat-header-sub { display: flex; gap: 6px; flex-wrap: wrap; }

.msg-main { background: #f5f7fa; padding: 16px 20px; }
.msg-group { margin-bottom: 14px; }
.time-divider {
  text-align: center; margin: 8px 0 12px;
  font-size: 12px; color: #909399;
}
.time-divider span {
  background: #e9eaec; padding: 2px 10px; border-radius: 10px;
}
.msg-line { display: flex; gap: 10px; align-items: flex-start; }
.msg-line--mine { flex-direction: row-reverse; }
.msg-stack { display: flex; flex-direction: column; gap: 4px; max-width: 70%; }
.msg-line--mine .msg-stack { align-items: flex-end; }
.msg-stack-name { font-size: 12px; color: #909399; padding: 0 4px; }

.bubble {
  position: relative;
  padding: 10px 14px; border-radius: 8px;
  background: #fff; border: 1px solid #e6e6e6; color: #303133;
  word-break: break-word; white-space: pre-wrap; line-height: 1.5;
  font-size: 14px; box-shadow: 0 1px 2px rgba(0,0,0,.03);
}
.bubble--mine { background: #409EFF; color: #fff; border-color: #409EFF; }
/* 复制按钮：默认隐藏，hover 气泡时出现 */
.bubble-copy {
  position: absolute; top: 6px; right: 6px;
  width: 22px; height: 22px;
  display: flex; align-items: center; justify-content: center;
  border-radius: 4px;
  opacity: 0; transition: opacity .15s, background .15s;
  cursor: pointer;
  color: #909399; background: rgba(255,255,255,.85);
}
.bubble:hover .bubble-copy { opacity: 1; }
.bubble-copy:hover { background: rgba(0,0,0,.06); color: #409EFF; }
.bubble-copy--mine { color: #fff; background: rgba(255,255,255,.18); }
.bubble-copy--mine:hover { background: rgba(255,255,255,.32); color: #fff; }
.bubble-text { display: block; }
.bubble-img { max-width: 240px; max-height: 240px; border-radius: 4px; display: block; cursor: pointer; }
.bubble-file {
  display: inline-flex; align-items: center; gap: 6px;
  color: inherit; text-decoration: underline; word-break: break-all;
}
.bubble--mine .bubble-file { color: #fff; }

.chat-footer {
  background: #fff; border-top: 1px solid #e6e6e6;
  height: auto; padding: 12px 20px;
}
.chat-footer-actions {
  display: flex; justify-content: space-between; align-items: center; margin-top: 8px;
}

/* 已读 / 未读 角标：在 msg-stack 内最后一个 bubble 下面右对齐 */
.read-indicator {
  font-size: 11px; color: #909399; margin-top: 4px; padding: 0 4px;
  align-self: flex-end;
}

/* 页面跳转横幅（Crisp 风格） */
.page-banner {
  display: flex; align-items: center; gap: 6px;
  margin: 8px auto;
  padding: 6px 14px;
  background: #fff7ed;
  color: #92400e;
  border: 1px solid #fed7aa;
  border-radius: 14px;
  font-size: 12px; line-height: 1.5;
  max-width: 90%;
  word-break: break-all;
}
.page-banner-arrow { color: #f97316; font-weight: 600; flex-shrink: 0; }
.page-banner-label { color: #c2410c; flex-shrink: 0; }
.page-banner-link { color: #c2410c; text-decoration: underline; }
.page-banner-link:hover { color: #9a3412; }

/* ====== 语音通话浮窗 ====== */
.voice-overlay {
  position: fixed; top: 20px; right: 20px;
  z-index: 9999;
  animation: voice-slide-in .25s ease-out;
}
@keyframes voice-slide-in {
  from { transform: translateX(20px); opacity: 0; }
  to   { transform: translateX(0); opacity: 1; }
}
.voice-card {
  width: 300px;
  background: #fff; border-radius: 12px;
  padding: 22px 20px 18px;
  box-shadow: 0 10px 32px rgba(0,0,0,.18);
  border: 1px solid #e5e7eb;
  text-align: center;
}
.voice-overlay--incoming .voice-card {
  background: linear-gradient(160deg, #1e3a8a 0%, #2974ff 100%); color: #fff;
  border-color: transparent;
}
.voice-overlay--talking .voice-card {
  background: linear-gradient(160deg, #064e3b 0%, #10b981 100%); color: #fff;
  border-color: transparent;
}
.voice-overlay--accepting .voice-card {
  background: linear-gradient(160deg, #1e3a8a 0%, #2974ff 100%); color: #fff;
  border-color: transparent;
}
.voice-icon-wrap {
  width: 64px; height: 64px; border-radius: 50%;
  background: rgba(255,255,255,.22);
  display: flex; align-items: center; justify-content: center;
  margin: 0 auto 12px;
  color: #fff;
}
.voice-overlay--incoming .voice-icon-wrap {
  animation: voice-pulse 1.4s ease-in-out infinite;
}
@keyframes voice-pulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(255,255,255,.4); }
  50%      { box-shadow: 0 0 0 14px rgba(255,255,255,0); }
}
.voice-name { font-size: 17px; font-weight: 600; margin-bottom: 4px; }
.voice-status { font-size: 13px; opacity: .88; margin-bottom: 18px; }
.voice-actions { display: flex; justify-content: center; gap: 12px; }

/* [040] pending 多文件预览队列 */
.pending-list {
  display: flex; flex-wrap: wrap; gap: 6px;
  margin-bottom: 6px;
}
.pending-chip {
  display: inline-flex; align-items: center; gap: 6px;
  background: #fff; border: 1px solid #e5e7eb; border-radius: 8px;
  padding: 4px 6px; max-width: 220px; min-height: 36px;
}
.pending-chip img.thumb {
  width: 36px; height: 36px; object-fit: cover; border-radius: 5px; flex-shrink: 0;
}
.pending-chip .file-icon {
  width: 30px; height: 30px; border-radius: 5px; background: #eef4ff;
  color: #2974ff; display: inline-flex; align-items: center; justify-content: center;
  flex-shrink: 0;
}
.pending-chip .meta { display: flex; flex-direction: column; min-width: 0; overflow: hidden; flex: 1; }
.pending-chip .name {
  font-size: 12px; color: #2c3034; white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis;
}
.pending-chip .size { font-size: 10px; color: #9ca3af; margin-top: 1px; }
.pending-chip .remove-btn {
  width: 20px; height: 20px; border-radius: 50%; border: 0; cursor: pointer;
  background: #f3f4f6; color: #6b7280; font-size: 13px; line-height: 1;
  display: inline-flex; align-items: center; justify-content: center;
  flex-shrink: 0;
}
.pending-chip .remove-btn:hover { background: #fee2e2; color: #dc2626; }
</style>
