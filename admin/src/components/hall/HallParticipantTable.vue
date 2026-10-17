<script setup>
// 大厅参与者表格：名字、角色、麦克风、摄像头、正在说话、管理操作。只负责展示与发出事件，操作由大厅页执行。
// 不展示访客 vid / IP / 地区（契约第 2 节）。
import { computed, ref, watch } from 'vue'
import { filterRows, rowActions } from '../../modules/hallParticipants'
import { hallIcons } from './hallIcons'

const props = defineProps({
  rows: { type: Array, default: () => [] },
  pending: { type: Object, default: () => ({}) },
  loading: { type: Boolean, default: false },
  connected: { type: Boolean, default: false },
  isAdmin: { type: Boolean, default: false },
})
const emit = defineEmits(['mute', 'camera-off', 'kick', 'block', 'chat'])

const keyword = ref('')
const page = ref(1)
const pageSize = ref(20)

const filtered = computed(() => filterRows(props.rows, keyword.value))
const paged = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filtered.value.slice(start, start + pageSize.value).map(r => ({ ...r, actions: rowActions(r, { isAdmin: props.isAdmin }) }))
})

watch([() => filtered.value.length, pageSize], ([n]) => {
  const maxPage = Math.max(1, Math.ceil(n / pageSize.value))
  if (page.value > maxPage) page.value = maxPage
})
watch(keyword, () => { page.value = 1 })
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <el-row justify="space-between" align="middle">
        <el-space :size="8">
          <el-icon><component :is="hallIcons.users" /></el-icon>
          <el-text tag="b">参与者</el-text>
          <el-text size="small" type="info">{{ connected ? '实时' : '每 30 秒自动刷新' }}</el-text>
        </el-space>
        <el-input v-model="keyword" clearable maxlength="20" placeholder="按名字筛选" style="width: 200px" />
      </el-row>
    </template>
    <el-table v-loading="loading" :data="paged" row-key="identity" size="small" empty-text="大厅里暂时没有人">
      <el-table-column label="名字" min-width="140" show-overflow-tooltip>
        <template #default="{ row }">
          <el-space :size="4">
            <el-text>{{ row.name }}</el-text>
            <el-tag v-if="row.isSelf" size="small" effect="plain">我</el-tag>
            <el-tag v-if="row.blocked" size="small" type="danger" effect="plain">已拉黑</el-tag>
          </el-space>
        </template>
      </el-table-column>
      <el-table-column label="角色" width="72">
        <template #default="{ row }">
          <el-tag size="small" :type="row.role === 'agent' ? 'primary' : 'info'">{{ row.role === 'agent' ? '客服' : '访客' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="麦克风" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.micOn ? 'success' : 'info'" effect="plain">{{ row.micOn ? '已开' : '关闭' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="摄像头" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.cameraOn ? 'success' : 'info'" effect="plain">{{ row.cameraOn ? '已开' : '关闭' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="正在说话" width="90">
        <template #default="{ row }">
          <el-tag v-if="row.speaking" size="small" type="success" effect="dark">说话中</el-tag>
          <el-text v-else size="small" type="info">{{ connected ? '否' : '进入后可见' }}</el-text>
        </template>
      </el-table-column>
      <el-table-column label="操作" min-width="360" fixed="right">
        <template #default="{ row }">
          <el-space :size="4" wrap>
            <el-button size="small" :icon="hallIcons.micOff" :disabled="!row.actions.mute || !!pending[row.identity]" @click="emit('mute', row)">静音</el-button>
            <el-button size="small" :icon="hallIcons.cameraOff" :disabled="!row.actions.cameraOff || !!pending[row.identity]" @click="emit('camera-off', row)">关摄像头</el-button>
            <el-button size="small" type="warning" plain :icon="hallIcons.kick" :disabled="!row.actions.kick || !!pending[row.identity]" @click="emit('kick', row)">移出</el-button>
            <el-button size="small" type="danger" plain :icon="hallIcons.ban" :disabled="!row.actions.block" @click="emit('block', row)">拉黑</el-button>
            <el-button size="small" type="primary" link :icon="hallIcons.chat" :disabled="!row.actions.chat" @click="emit('chat', row)">私聊</el-button>
          </el-space>
        </template>
      </el-table-column>
    </el-table>
    <template #footer>
      <el-row justify="end">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :page-sizes="[20, 50, 100]"
          :total="filtered.length"
          layout="total, sizes, prev, pager, next"
          size="small" />
      </el-row>
    </template>
  </el-card>
</template>
