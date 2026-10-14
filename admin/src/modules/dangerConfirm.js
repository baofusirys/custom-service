// 高危操作确认文字（契约第 2 节）；请求体只发送常量，不发送用户输入原文。
export const CONFIRM_DELETE_ALL = '删除全部'
export const CONFIRM_PURGE_RECYCLE = '清空回收站'

export function matchesConfirmText(input, expected) {
  return typeof input === 'string' && typeof expected === 'string' && expected !== '' && input.trim() === expected
}
