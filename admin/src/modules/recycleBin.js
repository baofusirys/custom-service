// 回收站剩余天数计算（契约第 2 节：保留 7 天，后台每小时清理到期数据）。
import { toTimestamp } from './timeFormat.js'

const DAY_MS = 24 * 60 * 60 * 1000

// 剩余天数向上取整：还剩 6.2 天显示 7 天；已到期返回 0；时间无效返回 null。
export function remainingDays(purgeAt, now = Date.now()) {
  const purge = toTimestamp(purgeAt)
  if (!Number.isFinite(purge) || !Number.isFinite(now)) return null
  const diff = purge - now
  if (diff <= 0) return 0
  return Math.ceil(diff / DAY_MS)
}

export function remainingLabel(purgeAt, now = Date.now()) {
  const purge = toTimestamp(purgeAt)
  if (!Number.isFinite(purge)) return '—'
  const diff = purge - now
  if (diff <= 0) return '待清理'
  if (diff < DAY_MS) return '不足 1 天'
  return `${Math.ceil(diff / DAY_MS)} 天`
}

// 剩余时间越少越需要提醒：用于 el-tag 的 type。
export function remainingTagType(purgeAt, now = Date.now()) {
  const days = remainingDays(purgeAt, now)
  if (days === null) return 'info'
  if (days <= 1) return 'danger'
  if (days <= 3) return 'warning'
  return 'info'
}
