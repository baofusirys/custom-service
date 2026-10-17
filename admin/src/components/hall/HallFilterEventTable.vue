<script setup>
// [122] 敏感词拦截记录（契约第 9.2 节 GET /api/admin/hall/filter/events，新到旧，before 游标翻页）。
// 原文仅管理员可见；全部文本插值渲染（无 v-html），长文本用 show-overflow-tooltip 查看全文。
// 内存中最多保留 EVENTS_MEMORY_LIMIT 条，防止一直「加载更多」拖慢页面。
import { onMounted, ref, watch } from 'vue'
import { listHallFilterEvents } from '../../api/hallFilter'
import { hallErrorMessage } from '../../modules/hallErrors'
import { formatBeijing } from '../../modules/timeFormat'
import {
  EVENTS_MEMORY_LIMIT,
  EVENTS_PAGE_SIZE,
  categoryLabel,
  mailStatusMeta,
  mergeEvents,
  normalizeEventList,
  oldestEventCursor,
} from '../../modules/hallFilterForm'

const props = defineProps({
  // 所在页签是否处于激活状态：再次切换到本页签时自动刷新第一页
  active: { type: Boolean, default: false },
})

const rows = ref([])
const loading = ref(false)
const loadingMore = ref(false)
const hasMore = ref(false)
const limitReached = ref(false)
const loadError = ref('')
// 每次刷新递增，丢弃过期请求的结果（刷新与加载更多交错时以最新一次刷新为准）
let generation = 0

async function refresh() {
  if (loading.value) return
  const gen = ++generation
  loading.value = true
  loadError.value = ''
  try {
    const r = await listHallFilterEvents({ limit: EVENTS_PAGE_SIZE })
    if (gen !== generation) return
    const raw = Array.isArray(r?.data) ? r.data : []
    rows.value = normalizeEventList(raw)
    hasMore.value = raw.length >= EVENTS_PAGE_SIZE
    limitReached.value = false
  } catch (e) {
    if (gen === generation) loadError.value = hallErrorMessage(e, '加载拦截记录失败，请稍后重试')
  } finally {
    if (gen === generation) loading.value = false
  }
}

async function loadMore() {
  if (loadingMore.value || loading.value || !hasMore.value) return
  if (rows.value.length >= EVENTS_MEMORY_LIMIT) {
    limitReached.value = true
    return
  }
  const before = oldestEventCursor(rows.value)
  if (!before) {
    hasMore.value = false
    return
  }
  const gen = generation
  loadingMore.value = true
  try {
    const r = await listHallFilterEvents({ before, limit: EVENTS_PAGE_SIZE })
    if (gen !== generation) return
    const raw = Array.isArray(r?.data) ? r.data : []
    rows.value = mergeEvents(rows.value, normalizeEventList(raw))
    hasMore.value = raw.length >= EVENTS_PAGE_SIZE
    if (rows.value.length >= EVENTS_MEMORY_LIMIT) limitReached.value = true
  } catch (e) {
    if (gen === generation) loadError.value = hallErrorMessage(e, '加载更多拦截记录失败，请稍后重试')
  } finally {
    loadingMore.value = false
  }
}

watch(() => props.active, (now, before) => {
  if (now && !before) refresh()
})

onMounted(refresh)
</script>

<template>
  <el-space direction="vertical" alignment="stretch" fill :size="12" style="width: 100%">
    <el-row justify="space-between" align="middle">
      <el-text size="small" type="info">最近 30 天的拦截记录（新到旧），30 天后自动清理。原文仅管理员可见，鼠标停在单元格上可看全文。</el-text>
      <el-button size="small" :loading="loading" data-testid="hall-filter-events-refresh" @click="refresh">刷新</el-button>
    </el-row>

    <el-alert v-if="loadError" type="error" show-icon :closable="false" :title="loadError" />

    <el-table
      v-loading="loading"
      :data="rows"
      row-key="key"
      size="small"
      border
      empty-text="暂无拦截记录"
      data-testid="hall-filter-events">
      <el-table-column label="时间" width="150">
        <template #default="{ row }">{{ formatBeijing(row.createdAt) }}</template>
      </el-table-column>
      <el-table-column label="发送者" min-width="110">
        <template #default="{ row }">
          <el-space direction="vertical" alignment="flex-start" :size="0">
            <el-text size="small">{{ row.name || '—' }}</el-text>
            <el-text v-if="row.vid" size="small" type="info">{{ row.vid }}</el-text>
          </el-space>
        </template>
      </el-table-column>
      <el-table-column label="类别" min-width="96">
        <template #default="{ row }">
          <el-space wrap :size="4">
            <el-tag v-for="c in row.categories" :key="c" size="small" type="warning">{{ categoryLabel(c) }}</el-tag>
          </el-space>
        </template>
      </el-table-column>
      <el-table-column label="原文" prop="original" min-width="160" show-overflow-tooltip />
      <el-table-column label="屏蔽后" prop="masked" min-width="110" show-overflow-tooltip />
      <el-table-column label="邮件状态" width="112">
        <template #default="{ row }">
          <el-tooltip v-if="row.mailError" :content="row.mailError" placement="top">
            <el-tag size="small" :type="mailStatusMeta(row.mailStatus).type">{{ mailStatusMeta(row.mailStatus).label }}</el-tag>
          </el-tooltip>
          <el-tag v-else size="small" :type="mailStatusMeta(row.mailStatus).type">{{ mailStatusMeta(row.mailStatus).label }}</el-tag>
          <el-text v-if="row.mailAttempts && row.mailStatus !== 'sent'" size="small" type="info">已试 {{ row.mailAttempts }} 次</el-text>
        </template>
      </el-table-column>
    </el-table>

    <el-row justify="center">
      <el-button
        v-if="hasMore && !limitReached"
        link
        type="primary"
        :loading="loadingMore"
        data-testid="hall-filter-events-more"
        @click="loadMore">加载更多</el-button>
      <el-text v-else-if="limitReached" size="small" type="info">已显示最近 {{ EVENTS_MEMORY_LIMIT }} 条记录</el-text>
      <el-text v-else-if="rows.length" size="small" type="info">没有更早的记录了</el-text>
    </el-row>
  </el-space>
</template>
