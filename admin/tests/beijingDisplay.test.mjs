import test from 'node:test'
import assert from 'node:assert/strict'
import { formatMsgTime, formatGroupTime, formatBeijingHM, beijingDateKey, formatBeijing } from '../src/modules/timeFormat.js'

// [113] 不论运行环境时区，均按北京时间展示（测试用 UTC 时间戳构造，避免依赖本机时区）
const now = Date.UTC(2026, 9, 14, 13, 5, 0)            // 北京 2026-10-14 21:05

test('今天的消息只显示北京时间时分', () => {
  assert.equal(formatMsgTime(Date.UTC(2026, 9, 14, 0, 57), now), '08:57')
  assert.equal(formatMsgTime('2026-10-14T20:57:00+08:00', now), '20:57')
  assert.equal(formatBeijingHM('2026-10-14T12:57:00Z'), '20:57')
})

test('跨越北京时间零点按北京日历日判断', () => {
  // UTC 10-13 16:30 = 北京 10-14 00:30（今天）；UTC 10-13 15:30 = 北京 10-13 23:30（昨天）
  assert.equal(formatGroupTime(Date.UTC(2026, 9, 13, 16, 30), now), '今天 00:30')
  assert.equal(formatGroupTime(Date.UTC(2026, 9, 13, 15, 30), now), '昨天 23:30')
  assert.equal(formatGroupTime('2026-10-01T09:00:00+08:00', now), '10-01 09:00')
  assert.equal(beijingDateKey(Date.UTC(2026, 9, 13, 16, 0)), '2026-10-14')
})

test('今年与往年格式、无效时间', () => {
  assert.equal(formatMsgTime('2026-03-02T08:00:00+08:00', now), '03-02 08:00')
  assert.equal(formatMsgTime('2025-12-31T23:59:00+08:00', now), '2025-12-31 23:59')
  assert.equal(formatMsgTime('not-a-date', now), '')
  assert.equal(formatBeijing('2026-10-14T12:00:00Z'), '2026-10-14 20:00:00')
})
