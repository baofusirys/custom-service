<script setup>
// 聊天输入框旁的快捷回复：弹出列表、可搜索，点击填入输入框（随后可直接回车发送）。
import { computed, nextTick, ref } from 'vue'
import { listQuickReplies } from '../../api/quickReplies'
import { filterQuickReplies } from '../../modules/quickReply'

const props = defineProps({
  disabled: { type: Boolean, default: false },
})
const emit = defineEmits(['pick'])

// 话术变化不频繁：60 秒内重复打开直接复用，减少请求
const CACHE_TTL_MS = 60 * 1000
const visible = ref(false)
const loading = ref(false)
const list = ref([])
const keyword = ref('')
const searchRef = ref(null)
let loadedAt = 0

const filtered = computed(() => filterQuickReplies(list.value, keyword.value))

async function load(force = false) {
  if (loading.value) return
  if (!force && loadedAt && Date.now() - loadedAt < CACHE_TTL_MS) return
  loading.value = true
  try {
    const r = await listQuickReplies()
    list.value = Array.isArray(r?.data) ? r.data.filter(it => it && typeof it.content === 'string') : []
    loadedAt = Date.now()
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    loading.value = false
  }
}

function onShow() {
  load()
  nextTick(() => searchRef.value?.focus())
}

function pick(row) {
  if (!row || typeof row.content !== 'string' || !row.content) return
  visible.value = false
  keyword.value = ''
  emit('pick', row.content)
}
</script>

<template>
  <el-popover v-model:visible="visible" trigger="click" placement="top-start" :width="480" :disabled="props.disabled" @show="onShow">
    <template #reference>
      <el-button plain size="small" :disabled="props.disabled">快捷回复</el-button>
    </template>
    <el-space direction="vertical" fill :size="8">
      <el-space :size="8">
        <el-input ref="searchRef" v-model="keyword" size="small" clearable placeholder="搜索标题或内容" />
        <el-button size="small" link type="primary" :loading="loading" @click="load(true)">刷新</el-button>
      </el-space>
      <el-table
        v-loading="loading"
        :data="filtered"
        size="small"
        height="300"
        highlight-current-row
        empty-text="暂无快捷回复，可在左侧菜单「快捷回复」中添加"
        @row-click="pick">
        <el-table-column label="类型" width="64">
          <template #default="{ row }">
            <el-tag size="small" effect="plain" :type="row.scope === 'team' ? 'primary' : 'success'">{{ row.scope === 'team' ? '团队' : '我的' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="title" label="标题" width="120" show-overflow-tooltip />
        <el-table-column prop="content" label="内容（点击填入）" show-overflow-tooltip />
      </el-table>
    </el-space>
  </el-popover>
</template>
