// 退出登录前先离开大厅：大厅页注册离开函数，Layout 退出登录时调用。
// 先断开 LiveKit、取消 WS 大厅订阅并释放摄像头名额，再让登录态失效，避免残留占用。
let leaveHandler = null

export function registerHallLeaveHandler(fn) {
  if (typeof fn !== 'function') return () => {}
  leaveHandler = fn
  return () => {
    if (leaveHandler === fn) leaveHandler = null
  }
}

export function hasActiveHallLeaveHandler() {
  return typeof leaveHandler === 'function'
}

// 最多等待 timeoutMs，超时不阻塞退出登录
export async function leaveHallBeforeLogout(timeoutMs = 2000) {
  const fn = leaveHandler
  if (!fn) return false
  let timer = null
  try {
    await Promise.race([
      Promise.resolve().then(() => fn('logout')),
      new Promise(resolve => { timer = setTimeout(resolve, timeoutMs) }),
    ])
  } catch {
    // 离开失败不影响退出登录；LiveKit 连接会随页面跳转与令牌失效自然断开
  } finally {
    if (timer) clearTimeout(timer)
  }
  return true
}
