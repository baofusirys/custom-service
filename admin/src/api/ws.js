// 客服端 WSS 客户端：自动重连 + 心跳 + 消息回调
// 设计原则（爷爷需求）：WSS 通道优先处理消息

export class AgentWS {
  constructor({ url, token, onMessage, onOpen, onClose }) {
    this.url = url
    this.token = token
    this.onMessage = onMessage
    this.onOpen = onOpen
    this.onClose = onClose
    this.sock = null
    this.alive = false
    this.retry = 0
    this.heartbeat = null
    this.shouldRun = false
  }

  start() {
    this.shouldRun = true
    this._connect()
  }

  stop() {
    this.shouldRun = false
    if (this.heartbeat) clearInterval(this.heartbeat)
    if (this.sock) {
      try { this.sock.close() } catch {}
    }
  }

  send(env) {
    if (!this.alive) return false
    try {
      this.sock.send(JSON.stringify(env))
      return true
    } catch {
      return false
    }
  }

  _connect() {
    const full = `${this.url}?token=${encodeURIComponent(this.token)}`
    this.sock = new WebSocket(full)
    this.sock.onopen = () => {
      this.alive = true
      this.retry = 0
      this.onOpen?.()
      // 30s 心跳
      this.heartbeat = setInterval(() => {
        if (this.alive) this.send({ type: 'ping', ts: Date.now() })
      }, 30000)
    }
    this.sock.onmessage = (ev) => {
      try {
        const env = JSON.parse(ev.data)
        this.onMessage?.(env)
      } catch {}
    }
    this.sock.onclose = () => {
      this.alive = false
      if (this.heartbeat) clearInterval(this.heartbeat)
      this.onClose?.()
      if (this.shouldRun) {
        const backoff = Math.min(30000, 1000 * Math.pow(1.6, this.retry++))
        setTimeout(() => this._connect(), backoff)
      }
    }
    this.sock.onerror = () => {
      try { this.sock.close() } catch {}
    }
  }
}
