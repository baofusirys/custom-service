import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory('/admin/'),
  routes: [
    { path: '/login', component: () => import('../views/Login.vue') },
    {
      path: '/',
      component: () => import('../views/Layout.vue'),
      redirect: '/console',
      children: [
        { path: 'console', component: () => import('../views/Console.vue') },
        { path: 'history', component: () => import('../views/History.vue') },
        { path: 'agents', component: () => import('../views/Agents.vue') },
        { path: 'settings', component: () => import('../views/Settings.vue') },
        // [113] 0.8.0 新页面；adminOnly 仅控制前端可见性，真正的权限由后端 /api/admin/* 校验
        { path: 'quick-replies', component: () => import('../views/QuickReplies.vue') },
        { path: 'blocked', component: () => import('../views/Blocked.vue') },
        { path: 'recycle-bin', component: () => import('../views/RecycleBin.vue'), meta: { adminOnly: true } }
      ]
    }
  ]
})

function currentRole() {
  try {
    return JSON.parse(localStorage.getItem('cs_admin_agent') || 'null')?.role || ''
  } catch {
    return ''
  }
}

router.beforeEach((to) => {
  const agent = localStorage.getItem('cs_admin_agent')
  if (to.path !== '/login' && !agent) return '/login'
  if (to.path === '/login' && agent) return '/console'
  if (to.meta?.adminOnly && currentRole() !== 'admin') return '/console'
})

export default router
