import { defineStore } from 'pinia'

export const useSession = defineStore('session', {
  state: () => ({
    token: localStorage.getItem('cs_admin_token') || '',
    agent: JSON.parse(localStorage.getItem('cs_admin_agent') || 'null')
  }),
  actions: {
    setSession(token, agent) {
      this.token = token
      this.agent = agent
      localStorage.setItem('cs_admin_token', token)
      localStorage.setItem('cs_admin_agent', JSON.stringify(agent))
    },
    clear() {
      this.token = ''
      this.agent = null
      localStorage.removeItem('cs_admin_token')
      localStorage.removeItem('cs_admin_agent')
    }
  }
})
