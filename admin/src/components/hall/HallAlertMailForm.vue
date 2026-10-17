<script setup>
// [122] 敏感词拦截邮件通知（契约第 9.2/9.3 节）：启用开关、收件邮箱（1–5 个）、发件邮箱、SMTP 服务器 / 端口 / 加密方式 / 用户名 / 密码。
// 默认值由服务器环境变量提供，前端不写死任何邮箱；接口永不返回密码，密码框留空保存 = 不修改（请求里不带该字段）。
// 测试邮件只使用已保存的配置：有未保存的修改时先提示保存。
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getHallAlertMail, saveHallAlertMail, sendHallAlertMailTest } from '../../api/hallFilter'
import { hallErrorMessage } from '../../modules/hallErrors'
import {
  EMAIL_MAX_CHARS,
  MAIL_TO_MAX,
  SMTP_HOST_MAX_CHARS,
  SMTP_PASSWORD_MAX_CHARS,
  SMTP_PORT_MAX,
  SMTP_PORT_MIN,
  SMTP_SECURITY_OPTIONS,
  SMTP_USERNAME_MAX_CHARS,
  buildMailPayload,
  emptyMailForm,
  isMailConfigData,
  mailSnapshot,
  parseEmailList,
  parseMailConfig,
  suggestSmtpPort,
  validateMailForm,
} from '../../modules/hallFilterForm'

const form = reactive(emptyMailForm())
const passwordSet = ref(false)
const configured = ref(false)
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const loaded = ref(false)
const loadError = ref('')
const savedSnapshot = ref('')
const lastTest = ref(null)
let lastSecurity = form.smtp_security

// 加载完成前不显示校验错误（避免空表单闪现「请填写…」）
const NO_ERRORS = Object.freeze({ enabled: '', to: '', from: '', smtp_host: '', smtp_port: '', smtp_security: '', smtp_username: '', smtp_password: '' })
const errors = computed(() => (loaded.value ? validateMailForm(form).errors : NO_ERRORS))
const toList = computed(() => parseEmailList(form.toText))
const dirty = computed(() => loaded.value && mailSnapshot(form) !== savedSnapshot.value)

