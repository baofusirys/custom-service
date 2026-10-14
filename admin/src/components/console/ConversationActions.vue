<script setup>
// 单个客户的操作菜单：置顶 / 未读 / 导出 / 拉黑 / 删除聊天记录。
// 外层 span 阻止点击冒泡到会话项，避免点菜单时顺带切换会话。
import { computed } from 'vue'

const props = defineProps({
  conv: { type: Object, required: true },
  disabled: { type: Boolean, default: false },
})
const emit = defineEmits(['command'])

const isUnread = computed(() => props.conv?.marked_unread === true || Number(props.conv?.unread) > 0)
</script>

<template>
  <span @click.stop>
    <!-- persistent=false：菜单只在展开时创建，长列表不额外占用 DOM -->
    <el-dropdown trigger="click" :persistent="false" :disabled="disabled" @command="(cmd) => emit('command', cmd)">
      <el-button link type="primary" size="small" :disabled="disabled">操作</el-button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item :command="conv.pinned === true ? 'unpin' : 'pin'">
            {{ conv.pinned === true ? '取消置顶' : '置顶' }}
          </el-dropdown-item>
          <el-dropdown-item :command="isUnread ? 'mark-read' : 'mark-unread'">
            {{ isUnread ? '标为已读' : '标为未读' }}
          </el-dropdown-item>
          <el-dropdown-item command="profile">客户资料</el-dropdown-item>
          <el-dropdown-item command="export">导出聊天记录（xlsx）</el-dropdown-item>
          <el-dropdown-item command="block" divided>拉黑</el-dropdown-item>
          <el-dropdown-item command="delete">删除聊天记录</el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
  </span>
</template>
