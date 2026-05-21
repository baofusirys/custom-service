<script setup>
import { onMounted, onUnmounted, ref, nextTick, computed } from 'vue'
import { ElMessage } from 'element-plus'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'
import http from '../api/http'
import { AgentWS } from '../api/ws'
import { useSession } from '../store/session'

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

const session = useSession()
const convs = ref([])
const activeConv = ref(null)
const messages = ref([])
const draft = ref('')
const onlineStats = ref({ visitors: 0, agents: 0 })
const fileInput = ref(null)
const sending = ref(false)
let ws = null

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
  activeConv.value = c
  await loadMessages(c.id)
  await http.post(`/agent/conversations/${c.id}/assign`)
  await http.post(`/agent/conversations/${c.id}/read`)
  c.unread = 0
}

function sendText() {
  const text = (draft.value || '').trim()
  if (!text || !activeConv.value) return
  if (!ws?.alive) {
    ElMessage.warning('与服务器断开，正在重连')
    return
  }
  sending.value = true
  ws.send({ type: 'chat', conv: activeConv.value.id, content: text, ts: Date.now(), prio: 0 })
  messages.value.push({
    id: 'local-' + Date.now(),
    sender: 'agent',
    sender_ref: String(session.agent?.id),
    content: text,
    created_at: new Date().toISOString()
  })
  draft.value = ''
  sending.value = false
  nextTick(scrollToBottom)
}

async function pickFile(e) {
  if (!activeConv.value) return
  const file = e.target.files?.[0]
  if (!file) return
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
  e.target.value = ''
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

function lastMsgPreview(c) {
  // 简化：服务端 list 接口暂未返回最后一条；用更新时间提示
  return '最近活动 · ' + fmtGroupTime(c.updated_at)
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
  await refreshConvs()
  await refreshStats()
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new AgentWS({
    url: `${proto}://${location.host}/ws/agent`,
    token: session.token,
    onMessage: (env) => {
      if (env.type === 'chat') {
        // 自己发的回声：服务端会把客服自己发的消息也回推；本地已乐观渲染，跳过避免重复
        const fromAgent = env.from && env.from.startsWith('agent:')
        const isMyOwn = fromAgent && String(env.from.split(':')[1]) === String(session.agent?.id)
        if (isMyOwn) return

        const inCurrent = activeConv.value && env.conv === activeConv.value.id
        if (inCurrent) {
          messages.value.push({
            id: env.id,
            sender: fromAgent ? 'agent' : 'visitor',
            sender_ref: env.from?.split(':')[1] || '',
            content: env.content || '',
            media_url: env.media ? { String: env.media, Valid: true } : null,
            media_kind: env.mkind ? { String: env.mkind, Valid: true } : null,
            media_name: env.mname ? { String: env.mname, Valid: true } : null,
            created_at: new Date(env.ts || Date.now()).toISOString()
          })
          nextTick(scrollToBottom)
          // 当前会话有新消息：静默把未读清零（服务端持久化）
          http.post(`/agent/conversations/${env.conv}/read`).catch(() => {})
        } else {
          // 非当前会话的新消息：WSS 实时本地更新未读，0 延迟，不走 HTTP 轮询
          const conv = convs.value.find(x => x.id === env.conv)
          if (conv) {
            conv.unread = (conv.unread || 0) + 1
            conv.updated_at = new Date(env.ts || Date.now()).toISOString()
            // 自动上浮到列表顶部（最新活动）
            const idx = convs.value.indexOf(conv)
            if (idx > 0) {
              convs.value.splice(idx, 1)
              convs.value.unshift(conv)
            }
          } else {
            // 全新访客（不在当前列表）：触发一次防抖刷新
            scheduleConvsRefresh()
          }
        }
      } else if (env.type === 'sys') {
        scheduleConvsRefresh()
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
})
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
                <span class="conv-preview">{{ [c.country, c.city].filter(Boolean).join(' · ') || lastMsgPreview(c) }}</span>
                <el-badge v-if="c.unread > 0" :value="c.unread" :max="99" />
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
            </div>
          </div>
        </div>
      </el-header>

      <el-main id="msg-list" class="msg-main">
        <el-empty v-if="!activeConv" description="请从左侧选择一个会话开始服务" :image-size="120" />
        <template v-else>
          <div v-for="(g, gi) in grouped" :key="gi" class="msg-group">
            <div class="time-divider">
              <span>{{ fmtGroupTime(g.ts) }}</span>
            </div>
            <div class="msg-line" :class="{ 'msg-line--mine': isMineGroup(g) }">
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
                    <img v-if="mediaKind(m) === 'image'" :src="mediaURL(m)" class="bubble-img" />
                    <a v-else :href="mediaURL(m)" target="_blank" class="bubble-file">
                      <el-icon><svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zm-1 7V3.5L18.5 9H13z"/></svg></el-icon>
                      <span>{{ mediaName(m) }}</span>
                    </a>
                  </template>
                  <span v-if="m.content" class="bubble-text">{{ m.content }}</span>
                </div>
              </div>
            </div>
          </div>
        </template>
      </el-main>

      <el-footer v-if="activeConv" class="chat-footer">
        <el-input
          v-model="draft"
          type="textarea"
          :rows="3"
          resize="none"
          placeholder="回车发送，Shift+回车换行"
          @keydown.enter.exact.prevent="sendText" />
        <div class="chat-footer-actions">
          <div>
            <el-button :icon="undefined" plain size="small" @click="fileInput.click()">附件 / 图片</el-button>
            <input ref="fileInput" type="file" style="display:none" @change="pickFile" />
          </div>
          <el-button type="primary" :loading="sending" @click="sendText">发送</el-button>
        </div>
      </el-footer>
    </el-container>
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
  padding: 10px 14px; border-radius: 8px;
  background: #fff; border: 1px solid #e6e6e6; color: #303133;
  word-break: break-word; white-space: pre-wrap; line-height: 1.5;
  font-size: 14px; box-shadow: 0 1px 2px rgba(0,0,0,.03);
}
.bubble--mine { background: #409EFF; color: #fff; border-color: #409EFF; }
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
</style>
