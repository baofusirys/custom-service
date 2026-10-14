<script setup>
// 黑名单（契约第 6 节）：分页列表、解除拉黑。
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listBlockedVisitors, setVisitorBlocked } from '../api/visitors'
import { isValidVisitorId, visitorDisplayName } from '../modules/conversationOps'
import { formatBeijing } from '../modules/timeFormat'

const PAGE_SIZE = 50
const loading = ref(false)
const list = ref([])
const total = ref(0)
const page = ref(1)
const busy = ref({})
let loadSeq = 0

async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const r = await listBlockedVisitors({ offset: (page.value - 1) * PAGE_SIZE, limit: PAGE_SIZE })
    if (seq !== loadSeq) return
    list.value = Array.isArray(r?.data) ? r.data.filter(it => it && isValidVisitorId(it.visitor_id)) : []
    total.value = Number(r?.total) > 0 ? Math.trunc(Number(r.total)) : 0
    // 当前页被清空（例如最后一条被解除）时回退一页
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

async function unblock(row) {
  try {
    await ElMessageBox.confirm(`确定解除拉黑「${visitorDisplayName(row)}」？由该客户产生的 IP 拉黑会同时解除。`, '解除拉黑', {
      type: 'warning', confirmButtonText: '解除拉黑', cancelButtonText: '取消',
    })
  } catch { return }
  if (busy.value[row.visitor_id]) return
  busy.value[row.visitor_id] = true
  try {
    await setVisitorBlocked(row.visitor_id, { blocked: false })
    ElMessage.success('已解除拉黑')
    await load()
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    delete busy.value[row.visitor_id]
  }
}

function onPageChange(p) {
  page.value = p
  load()
}

onMounted(load)
</script>

<template>
  <el-card>
    <template #header>
      <el-row justify="space-between" align="middle">
        <el-text tag="b">黑名单（共 {{ total }} 位）</el-text>
        <el-button :loading="loading" @click="load">刷新</el-button>
      </el-row>
    </template>
    <el-alert
      type="info"
      :closable="false"
      show-icon
      title="被拉黑的客户仍可发消息（不会提示被拉黑），但不进入会话列表、不响提示音、不推送、不弹来电；聊天记录保留可查。" />
    <el-table v-loading="loading" :data="list" border empty-text="暂无拉黑客户">
      <el-table-column label="客户" min-width="160" show-overflow-tooltip>
        <template #default="{ row }">{{ visitorDisplayName(row) }}</template>
      </el-table-column>
      <el-table-column label="IP" width="150">
        <template #default="{ row }">{{ row.ip || '—' }}</template>
      </el-table-column>
      <el-table-column label="原因" min-width="180" show-overflow-tooltip>
        <template #default="{ row }">{{ row.reason || '—' }}</template>
      </el-table-column>
      <el-table-column label="IP 拉黑" width="90">
        <template #default="{ row }">
          <el-tag :type="row.ip_blocked ? 'danger' : 'info'" size="small">{{ row.ip_blocked ? '是' : '否' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作人" width="120" show-overflow-tooltip>
        <template #default="{ row }">{{ row.blocked_by || '—' }}</template>
      </el-table-column>
      <el-table-column label="拉黑时间（北京时间）" width="180">
        <template #default="{ row }">{{ formatBeijing(row.blocked_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="110">
        <template #default="{ row }">
          <el-button link type="primary" :loading="busy[row.visitor_id] === true" @click="unblock(row)">解除拉黑</el-button>
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
