<script setup>
import { onMounted, onUnmounted, ref, nextTick, computed } from 'vue'
import { ElMessage, ElNotification } from 'element-plus'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'
import http from '../api/http'
import { AgentWS } from '../api/ws'
import { playSound, unlockAudio, playRingLoop, stopRingLoop } from '../api/sound'
import { useSession } from '../store/session'

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

const session = useSession()
const convs = ref([])
const activeConv = ref(null)
const messages = ref([])
const draft = ref('')
// [040] 待发送文件队列（粘贴 / 选附件追加；点发送时依次上传）
// 每项：{ file: File, blobUrl: string|null, isImage: bool }
const pendingFiles = ref([])

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
const myConnId = ref('') // 自己 WSS 连接 ID（hello 时拿到，用于多端同步去重）
let ws = null

async function loadSoundPref() {
  // 仅管理员可读全设置；普通客服 fallback 默认值
  if (session.agent?.role !== 'admin') return
  try {
    const r = await http.get('/admin/settings')
    agentSound.value = r.data?.agent_notify_sound || 'agent1'
  } catch {}
}

async function refreshConvs() {
  const r = await http.get('/agent/conversations')
  convs.value = r.data || []
}

async function loadMessages(convID) {
  const r = await http.get(`/agent/conversations/${convID}/messages?limit=100`)
  messages.value = (r.data || []).slice().reverse()
  await nextTick()
  scrollToBottom()
}

async function pickConv(c) {
  // [063] 乐观 UI：未读 badge 立刻消失，不等任何网络 RPC
  // 旧逻辑串行 4 步（loadMessages → assign → unread=0 → sendReadAck），美国服务器 200ms RTT 下
  // 单次点击要 1-2 秒才让红色未读 badge 消失，体感像「卡顿」。
  // 改：unread 先清，DB 操作后台跑；loadMessages 和 assign 是独立 RPC 可并行（少一半串行时间）。
  activeConv.value = c
  c.unread = 0
  // 拉消息 + 接管会话并行（两个 RPC 互相独立，时间 = max 而非 sum）
  await Promise.all([
    loadMessages(c.id),
    http.post(`/agent/conversations/${c.id}/assign`),
  ])
  // assign 后 agent 已加入 byConv[c.id]，立即发 WSS read：
  // 触发后端 UpdateLastRead + FanoutToConv 通知访客「客服已读」
  sendReadAck(c.id)
  // 本地把所有访客消息直接标 read（自己看到了）
  for (const m of messages.value) {
    if (m.sender === 'visitor') m.read = true
  }
}

function sendReadAck(convID) {
  if (ws?.alive && convID) {
    ws.send({ type: 'read', conv: convID, ts: Date.now() })
  }
}

// 标记「自己发过的、created_at <= upToTs 的消息」为已读（被对端读了）
function markMineReadUpTo(upToTs) {
  const myID = String(session.agent?.id)
  for (const m of messages.value) {
    if (m.read) continue
    if (m.sender === 'agent' && String(m.sender_ref) === myID) {
      const mTs = new Date(m.created_at).getTime()
      if (mTs <= upToTs) m.read = true
    }
  }
}

// 该组是否是「页面跳转」横幅（不是普通消息组）
function isPageGroup(g) {
  return g.sender === 'sys' && g.items.length > 0 &&
    typeof g.items[0].sender_ref === 'string' &&
    g.items[0].sender_ref.indexOf('page:') === 0
}

function pageURL(m) {
  return m.page_url || (m.sender_ref && m.sender_ref.indexOf('page:') === 0 ? m.sender_ref.slice(5) : '')
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

// 自己发的最新一条消息（用于在它下方显示「已读」角标，不在每组都显示）
const lastMineMsg = computed(() => {
  const myID = String(session.agent?.id)
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const m = messages.value[i]
    if (m.sender === 'agent' && String(m.sender_ref) === myID) return m
  }
  return null
})

// [040] 发送：先发文本（如果有），再依次上传 pendingFiles 队列里的所有附件
async function sendText() {
  if (!activeConv.value) return
  const text = (draft.value || '').trim()
  const files = pendingFiles.value.map(it => it.file)
  if (!text && files.length === 0) return
  if (!ws?.alive) {
    ElMessage.warning('与服务器断开，正在重连')
    return
  }
  sending.value = true
  try {
    if (text) {
      const now = new Date().toISOString()
      ws.send({ type: 'chat', conv: activeConv.value.id, content: text, ts: Date.now(), prio: 0 })
      messages.value.push({
        id: 'local-' + Date.now(),
        sender: 'agent',
        sender_ref: String(session.agent?.id),
        content: text,
        created_at: now
      })
      activeConv.value.last_message = { sender: 'agent', content: text, created_at: now }
      activeConv.value.updated_at = now
      draft.value = ''
    }
    if (files.length > 0) {
      // 先清 UI 防重复点；上传失败 uploadAndSendFile 内部静默（catch {}）
      clearAllPending()
      for (const f of files) {
        await uploadAndSendFile(f)
      }
    }
  } finally {
    sending.value = false
    nextTick(scrollToBottom)
  }
}

