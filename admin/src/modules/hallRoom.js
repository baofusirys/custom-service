// 多人大厅 LiveKit 适配层：把 livekit-client 的 Room 封装成「纯数据回调」，视图只拿到普通对象。
// 依赖：livekit-client 2.23.0（Apache-2.0，精确版本锁定）。
// 订阅策略（契约第 1 节带宽约束）：autoSubscribe 关闭；所有音频订阅（能听到所有开麦的人），
// 视频只订阅视图选中的最多 4 路且固定请求低清层（发布端 360p + 180p 联播）。
import {
  LogLevel,
  Room,
  RoomEvent,
  Track,
  VideoPresets,
  VideoQuality,
  setLogLevel,
} from 'livekit-client'
import { hasPublishPermission } from './hallProtocol.js'
import { roleFromIdentity } from './hallParticipants.js'

// 生产环境只保留警告及以上日志，避免控制台刷屏
setLogLevel(LogLevel.warn)

const CAMERA_CAPTURE = { resolution: VideoPresets.h360.resolution }
const CAMERA_PUBLISH = {
  simulcast: true,
  videoEncoding: VideoPresets.h360.encoding,
  videoSimulcastLayers: [VideoPresets.h180],
}

function snapshot(p, isLocal) {
  return {
    identity: p.identity,
    name: p.name || '',
    metadata: p.metadata || '',
    micOn: !!p.isMicrophoneEnabled,
    cameraOn: !!p.isCameraEnabled,
    speaking: !!p.isSpeaking,
    isLocal,
    joinedAt: p.joinedAt instanceof Date ? p.joinedAt.getTime() : 0,
  }
}

function safe(fn) {
  try { fn() } catch { /* 单个轨道操作失败不影响其它轨道 */ }
}

export class HallRoomClient {
  constructor(handlers = {}) {
    this.handlers = handlers
    this.room = new Room({
      adaptiveStream: false,
      dynacast: true,
      disconnectOnPageLeave: true,
      stopLocalTrackOnUnpublish: true,
      audioCaptureDefaults: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      videoCaptureDefaults: CAMERA_CAPTURE,
      publishDefaults: { ...CAMERA_PUBLISH, dtx: true, red: true },
    })
    this.audioContainer = null
    this.cameraSeenAt = new Map()
    this.leaving = false
    this.finished = false
    this.bindEvents()
  }

  emit(name, ...args) {
    const fn = this.handlers[name]
    if (typeof fn !== 'function') return
    try { fn(...args) } catch (e) { console.error('[hall] 回调异常', name, e) }
  }

  bindEvents() {
    const r = this.room
    const roster = () => this.emit('onRoster')
    const video = () => this.emit('onVideo')
    r.on(RoomEvent.ParticipantConnected, (p) => {
      this.subscribeAudioOf(p)
      this.emit('onParticipantJoined', p.identity)
      roster()
    })
    r.on(RoomEvent.ParticipantDisconnected, (p) => {
      this.cameraSeenAt.delete(p.identity)
      roster()
      video()
    })
    r.on(RoomEvent.TrackPublished, (pub, p) => {
      if (pub.kind === Track.Kind.Audio) safe(() => pub.setSubscribed(true))
      if (pub.source === Track.Source.Camera && !this.cameraSeenAt.has(p.identity)) this.cameraSeenAt.set(p.identity, Date.now())
      roster()
      if (pub.kind === Track.Kind.Video) video()
    })
    r.on(RoomEvent.TrackUnpublished, (pub, p) => {
      if (pub.source === Track.Source.Camera) this.cameraSeenAt.delete(p.identity)
      roster()
      if (pub.kind === Track.Kind.Video) video()
    })
    r.on(RoomEvent.TrackSubscribed, (track, pub) => {
      if (track.kind === Track.Kind.Audio) this.attachAudio(track)
      else if (pub.source === Track.Source.Camera) video()
    })
    r.on(RoomEvent.TrackUnsubscribed, (track, pub) => {
      if (track.kind === Track.Kind.Audio) safe(() => track.detach().forEach(el => el.remove()))
      else if (pub.source === Track.Source.Camera) video()
    })
    r.on(RoomEvent.TrackMuted, () => { roster(); video() })
    r.on(RoomEvent.TrackUnmuted, () => { roster(); video() })
    r.on(RoomEvent.LocalTrackPublished, () => { roster(); video() })
    r.on(RoomEvent.LocalTrackUnpublished, () => { roster(); video() })
    r.on(RoomEvent.ParticipantNameChanged, roster)
    r.on(RoomEvent.ParticipantMetadataChanged, roster)
    r.on(RoomEvent.ActiveSpeakersChanged, (speakers) => {
      this.emit('onSpeakers', (speakers || []).map(s => s.identity))
    })
    r.on(RoomEvent.ParticipantPermissionsChanged, (_prev, p) => {
      if (p === r.localParticipant) this.emit('onLocalPermissions')
    })
    r.on(RoomEvent.AudioPlaybackStatusChanged, () => this.emit('onAudioPlayback', r.canPlaybackAudio))
    r.on(RoomEvent.Reconnecting, () => this.emit('onConnectionState', 'reconnecting'))
    r.on(RoomEvent.SignalReconnecting, () => this.emit('onConnectionState', 'reconnecting'))
    r.on(RoomEvent.Reconnected, () => this.emit('onConnectionState', 'connected'))
    r.on(RoomEvent.MediaDevicesError, (e, kind) => this.emit('onMediaError', e, kind))
    r.on(RoomEvent.Disconnected, (reason) => {
      this.cleanupMedia()
      if (this.finished) return
      this.finished = true
      this.emit('onDisconnected', reason, { initiatedByClient: this.leaving })
    })
  }

