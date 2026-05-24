// 通知声音库：使用真实录制的 WAV 文件（替代之前的 Web Audio 程序合成）。
// 文件存在 admin/public/sounds/，Vite build 时会复制到 dist 根目录；
// nginx 反代 /admin/ → admin 容器后，浏览器访问 https://<domain>/admin/sounds/*.wav

// import.meta.env.BASE_URL 是 Vite 注入的部署基础路径
//   - dev:  '/'
//   - prod: '/admin/'  (因为 vite.config.js 里 base='/admin/')
const SOUNDS_BASE = (import.meta.env?.BASE_URL || '/').replace(/\/$/, '') + '/sounds/'

const SOUND_DEFS = {
  agent1: { label: '工作台音色 1', file: 'agent1.wav' },
  agent2: { label: '工作台音色 2', file: 'agent2.wav' },
  agent3: { label: '工作台音色 3', file: 'agent3.wav' },
  visitor1: { label: '访客端音色 1', file: 'visitor1.wav' },
  visitor2: { label: '访客端音色 2', file: 'visitor2.wav' },
  visitor3: { label: '访客端音色 3', file: 'visitor3.wav' },
  none: { label: '静音', file: null }
}

// 预加载所有音频元素：第一次试听零延迟
const cache = {}
for (const [k, v] of Object.entries(SOUND_DEFS)) {
  if (v.file) {
    const a = new Audio(SOUNDS_BASE + v.file)
    a.preload = 'auto'
    a.volume = 1.0
    cache[k] = a
  }
}

// 浏览器 autoplay policy：第一次手势后才能放声。在用户点击聊天界面时调一次。
export function unlockAudio() {
  for (const a of Object.values(cache)) {
    try {
      a.volume = 0
      const p = a.play()
      if (p && p.then) {
        p.then(() => { a.pause(); a.currentTime = 0; a.volume = 1.0 })
         .catch(() => { a.volume = 1.0 })
      } else {
        a.volume = 1.0
      }
    } catch { a.volume = 1.0 }
  }
}

// 500ms 内连续触发同种声音只播一次（防止连发消息时声音叠成噪声）
const lastPlayed = {}
const MIN_GAP = 500

export function playSound(name) {
  if (!name || name === 'none') return
  const def = SOUND_DEFS[name]
  if (!def || !def.file) return
  const now = Date.now()
  if (lastPlayed[name] && now - lastPlayed[name] < MIN_GAP) return
  lastPlayed[name] = now
  const a = cache[name]
  if (!a) return
  try {
    a.currentTime = 0
    a.volume = 1.0
    const p = a.play()
    if (p && p.catch) p.catch(() => {})
  } catch { /* 静默失败，不影响业务 */ }
}

export function listSounds() {
  return Object.entries(SOUND_DEFS).map(([k, v]) => ({ value: k, label: v.label }))
}
