// 客服端 WSS 客户端：自动重连 + 心跳 + 消息回调
// 设计原则（爷爷需求）：WSS 通道优先处理消息
//
// [064] 同 mobile_app/lib/api/ws_client.dart 一致的 [068] 修复：
//   - token 可变（不再 constructor 固化），refresh 后能更新
//   - isConnecting 重入锁
//   - 连接前主动检查 exp < 5min → 调 /agent/login/refresh 续 token
//   - close code 4001/4002 区分（实际 WSS handshake 401 时浏览器拿不到 code，仍靠主动检查兜底）

export class AgentWS {
  constructor({ url, token, onMessage, onOpen, onClose }) {
    this.url = url
    this._token = token            // [064] 内部变量，可变
    this.onMessage = onMessage
    this.onOpen = onOpen
    this.onClose = onClose
    this.sock = null
    this.alive = false
    this.retry = 0
    this.heartbeat = null
    this.shouldRun = false
    this._isConnecting = false     // [064] 重入锁
  }

  get token() { return this._token }

  start() {
    this.shouldRun = true
    this._connect()
  }

  stop() {
    this.shouldRun = false
    this._isConnecting = false
    if (this.heartbeat) clearInterval(this.heartbeat)
    if (this.sock) {
      try { this.sock.close() } catch {}
    }
    this.alive = false
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

  // [064] 检测当前 token 距离 exp < 5min
  _shouldRefreshToken() {
    try {
      const parts = (this._token || '').split('.')
      if (parts.length !== 3) return false
      // base64url → JSON
      const payload = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')))
      if (typeof payload.exp !== 'number') return false
      const expMs = payload.exp * 1000
      return (expMs - Date.now()) < 5 * 60 * 1000
    } catch {
      return false
    }
  }

  // [064] 主动调 refresh，更新 _token + localStorage
  async _refreshToken() {
    const oldToken = this._token
    if (!oldToken) return false
    try {
      const baseURL = import.meta.env.VITE_API_BASE || '/api'
      const r = await fetch(baseURL + '/agent/login/refresh', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer ' + oldToken,
        },
      })
      if (!r.ok) return false
      const d = await r.json()
      if (d && d.code === 0 && d.token) {
        this._token = d.token
        localStorage.setItem('cs_admin_token', d.token)
        return true
      }
    } catch {}
    return false
  }

  async _connect() {
    if (this._isConnecting) return
    this._isConnecting = true

    try {
      // [064] 连接前主动检查 token：距 exp < 5min 主动 refresh
      if (this._shouldRefreshToken()) {
        const ok = await this._refreshToken()
        if (!ok) {
          // refresh 失败：不再死循环重连
          this._isConnecting = false
          this.stop()
          return
        }
      }

      const full = `${this.url}?token=${encodeURIComponent(this._token)}`
      this.sock = new WebSocket(full)
      this.sock.onopen = () => {
        this.alive = true
        this.retry = 0
        this._isConnecting = false
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
      this.sock.onclose = async (ev) => {
        this.alive = false
        this._isConnecting = false
        if (this.heartbeat) clearInterval(this.heartbeat)
        this.onClose?.()
        // [064] close code 4001 token expired → refresh + 重连
        // 4002 invalid → 不重连让用户重登
        if (ev?.code === 4001) {
          const ok = await this._refreshToken()
          if (ok && this.shouldRun) {
            this.retry = 0
            this._connect()
            return
          }
          this.stop()
          return
        }
        if (ev?.code === 4002) {
          this.stop()
          return
        }
        if (this.shouldRun) {
          const backoff = Math.min(30000, 1000 * Math.pow(1.6, this.retry++))
          setTimeout(() => this._connect(), backoff)
        }
      }
      this.sock.onerror = () => {
        try { this.sock.close() } catch {}
      }
    } catch {
      this._isConnecting = false
      if (this.shouldRun) {
        const backoff = Math.min(30000, 1000 * Math.pow(1.6, this.retry++))
        setTimeout(() => this._connect(), backoff)
      }
    }
  }
}