  get connected() {
    return this.room.state === 'connected'
  }

  get canPlaybackAudio() {
    return this.room.canPlaybackAudio
  }

  get localIdentity() {
    return this.room.localParticipant?.identity || ''
  }

  // 必须在用户点击的同步调用栈里先调用一次：解锁浏览器自动播放限制（契约 7.3）
  primeAudio() {
    try {
      this.room.startAudio().catch(() => {})
    } catch { /* 部分浏览器在连接前会拒绝，连接后还会再调用一次 */ }
  }

  startAudio() {
    return this.room.startAudio()
  }

  async connect({ url, token, iceServers }) {
    const opts = { autoSubscribe: false, maxRetries: 1, peerConnectionTimeout: 15000 }
    // 契约 7.1：不启用 LiveKit 内置 TURN，使用后端下发的 coturn 短期凭证
    if (Array.isArray(iceServers) && iceServers.length) opts.rtcConfig = { iceServers }
    await this.room.connect(url, token, opts)
    for (const p of this.room.remoteParticipants.values()) {
      this.subscribeAudioOf(p)
      if (p.getTrackPublication(Track.Source.Camera)) this.cameraSeenAt.set(p.identity, Date.now())
    }
  }

  subscribeAudioOf(p) {
    for (const pub of p.audioTrackPublications.values()) safe(() => pub.setSubscribed(true))
  }

  attachAudio(track) {
    if (typeof document === 'undefined') return
    if (!this.audioContainer) {
      this.audioContainer = document.createElement('div')
      this.audioContainer.hidden = true
      this.audioContainer.setAttribute('data-hall-audio', '')
      document.body.appendChild(this.audioContainer)
    }
    const el = track.attach()
    this.audioContainer.appendChild(el)
  }

  cleanupMedia() {
    for (const p of this.room.remoteParticipants.values()) {
      for (const pub of p.audioTrackPublications.values()) {
        if (pub.track) safe(() => pub.track.detach().forEach(el => el.remove()))
      }
    }
    if (this.audioContainer) {
      safe(() => this.audioContainer.remove())
      this.audioContainer = null
    }
    this.cameraSeenAt.clear()
  }

  snapshots() {
    const out = []
    const lp = this.room.localParticipant
    if (lp && lp.identity) out.push(snapshot(lp, true))
    for (const p of this.room.remoteParticipants.values()) out.push(snapshot(p, false))
    return out
  }

  localState() {
    const lp = this.room.localParticipant
    return {
      micOn: !!lp?.isMicrophoneEnabled,
      cameraOn: !!lp?.isCameraEnabled,
      canPublishMic: hasPublishPermission(lp?.permissions, 'microphone'),
      canPublishCamera: hasPublishPermission(lp?.permissions, 'camera'),
    }
  }

