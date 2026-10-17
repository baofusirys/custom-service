<script setup>
// [123] 大厅开关面板（契约第 10 节）：总开关、文字聊天、语音、视频 4 个开关 + 最后修改人与时间。
// 所有客服可见可操作；未进入大厅、大厅已关闭时同样可用（可以把总开关重新打开）。
// 本组件只负责展示与发出切换意图；二次确认、请求、乐观显示与失败回滚由大厅页统一处理（modules/hallSwitches.js）。
import { computed } from 'vue'
import { HALL_SWITCHES, formatSwitchUpdated } from '../../modules/hallSwitches'
import { hallIcons } from './hallIcons'

const props = defineProps({
  // 显示值：服务端确认状态叠加正在提交的乐观值（displaySwitches）
  switches: { type: Object, required: true },
  // 正在提交的开关：{ [field]: true }
  busy: { type: Object, default: () => ({}) },
  updatedBy: { type: String, default: '' },
  updatedAt: { type: String, default: '' },
  // 至少成功读取过一次开关状态后才允许切换，避免按默认值误操作
  ready: { type: Boolean, default: false },
  loading: { type: Boolean, default: false },
  error: { type: String, default: '' },
})
const emit = defineEmits(['change', 'reload'])

const updatedText = computed(() => formatSwitchUpdated({ updatedBy: props.updatedBy, updatedAt: props.updatedAt }))

function onChange(field, value) {
  if (!props.ready || props.busy[field]) return
  emit('change', field, value === true)
}
</script>

<template>
  <el-card v-loading="loading && !ready" shadow="never" data-hall-switch-panel>
    <template #header>
      <el-row justify="space-between" align="middle">
        <el-space :size="8" wrap>
          <el-icon :size="18"><component :is="hallIcons.toggle" /></el-icon>
          <el-text tag="b" size="large">大厅开关</el-text>
          <el-text size="small" type="info">所有客服都可以切换，修改后立即对所有访客和客服生效</el-text>
        </el-space>
        <el-space :size="8" wrap>
          <el-text size="small" type="info" data-hall-switch-updated>{{ updatedText }}</el-text>
          <el-button link type="primary" :icon="hallIcons.refresh" :loading="loading" @click="emit('reload')">刷新开关</el-button>
        </el-space>
      </el-row>
    </template>
    <el-space direction="vertical" alignment="stretch" fill :size="12" style="width: 100%">
      <el-alert v-if="error" type="error" show-icon :closable="false" :title="error" :description="ready ? '当前显示的是最近一次读取到的状态。' : '读取成功前暂不能切换开关，请点击右上角「刷新开关」重试。'" />
      <el-row :gutter="16">
        <el-col v-for="s in HALL_SWITCHES" :key="s.field" :xs="24" :sm="12" :lg="6">
          <el-space direction="vertical" alignment="flex-start" :size="6" :data-hall-switch="s.apiKey">
            <el-space :size="8" wrap>
              <el-text tag="b">{{ s.label }}</el-text>
              <el-switch
                :model-value="switches[s.field]"
                :loading="!!busy[s.field]"
                :disabled="!ready"
                active-text="开启"
                inactive-text="关闭"
                :aria-label="s.label"
                @change="(v) => onChange(s.field, v)" />
            </el-space>
            <el-text size="small" type="info">{{ s.description }}</el-text>
            <el-text v-if="s.field !== 'enabled' && !switches.enabled" size="small" type="warning">总开关关闭中，此项暂不生效</el-text>
          </el-space>
        </el-col>
      </el-row>
    </el-space>
  </el-card>
</template>
