<script setup>
// 单路视频画面：把 LiveKit 视频轨道挂到 <video> 上，轨道变化时先卸载旧轨道；组件卸载时一定 detach，避免泄漏。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { hallIcons } from './hallIcons'

const props = defineProps({
  track: { type: Object, default: null },
  name: { type: String, default: '' },
  isLocal: { type: Boolean, default: false },
})

const videoRef = ref(null)
let attached = null

function detach() {
  if (attached && videoRef.value) {
    try { attached.detach(videoRef.value) } catch { /* 轨道可能已被 LiveKit 释放 */ }
  }
  attached = null
}

function attach() {
  detach()
  const track = props.track
  const el = videoRef.value
  if (!track || !el || typeof track.attach !== 'function') return
  try {
    track.attach(el)
    attached = track
  } catch {
    attached = null
  }
}

onMounted(attach)
watch(() => props.track, attach)
onBeforeUnmount(detach)
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <el-space :size="6">
        <el-icon><component :is="hallIcons.camera" /></el-icon>
        <el-text truncated>{{ name || '参与者' }}</el-text>
        <el-tag v-if="isLocal" size="small" effect="plain">我</el-tag>
      </el-space>
    </template>
    <video v-show="track" ref="videoRef" autoplay playsinline muted width="100%"></video>
    <el-empty v-if="!track" :image-size="56" description="画面加载中" />
  </el-card>
</template>
