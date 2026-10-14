// 客户（访客）相关接口：资料 / 标签 / 置顶 / 未读 / 拉黑 / 删除 / 导出（契约第 1、2、4-7 节）。
import http from './http'
import { requestDownload } from './download'
import { exportFallbackName, normalizeExportFormat } from '../modules/download'
import { CONFIRM_DELETE_ALL } from '../modules/dangerConfirm'

// 路径参数统一编码，杜绝路径注入
const seg = (value) => encodeURIComponent(String(value ?? ''))

export function listTags(options = {}) {
  return http.get('/agent/tags', { silent: !!options.silent })
}

export function getVisitorProfile(vid, options = {}) {
  return http.get(`/agent/visitor/${seg(vid)}/profile`, { silent: !!options.silent })
}

export function saveVisitorProfile(vid, { note, tags }) {
  return http.post(`/agent/visitor/${seg(vid)}/profile`, { note, tags })
}

export function setVisitorPinned(vid, pinned) {
  return http.post(`/agent/visitor/${seg(vid)}/pin`, { pinned: pinned === true })
}

export function setVisitorMarkedUnread(vid, unread) {
  return http.post(`/agent/visitor/${seg(vid)}/unread`, { unread: unread === true })
}

export function setVisitorBlocked(vid, { blocked, reason = '', blockIp = false }) {
  const body = { blocked: blocked === true }
  if (body.blocked) {
    if (reason) body.reason = reason
    body.block_ip = blockIp === true
  }
  return http.post(`/agent/visitor/${seg(vid)}/block`, body)
}

export function listBlockedVisitors({ offset = 0, limit = 50 } = {}) {
  return http.get('/agent/blocked', { params: { offset, limit } })
}

export function deleteVisitors(visitorIds) {
  return http.post('/agent/visitors/delete', { visitor_ids: visitorIds })
}

// 请求体只发送常量确认文字；调用方必须先让用户输入并校验一致
export function deleteAllVisitors() {
  return http.post('/admin/visitors/delete-all', { confirm: CONFIRM_DELETE_ALL })
}

export function exportVisitor(vid, format = 'xlsx') {
  const fmt = normalizeExportFormat(format)
  return requestDownload(
    { method: 'get', url: `/agent/visitor/${seg(vid)}/export`, params: { format: fmt } },
    exportFallbackName('聊天记录', fmt),
  )
}

export function exportVisitors(visitorIds, format = 'xlsx') {
  const fmt = normalizeExportFormat(format)
  return requestDownload(
    { method: 'post', url: '/agent/export', data: { visitor_ids: visitorIds, format: fmt } },
    exportFallbackName('聊天记录-批量', fmt),
  )
}

// 打开被“标为未读”的会话时调用：契约第 5 节，标记已读会自动清除 marked_unread
export function markConversationRead(convId) {
  return http.post(`/agent/conversations/${seg(convId)}/read`, null, { silent: true })
}
