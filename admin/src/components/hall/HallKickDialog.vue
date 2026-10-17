<script setup>
// 移出大厅：选择禁止再次进入的时长（契约 4.2：kick 的 minutes 范围 1–1440，界面提供 10/30/60/1440 分钟）。
import { ref, watch } from 'vue'
import { KICK_DEFAULT_MINUTES, KICK_MINUTES_OPTIONS, kickMinutesLabel } from '../../modules/hallProtocol'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  target: { type: Object, default: null },
  loading: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'confirm'])

const minutes = ref(KICK_DEFAULT_MINUTES)
watch(() => props.modelValue, (open) => { if (open) minutes.value = KICK_DEFAULT_MINUTES })

function close() {
  emit('update:modelValue', false)
}
</script>

<template>
  <el-dialog :model-value="modelValue" title="移出大厅" width="440" append-to-body @update:model-value="(v) => emit('update:modelValue', v)">
    <el-form label-position="top">
      <el-form-item label="参与者">
        <el-text>{{ target?.name || '' }}</el-text>
      </el-form-item>
      <el-form-item label="禁止再次进入">
        <el-radio-group v-model="minutes">
          <el-radio-button v-for="m in KICK_MINUTES_OPTIONS" :key="m" :value="m">{{ kickMinutesLabel(m) }}</el-radio-button>
        </el-radio-group>
      </el-form-item>
    </el-form>
    <el-alert type="info" :closable="false" show-icon title="移出后对方会立即离开大厅，禁入期间仍可正常使用私聊客服。" />
    <template #footer>
      <el-button @click="close">取消</el-button>
      <el-button type="warning" :loading="loading" @click="emit('confirm', minutes)">确认移出</el-button>
    </template>
  </el-dialog>
</template>
