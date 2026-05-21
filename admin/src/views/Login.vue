<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import http from '../api/http'
import { useSession } from '../store/session'

const router = useRouter()
const session = useSession()
const form = ref({ username: '', password: '' })
const loading = ref(false)

async function submit() {
  if (!form.value.username || !form.value.password) return
  loading.value = true
  try {
    const r = await http.post('/agent/login', form.value)
    session.setSession(r.token, r.agent)
    router.push('/console')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <el-container style="height:100vh;align-items:center;justify-content:center;background:#f5f7fa">
    <el-card style="width:380px">
      <template #header>
        <div style="text-align:center;font-size:18px">客服工作台登录</div>
      </template>
      <el-form @keyup.enter="submit">
        <el-form-item label="账号">
          <el-input v-model="form.username" autofocus placeholder="用户名" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.password" type="password" show-password placeholder="密码" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="loading" style="width:100%" @click="submit">登 录</el-button>
        </el-form-item>
      </el-form>
      <el-alert type="info" :closable="false" show-icon>
        首次登录后请立即在「客服管理」修改密码。
      </el-alert>
    </el-card>
  </el-container>
</template>
