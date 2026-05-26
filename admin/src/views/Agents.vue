<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import dayjs from 'dayjs'
import http from '../api/http'

const list = ref([])
const dialog = ref(false)
const form = ref({ username: '', password: '', role: 'agent', nickname: '' })

async function load() {
  const r = await http.get('/admin/agents')
  list.value = r.data || []
}

async function create() {
  // [052] 前端宽松前置校验（后端有严格校验做最终把关）
  if (!form.value.username || (form.value.password || '').length < 8) {
    ElMessage.warning('用户名必填，密码至少 8 位')
    return
  }
  try {
    await http.post('/admin/agents', form.value)
    ElMessage.success('已创建')
    dialog.value = false
    form.value = { username: '', password: '', role: 'agent', nickname: '' }
    load()
  } catch (e) {
    // [052] 按后端返回 code 走差异化提示（不再统一弹"用户名已存在或失败"歧义文案）
    const code = e?.response?.data?.code
    const msg = e?.response?.data?.msg || '创建失败'
    // 40007 用户名冲突：清掉 username 提示用户改名（保留密码/昵称/角色省得重填）
    if (code === 40007) {
      ElMessage.warning(msg)
      form.value.username = ''
    } else if (code === 40010 || code === 40011 || code === 40012) {
      // 用户名格式 / 昵称过长 / 角色不合法 → warning + 文案精确告诉用户改哪
      ElMessage.warning(msg)
    } else {
      // 50019 系统繁忙 / 50419 超时 / 其他 → error
      ElMessage.error(msg)
    }
  }
}

async function toggle(row) {
  await ElMessageBox.confirm(`确定${row.active ? '禁用' : '启用'}账号「${row.username}」？`, '确认')
  await http.post('/admin/agents/active', { id: row.id, active: !row.active })
  load()
}

onMounted(load)
</script>

<template>
  <el-card>
    <template #header>
      <div style="display:flex;justify-content:space-between;align-items:center">
        <span>客服 / 管理员 列表</span>
        <el-button type="primary" @click="dialog = true">新建账号</el-button>
      </div>
    </template>
    <el-table :data="list" border>
      <el-table-column prop="id" label="ID" width="80" />
      <el-table-column prop="username" label="用户名" />
      <el-table-column prop="nickname" label="昵称" />
      <el-table-column prop="role" label="角色" width="120">
        <template #default="{ row }">
          <el-tag :type="row.role === 'admin' ? 'danger' : ''">{{ row.role === 'admin' ? '管理员' : '客服' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag :type="row.active ? 'success' : 'info'">{{ row.active ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近登录" width="180">
        <template #default="{ row }">
          {{ row.last_login ? dayjs(row.last_login).format('YYYY-MM-DD HH:mm:ss') : '从未' }}
        </template>
      </el-table-column>
      <el-table-column label="操作" width="120">
        <template #default="{ row }">
          <el-button link :type="row.active ? 'danger' : 'success'" @click="toggle(row)">
            {{ row.active ? '禁用' : '启用' }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialog" title="新建账号" width="420">
      <el-form>
        <el-form-item label="用户名"><el-input v-model="form.username" /></el-form-item>
        <el-form-item label="密码"><el-input v-model="form.password" type="password" show-password /></el-form-item>
        <el-form-item label="昵称"><el-input v-model="form.nickname" /></el-form-item>
        <el-form-item label="角色">
          <el-radio-group v-model="form.role">
            <el-radio value="agent">客服</el-radio>
            <el-radio value="admin">管理员</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" @click="create">创建</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>
