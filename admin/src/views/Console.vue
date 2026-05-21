<script setup>
import { onMounted, onUnmounted, ref, nextTick } from 'vue'
import { ElMessage } from 'element-plus'
import dayjs from 'dayjs'
import http from '../api/http'
import { AgentWS } from '../api/ws'
import { useSession } from '../store/session'

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
  const r = await http.get(`/agent/conversations/${convID}/messages?limit=80`)
  messages.value = (r.data || []).slice().reverse()
  await nextTick()
  scrollToBottom()
}

async function pickConv(c) {
  activeConv.value = c
  await loadMessages(c.id)
  await http.post(`/agent/conversations/${c.id}/assign`)
  await http.post(`/agent/conversations/${c.id}/read`)
  // 服务端只会把 conv 内的实时消息推过来，我们这里手动重置未读
  c.unread = 0
}

function sendText() {
  const text = (draft.value || '').trim()
  if (!text || !activeConv.value) return
  if (!ws?.alive) {
    ElMessage.warning('与服务器断开，尝试重连中…')
    return
  }
  sending.value = true
  ws.send({
    type: 'chat',
    conv: activeConv.value.id,
    content: text,
    ts: Date.now(),
    prio: 0
  })
  // 本地立即回显（服务端确认后会有真实回包，这里乐观渲染）
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

function fmtTime(t) {
  return dayjs(t).format('YYYY-MM-DD HH:mm:ss')
}

function isMine(m) {
  return m.sender === 'agent' && String(m.sender_ref) === String(session.agent?.id)
}

function mediaURL(m) {
  if (m.media_url?.Valid) return m.media_url.String
  if (m.media_url) return m.media_url
  return ''
}

async function refreshStats() {
  try {
    const h = await http.get('/health')
    onlineStats.value = { visitors: h.visitors, agents: h.agents }
  } catch {}
}

onMounted(async () => {
  await refreshConvs()
  await refreshStats()
  // WSS 连接
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new AgentWS({
    url: `${proto}://${location.host}/ws/agent`,
    token: session.token,
    onMessage: (env) => {
      // 实时消息处理（爷爷需求：WSS 通道优先）
      if (env.type === 'chat') {
        if (activeConv.value && env.conv === activeConv.value.id) {
          messages.value.push({
            id: env.id,
            sender: env.from?.startsWith('agent:') ? 'agent' : 'visitor',
            sender_ref: env.from?.split(':')[1] || '',
            content: env.content || '',
            media_url: env.media ? { String: env.media, Valid: true } : null,
            media_kind: env.mkind ? { String: env.mkind, Valid: true } : null,
            media_name: env.mname ? { String: env.mname, Valid: true } : null,
            created_at: new Date(env.ts || Date.now()).toISOString()
          })
          nextTick(scrollToBottom)
        } else {
          // 其他会话有新消息：增加未读 + 刷新列表
          refreshConvs()
        }
      } else if (env.type === 'sys') {
        refreshConvs()
      }
    }
  })
  ws.start()
  // 定时刷新统计
  refreshTimer = setInterval(refreshStats, 15000)
  convsTimer = setInterval(refreshConvs, 20000)
})

let refreshTimer, convsTimer
onUnmounted(() => {
  ws?.stop()
  clearInterval(refreshTimer)
  clearInterval(convsTimer)
})
</script>

<template>
  <el-container style="height:calc(100vh - 100px)">
    <!-- 左：会话列表 -->
    <el-aside width="320px" style="border-right:1px solid #e6e6e6;background:#fff">
      <div style="padding:10px;border-bottom:1px solid #e6e6e6">
        <el-tag type="success">在线访客 {{ onlineStats.visitors }}</el-tag>
        <el-tag type="primary" style="margin-left:8px">在线客服 {{ onlineStats.agents }}</el-tag>
      </div>
      <div style="overflow-y:auto;height:calc(100% - 50px)">
        <div
          v-for="c in convs" :key="c.id"
          @click="pickConv(c)"
          :style="{
            padding:'12px',
            cursor:'pointer',
            borderBottom:'1px solid #f0f0f0',
            background: activeConv?.id === c.id ? '#ecf5ff' : 'transparent'
          }">
          <div style="display:flex;justify-content:space-between">
            <strong>{{ c.identifier || ('访客 ' + c.visitor_id.slice(0, 6)) }}</strong>
            <el-badge v-if="c.unread > 0" :value="c.unread" :max="99" />
          </div>
          <div style="font-size:12px;color:#909399;margin-top:4px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
            {{ c.country || '' }} {{ c.city || '' }} · 来自 {{ c.referer || '直接访问' }}
          </div>
          <div style="font-size:12px;color:#c0c4cc;margin-top:2px">
            {{ fmtTime(c.updated_at) }}
          </div>
        </div>
        <el-empty v-if="!convs.length" description="暂无进行中的会话" />
      </div>
    </el-aside>

    <!-- 右：聊天主区 -->
    <el-container>
      <el-header v-if="activeConv" style="background:#fff;border-bottom:1px solid #e6e6e6;display:flex;align-items:center;justify-content:space-between">
        <div>
          {{ activeConv.identifier || ('访客 ' + activeConv.visitor_id.slice(0, 6)) }}
          ·
          <span style="font-size:12px;color:#909399">
            {{ activeConv.country || '' }} {{ activeConv.city || '' }} ·
            最近停留：{{ activeConv.last_page || '-' }}
          </span>
        </div>
      </el-header>

      <el-main id="msg-list" style="background:#fafafa">
        <el-empty v-if="!activeConv" description="请从左侧选择一个会话" />
        <div v-else>
          <div v-for="m in messages" :key="m.id"
               :style="{display:'flex', justifyContent: isMine(m) ? 'flex-end' : 'flex-start', margin:'10px 0'}">
            <div :style="{
              maxWidth:'70%',
              padding:'10px 12px',
              borderRadius:'8px',
              background: isMine(m) ? '#409EFF' : '#fff',
              color: isMine(m) ? '#fff' : '#303133',
              border: isMine(m) ? 'none' : '1px solid #e6e6e6'
            }">
              <div v-if="mediaURL(m)">
                <img v-if="(m.media_kind?.String || m.media_kind) === 'image'" :src="mediaURL(m)" style="max-width:240px;border-radius:4px" />
                <a v-else :href="mediaURL(m)" target="_blank" :style="{color: isMine(m) ? '#fff' : '#409EFF'}">
                  附件：{{ m.media_name?.String || m.media_name }}
                </a>
              </div>
              <div v-if="m.content" style="white-space:pre-wrap;word-break:break-word">{{ m.content }}</div>
              <div :style="{fontSize:'11px', opacity:.7, marginTop:'4px', textAlign:'right'}">{{ fmtTime(m.created_at) }}</div>
            </div>
          </div>
        </div>
      </el-main>

      <el-footer v-if="activeConv" style="background:#fff;border-top:1px solid #e6e6e6;height:auto;padding:10px">
        <el-input v-model="draft" type="textarea" :rows="3" placeholder="输入消息，Enter 发送，Shift+Enter 换行"
                  @keydown.enter.exact.prevent="sendText" resize="none" />
        <div style="margin-top:6px;display:flex;justify-content:space-between;align-items:center">
          <div>
            <el-button @click="fileInput.click()">附件 / 图片</el-button>
            <input ref="fileInput" type="file" style="display:none" @change="pickFile" />
          </div>
          <el-button type="primary" :loading="sending" @click="sendText">发送</el-button>
        </div>
      </el-footer>
    </el-container>
  </el-container>
</template>
