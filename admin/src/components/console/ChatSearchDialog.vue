<script setup>
// 全局搜索聊天内容（契约第 8 节）：输入 2 字以上回车搜索，点击结果打开并定位对应客户会话。
import { ref, watch, nextTick } from 'vue'
import { ElMessage } from 'element-plus'
import { searchMessages } from '../../api/chat'
import { SEARCH_DEFAULT_LIMIT, SEARCH_MAX_CHARS, normalizeSearchKeyword } from '../../modules/chatSearch'
import { isValidVisitorId, senderLabel, visitorDisplayName } from '../../modules/conversationOps'
import { formatBeijing } from '../../modules/timeFormat'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'open'])

const keyword = ref('')
const loading = ref(false)
const results = ref([])
const searchedQ = ref('')
const inputRef = ref(null)
let searchSeq = 0

watch(() => props.modelValue, (open) => {
  if (open) nextTick(() => inputRef.value?.focus())
})

async function search() {
  const check = normalizeSearchKeyword(keyword.value)
  if (!check.ok) {
    ElMessage.warning(check.error)
    return
  }
  if (loading.value) return
  const seq = ++searchSeq
  loading.value = true
  try {
    const r = await searchMessages(check.q, SEARCH_DEFAULT_LIMIT)
    if (seq !== searchSeq) return
    results.value = (Array.isArray(r?.data) ? r.data : []).filter(it => it && isValidVisitorId(it.visitor_id))
    searchedQ.value = check.q
  } catch {
    // 错误提示（含限流 42910）由 http 拦截器统一弹出
  } finally {
    if (seq === searchSeq) loading.value = false
  }
}

function open(row) {
  if (!row || !isValidVisitorId(row.visitor_id)) return
  emit('open', row)
  emit('update:modelValue', false)
}
</script>

<template>
  <el-dialog :model-value="modelValue" title="搜索聊天记录" width="820" append-to-body @update:model-value="(v) => emit('update:modelValue', v)">
    <el-space direction="vertical" fill :size="12">
      <el-input
        ref="inputRef"
        v-model="keyword"
        clearable
        :maxlength="SEARCH_MAX_CHARS"
        placeholder="输入至少 2 个字后回车搜索（不含已撤回消息与浏览记录）"
        @keyup.enter="search">
        <template #append>
          <el-button :loading="loading" @click="search">搜索</el-button>
        </template>
      </el-input>
      <el-text v-if="searchedQ" size="small" type="info">「{{ searchedQ }}」共找到 {{ results.length }} 条（最多显示 {{ SEARCH_DEFAULT_LIMIT }} 条，按时间倒序），点击结果打开会话</el-text>
      <el-table v-loading="loading" :data="results" height="420" size="small" highlight-current-row :empty-text="searchedQ ? '没有匹配的聊天内容' : '请输入关键词'" @row-click="open">
        <el-table-column label="客户" width="150" show-overflow-tooltip>
          <template #default="{ row }">{{ visitorDisplayName(row) }}</template>
        </el-table-column>
        <el-table-column label="发送方" width="72">
          <template #default="{ row }">
            <el-tag size="small" effect="plain" :type="row.sender === 'agent' ? 'primary' : (row.sender === 'visitor' ? 'success' : 'info')">{{ senderLabel(row.sender) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="内容摘要" show-overflow-tooltip>
          <template #default="{ row }">{{ typeof row.snippet === 'string' ? row.snippet : '' }}</template>
        </el-table-column>
        <el-table-column label="时间（北京时间）" width="170">
          <template #default="{ row }">{{ formatBeijing(row.created_at) }}</template>
        </el-table-column>
      </el-table>
    </el-space>
  </el-dialog>
</template>
