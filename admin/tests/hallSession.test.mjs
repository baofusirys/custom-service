import test from 'node:test'
import assert from 'node:assert/strict'

import { hasActiveHallLeaveHandler, leaveHallBeforeLogout, registerHallLeaveHandler } from '../src/modules/hallSession.js'

test('退出登录前调用已注册的离开函数；注销后不再调用', async () => {
  const calls = []
  const off = registerHallLeaveHandler(async (reason) => { calls.push(reason) })
  assert.equal(hasActiveHallLeaveHandler(), true)
  assert.equal(await leaveHallBeforeLogout(), true)
  assert.deepEqual(calls, ['logout'])
  off()
  assert.equal(hasActiveHallLeaveHandler(), false)
  assert.equal(await leaveHallBeforeLogout(), false)
})

test('离开函数卡住或抛错都不阻塞退出登录', async () => {
  const off1 = registerHallLeaveHandler(() => new Promise(() => {}))
  const t0 = Date.now()
  await leaveHallBeforeLogout(50)
  assert.ok(Date.now() - t0 < 1000)
  off1()
  const off2 = registerHallLeaveHandler(() => { throw new Error('x') })
  assert.equal(await leaveHallBeforeLogout(50), true)
  off2()
  // 旧注销函数不会误删新注册的处理函数
  const offA = registerHallLeaveHandler(() => {})
  registerHallLeaveHandler(() => {})
  offA()
  assert.equal(hasActiveHallLeaveHandler(), true)
})
