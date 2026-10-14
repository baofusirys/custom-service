// 聊天内容相关接口：全局搜索（契约第 8 节）与消息撤回（契约第 9 节）。
import http from './http'
import { clampSearchLimit } from '../modules/chatSearch'

const seg = (value) => encodeURIComponent(String(value ?? ''))

export function searchMessages(q, limit = 30) {
  return http.get('/agent/search', { params: { q, limit: clampSearchLimit(limit) } })
}

export function recallMessage(messageId) {
  return http.post(`/agent/messages/${seg(messageId)}/recall`)
}
