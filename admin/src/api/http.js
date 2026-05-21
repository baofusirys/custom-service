import axios from 'axios'
import { ElMessage } from 'element-plus'
import router from '../router'

const http = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api',
  timeout: 15000
})

http.interceptors.request.use((cfg) => {
  const tok = localStorage.getItem('cs_admin_token')
  if (tok) cfg.headers.Authorization = 'Bearer ' + tok
  return cfg
})

http.interceptors.response.use(
  (res) => {
    const d = res.data
    if (d && typeof d === 'object' && 'code' in d && d.code !== 0) {
      ElMessage.error(d.msg || '操作失败')
      return Promise.reject(d)
    }
    return d
  },
  (err) => {
    const status = err.response?.status
    if (status === 401) {
      localStorage.removeItem('cs_admin_token')
      router.push('/login')
    }
    ElMessage.error(err.response?.data?.msg || '网络错误')
    return Promise.reject(err)
  }
)

export default http
