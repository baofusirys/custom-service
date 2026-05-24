<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import { listSounds, playSound, unlockAudio } from '../api/sound'

const loading = ref(false)
const saving = ref(false)
const form = ref({
  agent_notify_sound: 'agent1',
  visitor_notify_sound: 'visitor1',
  notify_visitor_enter: true,
  greeting_enabled: true,
  greeting_text: '您好，欢迎光临！请问有什么可以帮您？',
  widget_title: '在线客服',
  // luckfast APNs 推送：访客发消息时后端调 messagepush.luckfast.com 推到客服 iPhone
  // 两项都填才启用；留空即禁用，不影响其它功能
  push_user_id: '',
  push_user_key: '',
  // 三种推送场景独立的提示音（luckfast 0-15 共 16 种）
  push_sound_enter: '1',    // 新访客打开 widget
  push_sound_message: '9',  // 已有会话中访客发新消息
  push_sound_call: '4'      // 访客发起语音通话（拉起 App 接听）
})

// luckfast 推送提示音：0-15 共 16 种（具体音色需要在 iPhone 上听）
const pushSoundOptions = Array.from({ length: 16 }, (_, i) => ({
  value: String(i),
  label: i === 0 ? '0 - 默认' : `${i} - 提示音 ${i}`
}))

const soundOptions = listSounds()

async function load() {
  loading.value = true
  try {
    const r = await http.get('/admin/settings')
    const d = r.data || {}
    form.value.agent_notify_sound = d.agent_notify_sound || 'agent1'
    form.value.visitor_notify_sound = d.visitor_notify_sound || 'visitor1'
    form.value.notify_visitor_enter = d.notify_visitor_enter !== 'false'
    form.value.greeting_enabled = d.greeting_enabled !== 'false'
    form.value.greeting_text = d.greeting_text || form.value.greeting_text
    form.value.widget_title = d.widget_title || form.value.widget_title
    form.value.push_user_id = d.push_user_id || ''
    form.value.push_user_key = d.push_user_key || ''
    form.value.push_sound_enter = d.push_sound_enter || '1'
    form.value.push_sound_message = d.push_sound_message || '9'
    form.value.push_sound_call = d.push_sound_call || '4'
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const payload = {
      agent_notify_sound: form.value.agent_notify_sound,
      visitor_notify_sound: form.value.visitor_notify_sound,
      notify_visitor_enter: form.value.notify_visitor_enter ? 'true' : 'false',
      greeting_enabled: form.value.greeting_enabled ? 'true' : 'false',
      greeting_text: form.value.greeting_text || '',
      widget_title: form.value.widget_title || '在线客服',
      push_user_id: (form.value.push_user_id || '').trim(),
      push_user_key: (form.value.push_user_key || '').trim(),
      push_sound_enter: form.value.push_sound_enter || '1',
      push_sound_message: form.value.push_sound_message || '9',
      push_sound_call: form.value.push_sound_call || '4'
    }
    await http.post('/admin/settings', payload)
    ElMessage.success('保存成功')
  } finally {
    saving.value = false
  }
}

function preview(name) {
  unlockAudio()
  playSound(name)
}

onMounted(load)
</script>

