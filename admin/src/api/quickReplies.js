// 快捷回复接口（契约第 3 节）。
import http from './http'

const seg = (value) => encodeURIComponent(String(value ?? ''))

export function listQuickReplies(options = {}) {
  return http.get('/agent/quick-replies', { silent: !!options.silent })
}

export function createQuickReply({ scope, title, content, sort_order }) {
  const body = { scope, title, content }
  if (sort_order !== undefined) body.sort_order = sort_order
  return http.post('/agent/quick-replies', body)
}

export function updateQuickReply(id, { title, content, sort_order }) {
  const body = { title, content }
  if (sort_order !== undefined) body.sort_order = sort_order
  return http.post(`/agent/quick-replies/${seg(id)}/update`, body)
}

export function deleteQuickReply(id) {
  return http.post(`/agent/quick-replies/${seg(id)}/delete`)
}
