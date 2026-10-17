<script setup>
// 大厅消息区：最近 50 条 + 实时、向上加载更早、删除（由大厅页二次确认）、发送文字 / 图片 / 文件。
// 内容一律文本插值渲染（无 v-html），媒体地址已在 modules/hallMessages.js 中按白名单校验。
import { nextTick, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  HALL_FILE_EXTENSIONS,
  HALL_IMAGE_EXTENSIONS,
  HALL_MESSAGE_MAX_CHARS,
  HALL_MESSAGE_MEMORY_LIMIT,
  checkHallUploadFile,
  contentLines,
  formatFileSize,
  hallSenderLabel,
} from '../../modules/hallMessages'
import { formatMsgTime } from '../../modules/timeFormat'
import { hallIcons } from './hallIcons'

const props = defineProps({
  messages: { type: Array, default: () => [] },
  connected: { type: Boolean, default: false },
  realtime: { type: Boolean, default: false },
  hasMore: { type: Boolean, default: false },
  loading: { type: Boolean, default: false },
  loadingOlder: { type: Boolean, default: false },
  limitReached: { type: Boolean, default: false },
  uploading: { type: Boolean, default: false },
})
const draft = defineModel('draft', { type: String, default: '' })
const emit = defineEmits(['load-older', 'delete', 'retry', 'send', 'upload'])

const scrollRef = ref(null)
let nearBottom = true
const imageAccept = HALL_IMAGE_EXTENSIONS.map(e => '.' + e).join(',')
const fileAccept = [...HALL_IMAGE_EXTENSIONS, ...HALL_FILE_EXTENSIONS].map(e => '.' + e).join(',')

function wrapEl() {
  return scrollRef.value?.wrapRef || null
}

function scrollToBottom() {
  const wrap = wrapEl()
  if (wrap) scrollRef.value.setScrollTop(wrap.scrollHeight)
  nearBottom = true
}

function onScroll({ scrollTop }) {
  const wrap = wrapEl()
  if (!wrap) return
  nearBottom = wrap.scrollHeight - scrollTop - wrap.clientHeight < 80
  if (scrollTop < 24 && props.hasMore && !props.loadingOlder && !props.limitReached) emit('load-older')
}

// 消息变化：向上翻页时保持阅读位置；新消息到达且原本在底部（或是自己刚发的）时滚到底部
watch(() => props.messages, async (next, prev) => {
  const wrap = wrapEl()
  const prevList = Array.isArray(prev) ? prev : []
  const prevFirst = prevList[0]?.id
  const prevLast = prevList[prevList.length - 1]?.id
  const nextLast = next[next.length - 1]
  const prepended = prevList.length > 0 && next[0]?.id !== prevFirst && next.some(m => m.id === prevFirst) && nextLast?.id === prevLast
  const prevHeight = wrap ? wrap.scrollHeight : 0
  const prevTop = wrap ? wrap.scrollTop : 0
  const wasNear = nearBottom
  await nextTick()
  if (!wrapEl()) return
  if (prepended) {
    scrollRef.value.setScrollTop(wrapEl().scrollHeight - prevHeight + prevTop)
  } else if (!prevList.length || wasNear || (nextLast && nextLast.id !== prevLast && nextLast.status === 'sending')) {
    scrollToBottom()
  }
})

