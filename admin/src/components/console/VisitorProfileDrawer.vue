<script setup>
// 客户资料面板（契约第 4、6 节）：备注、标签编辑；拉黑 / 解除拉黑。
import { computed, nextTick, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getVisitorProfile, saveVisitorProfile, setVisitorBlocked } from '../../api/visitors'
import {
  NOTE_MAX_LENGTH,
  TAG_MAX_COUNT,
  TAG_MAX_LENGTH,
  normalizeProfile,
  normalizeTags,
  tryAddTag,
  validateNote,
} from '../../modules/visitorProfile'
import { isValidVisitorId, visitorDisplayName } from '../../modules/conversationOps'
import { formatBeijing } from '../../modules/timeFormat'
import BlockVisitorDialog from './BlockVisitorDialog.vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  visitor: { type: Object, default: null },
})
const emit = defineEmits(['update:modelValue', 'saved', 'blocked-change'])

const loading = ref(false)
const saving = ref(false)
const unblocking = ref(false)
const profile = ref(normalizeProfile(null))
const note = ref('')
const tags = ref([])
const tagInput = ref('')
const tagInputVisible = ref(false)
const tagInputRef = ref(null)
const staleNotice = ref(false)
const blockDialog = ref(false)
let loadSeq = 0

const vid = computed(() => props.visitor?.visitor_id || '')
const displayName = computed(() => (props.visitor ? visitorDisplayName(props.visitor) : ''))
const dirty = computed(() => note.value !== profile.value.note || JSON.stringify(tags.value) !== JSON.stringify(profile.value.tags))

