<script setup>
// 多人大厅（客服端）：进入/离开、收听与发言、摄像头、全员静音、参与者管理、视频区、大厅消息。
// 契约：_internal/reports/hall-contract.md。媒体走自托管 LiveKit（livekit-client 2.23.0），
// 实时事件复用现有 /ws/agent 连接的信封（hall_chat / hall_ack / hall_deleted / hall_settings / hall_moderation）。
// 参与者状态以 LiveKit 房间事件为主，每 30 秒用 GET /api/agent/hall/state 校准。
// [123] 顶部「大厅开关」面板（契约第 10 节）：所有客服可切换总开关 / 文字聊天 / 语音 / 视频；
// 页面状态（settings）只采用服务端确认的值（开关接口返回、hall_settings 广播、state、join），面板另叠加提交中的乐观值。
import { computed, markRaw, onBeforeUnmount, onMounted, reactive, ref, shallowRef } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, ElNotification } from 'element-plus'
import http from '../api/http'
import { AgentWS } from '../api/ws'
import { playSound } from '../api/sound'
import { useSession } from '../store/session'
import {
  deleteHallMessage,
  getHallState,
  getHallSwitches,
  joinHall,
  listHallMessages,
  moderateHall,
  setHallCamera,
  updateHallSwitches,
  uploadHallFile,
} from '../api/hall'
import {
  DISCONNECT_REASON,
  HALL_ERROR_CODES,
  connectErrorMessage,
  disconnectReasonMessage,
  hallErrorCode,
  hallErrorMessage,
  mediaErrorMessage,
  wsErrorText,
} from '../modules/hallErrors'
import { HallRoster } from '../modules/hallParticipants'
import {
  HALL_ACK_TIMEOUT_MS,
  HALL_MESSAGE_MEMORY_LIMIT,
  HALL_PAGE_SIZE,
  SlidingWindowLimiter,
  buildHallChatEnvelope,
  checkHallUploadFile,
  createHallMessageId,
  createPendingHallMessage,
  fromHallChatEnvelope,
  isValidHallMessageId,
  markHallMessageStatus,
  mediaFromUploadResponse,
  mergeHallMessages,
  normalizeHallMessageList,
  oldestCursor,
  removeHallMessage,
  validateOutgoingText,
} from '../modules/hallMessages'
import { kickMinutesLabel, normalizeHallState, normalizeJoinResponse } from '../modules/hallProtocol'
import { normalizeHallSettingsEvent } from '../modules/hallSettings'
import {
  HALL_MASTER_OFF_CONFIRM,
  HALL_SWITCH_FIELDS,
  HALL_SWITCH_TEXT,
  buildSwitchRequest,
  displaySwitches,
  effectiveSwitches,
  needsSwitchConfirm,
  normalizeSwitchesView,
  pickSwitches,
  switchToggleMessage,
  switchTransitions,
} from '../modules/hallSwitches'
import { HALL_MAX_VIDEO_TILES, selectVideoTiles } from '../modules/hallVideo'
import { HallTabCoordinator } from '../modules/hallTabCoordinator'
import { registerHallLeaveHandler } from '../modules/hallSession'
import { createJumpTicket } from '../modules/privateChatJump'
import { HallRoomClient } from '../modules/hallRoom'
import { hallIcons } from '../components/hall/hallIcons'
import HallParticipantTable from '../components/hall/HallParticipantTable.vue'
import HallMessagePanel from '../components/hall/HallMessagePanel.vue'
import HallVideoTile from '../components/hall/HallVideoTile.vue'
import HallKickDialog from '../components/hall/HallKickDialog.vue'
import HallSwitchPanel from '../components/hall/HallSwitchPanel.vue'
import BlockVisitorDialog from '../components/console/BlockVisitorDialog.vue'

const CALIBRATE_INTERVAL_MS = 30 * 1000
const MIN_CALIBRATE_GAP_MS = 5 * 1000
const RENDER_THROTTLE_MS = 200
const PRIVATE_NOTIFY_GAP_MS = 8 * 1000
// 收到其他来源的开关变化后，合并 600ms 再读取一次开关接口（更新「最后修改人与时间」）
const SWITCHES_RELOAD_DEBOUNCE_MS = 600
// 语音刚恢复时服务端经信令补发麦克风权限，开麦前最多等待这么久
const MIC_PERMISSION_WAIT_MS = 3000

const session = useSession()
const router = useRouter()

// ---------- 页面状态 ----------
const phase = ref('idle') // idle | joining | connected | reconnecting | leaving
const settings = reactive({ enabled: true, chatEnabled: true, voiceEnabled: true, videoEnabled: false, maxSpeakers: 6, maxCameras: 2, maxParticipants: 100 })
const roster = new HallRoster()
const rows = shallowRef([])
const counts = shallowRef({ participants: 0, speakers: 0, visitorSpeakers: 0, cameras: 0 })
const stateLoading = ref(false)
const messages = shallowRef([])
const messagesLoading = ref(false)
const hasMoreOlder = ref(false)
const loadingOlder = ref(false)
const limitReached = ref(false)
const draft = ref('')
const uploading = ref(false)
const micOn = ref(false)
const cameraOn = ref(false)
const micBusy = ref(false)
const cameraBusy = ref(false)
const muteAllBusy = ref(false)
const audioBlocked = ref(false)
const notice = ref(null)
const videoTiles = shallowRef([])
const videoTotal = ref(0)
const pending = reactive({})
const kickVisible = ref(false)
const kickTarget = ref(null)
const kickBusy = ref(false)
const blockVisible = ref(false)
const blockTarget = ref(null)
const degraded = ref(false)
const hallSubscribed = ref(false)
const isAdmin = computed(() => session.agent?.role === 'admin')

// [123] 大厅开关面板
const switchMeta = ref({ updatedBy: '', updatedAt: '' })
const switchOverlay = reactive({}) // 提交中的乐观值：{ [field]: boolean }，失败时删除即回滚
const switchBusy = reactive({}) // 提交中的开关：{ [field]: true }，期间禁用该开关防重复点击
const switchesReady = ref(false)
const switchesLoading = ref(false)
const switchesError = ref('')
const panelSwitches = computed(() => displaySwitches(settings, switchOverlay))
const effective = computed(() => effectiveSwitches(settings))
const chatActive = computed(() => effective.value.chat)
const voiceActive = computed(() => effective.value.voice)

