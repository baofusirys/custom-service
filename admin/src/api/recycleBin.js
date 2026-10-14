// 回收站接口（仅管理员，契约第 2 节）。
import http from './http'
import { CONFIRM_PURGE_RECYCLE } from '../modules/dangerConfirm'

export function listRecycleBin({ offset = 0, limit = 50 } = {}) {
  return http.get('/admin/recycle-bin', { params: { offset, limit } })
}

export function restoreRecycled(visitorIds) {
  return http.post('/admin/recycle-bin/restore', { visitor_ids: visitorIds })
}

export function purgeRecycled(visitorIds) {
  return http.post('/admin/recycle-bin/purge', { visitor_ids: visitorIds })
}

// 请求体只发送常量确认文字；调用方必须先让用户输入并校验一致
export function purgeAllRecycled() {
  return http.post('/admin/recycle-bin/purge', { all: true, confirm: CONFIRM_PURGE_RECYCLE })
}
