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
  if (!form.value.username || (form.value.password || '').length < 8) {
    ElMessage.warning('用户名必填，密码至少 8 位')
    return
  }
  await http.post('/admin/agents', form.value)
  ElMessage.success('已创建')
  dialog.value = false
  form.value = { username: '', password: '', role: 'agent', nickname: '' }
  load()
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
