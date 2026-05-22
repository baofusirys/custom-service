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
        { path: 'settings', component: () => import('../views/Settings.vue') }
      ]
    }
  ]
})

router.beforeEach((to) => {
  const tok = localStorage.getItem('cs_admin_token')
  if (to.path !== '/login' && !tok) return '/login'
  if (to.path === '/login' && tok) return '/console'
})

export default router