const inHall = computed(() => phase.value !== 'idle')
const isConnected = computed(() => phase.value === 'connected' || phase.value === 'reconnecting')
const phaseTag = computed(() => ({
  idle: { type: 'info', text: '未进入' },
  joining: { type: 'warning', text: '正在进入' },
  connected: { type: 'success', text: '已进入' },
  reconnecting: { type: 'warning', text: '网络波动，正在重连' },
  leaving: { type: 'info', text: '正在离开' },
}[phase.value]))
const showVideoCard = computed(() => isConnected.value && (settings.videoEnabled || videoTiles.value.length > 0))
const videoHint = computed(() => {
  if (!videoTotal.value) return '当前没有人开摄像头'
  const more = videoTotal.value > HALL_MAX_VIDEO_TILES ? '，仅显示 ' + HALL_MAX_VIDEO_TILES + ' 路' : ''
  return '当前 ' + videoTotal.value + ' 路摄像头' + more + '（低清观看）'
})

// ---------- 非响应式运行时 ----------
let client = null
let ws = null
let wsReady = false
let tabs = null
let unregisterLeave = null
let calibrateTimer = null
let calibrateDebounce = null
let renderTimer = null
let stateSeq = 0
let stateInflight = false
let lastStateAt = 0
let videoSelected = []
let unmounted = false
let selfInfo = { identity: '', name: '' }
let agentSound = 'agent1'
let lastPrivateNotifyAt = 0
let subscribeId = ''
let wsHelloSeen = false
let switchesGetSeq = 0
let switchesStamp = 0 // 每采用一次本页切换的返回结果就递增：比它早发出的读取结果作废
let switchesReloadTimer = null
const ackTimers = new Map()
const limiter = new SlidingWindowLimiter()

function setNotice(type, title, description = '', kind = '') {
  notice.value = { type, title, description, kind }
}

// ---------- 渲染（合并高频 LiveKit 事件，最多每 200ms 刷新一次表格与视频区） ----------
function scheduleRender() {
  if (renderTimer || unmounted) return
  renderTimer = setTimeout(() => {
    renderTimer = null
    render()
  }, RENDER_THROTTLE_MS)
}

function render() {
  if (unmounted) return
  if (client && isConnected.value) {
    roster.replaceLive(client.snapshots())
    const local = client.localState()
    micOn.value = local.micOn
    cameraOn.value = local.cameraOn
  }
  rows.value = roster.rows()
  counts.value = roster.counts()
  renderVideo()
}

function renderVideo() {
  if (!client || !isConnected.value) {
    videoSelected = []
    videoTiles.value = []
    videoTotal.value = 0
    return
  }
  const candidates = client.videoCandidates(roster.speakers)
  videoTotal.value = candidates.length
  videoSelected = selectVideoTiles(candidates, { max: HALL_MAX_VIDEO_TILES, previous: videoSelected })
  client.applyVideoSelection(videoSelected)
  // LiveKit 对象不做深层响应式代理
  videoTiles.value = client.videoTiles(videoSelected).map(t => ({ ...t, track: t.track ? markRaw(t.track) : null }))
}

// ---------- 设置 ----------
// s：normalizeHallSettingsEvent / normalizeSwitchesView 的输出（只含已提供的字段）；
// source：switches（开关接口）| ws（hall_settings 广播）| state（30 秒校准）| join（入场返回）| error（接口错误码推断）
function applySettings(s, { source = 'state' } = {}) {
  if (!s || unmounted) return
  const prev = pickSwitches(settings)
  for (const f of HALL_SWITCH_FIELDS) if (typeof s[f] === 'boolean') settings[f] = s[f]
  if (s.maxSpeakers !== undefined) settings.maxSpeakers = s.maxSpeakers
  if (s.maxCameras !== undefined) settings.maxCameras = s.maxCameras
  if (s.maxParticipants !== undefined) settings.maxParticipants = s.maxParticipants
  const t = switchTransitions(prev, settings)
  // 开关被其他客服或 App 改动：再读一次开关接口，更新「最后修改人与时间」
  if (t.changed && source !== 'switches') scheduleSwitchesReload()
  if (t.hallClosed && client) {
    leaveHall('closed')
    setNotice('warning', HALL_SWITCH_TEXT.hallClosed, '已自动退出大厅：访客窗口只保留私聊。任一客服在上方「大厅开关」中重新打开总开关后，可再次进入。', 'closed')
  }
  if (t.hallOpened) {
    if (notice.value?.kind === 'closed') notice.value = null
    if (source !== 'state') refreshState({ force: true })
    loadLatestMessages()
  }
  if (t.voicePaused && client) {
    // 服务端会收回麦克风权限；本端立即停止发布并释放设备（未开麦时为空操作）
    const hadMic = micOn.value
    const c = client
    c.stopMicrophone().catch(() => c.setMicrophone(false)).catch(() => {}).finally(scheduleRender)
    if (hadMic) ElMessage.warning(HALL_SWITCH_TEXT.voicePaused + '，你的麦克风已关闭')
  }
  if (t.videoOff && client && cameraOn.value) {
    client.setCamera(false).catch(() => {}).finally(scheduleRender)
    ElMessage.info(HALL_SWITCH_TEXT.videoOff + '，你的摄像头已关闭')
  }
  scheduleRender()
}

// ---------- 大厅开关（契约第 10 节） ----------
function applySwitchView(v) {
  applySettings(v, { source: 'switches' })
  switchMeta.value = { updatedBy: v.updatedBy, updatedAt: v.updatedAt }
  switchesReady.value = true
  switchesError.value = ''
}

