<script setup>
import { ref, onMounted } from 'vue'
import dayjs from 'dayjs'
import http from '../api/http'

const list = ref([])

async function load() {
  // 简化版：复用 conversations 接口（实际后续会扩展支持 closed 状态过滤）
  const r = await http.get('/agent/conversations')
  list.value = r.data || []
}
onMounted(load)
</script>

<template>
  <el-card>
    <template #header>历史会话（进行中 + 最近）</template>
    <el-table :data="list" border>
      <el-table-column prop="id" label="会话 ID" width="320" />
      <el-table-column prop="identifier" label="访客标识" />
      <el-table-column prop="country" label="国家" />
      <el-table-column prop="city" label="城市" />
      <el-table-column prop="referer" label="来源" />
      <el-table-column label="开始" width="180">
        <template #default="{ row }">{{ dayjs(row.started_at).format('YYYY-MM-DD HH:mm:ss') }}</template>
      </el-table-column>
      <el-table-column label="最近活动" width="180">
        <template #default="{ row }">{{ dayjs(row.updated_at).format('YYYY-MM-DD HH:mm:ss') }}</template>
      </el-table-column>
    </el-table>
  </el-card>
</template>