<template>
  <el-card v-loading="loading" class="settings-card">
    <template #header>
      <span>系统设置</span>
    </template>

    <el-form label-width="160px" label-position="left">
      <el-divider content-position="left">通知声音</el-divider>

      <el-form-item label="客服端提示音">
        <el-select v-model="form.agent_notify_sound" style="width:200px">
          <el-option v-for="s in soundOptions" :key="s.value" :value="s.value" :label="s.label" />
        </el-select>
        <el-button link type="primary" style="margin-left:12px" @click="preview(form.agent_notify_sound)">试听</el-button>
        <div class="form-tip">客服后台收到访客消息时播放</div>
      </el-form-item>

      <el-form-item label="访客端提示音">
        <el-select v-model="form.visitor_notify_sound" style="width:200px">
          <el-option v-for="s in soundOptions" :key="s.value" :value="s.value" :label="s.label" />
        </el-select>
        <el-button link type="primary" style="margin-left:12px" @click="preview(form.visitor_notify_sound)">试听</el-button>
        <div class="form-tip">访客网页上的 widget 收到客服消息时播放</div>
      </el-form-item>

      <el-divider content-position="left">访客进入网站</el-divider>

      <el-form-item label="通知客服">
        <el-switch v-model="form.notify_visitor_enter" />
        <div class="form-tip">访客打开有 widget 的页面 → 客服后台弹出提醒并播声</div>
      </el-form-item>

      <el-form-item label="自动问候">
        <el-switch v-model="form.greeting_enabled" />
        <div class="form-tip">访客打开 widget → 系统自动发送一条问候消息</div>
      </el-form-item>

      <el-form-item label="问候内容" v-if="form.greeting_enabled">
        <el-input v-model="form.greeting_text" type="textarea" :rows="2" maxlength="500" show-word-limit
                  placeholder="您好，欢迎光临！请问有什么可以帮您？" />
      </el-form-item>

      <el-divider content-position="left">显示</el-divider>

      <el-form-item label="Widget 标题">
        <el-input v-model="form.widget_title" maxlength="50" style="width:300px" placeholder="在线客服" />
        <div class="form-tip">访客端聊天窗口顶部显示的标题</div>
      </el-form-item>

      <el-divider content-position="left">iPhone APNs 推送（可选）</el-divider>

      <el-form-item label="Push User ID">
        <el-input v-model="form.push_user_id" maxlength="64" style="width:300px" placeholder="例如 2oKFBed8lWS" clearable />
        <div class="form-tip">
          从「消息推送助手」App (App Store 搜)拿到的 User ID。<br/>
          访客发消息时，后端会调 messagepush.luckfast.com 给你 iPhone 推送（锁屏也能收到）。<br/>
          <strong>两项都填才启用，留空则禁用推送。</strong>
        </div>
      </el-form-item>

      <el-form-item label="Push User Key">
        <el-input v-model="form.push_user_key" maxlength="64" style="width:300px" placeholder="例如 3633b2c515edee98e7bf06c792e67912" clearable show-password />
        <div class="form-tip">同上，从「消息推送助手」App 的设置页拿到。</div>
      </el-form-item>

      <el-form-item label="新访客提示音">
        <el-select v-model="form.push_sound_enter" style="width:200px">
          <el-option v-for="s in pushSoundOptions" :key="s.value" :value="s.value" :label="s.label" />
        </el-select>
        <div class="form-tip">新访客打开 widget 时，iPhone 推送用的提示音（luckfast 0-15 共 16 种，每个声音不同，请自己听效果）</div>
      </el-form-item>

      <el-form-item label="新消息提示音">
        <el-select v-model="form.push_sound_message" style="width:200px">
          <el-option v-for="s in pushSoundOptions" :key="s.value" :value="s.value" :label="s.label" />
        </el-select>
        <div class="form-tip">已有会话中访客发新消息时，iPhone 推送用的提示音</div>
      </el-form-item>

      <el-form-item label="语音来电提示音">
        <el-select v-model="form.push_sound_call" style="width:200px">
          <el-option v-for="s in pushSoundOptions" :key="s.value" :value="s.value" :label="s.label" />
        </el-select>
        <div class="form-tip">访客点击「电话」按钮发起语音通话时，iPhone 推送用的提示音（点推送拉起 App 接听）</div>
      </el-form-item>

      <el-form-item>
        <el-button type="primary" :loading="saving" @click="save">保 存</el-button>
        <el-button @click="load">重置</el-button>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<style scoped>
.settings-card { max-width: 720px; }
.form-tip { font-size: 12px; color: #909399; margin-top: 4px; line-height: 1.4; }
</style>
