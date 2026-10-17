<script setup>
// [122] 设置页「多人大厅」分组下的独立子卡片：敏感词屏蔽（契约 _internal/reports/hall-contract.md 第 9 节）。
// 仅管理员可见（由设置页按角色挂载，服务端接口同样只允许管理员）。三个页签各自加载、各自保存，
// 不参与设置页底部的「保 存」按钮；拦截记录页签首次打开时才加载。
import { ref } from 'vue'
import HallAlertMailForm from './HallAlertMailForm.vue'
import HallFilterEventTable from './HallFilterEventTable.vue'
import HallFilterRulesForm from './HallFilterRulesForm.vue'

const activeTab = ref('rules')
</script>

<template>
  <el-card shadow="never" data-testid="hall-filter-card">
    <template #header>
      <el-space direction="vertical" alignment="flex-start" :size="4">
        <el-text tag="b">敏感词屏蔽</el-text>
        <el-text size="small" type="info">只处理访客在大厅发送的文字（图片、文件和客服消息不处理）：命中后大厅里显示为 **，原文进入拦截记录，并按「邮件通知」发给管理员。</el-text>
      </el-space>
    </template>
    <el-tabs v-model="activeTab">
      <el-tab-pane label="屏蔽规则" name="rules">
        <HallFilterRulesForm />
      </el-tab-pane>
      <el-tab-pane label="邮件通知" name="mail">
        <HallAlertMailForm />
      </el-tab-pane>
      <el-tab-pane label="拦截记录" name="events" lazy>
        <HallFilterEventTable :active="activeTab === 'events'" />
      </el-tab-pane>
    </el-tabs>
  </el-card>
</template>
