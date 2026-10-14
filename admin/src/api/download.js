// 文件下载：后台登录态是 HttpOnly Cookie，必须走统一的 http 封装（带 Cookie、401 自动续期），
// 以 blob 方式接收后用 a 标签触发保存；文件名优先取 Content-Disposition 的 filename*。
import { ElMessage } from 'element-plus'
import http from './http'
import { parseContentDispositionFilename, sanitizeFilename } from '../modules/download'

// 导出最多 50000 条消息，生成文件需要时间，单独放宽超时
const DOWNLOAD_TIMEOUT_MS = 120 * 1000
const JSON_ERROR_MAX_BYTES = 64 * 1024

function headerOf(res, name) {
  const headers = res?.headers
  if (!headers) return ''
  if (typeof headers.get === 'function') {
    const value = headers.get(name)
    if (value) return String(value)
  }
  return String(headers[name] || headers[name.toLowerCase()] || '')
}

async function readJsonBlob(blob) {
  try {
    if (blob.size > JSON_ERROR_MAX_BYTES) return null
    return JSON.parse(await blob.text())
  } catch {
    return null
  }
}

export function saveBlob(blob, filename) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener'
  document.body.appendChild(link)
  try {
    link.click()
  } finally {
    document.body.removeChild(link)
    // 延迟回收：部分浏览器在 click 返回后才开始读取 blob
    setTimeout(() => URL.revokeObjectURL(url), 60 * 1000)
  }
}

// config 为 axios 请求配置（method/url/params/data）；返回 { filename, size }。
export async function requestDownload(config, fallbackName) {
  const res = await http.request({ timeout: DOWNLOAD_TIMEOUT_MS, ...config, responseType: 'blob', rawResponse: true })
  const blob = res?.data
  if (typeof Blob === 'undefined' || !(blob instanceof Blob)) {
    ElMessage.error('下载失败：响应格式不正确')
    throw new Error('invalid download response')
  }
  const contentType = headerOf(res, 'content-type') || blob.type || ''
  // 正常导出一定是文件；返回 JSON 说明是业务错误（如限流、无权限）
  if (/\bjson\b/i.test(contentType)) {
    const body = await readJsonBlob(blob)
    const msg = body?.msg || '下载失败，请稍后重试'
    ElMessage.error(msg)
    const error = new Error(msg)
    error.code = body?.code
    throw error
  }
  if (blob.size === 0) {
    ElMessage.error('下载失败：文件为空')
    throw new Error('empty download')
  }
  const filename = sanitizeFilename(parseContentDispositionFilename(headerOf(res, 'content-disposition')), fallbackName)
  saveBlob(blob, filename)
  return { filename, size: blob.size }
}
