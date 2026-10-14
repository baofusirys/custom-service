<script setup>
// 回收站（仅管理员，契约第 2 节）：剩余天数、多选恢复、多选彻底删除、清空回收站。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listRecycleBin, purgeAllRecycled, purgeRecycled, restoreRecycled } from '../api/recycleBin'
import {
  RECYCLE_BATCH_MAX,
  isValidVisitorId,
  summarizePurgeResult,
  summarizeRestoreResult,
  validateBatchSelection,
  visitorDisplayName,
} from '../modules/conversationOps'
import { remainingLabel, remainingTagType } from '../modules/recycleBin'
import { CONFIRM_PURGE_RECYCLE, matchesConfirmText } from '../modules/dangerConfirm'
import { formatBeijing } from '../modules/timeFormat'

const PAGE_SIZE = 50
const loading = ref(false)
const busy = ref(false)
const list = ref([])
const total = ref(0)
const page = ref(1)
const selection = ref([])
const now = ref(Date.now())
let nowTimer = null
let loadSeq = 0

const selectedIds = computed(() => selection.value.map(r => r.visitor_id))

async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const r = await listRecycleBin({ offset: (page.value - 1) * PAGE_SIZE, limit: PAGE_SIZE })
    if (seq !== loadSeq) return
    list.value = Array.isArray(r?.data) ? r.data.filter(it => it && isValidVisitorId(it.visitor_id)) : []
    total.value = Number(r?.total) > 0 ? Math.trunc(Number(r.total)) : 0
    now.value = Date.now()
    if (!list.value.length && page.value > 1 && total.value > 0) {
      page.value -= 1
      load()
    }
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function onSelectionChange(rows) {
  selection.value = rows
}

async function restoreSelected() {
  const check = validateBatchSelection(selectedIds.value, RECYCLE_BATCH_MAX)
  if (!check.ok) { ElMessage.warning(check.error); return }
  try {
    await ElMessageBox.confirm(`确定恢复选中的 ${check.ids.length} 位客户的聊天记录？恢复后重新出现在会话列表（访客端已清空的本地记录不会恢复）。`, '恢复聊天记录', {
      type: 'info', confirmButtonText: '恢复', cancelButtonText: '取消',
    })
  } catch { return }
  await run(() => restoreRecycled(check.ids), summarizeRestoreResult)
}

async function purgeSelected() {
  const check = validateBatchSelection(selectedIds.value, RECYCLE_BATCH_MAX)
  if (!check.ok) { ElMessage.warning(check.error); return }
  try {
    await ElMessageBox.confirm(`确定彻底删除选中的 ${check.ids.length} 位客户的聊天记录（含图片文件）？此操作无法恢复。`, '彻底删除', {
      type: 'error', confirmButtonText: '彻底删除', cancelButtonText: '取消',
    })
  } catch { return }
  await run(() => purgeRecycled(check.ids), summarizePurgeResult)
}

async function purgeAll() {
  try {
    await ElMessageBox.prompt(
      `将彻底删除回收站中的全部聊天记录（含图片文件），无法恢复。请输入「${CONFIRM_PURGE_RECYCLE}」确认：`,
      '清空回收站',
      {
        type: 'error',
        confirmButtonText: '清空回收站',
        cancelButtonText: '取消',
        inputPlaceholder: CONFIRM_PURGE_RECYCLE,
        inputValidator: (v) => matchesConfirmText(v, CONFIRM_PURGE_RECYCLE) || `请输入「${CONFIRM_PURGE_RECYCLE}」`,
      },
    )
  } catch { return }
  await run(() => purgeAllRecycled(), summarizePurgeResult)
}

async function run(action, summarize) {
  if (busy.value) return
  busy.value = true
  try {
    const r = await action()
    ElMessage.success({ message: summarize(r?.data), duration: 5000 })
    selection.value = []
    await load()
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    busy.value = false
  }
}

function onPageChange(p) {
  page.value = p
  selection.value = []
  load()
}

onMounted(() => {
  load()
  // 剩余天数按分钟刷新，无需频繁请求接口
  nowTimer = setInterval(() => { now.value = Date.now() }, 60 * 1000)
})
onUnmounted(() => { if (nowTimer) clearInterval(nowTimer) })
</script>

<template>
  <el-card>
    <template #header>
      <el-row justify="space-between" align="middle">
        <el-text tag="b">回收站（共 {{ total }} 位客户）</el-text>
        <el-space :size="8">
          <el-button :loading="loading" @click="load">刷新</el-button>
          <el-button type="primary" plain :disabled="!selection.length || busy" @click="restoreSelected">恢复所选（{{ selection.length }}）</el-button>
          <el-button type="danger" plain :disabled="!selection.length || busy" @click="purgeSelected">彻底删除所选</el-button>
          <el-button type="danger" :disabled="!total || busy" :loading="busy" @click="purgeAll">清空回收站</el-button>
        </el-space>
      </el-row>
    </template>
    <el-alert
      type="warning"
      :closable="false"
      show-icon
      title="删除的聊天记录在回收站保留 7 天，到期后系统每小时自动彻底删除（含图片文件），彻底删除后无法恢复。" />
    <el-table v-loading="loading" :data="list" border row-key="visitor_id" empty-text="回收站为空" @selection-change="onSelectionChange">
      <el-table-column type="selection" width="48" />
      <el-table-column label="客户" min-width="160" show-overflow-tooltip>
        <template #default="{ row }">{{ visitorDisplayName(row) }}</template>
      </el-table-column>
      <el-table-column label="IP" width="150">
        <template #default="{ row }">{{ row.ip || '—' }}</template>
      </el-table-column>
      <el-table-column prop="conversations" label="会话数" width="90" />
      <el-table-column prop="messages" label="消息数" width="90" />
      <el-table-column label="删除人" width="120" show-overflow-tooltip>
        <template #default="{ row }">{{ row.deleted_by || '—' }}</template>
      </el-table-column>
      <el-table-column label="删除时间（北京时间）" width="180">
        <template #default="{ row }">{{ formatBeijing(row.deleted_at) }}</template>
      </el-table-column>
      <el-table-column label="剩余" width="110">
        <template #default="{ row }">
          <el-tag :type="remainingTagType(row.purge_at, now)" size="small">{{ remainingLabel(row.purge_at, now) }}</el-tag>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination
      v-if="total > PAGE_SIZE"
      layout="total, prev, pager, next"
      :total="total"
      :page-size="PAGE_SIZE"
      :current-page="page"
      @current-change="onPageChange" />
  </el-card>
</template>
