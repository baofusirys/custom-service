// 自动清理设置（契约第 11 节）：auto_clean_days 为字符串，"0" 关闭，或 "7"-"3650"。
export const AUTO_CLEAN_MIN_DAYS = 7
export const AUTO_CLEAN_MAX_DAYS = 3650
export const AUTO_CLEAN_DEFAULT_DAYS = 90

export function parseAutoCleanDays(raw) {
  const text = typeof raw === 'number' ? String(raw) : (typeof raw === 'string' ? raw.trim() : '')
  if (!/^\d{1,4}$/.test(text)) return { enabled: false, days: AUTO_CLEAN_DEFAULT_DAYS }
  const n = Number(text)
  if (n >= AUTO_CLEAN_MIN_DAYS && n <= AUTO_CLEAN_MAX_DAYS) return { enabled: true, days: n }
  return { enabled: false, days: AUTO_CLEAN_DEFAULT_DAYS }
}

export function serializeAutoCleanDays(enabled, days) {
  if (!enabled) return { ok: true, value: '0', error: '' }
  const n = Number(days)
  if (!Number.isInteger(n) || n < AUTO_CLEAN_MIN_DAYS || n > AUTO_CLEAN_MAX_DAYS) {
    return { ok: false, value: '', error: `自动清理天数需为 ${AUTO_CLEAN_MIN_DAYS}-${AUTO_CLEAN_MAX_DAYS} 的整数` }
  }
  return { ok: true, value: String(n), error: '' }
}
