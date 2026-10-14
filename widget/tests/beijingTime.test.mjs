import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'

// [113] 访客端时间按北京时间显示：抽取 chat.js 的时间格式化段在隔离上下文中执行，固定「当前时间」验证
const src = readFileSync(new URL('../public/chat.js', import.meta.url), 'utf8')
const start = src.indexOf('// ====== 时间格式化 ======')
const end = src.indexOf('function escape(', start)
assert.ok(start > 0 && end > start, '找不到时间格式化代码段')

function load(nowMs) {
  const RealDate = Date
  class FixedDate extends RealDate {
    constructor(...a) { super(...(a.length ? a : [nowMs])) }
    static now() { return nowMs }
  }
  const ctx = { Date: FixedDate, isFinite }
  vm.createContext(ctx)
  vm.runInContext(src.slice(start, end) + '\n;this.api = { fmtAbs, fmtGroupTime, fmtMsgTime };', ctx)
  return ctx.api
}

const now = Date.UTC(2026, 9, 14, 13, 5, 0)   // 北京 2026-10-14 21:05

test('今天的消息显示北京时间', () => {
  const f = load(now)
  assert.equal(f.fmtMsgTime(Date.UTC(2026, 9, 14, 0, 57)), '08:57')
  assert.equal(f.fmtMsgTime('2026-10-14T12:57:00.000Z'), '20:57')
  assert.equal(f.fmtAbs('2026-10-14T12:00:00Z'), '2026-10-14 20:00:00')
})

test('按北京日历日判断今天/昨天', () => {
  const f = load(now)
  assert.equal(f.fmtGroupTime(Date.UTC(2026, 9, 13, 16, 30)), '今天 00:30')
  assert.equal(f.fmtGroupTime(Date.UTC(2026, 9, 13, 15, 30)), '昨天 23:30')
  assert.equal(f.fmtGroupTime('2026-10-01T09:00:00+08:00'), '10-01 09:00')
  assert.equal(f.fmtMsgTime('2025-12-31T23:59:00+08:00'), '2025-12-31 23:59')
})