async function loadSwitches({ notify = false } = {}) {
  if (unmounted) return
  const seq = ++switchesGetSeq
  const stamp = switchesStamp
  switchesLoading.value = true
  try {
    const r = await getHallSwitches()
    if (unmounted || seq !== switchesGetSeq) return
    // 读取期间本页切换过开关：以切换返回的结果为准（随后的广播 / 读取会再次对齐）
    if (stamp !== switchesStamp) return
    const v = normalizeSwitchesView(r?.data)
    if (!v.ok) throw { code: -1, msg: v.error }
    applySwitchView(v.value)
    if (notify) ElMessage.success('大厅开关已刷新')
  } catch (e) {
    if (unmounted || seq !== switchesGetSeq) return
    switchesError.value = hallErrorMessage(e, '获取大厅开关失败，请稍后重试')
    if (notify) ElMessage.error(switchesError.value)
  } finally {
    if (seq === switchesGetSeq) switchesLoading.value = false
  }
}

function scheduleSwitchesReload() {
  if (switchesReloadTimer || unmounted) return
  switchesReloadTimer = setTimeout(() => {
    switchesReloadTimer = null
    loadSwitches()
  }, SWITCHES_RELOAD_DEBOUNCE_MS)
}

async function changeSwitch(field, value) {
  if (unmounted || switchBusy[field] || !switchesReady.value) return
  const req = buildSwitchRequest(field, value)
  if (!req.ok) {
    ElMessage.error(req.error)
    return
  }
  if (needsSwitchConfirm(field, value)) {
    try {
      await ElMessageBox.confirm(HALL_MASTER_OFF_CONFIRM.message, HALL_MASTER_OFF_CONFIRM.title, {
        type: 'warning',
        confirmButtonText: HALL_MASTER_OFF_CONFIRM.confirmButtonText,
        cancelButtonText: HALL_MASTER_OFF_CONFIRM.cancelButtonText,
      })
    } catch {
      return
    }
    if (unmounted || switchBusy[field]) return
  }
  switchBusy[field] = true
  switchOverlay[field] = value
  try {
    const r = await updateHallSwitches(req.body)
    if (unmounted) return
    switchesStamp++
    const v = normalizeSwitchesView(r?.data)
    if (v.ok) {
      applySwitchView(v.value)
    } else {
      // 已保存成功但返回体异常：先采用本次提交的值，再重新读取一次对齐
      applySettings({ [field]: value }, { source: 'switches' })
      loadSwitches()
    }
    ElMessage.success(switchToggleMessage(field, value))
  } catch (e) {
    if (unmounted) return
    ElMessage.error(hallErrorMessage(e, '切换失败，请稍后重试'))
  } finally {
    // 去掉乐观值：成功时显示的已是服务端确认值；失败时即回滚到确认值（含期间收到的其他客服广播）
    delete switchOverlay[field]
    delete switchBusy[field]
  }
}

// ---------- 服务端状态校准 ----------
async function refreshState({ force = false, notify = false } = {}) {
  if (unmounted || stateInflight) return
  if (!force && typeof document !== 'undefined' && document.visibilityState === 'hidden' && !client) return
  stateInflight = true
  const seq = ++stateSeq
  if (notify) stateLoading.value = true
  try {
    const r = await getHallState()
    if (unmounted || seq !== stateSeq) return
    const st = normalizeHallState(r?.data)
    applySettings(st.settings)
    degraded.value = st.degraded
    roster.replaceServer(st.participants, st.counts)
    lastStateAt = Date.now()
    render()
  } catch (e) {
    if (hallErrorCode(e) === HALL_ERROR_CODES.HALL_CLOSED) applySettings({ enabled: false }, { source: 'error' })
    if (notify) ElMessage.error(hallErrorMessage(e, '获取大厅状态失败，请稍后重试'))
  } finally {
    stateInflight = false
    if (notify) stateLoading.value = false
  }
}

// 新参与者加入时提前校准一次（拿到 vid 才能拉黑 / 跳私聊），两次校准至少间隔 5 秒
function scheduleCalibrate(delay = 3000) {
  if (calibrateDebounce || unmounted) return
  const wait = Math.max(delay, MIN_CALIBRATE_GAP_MS - (Date.now() - lastStateAt))
  calibrateDebounce = setTimeout(() => {
    calibrateDebounce = null
    refreshState()
  }, wait)
}

// ---------- 大厅消息 ----------
async function loadLatestMessages({ initial = false } = {}) {
  if (initial) messagesLoading.value = true
  try {
    const r = await listHallMessages({ limit: HALL_PAGE_SIZE })
    if (unmounted) return
    const raw = Array.isArray(r?.data) ? r.data : []
    messages.value = mergeHallMessages(messages.value, normalizeHallMessageList(raw))
    if (initial) hasMoreOlder.value = raw.length >= HALL_PAGE_SIZE
  } catch (e) {
    if (initial) ElMessage.error(hallErrorMessage(e, '加载大厅消息失败，请稍后重试'))
  } finally {
    if (initial) messagesLoading.value = false
  }
}

async function loadOlder() {
  if (loadingOlder.value || !hasMoreOlder.value) return
  if (messages.value.length + HALL_PAGE_SIZE > HALL_MESSAGE_MEMORY_LIMIT) {
    limitReached.value = true
    return
  }
  const before = oldestCursor(messages.value)
  if (!before) {
    hasMoreOlder.value = false
    return
  }
  loadingOlder.value = true
  try {
    const r = await listHallMessages({ before, limit: HALL_PAGE_SIZE })
    if (unmounted) return
    const raw = Array.isArray(r?.data) ? r.data : []
    messages.value = mergeHallMessages(messages.value, normalizeHallMessageList(raw))
    hasMoreOlder.value = raw.length >= HALL_PAGE_SIZE
  } catch (e) {
    ElMessage.error(hallErrorMessage(e, '加载更早的消息失败，请稍后重试'))
  } finally {
    loadingOlder.value = false
  }
}

function clearAck(id) {
  const t = ackTimers.get(id)
  if (t) clearTimeout(t)
  ackTimers.delete(id)
}

function armAck(id) {
  clearAck(id)
  ackTimers.set(id, setTimeout(() => {
    ackTimers.delete(id)
    messages.value = markHallMessageStatus(messages.value, id, 'failed')
  }, HALL_ACK_TIMEOUT_MS))
}

