<script setup>
import { useRouter, RouterView, useRoute } from 'vue-router'
import { useSession } from '../store/session'
import { computed } from 'vue'

const router = useRouter()
const route = useRoute()
const session = useSession()
const active = computed(() => route.path)

function logout() {
  session.clear()
  router.push('/login')
}
</script>

<template>
  <el-container style="height:100vh">
    <el-aside width="200px" style="background:#001529">
      <div style="color:#fff;text-align:center;padding:18px;font-size:16px;letter-spacing:1px">客服工作台</div>
      <el-menu :default-active="active" router background-color="#001529" text-color="#bfcbd9" active-text-color="#409EFF">
        <el-menu-item index="/console">在线会话</el-menu-item>
        <el-menu-item index="/history">历史记录</el-menu-item>
        <el-menu-item index="/agents" v-if="session.agent?.role === 'admin'">客服管理</el-menu-item>
        <el-menu-item index="/settings" v-if="session.agent?.role === 'admin'">系统设置</el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header style="background:#fff;border-bottom:1px solid #e6e6e6;display:flex;align-items:center;justify-content:space-between">
        <div>{{ session.agent?.nickname || session.agent?.username }}（{{ session.agent?.role === 'admin' ? '管理员' : '客服' }}）</div>
        <el-button link type="primary" @click="logout">退出登录</el-button>
      </el-header>
      <el-main>
        <RouterView />
      </el-main>
    </el-container>
  </el-container>
</template>