  hasPublishPermission(source) {
    return hasPublishPermission(this.room.localParticipant?.permissions, source)
  }

  // 服务端通过 UpdateParticipant 授权后，权限变更经信令异步到达；等待到达或超时
  waitForPublishPermission(source, timeoutMs = 5000) {
    if (this.hasPublishPermission(source)) return Promise.resolve(true)
    return new Promise((resolve) => {
      let done = false
      const finish = (ok) => {
        if (done) return
        done = true
        clearTimeout(timer)
        clearInterval(poll)
        this.room.off(RoomEvent.ParticipantPermissionsChanged, check)
        resolve(ok)
      }
      const check = () => { if (this.hasPublishPermission(source)) finish(true) }
      const timer = setTimeout(() => finish(this.hasPublishPermission(source)), timeoutMs)
      const poll = setInterval(check, 250)
      this.room.on(RoomEvent.ParticipantPermissionsChanged, check)
    })
  }

  async setMicrophone(on) {
    const lp = this.room.localParticipant
    if (on && !this.hasPublishPermission('microphone')) {
      const e = new Error('insufficient permissions')
      e.status = 403
      throw e
    }
    await lp.setMicrophoneEnabled(!!on)
  }

  async setCamera(on) {
    const lp = this.room.localParticipant
    if (on) {
      if (!this.hasPublishPermission('camera')) {
        const e = new Error('insufficient permissions')
        e.status = 403
        throw e
      }
      await lp.setCameraEnabled(true, CAMERA_CAPTURE, CAMERA_PUBLISH)
    } else {
      await lp.setCameraEnabled(false)
    }
  }

  // 正在发布摄像头的参与者（含自己），供视频区选择
  videoCandidates(speakingSet = new Set()) {
    const out = []
    const lp = this.room.localParticipant
    const lpPub = lp?.getTrackPublication(Track.Source.Camera)
    if (lpPub && !lpPub.isMuted && lpPub.track) {
      out.push({ identity: lp.identity, isLocal: true, role: 'agent', speaking: false, publishedAt: 0 })
    }
    for (const p of this.room.remoteParticipants.values()) {
      const pub = p.getTrackPublication(Track.Source.Camera)
      if (!pub || pub.isMuted) continue
      out.push({
        identity: p.identity,
        isLocal: false,
        role: roleFromIdentity(p.identity),
        speaking: speakingSet.has(p.identity) || !!p.isSpeaking,
        publishedAt: this.cameraSeenAt.get(p.identity) || 0,
      })
    }
    return out
  }

  // 只订阅选中的远端摄像头并固定低清层；未选中的取消订阅以节省带宽
  applyVideoSelection(selected) {
    const want = new Set(Array.isArray(selected) ? selected : [])
    for (const p of this.room.remoteParticipants.values()) {
      const pub = p.getTrackPublication(Track.Source.Camera)
      if (!pub) continue
      if (want.has(p.identity)) {
        safe(() => { if (!pub.isDesired) pub.setSubscribed(true) })
        safe(() => pub.setVideoQuality(VideoQuality.LOW))
      } else if (pub.isDesired || pub.isSubscribed) {
        safe(() => pub.setSubscribed(false))
      }
    }
  }

  // 视频区画面：[{identity, name, isLocal, track|null}]
  videoTiles(selected) {
    const tiles = []
    const lp = this.room.localParticipant
    for (const id of Array.isArray(selected) ? selected : []) {
      if (lp && id === lp.identity) {
        const pub = lp.getTrackPublication(Track.Source.Camera)
        tiles.push({ identity: id, name: lp.name || '我', isLocal: true, track: pub?.videoTrack || null })
        continue
      }
      const p = this.room.remoteParticipants.get(id)
      if (!p) continue
      const pub = p.getTrackPublication(Track.Source.Camera)
      tiles.push({ identity: id, name: p.name || '', isLocal: false, track: pub?.isSubscribed ? pub.videoTrack || null : null })
    }
    return tiles
  }

  async disconnect() {
    this.leaving = true
    try {
      await this.room.disconnect(true)
    } finally {
      this.cleanupMedia()
    }
  }
}