// 公共上传函数，附件按钮和粘贴事件共用
async function uploadAndSendFile(file) {
  if (!activeConv.value || !file) return
  const fd = new FormData()
  fd.append('file', file)
  fd.append('uploader', 'agent')
  fd.append('conv_id', activeConv.value.id)
  try {
    const r = await http.post('/upload', fd, { headers: { 'Content-Type': 'multipart/form-data' } })
    ws?.send({
      type: 'chat', conv: activeConv.value.id,
      content: '', media: r.url, mkind: r.kind, mname: r.name, msize: r.size,
      ts: Date.now(), prio: 0
    })
    messages.value.push({
      id: 'local-' + Date.now(), sender: 'agent', sender_ref: String(session.agent?.id),
      content: '',
      media_url: { String: r.url, Valid: true },
      media_kind: { String: r.kind, Valid: true },
      media_name: { String: r.name, Valid: true },
      created_at: new Date().toISOString()
    })
    nextTick(scrollToBottom)
  } catch {}
}

// [040] 选附件：multiple 支持，所有文件追加到 pendingFiles 队列
function pickFile(e) {
  const files = e.target.files
  if (files && files.length) {
    for (const f of files) addPendingFile(f)
  }
  e.target.value = ''
}

// ============= [040] pendingFiles 队列管理 =============
function fmtBytes(n) {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

function addPendingFile(file) {
  if (!file) return
  const isImage = (file.type || '').indexOf('image/') === 0
  pendingFiles.value.push({
    file,
    blobUrl: isImage ? URL.createObjectURL(file) : null,
    isImage,
  })
}

function removePendingFile(item) {
  const idx = pendingFiles.value.indexOf(item)
  if (idx < 0) return
  if (item.blobUrl) {
    try { URL.revokeObjectURL(item.blobUrl) } catch {}
  }
  pendingFiles.value.splice(idx, 1)
}

function clearAllPending() {
  for (const it of pendingFiles.value) {
    if (it.blobUrl) {
      try { URL.revokeObjectURL(it.blobUrl) } catch {}
    }
  }
  pendingFiles.value = []
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
function onPasteDraft(e) {
  const items = e.clipboardData?.items
  if (!items) return
  let hadFile = false
  for (const it of items) {
    if (it.kind === 'file') {
      const f = it.getAsFile()
      if (f) { addPendingFile(f); hadFile = true }
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
  if (m.media_url?.Valid) return m.media_url.String
  if (typeof m.media_url === 'string') return m.media_url
  return ''
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

function fmtAbs(t) { return dayjs(t).format('YYYY-MM-DD HH:mm:ss') }
function fmtHM(t) { return dayjs(t).format('HH:mm') }
function fmtGroupTime(t) {
  const d = dayjs(t)
  const today = dayjs().startOf('day')
  if (d.isAfter(today)) return '今天 ' + d.format('HH:mm')
  const yesterday = today.subtract(1, 'day')
  if (d.isAfter(yesterday)) return '昨天 ' + d.format('HH:mm')
  return d.format('MM-DD HH:mm')
}

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
    refreshConvs()
  }, 3000)
}

let convsTimer
onMounted(async () => {
  await Promise.all([refreshConvs(), refreshStats(), loadSoundPref()])
  // 解锁 AudioContext（Chrome 等需要用户手势）—— 用户既然能进到 console 页就算手势
  document.addEventListener('click', unlockAudio, { once: true, capture: true })
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new AgentWS({
    url: `${proto}://${location.host}/ws/agent`,
    token: session.token,
    onMessage: (env) => {
      // hello：记住自己的 connID（多端同步去重必需）
      if (env.type === 'hello') {
        myConnId.value = env.extra?.conn_id || ''
        return
      }

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
          markMineReadUpTo(env.ts || Date.now())
        }
        return
      }
      if (env.type === 'sys') {
        // 系统通知：访客进入提醒（弹 toast + 播声 + 刷会话列表）
        if (env.extra?.kind === 'visitor_enter') {
          ElNotification({
            title: '访客进入',
            message: env.content || '有新访客进入网站',
            type: 'info',
            duration: 4500,
            position: 'bottom-right'
          })
          playSound(agentSound.value)
        }
        scheduleConvsRefresh()
        return
      }
      if (env.type !== 'chat') return

      const fromAgent = env.from && env.from.startsWith('agent:')
      const fromVisitor = env.from && env.from.startsWith('visitor:')
      const fromSys = env.from === 'sys'
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
        messages.value.push({
          id: env.id,
          sender: fromAgent ? 'agent' : (fromSys ? 'sys' : 'visitor'),
          sender_ref: senderRef,
          content: env.content || '',
          media_url: env.media ? { String: env.media, Valid: true } : null,
          media_kind: env.mkind ? { String: env.mkind, Valid: true } : null,
          media_name: env.mname ? { String: env.mname, Valid: true } : null,
          created_at: new Date(env.ts || Date.now()).toISOString(),
          // 给前端渲染用：标记是否是页面跳转 + URL
          page_url: isPageNav ? env.extra.url : '',
          page_title: isPageNav ? env.extra.title : ''
        })
        nextTick(scrollToBottom)
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
          sendReadAck(env.conv)
          playSound(agentSound.value)
        }
        return
      }

      // 非当前会话的新消息：本地实时更新（WSS，0 延迟）
      const conv = convs.value.find(x => x.id === env.conv)
      if (conv) {
        // 只有访客的消息才 +1 未读 + 播声；其他客服/sys 发的只更新活动时间 + 上浮
        if (fromVisitor) {
          conv.unread = (conv.unread || 0) + 1
          playSound(agentSound.value)
        }
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
      } else if (fromVisitor || fromSys) {
        // 全新访客（或新会话的系统问候）—— 会话还没在客服当前列表里，触发一次防抖刷新
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
  clearInterval(convsTimer)
  if (convsDebounce) clearTimeout(convsDebounce)
  if (voice.timer) { clearTimeout(voice.timer); clearInterval(voice.timer) }
  if (voice.pc) { try { voice.pc.close() } catch {} }
  if (voice.localStream) voice.localStream.getTracks().forEach(t => t.stop())
})

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
    <el-aside width="320px" class="aside">
      <div class="aside-header">
        <el-row :gutter="8">
          <el-col :span="12">
            <el-statistic title="在线访客" :value="onlineStats.visitors" />
          </el-col>
          <el-col :span="12">
            <el-statistic title="在线客服" :value="onlineStats.agents" />
          </el-col>
        </el-row>
      </div>
      <el-divider style="margin:0" />
      <el-scrollbar class="conv-scroll">
        <el-empty v-if="!convs.length" description="暂无进行中的会话" :image-size="80" />
        <template v-else>
          <div
            v-for="c in convs"
            :key="c.id"
            class="conv-item"
            :class="{ 'conv-item--active': activeConv?.id === c.id }"
            @click="pickConv(c)">
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
              <!-- [059] 第 3 行：访客地理位置 + IP（任一非空才显示，省垂直空间）-->
              <div v-if="convGeoIp(c)" class="conv-row3">
                <span class="conv-geoip">📍 {{ convGeoIp(c) }}</span>
              </div>
            </div>
          </div>
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
              {{ dayjs(row.last_seen).format('YYYY-MM-DD HH:mm') }}
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
                <a :href="pageURL(m)" target="_blank" class="page-banner-link"
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
                <div
                  v-for="m in g.items"
                  :key="m.id"
                  class="bubble"
                  :class="{ 'bubble--mine': isMineGroup(g) }"
                  :title="fmtAbs(m.created_at)">
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
                    <a v-else :href="mediaURL(m)" target="_blank" class="bubble-file">
                      <el-icon><svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zm-1 7V3.5L18.5 9H13z"/></svg></el-icon>
                      <span>{{ mediaName(m) }}</span>
                    </a>
                  </template>
                  <span v-if="m.content" class="bubble-text">{{ m.content }}</span>
                  <!-- 复制按钮：文本消息显示。点击复制消息内容到剪贴板 -->
                  <span
                    v-if="m.content || mediaURL(m)"
                    class="bubble-copy"
                    :class="{ 'bubble-copy--mine': isMineGroup(g) }"
                    title="复制"
                    @click.stop="copyMessage(m)">
                    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                  </span>
                </div>
                <!-- 已读角标：仅在「自己最新那条消息已被对方读了」时显示 -->
                <div
                  v-if="isMineGroup(g) && lastMineMsg && lastMineMsg.read && g.items[g.items.length-1].id === lastMineMsg.id"
                  class="read-indicator">
                  已读
                </div>
              </div>
            </div>
          </div>
        </template>
      </el-main>

      <el-footer v-if="activeConv" class="chat-footer">
        <!-- [040] pending 多文件预览队列：粘贴 / 选附件追加到这里，点发送才依次上传 -->
        <div v-if="pendingFiles.length" class="pending-list">
          <div v-for="(it, idx) in pendingFiles" :key="idx" class="pending-chip">
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
            <button class="remove-btn" title="移除" @click="removePendingFile(it)">×</button>
          </div>
        </div>
        <el-input
          v-model="draft"
          type="textarea"
          :rows="3"
          resize="none"
          placeholder="回车发送，Shift+回车换行，粘贴可上传图片/文件（可多张）"
          @keydown.enter.exact.prevent="sendText"
          @paste.native="onPasteDraft" />
        <div class="chat-footer-actions">
          <div>
            <el-button :icon="undefined" plain size="small" @click="fileInput.click()">附件 / 图片</el-button>
            <input ref="fileInput" type="file" multiple style="display:none" @change="pickFile" />
          </div>
          <el-button type="primary" :loading="sending" @click="sendText">发送</el-button>
        </div>
      </el-footer>
    </el-container>

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
