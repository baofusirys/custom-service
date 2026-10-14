// 文本长度与字符校验的唯一实现（纯函数，可在 node 测试中直接运行）。
// 统一按 Unicode 码点计数，与后端 Go 的 utf8.RuneCountInString 口径一致：
// 一个汉字、一个字母或一个代理对字符都只算 1 个字，避免前后端对“字数”理解不一致。

// 单行字段（标签、原因、关键词）不允许任何控制字符；多行字段（备注）允许换行与制表符。
const SINGLE_LINE_CONTROL = /[\u0000-\u001F\u007F]/
const MULTI_LINE_CONTROL = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/

export function charCount(value) {
  if (value === null || value === undefined) return 0
  return Array.from(String(value)).length
}

export function hasControlChars(value, { allowNewline = false } = {}) {
  const text = String(value ?? '')
  return (allowNewline ? MULTI_LINE_CONTROL : SINGLE_LINE_CONTROL).test(text)
}

// 按码点截断，超出部分以省略号结尾；不会把代理对切成半个乱码字符。
export function truncateChars(value, max, ellipsis = '…') {
  const chars = Array.from(String(value ?? ''))
  const limit = Math.max(0, Math.trunc(Number(max) || 0))
  if (chars.length <= limit) return chars.join('')
  return chars.slice(0, limit).join('') + ellipsis
}
