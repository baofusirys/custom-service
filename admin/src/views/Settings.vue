<script setup>
import { computed, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import { listSounds, playSound, unlockAudio } from '../api/sound'
import { useSession } from '../store/session'
import DeleteAllChats from '../components/settings/DeleteAllChats.vue'
import {
  AUTO_CLEAN_DEFAULT_DAYS,
  AUTO_CLEAN_MAX_DAYS,
  AUTO_CLEAN_MIN_DAYS,
  parseAutoCleanDays,
  serializeAutoCleanDays,
} from '../modules/autoClean'
import {
  HALL_RECOMMENDED_MAX_CAMERAS,
  HALL_SERVER_BANDWIDTH_MBPS,
  HALL_SETTING_DEFAULTS,
  HALL_SETTING_LIMITS,
  estimateHallBandwidth,
  parseHallSettings,
  validateHallSettings,
} from '../modules/hallSettings'

const session = useSession()
const isAdmin = computed(() => session.agent?.role === 'admin')

const loading = ref(false)
const saving = ref(false)
const form = ref({
  agent_notify_sound: 'agent1',
  visitor_notify_sound: 'visitor1',
  notify_visitor_enter: true,
  session_fresh_minutes: 30,
  greeting_enabled: true,
  greeting_text: '您好，欢迎光临！请问有什么可以帮您？',
  widget_title: '在线客服',
  voice_call_hint: '直接呼叫客服',
  // luckfast APNs 推送：访客发消息时后端调 messagepush.luckfast.com 推到客服 iPhone
  // 两项都填才启用；留空即禁用，不影响其它功能
  push_user_id: '',
  push_user_key: '',
  // 三种推送场景独立的提示音（luckfast 0-15 共 16 种）
  // 注：只控制 iPhone 系统通知栏弹通知时播的音；App 内界面来电时本地循环的
  // voice-ring.mp3（[036]）跟这个无关
  push_sound_enter: '1',    // 新访客打开 widget
  push_sound_message: '9',  // 已有会话中访客发新消息
  push_sound_call: '4',     // [043] 语音来电（系统通知栏）
  // [113] 自动清理：关闭 / 7-3650 天（存储为字符串 auto_clean_days，"0" 表示关闭）
  auto_clean_enabled: false,
  auto_clean_days: AUTO_CLEAN_DEFAULT_DAYS,
  // [119] 多人大厅（契约第 3 节）；仅管理员可见可改
  ...HALL_SETTING_DEFAULTS
})

// [119] 按当前上限粗估大厅峰值带宽（假设 2 人同时说话），超出服务器带宽时提示
const hallBandwidth = computed(() => estimateHallBandwidth({
  participants: form.value.hall_max_participants,
  maxCameras: form.value.hall_max_cameras,
  videoEnabled: form.value.hall_video_enabled,
}))

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
    form.value.session_fresh_minutes = parseInt(d.session_fresh_minutes) || 30
    form.value.greeting_enabled = d.greeting_enabled !== 'false'
    form.value.greeting_text = d.greeting_text || form.value.greeting_text
    form.value.widget_title = d.widget_title || form.value.widget_title
    form.value.voice_call_hint = d.voice_call_hint || '直接呼叫客服'
    form.value.push_user_id = d.push_user_id || ''
    form.value.push_user_key = d.push_user_key || ''
    form.value.push_sound_enter = d.push_sound_enter || '1'
    form.value.push_sound_message = d.push_sound_message || '9'
    form.value.push_sound_call = d.push_sound_call || '4'
    const autoClean = parseAutoCleanDays(d.auto_clean_days)
    form.value.auto_clean_enabled = autoClean.enabled
    form.value.auto_clean_days = autoClean.days
    Object.assign(form.value, parseHallSettings(d))
  } finally {
    loading.value = false
  }
}

