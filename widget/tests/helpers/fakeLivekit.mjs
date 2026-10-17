// [119] 测试用最小 LiveKit 假实现：只实现 hall.js 用到的 Room / 参与者 / 发布订阅 API 与事件名
// 事件名与 livekit-client 2.23.0 的 RoomEvent 取值一致（见 node_modules 之外的 src/room/events.ts）。
import { FakeElement } from './fakeWidgetEnv.mjs'

export const RoomEvent = {
  ParticipantConnected: 'participantConnected', ParticipantDisconnected: 'participantDisconnected',
  ParticipantNameChanged: 'participantNameChanged', ParticipantMetadataChanged: 'participantMetadataChanged',
  TrackPublished: 'trackPublished', TrackUnpublished: 'trackUnpublished',
  TrackSubscribed: 'trackSubscribed', TrackUnsubscribed: 'trackUnsubscribed',
  TrackMuted: 'trackMuted', TrackUnmuted: 'trackUnmuted', ActiveSpeakersChanged: 'activeSpeakersChanged',
  ParticipantPermissionsChanged: 'participantPermissionsChanged',
  LocalTrackPublished: 'localTrackPublished', LocalTrackUnpublished: 'localTrackUnpublished',
  AudioPlaybackStatusChanged: 'audioPlaybackChanged', Reconnecting: 'reconnecting', Reconnected: 'reconnected',
  MediaDevicesError: 'mediaDevicesError', Disconnected: 'disconnected',
}

export function createFakeLivekit() {
  const ctl = { rooms: [], failConnect: false, mediaError: '' }
  class Emitter {
    constructor() { this._l = {} }
    on(e, fn) { (this._l[e] ||= []).push(fn); return this }
    emit(e, ...a) { for (const fn of [...(this._l[e] || [])]) fn(...a) }
    removeAllListeners() { this._l = {}; return this }
  }
  const fakeTrack = (kind) => ({ kind, stopped: false, attached: [], stop() { this.stopped = true }, attach(el) { const e = el || new FakeElement(kind); this.attached.push(e); return e }, detach() { const a = this.attached; this.attached = []; return a } })
  class LocalParticipant {
    constructor(room) { this.room = room; this.permissions = { canPublish: false, canPublishSources: [] }; this.pubs = new Map(); this.calls = [] }
    getTrackPublication(src) { return this.pubs.get(src) }
    setMicrophoneEnabled(on) { this.calls.push(['mic', on]); return this._publish('microphone') }
    setCameraEnabled(on) { this.calls.push(['camera', on]); return this._publish('camera') }
    async _publish(src) {
      const proto = src === 'camera' ? 1 : 2
      const p = this.permissions
      if (!p.canPublish || (p.canPublishSources.length && !p.canPublishSources.includes(proto))) { const e = new Error('insufficient permissions'); e.name = 'PublishTrackError'; throw e }
      if (ctl.mediaError) { const e = new Error('media'); e.name = ctl.mediaError; throw e }
      const pub = { source: src, track: fakeTrack(src === 'camera' ? 'video' : 'audio'), trackSid: 'TR_local_' + src }
      this.pubs.set(src, pub)
      return pub
    }
    async unpublishTrack(track, stop) {
      this.calls.push(['unpublish', track.kind])
      for (const [k, v] of this.pubs) if (v.track === track) { this.pubs.delete(k); if (stop) track.stop() }
    }
    grant(sources) { const prev = this.permissions; this.permissions = { canPublish: true, canPublishSources: sources }; this.room.emit(RoomEvent.ParticipantPermissionsChanged, prev, this) }
    revoke() { const prev = this.permissions; this.permissions = { canPublish: false, canPublishSources: [] }; this.room.emit(RoomEvent.ParticipantPermissionsChanged, prev, this) }
  }
  class RemoteParticipant {
    constructor(identity, name, role) {
      this.identity = identity; this.name = name; this.metadata = JSON.stringify({ role, code: '0001' })
      this.audioTrackPublications = new Map(); this.videoTrackPublications = new Map()
      this.isMicrophoneEnabled = false; this.isCameraEnabled = false; this.isSpeaking = false; this.joinedAt = new Date(0)
    }
  }
  class Room extends Emitter {
    constructor(opts) { super(); this.opts = opts; this.remoteParticipants = new Map(); this.localParticipant = new LocalParticipant(this); this.canPlaybackAudio = true; this.startAudioCalls = 0; this.connected = false; ctl.rooms.push(this) }
    async connect(url, token, opts) { this.connectArgs = { url, token, opts }; if (ctl.failConnect) throw new Error('connect failed'); this.connected = true }
    async disconnect() { this.connected = false; this.emit(RoomEvent.Disconnected, 1) }
    async startAudio() { this.startAudioCalls++; this.canPlaybackAudio = true; this.emit(RoomEvent.AudioPlaybackStatusChanged, true) }
    // 测试辅助：远端参与者入场并发布音频
    addRemote(identity, name, role, { audio = true } = {}) {
      const p = new RemoteParticipant(identity, name, role)
      this.remoteParticipants.set(identity, p)
      this.emit(RoomEvent.ParticipantConnected, p)
      if (audio) {
        const room = this
        const pub = {
          kind: 'audio', source: 'microphone', trackSid: 'TR_a_' + identity, track: null, isMuted: false, subscribeCalls: [],
          setSubscribed(want) {
            this.subscribeCalls.push(want)
            if (want && !this.track) { this.track = fakeTrack('audio'); room.emit(RoomEvent.TrackSubscribed, this.track, this, p) }
            else if (!want && this.track) { const t = this.track; this.track = null; room.emit(RoomEvent.TrackUnsubscribed, t, this, p) }
          },
          setVideoQuality() {},
        }
        p.audioTrackPublications.set(pub.trackSid, pub)
        p.isMicrophoneEnabled = true
        this.emit(RoomEvent.TrackPublished, pub, p)
      }
      return p
    }
  }
  const LK = {
    Room, RoomEvent,
    Track: { Source: { Camera: 'camera', Microphone: 'microphone' } },
    VideoPresets: { h360: { resolution: { width: 640, height: 360 }, encoding: { maxBitrate: 450000, maxFramerate: 20 } }, h180: { resolution: { width: 320, height: 180 } } },
    AudioPresets: { speech: { maxBitrate: 24000 } },
    VideoQuality: { LOW: 0, MEDIUM: 1, HIGH: 2 },
    DisconnectReason: { CLIENT_INITIATED: 1, DUPLICATE_IDENTITY: 2, SERVER_SHUTDOWN: 3, PARTICIPANT_REMOVED: 4, ROOM_DELETED: 5, ROOM_CLOSED: 10 },
    isBrowserSupported: () => true,
  }
  return { LK, ctl }
}
