<script setup>
// 快捷回复管理（契约第 3 节）：团队话术（仅管理员可增删改）+ 我的话术。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useSession } from '../store/session'
import { createQuickReply, deleteQuickReply, listQuickReplies, updateQuickReply } from '../api/quickReplies'
import {
  QR_CONTENT_MAX,
  QR_PERSONAL_LIMIT,
  QR_SORT_MAX,
  QR_SORT_MIN,
  QR_TEAM_LIMIT,
  QR_TITLE_MAX,
  canManageQuickReply,
  filterQuickReplies,
  splitByScope,
  validateQuickReply,
} from '../modules/quickReply'
import { formatBeijing } from '../modules/timeFormat'

const session = useSession()
const isAdmin = computed(() => session.agent?.role === 'admin')
const loading = ref(false)
const saving = ref(false)
const list = ref([])
const tab = ref('team')
const keyword = ref('')
const dialogVisible = ref(false)
const editingId = ref(null)
const form = ref({ scope: 'personal', title: '', content: '', sort_order: 0 })

const groups = computed(() => splitByScope(list.value))
const rows = computed(() => filterQuickReplies(tab.value === 'team' ? groups.value.team : groups.value.personal, keyword.value))
const canCreateInTab = computed(() => tab.value === 'personal' || isAdmin.value)

async function load() {
  loading.value = true
  try {
    const r = await listQuickReplies()
    list.value = Array.isArray(r?.data) ? r.data.filter(it => it && (it.scope === 'team' || it.scope === 'personal')) : []
  } catch {
    // 错误提示由 http 拦截器统一弹出
  } finally {
    loading.value = false
  }
}

function openCreate() {
  const scope = tab.value === 'team' && isAdmin.value ? 'team' : 'personal'
  const count = scope === 'team' ? groups.value.team.length : groups.value.personal.length
  const limit = scope === 'team' ? QR_TEAM_LIMIT : QR_PERSONAL_LIMIT
  if (count >= limit) {
    ElMessage.warning(`${scope === 'team' ? '团队' : '我的'}话术已达上限 ${limit} 条，请先删除不用的话术`)
    return
  }
  editingId.value = null
  form.value = { scope, title: '', content: '', sort_order: 0 }
  dialogVisible.value = true
}

function openEdit(row) {
  if (!canManageQuickReply(row, isAdmin.value)) return
  editingId.value = row.id
  form.value = {
    scope: row.scope,
    title: typeof row.title === 'string' ? row.title : '',
    content: typeof row.content === 'string' ? row.content : '',
    sort_order: Number.isInteger(Number(row.sort_order)) ? Number(row.sort_order) : 0,
  }
  dialogVisible.value = true
}

async function submit() {
  const check = validateQuickReply(form.value)
  if (!check.ok) { ElMessage.warning(check.error); return }
  if (form.value.scope === 'team' && !isAdmin.value) { ElMessage.warning('团队话术仅管理员可管理'); return }
  if (saving.value) return
  saving.value = true
  try {
    if (editingId.value === null) await createQuickReply({ scope: form.value.scope, ...check.value })
    else await updateQuickReply(editingId.value, check.value)
    ElMessage.success('已保存')
    dialogVisible.value = false
    await load()
  } catch {
    // 错误提示（含 40312 无权限 / 40912 数量上限）由 http 拦截器统一弹出
  } finally {
    saving.value = false
  }
}

async function remove(row) {
  if (!canManageQuickReply(row, isAdmin.value)) return
  try {
    await ElMessageBox.confirm(`确定删除话术「${row.title || '（无标题）'}」？`, '删除快捷回复', {
      type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消',
    })
  } catch { return }
  try {
    await deleteQuickReply(row.id)
    ElMessage.success('已删除')
    await load()
  } catch {
    // 错误提示由 http 拦截器统一弹出
  }
}

onMounted(load)
</script>

<template>
  <el-card>
    <template #header>
      <el-row justify="space-between" align="middle">
        <el-text tag="b">快捷回复</el-text>
        <el-space :size="8">
          <el-input v-model="keyword" clearable placeholder="搜索标题或内容" />
          <el-button :loading="loading" @click="load">刷新</el-button>
          <el-button type="primary" :disabled="!canCreateInTab" @click="openCreate">新增话术</el-button>
        </el-space>
      </el-row>
    </template>

    <el-tabs v-model="tab">
      <el-tab-pane :label="'团队话术（' + groups.team.length + '/' + QR_TEAM_LIMIT + '）'" name="team" />
      <el-tab-pane :label="'我的话术（' + groups.personal.length + '/' + QR_PERSONAL_LIMIT + '）'" name="personal" />
    </el-tabs>
    <el-alert
      v-if="tab === 'team' && !isAdmin"
      type="info"
      :closable="false"
      show-icon
      title="团队话术由管理员统一维护，您可以在聊天输入框旁的「快捷回复」中直接使用。" />

    <el-table v-loading="loading" :data="rows" border empty-text="暂无话术">
      <el-table-column prop="sort_order" label="排序" width="80" />
      <el-table-column label="标题" width="200" show-overflow-tooltip>
        <template #default="{ row }">{{ row.title || '（无标题）' }}</template>
      </el-table-column>
      <el-table-column prop="content" label="内容" show-overflow-tooltip />
      <el-table-column label="更新时间（北京时间）" width="180">
        <template #default="{ row }">{{ formatBeijing(row.updated_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="140">
        <template #default="{ row }">
          <template v-if="canManageQuickReply(row, isAdmin)">
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
          <el-text v-else size="small" type="info">仅管理员可改</el-text>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialogVisible" :title="editingId === null ? '新增话术' : '编辑话术'" width="560" append-to-body>
      <el-form label-width="80px">
        <el-form-item label="类型">
          <el-radio-group v-model="form.scope" :disabled="editingId !== null">
            <el-radio value="personal">我的话术</el-radio>
            <el-radio value="team" :disabled="!isAdmin">团队话术</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="标题">
          <el-input v-model="form.title" :maxlength="QR_TITLE_MAX" show-word-limit placeholder="选填，便于搜索" />
        </el-form-item>
        <el-form-item label="内容" required>
          <el-input v-model="form.content" type="textarea" :rows="6" :maxlength="QR_CONTENT_MAX" show-word-limit placeholder="点击话术后会填入聊天输入框" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="form.sort_order" :min="QR_SORT_MIN" :max="QR_SORT_MAX" :precision="0" :step="1" step-strictly controls-position="right" />
          <el-text size="small" type="info">数字越小越靠前</el-text>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>