function dispatchHallMessage({ id = '', content = '', media = null }) {
  let mid = id
  try {
    if (!mid) mid = createHallMessageId()
  } catch (e) {
    ElMessage.error(e?.message || '消息 ID 生成失败')
    return false
  }
  const env = buildHallChatEnvelope({ id: mid, content, media })
  if (!env) {
    ElMessage.error('消息内容无效')
    return false
  }
  const local = createPendingHallMessage({ id: mid, content, media, self: selfInfo })
  if (local) messages.value = mergeHallMessages(messages.value, [local])
  const ok = !!ws && wsReady && ws.send(env)
  if (!ok) {
    messages.value = markHallMessageStatus(messages.value, mid, 'failed')
    ElMessage.error('实时连接未就绪，消息未发送，请稍后点击「重发」')
    return false
  }
  armAck(mid)
  return true
}

function sendDraft() {
  if (!isConnected.value) {
    ElMessage.warning('请先进入大厅')
    return
  }
  if (!chatActive.value) {
    ElMessage.warning(HALL_SWITCH_TEXT.chatPaused)
    return
  }
  const v = validateOutgoingText(draft.value)
  if (!v.ok) {
    ElMessage.warning(v.error)
    return
  }
  if (!limiter.tryAcquire()) {
    ElMessage.warning('发送过于频繁，请稍后再试')
    return
  }
  if (dispatchHallMessage({ content: v.content })) draft.value = ''
}

function retryMessage(m) {
  if (!m || m.status !== 'failed' || !isConnected.value) return
  if (!chatActive.value) {
    ElMessage.warning(HALL_SWITCH_TEXT.chatPaused)
    return
  }
  if (!limiter.tryAcquire()) {
    ElMessage.warning('发送过于频繁，请稍后再试')
    return
  }
  const media = m.mediaUrl ? { url: m.mediaUrl, kind: m.mediaKind, name: m.mediaName, size: m.mediaSize } : null
  dispatchHallMessage({ id: m.id, content: m.content, media })
}

async function uploadAndSend(file, kind) {
  if (!isConnected.value || uploading.value) return
  if (!chatActive.value) {
    ElMessage.warning(HALL_SWITCH_TEXT.chatPaused)
    return
  }
  const check = checkHallUploadFile(file, kind)
  if (!check.ok) {
    ElMessage.warning(check.error)
    return
  }
  if (!limiter.tryAcquire()) {
    ElMessage.warning('发送过于频繁，请稍后再试')
    return
  }
  uploading.value = true
  try {
    const r = await uploadHallFile(file)
    const media = mediaFromUploadResponse(r)
    if (!media) {
      ElMessage.error('上传结果无效，请重试')
      return
    }
    if (!isConnected.value || !chatActive.value) return
    dispatchHallMessage({ media })
  } catch (e) {
    const code = hallErrorCode(e)
    if (code === HALL_ERROR_CODES.CHAT_DISABLED) applySettings({ chatEnabled: false }, { source: 'error' })
    else if (code === HALL_ERROR_CODES.HALL_CLOSED) applySettings({ enabled: false }, { source: 'error' })
    ElMessage.error(hallErrorMessage(e, '文件上传失败，请重试'))
  } finally {
    uploading.value = false
  }
}

