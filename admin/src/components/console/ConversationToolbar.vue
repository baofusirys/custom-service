<script setup>
// 会话列表工具栏：按标签筛选、全选、批量删除 / 导出、全局搜索入口（契约第 1、2、7、8 节）。
import { computed, onMounted, ref } from 'vue'
import { listTags } from '../../api/visitors'

const props = defineProps({
  tag: { type: String, default: '' },
  selectedCount: { type: Number, default: 0 },
  loadedCount: { type: Number, default: 0 },
  busy: { type: Boolean, default: false },
})
const emit = defineEmits(['update:tag', 'toggle-all', 'batch-delete', 'batch-export', 'open-search'])

const TAGS_TTL_MS = 60 * 1000
const tagOptions = ref([])
const tagsLoading = ref(false)
let tagsLoadedAt = 0

async function loadTags(force = false) {
  if (tagsLoading.value) return
  if (!force && Date.now() - tagsLoadedAt < TAGS_TTL_MS) return
  tagsLoading.value = true
  try {
    const r = await listTags({ silent: true })
    const list = Array.isArray(r?.data) ? r.data : []
    tagOptions.value = list
      .filter(it => it && typeof it.tag === 'string' && it.tag)
      .slice(0, 100)
      .map(it => ({ tag: it.tag, count: Number(it.count) > 0 ? Math.trunc(Number(it.count)) : 0 }))
    tagsLoadedAt = Date.now()
  } catch {
    // 静默：下拉展开时会再次尝试
  } finally {
    tagsLoading.value = false
  }
}

function onTagVisible(visible) {
  if (visible) loadTags()
}

function onTagChange(value) {
  emit('update:tag', typeof value === 'string' ? value : '')
}

const allChecked = computed(() => props.loadedCount > 0 && props.selectedCount >= props.loadedCount)
const indeterminate = computed(() => props.selectedCount > 0 && props.selectedCount < props.loadedCount)

onMounted(() => loadTags(true))
defineExpose({ reloadTags: () => loadTags(true) })
</script>

<template>
  <el-space direction="vertical" fill :size="8">
    <el-select
      :model-value="tag"
      size="small"
      clearable
      filterable
      placeholder="按标签筛选"
      no-data-text="暂无标签"
      :loading="tagsLoading"
      @visible-change="onTagVisible"
      @update:model-value="onTagChange">
      <el-option v-for="t in tagOptions" :key="t.tag" :value="t.tag" :label="t.tag + '（' + t.count + '）'" />
    </el-select>
    <el-space :size="8" wrap>
      <el-checkbox
        :model-value="allChecked"
        :indeterminate="indeterminate"
        :disabled="!loadedCount"
        @change="(v) => emit('toggle-all', v === true)">
        全选
      </el-checkbox>
      <el-button size="small" type="danger" plain :disabled="!selectedCount || busy" @click="emit('batch-delete')">
        批量删除{{ selectedCount ? '（' + selectedCount + '）' : '' }}
      </el-button>
      <el-button size="small" plain :disabled="!selectedCount || busy" :loading="busy" @click="emit('batch-export')">
        批量导出
      </el-button>
      <el-button size="small" link type="primary" @click="emit('open-search')">搜索聊天</el-button>
    </el-space>
  </el-space>
</template>
