// 通知声音库：用 Web Audio API 程序合成 5 种短音效。
// 优势：零外部文件、零体积增加、浏览器兼容性 100%（Web Audio 是 W3C 标准）。

const SOUND_DEFS = {
  // ===== 短促音色（适合频繁消息）=====
  classic: {
    label: '经典',
    play: (ctx) => playTone(ctx, 880, 0.18, 'sine', 0.35)
  },
  chime: {
    label: '清脆',
    play: (ctx) => playSequence(ctx, [
      { freq: 1320, dur: 0.08 },
      { freq: 990, dur: 0.18 }
    ], 'sine', 0.35)
  },
  ding: {
    label: '叮咚',
    play: (ctx) => playSequence(ctx, [
      { freq: 587, dur: 0.1 },
      { freq: 784, dur: 0.18 }
    ], 'sine', 0.4)
  },
  soft: {
    label: '柔和',
    play: (ctx) => playTone(ctx, 523, 0.35, 'triangle', 0.25)
  },
  alert: {
    label: '提醒',
    play: (ctx) => playSequence(ctx, [
      { freq: 700, dur: 0.06 },
      { freq: 0, dur: 0.04 },
      { freq: 700, dur: 0.06 },
      { freq: 0, dur: 0.04 },
      { freq: 700, dur: 0.1 }
    ], 'sine', 0.3)
  },

  // ===== 响亮 / 长时音色（更明显的通知）=====
  bell: {
    label: '铃声 (长)',
    play: (ctx) => playLayered(ctx, [
      { freq: 1046.5, vol: 0.5,  dur: 1.2 },   // C6 主音
      { freq: 2093.0, vol: 0.18, dur: 0.85 }   // C7 二次谐波（金属感）
    ], 'sine')
  },
  doorbell: {
    label: '门铃 (长)',
    play: (ctx) => playSequence(ctx, [
      { freq: 659.25, dur: 0.25 },   // E5
      { freq: 523.25, dur: 0.6 }     // C5（拉长尾音）
    ], 'sine', 0.5)
  },
  trill: {
    label: '颤音 (急)',
    play: (ctx) => playSequence(ctx, [
      { freq: 880,  dur: 0.08 },
      { freq: 1100, dur: 0.08 },
      { freq: 880,  dur: 0.08 },
      { freq: 1100, dur: 0.08 },
      { freq: 880,  dur: 0.08 },
      { freq: 1100, dur: 0.08 },
      { freq: 880,  dur: 0.22 }
    ], 'sine', 0.45)
  },
  fanfare: {
    label: '号角',
    play: (ctx) => playSequence(ctx, [
      { freq: 523.25, dur: 0.12 },   // C5
      { freq: 659.25, dur: 0.12 },   // E5
      { freq: 783.99, dur: 0.12 },   // G5
      { freq: 1046.5, dur: 0.4 }     // C6 长延音
    ], 'sine', 0.5)
  },
  chord: {
    label: '和弦',
    play: (ctx) => playLayered(ctx, [
      { freq: 523.25, vol: 0.28, dur: 0.8 },   // C5
      { freq: 659.25, vol: 0.28, dur: 0.8 },   // E5
      { freq: 783.99, vol: 0.28, dur: 0.8 }    // G5
    ], 'sine')
  },

  none: {
    label: '静音',
    play: () => {}
  }
}

let sharedCtx = null
function getCtx() {
  if (sharedCtx && sharedCtx.state !== 'closed') return sharedCtx
  try {
    const C = window.AudioContext || window.webkitAudioContext
    if (!C) return null
    sharedCtx = new C()
    return sharedCtx
  } catch {
    return null
  }
}

// 浏览器要求首次声音必须由用户手势触发；先 resume 一次
export function unlockAudio() {
  const ctx = getCtx()
  if (ctx && ctx.state === 'suspended') {
    ctx.resume().catch(() => {})
  }
}

function playTone(ctx, freq, duration, type, peakVol) {
  if (!ctx) return
  const osc = ctx.createOscillator()
  const gain = ctx.createGain()
  osc.type = type
  osc.frequency.value = freq
  const t0 = ctx.currentTime
  gain.gain.setValueAtTime(0, t0)
  gain.gain.linearRampToValueAtTime(peakVol, t0 + 0.01)
  gain.gain.exponentialRampToValueAtTime(0.0001, t0 + duration)
  osc.connect(gain).connect(ctx.destination)
  osc.start(t0)
  osc.stop(t0 + duration + 0.02)
}

function playSequence(ctx, notes, type, peakVol) {
  if (!ctx) return
  let t = ctx.currentTime
  notes.forEach(({ freq, dur }) => {
    if (freq <= 0) {
      t += dur
      return
    }
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.type = type
    osc.frequency.value = freq
    gain.gain.setValueAtTime(0, t)
    gain.gain.linearRampToValueAtTime(peakVol, t + 0.01)
    gain.gain.exponentialRampToValueAtTime(0.0001, t + dur)
    osc.connect(gain).connect(ctx.destination)
    osc.start(t)
    osc.stop(t + dur + 0.02)
    t += dur
  })
}

// 多层叠加（和弦 / 谐波）：所有声音同时开始，各自有独立时长和音量
function playLayered(ctx, layers, type) {
  if (!ctx) return
  const t0 = ctx.currentTime
  layers.forEach(({ freq, vol, dur }) => {
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.type = type
    osc.frequency.value = freq
    gain.gain.setValueAtTime(0, t0)
    gain.gain.linearRampToValueAtTime(vol, t0 + 0.01)
    gain.gain.exponentialRampToValueAtTime(0.0001, t0 + dur)
    osc.connect(gain).connect(ctx.destination)
    osc.start(t0)
    osc.stop(t0 + dur + 0.02)
  })
}

// 防抖：500ms 内连续触发同种声音只播一次（避免连发消息时声音叠成噪声）
const lastPlayed = {}
const MIN_GAP = 500

export function playSound(name) {
  if (!name || name === 'none') return
  const def = SOUND_DEFS[name]
  if (!def) return
  const now = Date.now()
  if (lastPlayed[name] && now - lastPlayed[name] < MIN_GAP) return
  lastPlayed[name] = now
  const ctx = getCtx()
  if (!ctx) return
  // 用户没解锁时 state 是 suspended；resume 后立即播放
  if (ctx.state === 'suspended') ctx.resume().catch(() => {})
  try {
    def.play(ctx)
  } catch (e) {
    // 静默失败，不影响业务
  }
}

export function listSounds() {
  return Object.entries(SOUND_DEFS).map(([k, v]) => ({ value: k, label: v.label }))
}
