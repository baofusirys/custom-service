import test from 'node:test'
import assert from 'node:assert/strict'

import { TypingIndicator, TypingThrottle, parseParticipant } from '../src/modules/typing.js'

function fakeTimers() {
  let now = 0
  let seq = 0
  const timers = new Map()
  return {
    setTimer(fn, ms) { const id = ++seq; timers.set(id, { at: now + ms, fn }); return id },
    clearTimer(id) { timers.delete(id) },
    advance(ms) {
      const target = now + ms
      for (;;) {
        let next = null
        for (const [id, t] of timers) if (t.at <= target && (!next || t.at < next[1].at)) next = [id, t]
        if (!next) break
        timers.delete(next[0])
        now = next[1].at
        next[1].fn()
      }
      now = target
    },
    size: () => timers.size,
  }
}

test('typing 发送节流：同一会话每 3 秒最多一次，不同会话互不影响', () => {
  let now = 1000
  const t = new TypingThrottle({ intervalMs: 3000, now: () => now })
  assert.equal(t.tryAcquire('c1'), true)
  now += 1000
  assert.equal(t.tryAcquire('c1'), false)
  assert.equal(t.tryAcquire('c2'), true)
  now += 1999
  assert.equal(t.tryAcquire('c1'), false)
  now += 1
  assert.equal(t.tryAcquire('c1'), true)
})

test('typing 节流：发送失败可释放，时钟回拨不会长期卡住，key 数量有上限', () => {
  let now = 10000
  const t = new TypingThrottle({ intervalMs: 3000, now: () => now, maxKeys: 3 })
  assert.equal(t.tryAcquire('c1'), true)
  t.reset('c1')
  assert.equal(t.tryAcquire('c1'), true)
  now -= 5000
  assert.equal(t.tryAcquire('c1'), true)
  for (const k of ['a', 'b', 'c', 'd']) t.tryAcquire(k)
  assert.ok(t.last.size <= 3)
})

test('正在输入提示：5 秒无新 typing 自动隐藏，新 typing 续期，收到消息立即隐藏', () => {
  const timers = fakeTimers()
  const changes = []
  const ind = new TypingIndicator({ timeoutMs: 5000, onChange: (k, on) => changes.push([k, on]), setTimer: timers.setTimer, clearTimer: timers.clearTimer })
  ind.mark('v1')
  assert.deepEqual(changes, [['v1', true]])
  timers.advance(4000)
  ind.mark('v1')
  timers.advance(4000)
  assert.equal(ind.has('v1'), true)
  timers.advance(1000)
  assert.equal(ind.has('v1'), false)
  assert.deepEqual(changes, [['v1', true], ['v1', false]])
  ind.mark('v2')
  ind.clear('v2')
  assert.equal(ind.has('v2'), false)
  assert.equal(timers.size(), 0)
  ind.clear('v2')
  assert.equal(changes.filter(c => c[0] === 'v2').length, 2)
})

test('正在输入提示：dispose 清理全部计时器且不再回调', () => {
  const timers = fakeTimers()
  let calls = 0
  const ind = new TypingIndicator({ onChange: () => calls++, setTimer: timers.setTimer, clearTimer: timers.clearTimer })
  ind.mark('a'); ind.mark('b')
  ind.dispose()
  timers.advance(10000)
  assert.equal(timers.size(), 0)
  assert.equal(calls, 2)
})

test('from 字段解析只接受 visitor:/agent: 与安全 ID', () => {
  assert.deepEqual(parseParticipant('visitor:abc_1'), { role: 'visitor', id: 'abc_1' })
  assert.deepEqual(parseParticipant('agent:7'), { role: 'agent', id: '7' })
  assert.deepEqual(parseParticipant('sys'), { role: '', id: '' })
  assert.deepEqual(parseParticipant('visitor:../x'), { role: '', id: '' })
  assert.deepEqual(parseParticipant('admin:1'), { role: '', id: '' })
  assert.deepEqual(parseParticipant(null), { role: '', id: '' })
})
