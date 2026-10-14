// “对方正在输入”（契约第 10 节）：发送端节流 + 接收端超时隐藏。
// 计时器与时钟均可注入，便于 node 测试中用假时钟验证。

export const TYPING_SEND_INTERVAL_MS = 3000
export const TYPING_DISPLAY_TIMEOUT_MS = 5000
const DEFAULT_MAX_KEYS = 500

// 每个 key（会话）在 intervalMs 内最多放行一次；key 数量有上限，防长时间运行内存增长。
export class TypingThrottle {
  constructor({ intervalMs = TYPING_SEND_INTERVAL_MS, now = () => Date.now(), maxKeys = DEFAULT_MAX_KEYS } = {}) {
    this.intervalMs = intervalMs
    this.now = now
    this.maxKeys = maxKeys
    this.last = new Map()
  }

  tryAcquire(key = '') {
    const k = String(key)
    const t = this.now()
    const prev = this.last.get(k)
    // 时钟回拨（t < prev）时直接放行，避免长时间不发
    if (prev !== undefined && t >= prev && t - prev < this.intervalMs) return false
    this.last.delete(k)
    this.last.set(k, t)
    while (this.last.size > this.maxKeys) this.last.delete(this.last.keys().next().value)
    return true
  }

  reset(key) {
    if (key === undefined) this.last.clear()
    else this.last.delete(String(key))
  }
}

// 收到对方 typing 后显示，timeoutMs 内没有新 typing 自动隐藏；onChange(key, visible)。
export class TypingIndicator {
  constructor({
    timeoutMs = TYPING_DISPLAY_TIMEOUT_MS,
    onChange = () => {},
    setTimer = (fn, ms) => setTimeout(fn, ms),
    clearTimer = (id) => clearTimeout(id),
    maxKeys = DEFAULT_MAX_KEYS,
  } = {}) {
    this.timeoutMs = timeoutMs
    this.onChange = onChange
    this.setTimer = setTimer
    this.clearTimer = clearTimer
    this.maxKeys = maxKeys
    this.timers = new Map()
  }

  mark(key) {
    const k = String(key || '')
    if (!k) return
    const existed = this.timers.has(k)
    if (existed) {
      this.clearTimer(this.timers.get(k))
      this.timers.delete(k)
    } else if (this.timers.size >= this.maxKeys) {
      this.clear(this.timers.keys().next().value)
    }
    this.timers.set(k, this.setTimer(() => {
      this.timers.delete(k)
      this.onChange(k, false)
    }, this.timeoutMs))
    if (!existed) this.onChange(k, true)
  }

  clear(key) {
    const k = String(key || '')
    if (!this.timers.has(k)) return
    this.clearTimer(this.timers.get(k))
    this.timers.delete(k)
    this.onChange(k, false)
  }

  has(key) {
    return this.timers.has(String(key || ''))
  }

  dispose() {
    for (const id of this.timers.values()) this.clearTimer(id)
    this.timers.clear()
  }
}

// 解析 WS 的 from 字段（visitor:<vid> / agent:<aid>），非法格式返回空角色。
export function parseParticipant(from) {
  if (typeof from !== 'string') return { role: '', id: '' }
  const idx = from.indexOf(':')
  if (idx <= 0) return { role: '', id: '' }
  const role = from.slice(0, idx)
  const id = from.slice(idx + 1)
  if ((role !== 'visitor' && role !== 'agent') || !/^[A-Za-z0-9_-]{1,64}$/.test(id)) return { role: '', id: '' }
  return { role, id }
}