function onKeydown(e) {
  // 输入法组字过程中的回车不发送
  if (e.key !== 'Enter' || e.shiftKey || e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  emit('send')
}

function beforeUpload(file, kind) {
  const check = checkHallUploadFile(file, kind)
  if (!check.ok) {
    ElMessage.warning(check.error)
    return false
  }
  return true
}

function uploadRequest(kind) {
  return (options) => {
    emit('upload', options.file, kind)
    return Promise.resolve()
  }
}

defineExpose({ scrollToBottom })
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <el-row justify="space-between" align="middle">
        <el-space :size="8">
          <el-icon><component :is="hallIcons.chat" /></el-icon>
          <el-text tag="b">大厅消息</el-text>
        </el-space>
        <el-text size="small" :type="connected && !realtime ? 'warning' : 'info'">{{ connected ? (realtime ? '实时接收中' : '正在连接实时消息…') : '进入大厅后实时接收' }}</el-text>
      </el-row>
    </template>
    <el-scrollbar ref="scrollRef" height="460px" @scroll="onScroll">
      <el-space v-loading="loading" direction="vertical" alignment="stretch" fill :size="12" style="width: 100%">
        <el-row justify="center">
          <el-button v-if="hasMore && !limitReached" link type="primary" size="small" :loading="loadingOlder" @click="emit('load-older')">加载更早的消息</el-button>
          <el-text v-else-if="limitReached" size="small" type="info">已显示最近 {{ HALL_MESSAGE_MEMORY_LIMIT }} 条消息</el-text>
          <el-text v-else-if="messages.length" size="small" type="info">没有更早的消息了</el-text>
        </el-row>
        <el-empty v-if="!messages.length && !loading" :image-size="72" description="暂无大厅消息" />
        <div v-for="m in messages" :key="m.id" :data-hall-msg-id="m.id">
          <el-row justify="space-between" align="middle">
            <el-space :size="6" wrap>
              <el-tag size="small" :type="m.sender === 'system' ? 'warning' : (m.role === 'agent' ? 'primary' : 'info')">{{ hallSenderLabel(m) }}</el-tag>
              <el-text tag="b" size="small">{{ m.name }}</el-text>
              <el-text size="small" type="info">{{ formatMsgTime(m.createdAt) }}</el-text>
              <el-tag v-if="m.status === 'sending'" size="small" type="info" effect="plain">发送中</el-tag>
              <el-tag v-else-if="m.status === 'failed'" size="small" type="danger" effect="plain">发送失败</el-tag>
            </el-space>
            <el-space :size="4">
              <el-button v-if="m.status === 'failed'" link size="small" type="primary" :disabled="!connected" @click="emit('retry', m)">重发</el-button>
              <el-button v-if="m.status === 'sent'" link size="small" type="danger" :icon="hallIcons.trash" @click="emit('delete', m)">删除</el-button>
            </el-space>
          </el-row>
          <el-text v-if="m.content" tag="p">
            <template v-for="(line, i) in contentLines(m.content)" :key="i"><br v-if="i" />{{ line }}</template>
          </el-text>
          <el-image
            v-if="m.mediaUrl && m.mediaKind === 'image'"
            :src="m.mediaUrl"
            :preview-src-list="[m.mediaUrl]"
            preview-teleported
            lazy
            fit="cover"
            style="width: 160px; height: 120px" />
          <el-link v-else-if="m.mediaUrl" :href="m.mediaUrl" target="_blank" rel="noopener noreferrer" type="primary" :icon="hallIcons.attach">
            {{ m.mediaName }}<template v-if="m.mediaSize">（{{ formatFileSize(m.mediaSize) }}）</template>
          </el-link>
        </div>
      </el-space>
    </el-scrollbar>
    <template #footer>
      <el-space direction="vertical" alignment="stretch" fill :size="8" style="width: 100%">
        <el-input
          v-model="draft"
          type="textarea"
          :rows="3"
          resize="none"
          :maxlength="HALL_MESSAGE_MAX_CHARS"
          show-word-limit
          :disabled="!connected"
          :placeholder="connected ? '输入消息，回车发送，Shift+回车换行' : '进入大厅后才能发送消息'"
          @keydown="onKeydown" />
        <el-row justify="space-between" align="middle">
          <el-space :size="8">
            <el-upload :show-file-list="false" :accept="imageAccept" :disabled="!connected || uploading" :before-upload="(f) => beforeUpload(f, 'image')" :http-request="uploadRequest('image')">
              <el-button :icon="hallIcons.image" :disabled="!connected || uploading">图片</el-button>
            </el-upload>
            <el-upload :show-file-list="false" :accept="fileAccept" :disabled="!connected || uploading" :before-upload="(f) => beforeUpload(f, 'file')" :http-request="uploadRequest('file')">
              <el-button :icon="hallIcons.attach" :disabled="!connected || uploading">文件</el-button>
            </el-upload>
            <el-text v-if="uploading" size="small" type="info">上传中…</el-text>
          </el-space>
          <el-button type="primary" :icon="hallIcons.send" :disabled="!connected" @click="emit('send')">发送</el-button>
        </el-row>
      </el-space>
    </template>
  </el-card>
</template>
