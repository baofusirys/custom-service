<script setup>
// 删除全部聊天记录（仅管理员，契约第 2 节）：必须输入「删除全部」才调用接口。
import { ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { deleteAllVisitors } from '../../api/visitors'
import { CONFIRM_DELETE_ALL, matchesConfirmText } from '../../modules/dangerConfirm'
import { summarizeDeleteResult } from '../../modules/conversationOps'

const busy = ref(false)

async function run() {
  try {
    await ElMessageBox.prompt(
      `将把所有客户的聊天记录（含图片文件）移入回收站，7 天后自动彻底删除；在线访客的聊天窗口会立即清空。请输入「${CONFIRM_DELETE_ALL}」确认：`,
      '删除全部聊天记录',
      {
        type: 'error',
        confirmButtonText: '确认删除全部',
        cancelButtonText: '取消',
        inputPlaceholder: CONFIRM_DELETE_ALL,
        inputValidator: (v) => matchesConfirmText(v, CONFIRM_DELETE_ALL) || `请输入「${CONFIRM_DELETE_ALL}」`,
      },
    )
  } catch { return }
  if (busy.value) return
  busy.value = true
  try {
    const r = await deleteAllVisitors()
    ElMessage.success({ message: summarizeDeleteResult(r?.data), duration: 6000 })
  } catch {
    // 错误提示（含 40052 确认文字不正确）由 http 拦截器统一弹出
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <el-space direction="vertical" alignment="flex-start" :size="12">
    <el-alert
      type="error"
      :closable="false"
      show-icon
      title="删除全部聊天记录"
      description="所有客户的聊天记录（含图片文件）将移入回收站，保留 7 天后自动彻底删除；期间可在「回收站」恢复。在线访客的聊天窗口会立即清空。" />
    <el-button type="danger" :loading="busy" @click="run">删除全部聊天记录</el-button>
  </el-space>
</template>
