import { defineStore } from 'pinia'

export const useSession = defineStore('session', {
  state: () => ({
    // 管理后台使用 HttpOnly cs_session Cookie，JWT 不落入可被脚本读取的存储。
    token: '',
    agent: JSON.parse(localStorage.getItem('cs_admin_agent') || 'null')
  }),
  actions: {
    setSession(_token, agent) {
      this.token = ''
      this.agent = agent
      localStorage.removeItem('cs_admin_token')
      localStorage.setItem('cs_admin_agent', JSON.stringify(agent))
    },
    clear() {
      this.token = ''
      this.agent = null
      localStorage.removeItem('cs_admin_token')
      localStorage.removeItem('cs_admin_agent')
      // [070] 登出清掉本地消息缓存（换账号不串数据）
      try {
        const idx = JSON.parse(localStorage.getItem('cs_msgs:_index') || '[]')
        for (const c of idx) localStorage.removeItem('cs_msgs:' + c)
      } catch {}
      localStorage.removeItem('cs_msgs:_index')
    }
  }
})