async function load() {
  if (!isValidVisitorId(vid.value)) return
  const seq = ++loadSeq
  loading.value = true
  try {
    const r = await getVisitorProfile(vid.value)
    if (seq !== loadSeq) return
    profile.value = normalizeProfile(r?.data)
    note.value = profile.value.note
    tags.value = profile.value.tags.slice()
    staleNotice.value = false
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

// 其他客服修改了资料（profile_updated）：未编辑时直接刷新；正在编辑时只提示，不覆盖输入
function reload() {
  if (!props.modelValue) return
  if (dirty.value) staleNotice.value = true
  else load()
}
defineExpose({ reload })

watch(() => [props.modelValue, vid.value], ([open]) => {
  if (open) {
    tagInput.value = ''
    tagInputVisible.value = false
    load()
  }
})

function showTagInput() {
  tagInputVisible.value = true
  nextTick(() => tagInputRef.value?.focus())
}

function confirmTag() {
  const raw = tagInput.value
  if (raw.trim()) {
    const r = tryAddTag(tags.value, raw)
    if (!r.ok) {
      ElMessage.warning(r.error)
      return
    }
    tags.value = r.tags
  }
  tagInput.value = ''
  tagInputVisible.value = false
}

function removeTag(tag) {
  tags.value = tags.value.filter(t => t !== tag)
}

async function save() {
  const noteCheck = validateNote(note.value)
  if (!noteCheck.ok) { ElMessage.warning(noteCheck.error); return }
  const tagCheck = normalizeTags(tags.value)
  if (!tagCheck.ok) { ElMessage.warning(tagCheck.error); return }
  if (saving.value || !isValidVisitorId(vid.value)) return
  saving.value = true
  try {
    await saveVisitorProfile(vid.value, { note: noteCheck.note, tags: tagCheck.tags })
    profile.value = { ...profile.value, note: noteCheck.note, tags: tagCheck.tags.slice() }
    note.value = noteCheck.note
    tags.value = tagCheck.tags.slice()
    staleNotice.value = false
    ElMessage.success('客户资料已保存')
    emit('saved', { visitor_id: vid.value, ...profile.value })
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    saving.value = false
  }
}

async function unblock() {
  try {
    await ElMessageBox.confirm('解除拉黑后该客户将重新出现在会话列表中，同时解除由该客户产生的 IP 拉黑。确定解除？', '解除拉黑', {
      type: 'warning', confirmButtonText: '解除拉黑', cancelButtonText: '取消',
    })
  } catch { return }
  if (unblocking.value) return
  unblocking.value = true
  try {
    await setVisitorBlocked(vid.value, { blocked: false })
    ElMessage.success('已解除拉黑')
    profile.value = { ...profile.value, blocked: false, ip_blocked: false, block_reason: '', blocked_at: null }
    emit('blocked-change', { visitor_id: vid.value, blocked: false })
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    unblocking.value = false
  }
}

function onBlocked(payload) {
  profile.value = { ...profile.value, blocked: true, ip_blocked: payload?.ip_blocked === true }
  emit('blocked-change', payload)
}
</script>

<template>
  <el-drawer :model-value="modelValue" title="客户资料" size="440px" append-to-body @update:model-value="(v) => emit('update:modelValue', v)">
    <div v-loading="loading">
      <el-alert v-if="staleNotice" type="info" :closable="false" show-icon title="该客户资料已在其他位置更新">
        <el-button link type="primary" size="small" @click="load">加载最新资料（放弃未保存的修改）</el-button>
      </el-alert>
      <el-descriptions :column="1" border size="small">
        <el-descriptions-item label="客户">{{ displayName }}</el-descriptions-item>
        <el-descriptions-item label="访客 ID">{{ vid }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag v-if="profile.blocked" type="danger" size="small">已拉黑</el-tag>
          <el-tag v-else type="success" size="small">正常</el-tag>
          <el-tag v-if="profile.pinned" type="warning" size="small" effect="plain">置顶</el-tag>
        </el-descriptions-item>
        <template v-if="profile.blocked">
          <el-descriptions-item label="拉黑原因">{{ profile.block_reason || '—' }}</el-descriptions-item>
          <el-descriptions-item label="拉黑时间">{{ formatBeijing(profile.blocked_at) }}</el-descriptions-item>
          <el-descriptions-item label="IP 拉黑">{{ profile.ip_blocked ? '是' : '否' }}</el-descriptions-item>
        </template>
      </el-descriptions>

      <el-divider content-position="left">备注与标签</el-divider>
      <el-form label-position="top">
        <el-form-item label="备注">
          <el-input v-model="note" type="textarea" :rows="4" :maxlength="NOTE_MAX_LENGTH" show-word-limit placeholder="仅客服可见，例如：老客户，偏好电话沟通" />
        </el-form-item>
        <el-form-item :label="'标签（最多 ' + TAG_MAX_COUNT + ' 个，每个 1-' + TAG_MAX_LENGTH + ' 字）'">
          <el-space wrap :size="6">
            <el-tag v-for="t in tags" :key="t" closable @close="removeTag(t)">{{ t }}</el-tag>
            <el-input
              v-if="tagInputVisible"
              ref="tagInputRef"
              v-model="tagInput"
              size="small"
              :maxlength="TAG_MAX_LENGTH"
              placeholder="回车确认"
              @keyup.enter="confirmTag"
              @blur="confirmTag" />
            <el-button v-else-if="tags.length < TAG_MAX_COUNT" size="small" @click="showTagInput">新增标签</el-button>
          </el-space>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" :disabled="!dirty" @click="save">保存</el-button>
          <el-button :disabled="!dirty || saving" @click="load">撤销修改</el-button>
        </el-form-item>
      </el-form>

      <el-divider content-position="left">黑名单</el-divider>
      <el-button v-if="profile.blocked" type="success" plain :loading="unblocking" @click="unblock">解除拉黑</el-button>
      <el-button v-else type="danger" plain @click="blockDialog = true">拉黑该客户</el-button>
    </div>
    <BlockVisitorDialog v-model="blockDialog" :visitor="visitor" @done="onBlocked" />
  </el-drawer>
</template>