async function save() {
  const autoClean = serializeAutoCleanDays(form.value.auto_clean_enabled, form.value.auto_clean_days)
  if (!autoClean.ok) {
    ElMessage.warning(autoClean.error)
    return
  }
  const hall = validateHallSettings(form.value)
  if (isAdmin.value && !hall.ok) {
    ElMessage.warning(hall.error)
    return
  }
  saving.value = true
  try {
    const payload = {
      agent_notify_sound: form.value.agent_notify_sound,
      visitor_notify_sound: form.value.visitor_notify_sound,
      notify_visitor_enter: form.value.notify_visitor_enter ? 'true' : 'false',
      session_fresh_minutes: String(form.value.session_fresh_minutes || 30),
      greeting_enabled: form.value.greeting_enabled ? 'true' : 'false',
      greeting_text: form.value.greeting_text || '',
      widget_title: form.value.widget_title || '在线客服',
      voice_call_hint: form.value.voice_call_hint || '直接呼叫客服',
      push_user_id: (form.value.push_user_id || '').trim(),
      push_user_key: (form.value.push_user_key || '').trim(),
      push_sound_enter: form.value.push_sound_enter || '1',
      push_sound_message: form.value.push_sound_message || '9',
      push_sound_call: form.value.push_sound_call || '4',
      auto_clean_days: autoClean.value,
      ...(isAdmin.value && hall.ok ? hall.payload : {})
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

      <el-form-item label="新访客进入提醒">
        <el-switch v-model="form.notify_visitor_enter" />
        <div class="form-tip">
          开启：新访客打开带 widget 的页面 → 客服端弹提醒 + 响提示音。<br/>
          关闭：新访客进入<strong>不弹不响</strong>（访客真正发来消息仍照常提醒，不受影响）。
        </div>
      </el-form-item>

      <el-form-item label="会话保持时长">
        <el-input-number v-model="form.session_fresh_minutes" :min="1" :max="1440" :step="5" controls-position="right" style="width:160px" />
        <span style="margin-left:8px;color:#909399">分钟</span>
        <div class="form-tip">
          访客离开超过这个时长再回来，算「新一轮咨询」：系统关闭旧会话、开新会话并重新问候/提醒（默认 30 分钟，范围 1–1440）。<br/>
          注：旧会话<strong>有未读时不会关</strong>，且新会话<strong>继承原客服</strong>，不丢失「已联系」记录。
        </div>
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

      <el-form-item label="语音按钮提示">
        <el-input v-model="form.voice_call_hint" maxlength="20" style="width:300px" placeholder="直接呼叫客服" />
        <div class="form-tip">访客端 widget 电话按钮旁边显示的小字（指引访客点击呼叫）</div>
      </el-form-item>

      <!-- [119] 多人大厅：仅管理员可见 -->
      <template v-if="isAdmin">
        <el-divider content-position="left">多人大厅</el-divider>

        <el-form-item label="启用大厅">
          <el-switch v-model="form.hall_enabled" active-text="开启" inactive-text="关闭" />
          <div class="form-tip">关闭后不再允许进入大厅，已在大厅里的访客和客服会被移出；私聊不受影响。</div>
        </el-form-item>

        <el-form-item label="允许视频">
          <el-switch v-model="form.hall_video_enabled" active-text="开启" inactive-text="关闭" />
          <div class="form-tip">开启后客服和访客都可以开摄像头（受下方上限约束）；关闭时立即收回所有人的摄像头权限。</div>
        </el-form-item>

        <el-form-item label="同时开麦访客上限">
          <el-input-number
            v-model="form.hall_max_speakers"
            :min="HALL_SETTING_LIMITS.hall_max_speakers.min"
            :max="HALL_SETTING_LIMITS.hall_max_speakers.max"
            :step="1"
            :precision="0"
            step-strictly
            controls-position="right" />
          <div class="form-tip">范围 {{ HALL_SETTING_LIMITS.hall_max_speakers.min }}–{{ HALL_SETTING_LIMITS.hall_max_speakers.max }} 人，默认 {{ HALL_SETTING_DEFAULTS.hall_max_speakers }}；客服开麦不占名额。</div>
        </el-form-item>

        <el-form-item label="同时开摄像头上限">
          <el-input-number
            v-model="form.hall_max_cameras"
            :min="HALL_SETTING_LIMITS.hall_max_cameras.min"
            :max="HALL_SETTING_LIMITS.hall_max_cameras.max"
            :step="1"
            :precision="0"
            step-strictly
            controls-position="right" />
          <div class="form-tip">范围 {{ HALL_SETTING_LIMITS.hall_max_cameras.min }}–{{ HALL_SETTING_LIMITS.hall_max_cameras.max }} 路（含客服），默认 {{ HALL_SETTING_DEFAULTS.hall_max_cameras }}。</div>
        </el-form-item>

        <el-form-item label="大厅人数上限">
          <el-input-number
            v-model="form.hall_max_participants"
            :min="HALL_SETTING_LIMITS.hall_max_participants.min"
            :max="HALL_SETTING_LIMITS.hall_max_participants.max"
            :step="10"
            :precision="0"
            controls-position="right" />
          <div class="form-tip">范围 {{ HALL_SETTING_LIMITS.hall_max_participants.min }}–{{ HALL_SETTING_LIMITS.hall_max_participants.max }} 人，默认 {{ HALL_SETTING_DEFAULTS.hall_max_participants }}；满员时新访客进不了大厅，但仍可使用私聊。</div>
        </el-form-item>

        <el-form-item label="带宽说明">
          <el-alert
            :type="hallBandwidth.downstream > HALL_SERVER_BANDWIDTH_MBPS || form.hall_max_cameras > HALL_RECOMMENDED_MAX_CAMERAS ? 'warning' : 'info'"
            :closable="false"
            show-icon
            :title="'服务器公网带宽 ' + HALL_SERVER_BANDWIDTH_MBPS + 'Mbps，且与主站共用；摄像头建议不超过 ' + HALL_RECOMMENDED_MAX_CAMERAS + ' 路。'"
            :description="'按当前上限粗估（' + form.hall_max_participants + ' 人在线、2 人同时说话' + (form.hall_video_enabled ? '、每人观看 ' + Math.min(form.hall_max_cameras, 2) + ' 路低清视频' : '') + '），大厅峰值下行约 ' + hallBandwidth.downstream + 'Mbps（语音约 ' + hallBandwidth.audio + 'Mbps、视频约 ' + hallBandwidth.video + 'Mbps）。每路摄像头上行约 0.6Mbps（360p＋180p 联播），观看端默认只收低清层。超出服务器带宽时会卡顿，并影响主站访问。'" />
        </el-form-item>
      </template>

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
        <div class="form-tip">访客发起语音通话时，iPhone 系统通知栏弹通知的提示音（点通知拉起 App 接听）。注：App 内看到来电界面后会循环播 voice-ring.mp3 内置铃声，跟此项独立。</div>
      </el-form-item>

      <el-divider content-position="left">自动清理</el-divider>

      <el-form-item label="自动清理聊天记录">
        <el-switch v-model="form.auto_clean_enabled" active-text="开启" inactive-text="关闭" />
        <div class="form-tip">每天北京时间 04:30 把最近活动早于设定天数的会话移入回收站（置顶客户除外），回收站保留 7 天后自动彻底删除。</div>
      </el-form-item>

      <el-form-item v-if="form.auto_clean_enabled" label="清理早于（天）">
        <el-input-number
          v-model="form.auto_clean_days"
          :min="AUTO_CLEAN_MIN_DAYS"
          :max="AUTO_CLEAN_MAX_DAYS"
          :step="1"
          :precision="0"
          step-strictly
          controls-position="right" />
        <div class="form-tip">范围 {{ AUTO_CLEAN_MIN_DAYS }}–{{ AUTO_CLEAN_MAX_DAYS }} 天；最近活动早于该天数的会话会被自动清理。</div>
      </el-form-item>

      <el-form-item>
        <el-button type="primary" :loading="saving" @click="save">保 存</el-button>
        <el-button @click="load">重置</el-button>
      </el-form-item>
    </el-form>

    <!-- [113] 危险操作放在页面最下方，仅管理员可见 -->
    <template v-if="session.agent?.role === 'admin'">
      <el-divider content-position="left">危险操作</el-divider>
      <DeleteAllChats />
    </template>
  </el-card>
</template>

<style scoped>
.settings-card { max-width: 720px; }
.form-tip { font-size: 12px; color: #909399; margin-top: 4px; line-height: 1.4; }
</style>