function apply(data) {
  const parsed = parseMailConfig(data)
  Object.assign(form, parsed.form)
  lastSecurity = form.smtp_security
  passwordSet.value = parsed.passwordSet
  configured.value = parsed.configured
  savedSnapshot.value = mailSnapshot(form)
  loaded.value = true
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const r = await getHallAlertMail()
    apply(r?.data)
  } catch (e) {
    loadError.value = hallErrorMessage(e, '加载邮件设置失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

async function reload() {
  if (dirty.value) {
    try {
      await ElMessageBox.confirm('当前有未保存的修改，重新加载会丢弃这些修改，确定继续？', '重新加载', {
        type: 'warning', confirmButtonText: '丢弃并重新加载', cancelButtonText: '取消',
      })
    } catch {
      return
    }
  }
  await load()
}

// 切换加密方式时，端口仍是旧方式的默认端口才跟着换（用户自填的端口不动）
function onSecurityChange(next) {
  form.smtp_port = suggestSmtpPort(lastSecurity, next, form.smtp_port)
  lastSecurity = next
}

// 返回是否保存成功；quiet 为 true 时不弹成功提示（由调用方合并提示）
async function save({ quiet = false } = {}) {
  if (!loaded.value || saving.value) return false
  const built = buildMailPayload(form)
  if (!built.ok) {
    ElMessage.warning(built.error)
    return false
  }
  saving.value = true
  try {
    const r = await saveHallAlertMail(built.body)
    if (isMailConfigData(r?.data)) {
      apply(r.data)
    } else {
      // 保存接口未返回完整配置时重新读取，刷新「已配置 / 密码已设置」状态
      form.smtp_password = ''
      try {
        const g = await getHallAlertMail()
        apply(g?.data)
      } catch {
        if (Object.prototype.hasOwnProperty.call(built.body, 'smtp_password')) passwordSet.value = true
        savedSnapshot.value = mailSnapshot(form)
      }
    }
    if (!quiet) ElMessage.success('邮件设置已保存')
    return true
  } catch (e) {
    ElMessage.error(hallErrorMessage(e, '保存邮件设置失败，请稍后重试'))
    return false
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  if (!loaded.value || testing.value || saving.value) return
  if (dirty.value) {
    try {
      await ElMessageBox.confirm('邮件设置有未保存的修改，测试邮件只会使用已保存的配置。是否先保存再发送测试邮件？', '发送测试邮件', {
        type: 'warning', confirmButtonText: '保存并发送', cancelButtonText: '取消',
      })
    } catch {
      return
    }
    if (!(await save({ quiet: true }))) return
  }
  testing.value = true
  lastTest.value = null
  try {
    const r = await sendHallAlertMailTest()
    const to = Array.isArray(r?.data?.to) ? r.data.to.filter(s => typeof s === 'string') : []
    const text = '测试邮件已发送' + (to.length ? '至 ' + to.join('、') : '') + '，请到收件箱（含垃圾邮件）查看'
    lastTest.value = { ok: true, text }
    ElMessage.success(text)
  } catch (e) {
    const text = hallErrorMessage(e, '测试邮件发送失败，请稍后重试')
    lastTest.value = { ok: false, text }
    ElMessage.error(text)
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<template>
  <el-alert
    type="info"
    show-icon
    :closable="false"
    title="拦截到的原文会合并发送到下面的收件邮箱（每封最多 50 条，每 60 秒最多 1 封，失败自动重试）。"
    description="收件、发件邮箱和 SMTP 的默认值由服务器环境变量提供；在这里保存后以这里的设置为准。被拉黑的访客照样屏蔽，但不发邮件。" />

  <el-alert v-if="loadError" type="error" show-icon :closable="false" :title="loadError">
    <el-button size="small" :loading="loading" @click="load">重新加载</el-button>
  </el-alert>

  <el-form v-loading="loading" label-width="120px" label-position="left" @submit.prevent>
    <el-form-item label="配置状态">
      <el-space wrap>
        <el-tag v-if="loaded" :type="configured ? 'success' : 'warning'" data-testid="hall-mail-configured">{{ configured ? '已配置' : '未配置完整' }}</el-tag>
        <el-text v-else size="small" type="info">{{ loading ? '加载中…' : '未加载' }}</el-text>
        <el-tag v-if="dirty" type="warning" effect="plain" data-testid="hall-mail-dirty">有未保存的修改</el-tag>
      </el-space>
    </el-form-item>

    <el-form-item label="邮件通知">
      <el-switch v-model="form.enabled" active-text="开启" inactive-text="关闭" data-testid="hall-mail-enabled" />
    </el-form-item>

    <el-form-item label="收件邮箱" :error="errors.to">
      <el-input
        v-model="form.toText"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 6 }"
        placeholder="一行一个，最多 5 个；开启邮件通知时至少 1 个"
        data-testid="hall-mail-to" />
      <el-text size="small" :type="toList.count > MAIL_TO_MAX ? 'danger' : 'info'">当前 {{ toList.count }} / {{ MAIL_TO_MAX }} 个</el-text>
    </el-form-item>

    <el-form-item label="发件邮箱" :error="errors.from">
      <el-input
        v-model="form.from"
        :maxlength="EMAIL_MAX_CHARS"
        placeholder="发信使用的邮箱地址，一般与 SMTP 用户名相同"
        autocomplete="off"
        data-testid="hall-mail-from" />
    </el-form-item>

    <el-form-item label="SMTP 服务器" :error="errors.smtp_host">
      <el-input
        v-model="form.smtp_host"
        :maxlength="SMTP_HOST_MAX_CHARS"
        placeholder="域名或 IP，例如 smtp.163.com"
        autocomplete="off"
        data-testid="hall-mail-host" />
    </el-form-item>

    <el-form-item label="加密方式" :error="errors.smtp_security">
      <el-radio-group v-model="form.smtp_security" data-testid="hall-mail-security" @change="onSecurityChange">
        <el-radio-button v-for="o in SMTP_SECURITY_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</el-radio-button>
      </el-radio-group>
      <el-alert
        v-if="form.smtp_security === 'none'"
        type="warning"
        show-icon
        :closable="false"
        title="不加密只能连接本机的邮件服务（localhost、127.0.0.1、::1），连接外部邮件服务器请选择 SSL 或 STARTTLS。" />
    </el-form-item>

    <el-form-item label="端口" :error="errors.smtp_port">
      <el-space wrap>
        <el-input-number
          v-model="form.smtp_port"
          :min="SMTP_PORT_MIN"
          :max="SMTP_PORT_MAX"
          :step="1"
          :precision="0"
          step-strictly
          controls-position="right"
          data-testid="hall-mail-port" />
        <el-text size="small" type="info">SSL 通常 465，STARTTLS 通常 587</el-text>
      </el-space>
    </el-form-item>

    <el-form-item label="用户名" :error="errors.smtp_username">
      <el-input
        v-model="form.smtp_username"
        :maxlength="SMTP_USERNAME_MAX_CHARS"
        placeholder="SMTP 登录用户名，一般就是发件邮箱"
        autocomplete="off"
        data-testid="hall-mail-username" />
    </el-form-item>

    <el-form-item label="密码/授权码" :error="errors.smtp_password">
      <el-input
        v-model="form.smtp_password"
        type="password"
        show-password
        :maxlength="SMTP_PASSWORD_MAX_CHARS"
        :placeholder="passwordSet ? '已设置，留空不修改' : '未设置'"
        autocomplete="new-password"
        data-testid="hall-mail-password" />
      <el-text size="small" type="info">163、QQ 等邮箱请填写客户端授权码；密码加密保存，之后不再显示，留空保存不会修改已保存的密码。</el-text>
    </el-form-item>

    <el-form-item>
      <el-space wrap>
        <el-button type="primary" :loading="saving" :disabled="!loaded" data-testid="hall-mail-save" @click="save()">保存邮件设置</el-button>
        <el-button :loading="testing" :disabled="!loaded || saving" data-testid="hall-mail-test" @click="sendTest">发送测试邮件</el-button>
        <el-button :disabled="loading || saving" @click="reload">重新加载</el-button>
      </el-space>
    </el-form-item>
  </el-form>

  <el-alert
    v-if="lastTest"
    :type="lastTest.ok ? 'success' : 'error'"
    show-icon
    :closable="false"
    :title="lastTest.text"
    data-testid="hall-mail-test-result" />
</template>
