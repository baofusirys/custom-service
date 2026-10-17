// 视频区选择：最多显示 4 路（含自己的预览），远端一律低清订阅（纯函数，可在 node 测试中直接运行）。
// 「粘性」选择：已显示的画面只要还在发布就保持不变，避免有人说话时画面来回跳。
export const HALL_MAX_VIDEO_TILES = 4

function priority(c) {
  // 数值越小越优先：客服 > 正在说话 > 先开摄像头
  return (c.role === 'agent' ? 0 : 2) + (c.speaking ? 0 : 1)
}

export function selectVideoTiles(candidates, { max = HALL_MAX_VIDEO_TILES, previous = [] } = {}) {
  const limit = Math.max(0, Math.floor(max))
  const list = (Array.isArray(candidates) ? candidates : []).filter(c => c && typeof c.identity === 'string' && c.identity)
  const byId = new Map(list.map(c => [c.identity, c]))
  const chosen = []
  const take = id => {
    if (chosen.length < limit && byId.has(id) && !chosen.includes(id)) chosen.push(id)
  }
  // 1. 自己的本地预览始终第一位
  for (const c of list) if (c.isLocal) take(c.identity)
  // 2. 之前已显示且仍在发布的保持原顺序
  for (const id of Array.isArray(previous) ? previous : []) take(id)
  // 3. 剩余位置按优先级补齐
  const rest = list
    .filter(c => !chosen.includes(c.identity))
    .sort((a, b) => {
      const p = priority(a) - priority(b)
      if (p) return p
      const ta = Number(a.publishedAt) || Number.MAX_SAFE_INTEGER
      const tb = Number(b.publishedAt) || Number.MAX_SAFE_INTEGER
      if (ta !== tb) return ta - tb
      return a.identity < b.identity ? -1 : 1
    })
  for (const c of rest) take(c.identity)
  return chosen
}

// 订阅差异：返回需要新订阅与需要取消订阅的远端身份
export function diffSubscriptions(currentRemote, nextSelected, localIdentity = '') {
  const cur = new Set(Array.isArray(currentRemote) ? currentRemote : [])
  const next = new Set((Array.isArray(nextSelected) ? nextSelected : []).filter(id => id !== localIdentity))
  return {
    subscribe: [...next].filter(id => !cur.has(id)),
    unsubscribe: [...cur].filter(id => !next.has(id)),
  }
}
