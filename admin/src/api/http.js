import axios from 'axios'
import { ElMessage } from 'element-plus'
import router from '../router'

const http = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api',
  timeout: 15000,
  withCredentials: true
})

http.interceptors.request.use((cfg) => cfg)

// [064] refresh lock：多个并发 401 只触发 1 次 /agent/login/refresh
// 解决 [068] 同模式问题：admin 会话过期后所有请求 401 死循环
let _refreshPromise = null

async function refreshToken() {
  // 已经在 refresh 中 → 等待并复用结果
  if (_refreshPromise) return _refreshPromise
  _refreshPromise = (async () => {
    try {
      // 用 fetch 而非 axios，避免触发 http.interceptors 拦截器递归
      const baseURL = import.meta.env.VITE_API_BASE || '/api'
      const r = await fetch(baseURL + '/agent/login/refresh', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
      })
      if (!r.ok) return false
      const d = await r.json()
      if (d && d.code === 0 && d.token) {
        return true
      }
    } catch (_) { /* network err */ }
    return false
  })().finally(() => {
    // 100ms 后释放锁，给等待中的请求时间拿结果
    setTimeout(() => { _refreshPromise = null }, 100)
  })
  return _refreshPromise
}

function gotoLogin() {
  localStorage.removeItem('cs_admin_token')
  localStorage.removeItem('cs_admin_agent')
  // 避免在已经在 /login 的页面重复 push 报 NavigationDuplicated
  if (router.currentRoute?.value?.path !== '/login') {
    router.push('/login')
  }
}

// [113] blob 下载请求的错误体也是 Blob：先解析成 JSON，401 续期判断与错误提示才能拿到 code/msg。
// 错误体只会是很小的 JSON，超过 64KB 视为异常不解析，避免把大文件读进内存。
async function normalizeBlobErrorBody(res) {
  if (!res || typeof Blob === 'undefined' || !(res.data instanceof Blob)) return
  try {
    if (res.data.size > 64 * 1024) { res.data = {}; return }
    const parsed = JSON.parse(await res.data.text())
    res.data = parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    res.data = {}
  }
}

// [113] 请求级开关（axios 会把自定义配置原样带到 res.config / err.config）：
//   rawResponse: true  返回完整响应对象（下载文件需要读取 Content-Disposition）
//   silent: true       失败时不弹全局错误提示（后台静默刷新类请求自行处理）
http.interceptors.response.use(
  (res) => {
    if (res.config?.rawResponse) return res
    const d = res.data
    if (d && typeof d === 'object' && 'code' in d && d.code !== 0) {
      if (!res.config?.silent) ElMessage.error(d.msg || '操作失败')
      return Promise.reject(d)
    }
    return d
  },
  async (err) => {
    const res = err.response
    await normalizeBlobErrorBody(res)
    const status = res?.status
    if (status !== 401) {
      if (!err.config?.silent) ElMessage.error(res?.data?.msg || '网络错误')
      return Promise.reject(err)
    }
    // [064] 区分 40102 (token expired) vs 其他 401（40101 未登录 / 40103 token 无效）
    const code = res?.data?.code
    if (code !== 40102) {
      gotoLogin()
      ElMessage.error(res?.data?.msg || '请重新登录')
      return Promise.reject(err)
    }
    // 试图 refresh
    const ok = await refreshToken()
    if (!ok) {
      gotoLogin()
      ElMessage.error('登录已过期，请重新登录')
      return Promise.reject(err)
    }
    // refresh 成功 → 用新 token 重试原请求一次
    // 注意：不要走 http.request 触发 interceptor，直接复用 axios 实例的低层 fetch
    try {
      const opts = err.config
      opts.headers = opts.headers || {}
      return await http.request(opts)
    } catch (e2) {
      // 重试也失败 → 跳登录
      gotoLogin()
      ElMessage.error('登录已过期，请重新登录')
      return Promise.reject(e2)
    }
  }
)

export default http