async function confirmDelete(m) {
  if (!m || !isValidHallMessageId(m.id)) return
  try {
    await ElMessageBox.confirm('确定删除这条大厅消息？删除后所有人都将看不到这条消息，且无法恢复。', '删除大厅消息', {
      type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await deleteHallMessage(m.id)
    messages.value = removeHallMessage(messages.value, m.id)
    ElMessage.success('已删除')
  } catch (e) {
    if (hallErrorCode(e) === HALL_ERROR_CODES.MESSAGE_NOT_FOUND) {
      messages.value = removeHallMessage(messages.value, m.id)
      ElMessage.info('该消息已不存在')
      return
    }
    ElMessage.error(hallErrorMessage(e, '删除失败，请稍后重试'))
  }
}

// ---------- WS ----------
// 契约第 5 节修订：订阅帧带 UUID，成功回 hall_subscribed，失败回带同一 id 的 error
function subscribeHall() {
  if (!ws || !wsReady) return
  try {
    subscribeId = createHallMessageId()
  } catch {
    return
  }
  hallSubscribed.value = false
  if (!ws.send({ type: 'hall_subscribe', id: subscribeId })) subscribeId = ''
}

function unsubscribeHall() {
  subscribeId = ''
  hallSubscribed.value = false
  if (!ws || !wsReady) return
  try {
    ws.send({ type: 'hall_unsubscribe', id: createHallMessageId() })
  } catch { /* 连接断开时服务端会自动移出广播集合 */ }
}

function onWsMessage(env) {
  if (!env || typeof env !== 'object' || unmounted) return
  switch (env.type) {
    case 'hello':
      wsReady = true
      hallSubscribed.value = false
      // 断线重连：补读断线期间可能错过的开关变化（首次连接时挂载阶段已读取）
      if (wsHelloSeen) loadSwitches()
      wsHelloSeen = true
      // 重连后重新订阅（订阅成功后再补齐断线期间的消息）
      if (client && isConnected.value) subscribeHall()
      return
    case 'hall_subscribed':
      if (!subscribeId || env.id !== subscribeId || !client) return
      hallSubscribed.value = true
      // 补齐「拿到历史」到「订阅生效」之间可能漏掉的消息
      loadLatestMessages()
      return
    case 'hall_unsubscribed':
      return
    case 'hall_chat': {
      const m = fromHallChatEnvelope(env)
      if (!m) return
      clearAck(m.id)
      messages.value = mergeHallMessages(messages.value, [m])
      return
    }
    case 'hall_ack':
      if (!isValidHallMessageId(env.id)) return
      clearAck(env.id)
      messages.value = markHallMessageStatus(messages.value, env.id, 'sent')
      return
    case 'hall_deleted':
      if (!isValidHallMessageId(env.id)) return
      clearAck(env.id)
      messages.value = removeHallMessage(messages.value, env.id)
      return
    case 'hall_settings':
      // [123] extra 含 enabled / chat_enabled / voice_enabled / video_enabled（其他客服或 App 改动也实时同步）
      applySettings(normalizeHallSettingsEvent(env.extra), { source: 'ws' })
      return
    case 'hall_moderation':
      onModeration(env.extra)
      return
    case 'error':
      onWsError(env)
      return
    case 'chat':
      onPrivateMessage(env)
      return
    case 'voice_call':
      ElNotification({
        title: '语音来电',
        message: '有访客发起语音通话，请到「在线会话」页面接听（离开大厅页会退出大厅）。',
        type: 'warning',
        duration: 8000,
        position: 'bottom-right',
      })
      return
    default:
  }
}

function onWsError(env) {
  const id = typeof env.id === 'string' ? env.id : ''
  if (id && id === subscribeId) {
    subscribeId = ''
    ElMessage.error('大厅消息实时接收失败：' + wsErrorText(env.content, '请稍后重新进入大厅'))
    return
  }
  if (id && ackTimers.has(id)) {
    clearAck(id)
    messages.value = markHallMessageStatus(messages.value, id, 'failed')
    ElMessage.error(wsErrorText(env.content, '消息发送失败'))
    return
  }
  if (typeof env.content === 'string' && env.content.trim()) ElMessage.error(wsErrorText(env.content))
}

// 大厅页仍要提醒新的私聊消息（不展示内容，节流 8 秒）
function onPrivateMessage(env) {
  if (typeof env.from !== 'string' || !env.from.startsWith('visitor:')) return
  const now = Date.now()
  if (now - lastPrivateNotifyAt < PRIVATE_NOTIFY_GAP_MS) return
  lastPrivateNotifyAt = now
  ElNotification({ title: '新的私聊消息', message: '有访客发来私聊消息，可到「在线会话」查看。', type: 'info', duration: 4500, position: 'bottom-right' })
  playSound(agentSound)
}

function onModeration(extra) {
  const action = extra && typeof extra === 'object' ? extra.action : ''
  if (action === 'kicked') {
    const minutes = Number(extra.minutes)
    leaveHall('kicked')
    setNotice('warning', '你已被移出大厅', Number.isInteger(minutes) && minutes > 0 ? kickMinutesLabel(minutes) + '内不能再次进入。' : '')
  } else if (action === 'muted') {
    if (client && micOn.value) client.setMicrophone(false).catch(() => {}).finally(scheduleRender)
    ElMessage.warning('你已被管理员静音，重新进入大厅后可恢复麦克风')
  } else if (action === 'camera_off') {
    if (client && cameraOn.value) client.setCamera(false).catch(() => {}).finally(scheduleRender)
    ElMessage.warning('你的摄像头已被关闭')
  }
}

function startWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new AgentWS({
    url: proto + '://' + location.host + '/ws/agent',
    token: session.token,
    onMessage: onWsMessage,
    onClose: () => { wsReady = false },
  })
  ws.start()
}

// ---------- 进入 / 离开 ----------
function createClient() {
  let owner = null
  // 只处理当前连接的回调；已离开或被替换的旧连接回调直接忽略
  const guard = fn => (...args) => { if (owner && client === owner && !unmounted) fn(...args) }
  owner = new HallRoomClient({
    onRoster: guard(scheduleRender),
    onVideo: guard(scheduleRender),
    onSpeakers: guard((ids) => { roster.setSpeakers(ids); scheduleRender() }),
    onParticipantJoined: guard(() => scheduleCalibrate(3000)),
    onLocalPermissions: guard(scheduleRender),
    onAudioPlayback: guard((can) => { if (isConnected.value) audioBlocked.value = !can }),
    onConnectionState: guard((s) => { if (isConnected.value) phase.value = s }),
    onMediaError: guard((e, kind) => ElMessage.error(mediaErrorMessage(e, kind === 'videoinput' ? 'camera' : 'microphone'))),
    onDisconnected: guard((reason, info) => onRoomDisconnected(reason, info)),
  })
  return owner
}

async function enterHall() {
  if (phase.value !== 'idle') return
  if (!settings.enabled) {
    ElMessage.warning('大厅已关闭')
    return
  }
  notice.value = null
  phase.value = 'joining'
  tabs?.claim()
  const c = createClient()
  client = c
  // 「进入大厅」点击即用户手势：在同步调用栈里解锁浏览器声音（契约 7.3）
  c.primeAudio()
  const abandon = () => {
    if (client === c) {
      client = null
      phase.value = 'idle'
    }
    c.disconnect().catch(() => {})
  }
  let joined
  try {
    const r = await joinHall()
    joined = normalizeJoinResponse(r?.data, { pageProtocol: location.protocol })
    if (!joined.ok) throw { code: -1, msg: joined.error }
  } catch (e) {
    const stillMine = client === c
    abandon()
    if (!stillMine || unmounted) return
    if (hallErrorCode(e) === HALL_ERROR_CODES.HALL_CLOSED) applySettings({ enabled: false }, { source: 'error' })
    ElMessage.error(hallErrorMessage(e, '进入大厅失败，请稍后重试'))
    return
  }
  if (client !== c || unmounted) return abandon()
  try {
    await c.connect(joined.value)
  } catch (e) {
    const stillMine = client === c
    abandon()
    if (!stillMine || unmounted) return
    const text = connectErrorMessage(e)
    if (text) ElMessage.error(text)
    return
  }
  if (client !== c || unmounted) return abandon()
  const v = joined.value
  selfInfo = { identity: v.identity, name: v.name }
  roster.setConnected(true, v.identity)
  phase.value = 'connected'
  applySettings({ videoEnabled: v.videoEnabled, chatEnabled: v.chatEnabled, voiceEnabled: v.voiceEnabled, maxSpeakers: v.maxSpeakers, maxCameras: v.maxCameras }, { source: 'join' })
  messages.value = mergeHallMessages(messages.value, v.history)
  try {
    await c.startAudio()
  } catch { /* 被浏览器拦截时显示「开启声音」提示 */ }
  audioBlocked.value = !c.canPlaybackAudio
  subscribeHall()
  render()
  refreshState({ force: true })
  ElMessage.success('已进入大厅')
}

function resetAfterLeave() {
  phase.value = 'idle'
  micOn.value = false
  cameraOn.value = false
  audioBlocked.value = false
  roster.setConnected(false)
  videoSelected = []
  videoTiles.value = []
  videoTotal.value = 0
  for (const t of ackTimers.values()) clearTimeout(t)
  ackTimers.clear()
  messages.value = messages.value.map(m => (m.status === 'sending' ? { ...m, status: 'failed' } : m))
  if (unmounted) return
  render()
  refreshState({ force: true })
}

async function leaveHall(reason = 'user') {
  const c = client
  if (!c) return
  client = null
  phase.value = 'leaving'
  unsubscribeHall()
  const hadCamera = cameraOn.value
  try {
    await c.disconnect()
  } catch { /* 断开失败也继续收尾 */ }
  // 主动离开时释放摄像头名额（服务端收到 participant_left 也会释放，这里是兜底）
  if (hadCamera && reason !== 'kicked' && reason !== 'closed') {
    try { await setHallCamera(false) } catch { /* 忽略 */ }
  }
  resetAfterLeave()
}

function onRoomDisconnected(reason, info) {
  client = null
  unsubscribeHall()
  resetAfterLeave()
  if (info?.initiatedByClient) return
  const text = disconnectReasonMessage(reason)
  if (!text) return
  if (reason === DISCONNECT_REASON.DUPLICATE_IDENTITY) setNotice('info', '已在其他地方进入大厅', text)
  else if (reason === DISCONNECT_REASON.PARTICIPANT_REMOVED) setNotice('warning', '已离开大厅', text)
  else if (reason === DISCONNECT_REASON.ROOM_DELETED || reason === DISCONNECT_REASON.ROOM_CLOSED) setNotice('warning', '大厅已关闭', text)
  else ElMessage.warning(text)
}

function onTakenOver() {
  if (!client) return
  leaveHall('takeover')
  setNotice('info', '已在其他标签页进入大厅', '同一账号在其他标签页进入了大厅，本页已自动退出。如需在本页收听，请重新点击「进入大厅」。')
}

async function confirmLeave() {
  if (!client) return
  try {
    await ElMessageBox.confirm('离开后将不再收听和发言，确定离开大厅？', '离开大厅', { type: 'warning', confirmButtonText: '离开', cancelButtonText: '取消' })
  } catch {
    return
  }
  await leaveHall('user')
  ElMessage.success('已离开大厅')
}

// ---------- 麦克风 / 摄像头 / 声音 ----------
async function toggleMic() {
  if (!client || !isConnected.value || micBusy.value) return
  const c = client
  const target = !micOn.value
  if (target && !voiceActive.value) {
    ElMessage.warning(HALL_SWITCH_TEXT.voicePaused)
    return
  }
  micBusy.value = true
  try {
    if (target && !c.hasPublishPermission('microphone')) {
      // 语音刚恢复时服务端经信令补发麦克风权限，稍等片刻
      const permitted = await c.waitForPublishPermission('microphone', MIC_PERMISSION_WAIT_MS)
      if (client !== c) return
      if (!permitted) {
        ElMessage.warning('暂时没有麦克风权限：如果大厅语音刚刚恢复，请离开大厅后重新进入')
        return
      }
    }
    await c.setMicrophone(target)
    if (client === c) micOn.value = target
  } catch (e) {
    ElMessage.error(mediaErrorMessage(e, 'microphone'))
  } finally {
    micBusy.value = false
    scheduleRender()
  }
}

async function toggleCamera() {
  if (!client || !isConnected.value || cameraBusy.value) return
  const c = client
  cameraBusy.value = true
  try {
    if (cameraOn.value) {
      try { await c.setCamera(false) } catch { /* 本地已停止 */ }
      if (client === c) cameraOn.value = false
      try {
        await setHallCamera(false)
      } catch (e) {
        ElMessage.warning(hallErrorMessage(e, '摄像头已关闭，但释放名额失败，稍后会自动释放'))
      }
      return
    }
    if (!effective.value.video) {
      ElMessage.warning(HALL_SWITCH_TEXT.videoOff)
      return
    }
    try {
      await setHallCamera(true) // 服务端占名额并授予摄像头发布权限
    } catch (e) {
      const code = hallErrorCode(e)
      if (code === HALL_ERROR_CODES.VIDEO_DISABLED) applySettings({ videoEnabled: false }, { source: 'error' })
      ElMessage.error(hallErrorMessage(e, '开启摄像头失败，请稍后重试'))
      return
    }
    const permitted = await c.waitForPublishPermission('camera', 5000)
    if (client !== c) return
    if (!permitted) {
      setHallCamera(false).catch(() => {})
      ElMessage.error('服务器尚未授予摄像头权限，请稍后重试')
      return
    }
    try {
      await c.setCamera(true)
      if (client === c) cameraOn.value = true
    } catch (e) {
      setHallCamera(false).catch(() => {}) // 本地发布失败要释放名额
      ElMessage.error(mediaErrorMessage(e, 'camera'))
    }
  } finally {
    cameraBusy.value = false
    scheduleRender()
  }
}

async function unlockAudio() {
  if (!client) return
  try {
    await client.startAudio()
  } catch { /* 下面统一判断 */ }
  audioBlocked.value = !!client && !client.canPlaybackAudio
  if (audioBlocked.value) ElMessage.warning('浏览器仍阻止播放声音，请再点击一次或检查浏览器的声音设置')
}

// ---------- 管理操作 ----------
const ACTION_TEXT = { mute: '已静音', camera_off: '已关闭摄像头', kick: '已移出大厅' }

async function moderate(action, row, extra = {}) {
  const id = row?.identity
  if (!id || pending[id]) return false
  pending[id] = true
  try {
    await moderateHall({ action, identity: id, ...extra })
    ElMessage.success(ACTION_TEXT[action] + '：' + row.name)
    scheduleCalibrate(1000)
    return true
  } catch (e) {
    if (action === 'kick' && hallErrorCode(e) === HALL_ERROR_CODES.LIVEKIT_UNAVAILABLE) {
      // 契约 4.2 补充：禁入已写入，语音服务恢复后由对账自动移出
      ElMessage.warning('语音服务暂不可用：已记录禁入，服务恢复后会自动移出' + row.name)
      scheduleCalibrate(1000)
      return true
    }
    ElMessage.error(hallErrorMessage(e, '操作失败，请稍后重试'))
    return false
  } finally {
    delete pending[id]
  }
}

async function muteAll() {
  if (muteAllBusy.value) return
  try {
    await ElMessageBox.confirm('将收回所有访客的麦克风权限（客服不受影响），确定全员静音？', '全员静音', {
      type: 'warning', confirmButtonText: '全员静音', cancelButtonText: '取消',
    })
  } catch {
    return
  }
  muteAllBusy.value = true
  try {
    const r = await moderateHall({ action: 'mute_all' })
    const affected = Number(r?.data?.affected)
    ElMessage.success(Number.isInteger(affected) && affected >= 0 ? '已全员静音（' + affected + ' 人）' : '已全员静音')
    scheduleCalibrate(1000)
  } catch (e) {
    ElMessage.error(hallErrorMessage(e, '全员静音失败，请稍后重试'))
  } finally {
    muteAllBusy.value = false
  }
}

function openKick(row) {
  kickTarget.value = row
  kickVisible.value = true
}

async function confirmKick(minutes) {
  const row = kickTarget.value
  if (!row || kickBusy.value) return
  kickBusy.value = true
  try {
    if (await moderate('kick', row, { minutes })) kickVisible.value = false
  } finally {
    kickBusy.value = false
  }
}

function openBlock(row) {
  if (!row?.vid) {
    ElMessage.warning('尚未获取到该访客的信息，请稍后重试')
    return
  }
  blockTarget.value = { visitor_id: row.vid, identifier: row.name }
  blockVisible.value = true
}

function onBlocked() {
  scheduleCalibrate(500)
}

// 在新标签页打开在线会话并定位该访客（本页保持在大厅里继续收听）
function openPrivateChat(row) {
  const nonce = createJumpTicket(row?.vid)
  if (!nonce) {
    ElMessage.warning('无法定位该访客的私聊会话，请稍后重试')
    return
  }
  const href = router.resolve({ path: '/console', query: { open: nonce } }).href
  window.open(href, '_blank', 'noopener')
}

async function manualRefresh() {
  await refreshState({ force: true, notify: true })
}

async function loadSoundPref() {
  if (session.agent?.role !== 'admin') return
  try {
    const r = await http.get('/admin/settings', { silent: true })
    agentSound = r?.data?.agent_notify_sound || 'agent1'
  } catch { /* 使用默认提示音 */ }
}

function onVisibility() {
  if (document.visibilityState === 'visible' && Date.now() - lastStateAt > CALIBRATE_INTERVAL_MS) refreshState()
}

// ---------- 生命周期 ----------
onMounted(() => {
  try {
    tabs = new HallTabCoordinator({ agentID: session.agent?.id, onTakeover: onTakenOver })
    tabs.start()
  } catch {
    tabs = null // 不支持密码学随机数的环境无法做多标签协调，LiveKit 的重复身份踢出仍兜底
  }
  unregisterLeave = registerHallLeaveHandler((reason) => leaveHall(reason))
  startWS()
  loadSwitches()
  refreshState({ force: true, notify: true })
  loadLatestMessages({ initial: true })
  loadSoundPref()
  calibrateTimer = setInterval(() => {
    refreshState()
    // 开关读取失败过（例如网络抖动）时随校准一起重试，直到成功
    if (!switchesReady.value || switchesError.value) loadSwitches()
  }, CALIBRATE_INTERVAL_MS)
  document.addEventListener('visibilitychange', onVisibility)
})

onBeforeRouteLeave(async (to) => {
  if (!client || to.path === '/login') return true
  try {
    await ElMessageBox.confirm('离开本页面将退出大厅（不再收听和发言），确定离开？', '离开大厅', {
      type: 'warning', confirmButtonText: '离开', cancelButtonText: '留在大厅',
    })
    return true
  } catch {
    return false
  }
})

onBeforeUnmount(() => {
  // 先发 hall_unsubscribe（leaveHall 的同步部分），再关闭 WS
  leaveHall('unmount')
  unmounted = true
  ws?.stop()
  ws = null
  tabs?.stop()
  unregisterLeave?.()
  clearInterval(calibrateTimer)
  clearTimeout(calibrateDebounce)
  clearTimeout(renderTimer)
  clearTimeout(switchesReloadTimer)
  for (const t of ackTimers.values()) clearTimeout(t)
  ackTimers.clear()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <el-space direction="vertical" alignment="stretch" fill :size="16" style="width: 100%">
    <HallSwitchPanel
      :switches="panelSwitches"
      :busy="switchBusy"
      :updated-by="switchMeta.updatedBy"
      :updated-at="switchMeta.updatedAt"
      :ready="switchesReady"
      :loading="switchesLoading"
      :error="switchesError"
      @change="changeSwitch"
      @reload="loadSwitches({ notify: true })" />

    <el-card shadow="never">
      <template #header>
        <el-row justify="space-between" align="middle">
          <el-space :size="8" wrap>
            <el-icon :size="18"><component :is="hallIcons.users" /></el-icon>
            <el-text tag="b" size="large">多人大厅</el-text>
            <el-tag :type="phaseTag.type">{{ phaseTag.text }}</el-tag>
            <el-tag v-if="!settings.enabled" type="danger" effect="plain">大厅已关闭</el-tag>
            <el-tag :type="settings.videoEnabled ? 'success' : 'info'" effect="plain">视频{{ settings.videoEnabled ? '已开启' : '未开启' }}</el-tag>
          </el-space>
          <el-space :size="8" wrap>
            <el-button v-if="!inHall || phase === 'joining'" type="primary" :icon="hallIcons.enter" :loading="phase === 'joining'" :disabled="!settings.enabled" @click="enterHall">进入大厅</el-button>
            <el-button v-else :icon="hallIcons.leave" :loading="phase === 'leaving'" @click="confirmLeave">离开大厅</el-button>
            <el-tooltip :disabled="voiceActive || micOn" :content="HALL_SWITCH_TEXT.voicePaused" placement="bottom">
              <span>
                <el-button :type="micOn ? 'success' : 'default'" :icon="micOn ? hallIcons.mic : hallIcons.micOff" :disabled="!isConnected || (!voiceActive && !micOn)" :loading="micBusy" @click="toggleMic">{{ micOn ? '关闭麦克风' : '打开麦克风' }}</el-button>
              </span>
            </el-tooltip>
            <el-tooltip :disabled="settings.videoEnabled || cameraOn" :content="HALL_SWITCH_TEXT.videoOff" placement="bottom">
              <span>
                <el-button :type="cameraOn ? 'success' : 'default'" :icon="cameraOn ? hallIcons.camera : hallIcons.cameraOff" :disabled="!isConnected || (!settings.videoEnabled && !cameraOn)" :loading="cameraBusy" @click="toggleCamera">{{ cameraOn ? '关闭摄像头' : '打开摄像头' }}</el-button>
              </span>
            </el-tooltip>
            <el-button type="danger" plain :icon="hallIcons.volumeOff" :loading="muteAllBusy" :disabled="!settings.enabled" @click="muteAll">全员静音</el-button>
            <el-button :icon="hallIcons.refresh" :loading="stateLoading" @click="manualRefresh">刷新</el-button>
          </el-space>
        </el-row>
      </template>
      <el-row :gutter="16">
        <el-col :span="8">
          <el-statistic title="在线人数" :value="counts.participants">
            <template #suffix>/ {{ settings.maxParticipants }}</template>
          </el-statistic>
        </el-col>
        <el-col :span="8">
          <el-statistic title="开麦数" :value="counts.speakers">
            <template #suffix>（访客 {{ counts.visitorSpeakers }} / {{ settings.maxSpeakers }}）</template>
          </el-statistic>
        </el-col>
        <el-col :span="8">
          <el-statistic title="摄像头数" :value="counts.cameras">
            <template #suffix>/ {{ settings.maxCameras }}</template>
          </el-statistic>
        </el-col>
      </el-row>
    </el-card>

    <el-alert v-if="notice" :type="notice.type" :title="notice.title" :description="notice.description" show-icon @close="notice = null" />
    <el-alert v-if="!settings.enabled" type="error" show-icon :closable="false" title="大厅已关闭" description="大厅总开关已关闭：访客窗口不显示大厅、只保留私聊，已在线的人已被移出。可在上方「大厅开关」中重新打开。" />
    <el-alert v-else-if="!inHall" type="info" show-icon :closable="false" title="你尚未进入大厅" description="下方名单与消息可直接查看和管理；点击「进入大厅」后可实时收听、发言、查看视频并实时接收大厅消息。" />
    <el-alert v-if="settings.enabled && !settings.voiceEnabled" type="warning" show-icon :closable="false" :title="HALL_SWITCH_TEXT.voicePaused" description="所有人的麦克风已关闭，访客不能开麦；任一客服在上方「大厅开关」中打开语音后即可恢复。" />
    <el-alert v-if="degraded" type="warning" show-icon :closable="false" title="语音服务暂不可用" description="当前名单来自服务器缓存，麦克风与摄像头状态为推断值；服务恢复后会自动更新。" />
    <el-alert v-if="audioBlocked && isConnected" type="warning" show-icon :closable="false" title="浏览器阻止了自动播放，暂时听不到大厅声音">
      <el-button type="warning" size="small" :icon="hallIcons.volume" @click="unlockAudio">点击开启声音</el-button>
    </el-alert>

    <el-row :gutter="16">
      <el-col :xs="24" :lg="16">
        <el-space direction="vertical" alignment="stretch" fill :size="16" style="width: 100%">
          <HallParticipantTable
            :rows="rows"
            :pending="pending"
            :loading="stateLoading"
            :connected="isConnected"
            :is-admin="isAdmin"
            @mute="(row) => moderate('mute', row)"
            @camera-off="(row) => moderate('camera_off', row)"
            @kick="openKick"
            @block="openBlock"
            @chat="openPrivateChat" />
          <el-card v-if="showVideoCard" shadow="never">
            <template #header>
              <el-row justify="space-between" align="middle">
                <el-space :size="8">
                  <el-icon><component :is="hallIcons.camera" /></el-icon>
                  <el-text tag="b">视频</el-text>
                </el-space>
                <el-text size="small" type="info">{{ videoHint }}</el-text>
              </el-row>
            </template>
            <el-empty v-if="!videoTiles.length" :image-size="72" description="当前没有人开摄像头" />
            <el-space v-else wrap fill :fill-ratio="45" :size="12" style="width: 100%">
              <HallVideoTile v-for="t in videoTiles" :key="t.identity" :track="t.track" :name="t.name" :is-local="t.isLocal" />
            </el-space>
          </el-card>
        </el-space>
      </el-col>
      <el-col :xs="24" :lg="8">
        <HallMessagePanel
          v-model:draft="draft"
          :messages="messages"
          :connected="isConnected"
          :realtime="hallSubscribed"
          :has-more="hasMoreOlder"
          :loading="messagesLoading"
          :loading-older="loadingOlder"
          :limit-reached="limitReached"
          :uploading="uploading"
          :chat-enabled="settings.chatEnabled"
          @load-older="loadOlder"
          @delete="confirmDelete"
          @retry="retryMessage"
          @send="sendDraft"
          @upload="uploadAndSend" />
      </el-col>
    </el-row>
  </el-space>
  <HallKickDialog v-model="kickVisible" :target="kickTarget" :loading="kickBusy" @confirm="confirmKick" />
  <BlockVisitorDialog v-model="blockVisible" :visitor="blockTarget" @done="onBlocked" />
</template>
