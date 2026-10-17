// 多人大厅「敏感词屏蔽 / 邮件告警」管理接口（契约 _internal/reports/hall-contract.md 第 9.2 节，仅管理员）。
// 所有请求 silent：错误提示统一由 modules/hallErrors.js 映射为中文（40062/40063/50271 会带上服务端给出的具体原因），
// 避免 http 拦截器的通用提示重复弹出；401 续期/跳登录仍由拦截器统一处理。
// 请求体由 modules/hallFilterForm.js 构建并预校验，这里不做业务拼装。
import http from './http'
import { buildEventsQuery, EVENTS_PAGE_SIZE } from '../modules/hallFilterForm'

// 测试邮件需要连接外部 SMTP（含 TLS 握手与重试前的超时），给足等待时间，避免前端先超时而后端其实已发出
const MAIL_TEST_TIMEOUT_MS = 60000

export function getHallFilter() {
  return http.get('/admin/hall/filter', { silent: true })
}

// body: { enabled, mode, categories, custom_words, allow_words, allow_domains }（全部必填）
export function saveHallFilter(body) {
  return http.post('/admin/hall/filter', body, { silent: true })
}

// body: { content, config? }；config 结构同 saveHallFilter，用于保存前预览
export function testHallFilter(body) {
  return http.post('/admin/hall/filter/test', body, { silent: true })
}

export function listHallFilterEvents({ before = '', limit = EVENTS_PAGE_SIZE } = {}) {
  const q = buildEventsQuery({ before, limit })
  if (!q.ok) return Promise.reject({ code: -1, msg: q.error })
  return http.get('/admin/hall/filter/events', { params: q.params, silent: true })
}

export function getHallAlertMail() {
  return http.get('/admin/hall/alert-mail', { silent: true })
}

// body: { enabled, to, from, smtp_host, smtp_port, smtp_security, smtp_username, smtp_password? }；
// smtp_password 省略 = 不修改已保存的密码
export function saveHallAlertMail(body) {
  return http.post('/admin/hall/alert-mail', body, { silent: true })
}

// 用已保存的配置立即发一封测试邮件
export function sendHallAlertMailTest() {
  return http.post('/admin/hall/alert-mail/test', null, { silent: true, timeout: MAIL_TEST_TIMEOUT_MS })
}
