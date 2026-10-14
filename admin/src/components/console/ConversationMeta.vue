<script setup>
// 会话列表项的附加信息：置顶、手动未读、标签与备注摘要（全部以文本渲染）。
import { computed } from 'vue'
import { noteSummary } from '../../modules/visitorProfile'

const props = defineProps({
  conv: { type: Object, required: true },
  maxTags: { type: Number, default: 3 },
})

const tags = computed(() => (Array.isArray(props.conv?.tags) ? props.conv.tags.filter(t => typeof t === 'string' && t) : []))
const shownTags = computed(() => tags.value.slice(0, props.maxTags))
const extraTagCount = computed(() => Math.max(0, tags.value.length - props.maxTags))
const note = computed(() => noteSummary(props.conv?.note, 24))
const hasBadges = computed(() => props.conv?.pinned === true || props.conv?.marked_unread === true || tags.value.length > 0)
</script>

<template>
  <div v-if="hasBadges || note">
    <el-space v-if="hasBadges" :size="4" wrap>
      <el-tag v-if="conv.pinned === true" size="small" type="warning" effect="plain">置顶</el-tag>
      <el-tag v-if="conv.marked_unread === true" size="small" type="danger" effect="plain">未读</el-tag>
      <el-tag v-for="t in shownTags" :key="t" size="small" type="info" effect="plain">{{ t }}</el-tag>
      <el-text v-if="extraTagCount" size="small" type="info">+{{ extraTagCount }}</el-text>
    </el-space>
    <el-text v-if="note" tag="div" size="small" type="info" truncated>备注：{{ note }}</el-text>
  </div>
</template>
