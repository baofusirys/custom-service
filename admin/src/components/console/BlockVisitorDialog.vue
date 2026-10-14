<script setup>
// 拉黑客户对话框（契约第 6 节）：填写原因（≤100 字），可选同时拉黑当前 IP。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { setVisitorBlocked } from '../../api/visitors'
import { BLOCK_REASON_MAX_LENGTH, validateBlockReason } from '../../modules/visitorProfile'
import { isValidVisitorId, visitorDisplayName } from '../../modules/conversationOps'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  visitor: { type: Object, default: null },
})
const emit = defineEmits(['update:modelValue', 'done'])

const reason = ref('')
const blockIp = ref(false)
const submitting = ref(false)
const displayName = computed(() => (props.visitor ? visitorDisplayName(props.visitor) : ''))

watch(() => props.modelValue, (open) => {
  if (open) {
    reason.value = ''
    blockIp.value = false
  }
})

function close() {
  emit('update:modelValue', false)
}

async function submit() {
  const vid = props.visitor?.visitor_id
  if (!isValidVisitorId(vid)) {
    ElMessage.warning('客户信息无效，请刷新后重试')
    return
  }
  const check = validateBlockReason(reason.value)
  if (!check.ok) {
    ElMessage.warning(check.error)
    return
  }
  if (submitting.value) return
  submitting.value = true
  try {
    await setVisitorBlocked(vid, { blocked: true, reason: check.reason, blockIp: blockIp.value })
    ElMessage.success(blockIp.value ? '已拉黑该客户及其当前 IP' : '已拉黑该客户')
    emit('done', { visitor_id: vid, blocked: true, ip_blocked: blockIp.value })
    close()
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog :model-value="modelValue" title="拉黑客户" width="480" append-to-body @update:model-value="(v) => emit('update:modelValue', v)">
    <el-alert
      type="warning"
      :closable="false"
      show-icon
      title="静默拉黑"
      description="拉黑后访客仍可正常发消息（不会提示被拉黑），但不进入会话列表、不响提示音、不推送、不弹来电；聊天记录保留可查。" />
    <el-form label-position="top">
      <el-form-item label="客户">
        <el-text>{{ displayName }}</el-text>
      </el-form-item>
      <el-form-item label="拉黑原因（选填）">
        <el-input v-model="reason" type="textarea" :rows="3" :maxlength="BLOCK_REASON_MAX_LENGTH" show-word-limit placeholder="例如：恶意骚扰、广告" />
      </el-form-item>
      <el-form-item>
        <el-checkbox v-model="blockIp">同时拉黑该访客当前 IP（同 IP 的新访客会自动继承拉黑）</el-checkbox>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="close">取消</el-button>
      <el-button type="danger" :loading="submitting" @click="submit">确认拉黑</el-button>
    </template>
  </el-dialog>
</template>
